package converters

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/types"
)

func ResponsesToolsToClaudeTools(raw interface{}) ([]types.ClaudeTool, error) {
	items, err := interfaceSlice(raw)
	if err != nil || len(items) == 0 {
		return nil, err
	}

	tools := make([]types.ClaudeTool, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("Responses tools 项必须是对象")
		}
		spec := normalizeResponsesToolDefinition(m)
		if spec.ToolType != "" && spec.ToolType != "function" && spec.ToolType != "custom" {
			return nil, fmt.Errorf("Claude 上游暂不支持 Responses tool type %q", spec.ToolType)
		}
		if spec.Name == "" {
			return nil, fmt.Errorf("Responses function tool 缺少 name")
		}

		tools = append(tools, types.ClaudeTool{
			Name:        spec.Name,
			Description: spec.Description,
			InputSchema: spec.Parameters,
		})
	}
	return tools, nil
}

func ResponsesToolChoiceToClaude(raw interface{}) (interface{}, error) {
	if raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]interface{}{"type": "auto"}, nil
		case "required":
			return map[string]interface{}{"type": "any"}, nil
		case "none":
			return map[string]interface{}{"type": "none"}, nil
		default:
			return nil, fmt.Errorf("不支持的 Responses tool_choice: %s", v)
		}
	case map[string]interface{}:
		if typ, _ := v["type"].(string); typ == "function" || typ == "custom" {
			name := extractNamedToolChoice(v)
			if name == "" {
				return nil, fmt.Errorf("function tool_choice 缺少 name")
			}
			return map[string]interface{}{"type": "tool", "name": name}, nil
		}
		return v, nil
	default:
		return nil, fmt.Errorf("不支持的 tool_choice 类型: %T", raw)
	}
}

func validateClaudeThinkingToolChoice(raw interface{}) error {
	if raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case string:
		switch v {
		case "", "auto", "none":
			return nil
		case "required":
			return fmt.Errorf("Claude extended thinking 仅支持 tool_choice auto/none，不支持 required")
		default:
			return fmt.Errorf("不支持的 Responses tool_choice: %s", v)
		}
	case map[string]interface{}:
		typ, _ := v["type"].(string)
		switch typ {
		case "", "auto", "none":
			return nil
		case "function", "tool", "any", "required":
			return fmt.Errorf("Claude extended thinking 仅支持 tool_choice auto/none，不支持指定或强制工具")
		default:
			return fmt.Errorf("Claude extended thinking 不支持 tool_choice type %q", typ)
		}
	default:
		return fmt.Errorf("不支持的 tool_choice 类型: %T", raw)
	}
}

func ResponsesReasoningToClaudeThinking(raw interface{}, maxOutputTokens int) (interface{}, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("reasoning 必须是对象")
	}
	effort, _ := m["effort"].(string)
	if effort == "" || effort == "none" {
		return nil, nil
	}
	if maxOutputTokens > 0 && maxOutputTokens <= 1024 {
		return nil, fmt.Errorf("Claude extended thinking 需要 max_output_tokens 大于 1024")
	}
	budget := reasoningBudgetTokens(effort, maxOutputTokens)
	if budget <= 0 {
		return map[string]interface{}{"type": "enabled"}, nil
	}
	return map[string]interface{}{"type": "enabled", "budget_tokens": budget}, nil
}

func reasoningBudgetTokens(effort string, maxOutputTokens int) int {
	if maxOutputTokens <= 0 {
		switch effort {
		case "minimal", "low":
			return 1024
		case "medium", "auto":
			return 4096
		case "high", "xhigh":
			return 8192
		default:
			return 0
		}
	}
	ratio := 0.5
	switch effort {
	case "minimal", "low":
		ratio = 0.25
	case "medium", "auto":
		ratio = 0.5
	case "high", "xhigh":
		ratio = 0.8
	default:
		return 0
	}
	budget := int(float64(maxOutputTokens) * ratio)
	if budget < 1024 {
		budget = 1024
	}
	if budget >= maxOutputTokens {
		budget = maxOutputTokens - 1
	}
	if budget <= 0 {
		return 0
	}
	return budget
}

