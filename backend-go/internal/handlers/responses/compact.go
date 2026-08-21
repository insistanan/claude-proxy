// Package responses 提供 Responses API 的处理器
package responses

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/handlers/common"
	"github.com/BenedictKing/claude-proxy/internal/middleware"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/BenedictKing/claude-proxy/internal/session"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/gin-gonic/gin"
)

// compactError 封装 compact 请求错误
type compactError struct {
	status          int
	body            []byte
	shouldFailover  bool
	responseWritten bool
}

// CompactHandler Responses API compact 端点处理器
// POST /v1/responses/compact - 压缩对话上下文，用于长期代理工作流
func CompactHandler(
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	_ *session.SessionManager,
	channelScheduler *scheduler.ChannelScheduler,
	contentSafetyPipelines ...*common.HookPipeline,
) gin.HandlerFunc {
	contentSafetyPipeline := common.ResolveContentSafetyPipeline(cfgManager, contentSafetyPipelines...)
	return gin.HandlerFunc(func(c *gin.Context) {
		// 认证
		middleware.ProxyAuthMiddleware(envCfg)(c)
		if c.IsAborted() {
			return
		}
		c.Set(utils.ContextKeyCodexDisguise, cfgManager.GetCodexDisguiseEnabled())

		// 读取请求体
		maxBodySize := envCfg.MaxRequestBodySize
		bodyBytes, err := common.ReadRequestBody(c, maxBodySize)
		if err != nil {
			return
		}

		// 提取对话标识用于 Trace 亲和性
		userID := common.ExtractConversationID(c, bodyBytes)
		model := compactRequestModel(bodyBytes)
		common.AttachHookPipeline(c, contentSafetyPipeline, common.HookContext{
			APIType: string(scheduler.ChannelKindResponses),
			Model:   model,
			Stream:  false,
		})

		// 检查是否为多渠道模式
		isMultiChannel := channelScheduler.IsMultiChannelModeForModel(scheduler.ChannelKindResponses, model)

		if isMultiChannel {
			handleMultiChannelCompact(c, envCfg, cfgManager, channelScheduler, bodyBytes, userID, model)
		} else {
			handleSingleChannelCompact(c, envCfg, cfgManager, bodyBytes, model)
		}
	})
}

// handleSingleChannelCompact 单渠道 compact 请求（带 key 轮转）
func handleSingleChannelCompact(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	bodyBytes []byte,
	model string,
) {
	upstream, _, err := cfgManager.GetCurrentResponsesUpstreamWithIndexForModel(model)
	if err != nil {
		c.JSON(503, gin.H{"error": "未配置任何 Responses 渠道"})
		return
	}

	if len(upstream.APIKeys) == 0 {
		c.JSON(503, gin.H{"error": "当前渠道未配置 API 密钥"})
		return
	}

	// Key 轮转：尝试所有可用 key
	failedKeys := make(map[string]bool)
	var lastErr *compactError

	for attempt := 0; attempt < len(upstream.APIKeys); attempt++ {
		apiKey, err := cfgManager.GetNextResponsesAPIKey(upstream, failedKeys)
		if err != nil {
			break
		}

		success, compactErr := tryCompactWithKey(c, upstream, apiKey, bodyBytes, envCfg, cfgManager)
		if success {
			return
		}

		if compactErr != nil {
			if compactErr.responseWritten {
				return
			}
			lastErr = compactErr
			if compactErr.shouldFailover {
				failedKeys[apiKey] = true
				cfgManager.MarkKeyAsFailed(apiKey, "Responses")
				continue
			}
			// 非故障转移错误，直接返回
			c.Data(compactErr.status, "application/json", compactErr.body)
			return
		}
	}

	// 所有 key 都失败
	if cfgManager.GetFuzzyModeEnabled() {
		c.JSON(503, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "service_unavailable",
				"message": "All upstream channels are currently unavailable",
			},
		})
		return
	}

	if lastErr != nil {
		c.Data(lastErr.status, "application/json", lastErr.body)
	} else {
		c.JSON(503, gin.H{"error": "所有 API 密钥都不可用"})
	}
}

