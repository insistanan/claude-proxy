package converters

import (
	"encoding/json"

	"github.com/BenedictKing/claude-proxy/internal/session"
	"github.com/BenedictKing/claude-proxy/internal/types"
)

// ============== Responses 透传转换器 ==============

// ResponsesPassthroughConverter 实现 Responses → Responses 透传
// 用于上游服务本身就是 Responses API 的场景
type ResponsesPassthroughConverter struct{}

// ToProviderRequest 透传 Responses 请求（不做转换）
func (c *ResponsesPassthroughConverter) ToProviderRequest(sess *session.Session, req *types.ResponsesRequest) (interface{}, error) {
	// 直接返回原始请求，保留所有字段
	result := map[string]interface{}{
		"model":                req.Model,
		"instructions":         req.Instructions,
		"input":                req.Input,
		"previous_response_id": req.PreviousResponseID,
		"store":                req.Store,
		"max_tokens":           req.MaxTokens,
		"max_output_tokens":    req.MaxOutputTokens,
		"temperature":          req.Temperature,
		"top_p":                req.TopP,
		"frequency_penalty":    req.FrequencyPenalty,
		"presence_penalty":     req.PresencePenalty,
		"stream":               req.Stream,
		"stop":                 req.Stop,
		"user":                 req.User,
		"stream_options":       req.StreamOptions,
		"tools":                req.Tools,
		"tool_choice":          req.ToolChoice,
		"parallel_tool_calls":  req.ParallelToolCalls,
		"reasoning":            req.Reasoning,
		"metadata":             req.Metadata,
	}

	// 关键：透传 prompt_cache_key 和 prompt_cache_retention
	// 这两个字段是 OpenAI Prompt Caching 的核心路由键
	// 如果丢失，会导致缓存完全失效（即使走透传也无法命中缓存）
	if req.PromptCacheKey != "" {
		result["prompt_cache_key"] = req.PromptCacheKey
	}
	if req.PromptCacheRetention != "" {
		result["prompt_cache_retention"] = req.PromptCacheRetention
	}

	return result, nil
}

// FromProviderResponse 透传 Responses 响应（不做转换）
func (c *ResponsesPassthroughConverter) FromProviderResponse(resp map[string]interface{}, sessionID string) (*types.ResponsesResponse, error) {
	// 直接解析为 ResponsesResponse
	// 注意：这里假设上游返回的就是标准 Responses 格式
	id, _ := resp["id"].(string)
	object, _ := resp["object"].(string)
	model, _ := resp["model"].(string)
	status, _ := resp["status"].(string)
	previousID, _ := resp["previous_id"].(string)
	previousResponseID, _ := resp["previous_response_id"].(string)
	created := int64FromInterface(resp["created"])
	createdAt := int64FromInterface(resp["created_at"])

	// 解析 output
	output := []types.ResponsesItem{}
	if outputArr, ok := resp["output"].([]interface{}); ok {
		for _, item := range outputArr {
			if itemMap, ok := item.(map[string]interface{}); ok {
				itemID, _ := itemMap["id"].(string)
				itemType, _ := itemMap["type"].(string)
				itemStatus, _ := itemMap["status"].(string)
				role, _ := itemMap["role"].(string)
				content := itemMap["content"]
				summary := itemMap["summary"]
				callID, _ := itemMap["call_id"].(string)
				name, _ := itemMap["name"].(string)
				arguments, _ := itemMap["arguments"].(string)

				output = append(output, types.ResponsesItem{
					ID:        itemID,
					Type:      itemType,
					Status:    itemStatus,
					Role:      role,
					Content:   content,
					Summary:   summary,
					CallID:    callID,
					Name:      name,
					Arguments: arguments,
				})
			}
		}
	}

	// 解析 usage（使用统一入口自动检测格式：Claude/Gemini/OpenAI）
	usage := ExtractUsageMetrics(resp["usage"])

	return &types.ResponsesResponse{
		ID:                  id,
		Object:              object,
		Model:               model,
		Output:              output,
		Status:              status,
		PreviousID:          previousID,
		PreviousResponseID:  previousResponseID,
		Usage:               usage,
		Created:             created,
		CreatedAt:           createdAt,
		Extra:               resp,
	}, nil
}

func int64FromInterface(value interface{}) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

// GetProviderName 获取上游服务名称
func (c *ResponsesPassthroughConverter) GetProviderName() string {
	return "Responses API (Passthrough)"
}
