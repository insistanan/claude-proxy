package responses

import (
	"encoding/json"
	"log"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/tidwall/gjson"
)

func hasPreviousResponseID(requestBody []byte) bool {
	if len(requestBody) == 0 {
		return false
	}
	return strings.TrimSpace(gjson.GetBytes(requestBody, "previous_response_id").String()) != ""
}

// isCodexResponsesRequest 判断请求是否带有 Codex 专用的 client_metadata。
// Codex 的 input_tokens 可能包含服务端注入的工具定义、缓存上下文和内部历史，
// 不能用代理对请求体的近似估算覆盖上游 usage；否则客户端会看到远小于真实值的上下文，
// 自动压缩永远不会按真实窗口触发。
func isCodexResponsesRequest(requestBody []byte) bool {
	if len(requestBody) == 0 {
		return false
	}

	var request struct {
		ClientMetadata map[string]json.RawMessage `json:"client_metadata"`
	}
	if err := json.Unmarshal(requestBody, &request); err != nil {
		return false
	}

	for _, key := range []string{
		"x-codex-window-id",
		"x-codex-installation-id",
		"x-codex-turn-metadata",
	} {
		raw, exists := request.ClientMetadata[key]
		if !exists {
			continue
		}
		value := strings.TrimSpace(string(raw))
		if value != "" && value != "null" && value != `""` {
			return true
		}
	}
	return false
}

// patchResponsesUsage 补全 Responses 响应的 Token 统计
func patchResponsesUsage(resp *types.ResponsesResponse, requestBody []byte, envCfg *config.EnvConfig) {
	// 检查是否有 Claude 原生缓存 token（有时才跳过 input_tokens 修补）
	// 仅检测 Claude 原生字段：cache_creation_input_tokens, cache_read_input_tokens,
	// cache_creation_5m_input_tokens, cache_creation_1h_input_tokens
	// 注意：不检测 input_tokens_details.cached_tokens（OpenAI 格式），避免错误跳过
	hasClaudeCache := resp.Usage.CacheCreationInputTokens > 0 ||
		resp.Usage.CacheReadInputTokens > 0 ||
		resp.Usage.CacheCreation5mInputTokens > 0 ||
		resp.Usage.CacheCreation1hInputTokens > 0

	// 检查是否需要补全
	needInputPatch := resp.Usage.InputTokens <= 1 && !hasClaudeCache
	needOutputPatch := resp.Usage.OutputTokens <= 1

	// 如果 usage 完全为空，进行完整估算
	if resp.Usage.InputTokens == 0 && resp.Usage.OutputTokens == 0 && resp.Usage.TotalTokens == 0 {
		// input 用带 8000 上限的保护值：Cursor 全量重发历史的请求体可达 100k-300k，
		// 直接塞全量估算会让客户端误判压缩后上下文依然是满的（compact thrash）。
		estimatedInput := utils.SafeEstimatedInputTokens(utils.EstimateResponsesRequestTokens(requestBody))
		estimatedOutput := estimateResponsesOutputFromItems(resp.Output)
		resp.Usage.InputTokens = estimatedInput
		resp.Usage.OutputTokens = estimatedOutput
		resp.Usage.TotalTokens = estimatedInput + estimatedOutput
		if envCfg.EnableResponseLogs {
			log.Printf("[Responses-Token] 上游无Usage, 本地估算: input=%d, output=%d", estimatedInput, estimatedOutput)
		}
		return
	}

	// 修补虚假值
	originalInput := resp.Usage.InputTokens
	originalOutput := resp.Usage.OutputTokens
	patched := false

	if needInputPatch {
		// 同样的 8000 上限保护，避免全量估算覆盖 input_tokens。
		resp.Usage.InputTokens = utils.SafeEstimatedInputTokens(utils.EstimateResponsesRequestTokens(requestBody))
		patched = true
	}
	if needOutputPatch {
		resp.Usage.OutputTokens = estimateResponsesOutputFromItems(resp.Output)
		patched = true
	}

	// 重新计算 TotalTokens（修补时或 total_tokens 为 0 但 input/output 有效时）
	if patched || (resp.Usage.TotalTokens == 0 && (resp.Usage.InputTokens > 0 || resp.Usage.OutputTokens > 0)) {
		resp.Usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
	}

	if envCfg.EnableResponseLogs {
		if patched {
			log.Printf("[Responses-Token] 虚假值修补: InputTokens=%d->%d, OutputTokens=%d->%d",
				originalInput, resp.Usage.InputTokens, originalOutput, resp.Usage.OutputTokens)
		}
		log.Printf("[Responses-Token] InputTokens=%d, OutputTokens=%d, TotalTokens=%d, CacheCreation=%d, CacheRead=%d, CacheCreation5m=%d, CacheCreation1h=%d, CacheTTL=%s",
			resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.TotalTokens,
			resp.Usage.CacheCreationInputTokens, resp.Usage.CacheReadInputTokens,
			resp.Usage.CacheCreation5mInputTokens, resp.Usage.CacheCreation1hInputTokens,
			resp.Usage.CacheTTL)
	}
}

