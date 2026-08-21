// Package responses 提供 Responses API 的处理器
package responses

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
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
			userID, _ := c.Get(utils.ContextKeyConversationUserID)
			conversationID, _ := userID.(string)
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

	// Token 补全逻辑
	patchResponsesUsage(responsesResp, originalRequestJSON, envCfg)
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
			if err := sessionManager.CommitTurn(sess.ID, turnItems, responsesResp.Usage.TotalTokens, utils.DetectImageContent(originalRequestJSON), responsesResp.ID); err != nil {
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

	// 返回 usage 数据用于指标记录
	return &types.Usage{
		InputTokens:                responsesResp.Usage.InputTokens,
		OutputTokens:               responsesResp.Usage.OutputTokens,
		CacheCreationInputTokens:   responsesResp.Usage.CacheCreationInputTokens,
		CacheReadInputTokens:       responsesResp.Usage.CacheReadInputTokens,
		CacheCreation5mInputTokens: responsesResp.Usage.CacheCreation5mInputTokens,
		CacheCreation1hInputTokens: responsesResp.Usage.CacheCreation1hInputTokens,
		CacheTTL:                   responsesResp.Usage.CacheTTL,
	}, nil
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

// patchResponsesUsage 补全 Responses 响应的 Token 统计
func patchResponsesUsage(resp *types.ResponsesResponse, requestBody []byte, envCfg *config.EnvConfig) {
	// 检查是否有 Claude 原生缓存 token（有时才跳过 input_tokens 修补）
	// 仅检测 Claude 原生字段：cache_creation_input_tokens, cache_read_input_tokens,
	// cache_creation_5m_input_tokens, cache_creation_1h_input_tokens
	// 注意：不检测 input_tokens_details.cached_tokens（OpenAI 格式），避免错误跳过
	hasClaudeCache := resp.Usage.CacheCreationInputTokens > 0 ||
		resp.Usage.CacheReadInputTokens > 0 ||
		resp.Usage.CacheCreation5mInputTokens > 0 ||
		resp.Usage.CacheCreation1hInputTokens > 0

	// 检查是否需要补全
	needInputPatch := resp.Usage.InputTokens <= 1 && !hasClaudeCache
	needOutputPatch := resp.Usage.OutputTokens <= 1

	// 如果 usage 完全为空，进行完整估算
	if resp.Usage.InputTokens == 0 && resp.Usage.OutputTokens == 0 && resp.Usage.TotalTokens == 0 {
		estimatedInput := utils.EstimateResponsesRequestTokens(requestBody)
		estimatedOutput := estimateResponsesOutputFromItems(resp.Output)
		resp.Usage.InputTokens = estimatedInput
		resp.Usage.OutputTokens = estimatedOutput
		resp.Usage.TotalTokens = estimatedInput + estimatedOutput
		if envCfg.EnableResponseLogs {
			log.Printf("[Responses-Token] 上游无Usage, 本地估算: input=%d, output=%d", estimatedInput, estimatedOutput)
		}
		return
	}

	// 修补虚假值
	originalInput := resp.Usage.InputTokens
	originalOutput := resp.Usage.OutputTokens
	patched := false

	if needInputPatch {
		resp.Usage.InputTokens = utils.EstimateResponsesRequestTokens(requestBody)
		patched = true
	}
	if needOutputPatch {
		resp.Usage.OutputTokens = estimateResponsesOutputFromItems(resp.Output)
		patched = true
	}

	// 重新计算 TotalTokens（修补时或 total_tokens 为 0 但 input/output 有效时）
	if patched || (resp.Usage.TotalTokens == 0 && (resp.Usage.InputTokens > 0 || resp.Usage.OutputTokens > 0)) {
		resp.Usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
	}

	if envCfg.EnableResponseLogs {
		if patched {
			log.Printf("[Responses-Token] 虚假值修补: InputTokens=%d->%d, OutputTokens=%d->%d",
				originalInput, resp.Usage.InputTokens, originalOutput, resp.Usage.OutputTokens)
		}
		log.Printf("[Responses-Token] InputTokens=%d, OutputTokens=%d, TotalTokens=%d, CacheCreation=%d, CacheRead=%d, CacheCreation5m=%d, CacheCreation1h=%d, CacheTTL=%s",
			resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.TotalTokens,
			resp.Usage.CacheCreationInputTokens, resp.Usage.CacheReadInputTokens,
			resp.Usage.CacheCreation5mInputTokens, resp.Usage.CacheCreation1hInputTokens,
			resp.Usage.CacheTTL)
	}
}

// estimateResponsesOutputFromItems 从 ResponsesItem 数组估算输出 token
func estimateResponsesOutputFromItems(output []types.ResponsesItem) int {
	if len(output) == 0 {
		return 0
	}

	total := 0
	for _, item := range output {
		// 处理 content
		if item.Content != nil {
			switch v := item.Content.(type) {
			case string:
				total += utils.EstimateTokens(v)
			case []interface{}:
				for _, block := range v {
					if b, ok := block.(map[string]interface{}); ok {
						if text, ok := b["text"].(string); ok {
							total += utils.EstimateTokens(text)
						}
					}
				}
			case []types.ContentBlock:
				// 处理结构化 ContentBlock 数组
				for _, block := range v {
					if block.Text != "" {
						total += utils.EstimateTokens(block.Text)
					}
				}
			default:
				// 回退：序列化后估算
				data, _ := json.Marshal(v)
				total += utils.EstimateTokens(string(data))
			}
		}

		// 处理 tool_use
		if item.ToolUse != nil {
			if item.ToolUse.Name != "" {
				total += utils.EstimateTokens(item.ToolUse.Name) + 2
			}
			if item.ToolUse.Input != nil {
				data, _ := json.Marshal(item.ToolUse.Input)
				total += utils.EstimateTokens(string(data))
			}
		}

		// 处理 function_call 类型（item.Type == "function_call"）
		if item.Type == "function_call" {
			// 在转换后的响应中，function_call 的参数可能在 Content 中
			if contentStr, ok := item.Content.(string); ok {
				total += utils.EstimateTokens(contentStr)
			}
		}

		if item.Summary != nil {
			data, _ := json.Marshal(item.Summary)
			total += utils.EstimateTokens(string(data))
		}
	}

	return total
}

// hasResponsesContent 判断 SSE 事件是否包含实际 output。兼容只在
// response.completed 中返回完整 output、没有逐段 delta 的上游。
func hasResponsesContent(event string) bool {
	for _, line := range strings.Split(event, "\n") {
		jsonStr, isData := utils.SSEDataJSON(line)
		if !isData {
			continue
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
			continue
		}
		eventType, _ := data["type"].(string)
		if eventType == "response.completed" {
			if response, ok := data["response"].(map[string]interface{}); ok {
				if output, ok := response["output"].([]interface{}); ok && len(output) > 0 {
					return true
				}
			}
		}
		switch eventType {
		case "response.output_text.delta",
			"response.function_call_arguments.delta",
			"response.reasoning_summary_text.delta",
			"response.output_json.delta",
			"response.content_part.delta",
			"response.audio.delta",
			"response.audio_transcript.delta",
			"response.output_item.added",
			"response.output_text.done",
			"response.reasoning_summary_text.done",
			"response.function_call_arguments.done",
			"response.output_item.done":
			return true
		}
	}
	return false
}

func responsesStreamEventError(event string) error {
	for _, line := range strings.Split(event, "\n") {
		payload, isData := utils.SSEDataJSON(line)
		if !isData {
			continue
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			continue
		}
		eventType, _ := data["type"].(string)
		if eventType != "response.failed" && eventType != "error" {
			continue
		}
		if proxycore.IsUpstreamModelCapacityError([]byte(payload)) {
			return proxycore.NewRetrySameCandidateError(ErrEmptyStreamResponse)
		}
		return fmt.Errorf("upstream stream failed: %s", payload)
	}
	return nil
}

// hasDeliveredResponsesToolCall 判断事件是否已完整交付一个客户端可执行的工具调用。
// Codex 收到 output_item.done 后会立即进入工具执行阶段，并可能主动结束当前 SSE 连接；
// 这种终止代表本轮代理目标已经完成，不应记为普通客户端取消。
func hasDeliveredResponsesToolCall(event string) bool {
	for _, line := range strings.Split(event, "\n") {
		payload, isData := utils.SSEDataJSON(line)
		if !isData {
			continue
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			continue
		}
		if eventType, _ := data["type"].(string); eventType != "response.output_item.done" {
			continue
		}
		item, _ := data["item"].(map[string]interface{})
		itemType, _ := item["type"].(string)
		if itemType == "function_call" || itemType == "custom_tool_call" {
			return true
		}
	}
	return false
}

// handleStreamSuccess 处理流式响应。
// 返回 error 仅在"空响应未写入客户端"场景下非 nil（ErrEmptyStreamResponse），
// 使上层 UpstreamAttempt 的 Key/BaseURL 轮转可以安全 failover。
// 在缓冲阶段（尚未收到内容 delta 前），所有 SSE 事件只缓冲不 flush；
// 若最终 response.completed 时 output 为空，则返回 ErrEmptyStreamResponse。
func handleStreamSuccess(
	c *gin.Context,
	resp *http.Response,
	upstreamType string,
	envCfg *config.EnvConfig,
	sessionManager *session.SessionManager,
	startTime time.Time,
	originalReq *types.ResponsesRequest,
	originalRequestJSON []byte,
	channelScheduler *scheduler.ChannelScheduler,
	conversationID string,
) (*types.Usage, error) {
	if envCfg.EnableResponseLogs {
		responseTime := time.Since(startTime).Milliseconds()
		log.Printf("[Responses-Stream] Responses 流式响应开始: %dms, 状态: %d", responseTime, resp.StatusCode)
	}

	var synthesizer *utils.StreamSynthesizer
	var logBuffer bytes.Buffer
	streamLoggingEnabled := envCfg.IsDevelopment() && envCfg.EnableResponseLogs

	if streamLoggingEnabled {
		synthesizer = utils.NewStreamSynthesizer(upstreamType)
	}

	var converterState any

	flusher, _ := c.Writer.(http.Flusher)
	streamStarted := false
	startStream := func() {
		if streamStarted {
			return
		}
		utils.ForwardResponseHeaders(resp.Header, c.Writer)
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Status(resp.StatusCode)
		streamStarted = true
	}

	reader := bufio.NewReaderSize(resp.Body, 64*1024)

	// Token 统计状态
	var outputTextBuffer bytes.Buffer
	const maxOutputBufferSize = 1024 * 1024 // 1MB 上限，防止内存溢出
	var collectedUsage responsesStreamUsage
	hasUsage := false
	needTokenPatch := false
	clientGone := false
	toolCallDelivered := false
	var streamResponseID string

	// ---- 空响应检测：缓冲阶段 ----
	// 在收到第一个包含实际内容的事件前，所有事件暂存到 bufferedEvents，
	// 不调用 c.Writer.Write / flusher.Flush，确保 c.Writer.Written() == false。
	// 这样若最终发现是空响应（如 model at capacity），上层可安全 failover。
	bufferedEvents := make([]string, 0, 64)
	bufferedBytes := 0
	const maxBufferedEventBytes = 256 * 1024
	buffering := true
	writeEvent := func(event string) bool {
		if clientGone {
			return false
		}
		startStream()
		proxycore.MarkRequestLogFirstToken(c)
		if _, err := c.Writer.Write([]byte(event)); err != nil {
			clientGone = true
			if !isClientDisconnectError(err) {
				log.Printf("[Responses-Stream] 警告: 流式响应传输错误: %v", err)
			}
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	flushBufferedEvents := func() {
		for _, buffered := range bufferedEvents {
			if !writeEvent(buffered) {
				break
			}
		}
		bufferedEvents = nil
		bufferedBytes = 0
	}

	var streamReadErr error
	for {
		rawLine, readErr := reader.ReadBytes('\n')
		if len(rawLine) == 0 && readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				streamReadErr = readErr
			}
			break
		}

		line := strings.TrimSuffix(strings.TrimSuffix(string(rawLine), "\n"), "\r")

		if streamLoggingEnabled {
			logBuffer.Write(rawLine)
			if synthesizer != nil {
				synthesizer.ProcessLine(line)
			}
		}

		// 处理转换后的事件
		var eventsToProcess []string

		events, err := converters.ConvertUpstreamStreamLineToResponses(
			c.Request.Context(),
			upstreamType,
			originalReq.Model,
			originalRequestJSON,
			[]byte(line),
			&converterState,
		)
		if err != nil {
			log.Printf("[Responses-Stream] 流式响应转换失败: %v", err)
			streamReadErr = fmt.Errorf("转换上游流式响应失败: %w", err)
			break
		}
		eventsToProcess = events

		for _, event := range eventsToProcess {
			if eventErr := responsesStreamEventError(event); eventErr != nil {
				return nil, eventErr
			}
			// 提取文本内容用于估算（限制缓冲区大小）
			if outputTextBuffer.Len() < maxOutputBufferSize {
				extractResponsesTextFromEvent(event, &outputTextBuffer)
			}

			// 检测并收集 usage
			detected, needPatch, usageData := checkResponsesEventUsage(event, envCfg.EnableResponseLogs && envCfg.ShouldLog("debug"))
			if detected {
				if !hasUsage {
					hasUsage = true
					needTokenPatch = needPatch
					if envCfg.EnableResponseLogs && envCfg.ShouldLog("debug") && needPatch {
						log.Printf("[Responses-Stream-Token] 检测到虚假值, 延迟到流结束修补")
					}
				}
				updateResponsesStreamUsage(&collectedUsage, usageData)
			}

			// 在 response.completed 事件前注入/修补 usage
			eventToSend := event
			if isResponsesCompletedEvent(event) {
				if !hasUsage {
					// 上游完全没有 usage，注入本地估算
					var injectedInput, injectedOutput int
					eventToSend, injectedInput, injectedOutput = injectResponsesUsageToCompletedEvent(event, originalRequestJSON, outputTextBuffer.String(), envCfg)
					// 更新 collectedUsage 以便最终日志输出
					collectedUsage.InputTokens = injectedInput
					collectedUsage.OutputTokens = injectedOutput
					collectedUsage.TotalTokens = injectedInput + injectedOutput
					if envCfg.EnableResponseLogs && envCfg.ShouldLog("debug") {
						log.Printf("[Responses-Stream-Token] 上游无usage, 注入本地估算: input=%d, output=%d", injectedInput, injectedOutput)
					}
				} else if needTokenPatch {
					// 需要修补虚假值
					eventToSend = patchResponsesCompletedEventUsage(event, originalRequestJSON, outputTextBuffer.String(), &collectedUsage, envCfg)
				}
			}
			if streamResponseID == "" {
				streamResponseID = extractResponsesCompletedID(eventToSend)
			}
			text, toolArguments := extractResponsesStreamSafetyFragments(eventToSend)
			if err := hooks.FeedAttachedStreamText(c, text); err != nil {
				return nil, err
			}
			for _, toolArgument := range toolArguments {
				if err := hooks.FeedAttachedStreamToolArgumentsForKey(c, toolArgument.key, toolArgument.fragment); err != nil {
					return nil, err
				}
			}

			// ---- 缓冲阶段逻辑 ----
			if buffering {
				if hasResponsesContent(eventToSend) {
					// 收到第一个内容 delta，退出缓冲阶段
					// 先 flush 所有已缓冲事件（包括 response.created, response.in_progress 等）
					flushBufferedEvents()
					buffering = false
				} else {
					// 仍在缓冲阶段，暂存事件
					bufferedEvents = append(bufferedEvents, eventToSend)
					bufferedBytes += len(eventToSend)

					// 如果是 response.completed 且仍在缓冲阶段，说明是空响应
					// （没有收到任何内容 delta 就完成了）
					if isResponsesCompletedEvent(eventToSend) {
						log.Printf("[Responses-Stream] 检测到空响应 (model at capacity)，buffered=%d, written=%v",
							len(bufferedEvents), c.Writer.Written())
						// 不写入任何数据，返回 error 让上层 failover
						if envCfg.EnableResponseLogs {
							logBuffer.WriteString(eventToSend)
						}
						// 跳出循环，返回 ErrEmptyStreamResponse
						// 直接返回，不 flush 任何缓冲事件
						return nil, proxycore.NewRetrySameCandidateError(ErrEmptyStreamResponse)
					}
					if bufferedBytes > maxBufferedEventBytes {
						log.Printf("[Responses-Stream] 首个内容事件前的元数据超过 %d 字节，停止缓冲", maxBufferedEventBytes)
						flushBufferedEvents()
						buffering = false
					}
					continue // 缓冲阶段不执行下方 write 逻辑
				}
			}

			// 正常（已退出缓冲阶段）转发给客户端
			if writeEvent(eventToSend) && hasDeliveredResponsesToolCall(eventToSend) {
				toolCallDelivered = true
			}
		}

		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				streamReadErr = readErr
			}
			break
		}
	}

	contextErr := c.Request.Context().Err()
	toolCallCompletedCancellation := toolCallDelivered && errors.Is(contextErr, context.Canceled)
	if contextErr != nil && !toolCallCompletedCancellation {
		return nil, contextErr
	}
	if streamReadErr != nil && !toolCallCompletedCancellation {
		return nil, streamReadErr
	}
	if toolCallCompletedCancellation && envCfg.ShouldLog("info") {
		log.Printf("[Responses-Stream] 工具调用已完整交付，客户端结束当前流，按成功回合记录")
	}

	// 流正常结束但没有内容时才按瞬时空响应重试；不要覆盖转换或读取错误。
	if buffering && !c.Writer.Written() {
		log.Printf("[Responses-Stream] 流结束仍无内容，判定为空响应 (buffered=%d)", len(bufferedEvents))
		return nil, proxycore.NewRetrySameCandidateError(ErrEmptyStreamResponse)
	}
	if err := hooks.FlushAttachedStreamHooks(c); err != nil {
		return nil, err
	}

	if envCfg.EnableResponseLogs {
		responseTime := time.Since(startTime).Milliseconds()
		log.Printf("[Responses-Stream] Responses 流式响应完成: %dms", responseTime)

		// 输出 Token 统计
		if hasUsage || collectedUsage.InputTokens > 0 || collectedUsage.OutputTokens > 0 {
			log.Printf("[Responses-Stream-Token] InputTokens=%d, OutputTokens=%d, CacheCreation=%d, CacheRead=%d, CacheCreation5m=%d, CacheCreation1h=%d, CacheTTL=%s",
				collectedUsage.InputTokens, collectedUsage.OutputTokens,
				collectedUsage.CacheCreationInputTokens, collectedUsage.CacheReadInputTokens,
				collectedUsage.CacheCreation5mInputTokens, collectedUsage.CacheCreation1hInputTokens,
				collectedUsage.CacheTTL)
		}

		if envCfg.IsDevelopment() {
			if synthesizer != nil {
				synthesizedContent := synthesizer.GetSynthesizedContent()
				parseFailed := synthesizer.IsParseFailed()
				if synthesizedContent != "" && !parseFailed {
					log.Printf("[Responses-Stream] 上游流式响应合成内容:\n%s", strings.TrimSpace(synthesizedContent))
				} else if logBuffer.Len() > 0 {
					log.Printf("[Responses-Stream] 上游流式响应原始内容:\n%s", logBuffer.String())
				}
			} else if logBuffer.Len() > 0 {
				log.Printf("[Responses-Stream] 上游流式响应原始内容:\n%s", logBuffer.String())
			}
		}
	}

	// 即使客户端向上游声明 store=false，本代理仍需保存会话链，保证重启后可继续。
	if originalReq != nil {
		if sess, err := sessionManager.GetOrCreateSessionForConversation(originalReq.PreviousResponseID, conversationID); err == nil {
			inputItems, _ := parseInputToItems(originalReq.Input)
			turnItems := make([]types.ResponsesItem, 0, len(inputItems)+1)
			turnItems = append(turnItems, inputItems...)
			if outputText := strings.TrimSpace(outputTextBuffer.String()); outputText != "" {
				turnItems = append(turnItems, types.ResponsesItem{
					Type:    "text",
					Role:    "assistant",
					Content: outputText,
				})
			}
			if err := sessionManager.CommitTurn(sess.ID, turnItems, collectedUsage.TotalTokens, utils.DetectImageContent(originalRequestJSON), streamResponseID); err != nil {
				log.Printf("[Session] 持久化 Responses 流式会话轮次失败: %v", err)
			}
		} else {
			log.Printf("[Session] 保存 Responses 流式会话失败: %v", err)
		}
	}
	proxycore.AssociateConversationExternalID(channelScheduler, conversationID, scheduler.ChannelKindResponses, streamResponseID)

	// 返回收集到的 usage 数据
	return &types.Usage{
		InputTokens:                collectedUsage.InputTokens,
		OutputTokens:               collectedUsage.OutputTokens,
		CacheCreationInputTokens:   collectedUsage.CacheCreationInputTokens,
		CacheReadInputTokens:       collectedUsage.CacheReadInputTokens,
		CacheCreation5mInputTokens: collectedUsage.CacheCreation5mInputTokens,
		CacheCreation1hInputTokens: collectedUsage.CacheCreation1hInputTokens,
		CacheTTL:                   collectedUsage.CacheTTL,
	}, nil
}

