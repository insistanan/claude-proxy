// 本文件是单渠道内"Key/BaseURL 层"故障转移的主循环：按调用方给定的 URLResults 顺序
// （对话粘性会把偏好 BaseURL 提到最前）遍历 BaseURL，每个 BaseURL 内轮换 Key，处理熔断跳过、
// 全 Key 熔断时的强制探测、同候选重试、请求准备（内容安全 + 视觉层）失败与成功响应写回。
// 上层的模型映射循环在 upstream_failover.go，失败该怎么办的分类在 upstream_attempt_error.go。
package common

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/BenedictKing/claude-proxy/internal/visionlayer"
	"github.com/gin-gonic/gin"
)

type upstreamAttemptPreflight struct {
	proxyURL string
}

func (a UpstreamAttempt) prepareAllKeys() (upstreamAttemptPreflight, bool, error) {
	if a.Upstream == nil || len(a.Upstream.APIKeys) == 0 {
		return upstreamAttemptPreflight{}, false, nil
	}
	if a.MetricsManager == nil {
		return upstreamAttemptPreflight{}, false, nil
	}
	if a.NextAPIKey == nil || a.BuildRequest == nil || a.HandleSuccess == nil {
		return upstreamAttemptPreflight{}, false, nil
	}
	if len(a.URLResults) == 0 {
		return upstreamAttemptPreflight{}, false, nil
	}

	proxyURL, err := a.ConfigManager.ResolveUpstreamProxyURL(a.Upstream)
	if err != nil {
		return upstreamAttemptPreflight{}, false, err
	}
	return upstreamAttemptPreflight{proxyURL: proxyURL}, true, nil
}

