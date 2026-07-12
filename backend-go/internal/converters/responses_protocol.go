package converters

import (
	"context"
	"encoding/json"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"fmt"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/session"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/tidwall/gjson"
)

const (
	ResponsesUpstreamOpenAI    = "openai"
	ResponsesUpstreamClaude    = "claude"
	ResponsesUpstreamResponses = "responses"
	ResponsesUpstreamGemini    = "gemini"
)

// ConvertResponsesRequestToUpstream converts a Responses entry request to the target upstream protocol.
// upstream may be nil (tests); when set it drives compact / history-thinking / prompt_cache_key options.
func ConvertResponsesRequestToUpstream(serviceType string, model string, bodyBytes []byte, stream bool, sess *session.Session, req *types.ResponsesRequest, upstream *config.UpstreamConfig) ([]byte, error) {
	switch serviceType {
	case ResponsesUpstreamResponses:
		return convertResponsesPassthroughRequest(model, bodyBytes)
	case ResponsesUpstreamOpenAI:
		return convertResponsesRequestToOpenAIChat(model, bodyBytes, stream, sess, req, upstream)
	case ResponsesUpstreamClaude:
		return convertResponsesRequestWithStructConverter(serviceType, sess, req, upstream)
	case ResponsesUpstreamGemini:
		if req == nil {
			return nil, fmt.Errorf("Responses -> Gemini 请求转换缺少解析后的请求")
		}
		return ConvertResponsesToGeminiRequest(model, sess, req)
	default:
		return nil, fmt.Errorf("Responses 上游 serviceType %q 不支持", serviceType)
	}
}

// ConvertUpstreamResponseToResponses 将上游非流式响应转换为 Responses 响应。
func ConvertUpstreamResponseToResponses(serviceType string, originalRequestJSON []byte, upstreamResponseJSON []byte, sessionID string) (*types.ResponsesResponse, error) {
	switch serviceType {
	case ResponsesUpstreamResponses:
		respMap, err := JSONToMap(upstreamResponseJSON)
		if err != nil {
			return nil, fmt.Errorf("解析 Responses 响应失败: %w", err)
		}
		converter := &ResponsesPassthroughConverter{}
		return converter.FromProviderResponse(respMap, sessionID)
	case ResponsesUpstreamOpenAI:
		converted := ConvertOpenAIChatToResponsesNonStream(context.Background(), "", originalRequestJSON, nil, upstreamResponseJSON, nil)
		var resp types.ResponsesResponse
		if err := json.Unmarshal([]byte(converted), &resp); err != nil {
			return nil, fmt.Errorf("转换 OpenAI Chat 响应失败: %w", err)
		}
		return &resp, nil
	case ResponsesUpstreamClaude:
		respMap, err := JSONToMap(upstreamResponseJSON)
		if err != nil {
			return nil, fmt.Errorf("解析 Claude 响应失败: %w", err)
		}
		converter := &ClaudeConverter{}
		return converter.FromProviderResponse(respMap, sessionID)
	case ResponsesUpstreamGemini:
		return ConvertGeminiResponseToResponses(originalRequestJSON, upstreamResponseJSON)
	default:
		return nil, fmt.Errorf("Responses 上游 serviceType %q 不支持", serviceType)
	}
}

// ConvertUpstreamStreamLineToResponses 将单行上游 SSE 转换为 Responses SSE。
func ConvertUpstreamStreamLineToResponses(ctx context.Context, serviceType string, model string, originalRequestJSON []byte, line []byte, state *any) ([]string, error) {
	switch serviceType {
	case ResponsesUpstreamResponses:
		return []string{string(line) + "\n"}, nil
	case ResponsesUpstreamOpenAI:
		return ConvertOpenAIChatToResponses(ctx, model, originalRequestJSON, nil, line, state), nil
	case ResponsesUpstreamClaude:
		return ConvertClaudeStreamToResponses(ctx, model, originalRequestJSON, line, state)
	case ResponsesUpstreamGemini:
		return ConvertGeminiStreamToResponses(ctx, model, originalRequestJSON, line, state)
	default:
		return nil, fmt.Errorf("Responses 上游 serviceType %q 不支持", serviceType)
	}
}

func convertResponsesPassthroughRequest(model string, bodyBytes []byte) ([]byte, error) {
	var reqMap map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &reqMap); err != nil {
		return nil, fmt.Errorf("透传模式下解析请求失败: %w", err)
	}
	if model != "" {
		reqMap["model"] = model
	}
	return utils.MarshalJSONNoEscape(reqMap)
}

