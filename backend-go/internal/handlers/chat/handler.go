// Package chat 提供 OpenAI Chat Completions API 的代理处理器
package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"regexp"
	"strings"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/handlers/common"
	"github.com/BenedictKing/claude-proxy/internal/middleware"
	"github.com/BenedictKing/claude-proxy/internal/modelcatalog"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/gin-gonic/gin"
)

var chatVersionPattern = regexp.MustCompile(`/v\d+[a-z]*$`)

// Handler Chat Completions API 代理处理器。
// Chat 是独立一等公民：只走 Chat 渠道池，不默认进行 Anthropic/Gemini 协议转换。
func Handler(envCfg *config.EnvConfig, cfgManager *config.ConfigManager, channelScheduler *scheduler.ChannelScheduler) gin.HandlerFunc {
	return gin.HandlerFunc(func(c *gin.Context) {
		middleware.ProxyAuthMiddleware(envCfg)(c)
		if c.IsAborted() {
			return
		}

		startTime := time.Now()
		bodyBytes, err := common.ReadRequestBody(c, envCfg.MaxRequestBodySize)
		if err != nil {
			return
		}

		var chatReq types.OpenAIRequest
		if len(bodyBytes) == 0 || json.Unmarshal(bodyBytes, &chatReq) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Chat Completions request body"})
			return
		}
		if strings.TrimSpace(chatReq.Model) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
			return
		}

		hasImage := utils.DetectImageContent(bodyBytes)
		userID := chatReqUserID(bodyBytes)
		if userID == "" {
			userID = common.ExtractConversationID(c, bodyBytes)
		}
		prompts := common.ExtractPromptsFromOpenAI(chatReq.Messages)
		userID = common.ObserveConversationPrompts(channelScheduler, scheduler.ChannelKindChat, userID, chatReq.Model, prompts, chatReq.Stream)
		defer common.MarkConversationComplete(channelScheduler, userID, scheduler.ChannelKindChat)

		common.LogOriginalRequest(c, bodyBytes, envCfg, "Chat")

		requestedChannelIndex, hasRequestedChannel, err := common.ExtractRequestedChannelIndex(bodyBytes)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": err.Error(),
				"code":  "INVALID_CHANNEL_INDEX",
			})
			return
		}
		if hasRequestedChannel {
			upstream, channelIndex, err := common.ResolveRequestedUpstream(cfgManager, scheduler.ChannelKindChat, requestedChannelIndex)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": err.Error(),
					"code":  "INVALID_CHANNEL_INDEX",
				})
				return
			}
			handleSingleChannelWithUpstream(c, envCfg, cfgManager, channelScheduler, bodyBytes, chatReq, userID, upstream, channelIndex, startTime)
			return
		}

		if route, ok := modelcatalog.ResolveChatRoute(c.Request.Context(), cfgManager, chatReq.Model); ok {
			handleRoutedChat(c, envCfg, cfgManager, channelScheduler, route, bodyBytes, chatReq, userID, startTime)
			return
		}

		if channelScheduler.IsMultiChannelMode(scheduler.ChannelKindChat) {
			handleMultiChannel(c, envCfg, cfgManager, channelScheduler, bodyBytes, chatReq, userID, hasImage, startTime)
			return
		}

		handleSingleChannel(c, envCfg, cfgManager, channelScheduler, bodyBytes, chatReq, userID, startTime)
	})
}

