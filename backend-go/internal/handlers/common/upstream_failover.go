// Package common 提供 handlers 模块的公共功能
package common

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/metrics"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/BenedictKing/claude-proxy/internal/urlhealth"
	"github.com/gin-gonic/gin"
)

// isClientSideError 判断错误是否由客户端明确取消（不应计入渠道失败）
// 仅识别 context.Canceled，broken pipe/connection reset 视为连接故障需要 failover
func isClientSideError(err error) bool {
	if err == nil {
		return false
	}
	// 只有 context.Canceled 才是明确的客户端取消意图
	return errors.Is(err, context.Canceled)
}

// NextAPIKeyFunc 返回下一个可用 API key（按 failover 策略）
type NextAPIKeyFunc func(upstream *config.UpstreamConfig, failedKeys map[string]bool) (string, error)

// BuildRequestFunc 构建上游请求（upstreamCopy.BaseURL 已写入当前尝试的 BaseURL）
type BuildRequestFunc func(c *gin.Context, upstreamCopy *config.UpstreamConfig, apiKey string) (*http.Request, error)

// DeprioritizeKeyFunc 对 quota 相关失败的 key 做降级（实现可选择是否记录日志）
type DeprioritizeKeyFunc func(apiKey string)

// HandleSuccessFunc 处理成功响应（负责写回客户端），并返回 usage（可为 nil）
// 注意：实现方需要自行关闭 resp.Body（与现有 handlers 保持一致）。
type HandleSuccessFunc func(c *gin.Context, resp *http.Response, upstreamCopy *config.UpstreamConfig, apiKey string) (*types.Usage, error)

type AttemptLogContext struct {
	ChannelIndex    int
	Model           string
	ConversationID  string
	LogStore        *metrics.ChannelLogStore
	RequestLogStore *metrics.RequestLogStore
}

var attemptLogCounter uint64

const requestLogFirstTokenAtKey = "__request_log_first_token_at"

func ResetRequestLogFirstToken(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(requestLogFirstTokenAtKey, time.Time{})
}

func MarkRequestLogFirstToken(c *gin.Context) {
	if c == nil {
		return
	}
	if value, ok := c.Get(requestLogFirstTokenAtKey); ok {
		if markedAt, ok := value.(time.Time); ok && !markedAt.IsZero() {
			return
		}
	}
	c.Set(requestLogFirstTokenAtKey, time.Now())
}