// estimateResponsesOutputFromItems 从 ResponsesItem 数组估算输出 token
func estimateResponsesOutputFromItems(output []types.ResponsesItem) int {
	if len(output) == 0 {
		return 0
	}

	total := 0
	for _, item := range output {
		// 处理 content
		if item.Content != nil {
			switch v := item.Content.(type) {
			case string:
				total += utils.EstimateTokens(v)
			case []interface{}:
				for _, block := range v {
					if b, ok := block.(map[string]interface{}); ok {
						if text, ok := b["text"].(string); ok {
							total += utils.EstimateTokens(text)
						}
					}
				}
			case []types.ContentBlock:
				// 处理结构化 ContentBlock 数组
				for _, block := range v {
					if block.Text != "" {
						total += utils.EstimateTokens(block.Text)
					}
				}
			default:
				// 回退：序列化后估算
				data, _ := json.Marshal(v)
				total += utils.EstimateTokens(string(data))
			}
		}

		// 处理 tool_use
		if item.ToolUse != nil {
			if item.ToolUse.Name != "" {
				total += utils.EstimateTokens(item.ToolUse.Name) + 2
			}
			if item.ToolUse.Input != nil {
				data, _ := json.Marshal(item.ToolUse.Input)
				total += utils.EstimateTokens(string(data))
			}
		}

		// 处理 function_call 类型（item.Type == "function_call"）
		if item.Type == "function_call" {
			// 在转换后的响应中，function_call 的参数可能在 Content 中
			if contentStr, ok := item.Content.(string); ok {
				total += utils.EstimateTokens(contentStr)
			}
		}

		if item.Summary != nil {
			data, _ := json.Marshal(item.Summary)
			total += utils.EstimateTokens(string(data))
		}
	}

	return total
}

