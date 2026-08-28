package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// OpenAIProvider OpenAI 提供商
type OpenAIProvider struct{}

// ConvertToProviderRequest 转换为 OpenAI 请求
func (p *OpenAIProvider) ConvertToProviderRequest(c *gin.Context, upstream *config.UpstreamConfig, apiKey string) (*http.Request, []byte, error) {
	// 读取和解析原始请求体
	originalBodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("读取请求体失败: %w", err)
	}
	// 恢复请求体，以便gin context可以被其他地方再次读取（尽管这里我们已经完全处理了）
	c.Request.Body = io.NopCloser(bytes.NewReader(originalBodyBytes))

	var claudeReq types.ClaudeRequest
	if err := json.Unmarshal(originalBodyBytes, &claudeReq); err != nil {
		return nil, originalBodyBytes, fmt.Errorf("解析Claude请求体失败: %w", err)
	}

	// Chat 推理模型要求将 reasoning_content 原样随 assistant 历史消息回传。
	// 因此所有客户端统一保留历史 thinking，避免严格上游在下一轮拒绝请求。

	openaiReq := &types.OpenAIRequest{
		Model:       config.ResolveUpstreamModel(claudeReq.Model, upstream),
		Messages:    p.convertMessages(&claudeReq, upstream != nil && upstream.RequireReasoningContent),
		Stream:      claudeReq.Stream,
		Temperature: claudeReq.Temperature,
	}
	if claudeReq.Stream {
		openaiReq.StreamOptions = map[string]interface{}{"include_usage": true}
	}

	// 只发送一个 token 限制字段，避免部分 OpenAI 兼容网关因同时收到
	// max_tokens 和 max_completion_tokens 而返回 invalid_request。
	p.applyTokenLimit(openaiReq, &claudeReq, upstream)

	// 转换工具
	if len(claudeReq.Tools) > 0 {
		openaiReq.Tools = p.convertTools(claudeReq.Tools)
	}

	// 转换 tool_choice
	openaiReq.ToolChoice = p.convertToolChoice(claudeReq.ToolChoice)
	if len(openaiReq.Tools) == 0 {
		openaiReq.ToolChoice = nil
	}

	// 转换 thinking / output_config.effort → reasoning_effort
	// "auto" 在 OpenAI Chat Completions 无等价字段：省略即交由上游默认策略，避免硬塞无效值触发 400。
	if effort := resolveClaudeReasoningEffort(&claudeReq); effort != "" && effort != "auto" {
		openaiReq.ReasoningEffort = effort
	}
	// 稳定 prompt_cache_key：提升 OpenAI/兼容网关的缓存亲和；不支持端通常忽略。
	if upstream == nil || !upstream.DisablePromptCacheKey {
		openaiReq.PromptCacheKey = buildClaudeChatPromptCacheKey(&claudeReq, upstream, openaiReq.Model)
	}
	// --- 转换逻辑结束 ---

	reqBodyBytes, err := json.Marshal(openaiReq)
	if err != nil {
		return nil, originalBodyBytes, fmt.Errorf("序列化OpenAI请求体失败: %w", err)
	}

	// 构建URL（"#"后缀与版本前缀约定见 utils.BuildUpstreamURL）
	url := utils.BuildUpstreamURL(upstream.GetEffectiveBaseURL(), "/v1", "/chat/completions")

	req, err := http.NewRequestWithContext(c.Request.Context(), "POST", url, bytes.NewReader(reqBodyBytes))
	if err != nil {
		return nil, originalBodyBytes, fmt.Errorf("创建OpenAI请求失败: %w", err)
	}

	// 使用统一的头部处理逻辑（透明代理）
	// 保留客户端的大部分 headers，只移除/替换必要的认证和代理相关 headers
	req.Header = utils.PrepareUpstreamHeaders(c, req.URL.Host)

	// 删除开发测试注入的客户端请求头，避免上游严格验证报错。
	req.Header.Del("X-Codex-Window-Id")
	req.Header.Del("X-Codex-Installation-Id")
	req.Header.Del("X-Request-Id")
	req.Header.Del("X-Codex-Turn-Metadata")
	req.Header.Del("X-Claude-Code-Session-Id")
	req.Header.Del("X-Stainless-Lang")
	req.Header.Del("X-Stainless-Runtime")
	req.Header.Del("X-Stainless-Runtime-Version")
	req.Header.Del("X-Stainless-Os")
	req.Header.Del("X-Stainless-Arch")
	req.Header.Del("X-Stainless-Package-Version")
	req.Header.Del("X-Stainless-Retry-Count")
	req.Header.Del("X-Stainless-Timeout")
	req.Header.Del("X-App")

	utils.SetAuthenticationHeader(req.Header, apiKey)

	return req, originalBodyBytes, nil
}

// convertMessages 转换消息
func (p *OpenAIProvider) convertMessages(claudeReq *types.ClaudeRequest, forceReasoningContent bool) []types.OpenAIMessage {
	messages := []types.OpenAIMessage{}

	// 添加系统消息
	if claudeReq.System != nil {
		systemText := extractSystemText(claudeReq.System)
		if systemText != "" {
			messages = append(messages, types.OpenAIMessage{
				Role:    "system",
				Content: systemText,
			})
		}
	}

	for _, msg := range claudeReq.Messages {
		openaiMsg := p.convertMessage(msg, forceReasoningContent)
		messages = append(messages, openaiMsg...)
	}

	return messages
}

