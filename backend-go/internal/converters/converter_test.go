package converters

import (
	"encoding/json"
	"testing"

	"github.com/BenedictKing/claude-proxy/internal/session"
	"github.com/BenedictKing/claude-proxy/internal/types"
)

// ============== extractTextFromContent 测试 ==============

func TestExtractTextFromContent_String(t *testing.T) {
	content := "Hello, world!"
	result := extractTextFromContent(content)

	if result != "Hello, world!" {
		t.Errorf("期望 'Hello, world!'，实际得到 '%s'", result)
	}
}

func TestExtractTextFromContent_ContentBlockArray(t *testing.T) {
	content := []interface{}{
		map[string]interface{}{
			"type": "input_text",
			"text": "First message",
		},
		map[string]interface{}{
			"type": "input_text",
			"text": "Second message",
		},
	}

	result := extractTextFromContent(content)
	expected := "First message\nSecond message"

	if result != expected {
		t.Errorf("期望 '%s'，实际得到 '%s'", expected, result)
	}
}

func TestExtractTextFromContent_MixedTypes(t *testing.T) {
	content := []interface{}{
		map[string]interface{}{
			"type": "input_text",
			"text": "User message",
		},
		map[string]interface{}{
			"type": "output_text",
			"text": "Assistant message",
		},
		map[string]interface{}{
			"type": "unknown",
			"text": "Should be ignored",
		},
	}

	result := extractTextFromContent(content)
	expected := "User message\nAssistant message"

	if result != expected {
		t.Errorf("期望 '%s'，实际得到 '%s'", expected, result)
	}
}

func TestExtractTextFromContent_EmptyArray(t *testing.T) {
	content := []interface{}{}
	result := extractTextFromContent(content)

	if result != "" {
		t.Errorf("期望空字符串，实际得到 '%s'", result)
	}
}

func TestExtractUsageMetrics_OpenAICachedTokensAdjustsInput(t *testing.T) {
	usage := ExtractUsageMetrics(map[string]interface{}{
		"input_tokens":  1000,
		"output_tokens": 20,
		"input_tokens_details": map[string]interface{}{
			"cached_tokens": 900,
		},
	})

	if usage.InputTokens != 100 {
		t.Fatalf("expected adjusted input_tokens=100, got %d", usage.InputTokens)
	}
	if usage.CacheReadInputTokens != 900 {
		t.Fatalf("expected cache_read_input_tokens=900, got %d", usage.CacheReadInputTokens)
	}
	if usage.TotalTokens != 120 {
		t.Fatalf("expected total_tokens=120, got %d", usage.TotalTokens)
	}
}

func TestExtractUsageMetrics_ClaudeCacheReadDoesNotDoubleSubtract(t *testing.T) {
	usage := ExtractUsageMetrics(map[string]interface{}{
		"input_tokens":            100,
		"output_tokens":           20,
		"cache_read_input_tokens": 900,
		"input_tokens_details": map[string]interface{}{
			"cached_tokens": 900,
		},
	})

	if usage.InputTokens != 100 {
		t.Fatalf("expected input_tokens to stay 100, got %d", usage.InputTokens)
	}
	if usage.CacheReadInputTokens != 900 {
		t.Fatalf("expected cache_read_input_tokens=900, got %d", usage.CacheReadInputTokens)
	}
}

// ============== OpenAI 转换器测试 ==============