// TryUpstreamWithModelMappingFailover 在模型映射级别进行 failover
// 如果配置了模型重定向（一对多），会依次尝试所有映射的模型
// 返回值与 TryUpstreamWithAllKeys 相同
func TryUpstreamWithModelMappingFailover(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	kind scheduler.ChannelKind,
	apiType string,
	metricsManager *metrics.MetricsManager,
	upstream *config.UpstreamConfig,
	requestedModel string, // 客户端请求的原始模型
	urlResults []urlhealth.URLLatencyResult,
	requestBody []byte,
	isStream bool,
	nextAPIKey NextAPIKeyFunc,
	buildRequest BuildRequestFunc,
	deprioritizeKey DeprioritizeKeyFunc,
	markURLFailure func(url string),
	markURLSuccess func(url string),
	handleSuccess HandleSuccessFunc,
	logCtx AttemptLogContext,
) (handled bool, successKey string, successBaseURLIdx int, failoverErr *FailoverError, usage *types.Usage, lastError error) {
	
	// 获取该模型的映射列表
	targetModels := config.ResolveUpstreamModelList(requestedModel, upstream)
	
	if len(targetModels) == 0 {
		// 没有映射，直接使用原始模型
		targetModels = []string{requestedModel}
	}
	
	// 如果只有一个目标模型，直接调用原有逻辑
	if len(targetModels) == 1 {
		return TryUpstreamWithAllKeys(
			c, envCfg, cfgManager, channelScheduler, kind, apiType,
			metricsManager, upstream, urlResults, requestBody, isStream,
			nextAPIKey, buildRequest, deprioritizeKey,
			markURLFailure, markURLSuccess, handleSuccess, logCtx,
		)
	}
	
	// 多个目标模型：依次尝试
	log.Printf("[%s-ModelMapping] 模型 %s 映射到 %d 个备选: %v", apiType, requestedModel, len(targetModels), targetModels)
	
	var lastFailoverError *FailoverError
	var lastErr error
	
	for modelIdx, targetModel := range targetModels {
		// 检查客户端是否已取消
		select {
		case <-c.Request.Context().Done():
			log.Printf("[%s-Cancel] 客户端已取消，停止模型 failover", apiType)
			return true, "", 0, nil, nil, context.Canceled
		default:
		}
		
		log.Printf("[%s-ModelMapping] 尝试备选模型 %d/%d: %s -> %s", 
			apiType, modelIdx+1, len(targetModels), requestedModel, targetModel)
		
		// 创建上游副本，临时覆盖模型映射为当前尝试的单一模型
		upstreamCopy := upstream.Clone()
		upstreamCopy.ModelMapping = map[string][]string{
			requestedModel: {targetModel},
		}
		
		// 更新日志上下文中的模型信息
		modelLogCtx := logCtx
		modelLogCtx.Model = targetModel
		
		handled, successKey, successBaseURLIdx, failoverErr, usage, err := TryUpstreamWithAllKeys(
			c, envCfg, cfgManager, channelScheduler, kind, apiType,
			metricsManager, upstreamCopy, urlResults, requestBody, isStream,
			nextAPIKey, buildRequest, deprioritizeKey,
			markURLFailure, markURLSuccess, handleSuccess, modelLogCtx,
		)
		
		if handled {
			if successKey != "" {
				// 成功
				log.Printf("[%s-ModelMapping] 模型 %s (备选 %d/%d) 请求成功", 
					apiType, targetModel, modelIdx+1, len(targetModels))
				return handled, successKey, successBaseURLIdx, failoverErr, usage, err
			}
			// handled=true 但 successKey 为空：非 failover 错误（如客户端取消、参数错误等）
			return handled, successKey, successBaseURLIdx, failoverErr, usage, err
		}
		
		// 未处理（failover 错误），保存错误信息并尝试下一个模型
		if failoverErr != nil {
			lastFailoverError = failoverErr
		}
		if err != nil {
			lastErr = err
		}
		
		log.Printf("[%s-ModelMapping] 模型 %s (备选 %d/%d) 失败，尝试下一个备选模型", 
			apiType, targetModel, modelIdx+1, len(targetModels))
	}
	
	// 所有模型都失败
	log.Printf("[%s-ModelMapping] 所有 %d 个备选模型都失败", apiType, len(targetModels))
	return false, "", 0, lastFailoverError, nil, lastErr
}
// 返回:
//   - handled: 是否已向客户端写回响应（成功或非 failover 错误）
//   - successKey: 成功的 key（仅 handled=true 且成功时有值）
//   - successBaseURLIdx: 成功 BaseURL 的原始索引（用于指标记录）
//   - failoverErr: 最后一次可故障转移的上游错误（用于多渠道聚合错误）
//   - usage: usage 统计（可能为 nil）
func TryUpstreamWithAllKeys(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	kind scheduler.ChannelKind,
	apiType string,
	metricsManager *metrics.MetricsManager,
	upstream *config.UpstreamConfig,
	urlResults []urlhealth.URLLatencyResult,
	requestBody []byte,
	isStream bool,
	nextAPIKey NextAPIKeyFunc,
	buildRequest BuildRequestFunc,
	deprioritizeKey DeprioritizeKeyFunc,
	markURLFailure func(url string),
	markURLSuccess func(url string),
	handleSuccess HandleSuccessFunc,
	logCtx AttemptLogContext,
) (handled bool, successKey string, successBaseURLIdx int, failoverErr *FailoverError, usage *types.Usage, lastError error) {
	if upstream == nil || len(upstream.APIKeys) == 0 {
		return false, "", 0, nil, nil, nil
	}
	if metricsManager == nil {
		return false, "", 0, nil, nil, nil
	}
	if nextAPIKey == nil || buildRequest == nil || handleSuccess == nil {
		return false, "", 0, nil, nil, nil
	}
	if len(urlResults) == 0 {
		return false, "", 0, nil, nil, nil
	}

	var lastFailoverError *FailoverError
	deprioritizeCandidates := make(map[string]bool)
	requestLogID := nextAttemptLogID("req")

	// 强制探测模式：基于本次优先尝试的 BaseURL 判断（避免 BaseURL/BaseURLs 不一致导致误判）
	forceProbeMode := AreAllKeysSuspended(metricsManager, urlResults[0].URL, upstream.APIKeys)
	if forceProbeMode {
		log.Printf("[%s-ForceProbe] 渠道 %s 所有 Key 都被熔断，启用强制探测模式", apiType, upstream.Name)
	}

	for urlIdx, urlResult := range urlResults {
		currentBaseURL := urlResult.URL
		originalIdx := urlResult.OriginalIdx // 原始索引用于指标记录
		failedKeys := make(map[string]bool)  // 每个 BaseURL 重置失败 Key 列表
		maxRetries := len(upstream.APIKeys)

		for attempt := 0; attempt < maxRetries; attempt++ {
			RestoreRequestBody(c, requestBody)
			ResetRequestLogFirstToken(c)
			attemptStart := time.Now()

			apiKey, err := nextAPIKey(upstream, failedKeys)
			if err != nil {
				lastError = err
				break // 当前 BaseURL 没有可用 Key，尝试下一个 BaseURL
			}

			// 检查熔断状态
			if !forceProbeMode && metricsManager.ShouldSuspendKey(currentBaseURL, apiKey) {
				failedKeys[apiKey] = true
				log.Printf("[%s-Circuit] 跳过熔断中的 Key: %s", apiType, utils.MaskAPIKey(apiKey))
				continue
			}

			if envCfg.ShouldLog("info") {
				log.Printf("[%s-Key] 使用API密钥: %s (BaseURL %d/%d, 尝试 %d/%d)",
					apiType, utils.MaskAPIKey(apiKey), urlIdx+1, len(urlResults), attempt+1, maxRetries)
			}

			// 使用深拷贝避免并发修改问题
			upstreamCopy := upstream.Clone()
			upstreamCopy.BaseURL = currentBaseURL
			recordConversationAttempt(channelScheduler, kind, upstream, logCtx, isStream)

			req, err := buildRequest(c, upstreamCopy, apiKey)
			if err != nil {
				lastError = err
				failedKeys[apiKey] = true
				channelScheduler.RecordFailure(currentBaseURL, apiKey, kind)
				recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", 0, false, attemptStart, "build_request", err.Error(), true, isStream, nil)
				continue
			}

			// 记录请求开始
			channelScheduler.RecordRequestStart(currentBaseURL, apiKey, kind)

			// TCP 建连开始即计数：将活跃度统计提前到发起上游请求之前
			requestID := metricsManager.RecordRequestConnected(currentBaseURL, apiKey, logCtx.Model)

			resp, err := SendRequest(req, upstream, envCfg, isStream, apiType)
			if err != nil {
				lastError = err
				// 区分客户端取消和真实渠道故障（统一口径）
				if isClientSideError(err) {
					// 客户端取消：不计入失败，不触发 failover
					metricsManager.RecordRequestFinalizeClientCancel(currentBaseURL, apiKey, requestID)
					channelScheduler.RecordRequestEnd(currentBaseURL, apiKey, kind)
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "cancelled", 0, false, attemptStart, "client_cancelled", err.Error(), false, isStream, nil)
					log.Printf("[%s-Cancel] 请求已取消（SendRequest 阶段）", apiType)
					return true, "", 0, nil, nil, err
				}
				// 真实渠道故障：计入失败，继续 failover
				failedKeys[apiKey] = true
				cfgManager.MarkKeyAsFailed(apiKey, apiType)
				metricsManager.RecordRequestFinalizeFailure(currentBaseURL, apiKey, requestID)
				channelScheduler.RecordRequestEnd(currentBaseURL, apiKey, kind)
				if markURLFailure != nil {
					markURLFailure(currentBaseURL)
				}
				recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", 0, false, attemptStart, "network", err.Error(), true, isStream, nil)
				log.Printf("[%s-Key] 警告: API密钥失败: %v", apiType, err)
				continue
			}

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				respBodyBytes, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				respBodyBytes = utils.DecompressGzipIfNeeded(resp, respBodyBytes)

				shouldFailover, isQuotaRelated := ShouldRetryWithNextKey(resp.StatusCode, respBodyBytes, cfgManager.GetFuzzyModeEnabled(), apiType)
				if shouldFailover {
					lastError = fmt.Errorf("上游错误: %d", resp.StatusCode)
					failedKeys[apiKey] = true
					cfgManager.MarkKeyAsFailed(apiKey, apiType)
					metricsManager.RecordRequestFinalizeFailure(currentBaseURL, apiKey, requestID)
					channelScheduler.RecordRequestEnd(currentBaseURL, apiKey, kind)
					if markURLFailure != nil {
						markURLFailure(currentBaseURL)
					}
					log.Printf("[%s-Key] 警告: API密钥失败 (状态: %d)，尝试下一个密钥", apiType, resp.StatusCode)

					lastFailoverError = &FailoverError{
						Status: resp.StatusCode,
						Body:   respBodyBytes,
					}

					if isQuotaRelated {
						deprioritizeCandidates[apiKey] = true
					}
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, classifyUpstreamError(resp.StatusCode, isQuotaRelated), string(respBodyBytes), true, isStream, nil)
					continue
				}

				// 非 failover 错误，记录失败指标后返回（请求已处理）
				metricsManager.RecordRequestFinalizeFailure(currentBaseURL, apiKey, requestID)
				channelScheduler.RecordRequestEnd(currentBaseURL, apiKey, kind)
				recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, classifyUpstreamError(resp.StatusCode, false), string(respBodyBytes), false, isStream, nil)
				c.Data(resp.StatusCode, "application/json", respBodyBytes)
				return true, "", 0, nil, nil, nil
			}

			// 成功响应：处理 quota key 降级
			if deprioritizeKey != nil && len(deprioritizeCandidates) > 0 {
				for key := range deprioritizeCandidates {
					deprioritizeKey(key)
				}
			}

			if markURLSuccess != nil {
				markURLSuccess(currentBaseURL)
			}

			usage, err = handleSuccess(c, resp, upstreamCopy, apiKey)
			if err != nil {
				lastError = err
				// 区分客户端错误和渠道故障
				if isClientSideError(err) {
					// 客户端取消/断开：计入总请求数但不计入失败
					metricsManager.RecordRequestFinalizeClientCancel(currentBaseURL, apiKey, requestID)
					channelScheduler.RecordRequestEnd(currentBaseURL, apiKey, kind)
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "cancelled", resp.StatusCode, false, attemptStart, "client_cancelled", err.Error(), false, isStream, usage)
					log.Printf("[%s-Cancel] 请求已取消，停止渠道 failover", apiType)
				} else {
					// 真实渠道故障：计入失败指标
					cfgManager.MarkKeyAsFailed(apiKey, apiType)
					metricsManager.RecordRequestFinalizeFailure(currentBaseURL, apiKey, requestID)
					channelScheduler.RecordRequestEnd(currentBaseURL, apiKey, kind)
					shouldRetryResponseProcessing := !c.Writer.Written()
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, "response_processing", err.Error(), shouldRetryResponseProcessing, isStream, usage)
					log.Printf("[%s-Key] 警告: 响应处理失败: %v", apiType, err)
					if shouldRetryResponseProcessing {
						failedKeys[apiKey] = true
						if markURLFailure != nil {
							markURLFailure(currentBaseURL)
						}
						lastFailoverError = &FailoverError{
							Status: resp.StatusCode,
							Body:   []byte(err.Error()),
						}
						continue
					}
				}
				return true, "", 0, nil, usage, err
			}

			metricsManager.RecordRequestFinalizeSuccess(currentBaseURL, apiKey, requestID, usage)
			channelScheduler.RecordRequestEnd(currentBaseURL, apiKey, kind)
			recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "completed", resp.StatusCode, true, attemptStart, "", "", false, isStream, usage)
			return true, apiKey, originalIdx, nil, usage, nil
		}

		// 当前 BaseURL 的所有 Key 都失败，记录并尝试下一个 BaseURL
		if envCfg.ShouldLog("info") && urlIdx < len(urlResults)-1 {
			log.Printf("[%s-BaseURL] BaseURL %d/%d 所有 Key 失败，切换到下一个 BaseURL", apiType, urlIdx+1, len(urlResults))
		}
	}

	return false, "", 0, lastFailoverError, nil, lastError
}

