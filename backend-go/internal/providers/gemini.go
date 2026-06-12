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
	"regexp"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// GeminiProvider Gemini 提供商
type GeminiProvider struct {
	shadowProviderID            string
	shadowSessionID             string
	shadowSnapshot              GeminiShadowSnapshot
	stripThoughtSignature       bool
	injectDummyThoughtSignature bool
}

// ConvertToProviderRequest 转换为 Gemini 请求
func (p *GeminiProvider) ConvertToProviderRequest(c *gin.Context, upstream *config.UpstreamConfig, apiKey string) (*http.Request, []byte, error) {
	// 读取和解析原始请求体
	originalBodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("读取请求体失败: %w", err)
	}
	// 恢复请求体，以便gin context可以被其他地方再次读取（尽管这里我们已经完全处理了）
	c.Request.Body = io.NopCloser(bytes.NewReader(originalBodyBytes))

	var claudeReq types.ClaudeRequest
	if err := json.Unmarshal(originalBodyBytes, &claudeReq); err != nil {
		return nil, originalBodyBytes, fmt.Errorf("解析Claude请求体失败: %w", err)
	}

	p.shadowProviderID = buildGeminiShadowProviderID(upstream)
	p.shadowSessionID = buildGeminiShadowSessionID(c, &claudeReq, originalBodyBytes)
	p.shadowSnapshot = defaultGeminiShadowStore.Get(p.shadowProviderID, p.shadowSessionID)
	p.stripThoughtSignature = false
	p.injectDummyThoughtSignature = false
	if upstream != nil {
		p.stripThoughtSignature = upstream.StripThoughtSignature
		p.injectDummyThoughtSignature = upstream.InjectDummyThoughtSignature
	}

	geminiReq, err := p.convertToGeminiRequest(&claudeReq, upstream)
	if err != nil {
		return nil, originalBodyBytes, err
	}

	reqBodyBytes, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, originalBodyBytes, fmt.Errorf("序列化Gemini请求体失败: %w", err)
	}

	model := config.ResolveUpstreamModel(claudeReq.Model, upstream)
	baseURL := ""
	if upstream != nil {
		baseURL = upstream.GetEffectiveBaseURL()
	}
	url := buildGeminiGenerateContentURL(baseURL, model, claudeReq.Stream)

	req, err := http.NewRequestWithContext(c.Request.Context(), "POST", url, bytes.NewReader(reqBodyBytes))
	if err != nil {
		return nil, originalBodyBytes, fmt.Errorf("创建Gemini请求失败: %w", err)
	}

	// 使用统一的头部处理逻辑（透明代理）
	// 保留客户端的大部分 headers，只移除/替换必要的认证和代理相关 headers
	req.Header = utils.PrepareUpstreamHeaders(c, req.URL.Host)
	
	// 删除演练台模拟的客户端请求头（避免上游严格验证报错）
	req.Header.Del("X-Codex-Window-Id")
	req.Header.Del("X-Codex-Installation-Id")
	req.Header.Del("X-Request-Id")
	req.Header.Del("X-Codex-Turn-Metadata")
	req.Header.Del("X-Claude-Code-Session-Id")
	req.Header.Del("X-Stainless-Lang")
	req.Header.Del("X-Stainless-Runtime")
	req.Header.Del("X-Stainless-Runtime-Version")
	req.Header.Del("X-Stainless-Os")
	req.Header.Del("X-Stainless-Arch")
	req.Header.Del("X-Stainless-Package-Version")
	req.Header.Del("X-Stainless-Retry-Count")
	req.Header.Del("X-Stainless-Timeout")
	req.Header.Del("X-App")
	
	utils.SetGeminiAuthenticationHeader(req.Header, apiKey)

	return req, originalBodyBytes, nil
}

func buildGeminiGenerateContentURL(baseURL string, model string, stream bool) string {
	skipVersionPrefix := strings.HasSuffix(baseURL, "#")
	if skipVersionPrefix {
		baseURL = strings.TrimSuffix(baseURL, "#")
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	modelPath := strings.TrimPrefix(strings.TrimSpace(model), "/")
	if !strings.HasPrefix(modelPath, "models/") && !strings.HasPrefix(modelPath, "tunedModels/") {
		modelPath = "models/" + modelPath
	}

	action := "generateContent"
	if stream {
		action = "streamGenerateContent?alt=sse"
	}

	endpoint := fmt.Sprintf("/%s:%s", modelPath, action)
	versionPattern := regexp.MustCompile(`/v\d+[a-z]*$`)
	if !skipVersionPrefix && !versionPattern.MatchString(baseURL) {
		endpoint = "/v1beta" + endpoint
	}
	return baseURL + endpoint
}

func buildGeminiShadowProviderID(upstream *config.UpstreamConfig) string {
	if upstream == nil {
		return "gemini"
	}
	parts := []string{"gemini"}
	if upstream.Name != "" {
		parts = append(parts, upstream.Name)
	}
	if baseURL := strings.TrimRight(upstream.GetEffectiveBaseURL(), "/#"); baseURL != "" {
		parts = append(parts, baseURL)
	}
	return strings.Join(parts, "|")
}

func buildGeminiShadowSessionID(c *gin.Context, claudeReq *types.ClaudeRequest, originalBody []byte) string {
	if claudeReq != nil && len(claudeReq.Metadata) > 0 {
		if sessionID := extractGeminiSessionIDFromMetadata(claudeReq.Metadata); sessionID != "" {
			return sessionID
		}
	}
	if c != nil {
		if sessionID := strings.TrimSpace(c.GetHeader("X-Claude-Code-Session-Id")); sessionID != "" {
			return sessionID
		}
	}
	sum := sha256.Sum256(originalBody)
	return "body_" + hex.EncodeToString(sum[:8])
}

func extractGeminiSessionIDFromMetadata(metadata map[string]interface{}) string {
	raw, ok := metadata["user_id"]
	if !ok {
		return ""
	}
	if rawString, ok := raw.(string); ok {
		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(rawString), &decoded); err == nil {
			if sessionID, _ := decoded["session_id"].(string); strings.TrimSpace(sessionID) != "" {
				return strings.TrimSpace(sessionID)
			}
		}
		return strings.TrimSpace(rawString)
	}
	if rawMap, ok := raw.(map[string]interface{}); ok {
		if sessionID, _ := rawMap["session_id"].(string); strings.TrimSpace(sessionID) != "" {
			return strings.TrimSpace(sessionID)
		}
	}
	return ""
}

