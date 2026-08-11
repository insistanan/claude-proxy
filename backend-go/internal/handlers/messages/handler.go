// Package messages 提供 Claude Messages API 的处理器
package messages

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/handlers/common"
	"github.com/BenedictKing/claude-proxy/internal/middleware"
	"github.com/BenedictKing/claude-proxy/internal/providers"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/gin-gonic/gin"
)

// Handler Messages API 代理处理器
// 支持多渠道调度：当配置多个渠道时自动启用
// Handler Messages API 代理处理器。
// 使用通用 RunProxyRequest 骨架，通过 ProtocolSpec 注入协议特有逻辑。
func Handler(
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	contentSafetyPipelines ...*common.HookPipeline,
) gin.HandlerFunc {
	contentSafetyPipeline := common.ResolveContentSafetyPipeline(cfgManager, contentSafetyPipelines...)
	spec := common.ProtocolSpec{
		Kind:         scheduler.ChannelKindMessages,
		LogName:      "Messages",
		HookPipeline: contentSafetyPipeline,
		ParseRequest: func(c *gin.Context, body []byte) (string, bool, []string, bool) {
			body, _ = common.RemoveEmptySignatures(body, envCfg.EnableRequestLogs, "Messages")
			c.Set(utils.ContextKeyClaudeCodeDisguise, cfgManager.GetClaudeCodeDisguiseEnabled())
			var claudeReq types.ClaudeRequest
			if len(body) > 0 {
				_ = json.Unmarshal(body, &claudeReq)
			}
			prompts := common.ExtractPromptsFromClaude(claudeReq.Messages)
			return claudeReq.Model, claudeReq.Stream, prompts, true
		},
		PreRoute: nil,
		BuildUpstreamRequest: func(c *gin.Context, up *config.UpstreamConfig, apiKey string, body []byte) (*http.Request, error) {
			provider := providers.GetProvider(up.ServiceType)
			if provider == nil {
				return nil, fmt.Errorf("unsupported service type: %s", up.ServiceType)
			}
			req, _, err := provider.ConvertToProviderRequest(c, up, apiKey)
			return req, err
		},
		HandleSuccess: func(c *gin.Context, resp *http.Response, up *config.UpstreamConfig, apiKey string, body []byte, startTime time.Time) (*types.Usage, error) {
			provider := providers.GetProvider(up.ServiceType)
			if provider == nil {
				return nil, fmt.Errorf("unsupported service type: %s", up.ServiceType)
			}
			var claudeReq types.ClaudeRequest
			_ = json.Unmarshal(body, &claudeReq)
			if claudeReq.Stream {
				return common.HandleStreamResponse(c, resp, provider, envCfg, startTime, up, body, claudeReq.Model)
			}
			return handleNormalResponse(c, resp, provider, envCfg, startTime, body, up, apiKey)
		},
	}
	return func(c *gin.Context) {
		common.RunProxyRequest(c, envCfg, cfgManager, channelScheduler, spec)
	}
}

