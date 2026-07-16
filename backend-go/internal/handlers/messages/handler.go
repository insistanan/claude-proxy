// Package messages 提供 Claude Messages API 的处理器
package messages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/handlers/common"
	"github.com/BenedictKing/claude-proxy/internal/middleware"
	"github.com/BenedictKing/claude-proxy/internal/providers"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/gin-gonic/gin"
)

// Handler Messages API 代理处理器
// 支持多渠道调度：当配置多个渠道时自动启用
func Handler(envCfg *config.EnvConfig, cfgManager *config.ConfigManager, channelScheduler *scheduler.ChannelScheduler) gin.HandlerFunc {
	return gin.HandlerFunc(func(c *gin.Context) {
		// 先进行认证
		middleware.ProxyAuthMiddleware(envCfg)(c)
		if c.IsAborted() {
			return
		}
		c.Set(utils.ContextKeyClaudeCodeDisguise, cfgManager.GetClaudeCodeDisguiseEnabled())

		startTime := time.Now()

		// 读取请求体
		bodyBytes, err := common.ReadRequestBody(c, envCfg.MaxRequestBodySize)
		if err != nil {
			return
		}

		// 预处理：移除空 signature 字段，预防 400 错误
		// modified 表示请求体是否被修改，详细日志由 RemoveEmptySignatures 内部记录
		bodyBytes, modified := common.RemoveEmptySignatures(bodyBytes, envCfg.EnableRequestLogs, "Messages")
		_ = modified // 保留以便未来扩展（如需在 handler 层面做额外处理）

		// 解析请求
		var claudeReq types.ClaudeRequest
		if len(bodyBytes) > 0 {
			_ = json.Unmarshal(bodyBytes, &claudeReq)
		}

		hasImage := utils.DetectImageContent(bodyBytes)

		// 提取对话标识
		prompts := common.ExtractPromptsFromClaude(claudeReq.Messages)
		userID := common.ObserveConversationRequest(
			channelScheduler,
			scheduler.ChannelKindMessages,
			common.ResolveConversationIdentity(c, bodyBytes),
			common.BuildConversationTranscript(string(scheduler.ChannelKindMessages), bodyBytes),
			claudeReq.Model,
			prompts,
			utils.ExtractImageFingerprints(bodyBytes),
			claudeReq.Stream,
		)
		defer common.MarkConversationComplete(channelScheduler, userID, scheduler.ChannelKindMessages)

		// 记录原始请求信息（仅在入口处记录一次）
		common.LogOriginalRequest(c, bodyBytes, envCfg, "Messages")

		requestedChannelIndex, hasRequestedChannel, err := common.ExtractRequestedChannelIndex(bodyBytes)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": err.Error(),
				"code":  "INVALID_CHANNEL_INDEX",
			})
			return
		}
		if hasRequestedChannel {
			upstream, channelIndex, err := common.ResolveRequestedUpstream(cfgManager, scheduler.ChannelKindMessages, requestedChannelIndex)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": err.Error(),
					"code":  "INVALID_CHANNEL_INDEX",
				})
				return
			}
			handleSingleChannelWithUpstream(c, envCfg, cfgManager, channelScheduler, bodyBytes, claudeReq, userID, upstream, channelIndex, startTime)
			return
		}

		// 检查是否为多渠道模式
		isMultiChannel := channelScheduler.IsMultiChannelModeForModel(scheduler.ChannelKindMessages, claudeReq.Model)

		if isMultiChannel {
			handleMultiChannel(c, envCfg, cfgManager, channelScheduler, bodyBytes, claudeReq, userID, hasImage, startTime)
		} else {
			handleSingleChannel(c, envCfg, cfgManager, channelScheduler, bodyBytes, claudeReq, userID, startTime)
		}
	})
}