func handleMultiChannel(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	bodyBytes []byte,
	chatReq types.OpenAIRequest,
	userID string,
	hasImage bool,
	startTime time.Time,
) {
	metricsManager := channelScheduler.GetChatMetricsManager()

	common.HandleMultiChannelFailover(
		c,
		envCfg,
		channelScheduler,
		scheduler.ChannelKindChat,
		"Chat",
		userID,
		hasImage,
		func(selection *scheduler.SelectionResult) common.MultiChannelAttemptResult {
			upstream := selection.Upstream
			channelIndex := selection.ChannelIndex
			if upstream == nil {
				return common.MultiChannelAttemptResult{}
			}

			baseURLs := upstream.GetAllBaseURLs()
			sortedURLResults := channelScheduler.GetSortedURLsForChannel(scheduler.ChannelKindChat, channelIndex, baseURLs)

			handled, successKey, successBaseURLIdx, failoverErr, usage, lastErr := common.TryUpstreamWithAllKeys(
				c,
				envCfg,
				cfgManager,
				channelScheduler,
				scheduler.ChannelKindChat,
				"Chat",
				metricsManager,
				upstream,
				sortedURLResults,
				bodyBytes,
				chatReq.Stream,
				func(upstream *config.UpstreamConfig, failedKeys map[string]bool) (string, error) {
					return cfgManager.GetNextChatAPIKey(upstream, failedKeys)
				},
				func(c *gin.Context, upstreamCopy *config.UpstreamConfig, apiKey string) (*http.Request, error) {
					return buildChatUpstreamRequest(c, upstreamCopy, apiKey, bodyBytes)
				},
				func(apiKey string) {
					if err := cfgManager.MoveChatAPIKeyToBottom(channelIndex, apiKey); err != nil {
						log.Printf("[Chat-Key] 警告: 密钥降级失败: %v", err)
					}
				},
				func(url string) {
					channelScheduler.MarkURLFailure(scheduler.ChannelKindChat, channelIndex, url)
				},
				func(url string) {
					channelScheduler.MarkURLSuccess(scheduler.ChannelKindChat, channelIndex, url)
				},
				func(c *gin.Context, resp *http.Response, upstreamCopy *config.UpstreamConfig, apiKey string) (*types.Usage, error) {
					return handleSuccess(c, resp, envCfg, startTime, chatReq.Stream, bodyBytes)
				},
				common.AttemptLogContext{
					ChannelIndex:    channelIndex,
					Model:           chatReq.Model,
					ConversationID:  userID,
					LogStore:        channelScheduler.GetChannelLogStore(scheduler.ChannelKindChat),
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
				common.MarkConversationSuccess(channelScheduler, userID, scheduler.ChannelKindChat, selection.ChannelIndex, selection.Upstream.Name)
				return
			}
			if result.LastError != nil && !errors.Is(result.LastError, context.Canceled) {
				common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindChat, result.LastError)
			}
		},
		func(ctx *gin.Context, failoverErr *common.FailoverError, lastError error) {
			common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindChat, lastError)
			common.HandleAllChannelsFailed(ctx, cfgManager.GetFuzzyModeEnabled(), failoverErr, lastError, "Chat")
		},
	)
}

func handleRoutedChat(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	route modelcatalog.ChatRoute,
	bodyBytes []byte,
	chatReq types.OpenAIRequest,
	userID string,
	startTime time.Time,
) {
	upstream, err := chatRouteUpstream(cfgManager, route)
	if err != nil {
		common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindChat, err)
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"message": err.Error(),
				"type":    "invalid_request_error",
			},
		})
		return
	}

	if err := channelScheduler.ValidateFixedChannel(userID, scheduler.ChannelKindChat, route.ChannelIndex); err != nil {
		common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindChat, err)
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "CONVERSATION_ROUTE_OVERRIDE"})
		return
	}

	routedBody, err := replaceChatModel(bodyBytes, route.UpstreamModel)
	if err != nil {
		common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindChat, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	metricsManager := channelScheduler.GetChatMetricsManager()
	urlResults := common.BuildDefaultURLResults([]string{route.BaseURL})
	handled, successKey, _, lastFailoverError, _, lastError := common.TryUpstreamWithAllKeys(
		c,
		envCfg,
		cfgManager,
		channelScheduler,
		scheduler.ChannelKindChat,
		"Chat",
		metricsManager,
		upstream,
		urlResults,
		routedBody,
		chatReq.Stream,
		func(upstream *config.UpstreamConfig, failedKeys map[string]bool) (string, error) {
			if failedKeys[route.APIKey] {
				return "", fmt.Errorf("路由模型 %s 指定的 API Key 已失败", route.Alias)
			}
			return route.APIKey, nil
		},
		func(c *gin.Context, upstreamCopy *config.UpstreamConfig, apiKey string) (*http.Request, error) {
			return buildChatDirectRequest(c, upstreamCopy, apiKey, routedBody)
		},
		nil,
		func(url string) {
			channelScheduler.MarkURLFailure(scheduler.ChannelKindChat, route.ChannelIndex, url)
		},
		func(url string) {
			channelScheduler.MarkURLSuccess(scheduler.ChannelKindChat, route.ChannelIndex, url)
		},
		func(c *gin.Context, resp *http.Response, upstreamCopy *config.UpstreamConfig, apiKey string) (*types.Usage, error) {
			return handleSuccess(c, resp, envCfg, startTime, chatReq.Stream, routedBody)
		},
		common.AttemptLogContext{
			ChannelIndex:    route.ChannelIndex,
			Model:           chatReq.Model,
			ConversationID:  userID,
			LogStore:        channelScheduler.GetChannelLogStore(scheduler.ChannelKindChat),
			RequestLogStore: channelScheduler.GetRequestLogStore(),
		},
	)
	if handled {
		if successKey != "" {
			common.MarkConversationSuccess(channelScheduler, userID, scheduler.ChannelKindChat, route.ChannelIndex, route.ChannelName)
			channelScheduler.ConsumePromotionCount(route.ChannelIndex, scheduler.ChannelKindChat)
		} else if lastError != nil && !errors.Is(lastError, context.Canceled) {
			common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindChat, lastError)
		}
		return
	}

	log.Printf("[Chat-Route] 路由模型失败: alias=%s channel=%d key=%s", route.Alias, route.ChannelIndex, route.KeyID)
	common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindChat, lastError)
	common.HandleAllKeysFailed(c, cfgManager.GetFuzzyModeEnabled(), lastFailoverError, lastError, "Chat")
}