// checkResponsesEventUsage 检测 Responses 事件是否包含 usage
func checkResponsesEventUsage(event string, enableLog bool) (bool, bool, responsesStreamUsage) {
	lines := strings.Split(event, "\n")
	for _, line := range lines {
		jsonStr, isData := utils.SSEDataJSON(line)
		if !isData {
			continue
		}

		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
			continue
		}

		eventType, _ := data["type"].(string)

		// 检查 response.completed 事件中的 usage
		if eventType == "response.completed" {
			if response, ok := data["response"].(map[string]interface{}); ok {
				if usage, ok := response["usage"].(map[string]interface{}); ok {
					usageData := extractResponsesUsageFromMap(usage)
					needPatch := usageData.InputTokens <= 1 || usageData.OutputTokens <= 1

					// 仅当检测到 Claude 原生缓存字段时，才跳过 input_tokens 补全
					// OpenAI 的 input_tokens_details.cached_tokens 不应阻止补全
					if usageData.HasClaudeCache && usageData.InputTokens <= 1 {
						needPatch = usageData.OutputTokens <= 1 // 有 Claude 缓存时只检查 output
					}

					// 检查 total_tokens 是否需要补全（有效 input/output 但 total=0）
					if !needPatch && usageData.TotalTokens == 0 && (usageData.InputTokens > 0 || usageData.OutputTokens > 0) {
						needPatch = true
					}

					if enableLog {
						log.Printf("[Responses-Stream-Token] response.completed: InputTokens=%d, OutputTokens=%d, TotalTokens=%d, CacheCreation=%d, CacheRead=%d, HasClaudeCache=%v, 需补全=%v",
							usageData.InputTokens, usageData.OutputTokens, usageData.TotalTokens, usageData.CacheCreationInputTokens, usageData.CacheReadInputTokens, usageData.HasClaudeCache, needPatch)
					}
					return true, needPatch, usageData
				} else if enableLog {
					log.Printf("[Responses-Stream-Token] response.completed 事件中无 usage 字段")
				}
			} else if enableLog {
				log.Printf("[Responses-Stream-Token] response.completed 事件中无 response 字段")
			}
		}
	}
	return false, false, responsesStreamUsage{}
}

// extractResponsesUsageFromMap 从 usage map 中提取数据
func extractResponsesUsageFromMap(usage map[string]interface{}) responsesStreamUsage {
	var data responsesStreamUsage

	if v, ok := usage["input_tokens"].(float64); ok {
		data.InputTokens = int(v)
	}
	if v, ok := usage["output_tokens"].(float64); ok {
		data.OutputTokens = int(v)
	}
	if v, ok := usage["total_tokens"].(float64); ok {
		data.TotalTokens = int(v)
	}
	if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
		data.CacheCreationInputTokens = int(v)
		if v > 0 {
			data.HasClaudeCache = true
		}
	}
	_, hasExplicitCacheRead := usage["cache_read_input_tokens"]
	if v, ok := usage["cache_read_input_tokens"].(float64); ok {
		data.CacheReadInputTokens = int(v)
		if v > 0 {
			data.HasClaudeCache = true
		}
	}
	if v, ok := usage["cache_creation_5m_input_tokens"].(float64); ok {
		data.CacheCreation5mInputTokens = int(v)
		if v > 0 {
			data.HasClaudeCache = true
		}
	}
	if v, ok := usage["cache_creation_1h_input_tokens"].(float64); ok {
		data.CacheCreation1hInputTokens = int(v)
		if v > 0 {
			data.HasClaudeCache = true
		}
	}

	// 检查 input_tokens_details.cached_tokens (OpenAI 格式，不设置 HasClaudeCache)
	openAICachedTokens := 0
	if details, ok := usage["input_tokens_details"].(map[string]interface{}); ok {
		if cached, ok := details["cached_tokens"].(float64); ok && cached > 0 {
			openAICachedTokens = int(cached)
		}
	}
	if openAICachedTokens == 0 {
		if details, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
			if cached, ok := details["cached_tokens"].(float64); ok && cached > 0 {
				openAICachedTokens = int(cached)
			}
		}
	}
	if openAICachedTokens > 0 {
		// 仅当 CacheReadInputTokens 未被设置时才使用 OpenAI 的 cached_tokens
		if data.CacheReadInputTokens == 0 {
			data.CacheReadInputTokens = openAICachedTokens
		}
		if !hasExplicitCacheRead && data.InputTokens > openAICachedTokens {
			data.InputTokens -= openAICachedTokens
		}
		// 注意：不设置 HasClaudeCache，因为这是 OpenAI 格式
	}

	// 设置 CacheTTL
	var has5m, has1h bool
	if data.CacheCreation5mInputTokens > 0 {
		has5m = true
	}
	if data.CacheCreation1hInputTokens > 0 {
		has1h = true
	}
	if has5m && has1h {
		data.CacheTTL = "mixed"
	} else if has1h {
		data.CacheTTL = "1h"
	} else if has5m {
		data.CacheTTL = "5m"
	}

	return data
}