// responsesStreamUsage 流式响应 usage 收集结构
type responsesStreamUsage struct {
	InputTokens                int
	OutputTokens               int
	TotalTokens                int // 用于检测 total_tokens 是否需要补全
	CacheCreationInputTokens   int
	CacheReadInputTokens       int
	CacheCreation5mInputTokens int
	CacheCreation1hInputTokens int
	CacheTTL                   string
	HasClaudeCache             bool // 是否检测到 Claude 原生缓存字段（区别于 OpenAI cached_tokens）
}

// extractResponsesTextFromEvent 从 Responses SSE 事件中提取文本内容
func extractResponsesTextFromEvent(event string, buf *bytes.Buffer) {
	for _, line := range strings.Split(event, "\n") {
		jsonStr, isData := utils.SSEDataJSON(line)
		if !isData {
			continue
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
			continue
		}

		eventType, _ := data["type"].(string)

		// 处理各种 delta 类型
		switch eventType {
		case "response.output_text.delta":
			if delta, ok := data["delta"].(string); ok {
				buf.WriteString(delta)
			}
		case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
			if delta, ok := data["delta"].(string); ok {
				buf.WriteString(delta)
			}
		case "response.reasoning_summary_text.delta":
			if delta, ok := data["delta"].(string); ok {
				buf.WriteString(delta)
			} else if text, ok := data["text"].(string); ok {
				buf.WriteString(text)
			}
		case "response.output_json.delta":
			// JSON 输出增量
			if delta, ok := data["delta"].(string); ok {
				buf.WriteString(delta)
			}
		case "response.content_part.delta":
			// 内容块增量（通用）
			if delta, ok := data["delta"].(string); ok {
				buf.WriteString(delta)
			} else if text, ok := data["text"].(string); ok {
				buf.WriteString(text)
			}
		case "response.audio.delta", "response.audio_transcript.delta":
			// 音频转录增量
			if delta, ok := data["delta"].(string); ok {
				buf.WriteString(delta)
			}
		}
	}
}

