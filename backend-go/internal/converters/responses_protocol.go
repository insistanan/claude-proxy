package converters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	ResponsesUpstreamOpenAI    = "openai"
	ResponsesUpstreamClaude    = "claude"
	ResponsesUpstreamResponses = "responses"
	ResponsesUpstreamGemini    = "gemini"
)

// ConvertResponsesRequestToUpstream converts a Responses entry request to the target upstream protocol.
// upstream may be nil (tests); when set it drives history-thinking and prompt_cache_key options.
func ConvertResponsesRequestToUpstream(serviceType string, model string, bodyBytes []byte, stream bool, sess *types.Session, req *types.ResponsesRequest, upstream *config.UpstreamConfig) ([]byte, error) {
	switch serviceType {
	case ResponsesUpstreamResponses:
		return convertResponsesPassthroughRequestWithSession(model, bodyBytes, upstream, sess, req)
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

func convertResponsesPassthroughRequest(model string, bodyBytes []byte, upstream *config.UpstreamConfig) ([]byte, error) {
	return convertResponsesPassthroughRequestWithSession(model, bodyBytes, upstream, nil, nil)
}

// convertResponsesPassthroughRequestWithSession 保留 Responses 原始 item 的全部扩展
// 字段，只在 previous_response_id 对应的本地 session 能确认客户端重放了完整历史时，
// 从 input 数组裁掉已经存在于服务端链上的前缀。这样不会把客户端的工具结果、图片
// 或 encrypted_content 重新编码成代理内部结构，也不会误删只发送新增后缀的正常请求。
func convertResponsesPassthroughRequestWithSession(
	model string,
	bodyBytes []byte,
	upstream *config.UpstreamConfig,
	sess *types.Session,
	req *types.ResponsesRequest,
) ([]byte, error) {
	if !gjson.ValidBytes(bodyBytes) {
		return nil, fmt.Errorf("透传模式下解析请求失败: 请求体不是有效 JSON")
	}

	result := bodyBytes
	var err error

	// 只原位修改顶层字段，避免为模型映射重排整份 Responses 请求。
	if model != "" && gjson.GetBytes(result, "model").String() != model {
		result, err = sjson.SetBytes(result, "model", model)
		if err != nil {
			return nil, fmt.Errorf("透传模式下修改 model 失败: %w", err)
		}
	}

	if upstream != nil && upstream.DisablePromptCacheKey {
		if gjson.GetBytes(result, "prompt_cache_key").Exists() {
			result, err = sjson.DeleteBytes(result, "prompt_cache_key")
			if err != nil {
				return nil, fmt.Errorf("透传模式下删除 prompt_cache_key 失败: %w", err)
			}
		}
		if gjson.GetBytes(result, "prompt_cache_retention").Exists() {
			result, err = sjson.DeleteBytes(result, "prompt_cache_retention")
			if err != nil {
				return nil, fmt.Errorf("透传模式下删除 prompt_cache_retention 失败: %w", err)
			}
		}
	}

	if req != nil && strings.TrimSpace(req.PreviousResponseID) != "" {
		result, err = trimResponsesPassthroughInput(result, req, sess)
		if err != nil {
			return nil, err
		}
	}

	return result, nil
}

func trimResponsesPassthroughInput(bodyBytes []byte, req *types.ResponsesRequest, sess *types.Session) ([]byte, error) {
	if req == nil || req.Input == nil {
		return bodyBytes, nil
	}

	input := gjson.GetBytes(bodyBytes, "input")
	if !input.Exists() || !input.IsArray() {
		return bodyBytes, nil
	}

	var rawItems []json.RawMessage
	if err := json.Unmarshal([]byte(input.Raw), &rawItems); err != nil {
		return nil, fmt.Errorf("透传模式下解析 input 数组失败: %w", err)
	}
	if len(rawItems) == 0 {
		return bodyBytes, nil
	}
	if utils.ResponsesRawItemsContainCompaction(rawItems) {
		// compaction item 已经是新的上下文根。继续携带旧的
		// previous_response_id 会把压缩前的服务端链和压缩 item 叠加发送，
		// 这是压缩后上下文迅速恢复到旧长度的直接原因。
		trimmed, err := sjson.DeleteBytes(bodyBytes, "previous_response_id")
		if err != nil {
			return nil, fmt.Errorf("透传模式下删除压缩后的 previous_response_id 失败: %w", err)
		}
		return trimmed, nil
	}
	if sess == nil || len(sess.Messages) == 0 {
		return bodyBytes, nil
	}

	parsedInput, err := parseResponsesInput(req.Input)
	if err != nil {
		return nil, fmt.Errorf("透传模式下解析 input item 失败: %w", err)
	}
	// 内部解析器无法完整识别某个扩展 item 时，禁止按错位下标裁剪原始数组。
	// 保留原请求比误删工具结果更安全，也让上游返回明确的协议错误。
	if len(parsedInput) != len(rawItems) {
		return bodyBytes, nil
	}

	prefixEnd, ok := utils.ResponsesItemsPrefixMatchRaw(sess.Messages, rawItems)
	if !ok || prefixEnd <= 0 || prefixEnd > len(rawItems) {
		// 前缀无法确认时，不能把“看起来像完整历史”的 input 与旧服务端
		// 链同时发送。那会让同一轮历史被计费两次，并把 Cursor 的上下文
		// 迅速推满。只有明显是新增工具结果后缀时才保留 previous_response_id；
		// 其余长 input 去掉旧链，按客户端给出的完整历史建立新边界。
		if looksLikeResponsesFullReplay(sess.Messages, rawItems) {
			trimmed, err := sjson.DeleteBytes(bodyBytes, "previous_response_id")
			if err != nil {
				return nil, fmt.Errorf("透传模式下删除无法确认历史的 previous_response_id 失败: %w", err)
			}
			return trimmed, nil
		}
		return bodyBytes, nil
	}

	suffix := rawItems[prefixEnd:]
	if suffix == nil {
		suffix = []json.RawMessage{}
	}
	suffixJSON, err := json.Marshal(suffix)
	if err != nil {
		return nil, fmt.Errorf("透传模式下序列化 input 后缀失败: %w", err)
	}
	trimmed, err := sjson.SetRawBytes(bodyBytes, "input", suffixJSON)
	if err != nil {
		return nil, fmt.Errorf("透传模式下裁剪重复 input 失败: %w", err)
	}
	return trimmed, nil
}

func looksLikeResponsesFullReplay(history []types.ResponsesItem, rawItems []json.RawMessage) bool {
	visibleHistoryCount := 0
	for _, item := range history {
		if strings.EqualFold(strings.TrimSpace(item.Type), "reasoning") {
			continue
		}
		visibleHistoryCount++
	}
	if visibleHistoryCount == 0 || len(rawItems) < visibleHistoryCount || len(rawItems) == 0 {
		return false
	}
	var first map[string]interface{}
	if err := json.Unmarshal(rawItems[0], &first); err != nil {
		return false
	}
	itemType, _ := first["type"].(string)
	// Responses 的增量工具回合通常从 function_call_output 开始；这类
	// 后缀不能因为 item 数量刚好较多而被误判成完整历史。
	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case "function_call_output", "custom_tool_call_output", "tool_result":
		return false
	default:
		return true
	}
}

func convertResponsesRequestToOpenAIChat(model string, bodyBytes []byte, stream bool, sess *types.Session, req *types.ResponsesRequest, upstream *config.UpstreamConfig) ([]byte, error) {
	if err := ValidateResponsesToOpenAIChatRequest(bodyBytes); err != nil {
		return nil, err
	}
	if req != nil && utils.ResponsesItemsContainCompactionFromInput(req.Input) {
		return nil, fmt.Errorf("Responses compaction item 只能由 Responses 原生上游处理，无法转换为 OpenAI Chat")
	}

	// IncludeHistoryThinking controls whether type=reasoning items become visible assistant text.
	// Default false: skip history reasoning (Chat has no native reasoning field for history).
	includeHistoryThinking := upstream != nil && upstream.IncludeHistoryThinking
	conversionBody := bodyBytes
	var mergedItems []types.ResponsesItem
	if sess != nil && req != nil && len(sess.Messages) > 0 {
		currentItems, err := parseResponsesInput(req.Input)
		if err != nil {
			return nil, fmt.Errorf("解析 Responses input 失败: %w", err)
		}
		mergedItems = utils.MergeResponsesItemsDedup(sess.Messages, currentItems)
		if utils.ResponsesItemsContainCompaction(mergedItems) {
			return nil, fmt.Errorf("Responses compaction item 只能由 Responses 原生上游处理，无法转换为 OpenAI Chat")
		}
		conversionBody, err = sjson.SetBytes(bodyBytes, "input", mergedItems)
		if err != nil {
			return nil, fmt.Errorf("构建 OpenAI Chat 合并 input 失败: %w", err)
		}
	}
	base := ConvertResponsesToOpenAIChatRequestWithOptions(model, conversionBody, stream, includeHistoryThinking)

	var chatReq map[string]interface{}
	if err := json.Unmarshal(base, &chatReq); err != nil {
		return nil, fmt.Errorf("解析 OpenAI Chat 转换结果失败: %w", err)
	}

	if _, ok := chatReq["messages"].([]interface{}); !ok {
		return nil, fmt.Errorf("OpenAI Chat 转换结果缺少 messages")
	}

	// session input 已在 Responses item 层合并；这里仅补齐从历史 tool_search_output
	// 恢复出来的工具定义，避免工具转换因协议字段缺失而静默退化。
	if len(mergedItems) > 0 && req != nil {
		if toolDefinitions, err := collectResponsesToolDefinitions(req.Tools, mergedItems); err != nil {
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
	}

	if _, hasTools := chatReq["tools"]; !hasTools {
		delete(chatReq, "tool_choice")
		delete(chatReq, "parallel_tool_calls")
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

func buildOpenAIHistoryMessages(sess *types.Session, includeHistoryThinking bool) []interface{} {
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
	hasCurrentSystem := false
	if len(currentMessages) > 0 {
		if first, ok := currentMessages[0].(map[string]interface{}); ok && first["role"] == "system" {
			merged = append(merged, first)
			currentMessages = currentMessages[1:]
			hasCurrentSystem = true
		}
	}

	// instructions 是当前请求独立携带的 system message，通常不在 session 历史中；
	// 只有当前确实有该字段时才从历史比较序列移除 system。没有当前 instructions
	// 时必须保留历史 system，不能为了去重静默丢掉用户显式发送的系统消息。
	historyComparable := historyMessages
	if hasCurrentSystem {
		historyComparable = make([]interface{}, 0, len(historyMessages))
		for _, msg := range historyMessages {
			if m, ok := msg.(map[string]interface{}); ok && m["role"] == "system" {
				continue
			}
			historyComparable = append(historyComparable, msg)
		}
	}

	// 优先寻找最长连续前缀。完整前缀可以确定是客户端重放；部分前缀只有
	// 跨过至少一轮 assistant/user 或工具调用边界时才裁剪，避免吞掉一条
	// 恰好与历史首条相同的新输入。
	overlap := 0
	maxCheck := len(historyComparable)
	if len(currentMessages) < maxCheck {
		maxCheck = len(currentMessages)
	}
	for candidate := maxCheck; candidate > 0; candidate-- {
		if openAIChatMessagePrefixEqual(historyComparable[:candidate], currentMessages[:candidate]) {
			overlap = candidate
			break
		}
	}
	if overlap == len(historyComparable) {
		// Current fully includes history (or more): use current only (already has history).
		merged = append(merged, currentMessages...)
		return merged
	}
	if overlap >= 2 && overlap < len(currentMessages) &&
		openAIChatReplayBoundary(historyComparable[overlap-1], currentMessages[overlap]) {
		merged = append(merged, historyComparable...)
		merged = append(merged, currentMessages[overlap:]...)
		return merged
	}

	// No overlap: session + current (classic previous_response_id incremental input).
	merged = append(merged, historyComparable...)
	merged = append(merged, currentMessages...)
	return merged
}

func openAIChatReplayBoundary(previous, next interface{}) bool {
	previousMessage, previousOK := previous.(map[string]interface{})
	nextMessage, nextOK := next.(map[string]interface{})
	if !previousOK || !nextOK {
		return false
	}
	previousRole, _ := previousMessage["role"].(string)
	nextRole, _ := nextMessage["role"].(string)
	previousRole = strings.ToLower(strings.TrimSpace(previousRole))
	nextRole = strings.ToLower(strings.TrimSpace(nextRole))
	if nextRole == "tool" {
		return true
	}
	return nextRole == "user" && previousRole == "assistant"
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
		if semanticJSON(canonicalOpenAIChatMessage(leftMsg)) != semanticJSON(canonicalOpenAIChatMessage(rightMsg)) {
			return false
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
	sum := sha256.Sum256([]byte(utils.CanonicalJSON(stableParts)))
	return "resp-chat-" + hex.EncodeToString(sum[:])[:24]
}

func convertResponsesRequestWithStructConverter(serviceType string, sess *types.Session, req *types.ResponsesRequest, upstream *config.UpstreamConfig) ([]byte, error) {
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
