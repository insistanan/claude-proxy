package converters

import (
	"context"
	"encoding/json"
	"fmt"

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

// ConvertResponsesRequestToUpstream 将 Responses 入口请求转换为指定上游协议。
func ConvertResponsesRequestToUpstream(serviceType string, model string, bodyBytes []byte, stream bool, sess *session.Session, req *types.ResponsesRequest) ([]byte, error) {
	switch serviceType {
	case ResponsesUpstreamResponses:
		return convertResponsesPassthroughRequest(model, bodyBytes)
	case ResponsesUpstreamOpenAI:
		return convertResponsesRequestToOpenAIChat(model, bodyBytes, stream, sess, req)
	case ResponsesUpstreamClaude:
		return convertResponsesRequestWithStructConverter(serviceType, sess, req)
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

func convertResponsesRequestToOpenAIChat(model string, bodyBytes []byte, stream bool, sess *session.Session, req *types.ResponsesRequest) ([]byte, error) {
	if err := ValidateResponsesToOpenAIChatRequest(bodyBytes); err != nil {
		return nil, err
	}

	base := ConvertResponsesToOpenAIChatRequest(model, bodyBytes, stream)

	if sess == nil || req == nil || len(sess.Messages) == 0 {
		return base, nil
	}

	var chatReq map[string]interface{}
	if err := json.Unmarshal(base, &chatReq); err != nil {
		return nil, fmt.Errorf("解析 OpenAI Chat 转换结果失败: %w", err)
	}

	currentMessages, ok := chatReq["messages"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("OpenAI Chat 转换结果缺少 messages")
	}

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

	historyMessages := buildOpenAIHistoryMessages(sess)
	if len(historyMessages) == 0 {
		if _, hasTools := chatReq["tools"]; !hasTools {
			delete(chatReq, "tool_choice")
			delete(chatReq, "parallel_tool_calls")
		}
		out, err := utils.MarshalJSONNoEscape(chatReq)
		if err != nil {
			return nil, fmt.Errorf("序列化 OpenAI Chat 请求失败: %w", err)
		}
		return out, nil
	}

	chatReq["messages"] = mergeOpenAIHistoryMessages(historyMessages, currentMessages)
	if _, hasTools := chatReq["tools"]; !hasTools {
		delete(chatReq, "tool_choice")
		delete(chatReq, "parallel_tool_calls")
	}
	out, err := utils.MarshalJSONNoEscape(chatReq)
	if err != nil {
		return nil, fmt.Errorf("序列化 OpenAI Chat 请求失败: %w", err)
	}
	return out, nil
}

func buildOpenAIHistoryMessages(sess *session.Session) []interface{} {
	if sess == nil || len(sess.Messages) == 0 {
		return nil
	}

	messages := make([]interface{}, 0, len(sess.Messages))
	for _, item := range sess.Messages {
		msg := responsesItemToOpenAIMessage(item)
		if msg != nil {
			messages = append(messages, msg)
		}
	}
	return messages
}

func mergeOpenAIHistoryMessages(historyMessages []interface{}, currentMessages []interface{}) []interface{} {
	merged := make([]interface{}, 0, len(historyMessages)+len(currentMessages))

	if len(currentMessages) > 0 {
		if first, ok := currentMessages[0].(map[string]interface{}); ok && first["role"] == "system" {
			merged = append(merged, first)
			currentMessages = currentMessages[1:]
		}
	}

	merged = append(merged, historyMessages...)
	merged = append(merged, currentMessages...)
	return merged
}

func convertResponsesRequestWithStructConverter(serviceType string, sess *session.Session, req *types.ResponsesRequest) ([]byte, error) {
	converter, err := NewConverterStrict(serviceType)
	if err != nil {
		return nil, err
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