// convertToGeminiRequest 转换为 Gemini 请求体
func (p *GeminiProvider) convertToGeminiRequest(claudeReq *types.ClaudeRequest, upstream *config.UpstreamConfig) (map[string]interface{}, error) {
	systemText := buildGeminiSystemInstructionText(claudeReq)
	contents, err := p.convertMessages(claudeReq.Messages, p.shadowSnapshot)
	if err != nil {
		return nil, err
	}
	req := map[string]interface{}{
		"contents": contents,
	}

	// 添加系统指令
	if systemText != "" {
		req["systemInstruction"] = map[string]interface{}{
			"parts": []map[string]string{
				{"text": systemText},
			},
		}
	}

	// 生成配置
	genConfig := map[string]interface{}{}

	if claudeReq.MaxCompletionTokens > 0 {
		genConfig["maxOutputTokens"] = claudeReq.MaxCompletionTokens
	} else if claudeReq.MaxTokens > 0 {
		genConfig["maxOutputTokens"] = claudeReq.MaxTokens
	}

	if claudeReq.Temperature > 0 {
		genConfig["temperature"] = claudeReq.Temperature
	}

	if len(genConfig) > 0 {
		req["generationConfig"] = genConfig
	}

	// 工具
	var functionDeclarations []map[string]interface{}
	if len(claudeReq.Tools) > 0 {
		functionDeclarations = p.convertTools(claudeReq.Tools)
		if len(functionDeclarations) > 0 {
			req["tools"] = []map[string]interface{}{
				{
					"functionDeclarations": functionDeclarations,
				},
			}
		}
	}

	toolConfig, err := mapClaudeToolChoiceToGemini(claudeReq.ToolChoice)
	if err != nil {
		return nil, err
	}
	if toolConfig != nil && len(functionDeclarations) > 0 {
		req["toolConfig"] = toolConfig
	}

	return req, nil
}