// updateResponsesStreamUsage 更新收集的 usage 数据
func updateResponsesStreamUsage(collected *responsesStreamUsage, usageData responsesStreamUsage) {
	if usageData.InputTokens > collected.InputTokens {
		collected.InputTokens = usageData.InputTokens
	}
	if usageData.OutputTokens > collected.OutputTokens {
		collected.OutputTokens = usageData.OutputTokens
	}
	if usageData.TotalTokens > collected.TotalTokens {
		collected.TotalTokens = usageData.TotalTokens
	}
	if usageData.CacheCreationInputTokens > 0 {
		collected.CacheCreationInputTokens = usageData.CacheCreationInputTokens
	}
	if usageData.CacheReadInputTokens > 0 {
		collected.CacheReadInputTokens = usageData.CacheReadInputTokens
	}
	if usageData.CacheCreation5mInputTokens > 0 {
		collected.CacheCreation5mInputTokens = usageData.CacheCreation5mInputTokens
	}
	if usageData.CacheCreation1hInputTokens > 0 {
		collected.CacheCreation1hInputTokens = usageData.CacheCreation1hInputTokens
	}
	if usageData.CacheTTL != "" {
		collected.CacheTTL = usageData.CacheTTL
	}
	// 传播 HasClaudeCache 标志
	if usageData.HasClaudeCache {
		collected.HasClaudeCache = true
	}
}

// injectResponsesUsageToCompletedEvent 向 response.completed 事件注入 usage
// 返回: 修改后的事件字符串, 估算的 inputTokens, 估算的 outputTokens
func injectResponsesUsageToCompletedEvent(event string, requestBody []byte, outputText string, envCfg *config.EnvConfig) (string, int, int) {
	// input 用带 8000 上限的保护值，理由同 SafeEstimatedInputTokens：
	// Cursor 全量重发历史的请求体极大，直接注入全量估算会让客户端误判上下文已满，
	// 触发 compact thrash。超过上限则返回 0（放弃填补），让客户端用真实 usage 或自己的估算。
	inputTokens := utils.SafeEstimatedInputTokens(utils.EstimateResponsesRequestTokens(requestBody))
	outputTokens := utils.EstimateTokens(outputText)
	totalTokens := inputTokens + outputTokens

	debugLog := envCfg.EnableResponseLogs && envCfg.ShouldLog("debug")

	// 调试日志：记录估算开始
	if debugLog {
		log.Printf("[Responses-Stream-Token] injectUsage 开始: inputTokens=%d, outputTokens=%d, event长度=%d",
			inputTokens, outputTokens, len(event))
	}

	rewritten, injected := utils.RewriteSSEDataLines(event, func(payload string) (string, bool) {
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			// 调试日志：JSON 解析失败
			if debugLog {
				log.Printf("[Responses-Stream-Token] JSON解析失败: %v, 内容前200字符: %.200s", err, payload)
			}
			return "", false
		}

		if eventType, _ := data["type"].(string); eventType != "response.completed" {
			return "", false
		}

		response, ok := data["response"].(map[string]interface{})
		if !ok {
			// response 字段缺失或类型错误，创建一个新的
			if debugLog {
				log.Printf("[Responses-Stream-Token] response字段缺失, 创建新的response对象")
			}
			response = make(map[string]interface{})
			data["response"] = response
		}

		response["usage"] = map[string]interface{}{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"total_tokens":  totalTokens,
		}

		patchedJSON, err := json.Marshal(data)
		if err != nil {
			if debugLog {
				log.Printf("[Responses-Stream-Token] JSON序列化失败: %v", err)
			}
			return "", false
		}

		if debugLog {
			log.Printf("[Responses-Stream-Token] 注入本地估算成功: InputTokens=%d, OutputTokens=%d, TotalTokens=%d",
				inputTokens, outputTokens, totalTokens)
		}
		return string(patchedJSON), true
	})

	if injected {
		return rewritten, inputTokens, outputTokens
	}

	// 逐行未命中：可能是 JSON 被拆到多个连续 data 行，合并后再试
	if debugLog {
		log.Printf("[Responses-Stream-Token] 逐行解析未找到, 尝试整体解析 event")
	}

	if merged, ok := injectUsageIntoMultiLineDataEvent(event, inputTokens, outputTokens, totalTokens); ok {
		if debugLog {
			log.Printf("[Responses-Stream-Token] 整体解析注入成功: InputTokens=%d, OutputTokens=%d",
				inputTokens, outputTokens)
		}
		return merged, inputTokens, outputTokens
	}

	// 仍然没有成功注入，记录警告并打印 event 内容
	if debugLog {
		// 打印 event 的前500个字符帮助调试
		eventPreview := event
		if len(eventPreview) > 500 {
			eventPreview = eventPreview[:500] + "..."
		}
		log.Printf("[Responses-Stream-Token] 警告: 未找到 response.completed 事件进行注入, event内容: %s", eventPreview)
	}
	return event, inputTokens, outputTokens
}

