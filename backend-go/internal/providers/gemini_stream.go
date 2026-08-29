package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/google/uuid"
)

// HandleStreamResponse 处理流式响应
// 兼容旧签名：内部委托带 ctx 的版本，使用 context.Background()。
func (p *GeminiProvider) HandleStreamResponse(body io.ReadCloser) (<-chan string, <-chan error, error) {
	return p.HandleStreamResponseCtx(context.Background(), body)
}

// HandleStreamResponseCtx 处理流式响应（支持客户端断连中止）
// 修复：所有向 eventChan 的发送都通过 send() 包装，select ctx.Done()，
// 客户端断连时立即停止读取上游并退出，杜绝"缓冲写满后永久阻塞"的 goroutine 泄漏。
func (p *GeminiProvider) HandleStreamResponseCtx(ctx context.Context, body io.ReadCloser) (<-chan string, <-chan error, error) {
	pump := newStreamPump(ctx)
	eventChan, errChan := pump.eventChan, pump.errChan
	shadowProviderID := p.shadowProviderID
	shadowSessionID := p.shadowSessionID

	go func() {
		defer close(eventChan)
		defer close(errChan)
		defer body.Close()

		send := pump.send
		fail := pump.fail

		scanner := pump.newScanner(body)

		nextBlockIndex := 0

		// 文本块状态跟踪
		textBlockStarted := false
		textBlockIndex := -1

		// message_start 事件状态
		messageStartEmitted := false
		var streamModel string

		// 跟踪是否有工具调用（用于确定 stop_reason）
		hasToolCall := false
		shadowParts := make([]interface{}, 0)
		shadowToolCalls := make([]GeminiShadowToolCall, 0)
		shadowTextParts := map[string]int{}
		seenStreamToolCalls := map[string]struct{}{}
		var reasoningText strings.Builder
		var assistantText strings.Builder
		toolUseParts := make([]types.ClaudeContent, 0)

		// 发送 message_stop 的辅助函数
		emitMessageStop := func() {
			send("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
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
			send(fmt.Sprintf("event: content_block_stop\ndata: %s\n\n", stopJSON))
			textBlockStarted = false
			textBlockIndex = -1
		}

		// 上游真实 usage。抓取点必须在 candidates 守卫之前：Gemini 原生每个 chunk 都带
		// 累积 usageMetadata、终值与 finishReason 落在同一个 chunk，但没有 candidates 的
		// chunk 会被守卫 continue 掉，抓取点放在守卫之后就读不到那一类（第三方兼容网关
		// 会把终值单独放在一个只有 usageMetadata 的尾 chunk）。
		var (
			usageInput       int
			usageOutput      int
			usageThoughts    int
			usageCacheRead   int
			usageSeen        bool
			messageDeltaSent bool
			usageArrivedLate bool
		)

		// captureUsage 用 last-wins 覆盖，不能改成"首个非空生效"：thinking 模型的早期
		// chunk 会给出只含 promptTokenCount/thoughtsTokenCount 的 usageMetadata（没有
		// candidatesTokenCount），first-wins 会把 output_tokens 永久锁在 0。
		captureUsage := func(chunk map[string]interface{}) {
			meta, ok := chunk["usageMetadata"].(map[string]interface{})
			if !ok {
				return
			}
			usageSeen = true
			if v, ok := meta["cachedContentTokenCount"].(float64); ok {
				usageCacheRead = int(v)
			}
			if v, ok := meta["promptTokenCount"].(float64); ok {
				// promptTokenCount 含 cachedContentTokenCount，扣除后才是新增输入。
				// 与非流式 ConvertResponse、handlers/gemini 两条路径、converters
				// responses_stream 同一口径，含 <0 钳制防上游异常回包污染指标。
				usageInput = int(v) - usageCacheRead
				if usageInput < 0 {
					usageInput = 0
				}
			}
			if v, ok := meta["thoughtsTokenCount"].(float64); ok {
				usageThoughts = int(v)
			}
			// candidatesTokenCount 不含 thoughtsTokenCount，而 Anthropic 的 output_tokens
			// 含 thinking，必须相加；不加就把 thinking 模型的输出系统性低报（reasoning 重的
			// 场景缺口可达 86%）。三家 output 侧语义与官方证据见 types.ClaudeOutputTokensDetails，
			// 与非流式 ConvertResponse、converters 两条 Responses 路径同口径。
			//
			// thoughts 必须先于 candidates 赋值：同一个 meta 内相加要用本轮的 thoughts。
			// 只报 thoughts 不报 candidates 的早期 chunk，output 就等于 thoughts。
			if v, ok := meta["candidatesTokenCount"].(float64); ok {
				usageOutput = int(v) + usageThoughts
			} else if usageThoughts > 0 {
				usageOutput = usageThoughts
			}
			if messageDeltaSent {
				usageArrivedLate = true
			}
		}

		// buildMessageDeltaUsage 构造 message_delta 的 usage 负载。
		// 上游没给 usageMetadata 时保持 0：不猜测、不用请求体估算（客户端每轮全量重发，
		// 估算值会离真实输入差一个数量级），由流结束后的日志留痕。
		buildMessageDeltaUsage := func() map[string]interface{} {
			messageDeltaSent = true
			if !usageSeen {
				return map[string]interface{}{"output_tokens": 0}
			}
			usage := map[string]interface{}{
				"input_tokens":  usageInput,
				"output_tokens": usageOutput,
			}
			if usageCacheRead > 0 {
				usage["cache_read_input_tokens"] = usageCacheRead
			}
			// 已计入 output_tokens，这里只作拆分展示，客户端不得再求和。
			if usageThoughts > 0 {
				usage["output_tokens_details"] = map[string]interface{}{
					"thinking_tokens": usageThoughts,
				}
			}
			return usage
		}

		for scanner.Scan() {
			line := scanner.Text()
			line = strings.TrimSpace(line)

			if line == "" {
				continue
			}

			jsonStr, isData := utils.SSEDataJSON(line)
			if !isData {
				continue
			}

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
				send(fmt.Sprintf("event: message_start\ndata: %s\n\n", startJSON))
				messageStartEmitted = true
			}

			captureUsage(chunk)

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
						"usage": buildMessageDeltaUsage(),
					}
					deltaJSON, _ := json.Marshal(deltaEvent)
					send(fmt.Sprintf("event: message_delta\ndata: %s\n\n", deltaJSON))
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

				// 处理 thinking/thought 内容（Gemini 2.5 thinking 模型）。
				// 思考只登记到代理内部缓存，绝不转换为客户端可重放的
				// Claude thinking block。
				if isGeminiThoughtPart(part) {
					thought, _ := part["text"].(string)
					if thought != "" {
						reasoningText.WriteString(thought)
					}
					continue
				}

				// 处理文本
				if text, ok := part["text"].(string); ok {
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
						send(fmt.Sprintf("event: content_block_start\ndata: %s\n\n", startJSON))
						textBlockStarted = true
					}
					if text != "" {
						assistantText.WriteString(text)
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
					send(fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", deltaJSON))
				}

				// 处理函数调用
				if fc, ok := part["functionCall"].(map[string]interface{}); ok {
					name, _ := fc["name"].(string)
					args := fc["args"]
					streamToolCallKey := geminiFunctionCallKey(fc, name, args)
					if _, exists := seenStreamToolCalls[streamToolCallKey]; exists {
						// Gemini 兼容网关可能在多个 chunk 重复发送同一个完整
						// functionCall。它不是参数 delta，不能重复转成多个
						// Claude tool_use 或 shadow tool call。
						continue
					}
					seenStreamToolCalls[streamToolCallKey] = struct{}{}
					closeTextBlock()

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
					toolUseParts = append(toolUseParts, types.ClaudeContent{
						Type:  "tool_use",
						ID:    id,
						Name:  name,
						Input: args,
					})

					events := processToolUsePart(id, name, args, toolUseBlockIndex)
					for _, event := range events {
						send(event)
					}
				}
			}

			// 处理结束原因
			if finishReason, ok := candidate["finishReason"].(string); ok {
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
					"usage": buildMessageDeltaUsage(),
				}
				deltaJSON, _ := json.Marshal(deltaEvent)
				send(fmt.Sprintf("event: message_delta\ndata: %s\n\n", deltaJSON))
			}
		}

		// 确保流结束时关闭任何未关闭的文本块
		closeTextBlock()

		// usage 失配留痕：两种情况都会让客户端与指标看到 0，必须能从日志查出来，
		// 不允许静默当成"这一轮真的是 0 token"。
		switch {
		case !usageSeen:
			log.Printf("[Gemini-Stream-Token] 警告: 上游整条流未提供 usageMetadata, usage 记为 0")
		case usageArrivedLate:
			log.Printf("[Gemini-Stream-Token] 警告: usageMetadata 在 message_delta 之后才到达(上游把终值单独放在尾 chunk), 客户端侧 usage 记为 0; 实际 input=%d output=%d(含 thinking %d) cache_read=%d",
				usageInput, usageOutput, usageThoughts, usageCacheRead)
		}

		if len(shadowParts) > 0 {
			defaultGeminiShadowStore.Record(shadowProviderID, shadowSessionID, GeminiShadowTurn{
				AssistantContent: map[string]interface{}{"parts": cloneInterface(shadowParts)},
				ToolCalls:        shadowToolCalls,
			})
		}

		if err := scanner.Err(); err != nil {
			if isDisconnectLikeError(err) {
				emitMessageStop()
				return
			}
			fail(err)
		} else if reasoningText.Len() > 0 {
			// 流式响应只有正常读完后才登记 reasoning，避免把断流产生的
			// 不完整思考绑定到下一轮 assistant 消息。
			cacheContent := make([]types.ClaudeContent, 0, len(toolUseParts)+2)
			cacheContent = append(cacheContent, types.ClaudeContent{
				Type:     "thinking",
				Thinking: reasoningText.String(),
			})
			if assistantText.Len() > 0 {
				cacheContent = append(cacheContent, types.ClaudeContent{
					Type: "text",
					Text: assistantText.String(),
				})
			}
			cacheContent = append(cacheContent, toolUseParts...)
			CacheClaudeResponseReasoning(&types.ClaudeResponse{
				Role:    "assistant",
				Content: cacheContent,
			})
		}

		emitMessageStop()
	}()

	return eventChan, errChan, nil
}
