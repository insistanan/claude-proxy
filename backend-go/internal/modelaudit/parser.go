package modelaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

const maxResponseBodyBytes = 16 * 1024 * 1024

func parseProtocolResponse(response *http.Response, target TargetSnapshot, stream bool, result *ExecutionResult) error {
	if response == nil || response.Body == nil {
		return fmt.Errorf("上游响应为空")
	}
	result.HTTPStatus = response.StatusCode
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return parseHTTPFailure(response, result)
	}
	if stream && target.WireProtocol != ProtocolImages {
		return parseStreamingResponse(response.Body, target.WireProtocol, result)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBodyBytes+1))
	if err != nil {
		return fmt.Errorf("读取上游响应失败: %w", err)
	}
	if len(body) > maxResponseBodyBytes {
		return fmt.Errorf("上游响应超过 %d 字节限制", maxResponseBodyBytes)
	}
	return parseJSONResponse(body, target.WireProtocol, result)
}

func parseHTTPFailure(response *http.Response, result *ExecutionResult) error {
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if err != nil {
		return fmt.Errorf("读取上游错误响应失败: %w", err)
	}
	message := upstreamErrorMessage(body)
	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	code := ErrorCodeUpstream
	category := ErrorCategoryUpstream
	retryable := response.StatusCode >= 500
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		code = ErrorCodeAuthentication
		category = ErrorCategoryAuthentication
		retryable = false
	case http.StatusTooManyRequests:
		code = ErrorCodeRateLimit
		category = ErrorCategoryRateLimit
		retryable = true
	}
	result.Status = StatusFailed
	result.ProtocolTerminal = ProtocolTerminalFailed
	result.Failure = &ExecutionFailure{
		Code: code, Category: category, Message: message, Retryable: retryable, StatusCode: response.StatusCode,
	}
	return nil
}

func parseJSONResponse(body []byte, protocol Protocol, result *ExecutionResult) error {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("解析 %s JSON 响应失败: %w", protocol, err)
	}
	switch protocol {
	case ProtocolMessages:
		return parseMessagesObject(payload, result)
	case ProtocolResponses:
		return parseResponsesObject(payload, result)
	case ProtocolChat:
		return parseChatObject(payload, result)
	case ProtocolGemini:
		return parseGeminiObject(payload, result)
	case ProtocolImages:
		return parseImagesObject(payload, result)
	default:
		return fmt.Errorf("不支持解析协议 %q", protocol)
	}
}

func parseStreamingResponse(body io.Reader, protocol Protocol, result *ExecutionResult) error {
	switch protocol {
	case ProtocolResponses:
		return parseResponsesSSE(body, result)
	case ProtocolMessages:
		return parseMessagesSSE(body, result)
	case ProtocolChat:
		return parseChatSSE(body, result)
	case ProtocolGemini:
		return parseGeminiSSE(body, result)
	default:
		return fmt.Errorf("协议 %q 不支持 SSE", protocol)
	}
}

func parseMessagesObject(payload map[string]interface{}, result *ExecutionResult) error {
	result.ResponseID = stringField(payload, "id")
	result.ReturnedModel = stringField(payload, "model")
	text, tools, err := messagesContent(payload["content"])
	if err != nil {
		return err
	}
	result.Text, result.ToolCalls = text, tools
	result.Usage = usageFromMap(mapField(payload, "usage"))
	result.ProtocolTerminal = ProtocolTerminalCompleted
	completeOrEmpty(result)
	return nil
}

func parseResponsesObject(payload map[string]interface{}, result *ExecutionResult) error {
	result.ResponseID = stringField(payload, "id")
	result.ReturnedModel = stringField(payload, "model")
	result.Usage = usageFromMap(mapField(payload, "usage"))
	text, tools, err := responsesOutput(payload["output"])
	if err != nil {
		return err
	}
	result.Text, result.ToolCalls = text, tools
	status := strings.ToLower(stringField(payload, "status"))
	switch status {
	case "completed", "":
		result.ProtocolTerminal = ProtocolTerminalCompleted
		completeOrEmpty(result)
	case "failed":
		result.Status = StatusFailed
		result.ProtocolTerminal = ProtocolTerminalFailed
		result.Failure = responseTerminalFailure(payload, ErrorCodeUpstream, ErrorCategoryUpstream, "Responses 执行失败")
	case "incomplete":
		result.Status = StatusIncomplete
		result.ProtocolTerminal = ProtocolTerminalIncomplete
		result.Failure = responseTerminalFailure(payload, ErrorCodeIncomplete, ErrorCategoryProtocol, "Responses 执行不完整")
	default:
		return fmt.Errorf("Responses 返回未知终态 %q", status)
	}
	return nil
}