func buildGeminiSystemInstructionText(claudeReq *types.ClaudeRequest) string {
	parts := make([]string, 0, 1)
	if claudeReq.System != nil {
		if systemText := extractSystemText(claudeReq.System); systemText != "" {
			parts = append(parts, systemText)
		}
	}
	for _, msg := range claudeReq.Messages {
		if strings.ToLower(msg.Role) != "system" {
			continue
		}
		if text := extractClaudeMessageText(msg.Content); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func extractClaudeMessageText(content interface{}) string {
	if str, ok := content.(string); ok {
		return str
	}
	blocks := utils.NormalizeContentBlocks(content)
	if len(blocks) == 0 {
		return ""
	}
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if text, ok := utils.ExtractTextFromBlock(block); ok {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

// convertMessages 转换消息
func (p *GeminiProvider) convertMessages(claudeMessages []types.ClaudeMessage, snapshot GeminiShadowSnapshot) ([]map[string]interface{}, error) {
	messages := []map[string]interface{}{}
	shadowTurns := snapshot.Turns
	toolNameByID := buildGeminiToolNameMapFromShadowTurns(shadowTurns)
	thoughtSignatureByID := buildGeminiThoughtSignatureMapFromShadowTurns(shadowTurns)
	for id, name := range buildClaudeToolNameMap(claudeMessages) {
		if _, exists := toolNameByID[id]; !exists {
			toolNameByID[id] = name
		}
	}

	totalAssistantMessages := 0
	for _, msg := range claudeMessages {
		if strings.ToLower(msg.Role) == "assistant" {
			totalAssistantMessages++
		}
	}

	effectiveShadowTurns := shadowTurns
	if len(effectiveShadowTurns) > totalAssistantMessages {
		effectiveShadowTurns = effectiveShadowTurns[len(effectiveShadowTurns)-totalAssistantMessages:]
	}
	shadowStartIndex := totalAssistantMessages - len(effectiveShadowTurns)
	assistantSeenIndex := 0
	usedShadowIndices := map[int]bool{}

	for _, msg := range claudeMessages {
		role := strings.ToLower(msg.Role)
		if role == "system" {
			continue
		}

		var geminiMsg map[string]interface{}
		var err error
		if role == "assistant" {
			positionalIndex := -1
			if assistantSeenIndex >= shadowStartIndex {
				candidate := assistantSeenIndex - shadowStartIndex
				if candidate >= 0 && candidate < len(effectiveShadowTurns) && !usedShadowIndices[candidate] {
					positionalIndex = candidate
				}
			}
			matchIndex := findMatchingGeminiShadowTurnForAssistantMessage(msg.Content, effectiveShadowTurns, usedShadowIndices)
			assistantSeenIndex++

			shadowIndex := matchIndex
			if shadowIndex < 0 {
				shadowIndex = positionalIndex
			}
			if shadowIndex >= 0 {
				usedShadowIndices[shadowIndex] = true
				shadowTurn := effectiveShadowTurns[shadowIndex]
				mergeGeminiToolNamesFromShadow(shadowTurn, toolNameByID)
				mergeGeminiThoughtSignaturesFromShadow(shadowTurn, thoughtSignatureByID)
				if parts := geminiShadowReplayParts(shadowTurn.AssistantContent, p.stripThoughtSignature, p.injectDummyThoughtSignature); len(parts) > 0 {
					mergeGeminiToolNamesFromParts(parts, toolNameByID)
					geminiMsg = map[string]interface{}{
						"role":  "model",
						"parts": parts,
					}
				}
			}
		}

		if geminiMsg == nil {
			geminiMsg, err = p.convertMessage(msg, toolNameByID, thoughtSignatureByID)
		}
		if err != nil {
			return nil, err
		}
		if geminiMsg != nil {
			if role == "assistant" {
				if parts, ok := geminiMsg["parts"].([]interface{}); ok {
					mergeGeminiToolNamesFromParts(parts, toolNameByID)
				}
			}
			messages = append(messages, geminiMsg)
		}
	}

	return messages, nil
}

func buildClaudeToolNameMap(messages []types.ClaudeMessage) map[string]string {
	toolNameByID := map[string]string{}
	for _, msg := range messages {
		if strings.ToLower(msg.Role) != "assistant" {
			continue
		}
		for _, block := range utils.NormalizeContentBlocks(msg.Content) {
			blockType, _ := block["type"].(string)
			if blockType != "tool_use" {
				continue
			}
			id, _ := block["id"].(string)
			name, _ := block["name"].(string)
			if id != "" && name != "" {
				toolNameByID[id] = name
			}
		}
	}
	return toolNameByID
}

func buildGeminiToolNameMapFromShadowTurns(turns []GeminiShadowTurn) map[string]string {
	out := map[string]string{}
	for _, turn := range turns {
		mergeGeminiToolNamesFromShadow(turn, out)
	}
	return out
}

func buildGeminiThoughtSignatureMapFromShadowTurns(turns []GeminiShadowTurn) map[string]string {
	out := map[string]string{}
	for _, turn := range turns {
		mergeGeminiThoughtSignaturesFromShadow(turn, out)
	}
	return out
}

func mergeGeminiToolNamesFromShadow(turn GeminiShadowTurn, toolNameByID map[string]string) {
	for _, toolCall := range turn.ToolCalls {
		if toolCall.ID != "" && toolCall.Name != "" {
			toolNameByID[toolCall.ID] = toolCall.Name
		}
	}
	mergeGeminiToolNamesFromParts(geminiShadowReplayParts(turn.AssistantContent, false, false), toolNameByID)
}

func mergeGeminiThoughtSignaturesFromShadow(turn GeminiShadowTurn, thoughtSignatureByID map[string]string) {
	for _, toolCall := range turn.ToolCalls {
		if toolCall.ID != "" && toolCall.ThoughtSignature != "" {
			thoughtSignatureByID[toolCall.ID] = toolCall.ThoughtSignature
		}
	}
}

func mergeGeminiToolNamesFromParts(parts []interface{}, toolNameByID map[string]string) {
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]interface{})
		if !ok {
			continue
		}
		fc, ok := part["functionCall"].(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := fc["id"].(string)
		name, _ := fc["name"].(string)
		if id != "" && name != "" {
			toolNameByID[id] = name
		}
	}
}

func findMatchingGeminiShadowTurnForAssistantMessage(content interface{}, shadowTurns []GeminiShadowTurn, used map[int]bool) int {
	toolUseIDs, toolUseNames := extractAssistantToolUseKeys(content)
	if len(toolUseIDs) == 0 && len(toolUseNames) == 0 {
		return -1
	}
	if len(toolUseIDs) > 0 {
		for i, turn := range shadowTurns {
			if used[i] {
				continue
			}
			for _, toolCall := range turn.ToolCalls {
				if toolUseIDs[toolCall.ID] {
					return i
				}
			}
		}
	}
	for i, turn := range shadowTurns {
		if used[i] {
			continue
		}
		for _, toolCall := range turn.ToolCalls {
			if toolUseNames[toolCall.Name] || toolUseNames[normalizeGeminiToolName(toolCall.Name)] {
				return i
			}
		}
	}
	return -1
}

func extractAssistantToolUseKeys(content interface{}) (map[string]bool, map[string]bool) {
	ids := map[string]bool{}
	names := map[string]bool{}
	for _, block := range utils.NormalizeContentBlocks(content) {
		if blockType, _ := block["type"].(string); blockType != "tool_use" {
			continue
		}
		if id, _ := block["id"].(string); id != "" {
			ids[id] = true
		}
		if name, _ := block["name"].(string); name != "" {
			names[name] = true
			names[normalizeGeminiToolName(name)] = true
		}
	}
	return ids, names
}

func normalizeGeminiToolName(name string) string {
	if idx := strings.LastIndex(name, ":"); idx >= 0 && idx+1 < len(name) {
		return name[idx+1:]
	}
	return name
}

func geminiShadowReplayParts(content map[string]interface{}, stripThoughtSignature bool, injectDummyThoughtSignature bool) []interface{} {
	if len(content) == 0 {
		return nil
	}
	var parts []interface{}
	if rawParts, ok := content["parts"].([]interface{}); ok {
		parts = cloneInterface(rawParts).([]interface{})
	} else if rawArray, ok := content["parts"].([]map[string]interface{}); ok {
		parts = make([]interface{}, 0, len(rawArray))
		for _, part := range rawArray {
			parts = append(parts, cloneMapStringInterface(part))
		}
	} else {
		return nil
	}

	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]interface{})
		if !ok {
			continue
		}
		applyGeminiThoughtSignaturePolicy(part, stripThoughtSignature, injectDummyThoughtSignature)
		fc, ok := part["functionCall"].(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := fc["id"].(string)
		if !shouldSendGeminiFunctionID(id) {
			delete(fc, "id")
		}
	}
	return parts
}

// convertMessage 转换单个消息
func (p *GeminiProvider) convertMessage(msg types.ClaudeMessage, toolNameByID map[string]string, thoughtSignatureByID map[string]string) (map[string]interface{}, error) {
	role := strings.ToLower(msg.Role)
	if role == "assistant" {
		role = "model"
	} else {
		role = "user"
	}

	parts := []interface{}{}

	// 处理字符串内容
	if str, ok := msg.Content.(string); ok {
		parts = append(parts, map[string]string{
			"text": str,
		})
		return map[string]interface{}{
			"role":  role,
			"parts": parts,
		}, nil
	}

	contents := utils.NormalizeContentBlocks(msg.Content)
	if len(contents) == 0 {
		return nil, nil
	}

	for _, content := range contents {
		contentType, _ := content["type"].(string)

		switch contentType {
		case "thinking":
			// thinking 块不转发到 Gemini（Gemini 不支持历史 thinking），跳过

		case "text":
			if text, ok := content["text"].(string); ok {
				parts = append(parts, map[string]string{
					"text": text,
				})
			}

		case "image", "image_url", "input_image", "document":
			if part, ok := claudeBlockToGeminiInlineData(content); ok {
				parts = append(parts, part)
			}

		case "tool_use":
			id, _ := content["id"].(string)
			name, _ := content["name"].(string)
			input := content["input"]
			if input == nil {
				input = map[string]interface{}{}
			}
			if id != "" && name != "" {
				toolNameByID[id] = name
			}

			functionCall := map[string]interface{}{
				"name": name,
				"args": input,
			}
			if id != "" && shouldSendGeminiFunctionID(id) {
				functionCall["id"] = id
			}
			part := map[string]interface{}{"functionCall": functionCall}
			if sig := thoughtSignatureByID[id]; sig != "" {
				part["thoughtSignature"] = sig
			}
			applyGeminiThoughtSignaturePolicy(part, p.stripThoughtSignature, p.injectDummyThoughtSignature)

			parts = append(parts, part)

		case "tool_result":
			toolUseID, _ := content["tool_use_id"].(string)
			name := toolNameByID[toolUseID]
			if name == "" {
				return nil, fmt.Errorf("Claude -> Gemini 无法解析 tool_result 对应的 functionResponse.name: tool_use_id=%q", toolUseID)
			}

			functionResponse := map[string]interface{}{
				"name":     name,
				"response": normalizeGeminiFunctionResponse(content["content"]),
			}
			if toolUseID != "" && shouldSendGeminiFunctionID(toolUseID) {
				functionResponse["id"] = toolUseID
			}

			parts = append(parts, map[string]interface{}{"functionResponse": functionResponse})
		}
	}

	if len(parts) == 0 {
		return nil, nil
	}

	return map[string]interface{}{
		"role":  role,
		"parts": parts,
	}, nil
}