// handleMultiChannel 处理多渠道代理请求
func handleMultiChannel(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	bodyBytes []byte,
	claudeReq types.ClaudeRequest,
	userID string,
	hasImage bool,
	startTime time.Time,
) {
	common.HandleMultiChannelFailover(
		c,
		envCfg,
		channelScheduler,
		scheduler.ChannelKindMessages,
		"Messages",
		userID,
		claudeReq.Model,
		hasImage,
		func(selection *scheduler.SelectionResult) common.MultiChannelAttemptResult {
			upstream := selection.Upstream
			channelIndex := selection.ChannelIndex

			if upstream == nil {
				return common.MultiChannelAttemptResult{}
			}

			provider := providers.GetProvider(upstream.ServiceType)
			if provider == nil {
				return common.MultiChannelAttemptResult{}
			}

			metricsManager := channelScheduler.GetMessagesMetricsManager()
			baseURLs := upstream.GetAllBaseURLs()
			sortedURLResults := channelScheduler.GetSortedURLsForChannel(scheduler.ChannelKindMessages, channelIndex, baseURLs)

			handled, successKey, successBaseURLIdx, failoverErr, usage, lastErr := common.TryUpstreamWithModelMappingFailover(
				c,
				envCfg,
				cfgManager,
				channelScheduler,
				scheduler.ChannelKindMessages,
				"Messages",
				metricsManager,
				upstream,
				claudeReq.Model, // 传入客户端请求的原始模型
				sortedURLResults,
				bodyBytes,
				claudeReq.Stream,
				func(upstream *config.UpstreamConfig, failedKeys map[string]bool) (string, error) {
					return cfgManager.GetNextAPIKey(upstream, failedKeys, "Messages")
				},
				func(c *gin.Context, upstreamCopy *config.UpstreamConfig, apiKey string) (*http.Request, error) {
					req, _, err := provider.ConvertToProviderRequest(c, upstreamCopy, apiKey)
					return req, err
				},
				func(apiKey string) {
					if err := cfgManager.MoveAPIKeyToBottom(channelIndex, apiKey); err != nil {
						log.Printf("[Messages-Key] 警告: 密钥降级失败: %v", err)
					}
				},
				func(url string) {
					channelScheduler.MarkURLFailure(scheduler.ChannelKindMessages, channelIndex, url)
				},
				func(url string) {
					channelScheduler.MarkURLSuccess(scheduler.ChannelKindMessages, channelIndex, url)
				},
				func(c *gin.Context, resp *http.Response, upstreamCopy *config.UpstreamConfig, apiKey string) (*types.Usage, error) {
					if claudeReq.Stream {
						return common.HandleStreamResponse(c, resp, provider, envCfg, startTime, upstreamCopy, bodyBytes, claudeReq.Model)
					}
					return handleNormalResponse(c, resp, provider, envCfg, startTime, bodyBytes, upstreamCopy, apiKey)
				},
				common.AttemptLogContext{
					ChannelIndex:    channelIndex,
					Model:           claudeReq.Model,
					ConversationID:  userID,
					LogStore:        channelScheduler.GetChannelLogStore(scheduler.ChannelKindMessages),
					RequestLogStore: channelScheduler.GetRequestLogStore(),
				},
			)

			return common.MultiChannelAttemptResult{
				Handled:           handled,
				Attempted:         true,
				SuccessKey:        successKey,
				SuccessBaseURLIdx: successBaseURLIdx,
				FailoverError:     failoverErr,
				Usage:             usage,
				LastError:         lastErr,
			}
		},
		func(selection *scheduler.SelectionResult, result common.MultiChannelAttemptResult) {
			if selection == nil || selection.Upstream == nil {
				return
			}
			if result.SuccessKey != "" {
				common.MarkConversationSuccess(channelScheduler, userID, scheduler.ChannelKindMessages, selection.ChannelIndex, selection.Upstream.Name)
				return
			}
			if result.LastError != nil && !errors.Is(result.LastError, context.Canceled) {
				common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindMessages, result.LastError)
			}
		},
		func(ctx *gin.Context, failoverErr *common.FailoverError, lastError error) {
			common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindMessages, lastError)
			common.HandleAllChannelsFailed(ctx, cfgManager.GetFuzzyModeEnabled(), failoverErr, lastError, "Messages")
		},
	)
}

// handleSingleChannel 处理单渠道代理请求
func handleSingleChannel(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	bodyBytes []byte,
	claudeReq types.ClaudeRequest,
	userID string,
	startTime time.Time,
) {
	upstream, channelIndex, err := cfgManager.GetCurrentUpstreamWithIndexForModel(claudeReq.Model)
	if err != nil {
		c.JSON(503, gin.H{
			"error": "未配置任何渠道，请先在管理界面添加渠道",
			"code":  "NO_UPSTREAM",
		})
		return
	}

	handleSingleChannelWithUpstream(c, envCfg, cfgManager, channelScheduler, bodyBytes, claudeReq, userID, upstream, channelIndex, startTime)
}

