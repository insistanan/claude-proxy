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
	"net/http"
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

const chatToolSearchProxyName = "tool_search"
const chatWebSearchProxyName = "web_search"

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
		userID := common.ExtractConversationID(c, bodyBytes)
		prompts := common.ExtractPromptsFromOpenAI(chatReq.Messages)
		userID = common.ObserveConversationPrompts(channelScheduler, scheduler.ChannelKindChat, userID, chatReq.Model, prompts, utils.ExtractImageFingerprints(bodyBytes), chatReq.Stream)
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
		chatReq.Model,
		hasImage,
		func(selection *scheduler.SelectionResult) common.MultiChannelAttemptResult {
			upstream := selection.Upstream
			channelIndex := selection.ChannelIndex
			if upstream == nil {
				return common.MultiChannelAttemptResult{}
			}

			baseURLs := upstream.GetAllBaseURLs()
			sortedURLResults := channelScheduler.GetSortedURLsForChannel(scheduler.ChannelKindChat, channelIndex, baseURLs)

			handled, successKey, successBaseURLIdx, failoverErr, usage, lastErr := common.TryUpstreamWithModelMappingFailover(
				c,
				envCfg,
				cfgManager,
				channelScheduler,
				scheduler.ChannelKindChat,
				"Chat",
				metricsManager,
				upstream,
				chatReq.Model, // 传入客户端请求的原始模型
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
	handled, successKey, _, lastFailoverError, _, lastError := common.TryUpstreamWithModelMappingFailover(
		c,
		envCfg,
		cfgManager,
		channelScheduler,
		scheduler.ChannelKindChat,
		"Chat",
		metricsManager,
		upstream,
		chatReq.Model, // 添加 requestedModel 参数
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

	handled, successKey, _, lastFailoverError, _, lastError := common.TryUpstreamWithModelMappingFailover(
		c,
		envCfg,
		cfgManager,
		channelScheduler,
		scheduler.ChannelKindChat,
		"Chat",
		metricsManager,
		upstream,
		chatReq.Model,
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
			channelScheduler.ConsumePromotionCount(channelIndex, scheduler.ChannelKindChat)
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
	originalBody = common.PreparedRequestBody(c, originalBody)
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
	if err := sanitizeOpenAIChatPayloadForUpstream(payload); err != nil {
		return nil, err
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
	reader := bufio.NewReaderSize(resp.Body, 64*1024)
	var streamUsage *types.Usage

	for {
		rawLine, readErr := reader.ReadBytes('\n')
		if len(rawLine) == 0 && readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, readErr
		}

		line := strings.TrimSuffix(strings.TrimSuffix(string(rawLine), "\n"), "\r")
		if usage := extractChatUsageFromSSELine(line); usage != nil {
			streamUsage = mergeChatUsage(streamUsage, usage)
		}
		common.MarkRequestLogFirstToken(c)
		if _, err := c.Writer.Write(rawLine); err != nil {
			return nil, err
		}
		if flusher != nil {
			flusher.Flush()
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, readErr
		}
	}
	return streamUsage, nil
}

func buildChatDirectRequest(c *gin.Context, upstream *config.UpstreamConfig, apiKey string, bodyBytes []byte) (*http.Request, error) {
	bodyBytes = common.PreparedRequestBody(c, bodyBytes)
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
	if err := sanitizeOpenAIChatPayloadForUpstream(payload); err != nil {
		return nil, err
	}
	ensureChatStreamUsageOptions(payload)
	return utils.MarshalJSONNoEscape(payload)
}

func sanitizeOpenAIChatPayloadForUpstream(payload map[string]interface{}) error {
	if err := sanitizeOpenAIChatMessagesForUpstream(payload); err != nil {
		return err
	}

	toolsRaw, hasToolsField := payload["tools"]
	tools, err := sanitizeOpenAIChatTools(toolsRaw)
	if err != nil {
		return err
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	} else {
		delete(payload, "tools")
	}

	toolChoice, ok, err := sanitizeOpenAIChatToolChoice(payload["tool_choice"])
	if err != nil {
		return err
	}
	if len(tools) > 0 && ok {
		payload["tool_choice"] = toolChoice
	} else {
		delete(payload, "tool_choice")
	}

	if len(tools) == 0 {
		delete(payload, "parallel_tool_calls")
	} else if hasToolsField {
		if _, exists := payload["parallel_tool_calls"]; exists {
			if b, ok := payload["parallel_tool_calls"].(bool); ok {
				payload["parallel_tool_calls"] = b
			}
		}
	}
	return nil
}

func sanitizeOpenAIChatMessagesForUpstream(payload map[string]interface{}) error {
	rawMessages, exists := payload["messages"]
	if !exists || rawMessages == nil {
		return nil
	}

	items, ok := rawMessages.([]interface{})
	if !ok {
		return fmt.Errorf("Chat messages 必须是数组")
	}

	for i, item := range items {
		message, ok := item.(map[string]interface{})
		if !ok {
			return fmt.Errorf("Chat messages 第 %d 项必须是对象", i)
		}
		role, _ := message["role"].(string)
		message["role"] = normalizeOpenAIChatRoleForUpstream(role)
	}
	return nil
}

func normalizeOpenAIChatRoleForUpstream(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "developer":
		return "system"
	case "system", "user", "assistant", "tool":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return "user"
	}
}

func sanitizeOpenAIChatTools(raw interface{}) ([]map[string]interface{}, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("Chat tools 必须是数组")
	}

	out := make([]map[string]interface{}, 0, len(items))
	for i, item := range items {
		tool, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("Chat tools 第 %d 项必须是对象", i)
		}
		normalized, err := sanitizeOpenAIChatTool(tool, i)
		if err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	return out, nil
}

func sanitizeOpenAIChatTool(tool map[string]interface{}, index int) (map[string]interface{}, error) {
	toolType, _ := tool["type"].(string)
	toolType = strings.TrimSpace(toolType)
	switch toolType {
	case "", "function":
		function := map[string]interface{}{}
		if nested, ok := tool["function"].(map[string]interface{}); ok {
			for k, v := range nested {
				function[k] = v
			}
		} else {
			copyToolField(function, tool, "name")
			copyToolField(function, tool, "description")
			copyToolField(function, tool, "parameters")
			copyToolField(function, tool, "strict")
		}
		name, _ := function["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("Chat tool 第 %d 项缺少 function.name", index)
		}
		if _, ok := function["parameters"]; !ok || function["parameters"] == nil {
			function["parameters"] = emptyOpenAIChatToolParameters()
		}
		return map[string]interface{}{
			"type":     "function",
			"function": function,
		}, nil
	case "custom":
		name, _ := tool["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("Chat tool 第 %d 项缺少 function.name", index)
		}
		return map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        name,
				"description": customChatToolDescription(tool),
				"parameters":  customChatToolParameters(tool),
			},
		}, nil
	case "web_search", "web_search_preview":
		return buildChatWebSearchTool(tool), nil
	default:
		return nil, fmt.Errorf("OpenAI Chat 不支持第 %d 个 tool type %q", index, toolType)
	}
}