type responsesStreamToolArgumentFragment struct {
	key      string
	fragment string
}

func extractResponsesStreamSafetyFragments(event string) (string, []responsesStreamToolArgumentFragment) {
	var text strings.Builder
	toolArguments := make([]responsesStreamToolArgumentFragment, 0)
	for lineIndex, line := range strings.Split(event, "\n") {
		jsonStr, isData := utils.SSEDataJSON(line)
		if !isData {
			continue
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
			continue
		}
		eventType, _ := data["type"].(string)
		delta, _ := data["delta"].(string)
		switch eventType {
		case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.output_json.delta", "response.content_part.delta":
			if delta != "" {
				text.WriteString(delta)
			} else if value, _ := data["text"].(string); value != "" {
				text.WriteString(value)
			}
		case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
			if delta != "" {
				toolArguments = append(toolArguments, responsesStreamToolArgumentFragment{
					key:      responsesStreamToolArgumentKey(data, lineIndex),
					fragment: delta,
				})
			}
		}
	}
	return text.String(), toolArguments
}

func responsesStreamToolArgumentKey(data map[string]interface{}, fallback int) string {
	for _, field := range []string{"item_id", "call_id", "output_index"} {
		if value, exists := data[field]; exists {
			if key := strings.TrimSpace(fmt.Sprint(value)); key != "" {
				return "responses.tool." + key
			}
		}
	}
	return fmt.Sprintf("responses.tool.default.%d", fallback)
}