func convertResponsesRequestToOpenAIChat(model string, bodyBytes []byte, stream bool, sess *session.Session, req *types.ResponsesRequest, upstream *config.UpstreamConfig) ([]byte, error) {
	if err := ValidateResponsesToOpenAIChatRequest(bodyBytes); err != nil {
		return nil, err
	}

	// IncludeHistoryThinking controls whether type=reasoning items become visible assistant text.
	// Default false: skip history reasoning (Chat has no native reasoning field for history).
	includeHistoryThinking := upstream != nil && upstream.IncludeHistoryThinking
	base := ConvertResponsesToOpenAIChatRequestWithOptions(model, bodyBytes, stream, includeHistoryThinking)

	var chatReq map[string]interface{}
	if err := json.Unmarshal(base, &chatReq); err != nil {
		return nil, fmt.Errorf("解析 OpenAI Chat 转换结果失败: %w", err)
	}

	currentMessages, ok := chatReq["messages"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("OpenAI Chat 转换结果缺少 messages")
	}

	// Session merge: when previous_response_id session has history, prefer session + avoid simple double-append
	// if client input already replays full history.
	if sess != nil && req != nil && len(sess.Messages) > 0 {
		if toolDefinitions, err := collectResponsesToolDefinitions(req.Tools, req.Input, sessionResponseItems(sess)); err != nil {
			return nil, fmt.Errorf("构建 OpenAI Chat tools 失败: %w", err)
		} else if chatTools, err := responsesToolDefinitionsToOpenAIChatTools(toolDefinitions); err != nil {
			return nil, fmt.Errorf("转换 OpenAI Chat tools 失败: %w", err)
		} else if len(chatTools) > 0 {
			chatReq["tools"] = chatTools
			if toolChoice, err := responsesToolChoiceToOpenAIChat(req.ToolChoice); err != nil {
				return nil, fmt.Errorf("转换 OpenAI Chat tool_choice 失败: %w", err)
			} else if toolChoice != nil {
				chatReq["tool_choice"] = toolChoice
			}
			if req.ParallelToolCalls != nil {
				chatReq["parallel_tool_calls"] = *req.ParallelToolCalls
			}
		}

		historyMessages := buildOpenAIHistoryMessages(sess, includeHistoryThinking)
		if len(historyMessages) > 0 {
			chatReq["messages"] = mergeOpenAIHistoryMessagesDedup(historyMessages, currentMessages)
		}
	}

	if _, hasTools := chatReq["tools"]; !hasTools {
		delete(chatReq, "tool_choice")
		delete(chatReq, "parallel_tool_calls")
	}

	// Context compact: truncate long tool-role contents (same policy as Messages entry).
	if messages, ok := chatReq["messages"].([]interface{}); ok && len(messages) > 0 {
		chatReq["messages"] = utils.CompactOpenAIChatMessagesForUpstream(messages)
	}

	// prompt_cache_key: stable model+instructions+tools fingerprint; respect DisablePromptCacheKey.
	if upstream == nil || !upstream.DisablePromptCacheKey {
		if existing, _ := chatReq["prompt_cache_key"].(string); strings.TrimSpace(existing) == "" {
			chatReq["prompt_cache_key"] = buildResponsesChatPromptCacheKey(model, chatReq, upstream)
		}
	} else {
		delete(chatReq, "prompt_cache_key")
	}

	out, err := utils.MarshalJSONNoEscape(chatReq)
	if err != nil {
		return nil, fmt.Errorf("序列化 OpenAI Chat 请求失败: %w", err)
	}
	return out, nil
}

func buildOpenAIHistoryMessages(sess *session.Session, includeHistoryThinking bool) []interface{} {
	if sess == nil || len(sess.Messages) == 0 {
		return nil
	}

	messages := make([]interface{}, 0, len(sess.Messages))
	for _, item := range sess.Messages {
		msg := responsesItemToOpenAIMessageWithOptions(item, includeHistoryThinking)
		if msg != nil {
			messages = append(messages, msg)
		}
	}
	return messages
}

// mergeOpenAIHistoryMessagesDedup merges session history with current input messages.
// If current input already contains the same prefix as session history (client replayed full history
// while also sending previous_response_id), keep session history + only the non-overlapping suffix.
func mergeOpenAIHistoryMessagesDedup(historyMessages []interface{}, currentMessages []interface{}) []interface{} {
	merged := make([]interface{}, 0, len(historyMessages)+len(currentMessages))

	// Keep a single leading system message from current input when present.
	if len(currentMessages) > 0 {
		if first, ok := currentMessages[0].(map[string]interface{}); ok && first["role"] == "system" {
			merged = append(merged, first)
			currentMessages = currentMessages[1:]
		}
	}

	// Strip system from history for overlap comparison (system already handled).
	historyNonSystem := make([]interface{}, 0, len(historyMessages))
	for _, msg := range historyMessages {
		if m, ok := msg.(map[string]interface{}); ok && m["role"] == "system" {
			continue
		}
		historyNonSystem = append(historyNonSystem, msg)
	}

	// Detect overlap: if current starts with the same sequence as history, only append the suffix.
	overlap := 0
	maxCheck := len(historyNonSystem)
	if len(currentMessages) < maxCheck {
		maxCheck = len(currentMessages)
	}
	// Prefer longest prefix match of history against start of current.
	for candidate := maxCheck; candidate > 0; candidate-- {
		if openAIChatMessagePrefixEqual(historyNonSystem[:candidate], currentMessages[:candidate]) {
			overlap = candidate
			break
		}
	}

	if overlap > 0 && overlap == len(historyNonSystem) {
		// Current fully includes history (or more): use current only (already has history).
		merged = append(merged, currentMessages...)
		return merged
	}
	if overlap > 0 {
		// Partial prefix overlap: keep full history then non-overlapping current suffix.
		merged = append(merged, historyNonSystem...)
		merged = append(merged, currentMessages[overlap:]...)
		return merged
	}

	// No overlap: session + current (classic previous_response_id incremental input).
	merged = append(merged, historyNonSystem...)
	merged = append(merged, currentMessages...)
	return merged
}