// handleMultiChannelCompact 多渠道 compact 请求（带故障转移和亲和性）
func handleMultiChannelCompact(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	bodyBytes []byte,
	userID string,
	model string,
) {
	failedChannels := make(map[int]bool)
	maxAttempts := channelScheduler.GetActiveChannelCountForModel(scheduler.ChannelKindResponses, model)
	var lastErr *compactError

	for attempt := 0; attempt < maxAttempts; attempt++ {
		selection, err := channelScheduler.SelectChannel(c.Request.Context(), userID, failedChannels, scheduler.ChannelKindResponses, model)
		if err != nil {
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

		// 每个渠道尝试所有 key
		success, successKey, compactErr := tryCompactChannelWithAllKeys(c, upstream, channelIndex, cfgManager, channelScheduler, bodyBytes, envCfg)

		if success {
			releaseReservation()
			// compact 不产生 usage，但仍需记录成功以更新熔断器/权重
			if successKey != "" {
				channelScheduler.RecordSuccessWithUsage(upstream.BaseURL, successKey, channelIndex, nil, scheduler.ChannelKindResponses)
				// 只有真正成功的请求才设置 Trace 亲和
				channelScheduler.SetTraceAffinityForKind(scheduler.ChannelKindResponses, userID, channelIndex)
				channelScheduler.ConsumePromotionCount(channelIndex, scheduler.ChannelKindResponses)
			}
			return
		}

		releaseReservation()
		failedChannels[channelIndex] = true
		if compactErr != nil {
			if compactErr.responseWritten {
				return
			}
			lastErr = compactErr
		}
	}

	// 所有渠道都失败
	if cfgManager.GetFuzzyModeEnabled() {
		c.JSON(503, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "service_unavailable",
				"message": "All upstream channels are currently unavailable",
			},
		})
		return
	}

	if lastErr != nil {
		c.Data(lastErr.status, "application/json", lastErr.body)
	} else {
		c.JSON(503, gin.H{"error": "所有 Responses 渠道都不可用"})
	}
}

func compactRequestModel(body []byte) string {
	var payload struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.Model)
}

// tryCompactChannelWithAllKeys 尝试渠道的所有 key
func tryCompactChannelWithAllKeys(
	c *gin.Context,
	upstream *config.UpstreamConfig,
	channelIndex int,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	bodyBytes []byte,
	envCfg *config.EnvConfig,
) (bool, string, *compactError) {
	if len(upstream.APIKeys) == 0 {
		return false, "", nil
	}

	metricsManager := channelScheduler.MetricsManager(scheduler.ChannelKindResponses)

	failedKeys := make(map[string]bool)
	var lastErr *compactError

	// 强制探测模式
	forceProbeMode := common.AreAllKeysSuspended(metricsManager, upstream.BaseURL, upstream.APIKeys, channelIndex)
	if forceProbeMode {
		log.Printf("[Compact-Probe] 渠道 %s 所有 Key 都被熔断，启用强制探测模式", upstream.Name)
	}

	for attempt := 0; attempt < len(upstream.APIKeys); attempt++ {
		apiKey, err := cfgManager.GetNextResponsesAPIKey(upstream, failedKeys)
		if err != nil {
			break
		}

		// 检查熔断状态
		if !forceProbeMode && metricsManager.ShouldSuspendKey(upstream.BaseURL, apiKey, channelIndex) {
			failedKeys[apiKey] = true
			log.Printf("[Compact-Key] 跳过熔断中的 Key: %s", utils.MaskAPIKey(apiKey))
			continue
		}

		success, compactErr := tryCompactWithKey(c, upstream, apiKey, bodyBytes, envCfg, cfgManager)
		if success {
			return true, apiKey, nil
		}

		if compactErr != nil {
			if compactErr.responseWritten {
				return true, "", nil
			}
			lastErr = compactErr
			if compactErr.shouldFailover {
				failedKeys[apiKey] = true
				cfgManager.MarkKeyAsFailed(apiKey, "Responses")
				channelScheduler.RecordFailure(upstream.BaseURL, apiKey, channelIndex, scheduler.ChannelKindResponses)
				continue
			}
			// 非故障转移错误，返回但标记渠道成功（请求已处理）
			c.Data(compactErr.status, "application/json", compactErr.body)
			return true, "", nil
		}
	}

	return false, "", lastErr
}