// checkResponsesEventUsage 检测 Responses 事件是否包含 usage
func checkResponsesEventUsage(event string, enableLog bool) (bool, bool, responsesStreamUsage) {
	lines := strings.Split(event, "\n")
	for _, line := range lines {
		jsonStr, isData := utils.SSEDataJSON(line)
		if !isData {
			continue
		}

		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
			continue
		}

		eventType, _ := data["type"].(string)

		// 检查 response.completed 事件中的 usage
		if eventType == "response.completed" {
			if response, ok := data["response"].(map[string]interface{}); ok {
				if usage, ok := response["usage"].(map[string]interface{}); ok {
					usageData := extractResponsesUsageFromMap(usage)
					needPatch := usageData.InputTokens <= 1 || usageData.OutputTokens <= 1

					// 仅当检测到 Claude 原生缓存字段时，才跳过 input_tokens 补全
					// OpenAI 的 input_tokens_details.cached_tokens 不应阻止补全
					if usageData.HasClaudeCache && usageData.InputTokens <= 1 {
						needPatch = usageData.OutputTokens <= 1 // 有 Claude 缓存时只检查 output
					}

					// 检查 total_tokens 是否需要补全（有效 input/output 但 total=0）
					if !needPatch && usageData.TotalTokens == 0 && (usageData.InputTokens > 0 || usageData.OutputTokens > 0) {
						needPatch = true
					}

					if enableLog {
						log.Printf("[Responses-Stream-Token] response.completed: InputTokens=%d, OutputTokens=%d, TotalTokens=%d, CacheCreation=%d, CacheRead=%d, HasClaudeCache=%v, 需补全=%v",
							usageData.InputTokens, usageData.OutputTokens, usageData.TotalTokens, usageData.CacheCreationInputTokens, usageData.CacheReadInputTokens, usageData.HasClaudeCache, needPatch)
					}
					return true, needPatch, usageData
				} else if enableLog {
					log.Printf("[Responses-Stream-Token] response.completed 事件中无 usage 字段")
				}
			} else if enableLog {
				log.Printf("[Responses-Stream-Token] response.completed 事件中无 response 字段")
			}
		}
	}
	return false, false, responsesStreamUsage{}
}

