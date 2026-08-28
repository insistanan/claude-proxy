// Package responses 提供 Responses API 的处理器
package responses

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/converters"
	"github.com/BenedictKing/claude-proxy/internal/handlers/hooks"
	"github.com/BenedictKing/claude-proxy/internal/handlers/proxycore"
	"github.com/BenedictKing/claude-proxy/internal/handlers/streams"
	"github.com/BenedictKing/claude-proxy/internal/providers"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/BenedictKing/claude-proxy/internal/session"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/gin-gonic/gin"
)

// ErrEmptyStreamResponse 表示上游返回 HTTP 200，但没有产出任何 output。
var ErrEmptyStreamResponse = errors.New("upstream returned empty stream response (model at capacity)")

// Handler Responses API 代理处理器
// 支持多渠道调度：当配置多个渠道时自动启用。
// 请求骨架（认证/读体/会话观测/渠道分派/failover）复用 proxycore.RunProxyRequest，
// 协议差异经 ProtocolSpec 表达。
func Handler(
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	sessionManager *session.SessionManager,
	channelScheduler *scheduler.ChannelScheduler,
	contentSafetyPipelines ...*hooks.Pipeline,
) gin.HandlerFunc {
	contentSafetyPipeline := hooks.ResolveContentSafetyPipeline(cfgManager, contentSafetyPipelines...)
	provider := &providers.ResponsesProvider{SessionManager: sessionManager}

	spec := proxycore.ProtocolSpec{
		Kind:         scheduler.ChannelKindResponses,
		LogName:      "Responses",
		HookPipeline: contentSafetyPipeline,
		// 多渠道模式下内容审核错误跨渠道转移（与历史行为一致；单渠道不生效）。
		AllowContentPolicyChannelFailover: cfgManager.GetFuzzyModeEnabled(),
		ParseRequest: func(c *gin.Context, body []byte) (string, bool, []string, bool) {
			// Codex 客户端伪装标记：ConvertToProviderRequest 构建上游请求时读取。
			c.Set(utils.ContextKeyCodexDisguise, cfgManager.GetCodexDisguiseEnabled())

			var responsesReq types.ResponsesRequest
			if len(body) > 0 {
				_ = json.Unmarshal(body, &responsesReq)
			}
			return responsesReq.Model, responsesReq.Stream, proxycore.ExtractPromptsFromResponsesInput(responsesReq.Input), true
		},
		BuildUpstreamRequest: func(c *gin.Context, up *config.UpstreamConfig, apiKey string, body []byte) (*http.Request, error) {
			req, _, err := provider.ConvertToProviderRequest(c, up, apiKey)
			return req, err
		},
		HandleSuccess: func(c *gin.Context, resp *http.Response, up *config.UpstreamConfig, apiKey string, body []byte, startTime time.Time) (*types.Usage, error) {
			var responsesReq types.ResponsesRequest
			if len(body) > 0 {
				_ = json.Unmarshal(body, &responsesReq)
			}
			conversationValue, _ := c.Get(utils.ContextKeyConversationUserID)
			conversationID, _ := conversationValue.(string)
			return handleSuccess(c, resp, provider, up.ServiceType, envCfg, sessionManager, startTime, &responsesReq, body, channelScheduler, conversationID)
		},
	}
	return gin.HandlerFunc(func(c *gin.Context) {
		proxycore.RunProxyRequest(c, envCfg, cfgManager, channelScheduler, spec)
	})
}