// injectUsageIntoMultiLineDataEvent 处理 JSON 被拆到多个连续 data 行的 response.completed 事件：
// 把这段连续 data 行的载荷拼成完整 JSON，注入 usage 后用一行 data 替换整段，其余行原样保留。
// 返回 ok=false 表示事件不是这种形态（无 data 行 / 拼不出合法 JSON / 不是 response.completed），调用方应保留原事件。
func injectUsageIntoMultiLineDataEvent(event string, inputTokens, outputTokens, totalTokens int) (string, bool) {
	lines := strings.Split(event, "\n")

	// 定位连续 data 行区间 [dataStart, dataEnd)
	dataStart := -1
	for i, line := range lines {
		if _, isData := utils.ParseSSEDataLine(line); isData {
			dataStart = i
			break
		}
	}
	if dataStart < 0 {
		return "", false
	}

	dataEnd := len(lines)
	var jsonBuilder strings.Builder
	for i := dataStart; i < len(lines); i++ {
		payload, isData := utils.ParseSSEDataLine(lines[i])
		if !isData {
			// 区间到首个非 data 行为止；dataEnd 保持 len(lines) 会把尾部行吞掉，必须显式记录
			dataEnd = i
			break
		}
		jsonBuilder.WriteString(payload)
	}

	fullJSON := jsonBuilder.String()
	if fullJSON == "" {
		return "", false
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(fullJSON), &data); err != nil {
		return "", false
	}
	if eventType, _ := data["type"].(string); eventType != "response.completed" {
		return "", false
	}

	response, ok := data["response"].(map[string]interface{})
	if !ok {
		response = make(map[string]interface{})
		data["response"] = response
	}
	response["usage"] = map[string]interface{}{
		"input_tokens":  inputTokens,
		"output_tokens": outputTokens,
		"total_tokens":  totalTokens,
	}

	patchedJSON, err := json.Marshal(data)
	if err != nil {
		return "", false
	}

	rebuilt := make([]string, 0, len(lines))
	rebuilt = append(rebuilt, lines[:dataStart]...)
	rebuilt = append(rebuilt, utils.SSEDataLinePrefix+string(patchedJSON))
	rebuilt = append(rebuilt, lines[dataEnd:]...)
	return strings.Join(rebuilt, "\n"), true
}