func handleSingleChannelWithUpstream(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	bodyBytes []byte,
	claudeReq types.ClaudeRequest,
	userID string,
	upstream *config.UpstreamConfig,
	channelIndex int,
	startTime time.Time,
) {

	if len(upstream.APIKeys) == 0 {
		c.JSON(503, gin.H{
			"error": fmt.Sprintf("当前渠道 \"%s\" 未配置API密钥", upstream.Name),
			"code":  "NO_API_KEYS",
		})
		return
	}
	if err := channelScheduler.ValidateFixedChannel(userID, scheduler.ChannelKindMessages, channelIndex); err != nil {
		common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindMessages, err)
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "CONVERSATION_ROUTE_OVERRIDE"})
		return
	}

	provider := providers.GetProvider(upstream.ServiceType)
	if provider == nil {
		c.JSON(400, gin.H{"error": "Unsupported service type"})
		return
	}

	metricsManager := channelScheduler.GetMessagesMetricsManager()
	baseURLs := upstream.GetAllBaseURLs()

	urlResults := common.BuildDefaultURLResults(baseURLs)

	handled, successKey, _, lastFailoverError, _, lastError := common.TryUpstreamWithModelMappingFailover(
		c,
		envCfg,
		cfgManager,
		channelScheduler,
		scheduler.ChannelKindMessages,
		"Messages",
		metricsManager,
		upstream,
		claudeReq.Model,
		urlResults,
		bodyBytes,
		claudeReq.Stream,
		func(upstream *config.UpstreamConfig, failedKeys map[string]bool) (string, error) {
			return cfgManager.GetNextAPIKey(upstream, failedKeys, "Messages")
		},
		func(c *gin.Context, upstreamCopy *config.UpstreamConfig, apiKey string) (*http.Request, error) {
			req, _, err := provider.ConvertToProviderRequest(c, upstreamCopy, apiKey)
			return req, err
		},
		func(apiKey string) {
			if err := cfgManager.MoveAPIKeyToBottom(channelIndex, apiKey); err != nil {
				log.Printf("[Messages-Key] 警告: 密钥降级失败: %v", err)
			}
		},
		nil,
		nil,
		func(c *gin.Context, resp *http.Response, upstreamCopy *config.UpstreamConfig, apiKey string) (*types.Usage, error) {
			if claudeReq.Stream {
				return common.HandleStreamResponse(c, resp, provider, envCfg, startTime, upstreamCopy, bodyBytes, claudeReq.Model)
			}
			return handleNormalResponse(c, resp, provider, envCfg, startTime, bodyBytes, upstreamCopy, apiKey)
		},
		common.AttemptLogContext{
			ChannelIndex:    channelIndex,
			Model:           claudeReq.Model,
			ConversationID:  userID,
			LogStore:        channelScheduler.GetChannelLogStore(scheduler.ChannelKindMessages),
			RequestLogStore: channelScheduler.GetRequestLogStore(),
		},
	)
	if handled {
		if successKey != "" {
			common.MarkConversationSuccess(channelScheduler, userID, scheduler.ChannelKindMessages, channelIndex, upstream.Name)
			channelScheduler.ConsumePromotionCount(channelIndex, scheduler.ChannelKindMessages)
		} else if lastError != nil && !errors.Is(lastError, context.Canceled) {
			common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindMessages, lastError)
		}
		return
	}

	log.Printf("[Messages-Error] 所有API密钥都失败了")
	common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindMessages, lastError)
	common.HandleAllKeysFailed(c, cfgManager.GetFuzzyModeEnabled(), lastFailoverError, lastError, "Messages")
}

