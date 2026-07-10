package converters

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/session"
	"github.com/BenedictKing/claude-proxy/internal/types"
)

const openAIChatToolSearchName = "tool_search"
const openAIChatWebSearchName = "web_search"

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
	if streamOptions := openAIChatStreamOptions(req); streamOptions != nil {
		openaiReq["stream_options"] = streamOptions
	}
	toolDefinitions, err := collectResponsesToolDefinitions(req.Tools, req.Input, sessionResponseItems(sess))
	if err != nil {
		return nil, err
	}
	if tools, err := responsesToolDefinitionsToOpenAIChatTools(toolDefinitions); err != nil {
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
	if _, hasTools := openaiReq["tools"]; !hasTools {
		delete(openaiReq, "tool_choice")
		delete(openaiReq, "parallel_tool_calls")
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
	tools, err := collectResponsesToolDefinitions(raw)
	if err != nil {
		return nil, err
	}
	return responsesToolDefinitionsToOpenAIChatTools(tools)
}

func responsesToolDefinitionsToOpenAIChatTools(tools []map[string]interface{}) ([]map[string]interface{}, error) {
	if len(tools) == 0 {
		return nil, nil
	}

	out := make([]map[string]interface{}, 0, len(tools))
	for i, tool := range tools {
		chatTools, err := buildOpenAIChatToolsFromResponsesTool(tool, i)
		if err != nil {
			return nil, err
		}
		out = append(out, chatTools...)
	}

	return out, nil
}

func sessionResponseItems(sess *session.Session) interface{} {
	if sess == nil || len(sess.Messages) == 0 {
		return nil
	}
	return sess.Messages
}

func collectResponsesToolDefinitions(toolsRaw interface{}, inputRaws ...interface{}) ([]map[string]interface{}, error) {
	out := make([]map[string]interface{}, 0)
	seen := make(map[string]struct{})

	if err := appendResponsesToolDefinitions(&out, seen, toolsRaw); err != nil {
		return nil, fmt.Errorf("Responses tools 必须是数组: %w", err)
	}
	for _, inputRaw := range inputRaws {
		appendResponsesToolDefinitionsFromToolSearchOutputs(&out, seen, inputRaw)
	}

	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func appendResponsesToolDefinitions(out *[]map[string]interface{}, seen map[string]struct{}, raw interface{}) error {
	if raw == nil {
		return nil
	}

	tools, err := normalizeMapSlice(raw)
	if err != nil {
		return err
	}
	for _, tool := range tools {
		key := responseToolDefinitionKey(tool)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		*out = append(*out, tool)
	}
	return nil
}

func appendResponsesToolDefinitionsFromToolSearchOutputs(out *[]map[string]interface{}, seen map[string]struct{}, raw interface{}) {
	switch value := raw.(type) {
	case []interface{}:
		for _, item := range value {
			appendResponsesToolDefinitionsFromToolSearchOutputs(out, seen, item)
		}
	case map[string]interface{}:
		itemType, _ := value["type"].(string)
		if itemType == "tool_search_output" {
			_ = appendResponsesToolDefinitions(out, seen, value["tools"])
		}
		for _, nested := range value {
			appendResponsesToolDefinitionsFromToolSearchOutputs(out, seen, nested)
		}
	case []types.ResponsesItem:
		for _, item := range value {
			appendResponsesToolDefinitionsFromToolSearchOutputs(out, seen, item)
		}
	case types.ResponsesItem:
		if value.Type == "tool_search_output" {
			_ = appendResponsesToolDefinitions(out, seen, value.Tools)
		}
	}
}

func responseToolDefinitionKey(tool map[string]interface{}) string {
	spec := normalizeResponsesToolDefinition(tool)
	switch spec.ToolType {
	case "namespace":
		return "namespace:" + strings.TrimSpace(spec.Name)
	case "tool_search":
		return "tool_search"
	case "web_search", "web_search_preview":
		return spec.ToolType
	case "custom":
		return "custom:" + strings.TrimSpace(spec.Name)
	default:
		return "function:" + strings.TrimSpace(spec.Name)
	}
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

	choice, err := normalizeMap(raw)
	if err != nil {
		return nil, fmt.Errorf("Responses tool_choice 必须是字符串或对象: %w", err)
	}

	choiceType, _ := choice["type"].(string)
	if choiceType == "" {
		choiceType = "function"
	}
	if choiceType != "function" && choiceType != "custom" && choiceType != "tool_search" && choiceType != "web_search" && choiceType != "web_search_preview" {
		return nil, fmt.Errorf("OpenAI Chat 不支持 tool_choice.type=%q", choiceType)
	}
	if choiceType == "tool_search" {
		return map[string]interface{}{
			"type":     "function",
			"function": map[string]interface{}{"name": openAIChatToolSearchName},
		}, nil
	}
	if choiceType == "web_search" || choiceType == "web_search_preview" {
		return map[string]interface{}{
			"type":     "function",
			"function": map[string]interface{}{"name": openAIChatWebSearchName},
		}, nil
	}

	if function, ok := choice["function"].(map[string]interface{}); ok {
		if name, _ := function["name"].(string); name != "" {
			if namespace, _ := choice["namespace"].(string); strings.TrimSpace(namespace) != "" {
				name = flattenNamespaceToolName(namespace, name)
			}
			return map[string]interface{}{
				"type":     "function",
				"function": map[string]interface{}{"name": name},
			}, nil
		}
	}
	if name := extractNamedToolChoice(choice); name != "" {
		if namespace, _ := choice["namespace"].(string); strings.TrimSpace(namespace) != "" {
			name = flattenNamespaceToolName(namespace, name)
		}
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

	reasoning, err := normalizeMap(raw)
	if err != nil {
		return "", fmt.Errorf("Responses reasoning 必须是对象: %w", err)
	}

	effort, _ := reasoning["effort"].(string)
	normalizedEffort, err := normalizeReasoningEffortForConstrainedUpstream(effort)
	if err != nil {
		return "", fmt.Errorf("OpenAI Chat %w", err)
	}
	switch normalizedEffort {
	case "", "auto":
		return "", nil
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return normalizedEffort, nil
	default:
		return "", fmt.Errorf("OpenAI Chat 不支持 reasoning.effort=%q", effort)
	}
}

func buildOpenAIChatToolsFromResponsesTool(tool map[string]interface{}, index int) ([]map[string]interface{}, error) {
	spec := normalizeResponsesToolDefinition(tool)
	switch spec.ToolType {
	case "", "function", "custom":
		if spec.Name == "" {
			return nil, fmt.Errorf("OpenAI Chat tool 第 %d 项缺少 function.name", index)
		}
		return []map[string]interface{}{{
			"type":     "function",
			"function": spec.Function,
		}}, nil
	case "namespace":
		return buildOpenAIChatNamespaceTools(tool, index)
	case "tool_search":
		return []map[string]interface{}{buildOpenAIChatToolSearchTool()}, nil
	case "web_search", "web_search_preview":
		return []map[string]interface{}{buildOpenAIChatWebSearchTool(tool)}, nil
	default:
		return nil, fmt.Errorf("OpenAI Chat 不支持第 %d 个 Responses tool type %q", index, spec.ToolType)
	}
}

func buildOpenAIChatNamespaceTools(tool map[string]interface{}, index int) ([]map[string]interface{}, error) {
	namespace, _ := tool["name"].(string)
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return nil, fmt.Errorf("OpenAI Chat namespace tool 第 %d 项缺少 name", index)
	}

	rawChildren := tool["tools"]
	if rawChildren == nil {
		rawChildren = tool["children"]
	}
	children, err := normalizeMapSlice(rawChildren)
	if err != nil {
		return nil, fmt.Errorf("OpenAI Chat namespace tool 第 %d 项 children 非法: %w", index, err)
	}

	out := make([]map[string]interface{}, 0, len(children))
	for childIndex, child := range children {
		childSpec := normalizeResponsesToolDefinition(child)
		if childSpec.ToolType != "" && childSpec.ToolType != "function" {
			continue
		}
		if childSpec.Name == "" {
			continue
		}
		function := map[string]interface{}{}
		for k, v := range childSpec.Function {
			function[k] = v
		}
		function["name"] = flattenNamespaceToolName(namespace, childSpec.Name)
		if _, ok := function["parameters"]; !ok || function["parameters"] == nil {
			function["parameters"] = emptyOpenAIChatToolParameters()
		}
		out = append(out, map[string]interface{}{
			"type":     "function",
			"function": function,
		})
		_ = childIndex
	}
	return out, nil
}

func buildOpenAIChatToolSearchTool() map[string]interface{} {
	return map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        openAIChatToolSearchName,
			"description": "Search and load Codex tools, plugins, connectors, and MCP namespaces for the current task.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Search query for tools or connectors to load.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of tool groups to return.",
					},
				},
				"required": []string{"query"},
			},
		},
	}
}

