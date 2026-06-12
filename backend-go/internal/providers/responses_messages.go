package providers

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// MessagesResponsesProvider 将 Claude Messages 入口请求转换为 OpenAI Responses 上游协议。
type MessagesResponsesProvider struct{}

type claudeResponsesRequest struct {
	Model                string                 `json:"model"`
	Instructions         string                 `json:"instructions,omitempty"`
	Input                []interface{}          `json:"input"`
	Stream               bool                   `json:"stream"`
	MaxOutputTokens      int                    `json:"max_output_tokens,omitempty"`
	Temperature          float64                `json:"temperature,omitempty"`
	Tools                []interface{}          `json:"tools,omitempty"`
	ToolChoice           interface{}            `json:"tool_choice,omitempty"`
	Reasoning            interface{}            `json:"reasoning,omitempty"`
	Metadata             map[string]interface{} `json:"metadata,omitempty"`
	PromptCacheKey       string                 `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string                 `json:"prompt_cache_retention,omitempty"`
}

func (p *MessagesResponsesProvider) ConvertToProviderRequest(c *gin.Context, upstream *config.UpstreamConfig, apiKey string) (*http.Request, []byte, error) {
	originalBodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("读取请求体失败: %w", err)
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(originalBodyBytes))

	var claudeReq types.ClaudeRequest
	if err := json.Unmarshal(originalBodyBytes, &claudeReq); err != nil {
		return nil, originalBodyBytes, fmt.Errorf("解析Claude请求体失败: %w", err)
	}

	responsesReq, err := claudeRequestToResponsesRequest(&claudeReq, upstream)
	if err != nil {
		return nil, originalBodyBytes, err
	}
	reqBodyBytes, err := utils.MarshalJSONNoEscape(responsesReq)
	if err != nil {
		return nil, originalBodyBytes, fmt.Errorf("序列化Responses请求体失败: %w", err)
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, buildResponsesURL(upstream.GetEffectiveBaseURL()), bytes.NewReader(reqBodyBytes))
	if err != nil {
		return nil, originalBodyBytes, fmt.Errorf("创建Responses请求失败: %w", err)
	}
	req.Header = utils.PrepareUpstreamHeaders(c, req.URL.Host)
	utils.SetAuthenticationHeader(req.Header, apiKey)
	req.Header.Set("Content-Type", "application/json")

	return req, originalBodyBytes, nil
}

func (p *MessagesResponsesProvider) ConvertToClaudeResponse(providerResp *types.ProviderResponse) (*types.ClaudeResponse, error) {
	var responsesResp types.ResponsesResponse
	if err := json.Unmarshal(providerResp.Body, &responsesResp); err != nil {
		return nil, err
	}
	return responsesResponseToClaude(&responsesResp), nil
}

func (p *MessagesResponsesProvider) HandleStreamResponse(body io.ReadCloser) (<-chan string, <-chan error, error) {
	eventChan := make(chan string, 100)
	errChan := make(chan error, 1)

	go func() {
		defer close(eventChan)
		defer body.Close()

		state := newResponsesToClaudeStreamState()
		scanner := bufio.NewScanner(body)
		const maxScannerBufferSize = 1024 * 1024
		scanner.Buffer(make([]byte, 0, 64*1024), maxScannerBufferSize)

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || line == "data: [DONE]" {
				continue
			}
			for _, event := range state.processLine(line) {
				eventChan <- event
			}
		}
		for _, event := range state.finish() {
			eventChan <- event
		}
		if err := scanner.Err(); err != nil {
			errMsg := err.Error()
			if strings.Contains(errMsg, "broken pipe") || strings.Contains(errMsg, "connection reset") || strings.Contains(errMsg, "EOF") {
				return
			}
			errChan <- err
		}
	}()

	return eventChan, errChan, nil
}

func claudeRequestToResponsesRequest(claudeReq *types.ClaudeRequest, upstream *config.UpstreamConfig) (*claudeResponsesRequest, error) {
	model := config.ResolveUpstreamModel(claudeReq.Model, upstream)
	req := &claudeResponsesRequest{
		Model:  model,
		Input:  claudeMessagesToResponsesInput(claudeReq.Messages),
		Stream: claudeReq.Stream,
	}
	if systemText := extractSystemText(claudeReq.System); systemText != "" {
		req.Instructions = systemText
	}
	if claudeReq.MaxCompletionTokens > 0 {
		req.MaxOutputTokens = claudeReq.MaxCompletionTokens
	} else if claudeReq.MaxTokens > 0 {
		req.MaxOutputTokens = claudeReq.MaxTokens
	}
	if claudeReq.Temperature > 0 {
		req.Temperature = claudeReq.Temperature
	}
	if len(claudeReq.Tools) > 0 {
		req.Tools = claudeToolsToResponsesTools(claudeReq.Tools)
	}
	if toolChoice := claudeToolChoiceToResponses(claudeReq.ToolChoice); toolChoice != nil {
		req.ToolChoice = toolChoice
	}
	if reasoning := claudeReasoningToResponsesReasoning(claudeReq); reasoning != nil {
		req.Reasoning = reasoning
	}
	if len(claudeReq.Metadata) > 0 {
		req.Metadata = claudeReq.Metadata
	}
	if supportsOpenAIPromptCaching(upstream) {
		req.PromptCacheKey = buildClaudeResponsesPromptCacheKey(claudeReq, upstream, model)
		req.PromptCacheRetention = "24h"
	}
	return req, nil
}

func supportsOpenAIPromptCaching(upstream *config.UpstreamConfig) bool {
	if upstream == nil {
		return false
	}
	u, err := url.Parse(strings.TrimRight(upstream.GetEffectiveBaseURL(), "#"))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "api.openai.com" || strings.HasSuffix(host, ".api.openai.com")
}

func buildClaudeResponsesPromptCacheKey(claudeReq *types.ClaudeRequest, upstream *config.UpstreamConfig, model string) string {
	channel := ""
	baseURL := ""
	if upstream != nil {
		channel = strings.TrimSpace(upstream.Name)
		baseURL = strings.TrimRight(upstream.GetEffectiveBaseURL(), "/#")
	}
	stableParts := map[string]interface{}{
		"protocol": "claude-messages-to-openai-responses-v1",
		"model":    model,
		"channel":  channel,
		"baseURL":  baseURL,
		"system":   claudeReq.System,
		"tools":    claudeReq.Tools,
	}
	sum := sha256.Sum256([]byte(canonicalJSON(stableParts)))
	return "claude-resp-" + hex.EncodeToString(sum[:])[:24]
}

func canonicalJSON(v interface{}) string {
	switch value := v.(type) {
	case nil:
		return "null"
	case string:
		data, _ := json.Marshal(value)
		return string(data)
	case bool:
		if value {
			return "true"
		}
		return "false"
	case float64, float32, int, int64, int32, uint, uint64, uint32, json.Number:
		data, _ := json.Marshal(value)
		return string(data)
	case []types.ClaudeTool:
		items := make([]interface{}, 0, len(value))
		for _, item := range value {
			items = append(items, item)
		}
		return canonicalJSON(items)
	case []interface{}:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			parts = append(parts, canonicalJSON(item))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]interface{}:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			keyJSON, _ := json.Marshal(key)
			parts = append(parts, string(keyJSON)+":"+canonicalJSON(value[key]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprint(value)
		}
		var normalized interface{}
		if err := json.Unmarshal(data, &normalized); err != nil {
			return string(data)
		}
		return canonicalJSON(normalized)
	}
}

func buildResponsesURL(baseURL string) string {
	skipVersionPrefix := strings.HasSuffix(baseURL, "#")
	if skipVersionPrefix {
		baseURL = strings.TrimSuffix(baseURL, "#")
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	endpoint := "/responses"
	versionPattern := regexp.MustCompile(`/v\d+[a-z]*$`)
	if !skipVersionPrefix && !versionPattern.MatchString(baseURL) {
		endpoint = "/v1" + endpoint
	}
	return baseURL + endpoint
}

func claudeMessagesToResponsesInput(messages []types.ClaudeMessage) []interface{} {
	items := make([]interface{}, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "assistant":
			items = append(items, claudeAssistantMessageToResponsesItems(msg)...)
		case "tool":
			items = append(items, claudeToolMessageToResponsesItems(msg)...)
		default:
			items = append(items, claudeUserMessageToResponsesItems(msg)...)
		}
	}
	return items
}

func claudeUserMessageToResponsesItems(msg types.ClaudeMessage) []interface{} {
	blocks := utils.NormalizeContentBlocks(msg.Content)
	if len(blocks) == 0 {
		if text, ok := msg.Content.(string); ok && text != "" {
			return []interface{}{map[string]interface{}{
				"type":    "message",
				"role":    "user",
				"content": []interface{}{map[string]interface{}{"type": "input_text", "text": text}},
			}}
		}
		return nil
	}

	var textBlocks []interface{}
	var items []interface{}
	for _, block := range blocks {
		blockType, _ := block["type"].(string)
		if blockType == "tool_result" {
			if len(textBlocks) > 0 {
				items = append(items, map[string]interface{}{
					"type":    "message",
					"role":    "user",
					"content": textBlocks,
				})
				textBlocks = nil
			}
			content := block["content"]
			items = append(items, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": block["tool_use_id"],
				"output":  responsesToolOutput(content),
			})
			continue
		}
		if text, ok := utils.ExtractTextFromBlock(block); ok {
			textBlocks = append(textBlocks, map[string]interface{}{"type": "input_text", "text": text})
			continue
		}
		if imageBlock, ok := toResponsesImageContentBlock(block); ok {
			textBlocks = append(textBlocks, imageBlock)
		}
	}
	if len(textBlocks) > 0 {
		items = append(items, map[string]interface{}{
			"type":    "message",
			"role":    "user",
			"content": textBlocks,
		})
	}
	return items
}

func claudeAssistantMessageToResponsesItems(msg types.ClaudeMessage) []interface{} {
	blocks := utils.NormalizeContentBlocks(msg.Content)
	if len(blocks) == 0 {
		if text, ok := msg.Content.(string); ok && text != "" {
			return []interface{}{map[string]interface{}{
				"type":    "message",
				"role":    "assistant",
				"content": []interface{}{map[string]interface{}{"type": "output_text", "text": text}},
			}}
		}
		return nil
	}

	var textBlocks []interface{}
	var items []interface{}
	for _, block := range blocks {
		blockType, _ := block["type"].(string)
		switch blockType {
		case "text":
			if text, _ := block["text"].(string); text != "" {
				textBlocks = append(textBlocks, map[string]interface{}{"type": "output_text", "text": text})
			}
		case "thinking":
			if text, _ := block["thinking"].(string); text != "" {
				items = append(items, map[string]interface{}{
					"type": "reasoning",
					"summary": []interface{}{map[string]interface{}{
						"type": "summary_text",
						"text": text,
					}},
				})
			}
		case "tool_use":
			id, _ := block["id"].(string)
			name, _ := block["name"].(string)
			argsBytes, _ := json.Marshal(block["input"])
			if len(argsBytes) == 0 || string(argsBytes) == "null" {
				argsBytes = []byte("{}")
			}
			items = append(items, map[string]interface{}{
				"type":      "function_call",
				"call_id":   id,
				"name":      name,
				"arguments": string(argsBytes),
			})
		}
	}
	if len(textBlocks) > 0 {
		items = append([]interface{}{map[string]interface{}{
			"type":    "message",
			"role":    "assistant",
			"content": textBlocks,
		}}, items...)
	}
	return items
}

func claudeToolMessageToResponsesItems(msg types.ClaudeMessage) []interface{} {
	blocks := utils.NormalizeContentBlocks(msg.Content)
	items := make([]interface{}, 0, len(blocks))
	for _, block := range blocks {
		if blockType, _ := block["type"].(string); blockType != "tool_result" {
			continue
		}
		content := block["content"]
		items = append(items, map[string]interface{}{
			"type":    "function_call_output",
			"call_id": block["tool_use_id"],
			"output":  responsesToolOutput(content),
		})
	}
	return items
}

func claudeToolsToResponsesTools(tools []types.ClaudeTool) []interface{} {
	out := make([]interface{}, 0, len(tools))
	for _, tool := range tools {
		if tool.Name == "" || tool.Name == "BatchTool" {
			continue
		}
		out = append(out, map[string]interface{}{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  cleanJsonSchema(tool.InputSchema),
		})
	}
	return out
}

func responsesToolOutput(content interface{}) string {
	if content == nil {
		return ""
	}
	if str, ok := content.(string); ok {
		return str
	}
	if blocks := utils.NormalizeContentBlocks(content); len(blocks) > 0 {
		texts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if text, ok := utils.ExtractTextFromBlock(block); ok {
				texts = append(texts, text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}
	contentJSON, err := utils.MarshalJSONNoEscape(content)
	if err != nil {
		return fmt.Sprint(content)
	}
	return string(contentJSON)
}

func toResponsesImageContentBlock(block map[string]interface{}) (map[string]interface{}, bool) {
	imageBlock, ok := utils.ToOpenAIImageContentBlock(block)
	if !ok {
		return nil, false
	}
	imageURL, ok := imageBlock["image_url"].(map[string]interface{})
	if !ok {
		return nil, false
	}
	url, _ := imageURL["url"].(string)
	if url == "" {
		return nil, false
	}
	return map[string]interface{}{
		"type":      "input_image",
		"image_url": url,
	}, true
}

func claudeToolChoiceToResponses(raw interface{}) interface{} {
	if raw == nil {
		return nil
	}
	if value, ok := raw.(string); ok {
		switch value {
		case "auto", "none":
			return value
		case "any":
			return "required"
		default:
			return nil
		}
	}
	if obj, ok := raw.(map[string]interface{}); ok {
		typ, _ := obj["type"].(string)
		switch typ {
		case "auto", "none":
			return typ
		case "any":
			return "required"
		case "tool":
			return map[string]interface{}{"type": "function", "name": obj["name"]}
		}
	}
	return nil
}

func claudeReasoningToResponsesReasoning(claudeReq *types.ClaudeRequest) interface{} {
	if effort := resolveClaudeReasoningEffort(claudeReq); effort != "" {
		return map[string]interface{}{"effort": effort}
	}
	return nil
}

func resolveClaudeReasoningEffort(claudeReq *types.ClaudeRequest) string {
	if claudeReq == nil {
		return ""
	}
	if claudeReq.OutputConfig != nil {
		if effort, _ := claudeReq.OutputConfig["effort"].(string); effort != "" {
			switch strings.ToLower(effort) {
			case "low", "medium", "high", "xhigh":
				return strings.ToLower(effort)
			case "max":
				return "xhigh"
			default:
				return ""
			}
		}
	}

	raw := claudeReq.Thinking
	obj, ok := raw.(map[string]interface{})
	if !ok {
		return ""
	}
	typ, _ := obj["type"].(string)
	if typ == "" || typ == "disabled" {
		return ""
	}
	switch typ {
	case "adaptive":
		return "xhigh"
	case "enabled":
		budget, ok := numericBudgetTokens(obj["budget_tokens"])
		if !ok {
			return "high"
		}
		switch {
		case budget < 4000:
			return "low"
		case budget < 16000:
			return "medium"
		default:
			return "high"
		}
	default:
		return ""
	}
}

func numericBudgetTokens(raw interface{}) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	default:
		return 0, false
	}
}

func responsesResponseToClaude(resp *types.ResponsesResponse) *types.ClaudeResponse {
	claudeResp := &types.ClaudeResponse{
		ID:      generateID(),
		Type:    "message",
		Role:    "assistant",
		Content: []types.ClaudeContent{},
	}
	var textParts []string
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, block := range utils.NormalizeContentBlocks(item.Content) {
				if text, ok := utils.ExtractTextFromBlock(block); ok && text != "" {
					textParts = append(textParts, text)
				}
			}
		case "function_call", "custom_tool_call":
			flushClaudeText(claudeResp, &textParts)
			var input interface{} = map[string]interface{}{}
			if item.Type == "custom_tool_call" {
				input = map[string]interface{}{"input": responsesToolOutput(item.Content)}
			} else if item.Arguments != "" {
				_ = json.Unmarshal([]byte(item.Arguments), &input)
			}
			callID := item.CallID
			if callID == "" {
				callID = strings.TrimPrefix(item.ID, "fc_")
				if callID == item.ID {
					callID = strings.TrimPrefix(item.ID, "ctc_")
				}
			}
			claudeResp.Content = append(claudeResp.Content, types.ClaudeContent{
				Type:  "tool_use",
				ID:    callID,
				Name:  item.Name,
				Input: input,
			})
		}
	}
	flushClaudeText(claudeResp, &textParts)

	claudeResp.StopReason = "end_turn"
	for _, content := range claudeResp.Content {
		if content.Type == "tool_use" {
			claudeResp.StopReason = "tool_use"
			break
		}
	}
	if resp.Status == "incomplete" {
		claudeResp.StopReason = "max_tokens"
	}
	claudeResp.Usage = &types.Usage{
		InputTokens:                resp.Usage.InputTokens,
		OutputTokens:               resp.Usage.OutputTokens,
		CacheCreationInputTokens:   resp.Usage.CacheCreationInputTokens,
		CacheCreation5mInputTokens: resp.Usage.CacheCreation5mInputTokens,
		CacheCreation1hInputTokens: resp.Usage.CacheCreation1hInputTokens,
		CacheReadInputTokens:       resp.Usage.CacheReadInputTokens,
		CacheTTL:                   resp.Usage.CacheTTL,
	}
	return claudeResp
}

func flushClaudeText(resp *types.ClaudeResponse, parts *[]string) {
	if len(*parts) == 0 {
		return
	}
	resp.Content = append(resp.Content, types.ClaudeContent{
		Type: "text",
		Text: strings.Join(*parts, ""),
	})
	*parts = nil
}

type responsesToClaudeStreamState struct {
	responseID       string
	messageStarted   bool
	nextBlockIndex   int
	textBlockIndex   int
	textBlockOpen    bool
	reasonBlockIndex int
	reasonBlockOpen  bool
	stopReason       string
	model            string
	toolCalls        map[string]*responsesStreamToolCall
	emittedToolCalls map[string]bool
}

type responsesStreamToolCall struct {
	CallID    string
	Name      string
	Arguments string
}

func newResponsesToClaudeStreamState() *responsesToClaudeStreamState {
	return &responsesToClaudeStreamState{
		responseID:       fmt.Sprintf("msg_%s", uuid.New().String()),
		toolCalls:        make(map[string]*responsesStreamToolCall),
		emittedToolCalls: make(map[string]bool),
	}
}

func (s *responsesToClaudeStreamState) processLine(line string) []string {
	if strings.HasPrefix(line, "event:") {
		return nil
	}
	if !strings.HasPrefix(line, "data:") {
		return nil
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" {
		return nil
	}
	root := gjson.Parse(data)
	if root.Get("error").Exists() {
		return []string{buildClaudeSSE("error", map[string]interface{}{
			"type":  "error",
			"error": root.Get("error").Value(),
		})}
	}

	eventType := root.Get("type").String()
	out := s.ensureMessageStart(root)
	switch {
	case strings.Contains(eventType, "output_text.delta"):
		out = append(out, s.emitTextDelta(root.Get("delta").String())...)
	case strings.Contains(eventType, "reasoning") && strings.Contains(eventType, "delta"):
		out = append(out, s.emitReasoningDelta(root.Get("delta").String())...)
	case strings.Contains(eventType, "response.output_item.added"):
		s.captureToolCall(root)
	case strings.Contains(eventType, "response.output_item.done"):
		s.captureToolCall(root)
		itemType := root.Get("item.type").String()
		if itemType == "function_call" || itemType == "custom_tool_call" {
			out = append(out, s.emitToolUse(root)...)
		}
	case strings.Contains(eventType, "function_call_arguments.done"):
		s.captureToolCall(root)
		out = append(out, s.emitToolUse(root)...)
	case strings.Contains(eventType, "response.completed"):
		if s.stopReason == "" {
			s.stopReason = "end_turn"
		}
		if status := root.Get("response.status").String(); status == "incomplete" {
			s.stopReason = "max_tokens"
		}
		out = append(out, s.emitMessageDelta()...)
	default:
		if text := root.Get("delta").String(); text != "" && strings.Contains(eventType, "text") {
			out = append(out, s.emitTextDelta(text)...)
		}
	}
	return out
}

func (s *responsesToClaudeStreamState) finish() []string {
	out := []string{}
	out = append(out, s.closeReasoningBlock()...)
	out = append(out, s.closeTextBlock()...)
	if s.messageStarted {
		if s.stopReason == "" {
			s.stopReason = "end_turn"
			out = append(out, s.emitMessageDelta()...)
		}
		out = append(out, buildClaudeSSE("message_stop", map[string]interface{}{"type": "message_stop"}))
	}
	return out
}

func (s *responsesToClaudeStreamState) ensureMessageStart(root gjson.Result) []string {
	if s.messageStarted {
		return nil
	}
	if model := root.Get("response.model").String(); model != "" {
		s.model = model
	} else if model := root.Get("model").String(); model != "" {
		s.model = model
	}
	s.messageStarted = true
	return []string{buildClaudeSSE("message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            s.responseID,
			"type":          "message",
			"role":          "assistant",
			"content":       []interface{}{},
			"model":         s.model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]interface{}{"input_tokens": 0, "output_tokens": 1},
		},
	})}
}

func (s *responsesToClaudeStreamState) emitTextDelta(text string) []string {
	if text == "" {
		return nil
	}
	out := s.closeReasoningBlock()
	if !s.textBlockOpen {
		s.textBlockIndex = s.nextBlockIndex
		s.nextBlockIndex++
		s.textBlockOpen = true
		out = append(out, buildClaudeSSE("content_block_start", map[string]interface{}{
			"type":  "content_block_start",
			"index": s.textBlockIndex,
			"content_block": map[string]interface{}{
				"type": "text",
				"text": "",
			},
		}))
	}
	out = append(out, buildClaudeSSE("content_block_delta", map[string]interface{}{
		"type":  "content_block_delta",
		"index": s.textBlockIndex,
		"delta": map[string]interface{}{"type": "text_delta", "text": text},
	}))
	return out
}

func (s *responsesToClaudeStreamState) emitReasoningDelta(text string) []string {
	if text == "" {
		return nil
	}
	out := s.closeTextBlock()
	if !s.reasonBlockOpen {
		s.reasonBlockIndex = s.nextBlockIndex
		s.nextBlockIndex++
		s.reasonBlockOpen = true
		out = append(out, buildClaudeSSE("content_block_start", map[string]interface{}{
			"type":  "content_block_start",
			"index": s.reasonBlockIndex,
			"content_block": map[string]interface{}{
				"type":      "thinking",
				"thinking":  "",
				"signature": "",
			},
		}))
	}
	out = append(out, buildClaudeSSE("content_block_delta", map[string]interface{}{
		"type":  "content_block_delta",
		"index": s.reasonBlockIndex,
		"delta": map[string]interface{}{"type": "thinking_delta", "thinking": text},
	}))
	return out
}

func (s *responsesToClaudeStreamState) emitToolUse(root gjson.Result) []string {
	key := responsesToolCallKey(root)
	if key != "" && s.emittedToolCalls[key] {
		return nil
	}
	out := s.closeReasoningBlock()
	out = append(out, s.closeTextBlock()...)
	stored := s.toolCalls[key]
	callID := root.Get("item.call_id").String()
	if callID == "" {
		callID = root.Get("call_id").String()
	}
	if callID == "" && stored != nil {
		callID = stored.CallID
	}
	name := root.Get("item.name").String()
	if name == "" {
		name = root.Get("name").String()
	}
	if name == "" && stored != nil {
		name = stored.Name
	}
	arguments := root.Get("arguments").String()
	if arguments == "" {
		arguments = root.Get("item.arguments").String()
	}
	if arguments == "" {
		if input := root.Get("item.input").String(); input != "" {
			inputJSON, _ := json.Marshal(map[string]string{"input": input})
			arguments = string(inputJSON)
		}
	}
	if arguments == "" && stored != nil {
		arguments = stored.Arguments
	}
	if callID == "" {
		callID = fmt.Sprintf("call_%d", s.nextBlockIndex)
	}
	var input interface{} = map[string]interface{}{}
	if arguments != "" {
		_ = json.Unmarshal([]byte(arguments), &input)
	}
	index := s.nextBlockIndex
	s.nextBlockIndex++
	s.stopReason = "tool_use"
	if key != "" {
		s.emittedToolCalls[key] = true
	}
	return append(out, processToolUsePart(callID, name, input, index)...)
}

func (s *responsesToClaudeStreamState) captureToolCall(root gjson.Result) {
	key := responsesToolCallKey(root)
	if key == "" {
		return
	}
	itemType := root.Get("item.type").String()
	if itemType != "" && itemType != "function_call" && itemType != "custom_tool_call" {
		return
	}
	call := s.toolCalls[key]
	if call == nil {
		call = &responsesStreamToolCall{}
		s.toolCalls[key] = call
	}
	if callID := root.Get("item.call_id").String(); callID != "" {
		call.CallID = callID
	} else if callID := root.Get("call_id").String(); callID != "" {
		call.CallID = callID
	}
	if name := root.Get("item.name").String(); name != "" {
		call.Name = name
	} else if name := root.Get("name").String(); name != "" {
		call.Name = name
	}
	if arguments := root.Get("item.arguments").String(); arguments != "" {
		call.Arguments = arguments
	} else if arguments := root.Get("arguments").String(); arguments != "" {
		call.Arguments = arguments
	} else if input := root.Get("item.input").String(); input != "" {
		inputJSON, _ := json.Marshal(map[string]string{"input": input})
		call.Arguments = string(inputJSON)
	}
}

func responsesToolCallKey(root gjson.Result) string {
	if itemID := root.Get("item.id").String(); itemID != "" {
		return itemID
	}
	if itemID := root.Get("item_id").String(); itemID != "" {
		return itemID
	}
	if outputIndex := root.Get("output_index"); outputIndex.Exists() {
		return fmt.Sprintf("output_%d", outputIndex.Int())
	}
	if callID := root.Get("item.call_id").String(); callID != "" {
		return callID
	}
	if callID := root.Get("call_id").String(); callID != "" {
		return callID
	}
	return ""
}

func (s *responsesToClaudeStreamState) closeTextBlock() []string {
	if !s.textBlockOpen {
		return nil
	}
	s.textBlockOpen = false
	return []string{buildClaudeSSE("content_block_stop", map[string]interface{}{
		"type":  "content_block_stop",
		"index": s.textBlockIndex,
	})}
}

func (s *responsesToClaudeStreamState) closeReasoningBlock() []string {
	if !s.reasonBlockOpen {
		return nil
	}
	s.reasonBlockOpen = false
	return []string{
		buildClaudeSSE("content_block_delta", map[string]interface{}{
			"type":  "content_block_delta",
			"index": s.reasonBlockIndex,
			"delta": map[string]interface{}{"type": "signature_delta", "signature": ""},
		}),
		buildClaudeSSE("content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": s.reasonBlockIndex,
		}),
	}
}

func (s *responsesToClaudeStreamState) emitMessageDelta() []string {
	if s.stopReason == "" {
		s.stopReason = "end_turn"
	}
	return []string{buildClaudeSSE("message_delta", map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   s.stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{"output_tokens": 0},
	})}
}

func buildClaudeSSE(event string, data map[string]interface{}) string {
	dataJSON, _ := json.Marshal(data)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", event, dataJSON)
}
