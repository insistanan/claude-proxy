package utils

import (
	"encoding/json"
	"unicode"

	"github.com/BenedictKing/claude-proxy/internal/types"
)

// EstimateTokens 估算文本的 token 数量
// 使用字符估算法：
// - 中文/日文/韩文：约 1.5 字符/token
// - 英文及其他：约 3.5 字符/token
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}

	cjkCount := 0
	otherCount := 0

	for _, r := range text {
		if isCJK(r) {
			cjkCount++
		} else if !unicode.IsSpace(r) {
			otherCount++
		}
	}

	// CJK: ~1.5 字符/token, 其他: ~3.5 字符/token
	cjkTokens := float64(cjkCount) / 1.5
	otherTokens := float64(otherCount) / 3.5

	return int(cjkTokens + otherTokens + 0.5) // 四舍五入
}

// estimateMessagesTokens 估算消息数组的 token 数量
func estimateMessagesTokens(messages interface{}) int {
	if messages == nil {
		return 0
	}

	// 序列化为 JSON 后估算
	data, err := json.Marshal(messages)
	if err != nil {
		return 0
	}

	// 每条消息额外开销约 4 tokens
	msgCount := 0
	if arr, ok := messages.([]interface{}); ok {
		msgCount = len(arr)
	}

	return EstimateTokens(string(data)) + msgCount*4
}

// EstimateRequestTokens 从请求体估算输入 token
func EstimateRequestTokens(bodyBytes []byte) int {
	if len(bodyBytes) == 0 {
		return 0
	}

	var req map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return EstimateTokens(string(bodyBytes))
	}

	total := 0

	// system prompt
	if system, ok := req["system"]; ok {
		if str, ok := system.(string); ok {
			total += EstimateTokens(str)
		} else if arr, ok := system.([]interface{}); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					if text, ok := m["text"].(string); ok {
						total += EstimateTokens(text)
					}
				}
			}
		}
	}

	// messages
	if messages, ok := req["messages"]; ok {
		total += estimateMessagesTokens(messages)
	}

	// tools (每个工具约 100-200 tokens)
	if tools, ok := req["tools"].([]interface{}); ok {
		total += len(tools) * 150
	}

	return total
}

// EstimateResponseTokens 从响应内容估算输出 token
func EstimateResponseTokens(content interface{}) int {
	if content == nil {
		return 0
	}

	// 字符串内容
	if str, ok := content.(string); ok {
		return EstimateTokens(str)
	}

	// 内容数组
	if arr, ok := content.([]interface{}); ok {
		total := 0
		for _, item := range arr {
			if m, ok := item.(map[string]interface{}); ok {
				if text, ok := m["text"].(string); ok {
					total += EstimateTokens(text)
				}
				// tool_use 的 input 也计入
				if input, ok := m["input"]; ok {
					data, _ := json.Marshal(input)
					total += EstimateTokens(string(data))
				}
			}
		}
		return total
	}

	// 其他情况序列化后估算
	data, err := json.Marshal(content)
	if err != nil {
		return 0
	}
	return EstimateTokens(string(data))
}

// isCJK 判断是否为中日韩字符
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

// ============== Responses API Token 估算 ==============

// EstimateResponsesRequestTokens 从 Responses API 请求体估算输入 token
// 支持 instructions、input (string 或 []item) 格式
func EstimateResponsesRequestTokens(bodyBytes []byte) int {
	if len(bodyBytes) == 0 {
		return 0
	}

	var req map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return EstimateTokens(string(bodyBytes))
	}

	total := 0

	// instructions (系统指令)
	if instructions, ok := req["instructions"].(string); ok {
		total += EstimateTokens(instructions)
	}

	// input 字段处理
	if input := req["input"]; input != nil {
		total += estimateResponsesInputTokens(input)
	}

	// tools（优先按定义本身估算；简单工具保留每个工具约 150 token 的下限）
	if tools, ok := req["tools"].([]interface{}); ok {
		estimatedTools := estimateResponsesValueTokens(tools)
		minimumTools := len(tools) * 150
		if estimatedTools < minimumTools {
			estimatedTools = minimumTools
		}
		total += estimatedTools
	}

	return total
}

// estimateResponsesInputTokens 估算 Responses input 字段的 token
func estimateResponsesInputTokens(input interface{}) int {
	switch v := input.(type) {
	case string:
		// 简单字符串输入
		return EstimateTokens(v)
	case []interface{}:
		// 消息数组格式
		total := 0
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				// 每条消息额外开销约 4 tokens
				total += 4
				total += estimateResponsesInputItemTokens(m)
			}
		}
		return total
	default:
		// 其他情况序列化后估算
		data, err := json.Marshal(input)
		if err != nil {
			return 0
		}
		return EstimateTokens(string(data))
	}
}