// patchResponsesCompletedEventUsage 修补 response.completed 事件中的 usage
func patchResponsesCompletedEventUsage(event string, requestBody []byte, outputText string, collected *responsesStreamUsage, envCfg *config.EnvConfig) string {
	rewritten, _ := utils.RewriteSSEDataLines(event, func(payload string) (string, bool) {
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			return "", false
		}
		if data["type"] != "response.completed" {
			return "", false
		}

		if response, ok := data["response"].(map[string]interface{}); ok {
			if usage, ok := response["usage"].(map[string]interface{}); ok {
				originalInput := collected.InputTokens
				originalOutput := collected.OutputTokens
				patched := false

				// 修补 input_tokens（仅当没有 Claude 原生缓存时）
				// OpenAI 的 cached_tokens 不应阻止 input_tokens 补全
				if collected.InputTokens <= 1 && !collected.HasClaudeCache {
					estimatedInput := utils.SafeEstimatedInputTokens(utils.EstimateResponsesRequestTokens(requestBody))
					usage["input_tokens"] = estimatedInput
					collected.InputTokens = estimatedInput
					patched = true
				}

				// 修补 output_tokens
				if collected.OutputTokens <= 1 {
					estimatedOutput := utils.EstimateTokens(outputText)
					usage["output_tokens"] = estimatedOutput
					collected.OutputTokens = estimatedOutput
					patched = true
				}

				// 重新计算 total_tokens（修补时或 total_tokens 为 0 但 input/output 有效时）
				currentTotal := 0
				if t, ok := usage["total_tokens"].(float64); ok {
					currentTotal = int(t)
				}
				if patched || (currentTotal == 0 && (collected.InputTokens > 0 || collected.OutputTokens > 0)) {
					collected.TotalTokens = collected.InputTokens + collected.OutputTokens
					usage["total_tokens"] = collected.TotalTokens
				}

				if envCfg.EnableResponseLogs && envCfg.ShouldLog("debug") && patched {
					log.Printf("[Responses-Stream-Token] 虚假值修补: InputTokens=%d->%d, OutputTokens=%d->%d",
						originalInput, collected.InputTokens, originalOutput, collected.OutputTokens)
				}
			}
		}

		patchedJSON, err := json.Marshal(data)
		if err != nil {
			return "", false
		}
		return string(patchedJSON), true
	})
	return rewritten
}

// stripAccumulatedCacheFromCompletedEvent 从 response.completed 事件里剥离累积式缓存统计。
//
// 背景：上游 grok-4.6（OpenAI 兼容格式）返回的 input_tokens_details.cached_tokens 是
// 跨请求单调递增的累积缓存命中统计（204800、205056 ...），不是"当前请求已缓存的上下文大小"。
// responses 透传分支原样转发该字段，Cursor 把它当成当前已缓存上下文大小，误判上下文一直满
// 200k，反复触发压缩、压不下去（日志证据：压缩后请求体仅 30KB，上游仍回 cached_tokens=204800）。
//
// 真正 Claude 上游的 cache_read_input_tokens 是本次请求的真实缓存命中，不在剥离范围。
// 本函数只针对 OpenAI 格式的 input_tokens_details / prompt_tokens_details，以及由它
// 派生的 cache_read_input_tokens：把这两类字段从下发给客户端的 usage 里删掉，让客户端
// 用 input_tokens（已扣除缓存的 uncached 真实值）判断上下文大小。
//
// 注意：如果请求携带 previous_response_id，说明客户端（如 Codex）正在进行链式增量会话，
// 上游返回的 cached_tokens 是服务端在 previous_response_id 链上的真实缓存，且客户端原生识别
// input_tokens_details，此时严禁剥离。
func stripAccumulatedCacheFromCompletedEvent(event string, requestBody []byte) string {
	if hasPreviousResponseID(requestBody) {
		return event
	}
	if !strings.Contains(event, "cached_tokens") &&
		!strings.Contains(event, "cache_read_input_tokens") {
		return event
	}
	rewritten, _ := utils.RewriteSSEDataLines(event, func(payload string) (string, bool) {
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			return "", false
		}
		if data["type"] != "response.completed" {
			return "", false
		}
		response, ok := data["response"].(map[string]interface{})
		if !ok {
			return "", false
		}
		usage, ok := response["usage"].(map[string]interface{})
		if !ok {
			return "", false
		}
		changed := false
		// 删除 OpenAI 累积式缓存统计字段
		if _, exists := usage["input_tokens_details"]; exists {
			delete(usage, "input_tokens_details")
			changed = true
		}
		if _, exists := usage["prompt_tokens_details"]; exists {
			delete(usage, "prompt_tokens_details")
			changed = true
		}
		// 删除由 cached_tokens 派生的 cache_read_input_tokens（extractResponsesUsageFromMap 会把它
		// 从 input_tokens_details.cached_tokens 拷贝过来，对累积式上游而言这个值是假大）。
		// 仅当没有 Claude 原生缓存创建字段时才删（有 cache_creation_* 说明是 Claude 上游，保留）。
		_, hasCacheCreation := usage["cache_creation_input_tokens"]
		_, hasCacheCreation5m := usage["cache_creation_5m_input_tokens"]
		_, hasCacheCreation1h := usage["cache_creation_1h_input_tokens"]
		isClaudeNativeCache := hasCacheCreation || hasCacheCreation5m || hasCacheCreation1h
		if _, exists := usage["cache_read_input_tokens"]; exists && !isClaudeNativeCache {
			delete(usage, "cache_read_input_tokens")
			changed = true
		}
		if !changed {
			return "", false
		}
		patchedJSON, err := json.Marshal(data)
		if err != nil {
			return "", false
		}
		return string(patchedJSON), true
	})
	return rewritten
}