func recordConversationAttempt(channelScheduler *scheduler.ChannelScheduler, kind scheduler.ChannelKind, upstream *config.UpstreamConfig, logCtx AttemptLogContext, isStream bool) {
	if channelScheduler == nil || strings.TrimSpace(logCtx.ConversationID) == "" || upstream == nil {
		return
	}
	channelScheduler.MarkConversationAttempt(
		logCtx.ConversationID,
		kind,
		logCtx.ChannelIndex,
		upstream.Name,
		logCtx.Model,
		config.ResolveUpstreamModel(logCtx.Model, upstream),
		isStream,
	)
}

func recordAttemptLog(
	c *gin.Context,
	logCtx AttemptLogContext,
	upstream *config.UpstreamConfig,
	apiType string,
	requestID string,
	baseURL string,
	apiKey string,
	status string,
	statusCode int,
	success bool,
	start time.Time,
	errorType string,
	errorMessage string,
	retried bool,
	isStream bool,
	usage *types.Usage,
) {
	if logCtx.LogStore == nil && logCtx.RequestLogStore == nil {
		return
	}

	channelName := ""
	resolvedModel := logCtx.Model
	if upstream != nil {
		channelName = upstream.Name
		resolvedModel = config.ResolveUpstreamModel(logCtx.Model, upstream)
	}
	transform := ""
	if strings.TrimSpace(logCtx.Model) != "" && strings.TrimSpace(resolvedModel) != "" && logCtx.Model != resolvedModel {
		transform = logCtx.Model + " -> " + resolvedModel
	}
	timestamp := time.Now().Format(time.RFC3339Nano)
	attemptID := nextAttemptLogID("attempt")
	durationMs := time.Since(start).Milliseconds()
	errorMessage = truncateLogMessage(errorMessage, 500)

	if logCtx.LogStore != nil {
		logCtx.LogStore.Record(&metrics.ChannelLog{
			RequestID:             requestID,
			AttemptID:             attemptID,
			Timestamp:             timestamp,
			Status:                status,
			StatusCode:            statusCode,
			Success:               success,
			DurationMs:            durationMs,
			APIType:               apiType,
			Model:                 logCtx.Model,
			InputTokens:           usageInputTokens(usage),
			OutputTokens:          usageOutputTokens(usage),
			CacheCreationTokens:   usageCacheCreationTokens(usage),
			CacheReadTokens:       usageCacheReadTokens(usage),
			CacheCreation5mTokens: usageCacheCreation5mTokens(usage),
			CacheCreation1hTokens: usageCacheCreation1hTokens(usage),
			ChannelIndex:          logCtx.ChannelIndex,
			ChannelName:           channelName,
			BaseURL:               baseURL,
			KeyMask:               utils.MaskAPIKey(apiKey),
			ErrorType:             errorType,
			ErrorMessage:          errorMessage,
			Retried:               retried,
			Stream:                isStream,
		})
	}
	if logCtx.RequestLogStore != nil {
		logCtx.RequestLogStore.Record(metrics.RequestLogEntry{
			RequestID:             requestID,
			AttemptID:             attemptID,
			Timestamp:             timestamp,
			APIType:               apiType,
			Status:                status,
			StatusCode:            statusCode,
			Success:               success,
			DurationMs:            durationMs,
			FirstTokenMs:          requestLogFirstTokenMs(c, start),
			Model:                 logCtx.Model,
			ResolvedModel:         resolvedModel,
			Transform:             transform,
			InputTokens:           usageInputTokens(usage),
			OutputTokens:          usageOutputTokens(usage),
			CacheCreationTokens:   usageCacheCreationTokens(usage),
			CacheReadTokens:       usageCacheReadTokens(usage),
			CacheCreation5mTokens: usageCacheCreation5mTokens(usage),
			CacheCreation1hTokens: usageCacheCreation1hTokens(usage),
			CacheTTL:              usageCacheTTL(usage),
			ChannelIndex:          logCtx.ChannelIndex,
			ChannelName:           channelName,
			BaseURL:               baseURL,
			KeyMask:               utils.MaskAPIKey(apiKey),
			ErrorType:             errorType,
			ErrorMessage:          errorMessage,
			Retried:               retried,
			Stream:                isStream,
			ConversationID:        logCtx.ConversationID,
		})
	}
}