func normalizeGeminiFunctionResponse(content interface{}) map[string]interface{} {
	if content == nil {
		return map[string]interface{}{"content": ""}
	}
	if _, isString := content.(string); !isString {
		if blocks, ok := content.([]map[string]interface{}); ok && len(blocks) > 0 {
			texts := make([]string, 0, len(blocks))
			for _, block := range blocks {
				if text, ok := utils.ExtractTextFromBlock(block); ok {
					texts = append(texts, text)
				}
			}
			if len(texts) > 0 {
				return map[string]interface{}{"content": strings.Join(texts, "\n")}
			}
			return map[string]interface{}{"content": content}
		}
	}
	if blocks := utils.NormalizeContentBlocks(content); len(blocks) > 0 {
		texts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if text, ok := utils.ExtractTextFromBlock(block); ok {
				texts = append(texts, text)
			}
		}
		if len(texts) > 0 {
			return map[string]interface{}{"content": strings.Join(texts, "\n")}
		}
	}
	return map[string]interface{}{"content": content}
}

func mapClaudeToolChoiceToGemini(toolChoice interface{}) (map[string]interface{}, error) {
	if toolChoice == nil {
		return nil, nil
	}
	switch v := toolChoice.(type) {
	case string:
		switch v {
		case "", "auto":
			return map[string]interface{}{"functionCallingConfig": map[string]interface{}{"mode": "AUTO"}}, nil
		case "none":
			return map[string]interface{}{"functionCallingConfig": map[string]interface{}{"mode": "NONE"}}, nil
		case "any":
			return map[string]interface{}{"functionCallingConfig": map[string]interface{}{"mode": "ANY"}}, nil
		default:
			return nil, fmt.Errorf("Claude -> Gemini 不支持的 tool_choice 字符串: %s", v)
		}
	case map[string]interface{}:
		choiceType, _ := v["type"].(string)
		switch choiceType {
		case "", "auto":
			return map[string]interface{}{"functionCallingConfig": map[string]interface{}{"mode": "AUTO"}}, nil
		case "none":
			return map[string]interface{}{"functionCallingConfig": map[string]interface{}{"mode": "NONE"}}, nil
		case "any":
			return map[string]interface{}{"functionCallingConfig": map[string]interface{}{"mode": "ANY"}}, nil
		case "tool":
			name, _ := v["name"].(string)
			if name == "" {
				if tool, ok := v["tool"].(map[string]interface{}); ok {
					name, _ = tool["name"].(string)
				}
			}
			if name == "" {
				return nil, fmt.Errorf("Claude -> Gemini tool_choice.type=tool 缺少 name")
			}
			return map[string]interface{}{
				"functionCallingConfig": map[string]interface{}{
					"mode":                 "ANY",
					"allowedFunctionNames": []string{name},
				},
			}, nil
		default:
			return nil, fmt.Errorf("Claude -> Gemini 不支持的 tool_choice.type: %s", choiceType)
		}
	default:
		return nil, fmt.Errorf("Claude -> Gemini 不支持的 tool_choice 类型: %T", toolChoice)
	}
}

func shouldSendGeminiFunctionID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if strings.HasPrefix(id, geminiSynthesizedIDPrefix) || strings.HasPrefix(id, "toolu_") {
		return false
	}
	return true
}

func synthesizeGeminiToolCallID() string {
	return fmt.Sprintf("%s%s", geminiSynthesizedIDPrefix, uuid.NewString())
}

func rectifyGeminiFunctionCallIDs(parts []interface{}) {
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]interface{})
		if !ok {
			continue
		}
		fc, ok := part["functionCall"].(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := fc["id"].(string)
		if strings.TrimSpace(id) == "" {
			fc["id"] = synthesizeGeminiToolCallID()
		}
	}
}

func visibleGeminiShadowParts(parts []interface{}) []interface{} {
	out := make([]interface{}, 0, len(parts))
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]interface{})
		if !ok {
			continue
		}
		if isGeminiThoughtPart(part) {
			continue
		}
		out = append(out, cloneMapStringInterface(part))
	}
	return out
}

func isGeminiThoughtPart(part map[string]interface{}) bool {
	if thought, ok := part["thought"].(bool); ok {
		return thought
	}
	if thought, ok := part["thought"].(string); ok {
		return thought != "" && thought != "false"
	}
	return false
}

func buildGeminiShadowTurnFromParts(parts []interface{}) GeminiShadowTurn {
	visibleParts := visibleGeminiShadowParts(parts)
	return GeminiShadowTurn{
		AssistantContent: map[string]interface{}{"parts": visibleParts},
		ToolCalls:        extractGeminiShadowToolCalls(visibleParts),
	}
}

func geminiShadowTurnHasParts(turn GeminiShadowTurn) bool {
	parts, ok := turn.AssistantContent["parts"].([]interface{})
	return ok && len(parts) > 0
}

func extractGeminiShadowToolCalls(parts []interface{}) []GeminiShadowToolCall {
	toolCalls := make([]GeminiShadowToolCall, 0)
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]interface{})
		if !ok {
			continue
		}
		if isGeminiThoughtPart(part) {
			continue
		}
		fc, ok := part["functionCall"].(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := fc["id"].(string)
		if id == "" {
			id = synthesizeGeminiToolCallID()
			fc["id"] = id
		}
		name, _ := fc["name"].(string)
		toolCalls = append(toolCalls, GeminiShadowToolCall{
			ID:               id,
			Name:             name,
			Args:             fc["args"],
			ThoughtSignature: extractGeminiThoughtSignature(part),
		})
	}
	return toolCalls
}

