// Package common 提供 handlers 模块的公共功能
package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/httpclient"
	"github.com/BenedictKing/claude-proxy/internal/metrics"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/gin-gonic/gin"
)

// ReadRequestBody 读取并验证请求体大小
// 返回: (bodyBytes, error)
// 如果请求体过大，会自动返回 413 错误并排空剩余数据
func ReadRequestBody(c *gin.Context, maxBodySize int64) ([]byte, error) {
	limitedReader := io.LimitReader(c.Request.Body, maxBodySize+1)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		c.JSON(400, gin.H{"error": "Failed to read request body"})
		return nil, err
	}

	if int64(len(bodyBytes)) > maxBodySize {
		// 排空剩余请求体，避免 keep-alive 连接污染
		io.Copy(io.Discard, c.Request.Body)
		c.JSON(413, gin.H{"error": fmt.Sprintf("Request body too large, maximum size is %d MB", maxBodySize/1024/1024)})
		return nil, fmt.Errorf("request body too large")
	}

	// 恢复请求体供后续使用
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	return bodyBytes, nil
}

// RestoreRequestBody 恢复请求体供后续使用
func RestoreRequestBody(c *gin.Context, bodyBytes []byte) {
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
}

// SendRequest 发送 HTTP 请求到上游
// isStream: 是否为流式请求（流式请求使用无超时客户端）
// apiType: 接口类型（Messages/Responses/Gemini），用于日志标签前缀
func SendRequest(req *http.Request, upstream *config.UpstreamConfig, envCfg *config.EnvConfig, isStream bool, apiType string) (*http.Response, error) {
	clientManager := httpclient.GetManager()

	var client *http.Client
	if isStream {
		client = clientManager.GetStreamClient(upstream.InsecureSkipVerify)
	} else {
		timeout := time.Duration(envCfg.RequestTimeout) * time.Millisecond
		client = clientManager.GetStandardClient(timeout, upstream.InsecureSkipVerify)
	}

	if upstream.InsecureSkipVerify && envCfg.EnableRequestLogs {
		log.Printf("[%s-Request-TLS] 警告: 正在跳过对 %s 的TLS证书验证", apiType, req.URL.String())
	}

	if envCfg.EnableRequestLogs {
		log.Printf("[%s-Request-URL] 实际请求URL: %s", apiType, req.URL.String())
		log.Printf("[%s-Request-Method] 请求方法: %s", apiType, req.Method)
		if envCfg.IsDevelopment() {
			logRequestDetails(req, envCfg, apiType)
		}
	}

	return client.Do(req)
}

// logRequestDetails 记录请求详情（仅开发模式）
// apiType: 接口类型（Messages/Responses/Gemini），用于日志标签前缀
func logRequestDetails(req *http.Request, envCfg *config.EnvConfig, apiType string) {
	// 对请求头做敏感信息脱敏
	reqHeaders := make(map[string]string)
	for key, values := range req.Header {
		if len(values) > 0 {
			reqHeaders[key] = values[0]
		}
	}
	maskedReqHeaders := utils.MaskSensitiveHeaders(reqHeaders)
	var reqHeadersJSON []byte
	if envCfg.RawLogOutput {
		reqHeadersJSON, _ = json.Marshal(maskedReqHeaders)
	} else {
		reqHeadersJSON, _ = json.MarshalIndent(maskedReqHeaders, "", "  ")
	}
	log.Printf("[%s-Request-Headers] 实际请求头:\n%s", apiType, string(reqHeadersJSON))

	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err == nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			var formattedBody string
			if envCfg.RawLogOutput {
				formattedBody = utils.FormatJSONBytesRaw(bodyBytes)
			} else {
				formattedBody = utils.FormatJSONBytesForLog(bodyBytes, 500)
			}
			log.Printf("[%s-Request-Body] 实际请求体:\n%s", apiType, formattedBody)
		}
	}
}