func chatRouteUpstream(cfgManager *config.ConfigManager, route modelcatalog.ChatRoute) (*config.UpstreamConfig, error) {
	cfg := cfgManager.GetConfig()
	if route.ChannelIndex < 0 || route.ChannelIndex >= len(cfg.ChatUpstream) {
		return nil, fmt.Errorf("路由模型 %s 指向的 Chat 渠道不存在", route.Alias)
	}

	upstream := cfg.ChatUpstream[route.ChannelIndex].Clone()
	if config.GetChannelStatus(upstream) != config.ChannelStatusActive || !config.IsChannelSchedulable(upstream) {
		return nil, fmt.Errorf("路由模型 %s 指向的 Chat 渠道不可用", route.Alias)
	}

	keyExists := false
	for _, apiKey := range upstream.APIKeys {
		if apiKey == route.APIKey {
			keyExists = true
			break
		}
	}
	if !keyExists {
		return nil, fmt.Errorf("路由模型 %s 指向的 API Key 已不存在", route.Alias)
	}

	upstream.BaseURL = route.BaseURL
	upstream.BaseURLs = nil
	upstream.APIKeys = []string{route.APIKey}
	upstream.ModelMapping = nil
	upstream.DefaultModel = ""
	return upstream, nil
}

func handleSingleChannel(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	bodyBytes []byte,
	chatReq types.OpenAIRequest,
	userID string,
	startTime time.Time,
) {
	upstream, channelIndex, err := cfgManager.GetCurrentChatUpstreamWithIndex()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "未配置任何 Chat 渠道，请先在管理界面添加渠道",
			"code":  "NO_CHAT_UPSTREAM",
		})
		return
	}

	handleSingleChannelWithUpstream(c, envCfg, cfgManager, channelScheduler, bodyBytes, chatReq, userID, upstream, channelIndex, startTime)
}

func handleSingleChannelWithUpstream(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	bodyBytes []byte,
	chatReq types.OpenAIRequest,
	userID string,
	upstream *config.UpstreamConfig,
	channelIndex int,
	startTime time.Time,
) {

	if len(upstream.APIKeys) == 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": fmt.Sprintf("当前 Chat 渠道 \"%s\" 未配置API密钥", upstream.Name),
			"code":  "NO_API_KEYS",
		})
		return
	}
	if err := channelScheduler.ValidateFixedChannel(userID, scheduler.ChannelKindChat, channelIndex); err != nil {
		common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindChat, err)
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "CONVERSATION_ROUTE_OVERRIDE"})
		return
	}

	metricsManager := channelScheduler.GetChatMetricsManager()
	urlResults := common.BuildDefaultURLResults(upstream.GetAllBaseURLs())

	handled, successKey, _, lastFailoverError, _, lastError := common.TryUpstreamWithAllKeys(
		c,
		envCfg,
		cfgManager,
		channelScheduler,
		scheduler.ChannelKindChat,
		"Chat",
		metricsManager,
		upstream,
		urlResults,
		bodyBytes,
		chatReq.Stream,
		func(upstream *config.UpstreamConfig, failedKeys map[string]bool) (string, error) {
			return cfgManager.GetNextChatAPIKey(upstream, failedKeys)
		},
		func(c *gin.Context, upstreamCopy *config.UpstreamConfig, apiKey string) (*http.Request, error) {
			return buildChatUpstreamRequest(c, upstreamCopy, apiKey, bodyBytes)
		},
		func(apiKey string) {
			if err := cfgManager.MoveChatAPIKeyToBottom(channelIndex, apiKey); err != nil {
				log.Printf("[Chat-Key] 警告: 密钥降级失败: %v", err)
			}
		},
		nil,
		nil,
		func(c *gin.Context, resp *http.Response, upstreamCopy *config.UpstreamConfig, apiKey string) (*types.Usage, error) {
			return handleSuccess(c, resp, envCfg, startTime, chatReq.Stream, bodyBytes)
		},
		common.AttemptLogContext{
			ChannelIndex:    channelIndex,
			Model:           chatReq.Model,
			ConversationID:  userID,
			LogStore:        channelScheduler.GetChannelLogStore(scheduler.ChannelKindChat),
			RequestLogStore: channelScheduler.GetRequestLogStore(),
		},
	)
	if handled {
		if successKey != "" {
			common.MarkConversationSuccess(channelScheduler, userID, scheduler.ChannelKindChat, channelIndex, upstream.Name)
		} else if lastError != nil && !errors.Is(lastError, context.Canceled) {
			common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindChat, lastError)
		}
		return
	}

	log.Printf("[Chat-Error] 所有 Chat API密钥都失败了")
	common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindChat, lastError)
	common.HandleAllKeysFailed(c, cfgManager.GetFuzzyModeEnabled(), lastFailoverError, lastError, "Chat")
}