func extractGeminiThoughtSignature(part map[string]interface{}) string {
	for _, key := range []string{"thoughtSignature", "thought_signature"} {
		if sig, _ := part[key].(string); sig != "" {
			return sig
		}
	}
	if fc, ok := part["functionCall"].(map[string]interface{}); ok {
		for _, key := range []string{"thoughtSignature", "thought_signature"} {
			if sig, _ := fc[key].(string); sig != "" {
				return sig
			}
		}
	}
	return ""
}

func applyGeminiThoughtSignaturePolicy(part map[string]interface{}, strip bool, injectDummy bool) {
	fc, ok := part["functionCall"].(map[string]interface{})
	if !ok {
		return
	}
	if strip {
		delete(part, "thoughtSignature")
		delete(part, "thought_signature")
		delete(fc, "thoughtSignature")
		delete(fc, "thought_signature")
		return
	}
	if _, exists := part["thoughtSignature"]; !exists {
		if sig, _ := fc["thoughtSignature"].(string); sig != "" {
			part["thoughtSignature"] = sig
		} else if sig, _ := fc["thought_signature"].(string); sig != "" {
			part["thoughtSignature"] = sig
		}
	}
	delete(fc, "thoughtSignature")
	delete(fc, "thought_signature")
	if !injectDummy {
		return
	}
	if extractGeminiThoughtSignature(part) != "" {
		return
	}
	part["thoughtSignature"] = types.DummyThoughtSignature
}

func claudeBlockToGeminiInlineData(block map[string]interface{}) (map[string]interface{}, bool) {
	source, _ := block["source"].(map[string]interface{})
	if source != nil {
		sourceType, _ := source["type"].(string)
		if sourceType == "base64" {
			data, _ := source["data"].(string)
			mimeType, _ := source["media_type"].(string)
			if data == "" {
				return nil, false
			}
			if mimeType == "" {
				mimeType = "image/png"
			}
			return map[string]interface{}{
				"inlineData": map[string]interface{}{
					"mimeType": mimeType,
					"data":     data,
				},
			}, true
		}
	}

	if mediaType, data, ok := extractGeminiInlineBase64(block); ok {
		return map[string]interface{}{
			"inlineData": map[string]interface{}{
				"mimeType": mediaType,
				"data":     data,
			},
		}, true
	}
	return nil, false
}

func extractGeminiInlineBase64(block map[string]interface{}) (string, string, bool) {
	mediaType, _ := block["media_type"].(string)
	data, _ := block["data"].(string)
	if data == "" {
		if nested, ok := block["image_url"].(map[string]interface{}); ok {
			url, _ := nested["url"].(string)
			if mt, payload, ok := parseGeminiDataURL(url); ok {
				return mt, payload, true
			}
		}
		if url, _ := block["image_url"].(string); url != "" {
			if mt, payload, ok := parseGeminiDataURL(url); ok {
				return mt, payload, true
			}
		}
		if url, _ := block["url"].(string); url != "" {
			if mt, payload, ok := parseGeminiDataURL(url); ok {
				return mt, payload, true
			}
		}
	}
	if data == "" {
		return "", "", false
	}
	if mediaType == "" {
		mediaType = "image/png"
	}
	return mediaType, data, true
}

func parseGeminiDataURL(url string) (string, string, bool) {
	if !strings.HasPrefix(url, "data:") {
		return "", "", false
	}
	payload := strings.TrimPrefix(url, "data:")
	header, data, ok := strings.Cut(payload, ",")
	if !ok || data == "" {
		return "", "", false
	}
	mediaType := header
	if before, _, found := strings.Cut(header, ";base64"); found {
		mediaType = before
	}
	if mediaType == "" {
		mediaType = "image/png"
	}
	return mediaType, data, true
}

// convertTools 转换工具
func (p *GeminiProvider) convertTools(claudeTools []types.ClaudeTool) []map[string]interface{} {
	tools := []map[string]interface{}{}

	for _, tool := range claudeTools {
		if tool.Name == "" || tool.Name == "BatchTool" {
			continue
		}
		tools = append(tools, buildGeminiFunctionDeclaration(tool.Name, tool.Description, tool.InputSchema))
	}

	return tools
}

func buildGeminiFunctionDeclaration(name string, description string, inputSchema interface{}) map[string]interface{} {
	declaration := map[string]interface{}{
		"name":        name,
		"description": description,
	}

	schema := normalizeGeminiJSONSchema(inputSchema)
	if requiresGeminiParametersJSONSchema(schema) {
		declaration["parametersJsonSchema"] = schema
		return declaration
	}

	declaration["parameters"] = toGeminiFunctionParametersSchema(schema)
	return declaration
}

func normalizeGeminiJSONSchema(schema interface{}) interface{} {
	if schema == nil {
		return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
	}
	normalized := normalizeGeminiJSONSchemaValue(schema)
	if schemaMap, ok := normalized.(map[string]interface{}); ok {
		if _, hasType := schemaMap["type"]; !hasType {
			schemaMap["type"] = "object"
		}
		if schemaMap["type"] == "object" {
			if _, hasProps := schemaMap["properties"]; !hasProps {
				schemaMap["properties"] = map[string]interface{}{}
			}
		}
		return schemaMap
	}
	return normalized
}

func normalizeGeminiJSONSchemaValue(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, raw := range v {
			if key == "$schema" || key == "$id" {
				continue
			}
			switch key {
			case "properties":
				if props, ok := raw.(map[string]interface{}); ok {
					cleanedProps := make(map[string]interface{}, len(props))
					for propName, propSchema := range props {
						cleanedProps[propName] = normalizeGeminiJSONSchemaValue(propSchema)
					}
					out[key] = cleanedProps
					continue
				}
			case "items", "not", "if", "then", "else", "additionalProperties":
				out[key] = normalizeGeminiJSONSchemaValue(raw)
				continue
			case "anyOf", "oneOf", "allOf", "prefixItems", "any_of", "one_of", "all_of", "prefix_items":
				if arr, ok := raw.([]interface{}); ok {
					cleaned := make([]interface{}, 0, len(arr))
					for _, item := range arr {
						cleaned = append(cleaned, normalizeGeminiJSONSchemaValue(item))
					}
					out[normalizeGeminiSchemaKeyword(key)] = cleaned
					continue
				}
			}
			out[normalizeGeminiSchemaKeyword(key)] = normalizeGeminiJSONSchemaValue(raw)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i := range v {
			out[i] = normalizeGeminiJSONSchemaValue(v[i])
		}
		return out
	default:
		return v
	}
}

