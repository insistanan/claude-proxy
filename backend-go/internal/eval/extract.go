package eval

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type Extracted struct {
	Text           string
	SVG            string
	Numbers        []float64
	Model          string
	ContentTypes   []string
	HasThinking    bool
	HasSignature   bool
	HasUsage       bool
	OutputTokens   float64
	ThinkingTokens float64
	// HasThinkingUsage 表示上游真的回报了"思考 token"这个字段。
	// Claude Messages 不单独回报，拿不到 ≠ 用量为 0，判定器必须能区分。
	HasThinkingUsage bool
	Raw              map[string]interface{}
}

var (
	svgPattern    = regexp.MustCompile(`(?is)<svg[\s\S]*?</svg>`)
	numberPattern = regexp.MustCompile(`-?\d+(?:\.\d+)?`)
)

func ExtractFromResponse(kind string, raw *RawResponse) (Extracted, error) {
	extracted := Extracted{Raw: raw.JSON}
	extracted.Text = extractText(raw.JSON)
	extracted.Model = asString(lookup(raw.JSON, "model"))
	if extracted.Model == "" {
		extracted.Model = raw.Model
	}
	extracted.ContentTypes = extractContentTypes(raw.JSON)
	extracted.HasSignature = hasSignature(raw.JSON)
	extracted.OutputTokens, extracted.ThinkingTokens, extracted.HasUsage, extracted.HasThinkingUsage = extractUsage(raw.JSON)
	extracted.HasThinking = containsString(extracted.ContentTypes, "thinking") || extracted.ThinkingTokens > 0
	extracted.SVG = firstSVG(extracted.Text)
	extracted.Numbers = parseNumbers(extracted.Text)

	switch kind {
	case ExtractText:
		if strings.TrimSpace(extracted.Text) == "" {
			return extracted, fmt.Errorf("未能抽出文本")
		}
	case ExtractSVG:
		if extracted.SVG == "" {
			return extracted, fmt.Errorf("未能抽出 SVG")
		}
	case ExtractNumbers:
		if len(extracted.Numbers) == 0 {
			return extracted, fmt.Errorf("未能抽出数字")
		}
	case ExtractUsage, ExtractProtocol:
	default:
		return extracted, fmt.Errorf("未知抽取方式 %s", kind)
	}
	return extracted, nil
}

