package providers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// GeminiProvider Gemini 提供商
type GeminiProvider struct{}

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

	// --- 复用旧的转换逻辑 ---
	geminiReq := p.convertToGeminiRequest(&claudeReq, upstream)
	// --- 转换逻辑结束 ---

	reqBodyBytes, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, originalBodyBytes, fmt.Errorf("序列化Gemini请求体失败: %w", err)
	}

	model := config.RedirectModel(claudeReq.Model, upstream)
	action := "generateContent"
	if claudeReq.Stream {
		action = "streamGenerateContent?alt=sse"
	}

	url := fmt.Sprintf("%s/models/%s:%s", strings.TrimSuffix(upstream.GetEffectiveBaseURL(), "/"), model, action)

	req, err := http.NewRequestWithContext(c.Request.Context(), "POST", url, bytes.NewReader(reqBodyBytes))
	if err != nil {
		return nil, originalBodyBytes, fmt.Errorf("创建Gemini请求失败: %w", err)
	}

	// 使用统一的头部处理逻辑（透明代理）
	// 保留客户端的大部分 headers，只移除/替换必要的认证和代理相关 headers
	req.Header = utils.PrepareUpstreamHeaders(c, req.URL.Host)
	utils.SetGeminiAuthenticationHeader(req.Header, apiKey)

	return req, originalBodyBytes, nil
}

// convertToGeminiRequest 转换为 Gemini 请求体
func (p *GeminiProvider) convertToGeminiRequest(claudeReq *types.ClaudeRequest, upstream *config.UpstreamConfig) map[string]interface{} {
	req := map[string]interface{}{
		"contents": p.convertMessages(claudeReq.Messages),
	}

	// 添加系统指令
	if claudeReq.System != nil {
		systemText := extractSystemText(claudeReq.System)
		if systemText != "" {
			req["systemInstruction"] = map[string]interface{}{
				"parts": []map[string]string{
					{"text": systemText},
				},
			}
		}
	}

	// 生成配置
	genConfig := map[string]interface{}{}

	if claudeReq.MaxTokens > 0 {
		genConfig["maxOutputTokens"] = claudeReq.MaxTokens
	}

	if claudeReq.Temperature > 0 {
		genConfig["temperature"] = claudeReq.Temperature
	}

	if len(genConfig) > 0 {
		req["generationConfig"] = genConfig
	}

	// 工具
	if len(claudeReq.Tools) > 0 {
		req["tools"] = []map[string]interface{}{
			{
				"functionDeclarations": p.convertTools(claudeReq.Tools),
			},
		}
	}

	return req
}

// convertMessages 转换消息
func (p *GeminiProvider) convertMessages(claudeMessages []types.ClaudeMessage) []map[string]interface{} {
	messages := []map[string]interface{}{}

	for _, msg := range claudeMessages {
		geminiMsg := p.convertMessage(msg)
		if geminiMsg != nil {
			messages = append(messages, geminiMsg)
		}
	}

	return messages
}

// convertMessage 转换单个消息
func (p *GeminiProvider) convertMessage(msg types.ClaudeMessage) map[string]interface{} {
	role := msg.Role
	if role == "assistant" {
		role = "model"
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
		}
	}

	// 处理内容数组
	contents, ok := msg.Content.([]interface{})
	if !ok {
		return nil
	}

	for _, c := range contents {
		content, ok := c.(map[string]interface{})
		if !ok {
			continue
		}

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

		case "tool_use":
			name, _ := content["name"].(string)
			input := content["input"]

			parts = append(parts, map[string]interface{}{
				"functionCall": map[string]interface{}{
					"name": name,
					"args": input,
				},
			})

		case "tool_result":
			toolUseID, _ := content["tool_use_id"].(string)
			resultContent := content["content"]

			var response interface{}
			if str, ok := resultContent.(string); ok {
				response = map[string]string{"result": str}
			} else {
				response = resultContent
			}

			parts = append(parts, map[string]interface{}{
				"functionResponse": map[string]interface{}{
					"name":     toolUseID,
					"response": response,
				},
			})
		}
	}

	if len(parts) == 0 {
		return nil
	}

	return map[string]interface{}{
		"role":  role,
		"parts": parts,
	}
}

// convertTools 转换工具
func (p *GeminiProvider) convertTools(claudeTools []types.ClaudeTool) []map[string]interface{} {
	tools := []map[string]interface{}{}

	for _, tool := range claudeTools {
		tools = append(tools, map[string]interface{}{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  normalizeGeminiParameters(cleanJsonSchema(tool.InputSchema)),
		})
	}

	return tools
}

// normalizeGeminiParameters 确保参数 schema 符合 Gemini 要求
// Gemini 要求 functionDeclaration.parameters 必须是 type: "object" 且有 properties 字段
func normalizeGeminiParameters(schema interface{}) map[string]interface{} {
	// 默认空 schema
	defaultSchema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}

	if schema == nil {
		return defaultSchema
	}

	schemaMap, ok := schema.(map[string]interface{})
	if !ok {
		return defaultSchema
	}

	// 确保有 type 字段且为 "object"
	if _, hasType := schemaMap["type"]; !hasType {
		schemaMap["type"] = "object"
	}

	// 确保有 properties 字段
	if _, hasProps := schemaMap["properties"]; !hasProps {
		schemaMap["properties"] = map[string]interface{}{}
	}

	return schemaMap
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

	// 处理各个部分
	for _, p := range parts {
		part, ok := p.(map[string]interface{})
		if !ok {
			continue
		}

		// 文本内容
		if text, ok := part["text"].(string); ok {
			claudeResp.Content = append(claudeResp.Content, types.ClaudeContent{
				Type: "text",
				Text: text,
			})
		}

		// 函数调用
		if fc, ok := part["functionCall"].(map[string]interface{}); ok {
			name, _ := fc["name"].(string)
			args := fc["args"]

			claudeResp.Content = append(claudeResp.Content, types.ClaudeContent{
				Type:  "tool_use",
				ID:    fmt.Sprintf("toolu_%d", len(claudeResp.Content)),
				Name:  name,
				Input: args,
			})
		}
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

	go func() {
		defer close(eventChan)
		defer body.Close()

		scanner := bufio.NewScanner(body)
		const maxScannerBufferSize = 1024 * 1024 // 1MB
		scanner.Buffer(make([]byte, 0, 64*1024), maxScannerBufferSize)

		toolUseBlockIndex := 0

		// 文本块状态跟踪
		textBlockStarted := false
		textBlockIndex := 0

		// thinking 块状态跟踪
		thinkingBlockStarted := false
		thinkingBlockIndex := 0

		// message_start 事件状态
		messageStartEmitted := false
		var streamModel string

		// 跟踪是否有工具调用（用于确定 stop_reason）
		hasToolCall := false

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
			thinkingBlockIndex++
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
			textBlockIndex++
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
				if thought, ok := part["thought"].(string); ok && thought != "" {
					closeTextBlock()

					if !thinkingBlockStarted {
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
				}

				// 处理文本
				if text, ok := part["text"].(string); ok {
					closeThinkingBlock()

					if !textBlockStarted {
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
					id := fmt.Sprintf("toolu_%d", toolUseBlockIndex)
					hasToolCall = true

					events := processToolUsePart(id, name, args, toolUseBlockIndex)
					for _, event := range events {
						eventChan <- event
					}
					toolUseBlockIndex++
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