// extractResponsesUsageFromMap 从 usage map 中提取数据
func extractResponsesUsageFromMap(usage map[string]interface{}) responsesStreamUsage {
	var data responsesStreamUsage

	if v, ok := usage["input_tokens"].(float64); ok {
		data.InputTokens = int(v)
	}
	if v, ok := usage["output_tokens"].(float64); ok {
		data.OutputTokens = int(v)
	}
	if v, ok := usage["total_tokens"].(float64); ok {
		data.TotalTokens = int(v)
	}
	if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
		data.CacheCreationInputTokens = int(v)
		if v > 0 {
			data.HasClaudeCache = true
		}
	}
	_, hasExplicitCacheRead := usage["cache_read_input_tokens"]
	if v, ok := usage["cache_read_input_tokens"].(float64); ok {
		data.CacheReadInputTokens = int(v)
		if v > 0 {
			data.HasClaudeCache = true
		}
	}
	if v, ok := usage["cache_creation_5m_input_tokens"].(float64); ok {
		data.CacheCreation5mInputTokens = int(v)
		if v > 0 {
			data.HasClaudeCache = true
		}
	}
	if v, ok := usage["cache_creation_1h_input_tokens"].(float64); ok {
		data.CacheCreation1hInputTokens = int(v)
		if v > 0 {
			data.HasClaudeCache = true
		}
	}

	// 检查 input_tokens_details.cached_tokens (OpenAI 格式，不设置 HasClaudeCache)
	openAICachedTokens := 0
	if details, ok := usage["input_tokens_details"].(map[string]interface{}); ok {
		if cached, ok := details["cached_tokens"].(float64); ok && cached > 0 {
			openAICachedTokens = int(cached)
		}
	}
	if openAICachedTokens == 0 {
		if details, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
			if cached, ok := details["cached_tokens"].(float64); ok && cached > 0 {
				openAICachedTokens = int(cached)
			}
		}
	}
	if openAICachedTokens > 0 {
		// 仅当 CacheReadInputTokens 未被设置时才使用 OpenAI 的 cached_tokens
		if data.CacheReadInputTokens == 0 {
			data.CacheReadInputTokens = openAICachedTokens
		}
		if !hasExplicitCacheRead && data.InputTokens > openAICachedTokens {
			data.InputTokens -= openAICachedTokens
		}
		// 注意：不设置 HasClaudeCache，因为这是 OpenAI 格式
	}

	// 设置 CacheTTL
	var has5m, has1h bool
	if data.CacheCreation5mInputTokens > 0 {
		has5m = true
	}
	if data.CacheCreation1hInputTokens > 0 {
		has1h = true
	}
	if has5m && has1h {
		data.CacheTTL = "mixed"
	} else if has1h {
		data.CacheTTL = "1h"
	} else if has5m {
		data.CacheTTL = "5m"
	}

	return data
}