func TestOpenAIChatConverter_WithInstructions(t *testing.T) {
	converter := &OpenAIChatConverter{}
	sess := &session.Session{
		ID:       "sess_test",
		Messages: []types.ResponsesItem{},
	}

	parallelToolCalls := true
	req := &types.ResponsesRequest{
		Model:             "gpt-4",
		Instructions:      "You are a helpful assistant.",
		Input:             "Hello!",
		MaxTokens:         100,
		MaxOutputTokens:   128,
		Temperature:       0.7,
		ParallelToolCalls: &parallelToolCalls,
		Reasoning:         map[string]interface{}{"effort": "minimal"},
		ToolChoice:        map[string]interface{}{"type": "function", "name": "lookup"},
		Tools:             []map[string]interface{}{{"name": "lookup", "description": "Lookup data", "parameters": map[string]interface{}{"type": "object"}}},
	}

	result, err := converter.ToProviderRequest(sess, req)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("结果不是 map[string]interface{}")
	}

	// 检查 model
	if resultMap["model"] != "gpt-4" {
		t.Errorf("期望 model 为 'gpt-4'，实际为 '%v'", resultMap["model"])
	}

	// 检查 messages
	messages, ok := resultMap["messages"].([]map[string]interface{})
	if !ok {
		t.Fatal("messages 不是正确的类型")
	}

	if len(messages) != 2 {
		t.Fatalf("期望 2 条消息（system + user），实际为 %d", len(messages))
	}

	// 检查第一条是 system
	if messages[0]["role"] != "system" {
		t.Errorf("第一条消息应该是 system，实际为 '%v'", messages[0]["role"])
	}
	if messages[0]["content"] != "You are a helpful assistant." {
		t.Errorf("system 内容不匹配")
	}

	// 检查第二条是 user
	if messages[1]["role"] != "user" {
		t.Errorf("第二条消息应该是 user，实际为 '%v'", messages[1]["role"])
	}
	if messages[1]["content"] != "Hello!" {
		t.Errorf("user 内容不匹配")
	}

	// 检查其他参数
	if resultMap["max_tokens"] != 128 {
		t.Errorf("max_tokens 不匹配")
	}
	if resultMap["temperature"] != 0.7 {
		t.Errorf("temperature 不匹配")
	}
	if resultMap["parallel_tool_calls"] != true {
		t.Errorf("parallel_tool_calls 不匹配")
	}
	if resultMap["reasoning_effort"] != "minimal" {
		t.Errorf("reasoning_effort 不匹配")
	}
	tools, ok := resultMap["tools"].([]map[string]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("tools 不匹配")
	}
	function, ok := tools[0]["function"].(map[string]interface{})
	if !ok || function["name"] != "lookup" {
		t.Errorf("tool function 不匹配")
	}
	toolChoice, ok := resultMap["tool_choice"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool_choice 类型不匹配")
	}
	choiceFunction, ok := toolChoice["function"].(map[string]interface{})
	if !ok || choiceFunction["name"] != "lookup" {
		t.Errorf("tool_choice function 不匹配")
	}
}

func TestOpenAIChatConverter_WithMessageType(t *testing.T) {
	converter := &OpenAIChatConverter{}
	sess := &session.Session{
		ID:       "sess_test",
		Messages: []types.ResponsesItem{},
	}

	req := &types.ResponsesRequest{
		Model: "gpt-4",
		Input: []interface{}{
			map[string]interface{}{
				"type": "message",
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type": "input_text",
						"text": "Hello from message type!",
					},
				},
			},
		},
	}

	result, err := converter.ToProviderRequest(sess, req)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	resultMap := result.(map[string]interface{})
	messages := resultMap["messages"].([]map[string]interface{})

	if len(messages) != 1 {
		t.Fatalf("期望 1 条消息，实际为 %d", len(messages))
	}

	if messages[0]["role"] != "user" {
		t.Errorf("角色应该是 user")
	}
	if messages[0]["content"] != "Hello from message type!" {
		t.Errorf("内容不匹配，实际为 '%v'", messages[0]["content"])
	}
}

