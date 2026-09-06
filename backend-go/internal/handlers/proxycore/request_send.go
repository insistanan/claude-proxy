package proxycore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/httpclient"
	"github.com/BenedictKing/claude-proxy/internal/logger"
	"github.com/BenedictKing/claude-proxy/internal/utils"
)

// ShouldForceIdentityEncoding 判定是否应强制上游使用 identity 编码（禁止压缩）。
// 对流式响应（SSE）、流式端点（如 Gemini streamGenerateContent / alt=sse）或指定 text/event-stream 的请求，
// 必须强制 identity，防止上游压缩 SSE 流导致分块按行解析失败。
func ShouldForceIdentityEncoding(req *http.Request, isStream bool) bool {
	if isStream {
		return true
	}
	if req == nil || req.URL == nil {
		return false
	}
	if strings.Contains(req.URL.Path, "streamGenerateContent") || strings.Contains(req.URL.RawQuery, "alt=sse") {
		return true
	}
	acceptHeader := strings.ToLower(req.Header.Get("Accept"))
	return strings.Contains(acceptHeader, "text/event-stream")
}

// SendRequest 发送 HTTP 请求到上游
// isStream: 是否为流式请求（流式请求使用无超时客户端）
// apiType: 接口类型（Messages/Responses/Gemini），用于日志标签前缀
func SendRequest(req *http.Request, upstream *config.UpstreamConfig, envCfg *config.EnvConfig, isStream bool, apiType string, proxyURL string) (*http.Response, error) {
	clientManager := httpclient.GetManager()

	var client *http.Client
	var err error
	if isStream {
		client, err = clientManager.GetStreamClient(upstream.InsecureSkipVerify, proxyURL)
	} else {
		timeout := time.Duration(envCfg.RequestTimeout) * time.Millisecond
		client, err = clientManager.GetStandardClient(timeout, upstream.InsecureSkipVerify, proxyURL)
	}

	// 流式请求或 SSE 端点强制设置 Accept-Encoding: identity，防止上游压缩 SSE 流
	// 普通非流式请求保持清空，由 Go Transport 自动添加 gzip 并处理
	if ShouldForceIdentityEncoding(req, isStream) {
		req.Header.Set("Accept-Encoding", "identity")
	}
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("创建上游 HTTP 客户端失败: %w", err)
	}

	if upstream.InsecureSkipVerify && envCfg.EnableRequestLogs {
		log.Printf("[%s-Request-TLS] 警告: 正在跳过对 %s 的TLS证书验证", apiType, req.URL.String())
	}

	if envCfg.EnableRequestLogs {
		log.Printf("[%s-Request-URL] 实际请求URL: %s", apiType, req.URL.String())
		log.Printf("[%s-Request-Method] 请求方法: %s", apiType, req.Method)
		logRequestDetails(req, envCfg, apiType)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	// 流式响应：包装 body 为带空闲超时的 reader，检测"TCP 通但流中挂起"的上游。
	// 空闲超时默认保守（5 分钟），避免误杀思考模型的长静默期。
	if isStream && resp != nil && resp.Body != nil {
		resp.Body = httpclient.NewIdleTimeoutReader(resp.Body, time.Duration(envCfg.StreamIdleTimeout)*time.Second, apiType)
	}
	if envCfg.EnableResponseLogs && resp != nil && resp.Body != nil {
		requestID := logger.RequestIDFromContext(req.Context())
		if requestID == "" {
			requestID = logger.NewRequestID()
		}
		urlText := ""
		if req.URL != nil {
			urlText = req.URL.String()
		}
		resp.Body = logger.CaptureResponseBody(resp.Body, logger.TrafficLog{
			RequestID:   requestID,
			APIType:     apiType,
			Method:      req.Method,
			URL:         urlText,
			StatusCode:  resp.StatusCode,
			Stream:      isStream,
			HeadersJSON: logger.MaskedHeaderJSON(resp.Header),
		})
	}

	return resp, nil
}

// logRequestDetails 记录请求头与请求体（受 ENABLE_REQUEST_LOGS / RAW_LOG_OUTPUT 控制）
// apiType: 接口类型（Messages/Responses/Gemini），用于日志标签前缀
func logRequestDetails(req *http.Request, envCfg *config.EnvConfig, apiType string) {
	// 对请求头做敏感信息脱敏
	reqHeaders := make(map[string]string)
	for key, values := range req.Header {
		if len(values) > 0 {
			reqHeaders[key] = values[0]
		}
	}
	maskedReqHeaders := utils.MaskSensitiveHeaders(reqHeaders)
	var reqHeadersJSON []byte
	if envCfg.RawLogOutput {
		reqHeadersJSON, _ = json.Marshal(maskedReqHeaders)
	} else {
		reqHeadersJSON, _ = json.MarshalIndent(maskedReqHeaders, "", "  ")
	}
	log.Printf("[%s-Request-Headers] 实际请求头:\n%s", apiType, string(reqHeadersJSON))

	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err == nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			var formattedBody string
			if envCfg.RawLogOutput {
				formattedBody = utils.FormatJSONBytesRaw(bodyBytes)
			} else {
				formattedBody = utils.FormatJSONBytesForLog(bodyBytes, 500)
			}
			log.Printf("[%s-Request-Body] 实际请求体:\n%s", apiType, formattedBody)
			requestID := logger.RequestIDFromContext(req.Context())
			urlText := ""
			if req.URL != nil {
				urlText = req.URL.String()
			}
			logger.RecordTraffic(logger.TrafficLog{
				RequestID:   requestID,
				Phase:       logger.PhaseUpstreamRequest,
				APIType:     apiType,
				Method:      req.Method,
				URL:         urlText,
				HeadersJSON: string(reqHeadersJSON),
				Body:        string(bodyBytes),
			})
		}
	}
}