func (a UpstreamAttempt) tryWithAllKeys() UpstreamAttemptResult {
	c := a.Context
	envCfg := a.EnvConfig
	cfgManager := a.ConfigManager
	channelScheduler := a.ChannelScheduler
	kind := a.Kind
	apiType := a.APIType
	metricsManager := a.MetricsManager
	upstream := a.Upstream
	urlResults := a.URLResults
	requestBody := a.RequestBody
	isStream := a.IsStream
	logCtx := a.LogContext

	var usage *types.Usage

	preflight, valid, err := a.prepareAllKeys()
	if err != nil {
		return newUpstreamAttemptResult(false, "", 0, nil, nil, err)
	}
	if !valid {
		return newUpstreamAttemptResult(false, "", 0, nil, nil, nil)
	}
	proxyURL := preflight.proxyURL

	urlResults = preferConversationBaseURL(channelScheduler, envCfg, apiType, logCtx, urlResults)

	failoverState := newUpstreamFailoverState()
	requestLogID := nextAttemptLogID("req")
	capabilities := newUpstreamCapabilityState(upstream)

	// 强制探测模式：基于本次优先尝试的 BaseURL 判断（避免 BaseURL/BaseURLs 不一致导致误判）
	forceProbeMode := AreAllKeysSuspended(metricsManager, urlResults[0].URL, upstream.APIKeys, logCtx.ChannelIndex)
	if forceProbeMode {
		log.Printf("[%s-ForceProbe] 渠道 %s 所有 Key 都被熔断，启用强制探测模式", apiType, upstream.Name)
	}

	for urlIdx, urlResult := range urlResults {
		currentBaseURL := urlResult.URL
		originalIdx := urlResult.OriginalIdx // 原始索引用于指标记录
		retryState := newCandidateRetryState(len(upstream.APIKeys))

		for attempt := 0; attempt < retryState.maxRetries; attempt++ {
			RestoreRequestBody(c, requestBody)
			ResetRequestLogFirstToken(c)
			attemptStart := time.Now()

			apiKey, err := a.NextAPIKey(upstream, retryState.failedKeys)
			if err != nil {
				failoverState.lastError = err
				break // 当前 BaseURL 没有可用 Key，尝试下一个 BaseURL
			}

			// 检查熔断状态
			if !forceProbeMode && metricsManager.ShouldSuspendKey(currentBaseURL, apiKey, logCtx.ChannelIndex) {
				retryState.markKeyFailed(apiKey)
				log.Printf("[%s-Circuit] 跳过熔断中的 Key: %s", apiType, utils.MaskAPIKey(apiKey))
				continue
			}

			if envCfg.ShouldLog("info") {
				log.Printf("[%s-Key] 使用API密钥: %s (BaseURL %d/%d, 尝试 %d/%d)",
					apiType, utils.MaskAPIKey(apiKey), urlIdx+1, len(urlResults), attempt+1, retryState.maxRetries)
			}

			// 使用深拷贝避免并发修改问题
			upstreamCopy := a.prepareCandidateUpstream(currentBaseURL, capabilities)

			req, prepareStage, err := a.buildPreparedAttemptRequest(upstreamCopy, apiKey)
			if err != nil && prepareStage == "build_request" {
				failoverState.lastError = err
				retryState.markKeyFailed(apiKey)
				channelScheduler.RecordFailure(currentBaseURL, apiKey, logCtx.ChannelIndex, kind)
				recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", 0, false, attemptStart, "build_request", err.Error(), true, isStream, nil)
				continue
			}
			if err != nil {
				_ = req.Body.Close()
				if prepareStage == "content_safety" {
					status := handleContentSafetyPreparationError(c, apiType, err)
					attemptStatus := "failed"
					if status == http.StatusForbidden {
						attemptStatus = "blocked"
					}
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, attemptStatus, status, false, attemptStart, prepareStage, err.Error(), false, isStream, nil)
					return newUpstreamAttemptResult(true, "", 0, nil, nil, err)
				}
				status, code := visionlayer.ErrorResponse(err)
				payload := gin.H{
					"error": err.Error(),
					"code":  code,
				}
				recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", status, false, attemptStart, "vision_layer", err.Error(), true, isStream, nil)
				if channelScheduler.GetActiveChannelCountForModel(kind, logCtx.Model) > 1 {
					body, _ := json.Marshal(payload)
					return newUpstreamAttemptResult(false, "", 0, &FailoverError{Status: status, Body: body}, nil, err)
				}
				c.JSON(status, payload)
				return newUpstreamAttemptResult(true, "", 0, nil, nil, err)
			}
			// 请求准备成功后再启动请求画像和活跃度观测。
			lifecycle := a.startAttemptLifecycle(currentBaseURL, apiKey)
			finishRetryContentSafety := func(preparationErr error) {
				status := handleContentSafetyPreparationError(c, apiType, preparationErr)
				lifecycle.finalizeNeutral()
				attemptStatus := "failed"
				if status == http.StatusForbidden {
					attemptStatus = "blocked"
				}
				recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, attemptStatus, status, false, attemptStart, "content_safety", preparationErr.Error(), false, isStream, nil)
			}

			resp, err := SendRequest(req, upstream, envCfg, isStream, apiType, proxyURL)
			if err != nil {
				failoverState.lastError = err
				// 区分客户端取消和真实渠道故障（统一口径）
				if isClientSideError(err) {
					// 客户端取消：不计入失败，不触发 failover
					lifecycle.finalizeClientCancelled()
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "cancelled", 0, false, attemptStart, "client_cancelled", err.Error(), false, isStream, nil)
					log.Printf("[%s-Cancel] 请求已取消（SendRequest 阶段）", apiType)
					return newUpstreamAttemptResult(true, "", 0, nil, nil, err)
				}
				// 真实渠道故障：计入失败，继续 failover
				retryState.markKeyFailed(apiKey)
				cfgManager.MarkKeyAsFailed(apiKey, apiType)
				lifecycle.finalizeFailed()
				if a.MarkURLFailure != nil {
					a.MarkURLFailure(currentBaseURL)
				}
				recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", 0, false, attemptStart, "network", err.Error(), true, isStream, nil)
				log.Printf("[%s-Key] 警告: API密钥失败: %v", apiType, err)
				continue
			}

			// 收到响应头，性能画像记录首字节时间
			if pm := channelScheduler.GetProfileManager(); pm != nil {
				pm.RecordFirstByte(lifecycle.observation.ProfileRequestID)
			}

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				respBodyBytes, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				if readErr != nil {
					// 响应体读取中断：连接已不可靠，按渠道网络故障处理，
					// 不把截断 body 用于错误分类或回给客户端。
					failoverState.lastError = fmt.Errorf("读取上游错误响应体失败: %w", readErr)
					retryState.markKeyFailed(apiKey)
					cfgManager.MarkKeyAsFailed(apiKey, apiType)
					lifecycle.finalizeFailed()
					if a.MarkURLFailure != nil {
						a.MarkURLFailure(currentBaseURL)
					}
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, "read_body", readErr.Error(), true, isStream, nil)
					log.Printf("[%s-Key] 警告: 读取上游错误响应体失败 (状态: %d)，尝试下一个密钥: %v", apiType, resp.StatusCode, readErr)
					continue
				}
				respBodyBytes = utils.DecompressGzipIfNeeded(resp, respBodyBytes)
				retrySucceeded := false

				if IsUpstreamModelCapacityError(respBodyBytes) {
					lifecycle.finalizeNeutral()

					failoverState.lastError = fmt.Errorf("上游模型暂时满载")
					failoverState.lastFailoverError = &FailoverError{Status: resp.StatusCode, Body: respBodyBytes}
					candidate := currentBaseURL + "\x00" + apiKey
					canRetry := retryState.canRetryCandidate(candidate, c.Request.Context())
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, "model_capacity", string(respBodyBytes), canRetry, isStream, nil)
					if canRetry {
						retryCount := retryState.recordCandidateRetry(candidate)
						log.Printf("[%s-Capacity] 模型暂时满载，使用同一 BaseURL 和 Key 重试 (%d/%d)", apiType, retryCount, retryState.maxSameCandidateRetries)
						continue
					}
					retryState.markKeyFailed(apiKey)
					continue
				}

				// 兼容部分严格的 OpenAI 协议网关：首次明确拒绝 prompt_cache_key 时，
				// 使用同一渠道、BaseURL 和 API key 移除该字段后重试一次，并记住能力。
				if !upstreamCopy.DisablePromptCacheKey && IsPromptCacheKeyUnsupported(resp.StatusCode, respBodyBytes) {
					log.Printf("[%s-ChannelCapability] 渠道 %s 不支持 prompt_cache_key，移除后使用同一 key 重试一次", apiType, upstream.Name)
					capabilities.disablePromptCacheKey = true
					upstreamCopy.DisablePromptCacheKey = true
					if err := cfgManager.MarkPromptCacheKeyUnsupported(string(kind), upstream.ID); err != nil {
						log.Printf("[%s-ChannelCapability] 持久化 prompt_cache_key 能力失败: %v", apiType, err)
					}

					RestoreRequestBody(c, requestBody)
					retryResult := a.retrySameCandidateRequest(upstreamCopy, apiKey, proxyURL)
					if retryResult.Err != nil {
						if retryResult.PreparationStage == "content_safety" {
							finishRetryContentSafety(retryResult.Err)
							return newUpstreamAttemptResult(true, "", 0, nil, nil, retryResult.Err)
						}
						if retryResult.PreparationStage == "build_request" {
							log.Printf("[%s-ChannelCapability] 重建无 prompt_cache_key 请求失败: %v", apiType, retryResult.Err)
						} else if retryResult.PreparationStage == "send_request" {
							log.Printf("[%s-ChannelCapability] 无 prompt_cache_key 重试失败: %v", apiType, retryResult.Err)
						} else {
							log.Printf("[%s-ChannelCapability] 准备无 prompt_cache_key 请求失败: %v", apiType, retryResult.Err)
						}
					} else {
						resp = retryResult.Response
						if retryResult.Succeeded {
							retrySucceeded = true
						} else {
							respBodyBytes = retryResult.Body
						}
					}
				}

				// 部分 DeepSeek 兼容渠道会在 thinking 模式下要求每条 assistant
				// 历史都带 reasoning_content。Cursor 等客户端可能已将该字段
				// 隐藏；首次收到明确 400 后，用同一渠道立即强制补齐并重试，
				// 同时持久化能力，避免之后的每轮请求都先失败一次。
				if !retrySucceeded && kind == scheduler.ChannelKindMessages && upstreamCopy.ServiceType == "openai" && !upstreamCopy.RequireReasoningContent &&
					IsReasoningContentRequired(resp.StatusCode, respBodyBytes) {
					log.Printf("[%s-ChannelCapability] 渠道 %s 要求完整 reasoning_content，补齐后使用同一 key 重试一次", apiType, upstream.Name)
					capabilities.requireReasoningContent = true
					upstreamCopy.RequireReasoningContent = true
					if err := cfgManager.MarkReasoningContentRequired(string(kind), upstream.ID); err != nil {
						log.Printf("[%s-ChannelCapability] 持久化 reasoning_content 能力失败: %v", apiType, err)
					}

					RestoreRequestBody(c, requestBody)
					retryResult := a.retrySameCandidateRequest(upstreamCopy, apiKey, proxyURL)
					if retryResult.Err != nil {
						if retryResult.PreparationStage == "content_safety" {
							finishRetryContentSafety(retryResult.Err)
							return newUpstreamAttemptResult(true, "", 0, nil, nil, retryResult.Err)
						}
						if retryResult.PreparationStage == "build_request" {
							log.Printf("[%s-ChannelCapability] 重建 reasoning_content 兼容请求失败: %v", apiType, retryResult.Err)
						} else if retryResult.PreparationStage == "send_request" {
							log.Printf("[%s-ChannelCapability] reasoning_content 兼容重试失败: %v", apiType, retryResult.Err)
						} else {
							log.Printf("[%s-ChannelCapability] 准备 reasoning_content 兼容请求失败: %v", apiType, retryResult.Err)
						}
					} else {
						resp = retryResult.Response
						if retryResult.Succeeded {
							retrySucceeded = true
						} else {
							respBodyBytes = retryResult.Body
						}
					}
				}

				// 某些 Responses 中转会扫描原始 JSON 字节，中文业务文本或历史中的审核错误码
				// 可能被稳定误判。保持 JSON 解码值不变，改用 Unicode 转义后重试一次。
				if !retrySucceeded && kind == scheduler.ChannelKindResponses && upstreamCopy.ServiceType == "responses" && utils.IsContentPolicyErrorBody(respBodyBytes) {
					escapedBody, changed, escapeErr := buildContentPolicyCompatibilityBody(requestBody)
					if escapeErr != nil {
						log.Printf("[%s-ContentPolicy] 构建等价 JSON 转义请求失败: %v", apiType, escapeErr)
					} else if changed {
						log.Printf("[%s-ContentPolicy] 使用等价 JSON Unicode 转义在同一渠道重试一次", apiType)
						RestoreRequestBody(c, escapedBody)
						retryResult := a.retrySameCandidateRequest(upstreamCopy, apiKey, proxyURL)
						if retryResult.Err != nil {
							if retryResult.PreparationStage == "content_safety" {
								finishRetryContentSafety(retryResult.Err)
								return newUpstreamAttemptResult(true, "", 0, nil, nil, retryResult.Err)
							}
							if retryResult.PreparationStage == "build_request" {
								log.Printf("[%s-ContentPolicy] 重建转义请求失败: %v", apiType, retryResult.Err)
							} else if retryResult.PreparationStage == "send_request" {
								log.Printf("[%s-ContentPolicy] 转义请求重试失败: %v", apiType, retryResult.Err)
							} else {
								log.Printf("[%s-ContentPolicy] 准备转义请求失败: %v", apiType, retryResult.Err)
							}
						} else {
							resp = retryResult.Response
							if retryResult.Succeeded {
								retrySucceeded = true
								log.Printf("[%s-ContentPolicy] 等价 JSON 转义重试成功", apiType)
							} else {
								respBodyBytes = retryResult.Body
							}
						}
						RestoreRequestBody(c, requestBody)
					}
				}

				if !retrySucceeded {
					decision := a.decideUpstreamFailure(resp.StatusCode, respBodyBytes)
					switch decision.action {
					case upstreamFailureTryNextChannel:
						// 内容审核与当前请求正文、上游策略相关。不要换 Key，也不要计入渠道故障；
						// 多渠道可用时把原始错误交给外层继续选渠。
						lifecycle.finalizeNeutral()

						failoverState.lastError = fmt.Errorf("上游内容审核拒绝请求")
						failoverState.lastFailoverError = &FailoverError{Status: resp.StatusCode, Body: respBodyBytes}
						recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, decision.errorType, string(respBodyBytes), true, isStream, nil)
						log.Printf("[%s-ContentPolicy] 渠道 %s 拒绝请求，跳过当前渠道", apiType, upstream.Name)
						return newUpstreamAttemptResult(false, "", 0, failoverState.lastFailoverError, nil, failoverState.lastError)

					case upstreamFailureTryNextKey:
						failoverState.lastError = fmt.Errorf("上游错误: %d", resp.StatusCode)
						retryState.markKeyFailed(apiKey)
						cfgManager.MarkKeyAsFailed(apiKey, apiType)
						lifecycle.finalizeFailed()
						if a.MarkURLFailure != nil {
							a.MarkURLFailure(currentBaseURL)
						}
						log.Printf("[%s-Key] 警告: API密钥失败 (状态: %d)，尝试下一个密钥", apiType, resp.StatusCode)

						failoverState.lastFailoverError = &FailoverError{
							Status: resp.StatusCode,
							Body:   respBodyBytes,
						}

						if decision.quotaRelated {
							failoverState.deprioritizeCandidates[apiKey] = true
						}
						recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, decision.errorType, string(respBodyBytes), true, isStream, nil)
						continue

					case upstreamFailureRespond:
						// 非 failover 错误，记录失败指标后返回（请求已处理）
						lifecycle.finalizeFailed()
						recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, decision.errorType, string(respBodyBytes), false, isStream, nil)
						c.Data(resp.StatusCode, "application/json", respBodyBytes)
						return newUpstreamAttemptResult(true, "", 0, nil, nil, nil)
					}
				}
			}

			// 成功响应：处理 quota key 降级
			if a.DeprioritizeKey != nil && len(failoverState.deprioritizeCandidates) > 0 {
				for key := range failoverState.deprioritizeCandidates {
					a.DeprioritizeKey(key)
				}
			}

			usage, err = a.HandleSuccess(c, resp, upstreamCopy, apiKey)
			if err != nil {
				failoverState.lastError = err
				decision := classifyResponseProcessingError(err, c.Writer.Written())
				switch decision.action {
				case responseProcessingClientCancelled:
					// 客户端取消/断开：计入总请求数但不计入失败
					lifecycle.finalizeClientCancelled()
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "cancelled", resp.StatusCode, false, attemptStart, "client_cancelled", err.Error(), false, isStream, usage)
					log.Printf("[%s-Cancel] 请求已取消，停止渠道 failover", apiType)

				case responseProcessingContentSafety:
					lifecycle.finalizeNeutral()
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "blocked", http.StatusForbidden, false, attemptStart, "content_safety", decision.safetyErr.Error(), false, isStream, usage)
					if c.Writer.Written() {
						if writeErr := WriteAttachedStreamError(c, decision.safetyErr); writeErr != nil {
							log.Printf("[%s-ContentSafety] 流错误写出失败: %v", apiType, writeErr)
						}
					} else {
						if writeErr := WriteAttachedContentSafetyError(c, decision.safetyErr); writeErr != nil {
							log.Printf("[%s-ContentSafety] 协议错误写出失败: %v", apiType, writeErr)
							c.JSON(http.StatusInternalServerError, gin.H{"error": writeErr.Error(), "code": "CONTENT_SAFETY_RESPONSE_ERROR"})
						}
					}

				case responseProcessingContentSafetyHook:
					lifecycle.finalizeNeutral()
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", http.StatusInternalServerError, false, attemptStart, "content_safety_hook", decision.hookErr.Error(), false, isStream, usage)
					log.Printf("[%s-ContentSafety] 本地响应检查失败: %v", apiType, decision.hookErr)
					if c.Writer.Written() {
						if writeErr := WriteAttachedStreamHookError(c, decision.hookErr); writeErr != nil {
							log.Printf("[%s-ContentSafety] 流内部错误写出失败: %v", apiType, writeErr)
						}
					} else {
						c.JSON(http.StatusInternalServerError, gin.H{
							"error": "内容安全检查失败",
							"code":  "CONTENT_SAFETY_HOOK_ERROR",
						})
					}

				case responseProcessingRetryCandidate:
					lifecycle.finalizeNeutral()

					candidate := currentBaseURL + "\x00" + apiKey
					canRetry := retryState.canRetryCandidate(candidate, c.Request.Context())
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, "model_capacity", err.Error(), canRetry, isStream, usage)
					if canRetry {
						retryCount := retryState.recordCandidateRetry(candidate)
						log.Printf("[%s-Capacity] 上游未产出内容，使用同一 BaseURL 和 Key 重试 (%d/%d)", apiType, retryCount, retryState.maxSameCandidateRetries)
						continue
					}

					// 仅在本次请求内排除该 Key，继续其他候选；不写入 Key 熔断和失败率。
					retryState.markKeyFailed(apiKey)
					failoverState.lastFailoverError = &FailoverError{Status: http.StatusServiceUnavailable, Body: []byte(err.Error())}
					continue

				case responseProcessingChannelFailure:
					// 真实渠道故障：计入失败指标
					cfgManager.MarkKeyAsFailed(apiKey, apiType)
					lifecycle.finalizeFailed()
					shouldRetryResponseProcessing := !c.Writer.Written()
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, "response_processing", err.Error(), shouldRetryResponseProcessing, isStream, usage)
					log.Printf("[%s-Key] 警告: 响应处理失败: %v", apiType, err)
					if shouldRetryResponseProcessing {
						retryState.markKeyFailed(apiKey)
						if a.MarkURLFailure != nil {
							a.MarkURLFailure(currentBaseURL)
						}
						failoverState.lastFailoverError = &FailoverError{
							Status: resp.StatusCode,
							Body:   []byte(err.Error()),
						}
						continue
					}
				}
				return newUpstreamAttemptResult(true, "", 0, nil, usage, err)
			}

			if a.MarkURLSuccess != nil {
				a.MarkURLSuccess(currentBaseURL)
			}

			lifecycle.finalizeSuccessful(usage)
			recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "completed", resp.StatusCode, true, attemptStart, "", "", false, isStream, usage)
			// 记录会话 BaseURL 粘滞，后续请求优先同 URL 以利 prompt cache
			if channelScheduler != nil && logCtx.ConversationID != "" {
				channelScheduler.SetPreferredBaseURL(logCtx.ConversationID, currentBaseURL)
			}
			return newUpstreamAttemptResult(true, apiKey, originalIdx, nil, usage, nil)
		}

		// 当前 BaseURL 的所有 Key 都失败，记录并尝试下一个 BaseURL
		if envCfg.ShouldLog("info") && urlIdx < len(urlResults)-1 {
			log.Printf("[%s-BaseURL] BaseURL %d/%d 所有 Key 失败，切换到下一个 BaseURL", apiType, urlIdx+1, len(urlResults))
		}
	}

	return newUpstreamAttemptResult(false, "", 0, failoverState.lastFailoverError, nil, failoverState.lastError)
}

