package utils

import (
	"net/http"
	"runtime"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	ContextKeyClaudeCodeDisguise = "client-disguise-claude-code"
	ContextKeyCodexDisguise      = "client-disguise-codex"
	claudeCodeDisguiseVersion    = "2.1.161"
	codexDisguiseVersion         = "0.144.0"
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

// ApplyCodexDisguise 为非 Codex 客户端规范化身份头，并保留已有会话与追踪字段。
func ApplyCodexDisguise(headers http.Header, stream bool) {
	userAgent := strings.ToLower(headers.Get("User-Agent"))
	isCodexClient := strings.Contains(userAgent, "codex_cli_rs") ||
		strings.Contains(userAgent, "codex-tui") ||
		strings.Contains(userAgent, "codex-cli")
	if !isCodexClient {
		headers.Set("User-Agent", codexUserAgent())
	}
	if headers.Get("originator") == "" {
		headers.Set("originator", "codex_cli_rs")
	}
	if headers.Get("version") == "" {
		headers.Set("version", codexDisguiseVersion)
	}

	if headers.Get("session_id") == "" {
		headers.Set("session_id", uuid.NewString())
	}

	// X-Codex-Installation-Id: 客户端安装 ID（如果没有则生成一个）
	if headers.Get("X-Codex-Installation-Id") == "" {
		headers.Set("X-Codex-Installation-Id", uuid.NewString())
	}

	// X-Codex-Window-Id: 编辑器窗口 ID（如果没有则使用默认值）
	if headers.Get("X-Codex-Window-Id") == "" {
		headers.Set("X-Codex-Window-Id", uuid.NewString())
	}

	// X-Request-Id: 请求追踪 ID（如果没有则生成）
	if headers.Get("X-Request-Id") == "" && headers.Get("X-Oai-Request-Id") == "" {
		headers.Set("X-Request-Id", uuid.NewString())
	}

	if stream && headers.Get("Accept") == "" {
		headers.Set("Accept", "text/event-stream")
	}
}

// ApplyClaudeCodeDisguise 为非 Claude Code 客户端规范化身份头，并保留已有会话与能力字段。
func ApplyClaudeCodeDisguise(headers http.Header, stream bool) {
	isClaudeCodeClient := isClaudeCodeUserAgent(headers.Get("User-Agent"))
	if !isClaudeCodeClient {
		headers.Set("User-Agent", "claude-cli/"+claudeCodeDisguiseVersion+" (external, cli)")
		headers.Set("X-Stainless-Lang", "js")
		headers.Set("X-Stainless-Package-Version", "0.94.0")
		headers.Set("X-Stainless-Runtime", "node")
		headers.Set("X-Stainless-Runtime-Version", "v24.3.0")
		headers.Set("X-Stainless-Os", stainlessOS())
		headers.Set("X-Stainless-Arch", stainlessArch())
		headers.Set("X-Stainless-Retry-Count", "0")
		headers.Set("X-Stainless-Timeout", "600")
	}

	// X-App 和 Beta 集合是严格 Claude Code 网关识别客户端的必要信号。
	headers.Set("X-App", "cli")
	headers.Set("Anthropic-Beta", mergeClaudeCodeBetaHeaders(headers.Values("Anthropic-Beta")))
	headers.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")

	// X-Claude-Code-Session-Id: 会话 ID（如果没有则生成）
	if headers.Get("X-Claude-Code-Session-Id") == "" {
		headers.Set("X-Claude-Code-Session-Id", uuid.NewString())
	}

	// 真实 Claude Code 请求可能缺少部分可选字段，仅补缺失值。
	if headers.Get("X-App") == "" {
		headers.Set("X-App", "cli")
	}
	if headers.Get("X-Stainless-Lang") == "" {
		headers.Set("X-Stainless-Lang", "js")
	}
	if headers.Get("X-Stainless-Package-Version") == "" {
		headers.Set("X-Stainless-Package-Version", "0.94.0")
	}
	if headers.Get("X-Stainless-Runtime") == "" {
		headers.Set("X-Stainless-Runtime", "node")
	}
	if headers.Get("X-Stainless-Runtime-Version") == "" {
		headers.Set("X-Stainless-Runtime-Version", "v24.3.0")
	}
	if headers.Get("X-Stainless-Os") == "" {
		headers.Set("X-Stainless-Os", stainlessOS())
	}
	if headers.Get("X-Stainless-Arch") == "" {
		headers.Set("X-Stainless-Arch", stainlessArch())
	}
	if headers.Get("X-Stainless-Retry-Count") == "" {
		headers.Set("X-Stainless-Retry-Count", "0")
	}
	if headers.Get("X-Stainless-Timeout") == "" {
		headers.Set("X-Stainless-Timeout", "600")
	}

	// anthropic-version: API 版本
	if headers.Get("anthropic-version") == "" {
		headers.Set("anthropic-version", "2023-06-01")
	}
	if headers.Get("Accept") == "" {
		if stream {
			headers.Set("Accept", "text/event-stream")
		} else {
			headers.Set("Accept", "application/json")
		}
	}
}

// EnsureCodexHeaders 保留旧调用入口，按非流式请求补全 Codex 特征。
func EnsureCodexHeaders(headers http.Header) {
	ApplyCodexDisguise(headers, false)
}

// EnsureClaudeCodeHeaders 保留旧调用入口，按非流式请求补全 Claude Code 特征。
func EnsureClaudeCodeHeaders(headers http.Header) {
	ApplyClaudeCodeDisguise(headers, false)
}

func codexUserAgent() string {
	switch runtime.GOOS {
	case "darwin":
		return "codex_cli_rs/" + codexDisguiseVersion + " (Mac OS; " + runtime.GOARCH + ") " + codexTargetTriple()
	case "windows":
		return "codex_cli_rs/" + codexDisguiseVersion + " (Windows; " + runtime.GOARCH + ") " + codexTargetTriple()
	default:
		return "codex_cli_rs/" + codexDisguiseVersion + " (Linux; " + runtime.GOARCH + ") " + codexTargetTriple()
	}
}

func codexTargetTriple() string {
	arch := "x86_64"
	if runtime.GOARCH == "arm64" {
		arch = "aarch64"
	}
	switch runtime.GOOS {
	case "darwin":
		return arch + "-apple-darwin"
	case "windows":
		return arch + "-pc-windows-msvc"
	default:
		return arch + "-unknown-linux-gnu"
	}
}

func stainlessOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "MacOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	default:
		return "Unknown"
	}
}

func stainlessArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	default:
		return runtime.GOARCH
	}
}
