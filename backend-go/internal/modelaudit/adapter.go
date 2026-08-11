package modelaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/utils"
)

var versionSuffixPattern = regexp.MustCompile(`/v\d+[a-z]*$`)

type preparedRequest struct {
	Request *http.Request
	Summary RedactedRequestSummary
}

func prepareProtocolRequest(ctx context.Context, request DirectExecutionRequest, executionID string) (preparedRequest, error) {
	baseURLs := request.Target.Upstream.GetAllBaseURLs()
	if len(baseURLs) == 0 || strings.TrimSpace(baseURLs[0]) == "" {
		return preparedRequest{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryTarget, "目标渠道未配置 BaseURL")
	}
	if len(request.Target.Upstream.APIKeys) == 0 || strings.TrimSpace(request.Target.Upstream.APIKeys[0]) == "" {
		return preparedRequest{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryTarget, "目标渠道未配置 API Key")
	}

	body, endpoint, err := buildWirePayload(request.Spec, request.Target.Snapshot)
	if err != nil {
		return preparedRequest{}, err
	}
	requestURL, err := joinProtocolEndpoint(baseURLs[0], endpoint, request.Target.Snapshot.WireProtocol)
	if err != nil {
		return preparedRequest{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return preparedRequest{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "创建上游请求失败", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	applyProtocolHeaders(httpRequest.Header, request.Target.Snapshot, request.Spec, request.Target.Upstream.APIKeys[0], executionID)

	summary, err := NewRedactedRequestSummary(
		request.Spec.Redaction,
		request.Spec.RequestProfile,
		[]byte(requestTextForDigest(request.Spec.Input)),
		httpRequest.Header,
		len(request.Spec.Input.Tools),
		len(request.Spec.Input.Images),
		payloadFieldNames(body),
	)
	if err != nil {
		return preparedRequest{}, err
	}
	return preparedRequest{Request: httpRequest, Summary: summary}, nil
}

func buildWirePayload(spec ExecutionSpec, target TargetSnapshot) ([]byte, string, error) {
	switch target.WireProtocol {
	case ProtocolMessages:
		return buildMessagesPayload(spec, target)
	case ProtocolResponses:
		return buildResponsesPayload(spec, target)
	case ProtocolChat:
		return buildChatPayload(spec, target)
	case ProtocolGemini:
		return buildGeminiPayload(spec, target)
	case ProtocolImages:
		return buildImagesPayload(spec, target)
	default:
		return nil, "", contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, fmt.Sprintf("不支持实际线协议 %q", target.WireProtocol))
	}
}

func buildMessagesPayload(spec ExecutionSpec, target TargetSnapshot) ([]byte, string, error) {
	payload := map[string]interface{}{
		"model":      target.ResolvedModel,
		"max_tokens": spec.MaxOutputTokens,
		"messages":   normalizedMessages(spec.Input),
		"stream":     spec.Stream,
	}
	if strings.TrimSpace(spec.Input.System) != "" {
		payload["system"] = spec.Input.System
	}
	if target.ThinkingMap != nil {
		thinking := map[string]interface{}{"type": target.ThinkingMap.Mode}
		if target.ThinkingMap.BudgetTokens != nil {
			thinking["budget_tokens"] = *target.ThinkingMap.BudgetTokens
		}
		payload["thinking"] = thinking
	}
	if len(spec.Input.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(spec.Input.Tools))
		for _, tool := range spec.Input.Tools {
			item := map[string]interface{}{"name": tool.Name, "description": tool.Description}
			if len(tool.InputSchema) > 0 {
				item["input_schema"] = json.RawMessage(tool.InputSchema)
			}
			tools = append(tools, item)
		}
		payload["tools"] = tools
	}
	body, err := json.Marshal(payload)
	return body, "/messages", err
}

func buildResponsesPayload(spec ExecutionSpec, target TargetSnapshot) ([]byte, string, error) {
	input := interface{}(spec.Input.Prompt)
	if len(spec.Input.Messages) > 0 {
		input = normalizedMessages(spec.Input)
	}
	payload := map[string]interface{}{
		"model":             target.ResolvedModel,
		"input":             input,
		"stream":            spec.Stream,
		"max_output_tokens": spec.MaxOutputTokens,
	}
	if strings.TrimSpace(spec.Input.System) != "" {
		payload["instructions"] = spec.Input.System
	}
	if target.ThinkingMap != nil {
		payload["reasoning"] = map[string]interface{}{"effort": target.ThinkingMap.Effort}
	}
	if len(spec.Input.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(spec.Input.Tools))
		for _, tool := range spec.Input.Tools {
			item := map[string]interface{}{
				"type": "function", "name": tool.Name, "description": tool.Description,
			}
			if len(tool.InputSchema) > 0 {
				item["parameters"] = json.RawMessage(tool.InputSchema)
			}
			tools = append(tools, item)
		}
		payload["tools"] = tools
	}
	if spec.Conversation.PreviousResponseID != "" {
		payload["previous_response_id"] = spec.Conversation.PreviousResponseID
	}
	if spec.Conversation.Store != nil {
		payload["store"] = *spec.Conversation.Store
	}
	if err := mergeProtocolOptions(payload, spec.Input.ProtocolOptions); err != nil {
		return nil, "", err
	}
	body, err := json.Marshal(payload)
	return body, "/responses", err
}