// tryCompactWithKey 使用单个 key 尝试 compact 请求
func tryCompactWithKey(
	c *gin.Context,
	upstream *config.UpstreamConfig,
	apiKey string,
	bodyBytes []byte,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
) (bool, *compactError) {
	if upstream != nil && upstream.DisablePromptCacheKey {
		var payload map[string]interface{}
		if json.Unmarshal(bodyBytes, &payload) == nil {
			delete(payload, "prompt_cache_key")
			delete(payload, "prompt_cache_retention")
			if sanitized, err := utils.MarshalJSONNoEscape(payload); err == nil {
				bodyBytes = sanitized
			}
		}
	}
	targetURL := buildCompactURL(upstream)
	req, err := http.NewRequestWithContext(c.Request.Context(), "POST", targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return false, &compactError{status: 500, body: []byte(`{"error":"创建请求失败"}`), shouldFailover: true}
	}

	req.Header = utils.PrepareUpstreamHeaders(c, req.URL.Host)
	req.Header.Del("authorization")
	req.Header.Del("x-api-key")
	utils.SetAuthenticationHeader(req.Header, apiKey)
	req.Header.Set("Content-Type", "application/json")
	if err := common.RunAttachedPreRequestHooks(c.Request.Context(), c, req, upstream.Name, "responses"); err != nil {
		_ = req.Body.Close()
		writeCompactContentSafetyError(c, err)
		return false, &compactError{responseWritten: true}
	}

	proxyURL, err := cfgManager.ResolveUpstreamProxyURL(upstream)
	if err != nil {
		_ = req.Body.Close()
		return false, &compactError{status: 500, body: []byte(`{"error":"上游代理配置无效"}`), shouldFailover: false}
	}
	resp, err := common.SendRequest(req, upstream, envCfg, false, "Responses", proxyURL)
	if err != nil {
		return false, &compactError{status: 502, body: []byte(`{"error":"上游请求失败"}`), shouldFailover: true}
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		// 读取中断：响应不完整，按上游网络故障转移处理，不回写截断 body。
		log.Printf("[Compact-Key] 警告: 读取上游响应体失败 (状态: %d): %v", resp.StatusCode, readErr)
		return false, &compactError{status: 502, body: []byte(`{"error":"读取上游响应失败"}`), shouldFailover: true}
	}
	respBody = utils.DecompressGzipIfNeeded(resp, respBody)

	// 判断是否需要故障转移
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		shouldFailover, _ := common.ShouldRetryWithNextKey(resp.StatusCode, respBody, cfgManager.GetFuzzyModeEnabled(), "Responses")
		return false, &compactError{status: resp.StatusCode, body: respBody, shouldFailover: shouldFailover}
	}

	// 成功
	respBody, err = common.RunAttachedPostResponseHooks(c.Request.Context(), c, respBody, resp)
	if err != nil {
		writeCompactContentSafetyError(c, err)
		return false, &compactError{responseWritten: true}
	}
	utils.ForwardResponseHeaders(resp.Header, c.Writer)
	c.Data(resp.StatusCode, "application/json", respBody)
	return true, nil
}

func writeCompactContentSafetyError(c *gin.Context, err error) {
	if writeErr := common.WriteAttachedContentSafetyError(c, err); writeErr == nil {
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{
		"error": "内容安全检查失败",
		"code":  "CONTENT_SAFETY_HOOK_ERROR",
	})
}

// buildCompactURL 构建 compact 端点 URL
func buildCompactURL(upstream *config.UpstreamConfig) string {
	baseURL := strings.TrimSuffix(upstream.BaseURL, "/")
	versionPattern := regexp.MustCompile(`/v\d+[a-z]*$`)
	if versionPattern.MatchString(baseURL) {
		return baseURL + "/responses/compact"
	}
	return baseURL + "/v1/responses/compact"
}