func (a UpstreamAttempt) buildPreparedAttemptRequest(
	upstreamCopy *config.UpstreamConfig,
	apiKey string,
) (*http.Request, string, error) {
	req, err := a.BuildRequest(a.Context, upstreamCopy, apiKey)
	if err != nil {
		return nil, "build_request", err
	}
	prepareStage, err := prepareRequestForUpstream(
		a.Context,
		a.EnvConfig,
		a.ConfigManager,
		a.ChannelScheduler,
		a.Kind,
		upstreamCopy,
		a.LogContext.Model,
		a.LogContext.ConversationID,
		req,
		a.APIType,
	)
	if err != nil {
		return req, prepareStage, err
	}
	return req, "", nil
}

func (a UpstreamAttempt) prepareCandidateUpstream(
	baseURL string,
	capabilities upstreamCapabilityState,
) *config.UpstreamConfig {
	upstreamCopy := a.Upstream.Clone()
	upstreamCopy.BaseURL = baseURL
	upstreamCopy.DisablePromptCacheKey = capabilities.disablePromptCacheKey
	upstreamCopy.RequireReasoningContent = capabilities.requireReasoningContent
	recordConversationAttempt(a.ChannelScheduler, a.Kind, a.Upstream, a.LogContext, a.IsStream)
	return upstreamCopy
}