func buildChatPayload(spec ExecutionSpec, target TargetSnapshot) ([]byte, string, error) {
	messages := normalizedMessages(spec.Input)
	if strings.TrimSpace(spec.Input.System) != "" {
		messages = append([]map[string]interface{}{{"role": "system", "content": spec.Input.System}}, messages...)
	}
	payload := map[string]interface{}{
		"model":                 target.ResolvedModel,
		"messages":              messages,
		"stream":                spec.Stream,
		"max_completion_tokens": spec.MaxOutputTokens,
	}
	if spec.Stream {
		payload["stream_options"] = map[string]interface{}{"include_usage": true}
	}
	if target.ThinkingMap != nil {
		payload["reasoning_effort"] = target.ThinkingMap.Effort
	}
	if len(spec.Input.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(spec.Input.Tools))
		for _, tool := range spec.Input.Tools {
			function := map[string]interface{}{"name": tool.Name, "description": tool.Description}
			if len(tool.InputSchema) > 0 {
				function["parameters"] = json.RawMessage(tool.InputSchema)
			}
			tools = append(tools, map[string]interface{}{"type": "function", "function": function})
		}
		payload["tools"] = tools
	}
	if err := mergeProtocolOptions(payload, spec.Input.ProtocolOptions); err != nil {
		return nil, "", err
	}
	body, err := json.Marshal(payload)
	return body, "/chat/completions", err
}

func buildGeminiPayload(spec ExecutionSpec, target TargetSnapshot) ([]byte, string, error) {
	contents := make([]map[string]interface{}, 0, len(spec.Input.Messages)+1)
	for _, message := range spec.Input.Messages {
		role := message.Role
		if role == "assistant" {
			role = "model"
		}
		contents = append(contents, map[string]interface{}{
			"role": role, "parts": []map[string]interface{}{{"text": message.Content}},
		})
	}
	if len(contents) == 0 {
		contents = append(contents, map[string]interface{}{
			"role": "user", "parts": []map[string]interface{}{{"text": spec.Input.Prompt}},
		})
	}
	generationConfig := map[string]interface{}{"maxOutputTokens": spec.MaxOutputTokens}
	if target.ThinkingMap != nil && target.ThinkingMap.BudgetTokens != nil {
		generationConfig["thinkingConfig"] = map[string]interface{}{"thinkingBudget": *target.ThinkingMap.BudgetTokens}
	}
	payload := map[string]interface{}{"contents": contents, "generationConfig": generationConfig}
	if strings.TrimSpace(spec.Input.System) != "" {
		payload["systemInstruction"] = map[string]interface{}{"parts": []map[string]interface{}{{"text": spec.Input.System}}}
	}
	if len(spec.Input.Tools) > 0 {
		declarations := make([]map[string]interface{}, 0, len(spec.Input.Tools))
		for _, tool := range spec.Input.Tools {
			item := map[string]interface{}{"name": tool.Name, "description": tool.Description}
			if len(tool.InputSchema) > 0 {
				item["parameters"] = json.RawMessage(tool.InputSchema)
			}
			declarations = append(declarations, item)
		}
		payload["tools"] = []map[string]interface{}{{"functionDeclarations": declarations}}
	}
	if err := mergeProtocolOptions(payload, spec.Input.ProtocolOptions); err != nil {
		return nil, "", err
	}
	action := "generateContent"
	if spec.Stream {
		action = "streamGenerateContent?alt=sse"
	}
	model := strings.TrimPrefix(target.ResolvedModel, "models/")
	body, err := json.Marshal(payload)
	return body, "/models/" + model + ":" + action, err
}

