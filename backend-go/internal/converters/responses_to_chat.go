package converters

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ValidateResponsesToOpenAIChatRequest 对 raw JSON 快路径做协议兼容校验。
func ValidateResponsesToOpenAIChatRequest(inputRawJSON []byte) error {
	root := gjson.ParseBytes(inputRawJSON)

	if tools := root.Get("tools"); tools.Exists() {
		if !tools.IsArray() {
			return fmt.Errorf("Responses -> OpenAI Chat 的 tools 必须是数组")
		}
		var err error
		tools.ForEach(func(key, tool gjson.Result) bool {
			toolType := tool.Get("type").String()
			if toolType != "" && toolType != "function" {
				err = fmt.Errorf("OpenAI Chat 不支持第 %d 个 Responses tool type %q", key.Int(), toolType)
				return false
			}
			name := tool.Get("name").String()
			if nested := tool.Get("function.name"); nested.Exists() && nested.String() != "" {
				name = nested.String()
			}
			if name == "" {
				err = fmt.Errorf("OpenAI Chat tool 第 %d 项缺少 function.name", key.Int())
				return false
			}
			return true
		})
		if err != nil {
			return err
		}
	}

	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		if err := validateOpenAIChatToolChoice(toolChoice); err != nil {
			return err
		}
	}

	if reasoning := root.Get("reasoning"); reasoning.Exists() {
		if !gjsonResultIsObject(reasoning) {
			return fmt.Errorf("Responses -> OpenAI Chat 的 reasoning 必须是对象")
		}
		if effort := reasoning.Get("effort"); effort.Exists() {
			switch effort.String() {
			case "", "none", "auto", "minimal", "low", "medium", "high", "xhigh":
			default:
				return fmt.Errorf("OpenAI Chat 不支持 reasoning.effort=%q", effort.String())
			}
		}
	}

	if input := root.Get("input"); input.Exists() && input.IsArray() {
		var err error
		input.ForEach(func(key, item gjson.Result) bool {
			if item.Get("type").String() != "function_call" {
				return true
			}
			if item.Get("call_id").String() == "" {
				err = fmt.Errorf("OpenAI Chat input 第 %d 项 function_call 缺少 call_id", key.Int())
				return false
			}
			if item.Get("name").String() == "" {
				err = fmt.Errorf("OpenAI Chat input 第 %d 项 function_call 缺少 name", key.Int())
				return false
			}
			if args := item.Get("arguments"); args.Exists() {
				if !json.Valid([]byte(args.String())) {
					err = fmt.Errorf("OpenAI Chat input 第 %d 项 function_call arguments 不是合法 JSON", key.Int())
					return false
				}
			}
			return true
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// ConvertResponsesToOpenAIChatRequest 将 OpenAI Responses 请求格式转换为 OpenAI Chat Completions 格式
// 转换内容包括:
// 1. model 和 stream 配置
// 2. instructions → system message
// 3. input 数组 → messages 数组
// 4. tools 定义转换
// 5. function_call 和 function_call_output 处理
// 6. 生成参数映射 (max_tokens, reasoning 等)
//
// 参数:
//   - modelName: 要使用的模型名称
//   - inputRawJSON: Responses 格式的原始 JSON 请求
//   - stream: 是否为流式请求
//
// 返回:
//   - []byte: Chat Completions 格式的请求 JSON
func ConvertResponsesToOpenAIChatRequest(modelName string, inputRawJSON []byte, stream bool) []byte {
	// 基础 Chat Completions 模板
	out := `{"model":"","messages":[],"stream":false}`

	root := gjson.ParseBytes(inputRawJSON)

	// 设置 model
	out, _ = sjson.Set(out, "model", modelName)

	// 设置 stream
	out, _ = sjson.Set(out, "stream", stream)

	// 如果是流式请求，添加 stream_options 以获取 usage 信息
	if stream {
		out, _ = sjson.Set(out, "stream_options.include_usage", true)
	}

	// 映射生成参数
	if maxTokens := root.Get("max_output_tokens"); maxTokens.Exists() {
		out, _ = sjson.Set(out, "max_tokens", maxTokens.Int())
	} else if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
		out, _ = sjson.Set(out, "max_tokens", maxTokens.Int())
	}

	if temperature := root.Get("temperature"); temperature.Exists() {
		out, _ = sjson.Set(out, "temperature", temperature.Float())
	}

	if topP := root.Get("top_p"); topP.Exists() {
		out, _ = sjson.Set(out, "top_p", topP.Float())
	}

	if user := root.Get("user"); user.Exists() {
		out, _ = sjson.Set(out, "user", user.String())
	}

	// 转换 instructions → system message
	if instructions := root.Get("instructions"); instructions.Exists() && instructions.String() != "" {
		systemMessage := `{"role":"system","content":""}`
		systemMessage, _ = sjson.Set(systemMessage, "content", instructions.String())
		out, _ = sjson.SetRaw(out, "messages.-1", systemMessage)
	}

	// 转换 input 数组 → messages
	if input := root.Get("input"); input.Exists() {
		if input.IsArray() {
			out = convertInputArrayToMessages(input, out)
		} else if input.Type == gjson.String {
			// 简单字符串输入
			msg := `{"role":"user","content":""}`
			msg, _ = sjson.Set(msg, "content", input.String())
			out, _ = sjson.SetRaw(out, "messages.-1", msg)
		}
	}

	// 转换 tools
	toolsConverted := false
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		out, toolsConverted = convertToolsToOpenAIFormat(tools, out)
	}

	// 转换 reasoning.effort → reasoning_effort
	if reasoningEffort := root.Get("reasoning.effort"); reasoningEffort.Exists() {
		effort := reasoningEffort.String()
		switch effort {
		case "none":
			out, _ = sjson.Set(out, "reasoning_effort", "none")
		case "auto":
			// Chat Completions 没有 auto 值；省略字段即使用上游默认值。
		case "minimal":
			out, _ = sjson.Set(out, "reasoning_effort", "minimal")
		case "low":
			out, _ = sjson.Set(out, "reasoning_effort", "low")
		case "medium":
			out, _ = sjson.Set(out, "reasoning_effort", "medium")
		case "high":
			out, _ = sjson.Set(out, "reasoning_effort", "high")
		case "xhigh":
			out, _ = sjson.Set(out, "reasoning_effort", "xhigh")
		}
	}

	// 转换 tool_choice
	if toolsConverted {
		if parallelToolCalls := root.Get("parallel_tool_calls"); parallelToolCalls.Exists() {
			out, _ = sjson.Set(out, "parallel_tool_calls", parallelToolCalls.Bool())
		}
		if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
			if normalized, ok := normalizeResponsesToolChoiceForChat(toolChoice); ok {
				out, _ = sjson.Set(out, "tool_choice", normalized)
			}
		}
	}

	return []byte(out)
}