func sanitizeOpenAIChatToolChoice(raw interface{}) (interface{}, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	if choice, ok := raw.(string); ok {
		switch choice {
		case "auto", "none", "required":
			return choice, true, nil
		default:
			return nil, false, fmt.Errorf("OpenAI Chat 不支持 tool_choice=%q", choice)
		}
	}

	obj, ok := raw.(map[string]interface{})
	if !ok {
		return nil, false, fmt.Errorf("Chat tool_choice 必须是字符串或对象")
	}

	choiceType, _ := obj["type"].(string)
	choiceType = strings.TrimSpace(choiceType)
	if choiceType == "" {
		choiceType = "function"
	}
	switch choiceType {
	case "function", "custom":
		name := extractNamedToolChoice(obj)
		if name == "" {
			return nil, false, fmt.Errorf("OpenAI Chat tool_choice 对象缺少 function.name 或 name")
		}
		return map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name": name,
			},
		}, true, nil
	case "tool_search":
		return map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name": chatToolSearchProxyName,
			},
		}, true, nil
	case "web_search", "web_search_preview":
		return map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name": chatWebSearchProxyName,
			},
		}, true, nil
	case "auto", "none", "required":
		return choiceType, true, nil
	default:
		return nil, false, fmt.Errorf("OpenAI Chat 不支持 tool_choice.type=%q", choiceType)
	}
}

func copyToolField(dst, src map[string]interface{}, key string) {
	if value, ok := src[key]; ok {
		dst[key] = value
	}
}

func extractNamedToolChoice(raw map[string]interface{}) string {
	name, _ := raw["name"].(string)
	if strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	if fn, ok := raw["function"].(map[string]interface{}); ok {
		if nested, _ := fn["name"].(string); strings.TrimSpace(nested) != "" {
			return strings.TrimSpace(nested)
		}
	}
	if tool, ok := raw["tool"].(map[string]interface{}); ok {
		if nested, _ := tool["name"].(string); strings.TrimSpace(nested) != "" {
			return strings.TrimSpace(nested)
		}
	}
	return ""
}

func customChatToolParameters(tool map[string]interface{}) interface{} {
	if parameters, ok := tool["parameters"]; ok && parameters != nil {
		return parameters
	}
	if inputSchema, ok := tool["input_schema"]; ok && inputSchema != nil {
		return inputSchema
	}
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"input": map[string]interface{}{
				"type": "string",
			},
		},
		"required":             []string{"input"},
		"additionalProperties": false,
	}
}

func customChatToolDescription(tool map[string]interface{}) string {
	description, _ := tool["description"].(string)
	metadata, err := json.Marshal(tool)
	if err != nil {
		return description
	}
	metadataText := strings.TrimSpace(string(metadata))
	if metadataText == "" || metadataText == "{}" {
		return description
	}
	if strings.TrimSpace(description) == "" {
		return "Original tool definition:\n" + metadataText
	}
	return description + "\n\nOriginal tool definition:\n" + metadataText
}

func buildChatWebSearchTool(tool map[string]interface{}) map[string]interface{} {
	description, _ := tool["description"].(string)
	if strings.TrimSpace(description) == "" {
		description = "Search the web for relevant information."
	}

	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query to run on the web.",
			},
		},
		"required": []string{"query"},
	}
	if rawParameters, ok := tool["parameters"].(map[string]interface{}); ok && rawParameters != nil {
		parameters = rawParameters
	}

	return map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        chatWebSearchProxyName,
			"description": description,
			"parameters":  parameters,
		},
	}
}

func emptyOpenAIChatToolParameters() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
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
	if cacheReadTokens > 0 && inputTokens > cacheReadTokens {
		inputTokens -= cacheReadTokens
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