func TestOpenAIChatResponseToResponses_WithReasoningAndToolCalls(t *testing.T) {
	resp := map[string]interface{}{
		"model": "gpt-4o",
		"choices": []interface{}{
			map[string]interface{}{
				"finish_reason": "tool_calls",
				"message": map[string]interface{}{
					"role":              "assistant",
					"reasoning_content": "I should call a tool.",
					"content":           "Checking now.",
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id":   "call_lookup",
							"type": "function",
							"function": map[string]interface{}{
								"name":      "lookup",
								"arguments": "{\"q\":\"test\"}",
							},
						},
					},
				},
			},
		},
	}

	result, err := OpenAIChatResponseToResponses(resp, "sess_test")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	if result.Object != "response" {
		t.Errorf("object 不匹配")
	}
	if result.Status != "completed" {
		t.Errorf("status 不匹配")
	}
	if len(result.Output) != 3 {
		t.Fatalf("期望 3 个 output item，实际为 %d", len(result.Output))
	}
	if result.Output[0].Type != "reasoning" {
		t.Errorf("第一个 item 应为 reasoning")
	}
	if result.Output[1].Type != "message" || result.Output[1].Role != "assistant" {
		t.Errorf("第二个 item 应为 assistant message")
	}
	if result.Output[2].Type != "function_call" || result.Output[2].CallID != "call_lookup" || result.Output[2].Name != "lookup" {
		t.Errorf("第三个 item 应为 function_call")
	}
}

func TestOpenAIChatResponseToResponses_WithCustomToolContext(t *testing.T) {
	resp := map[string]interface{}{
		"model": "gpt-4o",
		"choices": []interface{}{
			map[string]interface{}{
				"finish_reason": "tool_calls",
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "",
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id":   "call_exec",
							"type": "function",
							"function": map[string]interface{}{
								"name":      "code_exec",
								"arguments": "{\"input\":\"print('hi')\"}",
							},
						},
					},
				},
			},
		},
	}

	result, err := openAIChatResponseToResponsesWithCustomTools(resp, "sess_test", map[string]customToolSpec{
		"code_exec": {OriginalName: "code_exec"},
	})
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	if len(result.Output) != 1 {
		t.Fatalf("期望 1 个 output item，实际为 %d", len(result.Output))
	}
	if result.Output[0].Type != "custom_tool_call" {
		t.Fatalf("期望恢复为 custom_tool_call，实际为 %s", result.Output[0].Type)
	}
	if result.Output[0].Content != "print('hi')" {
		t.Fatalf("custom_tool_call input 不匹配，实际为 %#v", result.Output[0].Content)
	}
}

func TestResponsesToOpenAIChatMessages_CollapseSystemAndCustomToolReplay(t *testing.T) {
	sess := &session.Session{
		ID: "sess_test",
		Messages: []types.ResponsesItem{
			{Type: "message", Role: "system", Content: "历史系统提示"},
			{Type: "custom_tool_call", CallID: "call_exec", Name: "code_exec", Content: "print('history')"},
			{Type: "custom_tool_call_output", CallID: "call_exec", Content: "done"},
		},
	}

	messages, err := ResponsesToOpenAIChatMessages(sess, []interface{}{
		map[string]interface{}{"type": "message", "role": "system", "content": "本次系统提示"},
	}, "顶层指令")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	if len(messages) != 3 {
		t.Fatalf("期望 3 条消息，实际为 %d", len(messages))
	}
	if messages[0]["role"] != "system" {
		t.Fatalf("第一条应为 system，实际为 %v", messages[0]["role"])
	}
	systemContent, _ := messages[0]["content"].(string)
	if systemContent != "顶层指令\n\n历史系统提示\n\n本次系统提示" {
		t.Fatalf("system 折叠结果不匹配，实际为 %q", systemContent)
	}

	toolCalls, ok := messages[1]["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("第二条消息应包含 1 个 tool_call")
	}
	args, _ := toolCalls[0]["function"].(map[string]interface{})["arguments"].(string)
	if args != "{\"input\":\"print('history')\"}" {
		t.Fatalf("custom_tool_call 回放参数不匹配，实际为 %s", args)
	}

	if messages[2]["role"] != "tool" {
		t.Fatalf("第三条消息应为 tool，实际为 %v", messages[2]["role"])
	}
}