func claudeContentBlockToResponsesItem(block map[string]interface{}) (types.ResponsesItem, bool, error) {
	blockType, _ := block["type"].(string)
	switch blockType {
	case "text":
		text, _ := block["text"].(string)
		return types.ResponsesItem{
			Type:    "message",
			Status:  "completed",
			Role:    "assistant",
			Content: []interface{}{map[string]interface{}{"type": "output_text", "text": text}},
		}, true, nil
	case "thinking":
		text, _ := block["thinking"].(string)
		if text == "" {
			text, _ = block["text"].(string)
		}
		return types.ResponsesItem{
			Type:   "reasoning",
			Status: "completed",
			Summary: []interface{}{map[string]interface{}{
				"type": "summary_text",
				"text": text,
			}},
		}, true, nil
	case "tool_use":
		id, _ := block["id"].(string)
		name, _ := block["name"].(string)
		args := "{}"
		if input, ok := block["input"]; ok {
			data, err := json.Marshal(input)
			if err != nil {
				return types.ResponsesItem{}, false, fmt.Errorf("序列化 Claude tool_use input 失败: %w", err)
			}
			args = string(data)
		}
		return types.ResponsesItem{
			ID:        fmt.Sprintf("fc_%s", id),
			Type:      "function_call",
			Status:    "completed",
			CallID:    id,
			Name:      name,
			Arguments: args,
		}, true, nil
	default:
		return types.ResponsesItem{}, false, nil
	}
}

func responsesItemToClaudeToolMessage(item types.ResponsesItem) (*types.ClaudeMessage, error) {
	switch item.Type {
	case "function_call", "custom_tool_call":
		callID := item.CallID
		if callID == "" {
			callID = strings.TrimPrefix(item.ID, "fc_")
			if callID == item.ID {
				callID = strings.TrimPrefix(item.ID, "ctc_")
			}
		}
		var input interface{} = map[string]interface{}{}
		if item.Type == "custom_tool_call" {
			input = map[string]interface{}{"input": extractTextFromContent(item.Content)}
		} else if item.Arguments != "" {
			var parsed interface{}
			if err := json.Unmarshal([]byte(item.Arguments), &parsed); err == nil {
				input = parsed
			} else {
				return nil, fmt.Errorf("function_call arguments 不是合法 JSON: %w", err)
			}
		}
		return &types.ClaudeMessage{Role: "assistant", Content: []map[string]interface{}{{"type": "tool_use", "id": callID, "name": item.Name, "input": input}}}, nil
	case "tool_call":
		if item.ToolUse == nil {
			return nil, nil
		}
		return &types.ClaudeMessage{Role: "assistant", Content: []map[string]interface{}{{"type": "tool_use", "id": item.ToolUse.ID, "name": item.ToolUse.Name, "input": item.ToolUse.Input}}}, nil
	case "tool_result":
		return &types.ClaudeMessage{Role: "user", Content: []map[string]interface{}{{"type": "tool_result", "tool_use_id": item.CallID, "content": item.Content}}}, nil
	default:
		if isResponsesToolOutputType(item.Type) {
			return &types.ClaudeMessage{Role: "user", Content: []map[string]interface{}{{"type": "tool_result", "tool_use_id": item.CallID, "content": item.Content}}}, nil
		}
		return nil, nil
	}
}

func interfaceSlice(raw interface{}) ([]interface{}, error) {
	if raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case []interface{}:
		return v, nil
	case []map[string]interface{}:
		out := make([]interface{}, len(v))
		for i := range v {
			out[i] = v[i]
		}
		return out, nil
	default:
		return nil, fmt.Errorf("期望数组，实际为 %T", raw)
	}
}
