package providers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
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

	// --- 复用旧的转换逻辑 ---
	openaiReq := &types.OpenAIRequest{
		Model:       config.ResolveUpstreamModel(claudeReq.Model, upstream),
		Messages:    p.convertMessages(&claudeReq),
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

	// 转换 thinking → reasoning_effort
	if effort := p.convertThinkingToReasoningEffort(claudeReq.Thinking); effort != "" {
		openaiReq.ReasoningEffort = effort
	}
	// --- 转换逻辑结束 ---

	reqBodyBytes, err := json.Marshal(openaiReq)
	if err != nil {
		return nil, originalBodyBytes, fmt.Errorf("序列化OpenAI请求体失败: %w", err)
	}

	// 构建URL - baseURL可能已包含版本号(如/v1, /v2, /v1beta, /v2alpha等),需要智能拼接
	// 如果 baseURL 以 # 结尾，则跳过自动添加 /v1
	baseURL := upstream.GetEffectiveBaseURL()
	skipVersionPrefix := strings.HasSuffix(baseURL, "#")
	if skipVersionPrefix {
		baseURL = strings.TrimSuffix(baseURL, "#")
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	// 检查baseURL是否以版本号结尾(如/v1, /v2, /v1beta, /v2alpha等)
	// 使用正则表达式匹配 /v\d+[a-z]* 的模式(v后跟数字,可选字母后缀)
	versionPattern := regexp.MustCompile(`/v\d+[a-z]*$`)
	hasVersionSuffix := versionPattern.MatchString(baseURL)

	// 如果baseURL已经包含版本号或以#结尾,直接拼接/chat/completions
	// 否则拼接/v1/chat/completions
	endpoint := "/chat/completions"
	if !hasVersionSuffix && !skipVersionPrefix {
		endpoint = "/v1" + endpoint
	}
	url := baseURL + endpoint

	req, err := http.NewRequestWithContext(c.Request.Context(), "POST", url, bytes.NewReader(reqBodyBytes))
	if err != nil {
		return nil, originalBodyBytes, fmt.Errorf("创建OpenAI请求失败: %w", err)
	}

	// 使用统一的头部处理逻辑（透明代理）
	// 保留客户端的大部分 headers，只移除/替换必要的认证和代理相关 headers
	req.Header = utils.PrepareUpstreamHeaders(c, req.URL.Host)
	
	// 删除演练台模拟的客户端请求头（避免上游严格验证报错）
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
func (p *OpenAIProvider) convertMessages(claudeReq *types.ClaudeRequest) []types.OpenAIMessage {
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

	// 转换普通消息
	for _, msg := range claudeReq.Messages {
		openaiMsg := p.convertMessage(msg)
		messages = append(messages, openaiMsg...)
	}

	return messages
}

// convertMessage 转换单个消息
func (p *OpenAIProvider) convertMessage(msg types.ClaudeMessage) []types.OpenAIMessage {
	messages := []types.OpenAIMessage{}

	// 如果是字符串内容
	if str, ok := msg.Content.(string); ok {
		if msg.Role != "tool" {
			messages = append(messages, types.OpenAIMessage{
				Role:    normalizeRole(msg.Role),
				Content: str,
			})
		}
		return messages
	}

	contents := utils.NormalizeContentBlocks(msg.Content)
	if len(contents) == 0 {
		return messages
	}

	textContents := []string{}
	toolCalls := []types.OpenAIToolCall{}
	multimodalContents := []map[string]interface{}{}
	hasVisionContent := false
	flushAssistantOrUserMessage := func() {
		if len(textContents) == 0 && len(toolCalls) == 0 && len(multimodalContents) == 0 {
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

		messages = append(messages, openaiMsg)
		textContents = nil
		toolCalls = nil
		multimodalContents = nil
		hasVisionContent = false
	}

	for _, content := range contents {
		contentType, _ := content["type"].(string)

		switch contentType {
		case "thinking":
			// thinking 块不转发到 OpenAI（OpenAI 不支持历史 thinking），跳过
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
	var openaiResp types.OpenAIResponse
	if err := json.Unmarshal(providerResp.Body, &openaiResp); err != nil {
		return nil, err
	}

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

		// 添加文本内容
		if str, ok := msg.Content.(string); ok && str != "" {
			claudeResp.Content = append(claudeResp.Content, types.ClaudeContent{
				Type: "text",
				Text: str,
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
func (p *OpenAIProvider) HandleStreamResponse(body io.ReadCloser) (<-chan string, <-chan error, error) {
	eventChan := make(chan string, 100)
	errChan := make(chan error, 1)

	go func() {
		defer close(eventChan)
		// defer close(errChan) // 移除此行，避免竞态条件
		defer body.Close()

		scanner := bufio.NewScanner(body)
		// 设置更大的 buffer (1MB) 以处理大 JSON chunk，避免默认 64KB 限制
		const maxScannerBufferSize = 1024 * 1024 // 1MB
		scanner.Buffer(make([]byte, 0, 64*1024), maxScannerBufferSize)

		toolCallAccumulator := make(map[int]*ToolCallAccumulator)
		nextBlockIndex := 0

		// 文本块状态跟踪
		textBlockStarted := false
		textBlockIndex := -1

		// thinking 块状态跟踪
		thinkingBlockStarted := false
		thinkingBlockIndex := -1

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
			eventChan <- "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		}
		emitMessageDelta := func(stopReason string) {
			if messageDeltaEmitted {
				return
			}
			if stopReason == "" {
				stopReason = "end_turn"
			}
			eventChan <- buildOpenAIMessageDeltaEvent(stopReason, streamUsage, hasStreamUsage)
			messageDeltaEmitted = true
		}

		// 关闭 thinking 块的辅助函数
		closeThinkingBlock := func() {
			if !thinkingBlockStarted {
				return
			}
			// 发送 signature_delta（空签名字段，非 Claude 原生不支持真实签名）
			sigEvent := map[string]interface{}{
				"type":  "content_block_delta",
				"index": thinkingBlockIndex,
				"delta": map[string]string{
					"type":      "signature_delta",
					"signature": "",
				},
			}
			sigJSON, _ := json.Marshal(sigEvent)
			eventChan <- fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", sigJSON)

			// 发送 content_block_stop
			stopEvent := map[string]interface{}{
				"type":  "content_block_stop",
				"index": thinkingBlockIndex,
			}
			stopJSON, _ := json.Marshal(stopEvent)
			eventChan <- fmt.Sprintf("event: content_block_stop\ndata: %s\n\n", stopJSON)
			thinkingBlockStarted = false
			thinkingBlockIndex = -1
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
			eventChan <- fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", deltaJSON)
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
			eventChan <- fmt.Sprintf("event: content_block_stop\ndata: %s\n\n", stopJSON)
			textBlockStarted = false
			textBlockIndex = -1
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
			eventChan <- fmt.Sprintf("event: content_block_start\ndata: %s\n\n", startJSON)
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
			eventChan <- fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", deltaJSON)
		}
		emitContentBlockStop := func(index int) {
			stopEvent := map[string]interface{}{
				"type":  "content_block_stop",
				"index": index,
			}
			stopJSON, _ := json.Marshal(stopEvent)
			eventChan <- fmt.Sprintf("event: content_block_stop\ndata: %s\n\n", stopJSON)
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
		finishStream := func() {
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
			if line == "data: [DONE]" {
				finishStream()
				return
			}

			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			jsonStr := strings.TrimPrefix(line, "data: ")

			var chunk map[string]interface{}
			if err := json.Unmarshal([]byte(jsonStr), &chunk); err != nil {
				continue
			}
			if mergeOpenAIUsageFromChunk(chunk, &streamUsage) {
				hasStreamUsage = true
			}

			// 检查是否有错误
			if errObj, ok := chunk["error"]; ok {
				errChan <- fmt.Errorf("upstream error: %v", errObj)
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
				eventChan <- fmt.Sprintf("event: message_start\ndata: %s\n\n", startJSON)
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

			// 处理 thinking/reasoning 内容（必须在文本内容之前处理）
			if reasoningContent, ok := delta["reasoning_content"].(string); ok && reasoningContent != "" {
				// 如果有文本块正在进行，先关闭它
				closeTextBlock()

				if !thinkingBlockStarted {
					thinkingBlockIndex = nextBlockIndex
					nextBlockIndex++
					// 发送 thinking content_block_start
					startEvent := map[string]interface{}{
						"type":  "content_block_start",
						"index": thinkingBlockIndex,
						"content_block": map[string]string{
							"type":      "thinking",
							"thinking":  "",
							"signature": "",
						},
					}
					startJSON, _ := json.Marshal(startEvent)
					eventChan <- fmt.Sprintf("event: content_block_start\ndata: %s\n\n", startJSON)
					thinkingBlockStarted = true
				}

				// 发送 thinking_delta
				deltaEvent := map[string]interface{}{
					"type":  "content_block_delta",
					"index": thinkingBlockIndex,
					"delta": map[string]string{
						"type":     "thinking_delta",
						"thinking": reasoningContent,
					},
				}
				deltaJSON, _ := json.Marshal(deltaEvent)
				eventChan <- fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", deltaJSON)
			}

			// 处理文本内容
			if content, ok := delta["content"].(string); ok && content != "" {
				// 如果有 thinking 块正在进行，先关闭它
				closeThinkingBlock()

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
					eventChan <- fmt.Sprintf("event: content_block_start\ndata: %s\n\n", startJSON)
					textBlockStarted = true
				}

				textDeltaBuffer.WriteString(content)
				if shouldFlushOpenAITextDelta(textDeltaBuffer.String(), content) {
					flushTextDelta()
				}
			}

			// 处理工具调用。部分 OpenAI 兼容上游会在普通文本 delta 中携带空 tool_calls: []，
			// 不能因此关闭文本块，否则 Claude Code 会把连续文本拆成多个 content block 显示。
			if toolCalls, ok := delta["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
				// 如果有 thinking/文本块正在进行,先关闭它们
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
			errMsg := err.Error()
			if strings.Contains(errMsg, "broken pipe") ||
				strings.Contains(errMsg, "connection reset") ||
				strings.Contains(errMsg, "EOF") {
				// 客户端主动断开，仍然发送 message_stop
				finishStream()
				return
			}
			errChan <- err
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
		if cacheReadFromDetails > 0 && usage.InputTokens > cacheReadFromDetails {
			usage.InputTokens -= cacheReadFromDetails
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

	for sourceModel, mappedModel := range upstream.ModelMapping {
		if sourceModel == claudeReq.Model && looksLikeKimiOrMoonshot(mappedModel) {
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

// convertThinkingToReasoningEffort 将 Claude thinking 配置转换为 OpenAI reasoning_effort
func (p *OpenAIProvider) convertThinkingToReasoningEffort(thinking interface{}) string {
	if thinking == nil {
		return ""
	}

	if obj, ok := thinking.(map[string]interface{}); ok {
		thinkType, _ := obj["type"].(string)
		switch thinkType {
		case "enabled":
			// 有明确 budget 的 thinking → high reasoning effort
			return "high"
		case "adaptive":
			// adaptive 模式不设 reasoning_effort，使用上游默认
			return ""
		default:
			return ""
		}
	}

	return ""
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