func normalizeGeminiSchemaKeyword(key string) string {
	switch key {
	case "any_of":
		return "anyOf"
	case "one_of":
		return "oneOf"
	case "all_of":
		return "allOf"
	case "prefix_items":
		return "prefixItems"
	case "property_names":
		return "propertyNames"
	case "exclusive_minimum":
		return "exclusiveMinimum"
	case "exclusive_maximum":
		return "exclusiveMaximum"
	case "multiple_of":
		return "multipleOf"
	default:
		return key
	}
}

func requiresGeminiParametersJSONSchema(schema interface{}) bool {
	switch v := schema.(type) {
	case map[string]interface{}:
		return geminiSchemaObjectRequiresJSONSchema(v)
	case []interface{}:
		for _, item := range v {
			if requiresGeminiParametersJSONSchema(item) {
				return true
			}
		}
	}
	return false
}

func geminiSchemaObjectRequiresJSONSchema(obj map[string]interface{}) bool {
	for key, value := range obj {
		switch key {
		case "type":
			switch value.(type) {
			case []interface{}, []string:
				return true
			}
		case "format", "title", "description", "nullable", "enum",
			"maxItems", "minItems", "required", "minProperties", "maxProperties",
			"minLength", "maxLength", "pattern", "example", "propertyOrdering",
			"default", "minimum", "maximum":
		case "properties":
			props, ok := value.(map[string]interface{})
			if !ok {
				return true
			}
			for _, propSchema := range props {
				if requiresGeminiParametersJSONSchema(propSchema) {
					return true
				}
			}
		case "items":
			if _, ok := value.(map[string]interface{}); !ok {
				return true
			}
			if requiresGeminiParametersJSONSchema(value) {
				return true
			}
		case "anyOf":
			values, ok := value.([]interface{})
			if !ok {
				return true
			}
			for _, item := range values {
				if requiresGeminiParametersJSONSchema(item) {
					return true
				}
			}
		case "$ref", "$defs", "definitions",
			"additionalProperties", "unevaluatedProperties", "propertyNames", "patternProperties",
			"oneOf", "allOf", "const", "not", "if", "then", "else",
			"dependentRequired", "dependentSchemas", "contains", "minContains", "maxContains",
			"prefixItems", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "examples":
			return true
		default:
			return true
		}
	}
	return false
}

func toGeminiFunctionParametersSchema(schema interface{}) map[string]interface{} {
	defaultSchema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
	schemaMap, ok := schema.(map[string]interface{})
	if !ok {
		return defaultSchema
	}
	out := make(map[string]interface{}, len(schemaMap))
	for key, value := range schemaMap {
		switch key {
		case "type", "format", "title", "description", "nullable", "enum",
			"maxItems", "minItems", "required", "minProperties", "maxProperties",
			"minLength", "maxLength", "pattern", "example", "propertyOrdering",
			"default", "minimum", "maximum":
			out[key] = value
		case "properties":
			if props, ok := value.(map[string]interface{}); ok {
				converted := make(map[string]interface{}, len(props))
				for propName, propSchema := range props {
					converted[propName] = toGeminiSchemaValue(propSchema)
				}
				out[key] = converted
			}
		case "items":
			if _, ok := value.(map[string]interface{}); ok {
				out[key] = toGeminiSchemaValue(value)
			}
		case "anyOf":
			if values, ok := value.([]interface{}); ok {
				converted := make([]interface{}, 0, len(values))
				for _, item := range values {
					converted = append(converted, toGeminiSchemaValue(item))
				}
				out[key] = converted
			}
		}
	}
	if _, hasType := out["type"]; !hasType {
		out["type"] = "object"
	}
	if out["type"] == "object" {
		if _, hasProps := out["properties"]; !hasProps {
			out["properties"] = map[string]interface{}{}
		}
	}
	return out
}

func toGeminiSchemaValue(schema interface{}) interface{} {
	schemaMap, ok := schema.(map[string]interface{})
	if !ok {
		return schema
	}
	out := make(map[string]interface{}, len(schemaMap))
	for key, value := range schemaMap {
		switch key {
		case "type", "format", "title", "description", "nullable", "enum",
			"maxItems", "minItems", "required", "minProperties", "maxProperties",
			"minLength", "maxLength", "pattern", "example", "propertyOrdering",
			"default", "minimum", "maximum":
			out[key] = value
		case "properties":
			if props, ok := value.(map[string]interface{}); ok {
				converted := make(map[string]interface{}, len(props))
				for propName, propSchema := range props {
					converted[propName] = toGeminiSchemaValue(propSchema)
				}
				out[key] = converted
			}
		case "items":
			if _, ok := value.(map[string]interface{}); ok {
				out[key] = toGeminiSchemaValue(value)
			}
		case "anyOf":
			if values, ok := value.([]interface{}); ok {
				converted := make([]interface{}, 0, len(values))
				for _, item := range values {
					converted = append(converted, toGeminiSchemaValue(item))
				}
				out[key] = converted
			}
		}
	}
	return out
}