func buildImagesPayload(spec ExecutionSpec, target TargetSnapshot) ([]byte, string, error) {
	if spec.RequestProfile != "images.generation.v1" {
		return nil, "", contractError(
			ErrorCodeUnsupported,
			ErrorCategoryUnsupported,
			"Images edit/variation 需要受控图片上传解析器，当前执行器只接受 generation 轮廓",
		)
	}
	if len(spec.Input.Images) > 0 {
		return nil, "", contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "Images generation 不接受源图片")
	}
	payload := map[string]interface{}{
		"model":  target.ResolvedModel,
		"prompt": spec.Input.Prompt,
		"n":      1,
	}
	if err := mergeProtocolOptions(payload, spec.Input.ProtocolOptions); err != nil {
		return nil, "", err
	}
	body, err := json.Marshal(payload)
	return body, "/images/generations", err
}

func normalizedMessages(input ExecutionInput) []map[string]interface{} {
	messages := make([]map[string]interface{}, 0, len(input.Messages)+1)
	for _, message := range input.Messages {
		messages = append(messages, map[string]interface{}{"role": message.Role, "content": message.Content})
	}
	if len(messages) == 0 {
		messages = append(messages, map[string]interface{}{"role": "user", "content": input.Prompt})
	}
	return messages
}

func mergeProtocolOptions(payload map[string]interface{}, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var options map[string]interface{}
	if err := json.Unmarshal(raw, &options); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "protocolOptions 必须是 JSON 对象", err)
	}
	for key, value := range options {
		if _, protected := payload[key]; protected {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("protocolOptions 不允许覆盖受保护字段 %q", key))
		}
		payload[key] = value
	}
	return nil
}

func joinProtocolEndpoint(baseURL string, endpoint string, protocol Protocol) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryTarget, "BaseURL 不能为空")
	}
	skipVersion := strings.HasSuffix(baseURL, "#")
	baseURL = strings.TrimSuffix(strings.TrimSuffix(baseURL, "#"), "/")
	if !skipVersion && !versionSuffixPattern.MatchString(baseURL) {
		versionPrefix := "/v1"
		if protocol == ProtocolGemini {
			versionPrefix = "/v1beta"
		}
		endpoint = versionPrefix + endpoint
	}
	return baseURL + endpoint, nil
}

func applyProtocolHeaders(headers http.Header, target TargetSnapshot, spec ExecutionSpec, apiKey string, executionID string) {
	if target.WireProtocol == ProtocolGemini {
		utils.SetGeminiAuthenticationHeader(headers, apiKey)
	} else {
		utils.SetAuthenticationHeader(headers, apiKey)
	}
	switch target.WireProtocol {
	case ProtocolMessages:
		headers.Set("Anthropic-Version", "2023-06-01")
	case ProtocolResponses:
		if spec.RequestProfile == "responses.codex_compatible.v1" || spec.RequestProfile == "responses.native_codex.v1" {
			headers.Set("X-Codex-Installation-Id", executionID)
			headers.Set("X-Codex-Window-Id", executionID+":0")
			headers.Set("X-Request-Id", executionID)
			headers.Set("X-Codex-Turn-Metadata", fmt.Sprintf(`{"request_kind":"turn","session_id":%q}`, executionID))
			headers.Set("User-Agent", "codex-cli")
		}
	}
}

func requestTextForDigest(input ExecutionInput) string {
	parts := make([]string, 0, len(input.Messages)+2)
	if input.System != "" {
		parts = append(parts, input.System)
	}
	if input.Prompt != "" {
		parts = append(parts, input.Prompt)
	}
	for _, message := range input.Messages {
		parts = append(parts, message.Role+":"+message.Content)
	}
	return strings.Join(parts, "\n")
}

func payloadFieldNames(body []byte) []string {
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	fields := make([]string, 0, len(payload))
	for field := range payload {
		fields = append(fields, field)
	}
	return fields
}