// handleNormalResponse 处理非流式响应
func handleNormalResponse(
	c *gin.Context,
	resp *http.Response,
	provider providers.Provider,
	envCfg *config.EnvConfig,
	startTime time.Time,
	requestBody []byte,
	upstream *config.UpstreamConfig,
	apiKey string,
) (*types.Usage, error) {
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to read response"})
		return nil, err
	}

	if envCfg.EnableResponseLogs {
		responseTime := time.Since(startTime).Milliseconds()
		log.Printf("[Messages-Timing] 响应完成: %dms, 状态: %d", responseTime, resp.StatusCode)
		if envCfg.IsDevelopment() {
			respHeaders := make(map[string]string)
			for key, values := range resp.Header {
				if len(values) > 0 {
					respHeaders[key] = values[0]
				}
			}
			var respHeadersJSON []byte
			if envCfg.RawLogOutput {
				respHeadersJSON, _ = json.Marshal(respHeaders)
			} else {
				respHeadersJSON, _ = json.MarshalIndent(respHeaders, "", "  ")
			}
			log.Printf("[Messages-Response] 响应头:\n%s", string(respHeadersJSON))

			var formattedBody string
			if envCfg.RawLogOutput {
				formattedBody = utils.FormatJSONBytesRaw(bodyBytes)
			} else {
				formattedBody = utils.FormatJSONBytesForLog(bodyBytes, 500)
			}
			log.Printf("[Messages-Response] 响应体:\n%s", formattedBody)
		}
	}

	providerResp := &types.ProviderResponse{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		Body:       bodyBytes,
		Stream:     false,
	}

	claudeResp, err := provider.ConvertToClaudeResponse(providerResp)
	if err != nil {
		return nil, fmt.Errorf("转换上游响应失败: %w", err)
	}

	// Token 补全逻辑
	if claudeResp.Usage == nil {
		estimatedInput := utils.EstimateRequestTokens(requestBody)
		estimatedOutput := utils.EstimateResponseTokens(claudeResp.Content)
		claudeResp.Usage = &types.Usage{
			InputTokens:  estimatedInput,
			OutputTokens: estimatedOutput,
		}
		if envCfg.EnableResponseLogs {
			log.Printf("[Messages-Token] 上游无Usage, 本地估算: input=%d, output=%d", estimatedInput, estimatedOutput)
		}
	} else {
		originalInput := claudeResp.Usage.InputTokens
		originalOutput := claudeResp.Usage.OutputTokens
		patched := false

		hasCacheTokens := claudeResp.Usage.CacheCreationInputTokens > 0 || claudeResp.Usage.CacheReadInputTokens > 0

		if claudeResp.Usage.InputTokens <= 1 && !hasCacheTokens {
			claudeResp.Usage.InputTokens = utils.EstimateRequestTokens(requestBody)
			patched = true
		}
		if claudeResp.Usage.OutputTokens <= 1 {
			claudeResp.Usage.OutputTokens = utils.EstimateResponseTokens(claudeResp.Content)
			patched = true
		}
		if envCfg.EnableResponseLogs {
			if patched {
				log.Printf("[Messages-Token] 虚假值补全: InputTokens=%d->%d, OutputTokens=%d->%d",
					originalInput, claudeResp.Usage.InputTokens, originalOutput, claudeResp.Usage.OutputTokens)
			}
			log.Printf("[Messages-Token] InputTokens=%d, OutputTokens=%d, CacheCreationInputTokens=%d, CacheReadInputTokens=%d, CacheCreation5m=%d, CacheCreation1h=%d, CacheTTL=%s",
				claudeResp.Usage.InputTokens, claudeResp.Usage.OutputTokens,
				claudeResp.Usage.CacheCreationInputTokens, claudeResp.Usage.CacheReadInputTokens,
				claudeResp.Usage.CacheCreation5mInputTokens, claudeResp.Usage.CacheCreation1hInputTokens,
				claudeResp.Usage.CacheTTL)
		}
	}

	// 监听客户端断开连接
	ctx := c.Request.Context()
	go func() {
		<-ctx.Done()
		if !c.Writer.Written() {
			if envCfg.EnableResponseLogs {
				responseTime := time.Since(startTime).Milliseconds()
				log.Printf("[Messages-Timing] 响应中断: %dms, 状态: %d", responseTime, resp.StatusCode)
			}
		}
	}()

	// 转发上游响应头
	utils.ForwardResponseHeaders(resp.Header, c.Writer)

	// 缓存字段会被 Cursor 计入 Conversation 上下文。流式出口已经剥离它们，
	// 非流式响应也必须保持同一契约；内部 usage 仍用于后续指标记录。
	clientResp := *claudeResp
	if claudeResp.Usage != nil {
		clientUsage := *claudeResp.Usage
		clientUsage.CacheCreationInputTokens = 0
		clientUsage.CacheCreation5mInputTokens = 0
		clientUsage.CacheCreation1hInputTokens = 0
		clientUsage.CacheReadInputTokens = 0
		clientUsage.CacheTTL = ""
		clientResp.Usage = &clientUsage
	}

	common.MarkRequestLogFirstToken(c)
	c.JSON(200, &clientResp)

	if envCfg.EnableResponseLogs {
		responseTime := time.Since(startTime).Milliseconds()
		log.Printf("[Messages-Timing] 响应发送完成: %dms, 状态: %d", responseTime, resp.StatusCode)
	}

	return claudeResp.Usage, nil
}

// CountTokensHandler 处理 /v1/messages/count_tokens 请求
func CountTokensHandler(envCfg *config.EnvConfig, cfgManager *config.ConfigManager, channelScheduler *scheduler.ChannelScheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		middleware.ProxyAuthMiddleware(envCfg)(c)
		if c.IsAborted() {
			return
		}

		// 使用统一的请求体读取函数，应用大小限制
		bodyBytes, err := common.ReadRequestBody(c, envCfg.MaxRequestBodySize)
		if err != nil {
			// ReadRequestBody 已经返回了错误响应
			return
		}

		var req struct {
			Model    string      `json:"model"`
			System   interface{} `json:"system"`
			Messages interface{} `json:"messages"`
			Tools    interface{} `json:"tools"`
		}
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			c.JSON(400, gin.H{"error": "Invalid JSON"})
			return
		}

		inputTokens := utils.EstimateRequestTokens(bodyBytes)

		c.JSON(200, gin.H{
			"input_tokens": inputTokens,
		})

		if envCfg.EnableResponseLogs {
			log.Printf("[Messages-Token] CountTokens本地估算: model=%s, input_tokens=%d", req.Model, inputTokens)
		}
	}
}