func buildChatUpstreamRequest(c *gin.Context, upstream *config.UpstreamConfig, apiKey string, originalBody []byte) (*http.Request, error) {
	bodyBytes, err := applyChatModelMapping(originalBody, upstream)
	if err != nil {
		return nil, err
	}

	url := buildChatCompletionsURL(upstream.GetEffectiveBaseURL())
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("创建 Chat 请求失败: %w", err)
	}

	req.Header = utils.PrepareUpstreamHeaders(c, req.URL.Host)
	utils.SetAuthenticationHeader(req.Header, apiKey)

	return req, nil
}

func applyChatModelMapping(bodyBytes []byte, upstream *config.UpstreamConfig) ([]byte, error) {
	var payload map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("解析 Chat 请求体失败: %w", err)
	}

	model, ok := payload["model"].(string)
	if !ok || strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("model is required")
	}
	if mappedModel := config.ResolveUpstreamModel(model, upstream); strings.TrimSpace(mappedModel) != "" {
		payload["model"] = mappedModel
	}
	ensureChatStreamUsageOptions(payload)

	return utils.MarshalJSONNoEscape(payload)
}

func ensureChatStreamUsageOptions(payload map[string]interface{}) {
	stream, _ := payload["stream"].(bool)
	if !stream {
		return
	}
	options, ok := payload["stream_options"].(map[string]interface{})
	if !ok || options == nil {
		payload["stream_options"] = map[string]interface{}{"include_usage": true}
		return
	}
	if _, exists := options["include_usage"]; !exists {
		options["include_usage"] = true
	}
}