func handleNormalResponse(
	c *gin.Context,
	resp *http.Response,
	provider providers.Provider,
	envCfg *config.EnvConfig,
	startTime time.Time,
	requestBody []byte,
	upstream *config.UpstreamConfig,
	apiKey string,
) (*types.Usage, error) {
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to read response"})
		return nil, err
	}

	if envCfg.EnableResponseLogs {
		responseTime := time.Since(startTime).Milliseconds()
		log.Printf("[Messages-Timing] 响应完成: %dms, 状态: %d", responseTime, resp.StatusCode)
		if envCfg.IsDevelopment() {
			respHeaders := make(map[string]string)
			for key, values := range resp.Header {
				if len(values) > 0 {
					respHeaders[key] = values[0]
				}
			}
			var respHeadersJSON []byte
			if envCfg.RawLogOutput {
				respHeadersJSON, _ = json.Marshal(respHeaders)
			} else {
				respHeadersJSON, _ = json.MarshalIndent(respHeaders, "", "  ")
			}
			log.Printf("[Messages-Response] 响应头:\n%s", string(respHeadersJSON))

			var formattedBody string
			if envCfg.RawLogOutput {
				formattedBody = utils.FormatJSONBytesRaw(bodyBytes)
			} else {
				formattedBody = utils.FormatJSONBytesForLog(bodyBytes, 500)
			}
			log.Printf("[Messages-Response] 响应体:\n%s", formattedBody)
		}
	}

	providerResp := &types.ProviderResponse{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		Body:       bodyBytes,
		Stream:     false,
	}

	claudeResp, err := provider.ConvertToClaudeResponse(providerResp)
	if err != nil {
		return nil, fmt.Errorf("转换上游响应失败: %w", err)
	}
	// 将所有协议返回的 thinking 统一登记。下一轮即使故障转移到要求
	// reasoning_content 的 Chat 渠道，也能回放该 assistant 工具调用回合。
	providers.CacheClaudeResponseReasoning(claudeResp)

	// Token 补全逻辑
	if claudeResp.Usage == nil {
		estimatedInput := utils.EstimateRequestTokens(requestBody)
		estimatedOutput := utils.EstimateResponseTokens(claudeResp.Content)
		claudeResp.Usage = &types.Usage{
			InputTokens:  estimatedInput,
			OutputTokens: estimatedOutput,
		}
		if envCfg.EnableResponseLogs {
			log.Printf("[Messages-Token] 上游无Usage, 本地估算: input=%d, output=%d", estimatedInput, estimatedOutput)
		}
	} else {
		originalInput := claudeResp.Usage.InputTokens
		originalOutput := claudeResp.Usage.OutputTokens
		patched := false

		hasCacheTokens := claudeResp.Usage.CacheCreationInputTokens > 0 || claudeResp.Usage.CacheReadInputTokens > 0

		if claudeResp.Usage.InputTokens <= 1 && !hasCacheTokens {
			claudeResp.Usage.InputTokens = utils.EstimateRequestTokens(requestBody)
			patched = true
		}
		if claudeResp.Usage.OutputTokens <= 1 {
			claudeResp.Usage.OutputTokens = utils.EstimateResponseTokens(claudeResp.Content)
			patched = true
		}
		if envCfg.EnableResponseLogs {
			if patched {
				log.Printf("[Messages-Token] 虚假值补全: InputTokens=%d->%d, OutputTokens=%d->%d",
					originalInput, claudeResp.Usage.InputTokens, originalOutput, claudeResp.Usage.OutputTokens)
			}
			log.Printf("[Messages-Token] InputTokens=%d, OutputTokens=%d, CacheCreationInputTokens=%d, CacheReadInputTokens=%d, CacheCreation5m=%d, CacheCreation1h=%d, CacheTTL=%s",
				claudeResp.Usage.InputTokens, claudeResp.Usage.OutputTokens,
				claudeResp.Usage.CacheCreationInputTokens, claudeResp.Usage.CacheReadInputTokens,
				claudeResp.Usage.CacheCreation5mInputTokens, claudeResp.Usage.CacheCreation1hInputTokens,
				claudeResp.Usage.CacheTTL)
		}
	}

	// 监听客户端断开连接
	ctx := c.Request.Context()
	go func() {
		<-ctx.Done()
		if !c.Writer.Written() {
			if envCfg.EnableResponseLogs {
				responseTime := time.Since(startTime).Milliseconds()
				log.Printf("[Messages-Timing] 响应中断: %dms, 状态: %d", responseTime, resp.StatusCode)
			}
		}
	}()

	// 缓存字段会被 Cursor 计入 Conversation 上下文。流式出口已经剥离它们，
	// 非流式响应也必须保持同一契约；内部 usage 仍用于后续指标记录。
	clientResp := *claudeResp
	if claudeResp.Usage != nil {
		clientUsage := *claudeResp.Usage
		clientUsage.CacheCreationInputTokens = 0
		clientUsage.CacheCreation5mInputTokens = 0
		clientUsage.CacheCreation1hInputTokens = 0
		clientUsage.CacheReadInputTokens = 0
		clientUsage.CacheTTL = ""
		clientResp.Usage = &clientUsage
	}
	clientBody, err := utils.MarshalJSONNoEscape(&clientResp)
	if err != nil {
		return nil, fmt.Errorf("序列化 Messages 响应失败: %w", err)
	}
	clientBody, err = common.RunAttachedPostResponseHooks(c.Request.Context(), c, clientBody, resp)
	if err != nil {
		return nil, err
	}

	utils.ForwardResponseHeaders(resp.Header, c.Writer)
	common.MarkRequestLogFirstToken(c)
	c.Data(http.StatusOK, "application/json", clientBody)

	if envCfg.EnableResponseLogs {
		responseTime := time.Since(startTime).Milliseconds()
		log.Printf("[Messages-Timing] 响应发送完成: %dms, 状态: %d", responseTime, resp.StatusCode)
	}

	return claudeResp.Usage, nil
}

// CountTokensHandler 处理 /v1/messages/count_tokens 请求
func CountTokensHandler(envCfg *config.EnvConfig, cfgManager *config.ConfigManager, channelScheduler *scheduler.ChannelScheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		middleware.ProxyAuthMiddleware(envCfg)(c)
		if c.IsAborted() {
			return
		}

		// 使用统一的请求体读取函数，应用大小限制
		bodyBytes, err := common.ReadRequestBody(c, envCfg.MaxRequestBodySize)
		if err != nil {
			// ReadRequestBody 已经返回了错误响应
			return
		}

		var req struct {
			Model    string      `json:"model"`
			System   interface{} `json:"system"`
			Messages interface{} `json:"messages"`
			Tools    interface{} `json:"tools"`
		}
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			c.JSON(400, gin.H{"error": "Invalid JSON"})
			return
		}

		inputTokens := utils.EstimateRequestTokens(bodyBytes)

		c.JSON(200, gin.H{
			"input_tokens": inputTokens,
		})

		if envCfg.EnableResponseLogs {
			log.Printf("[Messages-Token] CountTokens本地估算: model=%s, input_tokens=%d", req.Model, inputTokens)
		}
	}
}
