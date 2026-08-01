package providers

import (
	"context"
	"io"
	"net/http"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/gin-gonic/gin"
)

// Provider 提供商接口
type Provider interface {
	// ConvertToProviderRequest 将 gin context 中的请求转换为目标上游的 http.Request，并返回用于日志的原始请求体
	ConvertToProviderRequest(c *gin.Context, upstream *config.UpstreamConfig, apiKey string) (*http.Request, []byte, error)

	// ConvertToClaudeResponse 将提供商响应转换为 Claude 响应
	ConvertToClaudeResponse(providerResp *types.ProviderResponse) (*types.ClaudeResponse, error)

	// HandleStreamResponse 处理流式响应
	HandleStreamResponse(body io.ReadCloser) (<-chan string, <-chan error, error)

	// HandleStreamResponseCtx 处理流式响应（带 context，客户端断连可中止，避免 goroutine/连接泄漏）
	// ctx 用于监听客户端断开：生产 goroutine 在向 eventChan 发送事件时 select ctx.Done()，
	// 一旦消费者离开（客户端断连）立即停止读取上游并退出，杜绝"缓冲写满后永久阻塞"。
	HandleStreamResponseCtx(ctx context.Context, body io.ReadCloser) (<-chan string, <-chan error, error)
}

// GetProvider 根据服务类型获取提供商
func GetProvider(serviceType string) Provider {
	switch serviceType {
	case "openai":
		return &OpenAIProvider{}
	case "gemini":
		return &GeminiProvider{}
	case "claude":
		return &ClaudeProvider{}
	case "responses":
		return &MessagesResponsesProvider{}
	default:
		return nil
	}
}
