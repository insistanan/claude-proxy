package images

import (
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
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/gin-gonic/gin"
)

var chatVersionPattern = regexp.MustCompile(`/v\d+[a-z]*$`)

func Handler(envCfg *config.EnvConfig, cfgManager *config.ConfigManager, channelScheduler *scheduler.ChannelScheduler, endpoint string) gin.HandlerFunc {
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
		userID := common.ObserveConversationPrompts(channelScheduler, scheduler.ChannelKindImages, common.ExtractConversationID(c, bodyBytes), model, prompts, false)
		defer common.MarkConversationComplete(channelScheduler, userID, scheduler.ChannelKindImages)

		common.LogOriginalRequest(c, bodyBytes, envCfg, "Images")

		if channelScheduler.IsMultiChannelMode(scheduler.ChannelKindImages) {
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
	metricsManager := channelScheduler.GetImagesMetricsManager()

	common.HandleMultiChannelFailover(
		c,
		envCfg,
		channelScheduler,
		scheduler.ChannelKindImages,
		"Images",
		userID,
		model,
		false,
		func(selection *scheduler.SelectionResult) common.MultiChannelAttemptResult {
			upstream := selection.Upstream
			channelIndex := selection.ChannelIndex
			if upstream == nil {
				return common.MultiChannelAttemptResult{}
			}

			sortedURLResults := channelScheduler.GetSortedURLsForChannel(scheduler.ChannelKindImages, channelIndex, upstream.GetAllBaseURLs())
			handled, successKey, successBaseURLIdx, failoverErr, usage, lastErr := common.TryUpstreamWithModelMappingFailover(
				c,
				envCfg,
				cfgManager,
				channelScheduler,
				scheduler.ChannelKindImages,
				"Images",
				metricsManager,
				upstream,
				model,
				sortedURLResults,
				bodyBytes,
				false,
				func(upstream *config.UpstreamConfig, failedKeys map[string]bool) (string, error) {
					return cfgManager.GetNextImagesAPIKey(upstream, failedKeys)
				},
				func(c *gin.Context, upstreamCopy *config.UpstreamConfig, apiKey string) (*http.Request, error) {
					return buildImagesUpstreamRequest(c, upstreamCopy, apiKey, endpoint, bodyBytes)
				},
				func(apiKey string) {
					if err := cfgManager.MoveImagesAPIKeyToBottom(channelIndex, apiKey); err != nil {
						log.Printf("[Images-Key] 警告: 密钥降级失败: %v", err)
					}
				},
				func(url string) {
					channelScheduler.MarkURLFailure(scheduler.ChannelKindImages, channelIndex, url)
				},
				func(url string) {
					channelScheduler.MarkURLSuccess(scheduler.ChannelKindImages, channelIndex, url)
				},
				func(c *gin.Context, resp *http.Response, upstreamCopy *config.UpstreamConfig, apiKey string) (*types.Usage, error) {
					return handleImagesSuccess(c, resp, envCfg, startTime)
				},
				common.AttemptLogContext{
					ChannelIndex:    channelIndex,
					Model:           model,
					ConversationID:  userID,
					LogStore:        channelScheduler.GetChannelLogStore(scheduler.ChannelKindImages),
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
				common.MarkConversationSuccess(channelScheduler, userID, scheduler.ChannelKindImages, selection.ChannelIndex, selection.Upstream.Name)
				return
			}
			if result.LastError != nil && !errors.Is(result.LastError, context.Canceled) {
				common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindImages, result.LastError)
			}
		},
		func(ctx *gin.Context, failoverErr *common.FailoverError, lastError error) {
			common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindImages, lastError)
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
	upstream, channelIndex, err := cfgManager.GetCurrentImagesUpstreamWithIndex()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "未配置任何 Images 渠道，请先在管理界面添加渠道", "code": "NO_IMAGES_UPSTREAM"})
		return
	}
	if len(upstream.APIKeys) == 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": fmt.Sprintf("当前 Images 渠道 \"%s\" 未配置API密钥", upstream.Name), "code": "NO_API_KEYS"})
		return
	}
	if err := channelScheduler.ValidateFixedChannel(userID, scheduler.ChannelKindImages, channelIndex); err != nil {
		common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindImages, err)
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "CONVERSATION_ROUTE_OVERRIDE"})
		return
	}

	handled, successKey, _, lastFailoverError, _, lastError := common.TryUpstreamWithModelMappingFailover(
		c,
		envCfg,
		cfgManager,
		channelScheduler,
		scheduler.ChannelKindImages,
		"Images",
		channelScheduler.GetImagesMetricsManager(),
		upstream,
		model,
		common.BuildDefaultURLResults(upstream.GetAllBaseURLs()),
		bodyBytes,
		false,
		func(upstream *config.UpstreamConfig, failedKeys map[string]bool) (string, error) {
			return cfgManager.GetNextImagesAPIKey(upstream, failedKeys)
		},
		func(c *gin.Context, upstreamCopy *config.UpstreamConfig, apiKey string) (*http.Request, error) {
			return buildImagesUpstreamRequest(c, upstreamCopy, apiKey, endpoint, bodyBytes)
		},
		func(apiKey string) {
			if err := cfgManager.MoveImagesAPIKeyToBottom(channelIndex, apiKey); err != nil {
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
			LogStore:        channelScheduler.GetChannelLogStore(scheduler.ChannelKindImages),
			RequestLogStore: channelScheduler.GetRequestLogStore(),
		},
	)
	if handled {
		if successKey != "" {
			common.MarkConversationSuccess(channelScheduler, userID, scheduler.ChannelKindImages, channelIndex, upstream.Name)
		} else if lastError != nil && !errors.Is(lastError, context.Canceled) {
			common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindImages, lastError)
		}
		return
	}

	log.Printf("[Images-Error] 所有 Images API密钥都失败了")
	common.MarkConversationFailure(channelScheduler, userID, scheduler.ChannelKindImages, lastError)
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