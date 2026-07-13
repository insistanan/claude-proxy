package common

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/gin-gonic/gin"
)

// IsEventStreamResponse 判断上游是否返回 SSE。
func IsEventStreamResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	}
	return strings.EqualFold(mediaType, "text/event-stream")
}

// ForwardUpstreamResponseBody 逐块转发上游响应体，避免为图片 Base64 或 SSE 事件设置固定行长上限。
// 调用方负责关闭 resp.Body。
func ForwardUpstreamResponseBody(c *gin.Context, resp *http.Response, defaultContentType string, flush bool) error {
	if c == nil || resp == nil || resp.Body == nil {
		return nil
	}

	utils.ForwardResponseHeaders(resp.Header, c.Writer)
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = defaultContentType
	}
	if contentType != "" {
		c.Header("Content-Type", contentType)
	}

	if IsEventStreamResponse(resp) {
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		flush = true
	}

	c.Status(resp.StatusCode)
	flusher, _ := c.Writer.(http.Flusher)
	buffer := make([]byte, 32*1024)
	markedFirstChunk := false

	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if !markedFirstChunk {
				MarkRequestLogFirstToken(c)
				markedFirstChunk = true
			}
			if _, writeErr := c.Writer.Write(buffer[:n]); writeErr != nil {
				return writeErr
			}
			if flush && flusher != nil {
				flusher.Flush()
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}