// ConvertToClaudeResponse 转换为 Claude 响应
func (p *GeminiProvider) ConvertToClaudeResponse(providerResp *types.ProviderResponse) (*types.ClaudeResponse, error) {
	var geminiResp map[string]interface{}
	if err := json.Unmarshal(providerResp.Body, &geminiResp); err != nil {
		return nil, err
	}

	claudeResp := &types.ClaudeResponse{
		ID:      generateID(),
		Type:    "message",
		Role:    "assistant",
		Content: []types.ClaudeContent{},
	}

	candidates, ok := geminiResp["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return claudeResp, nil
	}

	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		return claudeResp, nil
	}

	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return claudeResp, nil
	}

	parts, ok := content["parts"].([]interface{})
	if !ok {
		return claudeResp, nil
	}

	rectifyGeminiFunctionCallIDs(parts)
	shadowTurn := buildGeminiShadowTurnFromParts(parts)
	if geminiShadowTurnHasParts(shadowTurn) {
		defaultGeminiShadowStore.Record(p.shadowProviderID, p.shadowSessionID, shadowTurn)
	}

	textParts := make([]string, 0)

	// 处理各个部分
	for _, p := range parts {
		part, ok := p.(map[string]interface{})
		if !ok {
			continue
		}

		if isGeminiThoughtPart(part) {
			continue
		}

		// 文本内容
		if text, ok := part["text"].(string); ok {
			textParts = append(textParts, text)
		}

		// 函数调用
		if fc, ok := part["functionCall"].(map[string]interface{}); ok {
			name, _ := fc["name"].(string)
			args := fc["args"]
			id, _ := fc["id"].(string)

			claudeResp.Content = append(claudeResp.Content, types.ClaudeContent{
				Type:  "tool_use",
				ID:    id,
				Name:  name,
				Input: args,
			})
		}
	}
	if len(textParts) > 0 {
		claudeResp.Content = append([]types.ClaudeContent{{
			Type: "text",
			Text: strings.Join(textParts, ""),
		}}, claudeResp.Content...)
	}

	// 设置停止原因
	finishReason, _ := candidate["finishReason"].(string)
	if strings.Contains(strings.ToLower(finishReason), "stop") {
		// 检查是否有工具调用
		hasToolCall := false
		for _, c := range claudeResp.Content {
			if c.Type == "tool_use" {
				hasToolCall = true
				break
			}
		}

		if hasToolCall {
			claudeResp.StopReason = "tool_use"
		} else {
			claudeResp.StopReason = "end_turn"
		}
	} else if strings.Contains(strings.ToLower(finishReason), "length") {
		claudeResp.StopReason = "max_tokens"
	}

	// 使用统计
	if usageMetadata, ok := geminiResp["usageMetadata"].(map[string]interface{}); ok {
		usage := &types.Usage{}
		cachedTokens := 0

		if cachedContentTokens, ok := usageMetadata["cachedContentTokenCount"].(float64); ok {
			cachedTokens = int(cachedContentTokens)
			usage.CacheReadInputTokens = cachedTokens
		}

		if promptTokens, ok := usageMetadata["promptTokenCount"].(float64); ok {
			usage.InputTokens = int(promptTokens) - cachedTokens
			if usage.InputTokens < 0 {
				usage.InputTokens = 0
			}
		}
		if candidatesTokens, ok := usageMetadata["candidatesTokenCount"].(float64); ok {
			usage.OutputTokens = int(candidatesTokens)
		}
		claudeResp.Usage = usage
	}

	return claudeResp, nil
}