func parseChatObject(payload map[string]interface{}, result *ExecutionResult) error {
	result.ResponseID = stringField(payload, "id")
	result.ReturnedModel = stringField(payload, "model")
	result.Usage = usageFromMap(mapField(payload, "usage"))
	choices, _ := payload["choices"].([]interface{})
	finishReason := ""
	for _, rawChoice := range choices {
		choice, _ := rawChoice.(map[string]interface{})
		finishReason = stringField(choice, "finish_reason")
		message := mapField(choice, "message")
		result.Text += textValue(message["content"])
		tools, err := chatToolCalls(message["tool_calls"])
		if err != nil {
			return err
		}
		result.ToolCalls = append(result.ToolCalls, tools...)
	}
	if finishReason == "" {
		return fmt.Errorf("Chat 响应缺少 finish_reason")
	}
	result.ProtocolTerminal = ProtocolTerminalCompleted
	completeOrEmpty(result)
	return nil
}

func parseGeminiObject(payload map[string]interface{}, result *ExecutionResult) error {
	result.ResponseID = stringField(payload, "responseId")
	result.ReturnedModel = stringField(payload, "modelVersion")
	result.Usage = usageFromMap(mapField(payload, "usageMetadata"))
	text, tools, finishReason, err := geminiCandidates(payload["candidates"])
	if err != nil {
		return err
	}
	result.Text = text
	result.ToolCalls = tools
	if finishReason == "" {
		return fmt.Errorf("Gemini 响应缺少 finishReason")
	}
	result.ProtocolTerminal = ProtocolTerminalCompleted
	completeOrEmpty(result)
	return nil
}

func parseImagesObject(payload map[string]interface{}, result *ExecutionResult) error {
	data, ok := payload["data"].([]interface{})
	if !ok || len(data) == 0 {
		result.Status = StatusEmptyOutput
		result.ProtocolTerminal = ProtocolTerminalCompleted
		result.Failure = &ExecutionFailure{Code: ErrorCodeEmptyOutput, Category: ErrorCategoryProtocol, Message: "Images 响应未包含图片结果"}
		return nil
	}
	structured, err := json.Marshal(map[string]interface{}{"data": data})
	if err != nil {
		return err
	}
	result.StructuredOutput = structured
	result.ProtocolTerminal = ProtocolTerminalCompleted
	result.Status = StatusCompleted
	return nil
}

type responsesStreamState struct {
	result       *ExecutionResult
	delta        strings.Builder
	terminalText string
	terminal     bool
	lastSequence int
	seen         map[int]string
}

func parseResponsesSSE(body io.Reader, result *ExecutionResult) error {
	state := &responsesStreamState{result: result, lastSequence: -1, seen: make(map[int]string)}
	err := consumeSSE(body, state.consume)
	if err != nil {
		return err
	}
	if !state.terminal {
		setStreamTruncated(result, "Responses 流在协议终态前结束")
		return nil
	}
	if result.Status == StatusCompleted {
		deltaText := state.delta.String()
		if state.terminalText != "" && deltaText != "" && state.terminalText != deltaText {
			return fmt.Errorf("Responses 终态正文与累计 delta 不一致")
		}
		if state.terminalText != "" {
			result.Text = state.terminalText
		} else {
			result.Text = deltaText
		}
		if result.Text == "" && len(result.ToolCalls) == 0 {
			setEmptyOutput(result, "Responses completed 终态没有有效输出")
		}
	}
	return nil
}