func extractText(payload map[string]interface{}) string {
	if payload == nil {
		return ""
	}
	if raw, ok := payload["_raw"].(string); ok {
		return raw
	}
	// Claude: content[].text
	if content, ok := payload["content"].([]interface{}); ok {
		var parts []string
		for _, item := range content {
			block, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if asString(block["type"]) == "thinking" {
				continue
			}
			if text := asString(block["text"]); text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	// OpenAI chat
	if choices, ok := payload["choices"].([]interface{}); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]interface{})
		message, _ := choice["message"].(map[string]interface{})
		if text := asString(message["content"]); text != "" {
			return text
		}
		// OpenAI / 兼容中转会把 content 拆成多 part 数组，纯字符串取不到。
		if parts := extractContentParts(message["content"]); parts != "" {
			return parts
		}
	}
	// Responses output_text
	if text := asString(payload["output_text"]); text != "" {
		return text
	}
	if output, ok := payload["output"].([]interface{}); ok {
		var parts []string
		for _, item := range output {
			block, _ := item.(map[string]interface{})
			content, _ := block["content"].([]interface{})
			for _, piece := range content {
				part, _ := piece.(map[string]interface{})
				if text := asString(part["text"]); text != "" {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	// Gemini
	if candidates, ok := payload["candidates"].([]interface{}); ok && len(candidates) > 0 {
		candidate, _ := candidates[0].(map[string]interface{})
		content, _ := candidate["content"].(map[string]interface{})
		parts, _ := content["parts"].([]interface{})
		var texts []string
		for _, item := range parts {
			part, _ := item.(map[string]interface{})
			if asBool(part["thought"]) {
				continue
			}
			if text := asString(part["text"]); text != "" {
				texts = append(texts, text)
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

func extractContentTypes(payload map[string]interface{}) []string {
	seen := map[string]struct{}{}
	var types []string
	add := func(kind string) {
		if kind == "" {
			return
		}
		if _, ok := seen[kind]; ok {
			return
		}
		seen[kind] = struct{}{}
		types = append(types, kind)
	}
	if content, ok := payload["content"].([]interface{}); ok {
		for _, item := range content {
			block, _ := item.(map[string]interface{})
			add(asString(block["type"]))
		}
	}
	if extractText(payload) != "" {
		add("text")
	}
	return types
}

// extractContentParts 处理 OpenAI / 兼容中转把 message.content 拆成多 part 数组的情况：
// [{"type":"text","text":"北京"}] 之类。纯字符串 content 由 asString 兜底，这里只管数组。
func extractContentParts(value interface{}) string {
	parts, ok := value.([]interface{})
	if !ok {
		return ""
	}
	var texts []string
	for _, item := range parts {
		block, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		// 跳过 reasoning / thinking 段，只取输出文本，与 Claude 分支口径一致。
		blockType := asString(block["type"])
		if blockType == "reasoning" || blockType == "thinking" {
			continue
		}
		if text := asString(block["text"]); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

func hasSignature(payload map[string]interface{}) bool {
	if content, ok := payload["content"].([]interface{}); ok {
		for _, item := range content {
			block, _ := item.(map[string]interface{})
			if strings.TrimSpace(asString(block["signature"])) != "" {
				return true
			}
		}
	}
	return false
}

// extractUsage 归一化各协议的用量字段。
// hasUsage 表示响应带没带用量；hasThinkingUsage 表示上游确实回报了思考 token 这一项。
//
// 注意这里**不**对 outputTokens 做"扣掉思考"的换算：各家口径根本不统一——
// Anthropic / OpenAI 的 output 含思考，xAI 不含，Gemini API 与 Vertex 说法相反且实测有出入，
// 第三方中转还会自己改写 usage。猜错方向只会让展示出来的数字更假，不如原样呈现。
func extractUsage(payload map[string]interface{}) (outputTokens, thinkingTokens float64, hasUsage, hasThinkingUsage bool) {
	if usage, ok := payload["usage"].(map[string]interface{}); ok {
		hasUsage = true
		outputTokens, _ = lookupNumber(usage, "output_tokens", "completion_tokens")
		for _, key := range []string{"output_tokens_details", "completion_tokens_details"} {
			details, ok := usage[key].(map[string]interface{})
			if !ok {
				continue
			}
			if value, found := lookupNumber(details, "thinking_tokens", "reasoning_tokens"); found {
				thinkingTokens, hasThinkingUsage = value, true
				break
			}
		}
		if !hasThinkingUsage {
			if value, found := lookupNumber(usage, "reasoning_tokens", "thinking_tokens"); found {
				thinkingTokens, hasThinkingUsage = value, true
			}
		}
	}
	if meta, ok := payload["usageMetadata"].(map[string]interface{}); ok {
		hasUsage = true
		outputTokens, _ = lookupNumber(meta, "candidatesTokenCount")
		if value, found := lookupNumber(meta, "thoughtsTokenCount"); found {
			thinkingTokens, hasThinkingUsage = value, true
		}
	}
	return outputTokens, thinkingTokens, hasUsage, hasThinkingUsage
}

func firstSVG(text string) string {
	match := svgPattern.FindString(text)
	return strings.TrimSpace(match)
}

func parseNumbers(text string) []float64 {
	matches := numberPattern.FindAllString(text, -1)
	var numbers []float64
	for _, match := range matches {
		value, err := strconv.ParseFloat(match, 64)
		if err != nil {
			continue
		}
		numbers = append(numbers, value)
	}
	return numbers
}

func lookup(payload map[string]interface{}, key string) interface{} {
	if payload == nil {
		return nil
	}
	return payload[key]
}

func asString(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func asBool(value interface{}) bool {
	typed, _ := value.(bool)
	return typed
}

func asFloat(value interface{}) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0
		}
		return parsed
	case string:
		parsed, _ := strconv.ParseFloat(typed, 64)
		return parsed
	default:
		return 0
	}
}

// asFloatSlice 兼容内存里的 []float64 与 JSON 往返后的 []interface{}。
func asFloatSlice(value interface{}) []float64 {
	switch typed := value.(type) {
	case []float64:
		return typed
	case []interface{}:
		numbers := make([]float64, 0, len(typed))
		for _, item := range typed {
			numbers = append(numbers, asFloat(item))
		}
		return numbers
	default:
		return nil
	}
}

// lookupNumber 取第一个存在的数值字段。第二个返回值区分"没有这个字段"和"字段值就是 0"。
func lookupNumber(payload map[string]interface{}, keys ...string) (float64, bool) {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok || value == nil {
			continue
		}
		return asFloat(value), true
	}
	return 0, false
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
