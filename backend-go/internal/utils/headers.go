package utils

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// PrepareUpstreamHeaders 准备上游请求头（统一头部处理逻辑）
// 保留原始请求头，移除代理相关头部，设置认证头
// 注意：此函数适用于Claude类型渠道，对于其他类型请使用 PrepareMinimalHeaders
func PrepareUpstreamHeaders(c *gin.Context, targetHost string) http.Header {
	headers := c.Request.Header.Clone()

	// 设置正确的Host头部
	headers.Set("Host", targetHost)

	// 移除代理相关头部
	headers.Del("x-proxy-key")
	headers.Del("X-Forwarded-Host")
	headers.Del("X-Forwarded-Proto")

	// 移除所有可能泄露客户端真实 IP 的头部，确保上游只能看到代理服务器的 IP
	headers.Del("X-Forwarded-For")
	headers.Del("X-Real-IP")
	headers.Del("X-Forwarded")
	headers.Del("Forwarded")
	headers.Del("CF-Connecting-IP")
	headers.Del("True-Client-IP")
	headers.Del("X-Client-IP")

	// 移除 Accept-Encoding，让 Go 的 http.Client 自动处理 gzip 压缩/解压缩
	// 这样可以避免在原始请求包含 Accept-Encoding 时 Go 不自动解压缩的问题
	headers.Del("Accept-Encoding")

	return headers
}

// PrepareMinimalHeaders 准备最小化请求头（适用于非Claude渠道如OpenAI、Gemini等）
// 只保留必要的头部：Content-Type和Host，不包含任何Anthropic特定头部
// 注意：不设置Accept-Encoding，让Go的http.Client自动处理gzip压缩
func PrepareMinimalHeaders(targetHost string) http.Header {
	headers := http.Header{}

	// 只设置最基本的头部
	headers.Set("Host", targetHost)
	headers.Set("Content-Type", "application/json")
	// 不显式设置Accept-Encoding，让Go的http.Client自动添加并处理gzip解压

	return headers
}

// SetAuthenticationHeader 设置认证头部（根据密钥格式智能选择）
func SetAuthenticationHeader(headers http.Header, apiKey string) {
	// 移除旧的认证头
	headers.Del("authorization")
	headers.Del("x-api-key")
	headers.Del("x-goog-api-key")

	// Claude 官方密钥格式（sk-ant-api03-xxx）使用 x-api-key
	// 符合 Claude API 官方推荐的认证方式
	if strings.HasPrefix(apiKey, "sk-ant-") {
		headers.Set("x-api-key", apiKey)
	} else {
		// 其他格式密钥使用 Authorization: Bearer
		// 适用于 OpenAI、自定义密钥等
		headers.Set("Authorization", "Bearer "+apiKey)
	}
}

// SetGeminiAuthenticationHeader 设置Gemini认证头部
func SetGeminiAuthenticationHeader(headers http.Header, apiKey string) {
	headers.Del("authorization")
	headers.Del("x-api-key")
	headers.Set("x-goog-api-key", apiKey)
}

// EnsureCompatibleUserAgent 确保兼容的User-Agent（仅在必要时设置）
func EnsureCompatibleUserAgent(headers http.Header, serviceType string) {
	userAgent := headers.Get("User-Agent")

	// 仅在Claude服务类型且用户未设置或设置不正确时才修改
	if serviceType == "claude" {
		if userAgent == "" || !strings.HasPrefix(strings.ToLower(userAgent), "claude-cli") {
			headers.Set("User-Agent", "claude-cli/2.0.34 (external, cli)")
		}
	}
}

// ForwardResponseHeaders 转发上游响应头到客户端
// 作为透明代理，应该转发所有响应头，只过滤框架自动处理的头部
func ForwardResponseHeaders(upstreamHeaders http.Header, clientWriter http.ResponseWriter) {
	// 不应转发的头部列表（由框架或代理层自动处理）
	skipHeaders := map[string]bool{
		"transfer-encoding": true, // 由框架自动处理
		"content-length":    true, // 由框架自动处理
		"connection":        true, // 代理层控制
		"content-encoding":  true, // 如果已解压则不应转发
	}

	// 复制所有上游响应头到客户端
	for key, values := range upstreamHeaders {
		lowerKey := strings.ToLower(key)

		// 跳过不应转发的头部
		if skipHeaders[lowerKey] {
			continue
		}

		// 转发头部（可能有多个值）
		for _, value := range values {
			clientWriter.Header().Add(key, value)
		}
	}
}