func TestOpenAICompletionsConverter_MaxOutputTokensAndUnsupportedTools(t *testing.T) {
	converter := &OpenAICompletionsConverter{}
	req := &types.ResponsesRequest{
		Model:           "gpt-3.5-turbo-instruct",
		Input:           "Hello!",
		MaxTokens:       100,
		MaxOutputTokens: 256,
	}

	result, err := converter.ToProviderRequest(nil, req)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("结果不是 map[string]interface{}")
	}
	if resultMap["max_tokens"] != 256 {
		t.Errorf("max_tokens 应优先使用 max_output_tokens")
	}

	req.Tools = []map[string]interface{}{{"name": "lookup"}}
	if _, err := converter.ToProviderRequest(nil, req); err == nil {
		t.Fatal("OpenAI Completions 不支持 tools，应返回错误")
	}
}

func TestClaudeConverter_InvalidFunctionCallArguments(t *testing.T) {
	converter := &ClaudeConverter{}
	req := &types.ResponsesRequest{
		Model:           "claude-3-5-sonnet",
		MaxOutputTokens: 2048,
		Input: []interface{}{
			map[string]interface{}{
				"type":      "function_call",
				"call_id":   "call_bad",
				"name":      "lookup",
				"arguments": "{bad-json",
			},
		},
	}

	if _, err := converter.ToProviderRequest(nil, req); err == nil {
		t.Fatal("非法 function_call arguments 应返回错误")
	}
}

func TestResponsesToGemini_InvalidFunctionCallArguments(t *testing.T) {
	req := &types.ResponsesRequest{
		Model: "gemini-2.5-flash",
		Input: []interface{}{
			map[string]interface{}{
				"type":      "function_call",
				"call_id":   "call_bad",
				"name":      "lookup",
				"arguments": "{bad-json",
			},
		},
	}

	if _, err := ConvertResponsesToGeminiRequest("gemini-2.5-flash", nil, req); err == nil {
		t.Fatal("非法 function_call arguments 应返回错误")
	}
}

func TestResponsesToGemini_CustomToolCompatible(t *testing.T) {
	req := &types.ResponsesRequest{
		Model: "gemini-2.5-flash",
		Input: "Run code",
		Tools: []interface{}{
			map[string]interface{}{
				"type":        "custom",
				"name":        "code_exec",
				"description": "Execute code",
			},
		},
		ToolChoice: map[string]interface{}{
			"type": "custom",
			"name": "code_exec",
		},
	}

	body, err := ConvertResponsesToGeminiRequest("gemini-2.5-flash", nil, req)
	if err != nil {
		t.Fatalf("custom tool 对 Gemini 应兼容降级，实际报错: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("解析 Gemini 请求失败: %v", err)
	}

	tools, ok := result["tools"].([]interface{})
	if !ok || len(tools) == 0 {
		t.Fatal("Gemini 请求应包含 tools")
	}
	decls, ok := tools[0].(map[string]interface{})["functionDeclarations"].([]interface{})
	if !ok || len(decls) == 0 {
		t.Fatal("Gemini 请求应包含 functionDeclarations")
	}
	if decls[0].(map[string]interface{})["name"] != "code_exec" {
		t.Fatalf("tool 名称不匹配，实际为 %#v", decls[0].(map[string]interface{})["name"])
	}
	toolConfig, ok := result["toolConfig"].(map[string]interface{})
	if !ok {
		t.Fatal("Gemini 请求应包含 toolConfig")
	}
	cfg, ok := toolConfig["functionCallingConfig"].(map[string]interface{})
	if !ok || cfg["mode"] != "ANY" {
		t.Fatalf("tool choice 应降级为 ANY，实际为 %#v", toolConfig)
	}
}

func TestResponsesToGemini_PrunesToolConfigWithoutTools(t *testing.T) {
	req := &types.ResponsesRequest{
		Model:      "gemini-2.5-flash",
		Input:      "Hi",
		ToolChoice: "auto",
	}

	body, err := ConvertResponsesToGeminiRequest("gemini-2.5-flash", nil, req)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("解析 Gemini 请求失败: %v", err)
	}
	if _, ok := result["toolConfig"]; ok {
		t.Fatal("没有 tools 时不应保留 toolConfig")
	}
}