// LogOriginalRequest 记录原始请求信息
func LogOriginalRequest(c *gin.Context, bodyBytes []byte, envCfg *config.EnvConfig, apiType string) {
	if !envCfg.EnableRequestLogs {
		return
	}

	log.Printf("[Request-Receive] 收到%s请求: %s %s", apiType, c.Request.Method, c.Request.URL.Path)

	if envCfg.IsDevelopment() {
		var formattedBody string
		if envCfg.RawLogOutput {
			formattedBody = utils.FormatJSONBytesRaw(bodyBytes)
		} else {
			formattedBody = utils.FormatJSONBytesForLog(bodyBytes, 500)
		}
		log.Printf("[Request-OriginalBody] 原始请求体:\n%s", formattedBody)

		sanitizedHeaders := make(map[string]string)
		for key, values := range c.Request.Header {
			if len(values) > 0 {
				sanitizedHeaders[key] = values[0]
			}
		}
		maskedHeaders := utils.MaskSensitiveHeaders(sanitizedHeaders)
		var headersJSON []byte
		if envCfg.RawLogOutput {
			headersJSON, _ = json.Marshal(maskedHeaders)
		} else {
			headersJSON, _ = json.MarshalIndent(maskedHeaders, "", "  ")
		}
		log.Printf("[Request-OriginalHeaders] 原始请求头:\n%s", string(headersJSON))
	}
}

// AreAllKeysSuspended 检查渠道的所有 Key 是否都处于熔断状态
// 用于判断是否需要启用强制探测模式
func AreAllKeysSuspended(metricsManager *metrics.MetricsManager, baseURL string, apiKeys []string) bool {
	if len(apiKeys) == 0 {
		return false
	}

	for _, apiKey := range apiKeys {
		if !metricsManager.ShouldSuspendKey(baseURL, apiKey) {
			return false
		}
	}
	return true
}

// RemoveEmptySignatures 移除请求体中 messages[*].content[*].signature 的空值
// 用于预防 Claude API 返回 400 错误
// 仅处理已知路径：messages 数组中各消息的 content 数组中的 signature 字段
// enableLog: 是否输出日志（由 envCfg.EnableRequestLogs 控制）
// apiType: 接口类型（Messages/Responses/Gemini），用于日志标签前缀
func RemoveEmptySignatures(bodyBytes []byte, enableLog bool, apiType string) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
	decoder.UseNumber() // 保留数字精度

	var data map[string]interface{}
	if err := decoder.Decode(&data); err != nil {
		return bodyBytes, false
	}

	modified, removedCount := removeEmptySignaturesInMessages(data)
	if !modified {
		return bodyBytes, false
	}

	if enableLog && removedCount > 0 {
		log.Printf("[%s-Preprocess] 已移除 %d 个空 signature 字段", apiType, removedCount)
	}

	// 使用 Encoder 并禁用 HTML 转义，保持原始格式
	newBytes, err := utils.MarshalJSONNoEscape(data)
	if err != nil {
		return bodyBytes, false
	}
	return newBytes, true
}

// removeEmptySignaturesInMessages 仅处理 messages[*].content[*].signature 路径
// 返回 (是否有修改, 移除的字段数)
func removeEmptySignaturesInMessages(data map[string]interface{}) (bool, int) {
	modified := false
	removedCount := 0

	messages, ok := data["messages"].([]interface{})
	if !ok {
		return false, 0
	}

	for _, msg := range messages {
		msgMap, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}

		content, ok := msgMap["content"].([]interface{})
		if !ok {
			continue
		}

		for _, block := range content {
			blockMap, ok := block.(map[string]interface{})
			if !ok {
				continue
			}

			if sig, exists := blockMap["signature"]; exists {
				if sig == nil {
					delete(blockMap, "signature")
					modified = true
					removedCount++
				} else if str, isStr := sig.(string); isStr && str == "" {
					delete(blockMap, "signature")
					modified = true
					removedCount++
				}
			}
		}
	}

	return modified, removedCount
}