// handleSuccess 处理成功的 Responses 响应
func handleSuccess(
	c *gin.Context,
	resp *http.Response,
	provider *providers.ResponsesProvider,
	upstreamType string,
	envCfg *config.EnvConfig,
	sessionManager *session.SessionManager,
	startTime time.Time,
	originalReq *types.ResponsesRequest,
	originalRequestJSON []byte,
	channelScheduler *scheduler.ChannelScheduler,
	conversationID string,
) (*types.Usage, error) {
	defer resp.Body.Close()

	isStream := originalReq != nil && originalReq.Stream
	if upstreamType == converters.ResponsesUpstreamResponses && isResponsesImageGenerationRequest(originalRequestJSON) {
		return handleResponsesImagePassthrough(c, resp, envCfg, startTime, isStream)
	}

	if isStream {
		return handleStreamSuccess(c, resp, upstreamType, envCfg, sessionManager, startTime, originalReq, originalRequestJSON, channelScheduler, conversationID)
	}

	// 非流式响应处理
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to read response"})
		return nil, err
	}

	if envCfg.EnableResponseLogs {
		responseTime := time.Since(startTime).Milliseconds()
		log.Printf("[Responses-Timing] Responses 响应完成: %dms, 状态: %d", responseTime, resp.StatusCode)
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
			log.Printf("[Responses-Response] 响应头:\n%s", string(respHeadersJSON))

			var formattedBody string
			if envCfg.RawLogOutput {
				formattedBody = utils.FormatJSONBytesRaw(bodyBytes)
			} else {
				formattedBody = utils.FormatJSONBytesForLog(bodyBytes, 500)
			}
			log.Printf("[Responses-Response] 响应体:\n%s", formattedBody)
		}
	}

	responsesResp, err := converters.ConvertUpstreamResponseToResponses(upstreamType, originalRequestJSON, bodyBytes, "")
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to convert response"})
		return nil, err
	}

	// 容量错误有时会以 HTTP 200 的空结果返回。此时尚未写客户端，可安全地
	// 使用同一候选重试；该信号不会被上层计入 Key 熔断。
	if (len(responsesResp.Output) == 0 && responsesResp.Usage.OutputTokens == 0) ||
		(proxycore.IsUpstreamModelCapacityError(bodyBytes) && responsesResp.Usage.OutputTokens == 0) {
		log.Printf("[Responses] 检测到空响应 (非流式, output 为空), 尝试 failover")
		return nil, proxycore.NewRetrySameCandidateError(ErrEmptyStreamResponse)
	}

	// 先把用于指标的 usage 固化成上游原值快照——后续客户端补全/剥离/校正不应影响
	// 计费、熔断和性能画像使用的上游统计。
	metricsUsage := types.Usage{
		InputTokens:                responsesResp.Usage.InputTokens,
		OutputTokens:               responsesResp.Usage.OutputTokens,
		CacheCreationInputTokens:   responsesResp.Usage.CacheCreationInputTokens,
		CacheReadInputTokens:       responsesResp.Usage.CacheReadInputTokens,
		CacheCreation5mInputTokens: responsesResp.Usage.CacheCreation5mInputTokens,
		CacheCreation1hInputTokens: responsesResp.Usage.CacheCreation1hInputTokens,
		CacheTTL:                   responsesResp.Usage.CacheTTL,
	}
	// 会话清理只使用上游原始 total_tokens；如果上游没有提供 total_tokens，
	// 在客户端副本补全后再采用 input+output 的有限回退。严重错报的校正值只
	// 服务于客户端压缩判断，不写入本地会话累计，避免启发式估算污染持久化状态。
	sessionTokens := responsesResp.Usage.TotalTokens
	// Token 补全逻辑只作用于客户端响应副本。
	patchResponsesUsage(responsesResp, originalRequestJSON, envCfg)
	if sessionTokens <= 0 {
		sessionTokens = responsesResp.Usage.TotalTokens
		if sessionTokens <= 0 {
			sessionTokens = responsesResp.Usage.InputTokens + responsesResp.Usage.OutputTokens
		}
	}
	// 透传分支剥离累积式缓存统计（同 stream.go 的 stripAccumulatedCacheFromCompletedEvent）：
	// grok-4.6 等 OpenAI 兼容上游的 cached_tokens 是跨请求累积命中量，原样下发会让
	// Cursor 误判上下文一直满、反复触发压缩。Claude 原生缓存不受影响（函数内判断保留）
	if upstreamType == converters.ResponsesUpstreamResponses {
		stripAccumulatedCacheFromResponse(responsesResp)
		// 剥离缓存字段后 input_tokens 成了客户端判断上下文占用的唯一依据，
		// 校正上游对长上下文的错报值，否则 Cursor 会误判上下文为空、永不触发压缩。
		clientUsage := responsesResp.Usage
		correctUnderreportedInputTokensInResponse(responsesResp, &clientUsage, originalRequestJSON, envCfg)
		responsesResp.Usage = clientUsage
	}
	responseBody, err := utils.MarshalJSONNoEscape(responsesResp)
	if err != nil {
		return nil, fmt.Errorf("序列化 Responses 响应失败: %w", err)
	}
	if _, err := hooks.RunAttachedPostResponseHooks(c.Request.Context(), c, responseBody, resp); err != nil {
		return nil, err
	}
	proxycore.AssociateConversationExternalID(channelScheduler, conversationID, scheduler.ChannelKindResponses, responsesResp.ID)

	// 客户端的 store 只控制上游是否保存，不能关闭本代理自己的七天会话持久化。
	if originalReq != nil {
		sess, err := sessionManager.GetOrCreateSessionForConversation(originalReq.PreviousResponseID, conversationID)
		if err == nil {
			previousResponseID := sess.LastResponseID
			inputItems, _ := parseInputToItems(originalReq.Input)
			turnItems := make([]types.ResponsesItem, 0, len(inputItems)+len(responsesResp.Output))
			turnItems = append(turnItems, inputItems...)
			turnItems = append(turnItems, responsesResp.Output...)
			if err := sessionManager.CommitTurn(sess.ID, turnItems, sessionTokens, utils.DetectImageContent(originalRequestJSON), responsesResp.ID); err != nil {
				log.Printf("[Session] 持久化 Responses 会话轮次失败: %v", err)
			}

			if previousResponseID != "" {
				responsesResp.PreviousID = previousResponseID
				responsesResp.PreviousResponseID = previousResponseID
			}
		} else {
			log.Printf("[Session] 保存 Responses 会话失败: %v", err)
		}
	}

	utils.ForwardResponseHeaders(resp.Header, c.Writer)
	proxycore.MarkRequestLogFirstToken(c)
	c.JSON(200, responsesResp)

	// 返回 usage 数据用于指标记录（上游原值快照，不受下发侧校正影响）
	return &metricsUsage, nil
}