// EnsureCodexHeaders 确保 Codex CLI 所需的请求头存在（如果客户端没发送则补充）
// 参考：https://github.com/openai/codex 源码
func EnsureCodexHeaders(headers http.Header) {
	// X-Codex-Installation-Id: 客户端安装 ID（如果没有则生成一个）
	if headers.Get("X-Codex-Installation-Id") == "" {
		headers.Set("X-Codex-Installation-Id", "proxy-generated-installation-id")
	}

	// X-Codex-Window-Id: 编辑器窗口 ID（如果没有则使用默认值）
	if headers.Get("X-Codex-Window-Id") == "" {
		headers.Set("X-Codex-Window-Id", "proxy-window-1")
	}

	// X-Request-Id: 请求追踪 ID（如果没有则生成）
	if headers.Get("X-Request-Id") == "" && headers.Get("X-Oai-Request-Id") == "" {
		headers.Set("X-Request-Id", generateRequestID())
	}

	// User-Agent: Codex CLI 标识
	if headers.Get("User-Agent") == "" {
		headers.Set("User-Agent", "codex-cli")
	}
}

// EnsureClaudeCodeHeaders 确保 Claude Code CLI 所需的请求头存在（如果客户端没发送则补充）
// 参考：https://code.claude.com/docs/en/llm-gateway
func EnsureClaudeCodeHeaders(headers http.Header) {
	// X-Claude-Code-Session-Id: 会话 ID（如果没有则生成）
	if headers.Get("X-Claude-Code-Session-Id") == "" {
		headers.Set("X-Claude-Code-Session-Id", generateSessionID())
	}

	// X-App: CLI 标识
	if headers.Get("X-App") == "" {
		headers.Set("X-App", "cli")
	}

	// Stainless SDK 标准请求头（如果没有则补充）
	if headers.Get("X-Stainless-Lang") == "" {
		headers.Set("X-Stainless-Lang", "js")
	}
	if headers.Get("X-Stainless-Package-Version") == "" {
		headers.Set("X-Stainless-Package-Version", "0.74.0")
	}
	if headers.Get("X-Stainless-Runtime") == "" {
		headers.Set("X-Stainless-Runtime", "node")
	}
	if headers.Get("X-Stainless-Runtime-Version") == "" {
		headers.Set("X-Stainless-Runtime-Version", "v22.12.0")
	}
	if headers.Get("X-Stainless-Os") == "" {
		headers.Set("X-Stainless-Os", "Linux")
	}
	if headers.Get("X-Stainless-Arch") == "" {
		headers.Set("X-Stainless-Arch", "x64")
	}
	if headers.Get("X-Stainless-Retry-Count") == "" {
		headers.Set("X-Stainless-Retry-Count", "0")
	}
	if headers.Get("X-Stainless-Timeout") == "" {
		headers.Set("X-Stainless-Timeout", "600")
	}

	// User-Agent: Claude Code CLI 标识
	userAgent := headers.Get("User-Agent")
	if userAgent == "" || (!strings.Contains(strings.ToLower(userAgent), "claude-cli") && !strings.Contains(strings.ToLower(userAgent), "claude-code")) {
		headers.Set("User-Agent", "claude-cli/2.1.92 (external, cli)")
	}

	// anthropic-version: API 版本
	if headers.Get("anthropic-version") == "" {
		headers.Set("anthropic-version", "2023-06-01")
	}
}

// generateRequestID 生成请求 ID
func generateRequestID() string {
	return "proxy-req-" + randomHex(16)
}

// generateSessionID 生成会话 ID
func generateSessionID() string {
	return "proxy-session-" + randomHex(32)
}

// randomHex 生成指定长度的随机十六进制字符串
func randomHex(n int) string {
	const hexChars = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = hexChars[i%len(hexChars)]
	}
	return string(b)
}