// ExtractConversationID 从请求中提取明确的对话标识。
// 无明确标识时返回空，调用方会创建独立会话，避免错误合并不同 agent 的对话。
func ExtractConversationID(c *gin.Context, bodyBytes []byte) string {
	if c != nil {
		for _, header := range []string{"X-Conversation-Id", "Conversation_id", "Conversation-Id", "X-Claude-Code-Session-Id"} {
			if value := strings.TrimSpace(c.GetHeader(header)); value != "" {
				return value
			}
		}
		if value := strings.TrimSpace(c.Query("conversation_id")); value != "" {
			return value
		}
		if metadataID := extractCodexThreadID(c.GetHeader("X-Codex-Turn-Metadata")); metadataID != "" {
			return metadataID
		}
		for _, header := range []string{"Session_id", "Session-Id", "X-Session-Id"} {
			if value := strings.TrimSpace(c.GetHeader(header)); value != "" {
				return value
			}
		}
	}
	var req struct {
		PreviousResponseID string                 `json:"previous_response_id"`
		ConversationID     string                 `json:"conversation_id"`
		SessionID          string                 `json:"session_id"`
		ThreadID           string                 `json:"thread_id"`
		Metadata           map[string]interface{} `json:"metadata"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err == nil {
		for _, value := range []string{req.ConversationID, req.SessionID, req.ThreadID} {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
		if metadataID := extractMetadataConversationID(req.Metadata); metadataID != "" {
			return metadataID
		}
		if strings.TrimSpace(req.PreviousResponseID) != "" {
			return strings.TrimSpace(req.PreviousResponseID)
		}
	}

	return ""
}

func extractMetadataConversationID(metadata map[string]interface{}) string {
	for _, key := range []string{"conversation_id", "conversationId", "session_id", "sessionId", "thread_id", "threadId"} {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}

	// Claude Code 的 metadata.user_id 是 JSON 时，内部的 session_id 才是会话标识。
	// 普通 user_id 仍是用户身份，不能用于会话合并。
	userID, _ := metadata["user_id"].(string)
	if !strings.HasPrefix(strings.TrimSpace(userID), "{") {
		return ""
	}
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(userID), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.SessionID)
}

func extractCodexThreadID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var metadata struct {
		ThreadID  string `json:"thread_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(value), &metadata); err != nil {
		return ""
	}
	if threadID := strings.TrimSpace(metadata.ThreadID); threadID != "" {
		return threadID
	}
	return strings.TrimSpace(metadata.SessionID)
}