func TestClaudeResponseToResponses_StopReasonMaxTokens(t *testing.T) {
	resp := map[string]interface{}{
		"model":       "claude-3-5-sonnet",
		"stop_reason": "max_tokens",
		"content": []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": "partial",
			},
		},
	}

	result, err := ClaudeResponseToResponses(resp, "sess_test")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if result.Object != "response" {
		t.Errorf("object 不匹配")
	}
	if result.Status != "incomplete" {
		t.Errorf("max_tokens 应映射为 incomplete")
	}
}

func TestClaudeConverter_CustomToolCompatible(t *testing.T) {
	converter := &ClaudeConverter{}
	req := &types.ResponsesRequest{
		Model:           "claude-3-5-sonnet",
		MaxOutputTokens: 2048,
		Input:           "Run code",
		Tools: []interface{}{
			map[string]interface{}{
				"type":        "custom",
				"name":        "code_exec",
				"description": "Execute code",
			},
		},
		ToolChoice: map[string]interface{}{
			"type": "custom",
			"name": "code_exec",
		},
	}

	result, err := converter.ToProviderRequest(nil, req)
	if err != nil {
		t.Fatalf("custom tool 对 Claude 应兼容降级，实际报错: %v", err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("结果不是 map[string]interface{}")
	}
	tools, ok := resultMap["tools"].([]types.ClaudeTool)
	if !ok || len(tools) != 1 {
		t.Fatalf("Claude 请求 tools 不匹配: %#v", resultMap["tools"])
	}
	if tools[0].Name != "code_exec" {
		t.Fatalf("Claude tool 名称不匹配，实际为 %s", tools[0].Name)
	}
	toolChoice, ok := resultMap["tool_choice"].(map[string]interface{})
	if !ok || toolChoice["type"] != "tool" || toolChoice["name"] != "code_exec" {
		t.Fatalf("Claude tool_choice 不匹配: %#v", resultMap["tool_choice"])
	}
}

func TestResponsesToOpenAIChatMessages_CustomToolOutputReplay(t *testing.T) {
	messages, err := ResponsesToOpenAIChatMessages(nil, []interface{}{
		map[string]interface{}{
			"type":    "custom_tool_call_output",
			"call_id": "call_exec",
			"output":  "done",
		},
	}, "")
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("期望 1 条消息，实际为 %d", len(messages))
	}
	if messages[0]["role"] != "tool" {
		t.Fatalf("应转换为 tool 消息，实际为 %v", messages[0]["role"])
	}
	if messages[0]["content"] != "done" {
		t.Fatalf("tool 输出内容不匹配，实际为 %#v", messages[0]["content"])
	}
}

// ============== Claude 转换器测试 ==============

func TestClaudeConverter_WithInstructions(t *testing.T) {
	converter := &ClaudeConverter{}
	sess := &session.Session{
		ID:       "sess_test",
		Messages: []types.ResponsesItem{},
	}

	req := &types.ResponsesRequest{
		Model:        "claude-3-opus",
		Instructions: "You are Claude.",
		Input:        "Hello!",
		MaxTokens:    1000,
	}

	result, err := converter.ToProviderRequest(sess, req)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	resultMap := result.(map[string]interface{})

	// 检查 system 参数（Claude 使用独立的 system 字段，现在支持数组格式以实现 cache_control）
	systemVal, ok := resultMap["system"]
	if !ok {
		t.Fatal("缺少 system 参数")
	}

	// system 可能是字符串或数组格式（带 cache_control）
	var systemText string
	switch s := systemVal.(type) {
	case string:
		systemText = s
	case []interface{}:
		if len(s) > 0 {
			if block, ok := s[0].(map[string]interface{}); ok {
				systemText, _ = block["text"].(string)
			}
		}
	case []map[string]interface{}:
		if len(s) > 0 {
			systemText, _ = s[0]["text"].(string)
		}
	}
	if systemText != "You are Claude." {
		t.Errorf("system 参数不匹配: got %q, want %q", systemText, "You are Claude.")
	}

	// 检查 messages
	messages, ok := resultMap["messages"].([]types.ClaudeMessage)
	if !ok {
		t.Fatal("messages 不是正确的类型")
	}

	if len(messages) != 1 {
		t.Fatalf("期望 1 条消息，实际为 %d", len(messages))
	}

	if messages[0].Role != "user" {
		t.Errorf("角色应该是 user")
	}
}