// estimateResponsesInputItemTokens 按 Responses item 的实际字段估算 token。
// 同一语义在不同兼容实现中可能叫 content/input/output；不能把这些别名
// 无条件相加，否则一个同时带 input 与 content 的 custom_tool_call，或同时带
// output 与 content 的 function_call_output，会把同一份工具数据重复计入。
// 对未知类型保守地估算整个 item，避免静默漏掉新协议字段。
func estimateResponsesInputItemTokens(item map[string]interface{}) int {
	if item == nil {
		return 0
	}

	itemType, _ := item["type"].(string)
	switch itemType {
	case "", "message", "text":
		return estimateResponsesValueTokens(item["content"])
	case "function_call":
		return estimateResponsesCallFields(item["name"], item["arguments"])
	case "custom_tool_call":
		value, exists := item["input"]
		if !exists || value == nil {
			value = item["content"]
		}
		return estimateResponsesCallFields(item["name"], value)
	case "tool_call":
		if toolUse, ok := item["tool_use"].(map[string]interface{}); ok {
			return estimateResponsesCallFields(toolUse["name"], toolUse["input"])
		}
		return estimateResponsesCallFields(item["name"], item["arguments"])
	case "tool_search_call":
		return estimateResponsesCallFields(item["name"], item["arguments"])
	case "function_call_output", "custom_tool_call_output", "tool_result":
		return estimateResponsesOutputFieldTokens(item)
	case "tool_search_output":
		total := estimateResponsesOutputFieldTokens(item)
		total += estimateResponsesValueTokens(item["tools"])
		return total
	case "reasoning":
		// summary 与 encrypted/content 是不同字段：summary 是可读摘要，
		// encrypted_content 是上游要求回传的 opaque 上下文，不能因为不可读而丢弃。
		return estimateResponsesValueTokens(item["summary"]) +
			estimateResponsesValueTokens(item["content"]) +
			estimateResponsesValueTokens(item["encrypted_content"])
	default:
		data, err := json.Marshal(item)
		if err != nil {
			return 0
		}
		return EstimateTokens(string(data))
	}
}

func estimateResponsesCallFields(name, arguments interface{}) int {
	total := estimateResponsesValueTokens(name)
	return total + estimateResponsesValueTokens(arguments)
}

func estimateResponsesOutputFieldTokens(item map[string]interface{}) int {
	if output, exists := item["output"]; exists && output != nil {
		return estimateResponsesValueTokens(output)
	}
	return estimateResponsesValueTokens(item["content"])
}

// estimateContentTokens 估算 content 字段的 token
func estimateContentTokens(content interface{}) int {
	return estimateResponsesValueTokens(content)
}

// estimateResponsesValueTokens 递归估算 Responses item 中的文本或结构化值。
// Responses 的 content/output 既可能是字符串，也可能是多层数组/对象；只读取
// text 会漏掉 custom_tool_call_output.output 这类长工具结果，进而严重低估上下文。
func estimateResponsesValueTokens(content interface{}) int {
	switch v := content.(type) {
	case string:
		return EstimateTokens(v)
	case []interface{}:
		total := 0
		for _, item := range v {
			total += estimateResponsesValueTokens(item)
		}
		return total
	default:
		data, err := json.Marshal(content)
		if err != nil {
			return 0
		}
		return EstimateTokens(string(data))
	}
}

// EstimateResponsesOutputTokens 从 Responses API 响应估算输出 token
// 支持 []ResponsesItem 格式
func EstimateResponsesOutputTokens(output interface{}) int {
	if output == nil {
		return 0
	}

	// 处理 []types.ResponsesItem 类型
	if items, ok := output.([]types.ResponsesItem); ok {
		total := 0
		for _, item := range items {
			total += estimateResponsesItemTokens(item)
		}
		return total
	}

	// 处理 []interface{} 类型
	if arr, ok := output.([]interface{}); ok {
		total := 0
		for _, item := range arr {
			if m, ok := item.(map[string]interface{}); ok {
				// 处理 content 字段
				if content := m["content"]; content != nil {
					total += estimateContentTokens(content)
				}

				// 处理 tool_use
				if toolUse, ok := m["tool_use"].(map[string]interface{}); ok {
					data, _ := json.Marshal(toolUse)
					total += EstimateTokens(string(data))
				}

				// 处理 function_call 类型
				if m["type"] == "function_call" {
					if args, ok := m["arguments"].(string); ok {
						total += EstimateTokens(args)
					}
					if name, ok := m["name"].(string); ok {
						total += EstimateTokens(name) + 2 // 函数名 + 开销
					}
				}

				// 处理 reasoning 类型
				if m["type"] == "reasoning" {
					if summary, ok := m["summary"].([]interface{}); ok {
						for _, s := range summary {
							if sm, ok := s.(map[string]interface{}); ok {
								if text, ok := sm["text"].(string); ok {
									total += EstimateTokens(text)
								}
							}
						}
					}
				}
			}
		}
		return total
	}

	// 其他情况序列化后估算
	data, err := json.Marshal(output)
	if err != nil {
		return 0
	}
	return EstimateTokens(string(data))
}

// estimateResponsesItemTokens 估算单个 ResponsesItem 的 token 数
func estimateResponsesItemTokens(item types.ResponsesItem) int {
	total := 0

	// 处理 content 字段
	if item.Content != nil {
		total += estimateContentTokens(item.Content)
	}

	// 处理 tool_use
	if item.ToolUse != nil {
		data, _ := json.Marshal(item.ToolUse)
		total += EstimateTokens(string(data))
	}

	// 处理 Responses 原生 function_call 字段
	if item.Type == "function_call" {
		if item.Arguments != "" {
			total += EstimateTokens(item.Arguments)
		}
		if item.Name != "" {
			total += EstimateTokens(item.Name) + 2
		}
	}

	// 处理 reasoning summary 字段
	if item.Summary != nil {
		data, _ := json.Marshal(item.Summary)
		total += EstimateTokens(string(data))
	}
	if item.EncryptedContent != nil {
		data, _ := json.Marshal(item.EncryptedContent)
		total += EstimateTokens(string(data))
	}
	if item.Signature != "" {
		total += EstimateTokens(item.Signature)
	}

	// 如果是特殊类型且 content/tool_use 都为空，序列化整个结构估算
	// 这处理 function_call、reasoning 等类型，其数据可能在其他字段中
	if total == 0 && item.Type != "" && item.Type != "message" && item.Type != "text" {
		data, _ := json.Marshal(item)
		total = EstimateTokens(string(data))
	}

	return total
}