// correctedInputTokens 是 Responses 透传分支的 usage 合理性校正入口。
// 具体阈值和双向判定统一由 utils.SanityCheckedInputTokens 管理，避免各协议各自维护
// 一套容易漂移的规则。

// correctedInputTokens 判断上游回报的上下文规模是否错报，返回校正后的 input_tokens。
// 薄封装：把本地估算交给 utils.SanityCheckedInputTokens（全代理共用的合理性校验，
// 双向判定 + 语义归一，契约见该函数注释与 utils/usage_sanity_test.go）。
func correctedInputTokens(requestBody []byte, upstreamInput, upstreamCached int) (int, bool) {
	if len(requestBody) == 0 {
		return 0, false
	}
	return utils.SanityCheckedInputTokens(
		utils.EstimateResponsesRequestTokens(requestBody), upstreamInput, upstreamCached)
}

// upstreamCachedTokensFromUsageMap 汇总 usage 里上游单独回报的缓存 token 总量。
// 注意本函数在累积式缓存剥离之后调用：OpenAI 的 cached_tokens 派生字段此时已被删除，
// 剩下的只有 Claude 原生缓存字段（真实的本次缓存命中/创建量）。
func upstreamCachedTokensFromUsageMap(usage map[string]interface{}) int {
	total := 0
	for _, key := range []string{
		"cache_read_input_tokens",
		"cache_creation_input_tokens",
		"cache_creation_5m_input_tokens",
		"cache_creation_1h_input_tokens",
	} {
		if v, ok := usage[key].(float64); ok && v > 0 {
			total += int(v)
		}
	}
	return total
}