// updateResponsesStreamUsage 更新收集的 usage 数据
func updateResponsesStreamUsage(collected *responsesStreamUsage, usageData responsesStreamUsage) {
	if usageData.InputTokens > collected.InputTokens {
		collected.InputTokens = usageData.InputTokens
	}
	if usageData.OutputTokens > collected.OutputTokens {
		collected.OutputTokens = usageData.OutputTokens
	}
	if usageData.TotalTokens > collected.TotalTokens {
		collected.TotalTokens = usageData.TotalTokens
	}
	if usageData.CacheCreationInputTokens > 0 {
		collected.CacheCreationInputTokens = usageData.CacheCreationInputTokens
	}
	if usageData.CacheReadInputTokens > 0 {
		collected.CacheReadInputTokens = usageData.CacheReadInputTokens
	}
	if usageData.CacheCreation5mInputTokens > 0 {
		collected.CacheCreation5mInputTokens = usageData.CacheCreation5mInputTokens
	}
	if usageData.CacheCreation1hInputTokens > 0 {
		collected.CacheCreation1hInputTokens = usageData.CacheCreation1hInputTokens
	}
	if usageData.CacheTTL != "" {
		collected.CacheTTL = usageData.CacheTTL
	}
	// 传播 HasClaudeCache 标志
	if usageData.HasClaudeCache {
		collected.HasClaudeCache = true
	}
}

