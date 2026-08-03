package images

import (
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
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/gin-gonic/gin"
)

var chatVersionPattern = regexp.MustCompile(`/v\d+[a-z]*$`)

// Handler Images API 代理处理器
// 使用通用 RunProxyRequest 骨架，通过 ProtocolSpec 注入协议特有逻辑。
func Handler(envCfg *config.EnvConfig, cfgManager *config.ConfigManager, channelScheduler *scheduler.ChannelScheduler, endpoint string) gin.HandlerFunc {
	spec := common.ProtocolSpec{
		Kind:    scheduler.ChannelKindImages,
		LogName: "Images",
		PreRoute: nil,
		ParseRequest: func(c *gin.Context, body []byte) (string, bool, []string, bool) {
			requestMeta := extractImagesRequestMetadata(c.GetHeader("Content-Type"), body)
			prompts := common.ExtractPromptJSONFieldPrompts(body, "prompt")
			return requestMeta.Model, requestMeta.Stream, prompts, true
		},
		BuildUpstreamRequest: func(c *gin.Context, up *config.UpstreamConfig, apiKey string, body []byte) (*http.Request, error) {
			return buildImagesUpstreamRequest(c, up, apiKey, endpoint, body)
		},
		HandleSuccess: func(c *gin.Context, resp *http.Response, up *config.UpstreamConfig, apiKey string, body []byte, startTime time.Time) (*types.Usage, error) {
			return handleImagesSuccess(c, resp, envCfg, startTime, false)
		},
	}
	return func(c *gin.Context) {
		common.RunProxyRequest(c, envCfg, cfgManager, channelScheduler, spec)
	}
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
		metadata := extractImagesRequestMetadata(contentType, bodyBytes)
		if strings.TrimSpace(metadata.Model) == "" || config.ResolveUpstreamModel(metadata.Model, upstream) == metadata.Model {
			return bodyBytes, contentType, nil
		}
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
		mappedModel := config.ResolveUpstreamModel(model, upstream)
		if mappedModel == model {
			return bodyBytes, contentType, nil
		}
		payload["model"] = mappedModel
	} else {
		return bodyBytes, contentType, nil
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

func handleImagesSuccess(c *gin.Context, resp *http.Response, envCfg *config.EnvConfig, startTime time.Time, requestedStream bool) (*types.Usage, error) {
	defer resp.Body.Close()

	isStream := requestedStream || common.IsEventStreamResponse(resp)
	if envCfg.EnableResponseLogs {
		responseTime := time.Since(startTime).Milliseconds()
		if isStream {
			log.Printf("[Images-Stream] Images 流式响应开始: %dms, 状态: %d", responseTime, resp.StatusCode)
		} else {
			log.Printf("[Images-Timing] Images 响应开始转发: %dms, 状态: %d", responseTime, resp.StatusCode)
		}
	}

	err := common.ForwardUpstreamResponseBody(c, resp, "application/json", isStream)
	if envCfg.EnableResponseLogs {
		responseTime := time.Since(startTime).Milliseconds()
		if isStream {
			log.Printf("[Images-Stream] Images 流式响应完成: %dms", responseTime)
		} else {
			log.Printf("[Images-Timing] Images 响应转发完成: %dms", responseTime)
		}
	}
	return nil, err
}

type imagesRequestMetadata struct {
	Model  string
	Stream bool
}

func extractImagesRequestMetadata(contentType string, bodyBytes []byte) imagesRequestMetadata {
	var metadata imagesRequestMetadata
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return metadata
	}

	if strings.Contains(mediaType, "json") {
		var payload map[string]interface{}
		decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
		decoder.UseNumber()
		if err := decoder.Decode(&payload); err == nil {
			metadata.Model, _ = payload["model"].(string)
			metadata.Stream = parseImagesStreamValue(payload["stream"])
		}
		return metadata
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return metadata
		}
		reader := multipart.NewReader(bytes.NewReader(bodyBytes), boundary)
		for {
			part, err := reader.NextPart()
			if err != nil {
				return metadata
			}
			fieldName := part.FormName()
			if fieldName != "model" && fieldName != "stream" {
				part.Close()
				continue
			}
			valueBytes, err := io.ReadAll(io.LimitReader(part, 4096))
			part.Close()
			if err != nil {
				return metadata
			}
			value := strings.TrimSpace(string(valueBytes))
			switch fieldName {
			case "model":
				metadata.Model = value
			case "stream":
				metadata.Stream = parseImagesStreamValue(value)
			}
		}
	}

	return metadata
}

func parseImagesStreamValue(value interface{}) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on":
			return true
		}
	case json.Number:
		return typed.String() == "1"
	case float64:
		return typed == 1
	}
	return false
}