// correctUnderreportedInputTokensInCompletedEvent 校正下发给客户端的 response.completed 事件里
// 被上游错报的 input_tokens，让 Cursor 这类依据回包 usage 做压缩决策的客户端看到真实上下文规模。
//
// 注意：携带 previous_response_id 或 Codex client_metadata 的请求不能用本地请求体估算校正。
// 前者的 requestBody 仅包含单轮增量，后者的 input_tokens 还可能包含服务端注入内容。
//
// 只改下发给客户端的 SSE，不动 collectedUsage——计费、熔断、性能画像仍用上游原值。
func correctUnderreportedInputTokensInCompletedEvent(event string, requestBody []byte, envCfg *config.EnvConfig) string {
	if envCfg == nil || !envCfg.CorrectResponsesInputTokens || hasPreviousResponseID(requestBody) || isCodexResponsesRequest(requestBody) {
		return event
	}
	rewritten, _ := utils.RewriteSSEDataLines(event, func(payload string) (string, bool) {
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			return "", false
		}
		if data["type"] != "response.completed" {
			return "", false
		}
		response, ok := data["response"].(map[string]interface{})
		if !ok {
			return "", false
		}
		usage, ok := response["usage"].(map[string]interface{})
		if !ok {
			return "", false
		}
		upstreamInput := 0
		if v, ok := usage["input_tokens"].(float64); ok {
			upstreamInput = int(v)
		}
		corrected, need := correctedInputTokens(requestBody, upstreamInput, upstreamCachedTokensFromUsageMap(usage))
		if !need {
			return "", false
		}
		outputTokens := 0
		if v, ok := usage["output_tokens"].(float64); ok {
			outputTokens = int(v)
		}
		usage["input_tokens"] = corrected
		usage["total_tokens"] = corrected + outputTokens
		if envCfg != nil && envCfg.EnableResponseLogs {
			log.Printf("[Responses-Stream-Token] 上游 input_tokens 错报校正: %d -> %d（本地估算，仅改下发值）",
				upstreamInput, corrected)
		}
		patchedJSON, err := json.Marshal(data)
		if err != nil {
			return "", false
		}
		return string(patchedJSON), true
	})
	return rewritten
}

// correctUnderreportedInputTokensInResponse 非流式版本，语义同
// correctUnderreportedInputTokensInCompletedEvent。
//
// 注意：本函数改的是传入的 clientUsage 副本（调用方先做 `clientUsage := *resp.Usage` 再传入），
// resp 原件保持上游原值——handler 随后仍要拿原件上报计费与请求日志。
func correctUnderreportedInputTokensInResponse(resp *types.ResponsesResponse, clientUsage *types.ResponsesUsage, requestBody []byte, envCfg *config.EnvConfig) {
	if resp == nil || clientUsage == nil || envCfg == nil || !envCfg.CorrectResponsesInputTokens || len(requestBody) == 0 || hasPreviousResponseID(requestBody) || isCodexResponsesRequest(requestBody) {
		return
	}
	// 同流式：把上游单独回报的缓存量计入判定基数，避免误伤 Claude 语义的分开报数。
	upstreamCached := resp.Usage.CacheReadInputTokens + resp.Usage.CacheCreationInputTokens +
		resp.Usage.CacheCreation5mInputTokens + resp.Usage.CacheCreation1hInputTokens
	corrected, need := correctedInputTokens(requestBody, resp.Usage.InputTokens, upstreamCached)
	if !need {
		return
	}
	if envCfg != nil && envCfg.EnableResponseLogs {
		log.Printf("[Responses-Token] 上游 input_tokens 错报校正: %d -> %d（本地估算，仅改下发值）",
			resp.Usage.InputTokens, corrected)
	}
	clientUsage.InputTokens = corrected
	clientUsage.TotalTokens = corrected + clientUsage.OutputTokens
}

// stripAccumulatedCacheFromResponse 从非流式 ResponsesResponse 的 usage 里剥离累积式缓存统计。
// 语义同 stripAccumulatedCacheFromCompletedEvent，作用于结构体字段而非 SSE 事件。
func stripAccumulatedCacheFromResponse(resp *types.ResponsesResponse, requestBody []byte) {
	if resp == nil || hasPreviousResponseID(requestBody) {
		return
	}
	// 有 Claude 原生缓存创建字段说明上游是 Claude，cache_read 是真实当前缓存，保留。
	isClaudeNativeCache := resp.Usage.CacheCreationInputTokens > 0 ||
		resp.Usage.CacheCreation5mInputTokens > 0 ||
		resp.Usage.CacheCreation1hInputTokens > 0
	resp.Usage.InputTokensDetails = nil
	if !isClaudeNativeCache {
		resp.Usage.CacheReadInputTokens = 0
	}
}