func requestLogFirstTokenMs(c *gin.Context, start time.Time) int64 {
	if c == nil {
		return 0
	}
	value, ok := c.Get(requestLogFirstTokenAtKey)
	if !ok {
		return 0
	}
	markedAt, ok := value.(time.Time)
	if !ok || markedAt.IsZero() || markedAt.Before(start) {
		return 0
	}
	return markedAt.Sub(start).Milliseconds()
}

func usageInputTokens(usage *types.Usage) int {
	if usage == nil {
		return 0
	}
	if usage.InputTokens > 0 {
		return usage.InputTokens
	}
	return usage.PromptTokens
}

func usageOutputTokens(usage *types.Usage) int {
	if usage == nil {
		return 0
	}
	if usage.OutputTokens > 0 {
		return usage.OutputTokens
	}
	return usage.CompletionTokens
}

func usageCacheCreationTokens(usage *types.Usage) int {
	if usage == nil {
		return 0
	}
	if usage.CacheCreationInputTokens > 0 {
		return usage.CacheCreationInputTokens
	}
	return usage.CacheCreation5mInputTokens + usage.CacheCreation1hInputTokens
}

func usageCacheReadTokens(usage *types.Usage) int {
	if usage == nil {
		return 0
	}
	return usage.CacheReadInputTokens
}