func buildChatCompletionsURL(baseURL string) string {
	skipVersionPrefix := strings.HasSuffix(baseURL, "#")
	if skipVersionPrefix {
		baseURL = strings.TrimSuffix(baseURL, "#")
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	endpoint := "/chat/completions"
	if !skipVersionPrefix && !chatVersionPattern.MatchString(baseURL) {
		endpoint = "/v1" + endpoint
	}
	return baseURL + endpoint
}

func handleSuccess(c *gin.Context, resp *http.Response, envCfg *config.EnvConfig, startTime time.Time, isStream bool, originalBody []byte) (*types.Usage, error) {
	defer resp.Body.Close()

	if isStream {
		return handleStreamSuccess(c, resp, envCfg, startTime)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read response"})
		return nil, err
	}
	bodyBytes = utils.DecompressGzipIfNeeded(resp, bodyBytes)

	if envCfg.EnableResponseLogs {
		responseTime := time.Since(startTime).Milliseconds()
		log.Printf("[Chat-Timing] Chat 响应完成: %dms, 状态: %d", responseTime, resp.StatusCode)
		if envCfg.IsDevelopment() {
			var formattedBody string
			if envCfg.RawLogOutput {
				formattedBody = utils.FormatJSONBytesRaw(bodyBytes)
			} else {
				formattedBody = utils.FormatJSONBytesForLog(bodyBytes, 500)
			}
			log.Printf("[Chat-Response] 响应体:\n%s", formattedBody)
		}
	}

	usage := extractChatUsage(bodyBytes)
	if usage != nil && usage.InputTokens == 0 && usage.PromptTokens > 0 {
		usage.InputTokens = usage.PromptTokens
	}
	if usage != nil && usage.OutputTokens == 0 && usage.CompletionTokens > 0 {
		usage.OutputTokens = usage.CompletionTokens
	}
	if usage == nil {
		usage = &types.Usage{InputTokens: utils.EstimateTokens(string(originalBody))}
	}

	utils.ForwardResponseHeaders(resp.Header, c.Writer)
	common.MarkRequestLogFirstToken(c)
	c.Data(resp.StatusCode, "application/json", bodyBytes)
	return usage, nil
}

func handleStreamSuccess(c *gin.Context, resp *http.Response, envCfg *config.EnvConfig, startTime time.Time) (*types.Usage, error) {
	if envCfg.EnableResponseLogs {
		responseTime := time.Since(startTime).Milliseconds()
		log.Printf("[Chat-Stream] Chat 流式响应开始: %dms, 状态: %d", responseTime, resp.StatusCode)
	}

	utils.ForwardResponseHeaders(resp.Header, c.Writer)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(resp.StatusCode)

	flusher, _ := c.Writer.(http.Flusher)
	scanner := bufio.NewScanner(resp.Body)
	const maxCapacity = 1024 * 1024
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxCapacity)
	var streamUsage *types.Usage

	for scanner.Scan() {
		line := scanner.Text()
		if usage := extractChatUsageFromSSELine(line); usage != nil {
			streamUsage = mergeChatUsage(streamUsage, usage)
		}
		common.MarkRequestLogFirstToken(c)
		if _, err := c.Writer.Write([]byte(line + "\n")); err != nil {
			return nil, err
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return streamUsage, nil
}

func buildChatDirectRequest(c *gin.Context, upstream *config.UpstreamConfig, apiKey string, bodyBytes []byte) (*http.Request, error) {
	url := buildChatCompletionsURL(upstream.GetEffectiveBaseURL())
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("创建 Chat 请求失败: %w", err)
	}

	req.Header = utils.PrepareUpstreamHeaders(c, req.URL.Host)
	utils.SetAuthenticationHeader(req.Header, apiKey)
	return req, nil
}

func replaceChatModel(bodyBytes []byte, model string) ([]byte, error) {
	var payload map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("解析 Chat 请求体失败: %w", err)
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("路由模型缺少上游模型名")
	}
	payload["model"] = model
	ensureChatStreamUsageOptions(payload)
	return utils.MarshalJSONNoEscape(payload)
}

