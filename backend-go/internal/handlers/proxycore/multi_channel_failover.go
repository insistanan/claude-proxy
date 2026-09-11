package proxycore

import (
	"fmt"
	"log"

	"github.com/BenedictKing/api-proxy/internal/config"
	"github.com/BenedictKing/api-proxy/internal/scheduler"
	"github.com/BenedictKing/api-proxy/internal/types"
	"github.com/gin-gonic/gin"
)

// MultiChannelAttemptResult 描述一次“选中渠道”的尝试结果（用于多渠道 failover 外壳复用）。
type MultiChannelAttemptResult struct {
	Handled           bool
	Attempted         bool
	SuccessKey        string
	SuccessBaseURLIdx int
	FailoverError     *FailoverError
	Usage             *types.Usage
	LastError         error
}

// TrySelectedChannelFunc 尝试一次选中的渠道，返回该渠道的尝试结果。
type TrySelectedChannelFunc func(selection *scheduler.SelectionResult) MultiChannelAttemptResult

// OnMultiChannelHandledFunc 在请求被“处理完成”时回调（成功或非 failover 错误都会触发）。
type OnMultiChannelHandledFunc func(selection *scheduler.SelectionResult, result MultiChannelAttemptResult)

// HandleAllFailedFunc 处理“所有渠道都失败”的返回逻辑（不同入口可能有不同错误格式）。
type HandleAllFailedFunc func(c *gin.Context, failoverErr *FailoverError, lastError error)