// ExtractRequestedChannelIndex 从请求体 metadata.channel_index 中提取显式指定的渠道索引。
// 仅当字段显式存在时才返回 ok=true；若字段类型非法则返回错误，避免静默回退到默认调度。
func ExtractRequestedChannelIndex(bodyBytes []byte) (int, bool, error) {
	if len(bodyBytes) == 0 {
		return 0, false, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
	decoder.UseNumber()

	var payload map[string]interface{}
	if err := decoder.Decode(&payload); err != nil {
		return 0, false, nil
	}

	rawMetadata, exists := payload["metadata"]
	if !exists || rawMetadata == nil {
		return 0, false, nil
	}

	metadata, ok := rawMetadata.(map[string]interface{})
	if !ok {
		return 0, false, fmt.Errorf("metadata 必须是对象")
	}

	rawChannelIndex, exists := metadata["channel_index"]
	if !exists || rawChannelIndex == nil {
		return 0, false, nil
	}

	number, ok := rawChannelIndex.(json.Number)
	if !ok {
		return 0, true, fmt.Errorf("metadata.channel_index 必须是整数")
	}

	channelIndex, err := number.Int64()
	if err != nil {
		return 0, true, fmt.Errorf("metadata.channel_index 必须是整数")
	}
	if channelIndex < 0 {
		return 0, true, fmt.Errorf("metadata.channel_index 不能小于 0")
	}

	return int(channelIndex), true, nil
}

// ResolveRequestedUpstream 解析显式指定的渠道。
// 这里允许演练/调试显式命中非默认渠道，但会拒绝 deleted 渠道和未配置 BaseURL 的渠道。
func ResolveRequestedUpstream(
	cfgManager *config.ConfigManager,
	kind scheduler.ChannelKind,
	channelIndex int,
) (*config.UpstreamConfig, int, error) {
	cfg := cfgManager.GetConfig()

	var upstreams []config.UpstreamConfig
	switch kind {
	case scheduler.ChannelKindMessages:
		upstreams = cfg.Upstream
	case scheduler.ChannelKindResponses:
		upstreams = cfg.ResponsesUpstream
	case scheduler.ChannelKindGemini:
		upstreams = cfg.GeminiUpstream
	case scheduler.ChannelKindChat:
		upstreams = cfg.ChatUpstream
	case scheduler.ChannelKindImages:
		upstreams = cfg.ImagesUpstream
	default:
		return nil, -1, fmt.Errorf("不支持的渠道类型: %s", kind)
	}

	if len(upstreams) == 0 {
		return nil, -1, fmt.Errorf("当前未配置任何 %s 渠道", kind)
	}
	if channelIndex < 0 || channelIndex >= len(upstreams) {
		return nil, -1, fmt.Errorf("metadata.channel_index [%d] 超出 %s 渠道范围", channelIndex, kind)
	}

	upstream := upstreams[channelIndex].Clone()
	if upstream == nil {
		return nil, -1, fmt.Errorf("无法解析 %s 渠道 [%d]", kind, channelIndex)
	}
	if config.GetChannelStatus(upstream) == config.ChannelStatusDeleted {
		return nil, -1, fmt.Errorf("%s 渠道 [%d] 已删除", kind, channelIndex)
	}
	if kind != scheduler.ChannelKindImages && upstream.ExcludeFromConversation {
		return nil, -1, fmt.Errorf("%s 渠道 [%d] 已设置为不参与对话", kind, channelIndex)
	}
	if strings.TrimSpace(upstream.GetEffectiveBaseURL()) == "" {
		return nil, -1, fmt.Errorf("%s 渠道 [%d] 未配置 BaseURL", kind, channelIndex)
	}

	return upstream, channelIndex, nil
}

func ExtractFirstPromptFromClaude(messages []types.ClaudeMessage) string {
	return firstPrompt(ExtractPromptsFromClaude(messages))
}

func ExtractPromptsFromClaude(messages []types.ClaudeMessage) []string {
	prompts := make([]string, 0, 3)
	for _, msg := range messages {
		if strings.EqualFold(msg.Role, "user") {
			appendPromptsFromContent(&prompts, msg.Content, 3)
		}
	}
	return prompts
}

func ExtractFirstPromptFromOpenAI(messages []types.OpenAIMessage) string {
	return firstPrompt(ExtractPromptsFromOpenAI(messages))
}

func ExtractPromptsFromOpenAI(messages []types.OpenAIMessage) []string {
	prompts := make([]string, 0, 3)
	for _, msg := range messages {
		if strings.EqualFold(msg.Role, "user") {
			appendPromptsFromContent(&prompts, msg.Content, 3)
		}
	}
	return prompts
}

func ExtractFirstPromptFromResponsesInput(input interface{}) string {
	return firstPrompt(ExtractPromptsFromResponsesInput(input))
}

func ExtractPromptsFromResponsesInput(input interface{}) []string {
	prompts := make([]string, 0, 3)
	appendResponsesInputPrompts(&prompts, input, 3)
	return prompts
}

func ExtractFirstPromptFromGemini(contents []types.GeminiContent) string {
	return firstPrompt(ExtractPromptsFromGemini(contents))
}

func ExtractPromptsFromGemini(contents []types.GeminiContent) []string {
	prompts := make([]string, 0, 3)
	for _, content := range contents {
		if content.Role != "" && !strings.EqualFold(content.Role, "user") {
			continue
		}
		for _, part := range content.Parts {
			appendPrompt(&prompts, part.Text, 3)
		}
	}
	return prompts
}

func ExtractPromptJSONField(bodyBytes []byte, field string) string {
	return firstPrompt(ExtractPromptJSONFieldPrompts(bodyBytes, field))
}

func ExtractPromptJSONFieldPrompts(bodyBytes []byte, field string) []string {
	var payload map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return nil
	}
	prompts := make([]string, 0, 3)
	appendPromptsFromContent(&prompts, payload[field], 3)
	return prompts
}