func buildOpenAIChatWebSearchTool(tool map[string]interface{}) map[string]interface{} {
	description, _ := tool["description"].(string)
	if strings.TrimSpace(description) == "" {
		description = "Search the web for relevant information."
	}

	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query to run on the web.",
			},
		},
		"required": []string{"query"},
	}
	if rawParameters, ok := tool["parameters"].(map[string]interface{}); ok && rawParameters != nil {
		parameters = rawParameters
	}

	return map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        openAIChatWebSearchName,
			"description": description,
			"parameters":  parameters,
		},
	}
}

func flattenNamespaceToolName(namespace string, name string) string {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" {
		return name
	}
	if name == "" {
		return namespace
	}
	return namespace + "__" + name
}

func customResponsesToolParameters(tool map[string]interface{}) interface{} {
	if parameters, ok := tool["parameters"]; ok && parameters != nil {
		return parameters
	}
	if inputSchema, ok := tool["input_schema"]; ok && inputSchema != nil {
		return inputSchema
	}
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"input": map[string]interface{}{
				"type": "string",
			},
		},
		"required":             []string{"input"},
		"additionalProperties": false,
	}
}

func customResponsesToolDescription(tool map[string]interface{}) string {
	description, _ := tool["description"].(string)
	metadata, err := json.Marshal(tool)
	if err != nil {
		return description
	}
	metadataText := strings.TrimSpace(string(metadata))
	if metadataText == "" || metadataText == "{}" {
		return description
	}
	if strings.TrimSpace(description) == "" {
		return "Original tool definition:\n" + metadataText
	}
	return description + "\n\nOriginal tool definition:\n" + metadataText
}

func emptyOpenAIChatToolParameters() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func normalizeMapSlice(raw interface{}) ([]map[string]interface{}, error) {
	switch v := raw.(type) {
	case []map[string]interface{}:
		return v, nil
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(v))
		for i, item := range v {
			m, ok := item.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("第 %d 项不是对象", i)
			}
			out = append(out, m)
		}
		return out, nil
	default:
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var out []map[string]interface{}
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
}

func normalizeMap(raw interface{}) (map[string]interface{}, error) {
	if m, ok := raw.(map[string]interface{}); ok {
		return m, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func openAIChatStreamOptions(req *types.ResponsesRequest) interface{} {
	if req == nil || !req.Stream {
		return nil
	}
	if req.StreamOptions == nil {
		return map[string]interface{}{"include_usage": true}
	}
	options, ok := req.StreamOptions.(map[string]interface{})
	if !ok {
		return req.StreamOptions
	}
	cloned := make(map[string]interface{}, len(options)+1)
	for key, value := range options {
		cloned[key] = value
	}
	if _, exists := cloned["include_usage"]; !exists {
		cloned["include_usage"] = true
	}
	return cloned
}