func (s *responsesStreamState) consume(message sseMessage) error {
	if strings.TrimSpace(message.Data) == "" || strings.TrimSpace(message.Data) == "[DONE]" {
		return nil
	}
	var event map[string]interface{}
	if err := json.Unmarshal([]byte(message.Data), &event); err != nil {
		return fmt.Errorf("解析 Responses SSE 事件失败: %w", err)
	}
	eventType := stringField(event, "type")
	if eventType == "" {
		eventType = message.Event
	}
	sequence, hasSequence := intField(event, "sequence_number")
	if hasSequence {
		digest := sha256.Sum256([]byte(message.Data))
		digestText := hex.EncodeToString(digest[:])
		if previous, exists := s.seen[sequence]; exists {
			if previous != digestText {
				return fmt.Errorf("Responses SSE 序号 %d 出现冲突事件", sequence)
			}
			s.result.SSEEvents = append(s.result.SSEEvents, SSEEventSummary{Sequence: sequence, Type: eventType + ".duplicate", Bytes: len(message.Data)})
			return nil
		}
		if sequence < s.lastSequence {
			return fmt.Errorf("Responses SSE 事件乱序: %d 位于 %d 之后", sequence, s.lastSequence)
		}
		s.lastSequence = sequence
		s.seen[sequence] = digestText
	}
	s.result.SSEEvents = append(s.result.SSEEvents, SSEEventSummary{Sequence: sequence, Type: eventType, Bytes: len(message.Data)})

	switch eventType {
	case "response.created", "response.in_progress":
		response := mapField(event, "response")
		if s.result.ResponseID == "" {
			s.result.ResponseID = stringField(response, "id")
		}
	case "response.output_text.delta":
		s.delta.WriteString(stringField(event, "delta"))
	case "response.completed":
		response := mapField(event, "response")
		s.result.ResponseID = firstNonEmpty(stringField(response, "id"), s.result.ResponseID)
		s.result.ReturnedModel = stringField(response, "model")
		s.result.Usage = usageFromMap(mapField(response, "usage"))
		terminalText, tools, err := responsesOutput(response["output"])
		if err != nil {
			return err
		}
		s.terminalText, s.result.ToolCalls = terminalText, tools
		s.result.Status = StatusCompleted
		s.result.ProtocolTerminal = ProtocolTerminalCompleted
		s.terminal = true
	case "response.failed":
		response := mapField(event, "response")
		s.result.Status = StatusFailed
		s.result.ProtocolTerminal = ProtocolTerminalFailed
		s.result.Failure = responseTerminalFailure(response, ErrorCodeUpstream, ErrorCategoryUpstream, "Responses 执行失败")
		s.terminal = true
	case "response.incomplete":
		response := mapField(event, "response")
		s.result.Status = StatusIncomplete
		s.result.ProtocolTerminal = ProtocolTerminalIncomplete
		s.result.Failure = responseTerminalFailure(response, ErrorCodeIncomplete, ErrorCategoryProtocol, "Responses 执行不完整")
		s.terminal = true
	case "error":
		s.result.Status = StatusFailed
		s.result.ProtocolTerminal = ProtocolTerminalFailed
		s.result.Failure = &ExecutionFailure{Code: ErrorCodeUpstream, Category: ErrorCategoryUpstream, Message: upstreamEventError(event)}
		s.terminal = true
	}
	return nil
}

type streamedToolCall struct {
	ID               string
	Name             string
	InitialArguments interface{}
	ArgumentDelta    strings.Builder
}