func handleResponsesImagePassthrough(
	c *gin.Context,
	resp *http.Response,
	envCfg *config.EnvConfig,
	startTime time.Time,
	requestedStream bool,
) (*types.Usage, error) {
	isStream := requestedStream || streams.IsEventStreamResponse(resp)
	if envCfg.EnableResponseLogs {
		responseTime := time.Since(startTime).Milliseconds()
		if isStream {
			log.Printf("[Responses-Image-Stream] 原生生图流式响应开始: %dms, 状态: %d", responseTime, resp.StatusCode)
		} else {
			log.Printf("[Responses-Image] 原生生图响应开始转发: %dms, 状态: %d", responseTime, resp.StatusCode)
		}
	}

	err := streams.ForwardUpstreamResponseBody(c, resp, "application/json", isStream)
	if envCfg.EnableResponseLogs {
		responseTime := time.Since(startTime).Milliseconds()
		log.Printf("[Responses-Image] 原生生图响应转发完成: %dms", responseTime)
	}
	return nil, err
}

func isResponsesImageGenerationRequest(bodyBytes []byte) bool {
	var payload struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return false
	}
	for _, rawTool := range payload.Tools {
		var tool struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rawTool, &tool); err == nil && tool.Type == "image_generation" {
			return true
		}
	}
	return false
}

// parseInputToItems 解析 input 为 ResponsesItem 数组
func parseInputToItems(input interface{}) ([]types.ResponsesItem, error) {
	switch v := input.(type) {
	case string:
		return []types.ResponsesItem{{Type: "text", Content: v}}, nil
	case []interface{}:
		items := []types.ResponsesItem{}
		for _, item := range v {
			itemMap, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			itemType, _ := itemMap["type"].(string)
			role, _ := itemMap["role"].(string)
			content := itemMap["content"]
			summary := itemMap["summary"]
			id, _ := itemMap["id"].(string)
			status, _ := itemMap["status"].(string)
			callID, _ := itemMap["call_id"].(string)
			name, _ := itemMap["name"].(string)
			tools := itemMap["tools"]
			namespace, _ := itemMap["namespace"].(string)
			execution, _ := itemMap["execution"].(string)
			arguments, _ := itemMap["arguments"].(string)
			if arguments == "" {
				if rawArguments, ok := itemMap["arguments"]; ok && rawArguments != nil {
					if encoded, err := utils.MarshalJSONNoEscape(rawArguments); err == nil {
						arguments = string(encoded)
					}
				}
			}
			if itemType == "" && role != "" {
				itemType = "message"
			}
			if itemType == "custom_tool_call" {
				if inputValue, ok := itemMap["input"]; ok && content == nil {
					content = inputValue
				}
			}
			if output, ok := itemMap["output"]; ok && content == nil {
				content = output
			}
			items = append(items, types.ResponsesItem{
				ID:        id,
				Type:      itemType,
				Status:    status,
				Role:      role,
				Content:   content,
				Summary:   summary,
				CallID:    callID,
				Name:      name,
				Arguments: arguments,
				Tools:     tools,
				Namespace: namespace,
				Execution: execution,
			})
		}
		return items, nil
	default:
		return nil, fmt.Errorf("unsupported input type")
	}
}