// ============== 工厂模式测试 ==============

func TestConverterFactory(t *testing.T) {
	tests := []struct {
		serviceType  string
		expectedType string
	}{
		{"openai", "*converters.OpenAIChatConverter"},
		{"claude", "*converters.ClaudeConverter"},
		{"responses", "*converters.ResponsesPassthroughConverter"},
		{"unknown", "*converters.OpenAIChatConverter"}, // 默认
	}

	for _, tt := range tests {
		t.Run(tt.serviceType, func(t *testing.T) {
			converter := NewConverter(tt.serviceType)
			if converter == nil {
				t.Errorf("工厂返回 nil")
			}
			// 检查类型（简单验证）
			if converter.GetProviderName() == "" {
				t.Errorf("GetProviderName 返回空字符串")
			}
		})
	}
}

// ============== 会话历史测试 ==============

func TestOpenAIChatConverter_WithSessionHistory(t *testing.T) {
	converter := &OpenAIChatConverter{}
	sess := &session.Session{
		ID: "sess_test",
		Messages: []types.ResponsesItem{
			{
				Type:    "message",
				Role:    "user",
				Content: "Previous user message",
			},
			{
				Type:    "message",
				Role:    "assistant",
				Content: "Previous assistant message",
			},
		},
	}

	req := &types.ResponsesRequest{
		Model: "gpt-4",
		Input: "New user message",
	}

	result, err := converter.ToProviderRequest(sess, req)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}

	resultMap := result.(map[string]interface{})
	messages := resultMap["messages"].([]map[string]interface{})

	// 应该有 3 条消息：2 条历史 + 1 条新消息
	if len(messages) != 3 {
		t.Fatalf("期望 3 条消息，实际为 %d", len(messages))
	}

	// 检查顺序
	if messages[0]["content"] != "Previous user message" {
		t.Errorf("第一条消息内容不匹配")
	}
	if messages[1]["content"] != "Previous assistant message" {
		t.Errorf("第二条消息内容不匹配")
	}
	if messages[2]["content"] != "New user message" {
		t.Errorf("第三条消息内容不匹配")
	}
}

// ============== FinishReason 映射测试 ==============

func TestOpenAIFinishReasonToAnthropic(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"stop", "end_turn"},
		{"length", "max_tokens"},
		{"tool_calls", "tool_use"},
		{"function_call", "tool_use"},
		{"content_filter", "refusal"},
		{"empty", "end_turn"},
		{"unknown_reason", "unknown_reason"}, // 未知原因透传
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := OpenAIFinishReasonToAnthropic(tt.input)
			if result != tt.expected {
				t.Errorf("OpenAIFinishReasonToAnthropic(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestAnthropicStopReasonToOpenAI(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"end_turn", "stop"},
		{"max_tokens", "length"},
		{"stop_sequence", "stop"},
		{"pause_turn", "stop"},
		{"tool_use", "tool_calls"},
		{"refusal", "content_filter"},
		{"empty", "stop"},
		{"unknown_reason", "unknown_reason"}, // 未知原因透传
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := AnthropicStopReasonToOpenAI(tt.input)
			if result != tt.expected {
				t.Errorf("AnthropicStopReasonToOpenAI(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestOpenAIFinishReasonToResponses(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"stop", "completed"},
		{"tool_calls", "completed"},
		{"function_call", "completed"},
		{"length", "incomplete"},
		{"content_filter", "failed"},
		{"empty", "completed"},
		{"unknown_reason", "incomplete"}, // 未知原因视为未完成
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := OpenAIFinishReasonToResponses(tt.input)
			if result != tt.expected {
				t.Errorf("OpenAIFinishReasonToResponses(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