// HandleStreamResponse 处理流式响应
func (p *GeminiProvider) HandleStreamResponse(body io.ReadCloser) (<-chan string, <-chan error, error) {
	eventChan := make(chan string, 100)
	errChan := make(chan error, 1)
	shadowProviderID := p.shadowProviderID
	shadowSessionID := p.shadowSessionID

	go func() {
		defer close(eventChan)
		defer body.Close()

		scanner := bufio.NewScanner(body)
		const maxScannerBufferSize = 1024 * 1024 // 1MB
		scanner.Buffer(make([]byte, 0, 64*1024), maxScannerBufferSize)

		nextBlockIndex := 0

		// 文本块状态跟踪
		textBlockStarted := false
		textBlockIndex := -1

		// thinking 块状态跟踪
		thinkingBlockStarted := false
		thinkingBlockIndex := -1

		// message_start 事件状态
		messageStartEmitted := false
		var streamModel string

		// 跟踪是否有工具调用（用于确定 stop_reason）
		hasToolCall := false
		shadowParts := make([]interface{}, 0)
		shadowToolCalls := make([]GeminiShadowToolCall, 0)
		shadowTextParts := map[string]int{}

		// 发送 message_stop 的辅助函数
		emitMessageStop := func() {
			eventChan <- "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		}

		// 关闭 thinking 块
		closeThinkingBlock := func() {
			if !thinkingBlockStarted {
				return
			}
			sigEvent := map[string]interface{}{
				"type":  "content_block_delta",
				"index": thinkingBlockIndex,
				"delta": map[string]string{
					"type":      "signature_delta",
					"signature": "",
				},
			}
			sigJSON, _ := json.Marshal(sigEvent)
			eventChan <- fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", sigJSON)

			stopEvent := map[string]interface{}{
				"type":  "content_block_stop",
				"index": thinkingBlockIndex,
			}
			stopJSON, _ := json.Marshal(stopEvent)
			eventChan <- fmt.Sprintf("event: content_block_stop\ndata: %s\n\n", stopJSON)
			thinkingBlockStarted = false
			thinkingBlockIndex = -1
		}

		// 关闭文本块
		closeTextBlock := func() {
			if !textBlockStarted {
				return
			}
			stopEvent := map[string]interface{}{
				"type":  "content_block_stop",
				"index": textBlockIndex,
			}
			stopJSON, _ := json.Marshal(stopEvent)
			eventChan <- fmt.Sprintf("event: content_block_stop\ndata: %s\n\n", stopJSON)
			textBlockStarted = false
			textBlockIndex = -1
		}

		for scanner.Scan() {
			line := scanner.Text()
			line = strings.TrimSpace(line)

			if line == "" || line == "data: [DONE]" {
				continue
			}

			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			jsonStr := strings.TrimPrefix(line, "data: ")

			var chunk map[string]interface{}
			if err := json.Unmarshal([]byte(jsonStr), &chunk); err != nil {
				continue
			}

			// 首次收到有效 chunk 时，提取 model 并发送 message_start
			if !messageStartEmitted {
				if m, ok := chunk["model"].(string); ok {
					streamModel = m
				}
				msgStart := map[string]interface{}{
					"type": "message_start",
					"message": map[string]interface{}{
						"id":            fmt.Sprintf("msg_%s", uuid.New().String()),
						"type":          "message",
						"role":          "assistant",
						"content":       []interface{}{},
						"model":         streamModel,
						"stop_reason":   nil,
						"stop_sequence": nil,
						"usage":         map[string]interface{}{"input_tokens": 0, "output_tokens": 1},
					},
				}
				startJSON, _ := json.Marshal(msgStart)
				eventChan <- fmt.Sprintf("event: message_start\ndata: %s\n\n", startJSON)
				messageStartEmitted = true
			}

			candidates, ok := chunk["candidates"].([]interface{})
			if !ok || len(candidates) == 0 {
				continue
			}

			candidate, ok := candidates[0].(map[string]interface{})
			if !ok {
				continue
			}

			content, ok := candidate["content"].(map[string]interface{})
			if !ok {
				// 可能只有 finishReason 没有 content
				if finishReason, ok := candidate["finishReason"].(string); ok {
					closeThinkingBlock()
					closeTextBlock()

					stopReason := "end_turn"
					if hasToolCall {
						stopReason = "tool_use"
					} else if strings.Contains(strings.ToLower(finishReason), "length") {
						stopReason = "max_tokens"
					}

					deltaEvent := map[string]interface{}{
						"type": "message_delta",
						"delta": map[string]interface{}{
							"stop_reason":   stopReason,
							"stop_sequence": nil,
						},
						"usage": map[string]interface{}{
							"output_tokens": 0,
						},
					}
					deltaJSON, _ := json.Marshal(deltaEvent)
					eventChan <- fmt.Sprintf("event: message_delta\ndata: %s\n\n", deltaJSON)
				}
				continue
			}

			parts, ok := content["parts"].([]interface{})
			if !ok {
				continue
			}

			for _, p := range parts {
				part, ok := p.(map[string]interface{})
				if !ok {
					continue
				}

				// 处理 thinking/thought 内容（Gemini 2.5 thinking 模型）
				if isGeminiThoughtPart(part) {
					closeTextBlock()
					thought, _ := part["text"].(string)

					if !thinkingBlockStarted {
						thinkingBlockIndex = nextBlockIndex
						nextBlockIndex++
						startEvent := map[string]interface{}{
							"type":  "content_block_start",
							"index": thinkingBlockIndex,
							"content_block": map[string]string{
								"type":      "thinking",
								"thinking":  "",
								"signature": "",
							},
						}
						startJSON, _ := json.Marshal(startEvent)
						eventChan <- fmt.Sprintf("event: content_block_start\ndata: %s\n\n", startJSON)
						thinkingBlockStarted = true
					}

					deltaEvent := map[string]interface{}{
						"type":  "content_block_delta",
						"index": thinkingBlockIndex,
						"delta": map[string]string{
							"type":     "thinking_delta",
							"thinking": thought,
						},
					}
					deltaJSON, _ := json.Marshal(deltaEvent)
					eventChan <- fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", deltaJSON)
					continue
				}

				// 处理文本
				if text, ok := part["text"].(string); ok {
					closeThinkingBlock()

					if !textBlockStarted {
						textBlockIndex = nextBlockIndex
						nextBlockIndex++
						startEvent := map[string]interface{}{
							"type":  "content_block_start",
							"index": textBlockIndex,
							"content_block": map[string]string{
								"type": "text",
								"text": "",
							},
						}
						startJSON, _ := json.Marshal(startEvent)
						eventChan <- fmt.Sprintf("event: content_block_start\ndata: %s\n\n", startJSON)
						textBlockStarted = true
					}
					if text != "" {
						key := fmt.Sprintf("text:%d", textBlockIndex)
						if idx, exists := shadowTextParts[key]; exists {
							if existing, ok := shadowParts[idx].(map[string]interface{}); ok {
								existingText, _ := existing["text"].(string)
								existing["text"] = existingText + text
							}
						} else {
							shadowTextParts[key] = len(shadowParts)
							shadowParts = append(shadowParts, map[string]interface{}{"text": text})
						}
					}

					deltaEvent := map[string]interface{}{
						"type":  "content_block_delta",
						"index": textBlockIndex,
						"delta": map[string]string{
							"type": "text_delta",
							"text": text,
						},
					}
					deltaJSON, _ := json.Marshal(deltaEvent)
					eventChan <- fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", deltaJSON)
				}

				// 处理函数调用
				if fc, ok := part["functionCall"].(map[string]interface{}); ok {
					closeThinkingBlock()
					closeTextBlock()

					name, _ := fc["name"].(string)
					args := fc["args"]
					toolUseBlockIndex := nextBlockIndex
					nextBlockIndex++
					id, _ := fc["id"].(string)
					if id == "" {
						id = synthesizeGeminiToolCallID()
						fc["id"] = id
					}
					hasToolCall = true
					shadowPart := cloneMapStringInterface(part)
					if shadowFC, ok := shadowPart["functionCall"].(map[string]interface{}); ok {
						shadowFC["id"] = id
					}
					shadowParts = append(shadowParts, shadowPart)
					shadowToolCalls = append(shadowToolCalls, GeminiShadowToolCall{
						ID:               id,
						Name:             name,
						Args:             args,
						ThoughtSignature: extractGeminiThoughtSignature(part),
					})

					events := processToolUsePart(id, name, args, toolUseBlockIndex)
					for _, event := range events {
						eventChan <- event
					}
				}
			}

			// 处理结束原因
			if finishReason, ok := candidate["finishReason"].(string); ok {
				closeThinkingBlock()
				closeTextBlock()

				stopReason := "end_turn"
				if hasToolCall {
					stopReason = "tool_use"
				} else if strings.Contains(strings.ToLower(finishReason), "length") {
					stopReason = "max_tokens"
				}

				deltaEvent := map[string]interface{}{
					"type": "message_delta",
					"delta": map[string]interface{}{
						"stop_reason":   stopReason,
						"stop_sequence": nil,
					},
					"usage": map[string]interface{}{
						"output_tokens": 0,
					},
				}
				deltaJSON, _ := json.Marshal(deltaEvent)
				eventChan <- fmt.Sprintf("event: message_delta\ndata: %s\n\n", deltaJSON)
			}
		}

		// 确保流结束时关闭任何未关闭的块
		closeThinkingBlock()
		closeTextBlock()
		if len(shadowParts) > 0 {
			defaultGeminiShadowStore.Record(shadowProviderID, shadowSessionID, GeminiShadowTurn{
				AssistantContent: map[string]interface{}{"parts": cloneInterface(shadowParts)},
				ToolCalls:        shadowToolCalls,
			})
		}

		if err := scanner.Err(); err != nil {
			errMsg := err.Error()
			if strings.Contains(errMsg, "broken pipe") ||
				strings.Contains(errMsg, "connection reset") ||
				strings.Contains(errMsg, "EOF") {
				emitMessageStop()
				return
			}
			errChan <- err
		}

		emitMessageStop()
	}()

	return eventChan, errChan, nil
}