// isResponsesCompletedEvent 检测是否为 response.completed 事件
func isResponsesCompletedEvent(event string) bool {
	return strings.Contains(event, `"type":"response.completed"`) ||
		strings.Contains(event, `"type": "response.completed"`)
}

func extractResponsesCompletedID(event string) string {
	for _, line := range strings.Split(event, "\n") {
		jsonStr, isData := utils.SSEDataJSON(line)
		if !isData {
			continue
		}

		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
			continue
		}

		eventType, _ := data["type"].(string)
		if eventType != "response.completed" {
			continue
		}

		response, ok := data["response"].(map[string]interface{})
		if !ok {
			continue
		}

		responseID, _ := response["id"].(string)
		if responseID != "" {
			return responseID
		}
	}

	return ""
}

// isClientDisconnectError 判断是否为客户端断开连接错误
func isClientDisconnectError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset")
}

// injectResponsesUsageToCompletedEvent 向 response.completed 事件注入 usage
// 返回: 修改后的事件字符串, 估算的 inputTokens, 估算的 outputTokens
func injectResponsesUsageToCompletedEvent(event string, requestBody []byte, outputText string, envCfg *config.EnvConfig) (string, int, int) {
	inputTokens := utils.EstimateResponsesRequestTokens(requestBody)
	outputTokens := utils.EstimateTokens(outputText)
	totalTokens := inputTokens + outputTokens

	debugLog := envCfg.EnableResponseLogs && envCfg.ShouldLog("debug")

	// 调试日志：记录估算开始
	if debugLog {
		log.Printf("[Responses-Stream-Token] injectUsage 开始: inputTokens=%d, outputTokens=%d, event长度=%d",
			inputTokens, outputTokens, len(event))
	}

	rewritten, injected := utils.RewriteSSEDataLines(event, func(payload string) (string, bool) {
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			// 调试日志：JSON 解析失败
			if debugLog {
				log.Printf("[Responses-Stream-Token] JSON解析失败: %v, 内容前200字符: %.200s", err, payload)
			}
			return "", false
		}

		if eventType, _ := data["type"].(string); eventType != "response.completed" {
			return "", false
		}

		response, ok := data["response"].(map[string]interface{})
		if !ok {
			// response 字段缺失或类型错误，创建一个新的
			if debugLog {
				log.Printf("[Responses-Stream-Token] response字段缺失, 创建新的response对象")
			}
			response = make(map[string]interface{})
			data["response"] = response
		}

		response["usage"] = map[string]interface{}{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"total_tokens":  totalTokens,
		}

		patchedJSON, err := json.Marshal(data)
		if err != nil {
			if debugLog {
				log.Printf("[Responses-Stream-Token] JSON序列化失败: %v", err)
			}
			return "", false
		}

		if debugLog {
			log.Printf("[Responses-Stream-Token] 注入本地估算成功: InputTokens=%d, OutputTokens=%d, TotalTokens=%d",
				inputTokens, outputTokens, totalTokens)
		}
		return string(patchedJSON), true
	})

	if injected {
		return rewritten, inputTokens, outputTokens
	}

	// 逐行未命中：可能是 JSON 被拆到多个连续 data 行，合并后再试
	if debugLog {
		log.Printf("[Responses-Stream-Token] 逐行解析未找到, 尝试整体解析 event")
	}

	if merged, ok := injectUsageIntoMultiLineDataEvent(event, inputTokens, outputTokens, totalTokens); ok {
		if debugLog {
			log.Printf("[Responses-Stream-Token] 整体解析注入成功: InputTokens=%d, OutputTokens=%d",
				inputTokens, outputTokens)
		}
		return merged, inputTokens, outputTokens
	}

	// 仍然没有成功注入，记录警告并打印 event 内容
	if debugLog {
		// 打印 event 的前500个字符帮助调试
		eventPreview := event
		if len(eventPreview) > 500 {
			eventPreview = eventPreview[:500] + "..."
		}
		log.Printf("[Responses-Stream-Token] 警告: 未找到 response.completed 事件进行注入, event内容: %s", eventPreview)
	}
	return event, inputTokens, outputTokens
}