func (a UpstreamAttempt) retrySameCandidateRequest(
	upstreamCopy *config.UpstreamConfig,
	apiKey string,
	proxyURL string,
) compatibilityRetryResult {
	req, preparationStage, err := a.buildPreparedAttemptRequest(upstreamCopy, apiKey)
	if err != nil {
		if preparationStage != "build_request" && req != nil && req.Body != nil {
			_ = req.Body.Close()
		}
		return compatibilityRetryResult{
			PreparationStage: preparationStage,
			Err:              err,
		}
	}

	resp, err := SendRequest(req, upstreamCopy, a.EnvConfig, a.IsStream, a.APIType, proxyURL)
	if err != nil {
		return compatibilityRetryResult{PreparationStage: "send_request", Err: err}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return compatibilityRetryResult{Response: resp, Succeeded: true}
	}

	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		// 读取中断：响应不完整，按网络故障处理，不返回截断 body。
		return compatibilityRetryResult{PreparationStage: "read_body", Err: fmt.Errorf("读取重试响应体失败: %w", readErr)}
	}
	body = utils.DecompressGzipIfNeeded(resp, body)
	return compatibilityRetryResult{Response: resp, Body: body}
}

func prepareRequestForUpstream(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	kind scheduler.ChannelKind,
	upstream *config.UpstreamConfig,
	model string,
	conversationID string,
	req *http.Request,
	apiType string,
) (string, error) {
	payloadProtocol := payloadProtocolForServiceType(upstream.ServiceType, apiType)
	if err := runAttachedPreRequestHooks(c.Request.Context(), c, req, upstream.Name, payloadProtocol); err != nil {
		return "content_safety", err
	}
	if err := visionlayer.PrepareRequest(
		c,
		envCfg,
		cfgManager,
		channelScheduler,
		kind,
		upstream,
		model,
		conversationID,
		req,
	); err != nil {
		return "vision_layer", err
	}
	if err := runAttachedPreRequestHooks(c.Request.Context(), c, req, upstream.Name, payloadProtocol); err != nil {
		return "content_safety", err
	}
	return "", nil
}

func handleContentSafetyPreparationError(c *gin.Context, apiType string, err error) int {
	if safetyErr := contentSafetyError(err); safetyErr != nil {
		if writeErr := WriteAttachedContentSafetyError(c, safetyErr); writeErr != nil {
			log.Printf("[%s-ContentSafety] 协议错误写出失败: %v", apiType, writeErr)
			c.JSON(http.StatusInternalServerError, gin.H{"error": writeErr.Error(), "code": "CONTENT_SAFETY_RESPONSE_ERROR"})
			return http.StatusInternalServerError
		}
		return http.StatusForbidden
	}
	c.JSON(http.StatusInternalServerError, gin.H{
		"error": err.Error(),
		"code":  "CONTENT_SAFETY_HOOK_ERROR",
	})
	return http.StatusInternalServerError
}