func validateOpenAIChatToolChoice(toolChoice gjson.Result) error {
	if toolChoice.Type == gjson.String {
		switch toolChoice.String() {
		case "auto", "none", "required":
			return nil
		default:
			return fmt.Errorf("OpenAI Chat 不支持 tool_choice=%q", toolChoice.String())
		}
	}
	if !gjsonResultIsObject(toolChoice) {
		return fmt.Errorf("Responses -> OpenAI Chat 的 tool_choice 必须是字符串或对象")
	}
	choiceType := toolChoice.Get("type").String()
	if choiceType == "" {
		choiceType = "function"
	}
	if choiceType != "function" {
		return fmt.Errorf("OpenAI Chat 不支持 tool_choice.type=%q", choiceType)
	}
	name := toolChoice.Get("name").String()
	if nested := toolChoice.Get("function.name"); nested.Exists() && nested.String() != "" {
		name = nested.String()
	}
	if name == "" {
		return fmt.Errorf("OpenAI Chat tool_choice 对象缺少 function.name 或 name")
	}
	return nil
}

func gjsonResultIsObject(result gjson.Result) bool {
	return result.Type == gjson.JSON && strings.HasPrefix(strings.TrimSpace(result.Raw), "{")
}

func normalizeResponsesToolChoiceForChat(toolChoice gjson.Result) (interface{}, bool) {
	if toolChoice.Type == gjson.String {
		return toolChoice.String(), true
	}
	if !gjsonResultIsObject(toolChoice) {
		return nil, false
	}
	name := toolChoice.Get("function.name").String()
	if name == "" {
		name = toolChoice.Get("name").String()
	}
	if name == "" {
		return nil, false
	}
	return map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name": name,
		},
	}, true
}

// convertInputArrayToMessages 将 input 数组转换为 messages 数组
func convertInputArrayToMessages(input gjson.Result, out string) string {
	pendingToolCalls := make([]interface{}, 0)
	flushPendingToolCalls := func() {
		if len(pendingToolCalls) == 0 {
			return
		}
		assistantMessage := map[string]interface{}{
			"role":       "assistant",
			"content":    nil,
			"tool_calls": pendingToolCalls,
		}
		out, _ = sjson.Set(out, "messages.-1", assistantMessage)
		pendingToolCalls = nil
	}

	input.ForEach(func(_, item gjson.Result) bool {
		itemType := item.Get("type").String()

		// 如果没有 type 但有 role，则视为 message
		if itemType == "" && item.Get("role").String() != "" {
			itemType = "message"
		}

		switch itemType {
		case "message":
			flushPendingToolCalls()
			out = convertMessageItem(item, out)

		case "function_call":
			pendingToolCalls = append(pendingToolCalls, convertFunctionCallItem(item))

		case "function_call_output":
			flushPendingToolCalls()
			out = convertFunctionCallOutputItem(item, out)
		default:
			flushPendingToolCalls()
		}

		return true
	})

	flushPendingToolCalls()
	return out
}