func extractChatUsage(bodyBytes []byte) *types.Usage {
	var envelope struct {
		Usage *struct {
			PromptTokens       int `json:"prompt_tokens"`
			CompletionTokens   int `json:"completion_tokens"`
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			TotalTokens        int `json:"total_tokens"`
			PromptTokenDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			InputTokenDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil || envelope.Usage == nil {
		return nil
	}

	cacheReadTokens := envelope.Usage.PromptTokenDetails.CachedTokens
	if cacheReadTokens <= 0 {
		cacheReadTokens = envelope.Usage.InputTokenDetails.CachedTokens
	}
	inputTokens := envelope.Usage.PromptTokens
	if inputTokens <= 0 {
		inputTokens = envelope.Usage.InputTokens
	}
	outputTokens := envelope.Usage.CompletionTokens
	if outputTokens <= 0 {
		outputTokens = envelope.Usage.OutputTokens
	}

	return &types.Usage{
		InputTokens:          inputTokens,
		OutputTokens:         outputTokens,
		CacheReadInputTokens: cacheReadTokens,
		PromptTokens:         inputTokens,
		CompletionTokens:     outputTokens,
	}
}

func extractChatUsageFromSSELine(line string) *types.Usage {
	if !strings.HasPrefix(line, "data: ") {
		return nil
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
	if data == "" || data == "[DONE]" {
		return nil
	}
	return extractChatUsage([]byte(data))
}

func mergeChatUsage(current, next *types.Usage) *types.Usage {
	if next == nil {
		return current
	}
	if current == nil {
		return next
	}
	if next.InputTokens > 0 {
		current.InputTokens = next.InputTokens
	}
	if next.OutputTokens > 0 {
		current.OutputTokens = next.OutputTokens
	}
	if next.CacheReadInputTokens > 0 {
		current.CacheReadInputTokens = next.CacheReadInputTokens
	}
	if next.PromptTokens > 0 {
		current.PromptTokens = next.PromptTokens
	}
	if next.CompletionTokens > 0 {
		current.CompletionTokens = next.CompletionTokens
	}
	return current
}

func chatReqUserID(bodyBytes []byte) string {
	var req struct {
		User string `json:"user"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err == nil {
		return req.User
	}
	return ""
}

// ImagesHandler OpenAI Images API 代理处理器。
// Images 属于 OpenAI-compatible 直连能力，复用 Chat 渠道池和独立 Images 日志类型。
func ImagesHandler(envCfg *config.EnvConfig, cfgManager *config.ConfigManager, channelScheduler *scheduler.ChannelScheduler, endpoint string) gin.HandlerFunc {
	return gin.HandlerFunc(func(c *gin.Context) {
		middleware.ProxyAuthMiddleware(envCfg)(c)
		if c.IsAborted() {
			return
		}

		startTime := time.Now()
		bodyBytes, err := common.ReadRequestBody(c, envCfg.MaxRequestBodySize)
		if err != nil {
			return
		}

		model := extractImagesModel(c.GetHeader("Content-Type"), bodyBytes)
		prompts := common.ExtractPromptJSONFieldPrompts(bodyBytes, "prompt")
		userID := common.ObserveConversationPrompts(channelScheduler, scheduler.ChannelKindChat, common.ExtractConversationID(c, bodyBytes), model, prompts, false)
		defer common.MarkConversationComplete(channelScheduler, userID, scheduler.ChannelKindChat)

		common.LogOriginalRequest(c, bodyBytes, envCfg, "Images")

		if channelScheduler.IsMultiChannelMode(scheduler.ChannelKindChat) {
			handleImagesMultiChannel(c, envCfg, cfgManager, channelScheduler, endpoint, bodyBytes, model, userID, startTime)
			return
		}

		handleImagesSingleChannel(c, envCfg, cfgManager, channelScheduler, endpoint, bodyBytes, model, userID, startTime)
	})
}

func handleImagesMultiChannel(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	endpoint string,
	bodyBytes []byte,
	model string,
	userID string,
	startTime time.Time,
) {
	metricsManager := channelScheduler.GetChatMetricsManager()

	common.HandleMultiChannelFailover(
		c,
		envCfg,
		channelScheduler,
		scheduler.ChannelKindChat,
		"Images",
		userID,
		false,
		func(selection *scheduler.SelectionResult) common.MultiChannelAttemptResult {
			upstream := selection.Upstream
			channelIndex := selection.ChannelIndex
			if upstream == nil {
				return common.MultiChannelAttemptResult{}
			}

			sortedURLResults := channelScheduler.GetSortedURLsForChannel(scheduler.ChannelKindChat, channelIndex, upstream.GetAllBaseURLs())
			handled, successKey, successBaseURLIdx, failoverErr, usage, lastErr := common.TryUpstreamWithAllKeys(
				c,
				envCfg,
				cfgManager,
				channelScheduler,
				scheduler.ChannelKindChat,
				"Images",
				metricsManager,
				upstream,
				sortedURLResults,
				bodyBytes,
				false,
				func(upstream *config.UpstreamConfig, failedKeys map[string]bool) (string, error) {
					return cfgManager.GetNextChatAPIKey(upstream, failedKeys)
				},
				func(c *gin.Context, upstreamCopy *config.UpstreamConfig, apiKey string) (*http.Request, error) {
					return buildImagesUpstreamRequest(c, upstreamCopy, apiKey, endpoint, bodyBytes)
				},
				func(apiKey string) {
					if err := cfgManager.MoveChatAPIKeyToBottom(channelIndex, apiKey); err != nil {
						log.Printf("[Images-Key] 警告: 密钥降级失败: %v", err)
					}
				},
				func(url string) {
					channelScheduler.MarkURLFailure(scheduler.ChannelKindChat, channelIndex, url)
				},
				func(url string) {
					channelScheduler.MarkURLSuccess(scheduler.ChannelKindChat, channelIndex, url)
				},
				func(c *gin.Context, resp *http.Response, upstreamCopy *config.UpstreamConfig, apiKey string) (*types.Usage, error) {
					return handleImagesSuccess(c, resp, envCfg, startTime)
				},
				common.AttemptLogContext{
					ChannelIndex:    channelIndex,
					Model:           model,
					ConversationID:  userID,
					LogStore:        channelScheduler.GetChannelLogStore(scheduler.ChannelKindChat),
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
				common.MarkConversationSuccess(channelScheduler, userID, scheduler.ChannelKindChat, selection.ChannelIndex, selection.Upstream.Name)
				return
			}
			if result.LastError != nil && !errors.Is(result.LastError, context.Canceled) {
				common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindChat, result.LastError)
			}
		},
		func(ctx *gin.Context, failoverErr *common.FailoverError, lastError error) {
			common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindChat, lastError)
			common.HandleAllChannelsFailed(ctx, cfgManager.GetFuzzyModeEnabled(), failoverErr, lastError, "Images")
		},
	)
}

func handleImagesSingleChannel(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	endpoint string,
	bodyBytes []byte,
	model string,
	userID string,
	startTime time.Time,
) {
	upstream, channelIndex, err := cfgManager.GetCurrentChatUpstreamWithIndex()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "未配置任何 Chat 渠道，请先在管理界面添加渠道", "code": "NO_CHAT_UPSTREAM"})
		return
	}
	if len(upstream.APIKeys) == 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": fmt.Sprintf("当前 Chat 渠道 \"%s\" 未配置API密钥", upstream.Name), "code": "NO_API_KEYS"})
		return
	}
	if err := channelScheduler.ValidateFixedChannel(userID, scheduler.ChannelKindChat, channelIndex); err != nil {
		common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindChat, err)
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "CONVERSATION_ROUTE_OVERRIDE"})
		return
	}

	handled, successKey, _, lastFailoverError, _, lastError := common.TryUpstreamWithAllKeys(
		c,
		envCfg,
		cfgManager,
		channelScheduler,
		scheduler.ChannelKindChat,
		"Images",
		channelScheduler.GetChatMetricsManager(),
		upstream,
		common.BuildDefaultURLResults(upstream.GetAllBaseURLs()),
		bodyBytes,
		false,
		func(upstream *config.UpstreamConfig, failedKeys map[string]bool) (string, error) {
			return cfgManager.GetNextChatAPIKey(upstream, failedKeys)
		},
		func(c *gin.Context, upstreamCopy *config.UpstreamConfig, apiKey string) (*http.Request, error) {
			return buildImagesUpstreamRequest(c, upstreamCopy, apiKey, endpoint, bodyBytes)
		},
		func(apiKey string) {
			if err := cfgManager.MoveChatAPIKeyToBottom(channelIndex, apiKey); err != nil {
				log.Printf("[Images-Key] 警告: 密钥降级失败: %v", err)
			}
		},
		nil,
		nil,
		func(c *gin.Context, resp *http.Response, upstreamCopy *config.UpstreamConfig, apiKey string) (*types.Usage, error) {
			return handleImagesSuccess(c, resp, envCfg, startTime)
		},
		common.AttemptLogContext{
			ChannelIndex:    channelIndex,
			Model:           model,
			ConversationID:  userID,
			LogStore:        channelScheduler.GetChannelLogStore(scheduler.ChannelKindChat),
			RequestLogStore: channelScheduler.GetRequestLogStore(),
		},
	)
	if handled {
		if successKey != "" {
			common.MarkConversationSuccess(channelScheduler, userID, scheduler.ChannelKindChat, channelIndex, upstream.Name)
		} else if lastError != nil && !errors.Is(lastError, context.Canceled) {
			common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindChat, lastError)
		}
		return
	}

	log.Printf("[Images-Error] 所有 Images API密钥都失败了")
	common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindChat, lastError)
	common.HandleAllKeysFailed(c, cfgManager.GetFuzzyModeEnabled(), lastFailoverError, lastError, "Images")
}

func buildImagesUpstreamRequest(c *gin.Context, upstream *config.UpstreamConfig, apiKey string, endpoint string, originalBody []byte) (*http.Request, error) {
	bodyBytes, contentType, err := applyImagesModelMapping(c.GetHeader("Content-Type"), originalBody, upstream)
	if err != nil {
		return nil, err
	}

	url := buildOpenAIEndpointURL(upstream.GetEffectiveBaseURL(), endpoint)
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("创建 Images 请求失败: %w", err)
	}

	req.Header = utils.PrepareUpstreamHeaders(c, req.URL.Host)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	utils.SetAuthenticationHeader(req.Header, apiKey)
	return req, nil
}

func applyImagesModelMapping(contentType string, bodyBytes []byte, upstream *config.UpstreamConfig) ([]byte, string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return bodyBytes, contentType, nil
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		mappedBody, mappedContentType, err := applyImagesMultipartModelMapping(params["boundary"], bodyBytes, upstream)
		if err != nil {
			return nil, "", err
		}
		return mappedBody, mappedContentType, nil
	}

	if !strings.Contains(mediaType, "json") {
		return bodyBytes, contentType, nil
	}

	var payload map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, "", fmt.Errorf("解析 Images 请求体失败: %w", err)
	}

	if model, ok := payload["model"].(string); ok && strings.TrimSpace(model) != "" {
		payload["model"] = config.ResolveUpstreamModel(model, upstream)
	}

	mappedBody, err := utils.MarshalJSONNoEscape(payload)
	if err != nil {
		return nil, "", err
	}
	return mappedBody, contentType, nil
}

func applyImagesMultipartModelMapping(boundary string, bodyBytes []byte, upstream *config.UpstreamConfig) ([]byte, string, error) {
	if boundary == "" {
		return bodyBytes, "", nil
	}

	reader := multipart.NewReader(bytes.NewReader(bodyBytes), boundary)
	var out bytes.Buffer
	writer := multipart.NewWriter(&out)

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("解析 Images multipart 请求体失败: %w", err)
		}

		partBody, err := io.ReadAll(part)
		if closeErr := part.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		if err != nil {
			writer.Close()
			return nil, "", fmt.Errorf("读取 Images multipart 字段失败: %w", err)
		}

		header := cloneMIMEHeader(part.Header)
		if part.FormName() == "model" {
			model := strings.TrimSpace(string(partBody))
			if model != "" {
				partBody = []byte(config.ResolveUpstreamModel(model, upstream))
			}
		}

		outPart, err := writer.CreatePart(header)
		if err != nil {
			writer.Close()
			return nil, "", fmt.Errorf("重建 Images multipart 字段失败: %w", err)
		}
		if _, err := outPart.Write(partBody); err != nil {
			writer.Close()
			return nil, "", fmt.Errorf("写入 Images multipart 字段失败: %w", err)
		}
	}

	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("完成 Images multipart 请求体失败: %w", err)
	}

	return out.Bytes(), writer.FormDataContentType(), nil
}

func cloneMIMEHeader(src textproto.MIMEHeader) textproto.MIMEHeader {
	dst := make(textproto.MIMEHeader, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func buildOpenAIEndpointURL(baseURL string, endpoint string) string {
	endpoint = "/" + strings.TrimLeft(endpoint, "/")
	skipVersionPrefix := strings.HasSuffix(baseURL, "#")
	if skipVersionPrefix {
		baseURL = strings.TrimSuffix(baseURL, "#")
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	if !skipVersionPrefix && !chatVersionPattern.MatchString(baseURL) {
		endpoint = "/v1" + endpoint
	}
	return baseURL + endpoint
}

func handleImagesSuccess(c *gin.Context, resp *http.Response, envCfg *config.EnvConfig, startTime time.Time) (*types.Usage, error) {
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read response"})
		return nil, err
	}
	bodyBytes = utils.DecompressGzipIfNeeded(resp, bodyBytes)

	if envCfg.EnableResponseLogs {
		responseTime := time.Since(startTime).Milliseconds()
		log.Printf("[Images-Timing] Images 响应完成: %dms, 状态: %d", responseTime, resp.StatusCode)
		if envCfg.IsDevelopment() {
			var formattedBody string
			if envCfg.RawLogOutput {
				formattedBody = utils.FormatJSONBytesRaw(bodyBytes)
			} else {
				formattedBody = utils.FormatJSONBytesForLog(bodyBytes, 500)
			}
			log.Printf("[Images-Response] 响应体:\n%s", formattedBody)
		}
	}

	utils.ForwardResponseHeaders(resp.Header, c.Writer)
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	common.MarkRequestLogFirstToken(c)
	c.Data(resp.StatusCode, contentType, bodyBytes)
	return nil, nil
}

func extractImagesModel(contentType string, bodyBytes []byte) string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}

	if strings.Contains(mediaType, "json") {
		var req struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(bodyBytes, &req); err == nil {
			return req.Model
		}
		return ""
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return ""
		}
		reader := multipart.NewReader(bytes.NewReader(bodyBytes), boundary)
		for {
			part, err := reader.NextPart()
			if err != nil {
				return ""
			}
			if part.FormName() != "model" {
				part.Close()
				continue
			}
			modelBytes, err := io.ReadAll(io.LimitReader(part, 1024))
			part.Close()
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(modelBytes))
		}
	}

	return ""
}