func firstPrompt(prompts []string) string {
	if len(prompts) == 0 {
		return ""
	}
	return prompts[0]
}

func appendResponsesInputPrompts(prompts *[]string, input interface{}, limit int) {
	if len(*prompts) >= limit {
		return
	}
	switch value := input.(type) {
	case []types.ResponsesItem:
		for _, item := range value {
			if len(*prompts) >= limit {
				return
			}
			if item.Role != "" && !strings.EqualFold(item.Role, "user") {
				continue
			}
			appendPromptsFromContent(prompts, item.Content, limit)
		}
	case []interface{}:
		for _, item := range value {
			if len(*prompts) >= limit {
				return
			}
			if msg, ok := item.(map[string]interface{}); ok {
				if role, _ := msg["role"].(string); role != "" && !strings.EqualFold(role, "user") {
					continue
				}
			}
			appendPromptsFromContent(prompts, item, limit)
		}
	default:
		appendPromptsFromContent(prompts, input, limit)
	}
}

func appendPromptsFromContent(prompts *[]string, content interface{}, limit int) {
	if len(*prompts) >= limit {
		return
	}
	switch value := content.(type) {
	case string:
		appendPrompt(prompts, value, limit)
	case []types.ClaudeContent:
		for _, block := range utils.NormalizeContentBlocks(value) {
			if text, ok := utils.ExtractTextFromBlock(block); ok {
				appendPrompt(prompts, text, limit)
			}
		}
	case []types.ContentBlock:
		for _, block := range utils.NormalizeContentBlocks(value) {
			if text, ok := utils.ExtractTextFromBlock(block); ok {
				appendPrompt(prompts, text, limit)
			}
		}
	case []interface{}:
		for _, item := range value {
			appendPromptsFromContent(prompts, item, limit)
			if len(*prompts) >= limit {
				return
			}
		}
	case map[string]interface{}:
		if text, ok := utils.ExtractTextFromBlock(value); ok {
			appendPrompt(prompts, text, limit)
		}
		for _, key := range []string{"content", "text", "input_text"} {
			appendPromptsFromContent(prompts, value[key], limit)
			if len(*prompts) >= limit {
				return
			}
		}
	}
}

func appendPrompt(prompts *[]string, prompt string, limit int) {
	prompt = truncatePrompt(prompt)
	if prompt == "" || len(*prompts) >= limit {
		return
	}
	for _, current := range *prompts {
		if current == prompt {
			return
		}
	}
	*prompts = append(*prompts, prompt)
}

func firstTextFromContent(content interface{}) string {
	prompts := make([]string, 0, 1)
	appendPromptsFromContent(&prompts, content, 1)
	return firstPrompt(prompts)
}

func truncatePrompt(prompt string) string {
	prompt = cleanAndExtractRealPrompt(prompt)
	prompt = strings.Join(strings.Fields(prompt), " ")
	if len(prompt) <= 300 {
		return prompt
	}
	return prompt[:300] + "..."
}

func cleanAndExtractRealPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}

	// 1. 如果包含 <user_query> 标签，优先提取其中的真实内容
	if strings.Contains(prompt, "<user_query>") && strings.Contains(prompt, "</user_query>") {
		startIdx := strings.Index(prompt, "<user_query>") + len("<user_query>")
		endIdx := strings.Index(prompt, "</user_query>")
		if endIdx > startIdx {
			queryText := strings.TrimSpace(prompt[startIdx:endIdx])
			if queryText != "" {
				return queryText
			}
		}
	}

	// 2. 剥离掉一些常见的系统元数据标签（如整个标签块）
	// 例如：<user_info>...</user_info>
	for {
		startTagIdx := strings.Index(prompt, "<user_info>")
		if startTagIdx == -1 {
			break
		}
		endTagIdx := strings.Index(prompt, "</user_info>")
		if endTagIdx == -1 || endTagIdx <= startTagIdx {
			break
		}
		prompt = prompt[:startTagIdx] + prompt[endTagIdx+len("</user_info>"):]
	}

	// 例如：<system_reminder>...</system_reminder>
	for {
		startTagIdx := strings.Index(prompt, "<system_reminder>")
		if startTagIdx == -1 {
			break
		}
		endTagIdx := strings.Index(prompt, "</system_reminder>")
		if endTagIdx == -1 || endTagIdx <= startTagIdx {
			break
		}
		prompt = prompt[:startTagIdx] + prompt[endTagIdx+len("</system_reminder>"):]
	}

	// 例如：<agent_notification>...</agent_notification>
	for {
		startTagIdx := strings.Index(prompt, "<agent_notification>")
		if startTagIdx == -1 {
			break
		}
		endTagIdx := strings.Index(prompt, "</agent_notification>")
		if endTagIdx == -1 || endTagIdx <= startTagIdx {
			break
		}
		prompt = prompt[:startTagIdx] + prompt[endTagIdx+len("</agent_notification>"):]
	}

	// 3. 过滤由 CLI 客户端自动注入、并非用户真实输入的提示词。
	//    这些内容会被 codex / claude code 以 user 角色提交，但不应计入"用户提示词"。
	if isAutoInjectedAgentPrompt(prompt) {
		return ""
	}

	return strings.TrimSpace(prompt)
}

// isAutoInjectedAgentPrompt 判断提示词是否为 CLI 客户端自动注入的非用户内容。
// 依据实际抓取的本机 session：
//   - Codex CLI 首条 user 消息为 AGENTS.md 指令（"# AGENTS.md instructions for ..."），
//     第二条为 "<environment_context>"，续聊压缩为 "Another language model started to solve this problem"。
//   - Claude Code 会以 user 角色注入会话恢复提示（"Caveat: The messages below were generated by the user..."）、
//     续聊压缩（"This session is being continued from a previous conversation..."）以及
//     slash 命令产生的 <command-name>/<command-message>/<local-command-stdout> 等标签内容。
func isAutoInjectedAgentPrompt(prompt string) bool {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)

	// --- Codex CLI ---
	// AGENTS.md 指令注入
	if strings.HasPrefix(lower, "# agents.md instructions") ||
		strings.Contains(lower, "agents.md instructions for") {
		return true
	}
	// 环境上下文注入
	if strings.HasPrefix(lower, "<environment_context>") ||
		strings.HasPrefix(lower, "<user_instructions>") {
		return true
	}
	// 续聊压缩摘要（codex）
	if strings.HasPrefix(lower, "another language model started to solve this problem") {
		return true
	}

	// --- Claude Code ---
	// 会话恢复时注入的 Caveat 提示
	if strings.HasPrefix(lower, "caveat: the messages below were generated by the user") {
		return true
	}
	// 续聊压缩摘要（claude code）
	if strings.HasPrefix(lower, "this session is being continued from a previous conversation") {
		return true
	}
	// slash 命令 / 本地命令输出等标签内容
	if strings.HasPrefix(lower, "<command-name>") ||
		strings.HasPrefix(lower, "<command-message>") ||
		strings.HasPrefix(lower, "<command-args>") ||
		strings.HasPrefix(lower, "<local-command-stdout>") ||
		strings.HasPrefix(lower, "<local-command-stderr>") {
		return true
	}

	return false
}