// convertMessage 转换单个消息
func (p *OpenAIProvider) convertMessage(msg types.ClaudeMessage, forceReasoningContent bool) []types.OpenAIMessage {
	messages := []types.OpenAIMessage{}

	// 如果是字符串内容
	if str, ok := msg.Content.(string); ok {
		if msg.Role != "tool" {
			openaiMsg := types.OpenAIMessage{
				Role:    normalizeRole(msg.Role),
				Content: str,
			}
			if openaiMsg.Role == "assistant" && forceReasoningContent {
				openaiMsg.ReasoningContent = reasoningContentForAssistantMessage(openaiMsg, true)
			}
			messages = append(messages, openaiMsg)
		}
		return messages
	}

	contents := utils.NormalizeContentBlocks(msg.Content)
	if len(contents) == 0 {
		return messages
	}

	textContents := []string{}
	reasoningContents := []string{}
	toolCalls := []types.OpenAIToolCall{}
	multimodalContents := []map[string]interface{}{}
	hasVisionContent := false
	hasRedactedThinking := false
	flushAssistantOrUserMessage := func() {
		if len(textContents) == 0 && len(reasoningContents) == 0 && len(toolCalls) == 0 && len(multimodalContents) == 0 {
			return
		}
		role := normalizeRole(msg.Role)
		if role == "tool" {
			textContents = nil
			toolCalls = nil
			multimodalContents = nil
			hasVisionContent = false
			return
		}

		openaiMsg := types.OpenAIMessage{
			Role: role,
		}
		if hasVisionContent {
			openaiMsg.Content = multimodalContents
		} else if len(textContents) > 0 {
			openaiMsg.Content = strings.Join(textContents, "")
		} else {
			openaiMsg.Content = nil
		}
		if len(toolCalls) > 0 {
			openaiMsg.ToolCalls = toolCalls
		}
		// 客户端回传历史时可能把明文 thinking 转成 redacted_thinking 或直接丢弃，
		// 导致本消息没有任何明文 reasoning。用完整 assistant 消息（文本 + 工具调用）
		// 从缓存补回，满足 Chat 推理模型（如 DeepSeek）对原样回传的校验。若旧会话
		// 重启后缓存已不存在，则工具调用历史至少携带兼容续接值，避免严格渠道因
		// reasoning_content 缺失直接拒绝请求。
		if role == "assistant" && len(reasoningContents) == 0 {
			if reasoning := reasoningContentForAssistantMessage(openaiMsg, forceReasoningContent || hasRedactedThinking); reasoning != "" {
				reasoningContents = append(reasoningContents, reasoning)
			}
		}
		if role == "assistant" && len(reasoningContents) > 0 {
			openaiMsg.ReasoningContent = strings.Join(reasoningContents, "")
		}

		messages = append(messages, openaiMsg)
		textContents = nil
		reasoningContents = nil
		toolCalls = nil
		multimodalContents = nil
		hasVisionContent = false
		hasRedactedThinking = false
	}

	for _, content := range contents {
		contentType, _ := content["type"].(string)

		switch contentType {
		case "thinking":
			if normalizeRole(msg.Role) == "assistant" {
				if thinking, _ := content["thinking"].(string); thinking != "" {
					reasoningContents = append(reasoningContents, thinking)
				}
			}
		case "redacted_thinking":
			// 记录该历史回合确实产生过推理；缓存失效时仍需补齐严格 Chat
			// 渠道所要求的 reasoning_content 字段。
			hasRedactedThinking = normalizeRole(msg.Role) == "assistant"
		case "text":
			if text, ok := content["text"].(string); ok {
				textContents = append(textContents, text)
				multimodalContents = append(multimodalContents, map[string]interface{}{
					"type": "text",
					"text": text,
				})
			}
		case "image", "image_url", "input_image":
			if imageBlock, ok := utils.ToOpenAIImageContentBlock(content); ok {
				hasVisionContent = true
				multimodalContents = append(multimodalContents, imageBlock)
			}

		case "tool_use":
			id, _ := content["id"].(string)
			name, _ := content["name"].(string)
			input := content["input"]

			inputJSON, _ := json.Marshal(input)
			toolCalls = append(toolCalls, types.OpenAIToolCall{
				ID:   id,
				Type: "function",
				Function: types.OpenAIToolCallFunction{
					Name:      name,
					Arguments: string(inputJSON),
				},
			})

		case "tool_result":
			if normalizeRole(msg.Role) != "user" {
				flushAssistantOrUserMessage()
			}

			toolUseID, _ := content["tool_use_id"].(string)
			resultContent := content["content"]

			var contentStr string
			if str, ok := resultContent.(string); ok {
				contentStr = str
			} else {
				contentJSON, _ := json.Marshal(resultContent)
				contentStr = string(contentJSON)
			}

			messages = append(messages, types.OpenAIMessage{
				Role:       "tool",
				ToolCallID: toolUseID,
				Content:    contentStr,
			})
		}
	}

	flushAssistantOrUserMessage()

	return messages
}

// convertTools 转换工具
func (p *OpenAIProvider) convertTools(claudeTools []types.ClaudeTool) []types.OpenAITool {
	tools := []types.OpenAITool{}

	for _, tool := range claudeTools {
		if tool.Name == "" || tool.Name == "BatchTool" {
			continue
		}
		tools = append(tools, types.OpenAITool{
			Type: "function",
			Function: types.OpenAIToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  cleanJsonSchema(tool.InputSchema),
			},
		})
	}

	return tools
}

// cleanJsonSchema 清理 JSON Schema，移除某些上游不支持的字段
func cleanJsonSchema(schema interface{}) interface{} {
	if schema == nil {
		return schema
	}

	// 如果是 map，递归清理
	if schemaMap, ok := schema.(map[string]interface{}); ok {
		cleaned := make(map[string]interface{})

		for key, value := range schemaMap {
			// 移除不需要的字段
			if key == "$schema" || key == "title" || key == "examples" || key == "additionalProperties" {
				continue
			}
			// 移除 format 字段（当类型为 string 时）
			if key == "format" {
				if schemaType, hasType := schemaMap["type"]; hasType && schemaType == "string" {
					continue
				}
			}
			// 递归处理嵌套对象
			if key == "properties" || key == "items" {
				cleaned[key] = cleanJsonSchema(value)
			} else if valueMap, isMap := value.(map[string]interface{}); isMap {
				cleaned[key] = cleanJsonSchema(valueMap)
			} else if valueSlice, isSlice := value.([]interface{}); isSlice {
				cleanedSlice := make([]interface{}, len(valueSlice))
				for i, item := range valueSlice {
					cleanedSlice[i] = cleanJsonSchema(item)
				}
				cleaned[key] = cleanedSlice
			} else {
				cleaned[key] = value
			}
		}

		return cleaned
	}

	// 如果是数组，递归清理每个元素
	if schemaSlice, ok := schema.([]interface{}); ok {
		cleaned := make([]interface{}, len(schemaSlice))
		for i, item := range schemaSlice {
			cleaned[i] = cleanJsonSchema(item)
		}
		return cleaned
	}

	// 其他类型直接返回
	return schema
}

