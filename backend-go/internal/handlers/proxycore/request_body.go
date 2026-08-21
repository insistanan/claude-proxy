package proxycore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/gin-gonic/gin"
)

// ReadRequestBody 读取并验证请求体大小
// 返回: (bodyBytes, error)
// 如果请求体过大，会自动返回 413 错误并排空剩余数据
func ReadRequestBody(c *gin.Context, maxBodySize int64) ([]byte, error) {
	limitedReader := io.LimitReader(c.Request.Body, maxBodySize+1)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		c.JSON(400, gin.H{"error": "Failed to read request body"})
		return nil, err
	}

	if int64(len(bodyBytes)) > maxBodySize {
		// 排空剩余请求体，避免 keep-alive 连接污染
		io.Copy(io.Discard, c.Request.Body)
		c.JSON(413, gin.H{"error": fmt.Sprintf("Request body too large, maximum size is %d MB", maxBodySize/1024/1024)})
		return nil, fmt.Errorf("request body too large")
	}

	// 恢复请求体供后续使用
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	return bodyBytes, nil
}

// RestoreRequestBody 恢复请求体供后续使用
func RestoreRequestBody(c *gin.Context, bodyBytes []byte) {
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
}

// LogOriginalRequest 记录原始请求信息
func LogOriginalRequest(c *gin.Context, bodyBytes []byte, envCfg *config.EnvConfig, apiType string) {
	if !envCfg.EnableRequestLogs {
		return
	}

	log.Printf("[Request-Receive] 收到%s请求: %s %s", apiType, c.Request.Method, c.Request.URL.Path)

	if envCfg.IsDevelopment() {
		var formattedBody string
		if envCfg.RawLogOutput {
			formattedBody = utils.FormatJSONBytesRaw(bodyBytes)
		} else {
			formattedBody = utils.FormatJSONBytesForLog(bodyBytes, 500)
		}
		log.Printf("[Request-OriginalBody] 原始请求体:\n%s", formattedBody)

		sanitizedHeaders := make(map[string]string)
		for key, values := range c.Request.Header {
			if len(values) > 0 {
				sanitizedHeaders[key] = values[0]
			}
		}
		maskedHeaders := utils.MaskSensitiveHeaders(sanitizedHeaders)
		var headersJSON []byte
		if envCfg.RawLogOutput {
			headersJSON, _ = json.Marshal(maskedHeaders)
		} else {
			headersJSON, _ = json.MarshalIndent(maskedHeaders, "", "  ")
		}
		log.Printf("[Request-OriginalHeaders] 原始请求头:\n%s", string(headersJSON))
	}
}
