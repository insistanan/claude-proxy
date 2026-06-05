// Package chat 提供 OpenAI Chat Completions API 的代理处理器
package chat

import (
	"bufio"
	"bytes"
	"encoding/json"
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
		source := common.DetectRequestSource(c, bodyBytes, userID)

		common.LogOriginalRequest(c, bodyBytes, envCfg, "Chat")

		if channelScheduler.IsMultiChannelMode(scheduler.ChannelKindChat) {
			handleMultiChannel(c, envCfg, cfgManager, channelScheduler, bodyBytes, chatReq, userID, source, hasImage, startTime)
			return
		}

		handleSingleChannel(c, envCfg, cfgManager, channelScheduler, bodyBytes, chatReq, source, startTime)
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
	source common.RequestSource,
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
					ChannelIndex: channelIndex,
					Model:        chatReq.Model,
					Source:       source,
					LogStore:     channelScheduler.GetChannelLogStore(scheduler.ChannelKindChat),
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
		nil,
		func(ctx *gin.Context, failoverErr *common.FailoverError, lastError error) {
			common.HandleAllChannelsFailed(ctx, cfgManager.GetFuzzyModeEnabled(), failoverErr, lastError, "Chat")
		},
	)
}

func handleSingleChannel(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	bodyBytes []byte,
	chatReq types.OpenAIRequest,
	source common.RequestSource,
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

	if len(upstream.APIKeys) == 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": fmt.Sprintf("当前 Chat 渠道 \"%s\" 未配置API密钥", upstream.Name),
			"code":  "NO_API_KEYS",
		})
		return
	}

	metricsManager := channelScheduler.GetChatMetricsManager()
	urlResults := common.BuildDefaultURLResults(upstream.GetAllBaseURLs())

	handled, _, _, lastFailoverError, _, lastError := common.TryUpstreamWithAllKeys(
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
			ChannelIndex: channelIndex,
			Model:        chatReq.Model,
			Source:       source,
			LogStore:     channelScheduler.GetChannelLogStore(scheduler.ChannelKindChat),
		},
	)
	if handled {
		return
	}

	log.Printf("[Chat-Error] 所有 Chat API密钥都失败了")
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
	payload["model"] = config.RedirectModel(model, upstream)

	return utils.MarshalJSONNoEscape(payload)
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

	for scanner.Scan() {
		line := scanner.Text()
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
	return nil, nil
}

func extractChatUsage(bodyBytes []byte) *types.Usage {
	var envelope struct {
		Usage *struct {
			PromptTokens       int `json:"prompt_tokens"`
			CompletionTokens   int `json:"completion_tokens"`
			TotalTokens        int `json:"total_tokens"`
			PromptTokenDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil || envelope.Usage == nil {
		return nil
	}

	return &types.Usage{
		InputTokens:          envelope.Usage.PromptTokens,
		OutputTokens:         envelope.Usage.CompletionTokens,
		CacheReadInputTokens: envelope.Usage.PromptTokenDetails.CachedTokens,
		PromptTokens:         envelope.Usage.PromptTokens,
		CompletionTokens:     envelope.Usage.CompletionTokens,
	}
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
		userID := common.ExtractConversationID(c, bodyBytes)
		source := common.DetectRequestSource(c, bodyBytes, userID)

		common.LogOriginalRequest(c, bodyBytes, envCfg, "Images")

		if channelScheduler.IsMultiChannelMode(scheduler.ChannelKindChat) {
			handleImagesMultiChannel(c, envCfg, cfgManager, channelScheduler, endpoint, bodyBytes, model, userID, source, startTime)
			return
		}

		handleImagesSingleChannel(c, envCfg, cfgManager, channelScheduler, endpoint, bodyBytes, model, source, startTime)
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
	source common.RequestSource,
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
					ChannelIndex: channelIndex,
					Model:        model,
					Source:       source,
					LogStore:     channelScheduler.GetChannelLogStore(scheduler.ChannelKindChat),
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
		nil,
		func(ctx *gin.Context, failoverErr *common.FailoverError, lastError error) {
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
	source common.RequestSource,
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

	handled, _, _, lastFailoverError, _, lastError := common.TryUpstreamWithAllKeys(
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
			ChannelIndex: channelIndex,
			Model:        model,
			Source:       source,
			LogStore:     channelScheduler.GetChannelLogStore(scheduler.ChannelKindChat),
		},
	)
	if handled {
		return
	}

	log.Printf("[Images-Error] 所有 Images API密钥都失败了")
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
		payload["model"] = config.RedirectModel(model, upstream)
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
				partBody = []byte(config.RedirectModel(model, upstream))
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