// convertMessageItem 转换 message 类型的 item
func convertMessageItem(item gjson.Result, out string) string {
	role := item.Get("role").String()
	if role == "" {
		role = "user"
	}

	message := `{"role":"","content":""}`
	message, _ = sjson.Set(message, "role", role)

	content := item.Get("content")
	if content.Exists() {
		if content.IsArray() {
			messageContent := convertResponsesContentArrayToOpenAIContent(content)
			if messageContent != nil {
				message, _ = sjson.Set(message, "content", messageContent)
			}
		} else if content.Type == gjson.String {
			// content 是字符串
			message, _ = sjson.Set(message, "content", content.String())
		}
	}

	out, _ = sjson.SetRaw(out, "messages.-1", message)
	return out
}

func convertResponsesContentArrayToOpenAIContent(content gjson.Result) interface{} {
	blocks := make([]map[string]interface{}, 0)
	texts := make([]string, 0)
	hasImage := false

	content.ForEach(func(_, contentItem gjson.Result) bool {
		var block map[string]interface{}
		if err := json.Unmarshal([]byte(contentItem.Raw), &block); err != nil {
			return true
		}
		if text, ok := utils.ExtractTextFromBlock(block); ok {
			texts = append(texts, text)
			blocks = append(blocks, map[string]interface{}{"type": "text", "text": text})
			return true
		}
		if imageBlock, ok := utils.ToOpenAIImageContentBlock(block); ok {
			hasImage = true
			blocks = append(blocks, imageBlock)
		}
		return true
	})

	if len(blocks) == 0 {
		return nil
	}
	if hasImage {
		return blocks
	}
	return strings.Join(texts, "")
}

// convertFunctionCallItem 转换 function_call 类型的 item
func convertFunctionCallItem(item gjson.Result) interface{} {
	toolCall := `{"id":"","type":"function","function":{"name":"","arguments":""}}`

	if callID := item.Get("call_id"); callID.Exists() {
		toolCall, _ = sjson.Set(toolCall, "id", callID.String())
	}

	if name := item.Get("name"); name.Exists() {
		toolCall, _ = sjson.Set(toolCall, "function.name", name.String())
	}

	if arguments := item.Get("arguments"); arguments.Exists() {
		toolCall, _ = sjson.Set(toolCall, "function.arguments", arguments.String())
	}

	return gjson.Parse(toolCall).Value()
}

// convertFunctionCallOutputItem 转换 function_call_output 类型的 item
func convertFunctionCallOutputItem(item gjson.Result, out string) string {
	// function_call_output → tool message
	toolMessage := `{"role":"tool","tool_call_id":"","content":""}`

	if callID := item.Get("call_id"); callID.Exists() {
		toolMessage, _ = sjson.Set(toolMessage, "tool_call_id", callID.String())
	}

	if output := item.Get("output"); output.Exists() {
		toolMessage, _ = sjson.Set(toolMessage, "content", output.String())
	} else if content := item.Get("content"); content.Exists() {
		if content.Type == gjson.String {
			toolMessage, _ = sjson.Set(toolMessage, "content", content.String())
		} else {
			toolMessage, _ = sjson.SetRaw(toolMessage, "content", content.Raw)
		}
	}

	out, _ = sjson.SetRaw(out, "messages.-1", toolMessage)
	return out
}

// convertToolsToOpenAIFormat 将 Responses tools 转换为 OpenAI Chat Completions tools 格式
func convertToolsToOpenAIFormat(tools gjson.Result, out string) (string, bool) {
	var chatCompletionsTools []interface{}

	tools.ForEach(func(_, tool gjson.Result) bool {
		chatTool := `{"type":"function","function":{}}`

		function := `{"name":"","description":"","parameters":{}}`

		name := tool.Get("name")
		description := tool.Get("description")
		parameters := tool.Get("parameters")
		if nested := tool.Get("function"); nested.Exists() {
			if nestedName := nested.Get("name"); nestedName.Exists() {
				name = nestedName
			}
			if nestedDescription := nested.Get("description"); nestedDescription.Exists() {
				description = nestedDescription
			}
			if nestedParameters := nested.Get("parameters"); nestedParameters.Exists() {
				parameters = nestedParameters
			}
		}

		if !name.Exists() || name.String() == "" {
			return true
		}

		if name.Exists() {
			function, _ = sjson.Set(function, "name", name.String())
		}

		if description.Exists() {
			function, _ = sjson.Set(function, "description", description.String())
		}

		if parameters.Exists() {
			function, _ = sjson.SetRaw(function, "parameters", parameters.Raw)
		}

		chatTool, _ = sjson.SetRaw(chatTool, "function", function)
		chatCompletionsTools = append(chatCompletionsTools, gjson.Parse(chatTool).Value())

		return true
	})

	if len(chatCompletionsTools) > 0 {
		out, _ = sjson.Set(out, "tools", chatCompletionsTools)
		return out, true
	}

	return out, false
}