// HandleMultiChannelFailover 处理多渠道 failover 外壳逻辑（选渠道 + 聚合错误 + Trace 亲和）。
// 具体“渠道内 Key/BaseURL 轮转”由 trySelectedChannel 实现（通常调用 UpstreamAttempt.TryWithModelMappingFailover）。
//
// 选渠成功后会占用 in-flight 预留：
// - 若该渠道最终 Handled（成功/客户端取消等已写回响应），请求结束后释放预留
// - Fuzzy 模式下若该渠道失败并继续 failover，立即释放预留再选下一个
// 这样并发新对话在选渠瞬间就能看到彼此的占用，避免都打到同一供应商。
func HandleMultiChannelFailover(
	c *gin.Context,
	envCfg *config.EnvConfig,
	channelScheduler *scheduler.ChannelScheduler,
	kind scheduler.ChannelKind,
	apiType string,
	conversationID string,
	requestedModel string,
	fuzzyMode bool,
	trySelectedChannel TrySelectedChannelFunc,
	onHandled OnMultiChannelHandledFunc,
	handleAllFailed HandleAllFailedFunc,
) {
	if c == nil || envCfg == nil || channelScheduler == nil || trySelectedChannel == nil {
		return
	}
	if handleAllFailed == nil {
		handleAllFailed = func(c *gin.Context, failoverErr *FailoverError, lastError error) {
			HandleAllChannelsFailed(c, false, failoverErr, lastError, apiType)
		}
	}

	failedChannels := make(map[int]bool)
	var lastError error
	var lastFailoverError *FailoverError

	maxChannelAttempts := channelScheduler.GetActiveChannelCountForModel(kind, requestedModel)
	// 渠道故障转移始终按配置顺序执行。Fuzzy 模式只影响错误分类和最终错误
	// 格式，不应再决定是否尝试下一个渠道，否则普通模式下第一个渠道故障
	// 会直接把请求结束掉。

	for channelAttempt := 0; channelAttempt < maxChannelAttempts; channelAttempt++ {
		// 检查客户端是否已断开连接
		select {
		case <-c.Request.Context().Done():
			if envCfg.ShouldLog("info") {
				log.Printf("[%s-Cancel] 请求已取消，停止渠道 failover", apiType)
			}
			return
		default:
			// 继续正常流程
		}

		selection, err := channelScheduler.SelectChannel(c.Request.Context(), conversationID, failedChannels, kind, requestedModel)
		if err != nil {
			lastError = err
			break
		}

		upstream := selection.Upstream
		channelIndex := selection.ChannelIndex
		releaseReservation := func() {
			if selection != nil && selection.Reserved {
				channelScheduler.ReleaseChannelReservation(selection.Kind, selection.ChannelIndex)
				selection.Reserved = false
			}
		}

		if envCfg.ShouldLog("info") && upstream != nil {
			log.Printf("[%s-Select] 选择渠道: [%d] %s (原因: %s, 尝试 %d/%d, inFlight: %d)",
				apiType, channelIndex, upstream.Name, selection.Reason, channelAttempt+1, maxChannelAttempts,
				channelScheduler.GetChannelInFlight(kind, channelIndex))
		}

		result := trySelectedChannel(selection)
		if result.Handled {
			// 请求已完成（成功或已写回非 failover 错误），释放选渠预留。
			// 注意：上游实际 ActiveRequests 仍由 ProfileManager 独立维护。
			releaseReservation()
			// 渠道级熔断记账（internal/circuit）：真正成功记成功；
			// 已写回但非成功（内容安全拦截等 Handled-with-error 场景）按 Neutral 处理，
			// 由 SuccessKey 区分。客户端取消（LastError 是 context.Canceled）不计健康度。
			if result.SuccessKey != "" {
				channelScheduler.RecordCircuitSuccess(kind, channelIndex)
			} else if isClientSideError(result.LastError) {
				channelScheduler.RecordCircuitNeutral(kind, channelIndex)
			}
			if onHandled != nil {
				onHandled(selection, result)
			}
			// 只有真正成功的请求才设置 Trace 亲和（客户端取消时 SuccessKey 为空）
			if result.SuccessKey != "" {
				// 对话级亲和已由 onHandled 回调（protocol.go 的 MarkConversationSuccess）写入，
				// 这里只维护会话级 Trace 亲和。
				// 仅在以下情况设置会话级亲和：
				// 1. 该会话没有亲和记录（新会话）
				// 2. 当前选择的原因是 trace_affinity（续期现有亲和）
				// 3. 当前选择的原因不是 trace_affinity，但该会话原来的亲和渠道在本次已失败（failover后建立新亲和）
				shouldSetAffinity := false
				affinityMgr := channelScheduler.GetTraceAffinityManager()
				if affinityMgr == nil {
					shouldSetAffinity = true
				} else {
					if _, hasAffinity := affinityMgr.GetPreferredChannelForKind(string(kind), conversationID); !hasAffinity {
						// 情况1：新会话，建立亲和
						shouldSetAffinity = true
					} else if selection.Reason == "trace_affinity" {
						// 情况2：使用了亲和渠道成功，续期
						shouldSetAffinity = true
					} else {
						// 情况3：检查原亲和渠道是否在本次失败
						if oldChannelIdx, ok := affinityMgr.GetPreferredChannelForKind(string(kind), conversationID); ok {
							if failedChannels[oldChannelIdx] {
								// 原亲和渠道失败，允许建立新亲和
								shouldSetAffinity = true
							}
						}
					}
				}

				if shouldSetAffinity {
					channelScheduler.SetTraceAffinityForKind(kind, conversationID, channelIndex)
				}
				channelScheduler.ConsumePromotionCount(channelIndex, kind)
			}
			return
		}

		// 当前渠道失败，释放预留后再尝试下一渠道，避免”失败渠道”继续占负载。
		releaseReservation()
		failedChannels[channelIndex] = true
		// 渠道级熔断记账：渠道整体尝试失败（可重试错误）记失败；
		// 客户端取消不计入健康度。
		if result.Attempted {
			if isClientSideError(result.LastError) {
				channelScheduler.RecordCircuitNeutral(kind, channelIndex)
			} else {
				channelScheduler.RecordCircuitFailure(kind, channelIndex)
			}
		}

		if result.FailoverError != nil {
			lastFailoverError = result.FailoverError
			if upstream != nil {
				lastError = fmt.Errorf("渠道 [%d] %s 失败", channelIndex, upstream.Name)
			} else {
				lastError = fmt.Errorf("渠道 [%d] 失败", channelIndex)
			}
		}

		if result.Attempted && upstream != nil {
			log.Printf("[%s-Failover] 警告: 渠道 [%d] %s 所有密钥都失败，尝试下一个渠道", apiType, channelIndex, upstream.Name)
		}
	}

	log.Printf("[%s-Error] 所有渠道都失败了", apiType)
	handleAllFailed(c, lastFailoverError, lastError)
}