// injectUsageIntoMultiLineDataEvent 处理 JSON 被拆到多个连续 data 行的 response.completed 事件：
// 把这段连续 data 行的载荷拼成完整 JSON，注入 usage 后用一行 data 替换整段，其余行原样保留。
// 返回 ok=false 表示事件不是这种形态（无 data 行 / 拼不出合法 JSON / 不是 response.completed），调用方应保留原事件。
func injectUsageIntoMultiLineDataEvent(event string, inputTokens, outputTokens, totalTokens int) (string, bool) {
	lines := strings.Split(event, "\n")

	// 定位连续 data 行区间 [dataStart, dataEnd)
	dataStart := -1
	for i, line := range lines {
		if _, isData := utils.ParseSSEDataLine(line); isData {
			dataStart = i
			break
		}
	}
	if dataStart < 0 {
		return "", false
	}

	dataEnd := len(lines)
	var jsonBuilder strings.Builder
	for i := dataStart; i < len(lines); i++ {
		payload, isData := utils.ParseSSEDataLine(lines[i])
		if !isData {
			// 区间到首个非 data 行为止；dataEnd 保持 len(lines) 会把尾部行吞掉，必须显式记录
			dataEnd = i
			break
		}
		jsonBuilder.WriteString(payload)
	}

	fullJSON := jsonBuilder.String()
	if fullJSON == "" {
		return "", false
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(fullJSON), &data); err != nil {
		return "", false
	}
	if eventType, _ := data["type"].(string); eventType != "response.completed" {
		return "", false
	}

	response, ok := data["response"].(map[string]interface{})
	if !ok {
		response = make(map[string]interface{})
		data["response"] = response
	}
	response["usage"] = map[string]interface{}{
		"input_tokens":  inputTokens,
		"output_tokens": outputTokens,
		"total_tokens":  totalTokens,
	}

	patchedJSON, err := json.Marshal(data)
	if err != nil {
		return "", false
	}

	rebuilt := make([]string, 0, len(lines))
	rebuilt = append(rebuilt, lines[:dataStart]...)
	rebuilt = append(rebuilt, utils.SSEDataLinePrefix+string(patchedJSON))
	rebuilt = append(rebuilt, lines[dataEnd:]...)
	return strings.Join(rebuilt, "\n"), true
}

// patchResponsesCompletedEventUsage 修补 response.completed 事件中的 usage
func patchResponsesCompletedEventUsage(event string, requestBody []byte, outputText string, collected *responsesStreamUsage, envCfg *config.EnvConfig) string {
	rewritten, _ := utils.RewriteSSEDataLines(event, func(payload string) (string, bool) {
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			return "", false
		}
		if data["type"] != "response.completed" {
			return "", false
		}

		if response, ok := data["response"].(map[string]interface{}); ok {
			if usage, ok := response["usage"].(map[string]interface{}); ok {
				originalInput := collected.InputTokens
				originalOutput := collected.OutputTokens
				patched := false

				// 修补 input_tokens（仅当没有 Claude 原生缓存时）
				// OpenAI 的 cached_tokens 不应阻止 input_tokens 补全
				if collected.InputTokens <= 1 && !collected.HasClaudeCache {
					estimatedInput := utils.EstimateResponsesRequestTokens(requestBody)
					usage["input_tokens"] = estimatedInput
					collected.InputTokens = estimatedInput
					patched = true
				}

				// 修补 output_tokens
				if collected.OutputTokens <= 1 {
					estimatedOutput := utils.EstimateTokens(outputText)
					usage["output_tokens"] = estimatedOutput
					collected.OutputTokens = estimatedOutput
					patched = true
				}

				// 重新计算 total_tokens（修补时或 total_tokens 为 0 但 input/output 有效时）
				currentTotal := 0
				if t, ok := usage["total_tokens"].(float64); ok {
					currentTotal = int(t)
				}
				if patched || (currentTotal == 0 && (collected.InputTokens > 0 || collected.OutputTokens > 0)) {
					usage["total_tokens"] = collected.InputTokens + collected.OutputTokens
				}

				if envCfg.EnableResponseLogs && envCfg.ShouldLog("debug") && patched {
					log.Printf("[Responses-Stream-Token] 虚假值修补: InputTokens=%d->%d, OutputTokens=%d->%d",
						originalInput, collected.InputTokens, originalOutput, collected.OutputTokens)
				}
			}
		}

		patchedJSON, err := json.Marshal(data)
		if err != nil {
			return "", false
		}
		return string(patchedJSON), true
	})
	return rewritten
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