func openAIChatMessagePrefixEqual(left []interface{}, right []interface{}) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		leftMsg, leftOK := left[index].(map[string]interface{})
		rightMsg, rightOK := right[index].(map[string]interface{})
		if !leftOK || !rightOK {
			return false
		}
		if leftMsg["role"] != rightMsg["role"] {
			return false
		}
		// Compare tool_call_id for tool messages (stable id).
		if leftMsg["role"] == "tool" {
			if leftMsg["tool_call_id"] != rightMsg["tool_call_id"] {
				return false
			}
			continue
		}
		// Compare content string when both are strings; otherwise compare JSON fingerprint.
		leftContent, leftIsString := leftMsg["content"].(string)
		rightContent, rightIsString := rightMsg["content"].(string)
		if leftIsString && rightIsString {
			if leftContent != rightContent {
				return false
			}
			continue
		}
		leftRaw, _ := json.Marshal(leftMsg["content"])
		rightRaw, _ := json.Marshal(rightMsg["content"])
		if string(leftRaw) != string(rightRaw) {
			return false
		}
		// Also compare tool_calls if present.
		if leftMsg["tool_calls"] != nil || rightMsg["tool_calls"] != nil {
			leftTools, _ := json.Marshal(leftMsg["tool_calls"])
			rightTools, _ := json.Marshal(rightMsg["tool_calls"])
			if string(leftTools) != string(rightTools) {
				return false
			}
		}
	}
	return true
}

func buildResponsesChatPromptCacheKey(model string, chatReq map[string]interface{}, upstream *config.UpstreamConfig) string {
	channel := ""
	baseURL := ""
	if upstream != nil {
		channel = strings.TrimSpace(upstream.Name)
		baseURL = strings.TrimRight(upstream.GetEffectiveBaseURL(), "/#")
	}
	instructions := ""
	if messages, ok := chatReq["messages"].([]interface{}); ok {
		for _, raw := range messages {
			msg, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if msg["role"] == "system" {
				if text, ok := msg["content"].(string); ok {
					instructions = text
					break
				}
			}
		}
	}
	stableParts := map[string]interface{}{
		"protocol":     "responses-to-openai-chat-v1",
		"model":        model,
		"channel":      channel,
		"baseURL":      baseURL,
		"instructions": instructions,
		"tools":        chatReq["tools"],
	}
	sum := sha256.Sum256([]byte(canonicalJSONForCacheKey(stableParts)))
	return "resp-chat-" + hex.EncodeToString(sum[:])[:24]
}

func canonicalJSONForCacheKey(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		data, _ := json.Marshal(typed)
		return string(data)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64, float32, int, int64, int32, uint, uint64, uint32, json.Number:
		data, _ := json.Marshal(typed)
		return string(data)
	case []interface{}:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, canonicalJSONForCacheKey(item))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]interface{}:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			keyJSON, _ := json.Marshal(key)
			parts = append(parts, string(keyJSON)+":"+canonicalJSONForCacheKey(typed[key]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		var normalized interface{}
		if err := json.Unmarshal(data, &normalized); err != nil {
			return string(data)
		}
		return canonicalJSONForCacheKey(normalized)
	}
}

func convertResponsesRequestWithStructConverter(serviceType string, sess *session.Session, req *types.ResponsesRequest, upstream *config.UpstreamConfig) ([]byte, error) {
	converter, err := NewConverterStrict(serviceType)
	if err != nil {
		return nil, err
	}
	// Inject channel options for ClaudeConverter (and future converters that care).
	if claudeConverter, ok := converter.(*ClaudeConverter); ok {
		claudeConverter.Upstream = upstream
	}
	convertedReq, err := converter.ToProviderRequest(sess, req)
	if err != nil {
		return nil, fmt.Errorf("转换请求失败: %w", err)
	}
	return utils.MarshalJSONNoEscape(convertedReq)
}

func ResponsesRequestStream(bodyBytes []byte) bool {
	return gjson.ParseBytes(bodyBytes).Get("stream").Bool()
}