// ConvertToClaudeResponse 转换为 Claude 响应
func (p *OpenAIProvider) ConvertToClaudeResponse(providerResp *types.ProviderResponse) (*types.ClaudeResponse, error) {
	// 少数 OpenAI 兼容网关会接受 Chat Completions 请求却返回 Responses
	// 结构。先识别这类成功响应，避免图片理解层因没有 choices 而丢失结果。
	var responseShape struct {
		Choices json.RawMessage `json:"choices"`
		Output  json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(providerResp.Body, &responseShape); err != nil {
		return nil, err
	}
	if len(responseShape.Output) > 0 && string(responseShape.Output) != "null" &&
		(len(responseShape.Choices) == 0 || string(responseShape.Choices) == "null" || string(responseShape.Choices) == "[]") {
		var responsesResp types.ResponsesResponse
		if err := json.Unmarshal(providerResp.Body, &responsesResp); err != nil {
			return nil, err
		}
		return responsesResponseToClaude(&responsesResp), nil
	}

	var openaiResp types.OpenAIResponse
	if err := json.Unmarshal(providerResp.Body, &openaiResp); err != nil {
		return nil, err
	}
	populateOpenAIReasoningContent(&openaiResp, providerResp.Body)

	var usageEnvelope openAIUsageEnvelope
	if err := json.Unmarshal(providerResp.Body, &usageEnvelope); err != nil {
		return nil, err
	}

	claudeResp := &types.ClaudeResponse{
		ID:      generateID(),
		Type:    "message",
		Role:    "assistant",
		Content: []types.ClaudeContent{},
	}

	if len(openaiResp.Choices) > 0 {
		choice := openaiResp.Choices[0]
		msg := choice.Message
		visibleContent := ""
		thinkingContent := ""
		if content, ok := msg.Content.(string); ok {
			var embeddedReasoning string
			visibleContent, embeddedReasoning = splitEmbeddedReasoning(content)
			thinkingContent = msg.ReasoningContent + embeddedReasoning
		} else {
			thinkingContent = msg.ReasoningContent
		}

		// 剥离 content 中与 reasoning_content 重复的前缀：部分上游在 content
		// 中原样重复了 reasoning_content 的内容，需要去除以避免正文出现两份。
		if thinkingContent != "" && visibleContent != "" {
			visibleContent = stripReasoningPrefixFromContent(visibleContent, thinkingContent)
		}

		// 缓存 reasoning_content，供客户端回传历史时补回（Chat 推理模型要求）。
		if thinkingContent != "" {
			cacheMessage := msg
			cacheMessage.Content = visibleContent
			storeReasoningForAssistantMessage(cacheMessage, thinkingContent)
		}

		// 添加 thinking content block（在 text block 之前）
		if thinkingContent != "" {
			claudeResp.Content = append(claudeResp.Content, types.ClaudeContent{
				Type:     "thinking",
				Thinking: thinkingContent,
			})
		}

		// 添加文本内容
		if visibleContent != "" {
			claudeResp.Content = append(claudeResp.Content, types.ClaudeContent{
				Type: "text",
				Text: visibleContent,
			})
		}

		// 添加工具调用
		for _, toolCall := range msg.ToolCalls {
			var input interface{}
			json.Unmarshal([]byte(toolCall.Function.Arguments), &input)

			claudeResp.Content = append(claudeResp.Content, types.ClaudeContent{
				Type:  "tool_use",
				ID:    toolCall.ID,
				Name:  toolCall.Function.Name,
				Input: input,
			})
		}

		// 设置停止原因
		if len(msg.ToolCalls) > 0 {
			claudeResp.StopReason = "tool_use"
		} else if choice.FinishReason == "length" {
			claudeResp.StopReason = "max_tokens"
		} else {
			claudeResp.StopReason = "end_turn"
		}
	}

	// 添加使用统计
	if openaiResp.Usage != nil {
		normalizedUsage := normalizeOpenAIUsage(openaiResp.Usage, usageEnvelope.Usage)

		claudeResp.Usage = &types.Usage{
			InputTokens:                normalizedUsage.InputTokens,
			OutputTokens:               normalizedUsage.OutputTokens,
			CacheCreationInputTokens:   normalizedUsage.CacheCreationInputTokens,
			CacheReadInputTokens:       normalizedUsage.CacheReadInputTokens,
			CacheCreation5mInputTokens: normalizedUsage.CacheCreation5mInputTokens,
			CacheCreation1hInputTokens: normalizedUsage.CacheCreation1hInputTokens,
			CacheTTL:                   normalizedUsage.CacheTTL,
		}
	}

	return claudeResp, nil
}

// HandleStreamResponse 处理流式响应
// 兼容旧签名：内部委托带 ctx 的版本，使用 context.Background()。
func (p *OpenAIProvider) HandleStreamResponse(body io.ReadCloser) (<-chan string, <-chan error, error) {
	return p.HandleStreamResponseCtx(context.Background(), body)
}

// HandleStreamResponseCtx 处理流式响应（支持客户端断连中止）
// 修复：所有向 eventChan 的发送都通过 send() 包装，select ctx.Done()，
// 客户端断连时立即停止读取上游并退出，杜绝"缓冲写满后永久阻塞"的 goroutine 泄漏。
func (p *OpenAIProvider) HandleStreamResponseCtx(ctx context.Context, body io.ReadCloser) (<-chan string, <-chan error, error) {
	pump := newStreamPump(ctx)
	eventChan, errChan := pump.eventChan, pump.errChan

	go func() {
		defer close(eventChan)
		defer close(errChan)
		defer body.Close()

		send := pump.send
		fail := pump.fail

		scanner := pump.newScanner(body)

		toolCallAccumulator := make(map[int]*ToolCallAccumulator)
		assistantToolCalls := make(map[int]types.OpenAIToolCall)
		nextBlockIndex := 0

		// 文本块状态跟踪
		textBlockStarted := false
		textBlockIndex := -1

		// reasoning_content / 文本累积器：用于缓存 (assistant 文本 → reasoning)，
		// 供客户端回传历史丢失明文 thinking 时自动补回（Chat 推理模型要求回传）。
		// 注意：assistantTextBuffer 刻意不复用 textDeltaBuffer（后者在 closeTextBlock 时会 Reset）。
		//
		// thinking block 状态：reasoning_content 不仅缓存，也作为 thinking content
		// block 发给 Messages 客户端，让 Claude Code 等原生支持 thinking 的客户端能
		// 在思考区渲染。回传时代理的 reasoning_content_cache 机制会自动补齐。
		var (
			thinkingBlockStarted bool
			thinkingBlockIndex   int = -1
			thinkingDeltaBuffer  strings.Builder
		)
		var reasoningDeltaBuffer strings.Builder
		var assistantTextBuffer strings.Builder
		embeddedReasoningMode := false

		// message_start 事件状态
		messageStartEmitted := false
		var streamModel string
		var textDeltaBuffer strings.Builder
		var streamUsage types.Usage
		hasStreamUsage := false
		pendingStopReason := ""
		messageDeltaEmitted := false

		// 发送 message_stop 的辅助函数
		emitMessageStop := func() {
			send("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		}
		emitMessageDelta := func(stopReason string) {
			if messageDeltaEmitted {
				return
			}
			if stopReason == "" {
				stopReason = "end_turn"
			}
			send(buildOpenAIMessageDeltaEvent(stopReason, streamUsage, hasStreamUsage))
			messageDeltaEmitted = true
		}

		emitTextDelta := func(text string) {
			if text == "" {
				return
			}
			deltaEvent := map[string]interface{}{
				"type":  "content_block_delta",
				"index": textBlockIndex,
				"delta": map[string]string{
					"type": "text_delta",
					"text": text,
				},
			}
			deltaJSON, _ := json.Marshal(deltaEvent)
			send(fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", deltaJSON))
		}

		flushTextDelta := func() {
			if !textBlockStarted || textDeltaBuffer.Len() == 0 {
				return
			}
			emitTextDelta(textDeltaBuffer.String())
			textDeltaBuffer.Reset()
		}

		// 关闭文本块的辅助函数
		closeTextBlock := func() {
			if !textBlockStarted {
				return
			}
			flushTextDelta()
			stopEvent := map[string]interface{}{
				"type":  "content_block_stop",
				"index": textBlockIndex,
			}
			stopJSON, _ := json.Marshal(stopEvent)
			send(fmt.Sprintf("event: content_block_stop\ndata: %s\n\n", stopJSON))
			textBlockStarted = false
			textBlockIndex = -1
		}
		// ===== thinking block 辅助函数 =====
		// reasoning_content 转成 Claude thinking content block 发给客户端，
		// 让 Claude Code 等原生支持 thinking 的客户端在思考区渲染。
		emitThinkingDelta := func(text string) {
			if text == "" {
				return
			}
			deltaEvent := map[string]interface{}{
				"type":  "content_block_delta",
				"index": thinkingBlockIndex,
				"delta": map[string]string{
					"type":     "thinking_delta",
					"thinking": text,
				},
			}
			deltaJSON, _ := json.Marshal(deltaEvent)
			send(fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", deltaJSON))
		}
		flushThinkingDelta := func() {
			if !thinkingBlockStarted || thinkingDeltaBuffer.Len() == 0 {
				return
			}
			emitThinkingDelta(thinkingDeltaBuffer.String())
			thinkingDeltaBuffer.Reset()
		}
		closeThinkingBlock := func() {
			if !thinkingBlockStarted {
				return
			}
			flushThinkingDelta()
			stopEvent := map[string]interface{}{
				"type":  "content_block_stop",
				"index": thinkingBlockIndex,
			}
			stopJSON, _ := json.Marshal(stopEvent)
			send(fmt.Sprintf("event: content_block_stop\ndata: %s\n\n", stopJSON))
			thinkingBlockStarted = false
			thinkingBlockIndex = -1
		}
		// 确保 thinking block 已开启：首次收到 reasoning_content 时创建。
		ensureThinkingBlockStarted := func() {
			if thinkingBlockStarted {
				return
			}
			thinkingBlockIndex = nextBlockIndex
			nextBlockIndex++
			startEvent := map[string]interface{}{
				"type":  "content_block_start",
				"index": thinkingBlockIndex,
				"content_block": map[string]string{
					"type":     "thinking",
					"thinking": "",
				},
			}
			startJSON, _ := json.Marshal(startEvent)
			send(fmt.Sprintf("event: content_block_start\ndata: %s\n\n", startJSON))
			thinkingBlockStarted = true
		}
		emitToolCallStart := func(acc *ToolCallAccumulator) {
			startEvent := map[string]interface{}{
				"type":  "content_block_start",
				"index": acc.BlockIndex,
				"content_block": map[string]interface{}{
					"type":  "tool_use",
					"id":    acc.ID,
					"name":  acc.Name,
					"input": map[string]interface{}{},
				},
			}
			startJSON, _ := json.Marshal(startEvent)
			send(fmt.Sprintf("event: content_block_start\ndata: %s\n\n", startJSON))
		}
		emitToolCallArgumentDelta := func(acc *ToolCallAccumulator, partialJSON string) {
			if partialJSON == "" {
				return
			}
			deltaEvent := map[string]interface{}{
				"type":  "content_block_delta",
				"index": acc.BlockIndex,
				"delta": map[string]string{
					"type":         "input_json_delta",
					"partial_json": partialJSON,
				},
			}
			deltaJSON, _ := json.Marshal(deltaEvent)
			send(fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", deltaJSON))
		}
		emitContentBlockStop := func(index int) {
			stopEvent := map[string]interface{}{
				"type":  "content_block_stop",
				"index": index,
			}
			stopJSON, _ := json.Marshal(stopEvent)
			send(fmt.Sprintf("event: content_block_stop\ndata: %s\n\n", stopJSON))
		}
		ensureToolCallStarted := func(acc *ToolCallAccumulator) bool {
			if acc == nil {
				return false
			}
			if acc.Started {
				return true
			}
			if acc.ID == "" || acc.Name == "" {
				return false
			}
			acc.BlockIndex = nextBlockIndex
			nextBlockIndex++
			acc.Started = true
			emitToolCallStart(acc)
			if acc.Arguments != "" {
				emitToolCallArgumentDelta(acc, acc.Arguments)
				acc.EmittedArgumentLen = len(acc.Arguments)
			}
			return true
		}
		emitPendingToolCallArgumentDelta := func(acc *ToolCallAccumulator) {
			if acc == nil || !acc.Started || acc.EmittedArgumentLen >= len(acc.Arguments) {
				return
			}
			emitToolCallArgumentDelta(acc, acc.Arguments[acc.EmittedArgumentLen:])
			acc.EmittedArgumentLen = len(acc.Arguments)
		}
		closeToolCall := func(index int) {
			acc := toolCallAccumulator[index]
			if acc == nil {
				return
			}
			if ensureToolCallStarted(acc) {
				emitPendingToolCallArgumentDelta(acc)
				emitContentBlockStop(acc.BlockIndex)
			}
			delete(toolCallAccumulator, index)
		}
		closeAllToolCalls := func() {
			if len(toolCallAccumulator) == 0 {
				return
			}
			indexes := make([]int, 0, len(toolCallAccumulator))
			for index := range toolCallAccumulator {
				indexes = append(indexes, index)
			}
			sort.Ints(indexes)
			for _, index := range indexes {
				closeToolCall(index)
			}
		}
		snapshotAssistantToolCalls := func() []types.OpenAIToolCall {
			if len(assistantToolCalls) == 0 {
				return nil
			}
			indexes := make([]int, 0, len(assistantToolCalls))
			for index := range assistantToolCalls {
				indexes = append(indexes, index)
			}
			sort.Ints(indexes)
			toolCalls := make([]types.OpenAIToolCall, 0, len(indexes))
			for _, index := range indexes {
				toolCalls = append(toolCalls, assistantToolCalls[index])
			}
			return toolCalls
		}
		finishStream := func() {
			// 缓存本轮完整 assistant 消息。工具调用回合通常没有文本，仍需用其
			// 工具调用内容关联 reasoning_content，供 Claude Code 下一轮回传时补回。
			if reasoningDeltaBuffer.Len() > 0 {
				storeReasoningForAssistantMessage(types.OpenAIMessage{
					Role:      "assistant",
					Content:   assistantTextBuffer.String(),
					ToolCalls: snapshotAssistantToolCalls(),
				}, reasoningDeltaBuffer.String())
			}
			closeThinkingBlock()
			closeTextBlock()
			closeAllToolCalls()
			if pendingStopReason == "" && messageStartEmitted {
				pendingStopReason = "end_turn"
			}
			if pendingStopReason != "" {
				emitMessageDelta(pendingStopReason)
			}
			emitMessageStop()
		}

		for scanner.Scan() {
			line := scanner.Text()
			line = strings.TrimSpace(line)

			if line == "" {
				continue
			}

			jsonStr, isData := utils.ParseSSEDataLine(line)
			if !isData {
				continue
			}
			if jsonStr == utils.SSEDoneMarker {
				finishStream()
				return
			}

			var chunk map[string]interface{}
			if err := json.Unmarshal([]byte(jsonStr), &chunk); err != nil {
				continue
			}
			if mergeOpenAIUsageFromChunk(chunk, &streamUsage) {
				hasStreamUsage = true
			}

			// 检查是否有错误
			if errObj, ok := chunk["error"]; ok {
				fail(fmt.Errorf("upstream error: %v", errObj))
				emitMessageStop()
				return
			}

			// 首次收到有效 chunk 时，提取 model 并发送 message_start
			if !messageStartEmitted {
				if m, ok := chunk["model"].(string); ok {
					streamModel = m
				}
				msgStart := map[string]interface{}{
					"type": "message_start",
					"message": map[string]interface{}{
						"id":            fmt.Sprintf("msg_%s", uuid.New().String()),
						"type":          "message",
						"role":          "assistant",
						"content":       []interface{}{},
						"model":         streamModel,
						"stop_reason":   nil,
						"stop_sequence": nil,
						"usage":         map[string]interface{}{"input_tokens": 0, "output_tokens": 1},
					},
				}
				startJSON, _ := json.Marshal(msgStart)
				send(fmt.Sprintf("event: message_start\ndata: %s\n\n", startJSON))
				messageStartEmitted = true
			}

			choices, ok := chunk["choices"].([]interface{})
			if !ok || len(choices) == 0 {
				continue
			}

			choice, ok := choices[0].(map[string]interface{})
			if !ok {
				continue
			}

			delta, ok := choice["delta"].(map[string]interface{})
			if !ok {
				continue
			}

			// reasoning_content 缓存 + 下发为 thinking content block。
			// 下一轮若上游要求原样回传，finishStream 会按最终正文/工具调用指纹从
			// 代理缓存中补齐；同时 Claude Code 等客户端能在思考区渲染思考过程。
			if reasoningContent := extractOpenAIReasoningContent(delta); reasoningContent != "" {
				reasoningDeltaBuffer.WriteString(reasoningContent)
				ensureThinkingBlockStarted()
				thinkingDeltaBuffer.WriteString(reasoningContent)
				if shouldFlushOpenAITextDelta(thinkingDeltaBuffer.String(), reasoningContent) {
					flushThinkingDelta()
				}
			}

			// 处理文本内容
			if content, ok := delta["content"].(string); ok && content != "" {
				visibleContent, embeddedReasoning := splitEmbeddedReasoningDelta(content, &embeddedReasoningMode)
				if embeddedReasoning != "" {
					reasoningDeltaBuffer.WriteString(embeddedReasoning)
					ensureThinkingBlockStarted()
					thinkingDeltaBuffer.WriteString(embeddedReasoning)
					if shouldFlushOpenAITextDelta(thinkingDeltaBuffer.String(), embeddedReasoning) {
						flushThinkingDelta()
					}
				}
				if visibleContent != "" {
					// 可见正文到达意味着推理阶段结束，先关闭 thinking block。
					closeThinkingBlock()
					assistantTextBuffer.WriteString(visibleContent)
					// 如果是第一个文本块,发送 content_block_start
					if !textBlockStarted {
						textBlockIndex = nextBlockIndex
						nextBlockIndex++
						startEvent := map[string]interface{}{
							"type":  "content_block_start",
							"index": textBlockIndex,
							"content_block": map[string]string{
								"type": "text",
								"text": "",
							},
						}
						startJSON, _ := json.Marshal(startEvent)
						send(fmt.Sprintf("event: content_block_start\ndata: %s\n\n", startJSON))
						textBlockStarted = true
					}

					textDeltaBuffer.WriteString(visibleContent)
					if shouldFlushOpenAITextDelta(textDeltaBuffer.String(), visibleContent) {
						flushTextDelta()
					}
				}
			}

			// 处理工具调用。部分 OpenAI 兼容上游会在普通文本 delta 中携带空 tool_calls: []，
			// 不能因此关闭文本块，否则 Claude Code 会把连续文本拆成多个 content block 显示。
			if toolCalls, ok := delta["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
				closeThinkingBlock()
				closeTextBlock()

				for _, tc := range toolCalls {
					toolCall, ok := tc.(map[string]interface{})
					if !ok {
						continue
					}

					index := 0
					if idx, ok := toolCall["index"].(float64); ok {
						index = int(idx)
					}

					// 获取或创建累加器
					if _, exists := toolCallAccumulator[index]; !exists {
						toolCallAccumulator[index] = &ToolCallAccumulator{BlockIndex: -1}
					}
					acc := toolCallAccumulator[index]

					// 累积数据
					if id, ok := toolCall["id"].(string); ok {
						acc.ID = id
					}

					if function, ok := toolCall["function"].(map[string]interface{}); ok {
						if name, ok := function["name"].(string); ok {
							acc.Name = name
						}
						if args, ok := function["arguments"].(string); ok {
							acc.Arguments += args
						}
					}
					assistantToolCalls[index] = types.OpenAIToolCall{
						ID:   acc.ID,
						Type: "function",
						Function: types.OpenAIToolCallFunction{
							Name:      acc.Name,
							Arguments: acc.Arguments,
						},
					}

					if ensureToolCallStarted(acc) {
						emitPendingToolCallArgumentDelta(acc)
					}
				}
			}

			// 处理结束原因
			if finishReason, ok := choice["finish_reason"].(string); ok && finishReason != "" && finishReason != "none" && finishReason != "null" {
				// 关闭所有未关闭的块
				closeThinkingBlock()
				closeTextBlock()
				closeAllToolCalls()

				// 根据 finish_reason 确定 stop_reason
				stopReason := "end_turn"
				if finishReason == "tool_calls" || finishReason == "function_call" {
					stopReason = "tool_use"
				} else if finishReason == "length" {
					stopReason = "max_tokens"
				}
				pendingStopReason = stopReason
				if stopReason == "tool_use" {
					finishStream()
					return
				}
			}
		}

		if err := scanner.Err(); err != nil {
			if isDisconnectLikeError(err) {
				// 客户端主动断开，仍然发送 message_stop
				finishStream()
				return
			}
			fail(err)
		}

		// 流正常结束，发送 message_stop
		finishStream()
	}()

	return eventChan, errChan, nil
}

// ToolCallAccumulator 工具调用累加器
type ToolCallAccumulator struct {
	ID                 string
	Name               string
	Arguments          string
	BlockIndex         int
	Started            bool
	EmittedArgumentLen int
}

type openAIUsageEnvelope struct {
	Usage *openAIUsageDetails `json:"usage"`
}

type openAIUsageDetails struct {
	InputTokens                int                `json:"input_tokens"`
	OutputTokens               int                `json:"output_tokens"`
	PromptTokens               int                `json:"prompt_tokens"`
	CompletionTokens           int                `json:"completion_tokens"`
	CacheCreationInputTokens   int                `json:"cache_creation_input_tokens"`
	CacheReadInputTokens       int                `json:"cache_read_input_tokens"`
	CacheCreation5mInputTokens int                `json:"cache_creation_5m_input_tokens"`
	CacheCreation1hInputTokens int                `json:"cache_creation_1h_input_tokens"`
	CacheTTL                   string             `json:"cache_ttl"`
	CachedContentTokenCount    int                `json:"cachedContentTokenCount"`
	PromptTokenDetails         openAITokenDetails `json:"prompt_tokens_details"`
	InputTokenDetails          openAITokenDetails `json:"input_tokens_details"`
}

type openAITokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

func normalizeOpenAIUsage(base *types.Usage, details *openAIUsageDetails) types.Usage {
	var usage types.Usage
	if base != nil {
		usage = *base
	}
	if details != nil {
		usage.InputTokens = firstPositiveInt(details.InputTokens, details.PromptTokens, usage.InputTokens, usage.PromptTokens)
		usage.OutputTokens = firstPositiveInt(details.OutputTokens, details.CompletionTokens, usage.OutputTokens, usage.CompletionTokens)
		usage.CacheCreationInputTokens = firstPositiveInt(details.CacheCreationInputTokens, usage.CacheCreationInputTokens)
		cacheReadFromDetails := firstPositiveInt(
			details.InputTokenDetails.CachedTokens,
			details.PromptTokenDetails.CachedTokens,
			details.CachedContentTokenCount,
		)
		usage.CacheReadInputTokens = firstPositiveInt(
			details.CacheReadInputTokens,
			cacheReadFromDetails,
			usage.CacheReadInputTokens,
		)
		usage.CacheCreation5mInputTokens = firstPositiveInt(details.CacheCreation5mInputTokens, usage.CacheCreation5mInputTokens)
		usage.CacheCreation1hInputTokens = firstPositiveInt(details.CacheCreation1hInputTokens, usage.CacheCreation1hInputTokens)
		if details.CacheTTL != "" {
			usage.CacheTTL = details.CacheTTL
		}
		// input_tokens 收敛到 Anthropic uncached 口径。
		// Anthropic 契约由客户端自行求和：
		//   total_input = input_tokens + cache_read_input_tokens + cache_creation_input_tokens
		// 所以在下发 cache_read 的同时把总量塞进 input_tokens，规范客户端会把缓存前缀算两遍，
		// 上下文满度虚高，表现为 compact 之后立刻又触发 compact。
		//
		// 减不减由 cache 量的**字段名来源**决定，不看数值：
		//   - cached_tokens / prompt_tokens_details.cached_tokens / cachedContentTokenCount
		//     是 subset 式命名（OpenAI、Gemini 语义下 cached ⊆ prompt/input），此时上游报的
		//     input 是总量，必须减。
		//   - cache_read_input_tokens 是 Anthropic 式命名，上游本就按 uncached 报 input，
		//     再减一次就是双减（对照 converters.TestExtractUsageMetrics_ClaudeCacheReadDoesNotDoubleSubtract）。
		// 与 converters.ExtractUsageMetrics 同一判据，两条路径不允许分叉。
		//
		// 减法绑定在"本次 details 报出的 input 量"而非最终 usage.InputTokens，因此对流式
		// mergeOpenAIUsageFromChunk 的反复调用幂等：本 chunk 报了 input 就重算一次完整减法，
		// 没报就保留上一轮已减的值，不会二次相减。
		reportedInput := firstPositiveInt(details.InputTokens, details.PromptTokens)
		if reportedInput > 0 && cacheReadFromDetails > 0 && details.CacheReadInputTokens == 0 {
			usage.InputTokens = reportedInput - cacheReadFromDetails
			if usage.InputTokens < 0 {
				// 上游异常回包（cached > prompt）。钳制到 0 而不是放行负数：
				// 负数会流进客户端的上下文估算与 sqlite 指标日志。
				usage.InputTokens = 0
			}
		}
	}
	return usage
}

func mergeOpenAIUsageFromChunk(chunk map[string]interface{}, dst *types.Usage) bool {
	if chunk == nil || dst == nil {
		return false
	}
	usageRaw, ok := chunk["usage"]
	if !ok || usageRaw == nil {
		return false
	}

	payload, err := json.Marshal(map[string]interface{}{"usage": usageRaw})
	if err != nil {
		return false
	}
	var envelope openAIUsageEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Usage == nil {
		return false
	}
	normalized := normalizeOpenAIUsage(dst, envelope.Usage)
	*dst = normalized
	return openAIUsageHasData(normalized)
}

func buildOpenAIMessageDeltaEvent(stopReason string, usage types.Usage, hasUsage bool) string {
	// Cache fields are emitted for the admin stream collector. On the way to the client,
	// handlers/streams.StripCacheFieldsFromClaudeSSE drops only cache_creation_*/cache_ttl and
	// keeps cache_read_input_tokens, because an Anthropic-format client is expected to add it
	// back when sizing the context window. That addition is only correct because
	// normalizeOpenAIUsage has already reduced input_tokens to the uncached remainder --
	// the two must stay in lockstep: re-introducing total-style input_tokens here would
	// restore the double-count. Which client is on the other end is not knowable here, so do
	// not re-introduce a vendor name in this comment.
	usageMap := map[string]interface{}{
		"output_tokens": usage.OutputTokens,
	}
	if hasUsage {
		if usage.InputTokens > 0 {
			usageMap["input_tokens"] = usage.InputTokens
		}
		if usage.CacheCreationInputTokens > 0 {
			usageMap["cache_creation_input_tokens"] = usage.CacheCreationInputTokens
		}
		if usage.CacheReadInputTokens > 0 {
			usageMap["cache_read_input_tokens"] = usage.CacheReadInputTokens
			usageMap["input_tokens_details"] = map[string]interface{}{
				"cached_tokens": usage.CacheReadInputTokens,
			}
		}
		if usage.CacheCreation5mInputTokens > 0 {
			usageMap["cache_creation_5m_input_tokens"] = usage.CacheCreation5mInputTokens
		}
		if usage.CacheCreation1hInputTokens > 0 {
			usageMap["cache_creation_1h_input_tokens"] = usage.CacheCreation1hInputTokens
		}
		if usage.CacheTTL != "" {
			usageMap["cache_ttl"] = usage.CacheTTL
		}
	}
	deltaEvent := map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": usageMap,
	}
	deltaJSON, _ := json.Marshal(deltaEvent)
	return fmt.Sprintf("event: message_delta\ndata: %s\n\n", deltaJSON)
}

func openAIUsageHasData(usage types.Usage) bool {
	return usage.InputTokens > 0 ||
		usage.OutputTokens > 0 ||
		usage.CacheCreationInputTokens > 0 ||
		usage.CacheReadInputTokens > 0 ||
		usage.CacheCreation5mInputTokens > 0 ||
		usage.CacheCreation1hInputTokens > 0
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func shouldFlushOpenAITextDelta(buffer string, latest string) bool {
	if len(buffer) >= 96 {
		return true
	}
	if strings.Contains(latest, "\n") {
		return true
	}
	trimmed := strings.TrimSpace(buffer)
	if trimmed == "" {
		return false
	}
	for _, r := range []rune(trimmed) {
		switch r {
		case '。', '！', '？', '；', '：', '.', '!', '?', ';', ':':
			return true
		}
	}
	return false
}

// processToolUsePart 处理工具使用部分
func processToolUsePart(id, name string, input interface{}, index int) []string {
	events := []string{}

	// content_block_start
	startEvent := map[string]interface{}{
		"type":  "content_block_start",
		"index": index,
		"content_block": map[string]interface{}{
			"type":  "tool_use",
			"id":    id,
			"name":  name,
			"input": map[string]interface{}{},
		},
	}
	startJSON, _ := json.Marshal(startEvent)
	events = append(events, fmt.Sprintf("event: content_block_start\ndata: %s\n\n", startJSON))

	// content_block_delta
	inputJSON, _ := json.Marshal(input)
	deltaEvent := map[string]interface{}{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]string{
			"type":         "input_json_delta",
			"partial_json": string(inputJSON),
		},
	}
	deltaJSON, _ := json.Marshal(deltaEvent)
	events = append(events, fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", deltaJSON))

	// content_block_stop
	stopEvent := map[string]interface{}{
		"type":  "content_block_stop",
		"index": index,
	}
	stopJSON, _ := json.Marshal(stopEvent)
	events = append(events, fmt.Sprintf("event: content_block_stop\ndata: %s\n\n", stopJSON))

	return events
}

// 辅助函数

func (p *OpenAIProvider) applyTokenLimit(openaiReq *types.OpenAIRequest, claudeReq *types.ClaudeRequest, upstream *config.UpstreamConfig) {
	tokenValue := 65535
	if claudeReq.MaxCompletionTokens > 0 {
		tokenValue = claudeReq.MaxCompletionTokens
	} else if claudeReq.MaxTokens > 0 {
		tokenValue = claudeReq.MaxTokens
	}

	if shouldUseMaxCompletionTokens(openaiReq.Model, claudeReq, upstream) {
		openaiReq.MaxCompletionTokens = tokenValue
		return
	}

	openaiReq.MaxTokens = tokenValue
}

func shouldUseMaxCompletionTokens(targetModel string, claudeReq *types.ClaudeRequest, upstream *config.UpstreamConfig) bool {
	if claudeReq.MaxCompletionTokens > 0 {
		return true
	}

	// Kimi/Moonshot 兼容网关更容易接受 max_completion_tokens。
	if looksLikeKimiOrMoonshot(targetModel) {
		return true
	}

	if upstream == nil {
		return false
	}

	if looksLikeKimiOrMoonshot(upstream.Name) || looksLikeKimiOrMoonshot(upstream.BaseURL) {
		return true
	}

	for sourceModel, mappedModels := range upstream.ModelMapping {
		if sourceModel == claudeReq.Model && len(mappedModels) > 0 && looksLikeKimiOrMoonshot(mappedModels[0]) {
			return true
		}
	}

	return false
}

func looksLikeKimiOrMoonshot(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "kimi") || strings.Contains(value, "moonshot")
}

// convertToolChoice 转换 tool_choice
func (p *OpenAIProvider) convertToolChoice(toolChoice interface{}) interface{} {
	if toolChoice == nil {
		return nil
	}

	// 字符串格式：Claude "auto"/"any" → OpenAI "auto"/"required"
	if str, ok := toolChoice.(string); ok {
		switch str {
		case "any":
			return "required"
		case "auto":
			return "auto"
		case "none":
			return "none"
		default:
			return "auto"
		}
	}

	// 对象格式：Claude {type: "auto"/"any"/"tool", name: "..."} → OpenAI 对应格式
	if obj, ok := toolChoice.(map[string]interface{}); ok {
		tcType, _ := obj["type"].(string)
		switch tcType {
		case "auto":
			return "auto"
		case "any":
			return "required"
		case "none":
			return "none"
		case "tool":
			name, _ := obj["name"].(string)
			if name != "" {
				return map[string]interface{}{
					"type": "function",
					"function": map[string]string{
						"name": name,
					},
				}
			}
			return "auto"
		default:
			return "auto"
		}
	}

	return nil
}

// convertThinkingToReasoningEffort 保留给测试/兼容调用；实现统一走 resolveClaudeReasoningEffort。
func (p *OpenAIProvider) convertThinkingToReasoningEffort(thinking interface{}) string {
	return resolveClaudeReasoningEffort(&types.ClaudeRequest{Thinking: thinking})
}

// buildClaudeChatPromptCacheKey builds a stable prompt_cache_key for Messages->Chat.
func buildClaudeChatPromptCacheKey(claudeReq *types.ClaudeRequest, upstream *config.UpstreamConfig, model string) string {
	if claudeReq == nil {
		return ""
	}
	channel := ""
	baseURL := ""
	if upstream != nil {
		channel = strings.TrimSpace(upstream.Name)
		baseURL = strings.TrimRight(upstream.GetEffectiveBaseURL(), "/#")
	}
	stableParts := map[string]interface{}{
		"protocol": "claude-messages-to-openai-chat-v1",
		"model":    model,
		"channel":  channel,
		"baseURL":  baseURL,
		"system":   extractSystemText(claudeReq.System),
		"tools":    normalizeToolsForPromptCacheKey(claudeReq.Tools),
	}
	sum := sha256.Sum256([]byte(utils.CanonicalJSON(stableParts)))
	return "claude-chat-" + hex.EncodeToString(sum[:])[:24]
}

func extractSystemText(system interface{}) string {
	if str, ok := system.(string); ok {
		return str
	}

	// 可能是数组
	arr, ok := system.([]interface{})
	if !ok {
		return ""
	}

	parts := []string{}
	for _, item := range arr {
		obj, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		if obj["type"] == "text" {
			if text, ok := obj["text"].(string); ok {
				parts = append(parts, text)
			}
		}
	}

	return strings.Join(parts, "\n")
}

func normalizeRole(role string) string {
	role = strings.ToLower(role)
	switch role {
	case "user", "assistant", "system", "tool":
		return role
	default:
		return "user"
	}
}

func generateID() string {
	return fmt.Sprintf("msg_%d", time.Now().UnixNano())
}

// extractOpenAIMessageText 提取 OpenAI 响应消息中的纯文本（用于 reasoning 缓存键）。
// Content 可能是 string，也可能是部分 OpenAI 兼容网关返回的内容块数组。
func extractOpenAIMessageText(msg types.OpenAIMessage) string {
	switch v := msg.Content.(type) {
	case string:
		return v
	case []interface{}:
		var b strings.Builder
		for _, item := range v {
			obj, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if t, _ := obj["type"].(string); t == "text" {
				if text, ok := obj["text"].(string); ok {
					b.WriteString(text)
				}
			}
		}
		return b.String()
	}
	return ""
}

// extractOpenAIReasoningContent 兼容部分 Chat 网关使用的非标准推理字段。
// 对外仍统一发送 reasoning_content，满足严格渠道的历史消息校验。
func extractOpenAIReasoningContent(fields map[string]interface{}) string {
	for _, key := range []string{"reasoning_content", "reasoning", "thinking"} {
		if value, ok := fields[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

// splitEmbeddedReasoning 隐藏部分 Chat 网关把推理直接塞进 content 的常见标签格式。
// 没有标签时原样返回，避免误伤普通回答文本。
func splitEmbeddedReasoning(content string) (visible, reasoning string) {
	visibleBuilder := strings.Builder{}
	reasoningBuilder := strings.Builder{}
	reasoningMode := false
	visible, reasoning = splitEmbeddedReasoningDelta(content, &reasoningMode)
	visibleBuilder.WriteString(visible)
	reasoningBuilder.WriteString(reasoning)
	return visibleBuilder.String(), reasoningBuilder.String()
}

// stripReasoningPrefixFromContent 从 content 中剥离与 reasoning 重复的前缀。
// 部分上游在 content 中原样重复了 reasoning_content 的内容，导致正文出现两份
// 完全相同的思考文本。此函数检查 content 是否以 reasoning 的内容开头，
// 若是则去除重复前缀，返回剩余正文。
func stripReasoningPrefixFromContent(content string, reasoning string) string {
	if reasoning == "" || content == "" {
		return content
	}
	// 完全匹配：content 就是 reasoning 的原样副本
	if content == reasoning {
		return ""
	}
	// 前缀匹配：content 以 reasoning 开头，后面是正文
	if strings.HasPrefix(content, reasoning) {
		remaining := strings.TrimPrefix(content, reasoning)
		// 去除可能的前导空白/换行
		return strings.TrimLeft(remaining, "\r\n")
	}
	// 反向前缀匹配：reasoning 以 content 开头（reasoning 更长），
	// 说明 content 只是 reasoning 的一部分，无独立正文
	if strings.HasPrefix(reasoning, content) {
		return ""
	}
	return content
}

func splitEmbeddedReasoningDelta(content string, reasoningMode *bool) (visible, reasoning string) {
	if content == "" {
		return "", ""
	}
	if reasoningMode == nil {
		return content, ""
	}

	lower := strings.ToLower(content)
	visibleBuilder := strings.Builder{}
	reasoningBuilder := strings.Builder{}
	for len(content) > 0 {
		if *reasoningMode {
			closeIndex, closeLength := findEmbeddedReasoningClose(lower)
			if closeIndex < 0 {
				reasoningBuilder.WriteString(content)
				break
			}
			reasoningBuilder.WriteString(content[:closeIndex])
			content = content[closeIndex+closeLength:]
			lower = lower[closeIndex+closeLength:]
			*reasoningMode = false
			continue
		}

		openIndex, openLength := findEmbeddedReasoningOpen(lower)
		if openIndex < 0 {
			visibleBuilder.WriteString(content)
			break
		}
		visibleBuilder.WriteString(content[:openIndex])
		content = content[openIndex+openLength:]
		lower = lower[openIndex+openLength:]
		*reasoningMode = true
	}
	return visibleBuilder.String(), reasoningBuilder.String()
}

func findEmbeddedReasoningOpen(value string) (int, int) {
	thinkIndex := strings.Index(value, "<think>")
	thinkingIndex := strings.Index(value, "<thinking>")
	if thinkIndex < 0 {
		return thinkingIndex, len("<thinking>")
	}
	if thinkingIndex < 0 || thinkIndex < thinkingIndex {
		return thinkIndex, len("<think>")
	}
	return thinkingIndex, len("<thinking>")
}

func findEmbeddedReasoningClose(value string) (int, int) {
	thinkIndex := strings.Index(value, "</think>")
	thinkingIndex := strings.Index(value, "</thinking>")
	if thinkIndex < 0 {
		return thinkingIndex, len("</thinking>")
	}
	if thinkingIndex < 0 || thinkIndex < thinkingIndex {
		return thinkIndex, len("</think>")
	}
	return thinkingIndex, len("</thinking>")
}

func populateOpenAIReasoningContent(response *types.OpenAIResponse, body []byte) {
	if response == nil || len(response.Choices) == 0 {
		return
	}
	var envelope struct {
		Choices []struct {
			Message map[string]interface{} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return
	}
	for index := range response.Choices {
		if response.Choices[index].Message.ReasoningContent != "" || index >= len(envelope.Choices) {
			continue
		}
		response.Choices[index].Message.ReasoningContent = extractOpenAIReasoningContent(envelope.Choices[index].Message)
	}
}