func finalizeStreamedToolCalls(pending map[int]*streamedToolCall) ([]ToolCall, error) {
	indexes := make([]int, 0, len(pending))
	for index := range pending {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	tools := make([]ToolCall, 0, len(indexes))
	for _, index := range indexes {
		pendingCall := pending[index]
		if strings.TrimSpace(pendingCall.Name) == "" {
			return nil, fmt.Errorf("工具调用 %d 缺少名称", index)
		}
		var rawArguments interface{}
		if delta := strings.TrimSpace(pendingCall.ArgumentDelta.String()); delta != "" {
			rawArguments = delta
		} else if pendingCall.InitialArguments != nil {
			rawArguments = pendingCall.InitialArguments
		}
		arguments, err := normalizeToolArguments(rawArguments)
		if err != nil {
			return nil, fmt.Errorf("工具调用 %d 参数无效: %w", index, err)
		}
		tools = append(tools, ToolCall{ID: pendingCall.ID, Name: pendingCall.Name, Arguments: arguments})
	}
	return tools, nil
}

func parseMessagesSSE(body io.Reader, result *ExecutionResult) error {
	terminal := false
	pendingTools := make(map[int]*streamedToolCall)
	err := consumeSSE(body, func(message sseMessage) error {
		if message.Data == "" || message.Data == "[DONE]" {
			return nil
		}
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(message.Data), &event); err != nil {
			return err
		}
		eventType := firstNonEmpty(stringField(event, "type"), message.Event)
		result.SSEEvents = append(result.SSEEvents, SSEEventSummary{Sequence: len(result.SSEEvents), Type: eventType, Bytes: len(message.Data)})
		switch eventType {
		case "message_start":
			messageObject := mapField(event, "message")
			result.ResponseID = stringField(messageObject, "id")
			result.ReturnedModel = stringField(messageObject, "model")
			result.Usage = mergeUsage(result.Usage, usageFromMap(mapField(messageObject, "usage")))
		case "content_block_start":
			index, ok := intField(event, "index")
			contentBlock := mapField(event, "content_block")
			if stringField(contentBlock, "type") == "tool_use" {
				if !ok {
					return fmt.Errorf("Messages tool_use 缺少 content_block index")
				}
				if _, exists := pendingTools[index]; exists {
					return fmt.Errorf("Messages tool_use 重复使用 content_block index %d", index)
				}
				pendingTools[index] = &streamedToolCall{
					ID: stringField(contentBlock, "id"), Name: stringField(contentBlock, "name"), InitialArguments: contentBlock["input"],
				}
			}
		case "content_block_delta":
			delta := mapField(event, "delta")
			switch stringField(delta, "type") {
			case "text_delta":
				result.Text += stringField(delta, "text")
			case "input_json_delta":
				index, ok := intField(event, "index")
				pendingCall, exists := pendingTools[index]
				if !ok || !exists {
					return fmt.Errorf("Messages 工具参数增量缺少对应的 content_block_start")
				}
				pendingCall.ArgumentDelta.WriteString(stringField(delta, "partial_json"))
			}
		case "message_delta":
			result.Usage = mergeUsage(result.Usage, usageFromMap(mapField(event, "usage")))
		case "message_stop":
			terminal = true
			result.Status = StatusCompleted
			result.ProtocolTerminal = ProtocolTerminalCompleted
		case "error":
			terminal = true
			result.Status = StatusFailed
			result.ProtocolTerminal = ProtocolTerminalFailed
			result.Failure = &ExecutionFailure{Code: ErrorCodeUpstream, Category: ErrorCategoryUpstream, Message: upstreamEventError(event)}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("解析 Messages SSE 失败: %w", err)
	}
	if !terminal {
		setStreamTruncated(result, "Messages 流在 message_stop 前结束")
		return nil
	}
	if result.Status != StatusCompleted {
		return nil
	}
	result.ToolCalls, err = finalizeStreamedToolCalls(pendingTools)
	if err != nil {
		return fmt.Errorf("解析 Messages SSE 工具调用失败: %w", err)
	}
	if result.Text == "" && len(result.ToolCalls) == 0 {
		setEmptyOutput(result, "Messages completed 终态没有有效输出")
	}
	return nil
}

func parseChatSSE(body io.Reader, result *ExecutionResult) error {
	terminal := false
	pendingTools := make(map[int]*streamedToolCall)
	err := consumeSSE(body, func(message sseMessage) error {
		if message.Data == "" {
			return nil
		}
		if message.Data == "[DONE]" {
			return nil
		}
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(message.Data), &event); err != nil {
			return err
		}
		result.SSEEvents = append(result.SSEEvents, SSEEventSummary{Sequence: len(result.SSEEvents), Type: "chat.completion.chunk", Bytes: len(message.Data)})
		result.ResponseID = firstNonEmpty(stringField(event, "id"), result.ResponseID)
		result.ReturnedModel = firstNonEmpty(stringField(event, "model"), result.ReturnedModel)
		result.Usage = mergeUsage(result.Usage, usageFromMap(mapField(event, "usage")))
		choices, _ := event["choices"].([]interface{})
		for _, rawChoice := range choices {
			choice, _ := rawChoice.(map[string]interface{})
			delta := mapField(choice, "delta")
			result.Text += textValue(delta["content"])
			toolDeltas, _ := delta["tool_calls"].([]interface{})
			for _, rawToolDelta := range toolDeltas {
				toolDelta, _ := rawToolDelta.(map[string]interface{})
				index, ok := intField(toolDelta, "index")
				if !ok {
					return fmt.Errorf("Chat 工具调用增量缺少 index")
				}
				pendingCall, exists := pendingTools[index]
				if !exists {
					pendingCall = &streamedToolCall{}
					pendingTools[index] = pendingCall
				}
				pendingCall.ID = firstNonEmpty(stringField(toolDelta, "id"), pendingCall.ID)
				function := mapField(toolDelta, "function")
				pendingCall.Name = firstNonEmpty(stringField(function, "name"), pendingCall.Name)
				pendingCall.ArgumentDelta.WriteString(stringField(function, "arguments"))
			}
			if stringField(choice, "finish_reason") != "" {
				terminal = true
				result.Status = StatusCompleted
				result.ProtocolTerminal = ProtocolTerminalCompleted
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("解析 Chat SSE 失败: %w", err)
	}
	if !terminal {
		setStreamTruncated(result, "Chat 流在 finish_reason 前结束")
		return nil
	}
	result.ToolCalls, err = finalizeStreamedToolCalls(pendingTools)
	if err != nil {
		return fmt.Errorf("解析 Chat SSE 工具调用失败: %w", err)
	}
	if result.Text == "" && len(result.ToolCalls) == 0 {
		setEmptyOutput(result, "Chat completed 终态没有有效输出")
	}
	return nil
}

func parseGeminiSSE(body io.Reader, result *ExecutionResult) error {
	terminal := false
	err := consumeSSE(body, func(message sseMessage) error {
		if message.Data == "" || message.Data == "[DONE]" {
			return nil
		}
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(message.Data), &event); err != nil {
			return err
		}
		result.SSEEvents = append(result.SSEEvents, SSEEventSummary{Sequence: len(result.SSEEvents), Type: "gemini.candidate", Bytes: len(message.Data)})
		result.ResponseID = firstNonEmpty(stringField(event, "responseId"), result.ResponseID)
		result.ReturnedModel = firstNonEmpty(stringField(event, "modelVersion"), result.ReturnedModel)
		result.Usage = mergeUsage(result.Usage, usageFromMap(mapField(event, "usageMetadata")))
		text, tools, finishReason, err := geminiCandidates(event["candidates"])
		if err != nil {
			return err
		}
		result.Text += text
		result.ToolCalls = append(result.ToolCalls, tools...)
		if finishReason != "" {
			terminal = true
			result.Status = StatusCompleted
			result.ProtocolTerminal = ProtocolTerminalCompleted
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("解析 Gemini SSE 失败: %w", err)
	}
	if !terminal {
		setStreamTruncated(result, "Gemini 流在 finishReason 前结束")
	} else if result.Text == "" && len(result.ToolCalls) == 0 {
		setEmptyOutput(result, "Gemini completed 终态没有有效输出")
	}
	return nil
}

func completeOrEmpty(result *ExecutionResult) {
	if result.Text == "" && len(result.ToolCalls) == 0 && len(result.StructuredOutput) == 0 {
		setEmptyOutput(result, "协议完成终态没有有效输出")
		return
	}
	result.Status = StatusCompleted
}

func setEmptyOutput(result *ExecutionResult, message string) {
	result.Status = StatusEmptyOutput
	result.ProtocolTerminal = ProtocolTerminalCompleted
	result.Failure = &ExecutionFailure{Code: ErrorCodeEmptyOutput, Category: ErrorCategoryProtocol, Message: message}
}

func setStreamTruncated(result *ExecutionResult, message string) {
	result.Status = StatusStreamTruncated
	result.ProtocolTerminal = ProtocolTerminalNone
	result.Failure = &ExecutionFailure{Code: ErrorCodeStreamTruncated, Category: ErrorCategoryProtocol, Message: message, Retryable: true}
}

func normalizeToolArguments(raw interface{}) (json.RawMessage, error) {
	if raw == nil {
		return json.RawMessage(`{}`), nil
	}
	if text, ok := raw.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return json.RawMessage(`{}`), nil
		}
		if !json.Valid([]byte(text)) {
			return nil, fmt.Errorf("参数不是完整 JSON")
		}
		return append(json.RawMessage(nil), text...), nil
	}
	arguments, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("序列化参数失败: %w", err)
	}
	return arguments, nil
}

func messagesContent(raw interface{}) (string, []ToolCall, error) {
	items, _ := raw.([]interface{})
	var text strings.Builder
	tools := make([]ToolCall, 0)
	for _, rawItem := range items {
		item, _ := rawItem.(map[string]interface{})
		switch stringField(item, "type") {
		case "text":
			text.WriteString(stringField(item, "text"))
		case "tool_use":
			name := strings.TrimSpace(stringField(item, "name"))
			if name == "" {
				return "", nil, fmt.Errorf("Messages tool_use 缺少名称")
			}
			arguments, err := normalizeToolArguments(item["input"])
			if err != nil {
				return "", nil, fmt.Errorf("Messages tool_use 参数无效: %w", err)
			}
			tools = append(tools, ToolCall{ID: stringField(item, "id"), Name: name, Arguments: arguments})
		}
	}
	return text.String(), tools, nil
}

func responsesOutput(raw interface{}) (string, []ToolCall, error) {
	items, _ := raw.([]interface{})
	var text strings.Builder
	tools := make([]ToolCall, 0)
	for _, rawItem := range items {
		item, _ := rawItem.(map[string]interface{})
		switch stringField(item, "type") {
		case "message":
			content, _ := item["content"].([]interface{})
			for _, rawPart := range content {
				part, _ := rawPart.(map[string]interface{})
				if stringField(part, "type") == "output_text" {
					text.WriteString(stringField(part, "text"))
				}
			}
		case "function_call":
			name := strings.TrimSpace(stringField(item, "name"))
			if name == "" {
				return "", nil, fmt.Errorf("Responses function_call 缺少名称")
			}
			arguments, err := normalizeToolArguments(item["arguments"])
			if err != nil {
				return "", nil, fmt.Errorf("Responses function_call 参数无效: %w", err)
			}
			tools = append(tools, ToolCall{ID: stringField(item, "call_id"), Name: name, Arguments: arguments})
		}
	}
	return text.String(), tools, nil
}

func chatToolCalls(raw interface{}) ([]ToolCall, error) {
	items, _ := raw.([]interface{})
	tools := make([]ToolCall, 0, len(items))
	for _, rawItem := range items {
		item, _ := rawItem.(map[string]interface{})
		function := mapField(item, "function")
		name := strings.TrimSpace(stringField(function, "name"))
		if name == "" {
			return nil, fmt.Errorf("Chat tool_call 缺少名称")
		}
		arguments, err := normalizeToolArguments(function["arguments"])
		if err != nil {
			return nil, fmt.Errorf("Chat tool_call 参数无效: %w", err)
		}
		tools = append(tools, ToolCall{ID: stringField(item, "id"), Name: name, Arguments: arguments})
	}
	return tools, nil
}

func geminiCandidates(raw interface{}) (string, []ToolCall, string, error) {
	items, _ := raw.([]interface{})
	var text strings.Builder
	tools := make([]ToolCall, 0)
	finishReason := ""
	for _, rawItem := range items {
		item, _ := rawItem.(map[string]interface{})
		finishReason = firstNonEmpty(stringField(item, "finishReason"), finishReason)
		content := mapField(item, "content")
		parts, _ := content["parts"].([]interface{})
		for _, rawPart := range parts {
			part, _ := rawPart.(map[string]interface{})
			text.WriteString(stringField(part, "text"))
			if functionCall := mapField(part, "functionCall"); functionCall != nil {
				name := strings.TrimSpace(stringField(functionCall, "name"))
				if name == "" {
					return "", nil, "", fmt.Errorf("Gemini functionCall 缺少名称")
				}
				arguments, err := normalizeToolArguments(functionCall["args"])
				if err != nil {
					return "", nil, "", fmt.Errorf("Gemini functionCall 参数无效: %w", err)
				}
				tools = append(tools, ToolCall{Name: name, Arguments: arguments})
			}
		}
	}
	return text.String(), tools, finishReason, nil
}

func usageFromMap(raw map[string]interface{}) Usage {
	if raw == nil {
		return Usage{}
	}
	usage := Usage{
		InputTokens:  firstInt(raw, "input_tokens", "prompt_tokens", "inputTokens", "promptTokenCount"),
		OutputTokens: firstInt(raw, "output_tokens", "completion_tokens", "outputTokens", "candidatesTokenCount"),
		CachedTokens: firstInt(raw, "cached_tokens", "cachedContentTokenCount"),
		TotalTokens:  firstInt(raw, "total_tokens", "totalTokens", "totalTokenCount"),
	}
	outputDetails := mapField(raw, "output_tokens_details")
	completionDetails := mapField(raw, "completion_tokens_details")
	usage.ReasoningTokens = firstInt(outputDetails, "reasoning_tokens")
	if usage.ReasoningTokens == 0 {
		usage.ReasoningTokens = firstInt(completionDetails, "reasoning_tokens")
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	return usage
}

func mergeUsage(current Usage, update Usage) Usage {
	if update.InputTokens != 0 {
		current.InputTokens = update.InputTokens
	}
	if update.OutputTokens != 0 {
		current.OutputTokens = update.OutputTokens
	}
	if update.ReasoningTokens != 0 {
		current.ReasoningTokens = update.ReasoningTokens
	}
	if update.CachedTokens != 0 {
		current.CachedTokens = update.CachedTokens
	}
	if update.TotalTokens != 0 {
		current.TotalTokens = update.TotalTokens
	}
	return current
}

func responseTerminalFailure(payload map[string]interface{}, code ErrorCode, category ErrorCategory, fallback string) *ExecutionFailure {
	message := upstreamEventError(payload)
	if message == "" {
		message = fallback
	}
	return &ExecutionFailure{Code: code, Category: category, Message: message}
}

func upstreamEventError(payload map[string]interface{}) string {
	if errorObject := mapField(payload, "error"); errorObject != nil {
		if message := stringField(errorObject, "message"); message != "" {
			return message
		}
		if code := stringField(errorObject, "code"); code != "" {
			return code
		}
	}
	if details := mapField(payload, "incomplete_details"); details != nil {
		if reason := stringField(details, "reason"); reason != "" {
			return reason
		}
	}
	return ""
}

func upstreamErrorMessage(body []byte) string {
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	if message := upstreamEventError(payload); message != "" {
		return message
	}
	return stringField(payload, "message")
}

func mapField(payload map[string]interface{}, key string) map[string]interface{} {
	if payload == nil {
		return nil
	}
	value, _ := payload[key].(map[string]interface{})
	return value
}

func stringField(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	value, _ := payload[key].(string)
	return value
}

func intField(payload map[string]interface{}, key string) (int, bool) {
	if payload == nil {
		return 0, false
	}
	switch value := payload[key].(type) {
	case float64:
		return int(value), true
	case json.Number:
		integer, err := value.Int64()
		return int(integer), err == nil
	case int:
		return value, true
	default:
		return 0, false
	}
}

func firstInt(payload map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		if value, ok := intField(payload, key); ok {
			return value
		}
	}
	return 0
}

func textValue(raw interface{}) string {
	switch value := raw.(type) {
	case string:
		return value
	case []interface{}:
		var text strings.Builder
		for _, rawPart := range value {
			part, _ := rawPart.(map[string]interface{})
			text.WriteString(stringField(part, "text"))
		}
		return text.String()
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
