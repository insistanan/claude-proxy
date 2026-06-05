package converters

import (
	"encoding/json"
	"fmt"

	"github.com/BenedictKing/claude-proxy/internal/session"
	"github.com/BenedictKing/claude-proxy/internal/types"
)

// ============== OpenAI Chat Completions 转换器 ==============

// OpenAIChatConverter 实现 Responses → OpenAI Chat Completions 转换
type OpenAIChatConverter struct{}

// ToProviderRequest 将 Responses 请求转换为 OpenAI Chat Completions 格式
func (c *OpenAIChatConverter) ToProviderRequest(sess *session.Session, req *types.ResponsesRequest) (interface{}, error) {
	// 转换 messages
	messages, err := ResponsesToOpenAIChatMessages(sess, req.Input, req.Instructions)
	if err != nil {
		return nil, err
	}

	// 构建 OpenAI 请求
	openaiReq := map[string]interface{}{
		"model":    req.Model,
		"messages": messages,
		"stream":   req.Stream,
	}

	// 复制其他参数
	if req.MaxOutputTokens > 0 {
		openaiReq["max_tokens"] = req.MaxOutputTokens
	} else if req.MaxTokens > 0 {
		openaiReq["max_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		openaiReq["temperature"] = req.Temperature
	}
	if req.TopP > 0 {
		openaiReq["top_p"] = req.TopP
	}
	if req.FrequencyPenalty != 0 {
		openaiReq["frequency_penalty"] = req.FrequencyPenalty
	}
	if req.PresencePenalty != 0 {
		openaiReq["presence_penalty"] = req.PresencePenalty
	}
	if req.Stop != nil {
		openaiReq["stop"] = req.Stop
	}
	if req.User != "" {
		openaiReq["user"] = req.User
	}
	if req.StreamOptions != nil {
		openaiReq["stream_options"] = req.StreamOptions
	}
	if tools, err := responsesToolsToOpenAIChatTools(req.Tools); err != nil {
		return nil, err
	} else if len(tools) > 0 {
		openaiReq["tools"] = tools
	}
	if toolChoice, err := responsesToolChoiceToOpenAIChat(req.ToolChoice); err != nil {
		return nil, err
	} else if toolChoice != nil {
		openaiReq["tool_choice"] = toolChoice
	}
	if req.ParallelToolCalls != nil {
		openaiReq["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if reasoningEffort, err := responsesReasoningEffortToOpenAIChat(req.Reasoning); err != nil {
		return nil, err
	} else if reasoningEffort != "" {
		openaiReq["reasoning_effort"] = reasoningEffort
	}

	return openaiReq, nil
}

// FromProviderResponse 将 OpenAI Chat 响应转换为 Responses 格式
func (c *OpenAIChatConverter) FromProviderResponse(resp map[string]interface{}, sessionID string) (*types.ResponsesResponse, error) {
	return OpenAIChatResponseToResponses(resp, sessionID)
}

// GetProviderName 获取上游服务名称
func (c *OpenAIChatConverter) GetProviderName() string {
	return "OpenAI Chat Completions"
}

// ============== OpenAI Completions 转换器 ==============

// OpenAICompletionsConverter 实现 Responses → OpenAI Completions 转换
type OpenAICompletionsConverter struct{}

// ToProviderRequest 将 Responses 请求转换为 OpenAI Completions 格式
func (c *OpenAICompletionsConverter) ToProviderRequest(sess *session.Session, req *types.ResponsesRequest) (interface{}, error) {
	// 提取纯文本（Completions API 不支持 messages）
	prompt, err := ExtractTextFromResponses(sess, req.Input)
	if err != nil {
		return nil, err
	}

	// 如果有 instructions，添加到 prompt 前面
	if req.Instructions != "" {
		prompt = req.Instructions + "\n\n" + prompt
	}

	// 构建 OpenAI Completions 请求
	completionsReq := map[string]interface{}{
		"model":  req.Model,
		"prompt": prompt,
		"stream": req.Stream,
	}

	// 复制其他参数
	if req.MaxOutputTokens > 0 {
		completionsReq["max_tokens"] = req.MaxOutputTokens
	} else if req.MaxTokens > 0 {
		completionsReq["max_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		completionsReq["temperature"] = req.Temperature
	}
	if req.TopP > 0 {
		completionsReq["top_p"] = req.TopP
	}
	if req.FrequencyPenalty != 0 {
		completionsReq["frequency_penalty"] = req.FrequencyPenalty
	}
	if req.PresencePenalty != 0 {
		completionsReq["presence_penalty"] = req.PresencePenalty
	}
	if req.Stop != nil {
		completionsReq["stop"] = req.Stop
	}
	if req.User != "" {
		completionsReq["user"] = req.User
	}
	if req.Tools != nil {
		return nil, fmt.Errorf("OpenAI Completions 不支持 tools 字段")
	}
	if req.ToolChoice != nil {
		return nil, fmt.Errorf("OpenAI Completions 不支持 tool_choice 字段")
	}
	if req.ParallelToolCalls != nil {
		return nil, fmt.Errorf("OpenAI Completions 不支持 parallel_tool_calls 字段")
	}
	if req.Reasoning != nil {
		return nil, fmt.Errorf("OpenAI Completions 不支持 reasoning 字段")
	}

	return completionsReq, nil
}

// FromProviderResponse 将 OpenAI Completions 响应转换为 Responses 格式
func (c *OpenAICompletionsConverter) FromProviderResponse(resp map[string]interface{}, sessionID string) (*types.ResponsesResponse, error) {
	return OpenAICompletionsResponseToResponses(resp, sessionID)
}

// GetProviderName 获取上游服务名称
func (c *OpenAICompletionsConverter) GetProviderName() string {
	return "OpenAI Completions"
}

func responsesToolsToOpenAIChatTools(raw interface{}) ([]map[string]interface{}, error) {
	if raw == nil {
		return nil, nil
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("Responses tools 序列化失败: %w", err)
	}

	var tools []map[string]interface{}
	if err := json.Unmarshal(data, &tools); err != nil {
		return nil, fmt.Errorf("Responses tools 必须是数组: %w", err)
	}

	out := make([]map[string]interface{}, 0, len(tools))
	for i, tool := range tools {
		toolType, _ := tool["type"].(string)
		if toolType != "" && toolType != "function" {
			return nil, fmt.Errorf("OpenAI Chat 不支持第 %d 个 tool 类型 %q", i, toolType)
		}

		function := map[string]interface{}{}
		if nested, ok := tool["function"].(map[string]interface{}); ok {
			for k, v := range nested {
				function[k] = v
			}
		} else {
			copyToolField(function, tool, "name")
			copyToolField(function, tool, "description")
			copyToolField(function, tool, "parameters")
			copyToolField(function, tool, "strict")
		}

		name, _ := function["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("OpenAI Chat tool 第 %d 项缺少 function.name", i)
		}

		out = append(out, map[string]interface{}{
			"type":     "function",
			"function": function,
		})
	}

	return out, nil
}

func responsesToolChoiceToOpenAIChat(raw interface{}) (interface{}, error) {
	if raw == nil {
		return nil, nil
	}

	if choice, ok := raw.(string); ok {
		switch choice {
		case "auto", "none", "required":
			return choice, nil
		default:
			return nil, fmt.Errorf("OpenAI Chat 不支持 tool_choice=%q", choice)
		}
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("Responses tool_choice 序列化失败: %w", err)
	}

	var choice map[string]interface{}
	if err := json.Unmarshal(data, &choice); err != nil {
		return nil, fmt.Errorf("Responses tool_choice 必须是字符串或对象: %w", err)
	}

	choiceType, _ := choice["type"].(string)
	if choiceType == "" {
		choiceType = "function"
	}
	if choiceType != "function" {
		return nil, fmt.Errorf("OpenAI Chat 不支持 tool_choice.type=%q", choiceType)
	}

	if function, ok := choice["function"].(map[string]interface{}); ok {
		if name, _ := function["name"].(string); name != "" {
			return map[string]interface{}{
				"type":     "function",
				"function": map[string]interface{}{"name": name},
			}, nil
		}
	}
	if name, _ := choice["name"].(string); name != "" {
		return map[string]interface{}{
			"type":     "function",
			"function": map[string]interface{}{"name": name},
		}, nil
	}

	return nil, fmt.Errorf("OpenAI Chat tool_choice 对象缺少 function.name 或 name")
}

func responsesReasoningEffortToOpenAIChat(raw interface{}) (string, error) {
	if raw == nil {
		return "", nil
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("Responses reasoning 序列化失败: %w", err)
	}

	var reasoning map[string]interface{}
	if err := json.Unmarshal(data, &reasoning); err != nil {
		return "", fmt.Errorf("Responses reasoning 必须是对象: %w", err)
	}

	effort, _ := reasoning["effort"].(string)
	switch effort {
	case "", "auto":
		return "", nil
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return effort, nil
	default:
		return "", fmt.Errorf("OpenAI Chat 不支持 reasoning.effort=%q", effort)
	}
}

func copyToolField(dst, src map[string]interface{}, key string) {
	if value, ok := src[key]; ok {
		dst[key] = value
	}
}