func usageCacheCreation5mTokens(usage *types.Usage) int {
	if usage == nil {
		return 0
	}
	return usage.CacheCreation5mInputTokens
}

func usageCacheCreation1hTokens(usage *types.Usage) int {
	if usage == nil {
		return 0
	}
	return usage.CacheCreation1hInputTokens
}

func usageCacheTTL(usage *types.Usage) string {
	if usage == nil {
		return ""
	}
	return usage.CacheTTL
}

func nextAttemptLogID(prefix string) string {
	seq := atomic.AddUint64(&attemptLogCounter, 1)
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), seq)
}

func classifyUpstreamError(statusCode int, quotaRelated bool) string {
	if quotaRelated {
		return "quota"
	}
	switch {
	case statusCode == http.StatusTooManyRequests:
		return "rate_limit"
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return "auth"
	case statusCode >= 500:
		return "upstream_5xx"
	case statusCode >= 400:
		return "upstream_4xx"
	default:
		return "upstream"
	}
}

func truncateLogMessage(message string, limit int) string {
	message = strings.TrimSpace(message)
	if limit <= 0 || len(message) <= limit {
		return message
	}
	return message[:limit] + "..."
}

// BuildDefaultURLResults 将 URLs 转为按原始顺序的结果列表（无动态排序）
func BuildDefaultURLResults(urls []string) []urlhealth.URLLatencyResult {
	results := make([]urlhealth.URLLatencyResult, len(urls))
	for i, url := range urls {
		results[i] = urlhealth.URLLatencyResult{
			URL:         url,
			OriginalIdx: i,
			Success:     true,
		}
	}
	return results
}
