package utils

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/types"
)

var visionContentTypes = map[string]struct{}{
	"image":       {},
	"image_url":   {},
	"input_image": {},
}

// DetectImageContent 检测请求体中是否包含图片内容。
func DetectImageContent(body []byte) bool {
	if len(body) == 0 {
		return false
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}

	if messages, ok := payload["messages"]; ok && ValueHasVisionContent(messages) {
		return true
	}
	if input, ok := payload["input"]; ok && ValueHasVisionContent(input) {
		return true
	}

	return false
}

// ResponsesItemHasVisionContent 检测单个 ResponsesItem 是否包含图片内容。
func ResponsesItemHasVisionContent(item types.ResponsesItem) bool {
	if isVisionContentType(item.Type) {
		return true
	}
	if ValueHasVisionContent(item.Content) {
		return true
	}
	if item.ToolUse != nil && ValueHasVisionContent(item.ToolUse.Input) {
		return true
	}
	return false
}

// ValueHasVisionContent 宽松遍历 JSON 值，检测是否包含图片内容。
func ValueHasVisionContent(value interface{}) bool {
	switch v := value.(type) {
	case nil, string, bool, float64, int, int32, int64, json.Number:
		return false
	case map[string]interface{}:
		if typeVal, _ := v["type"].(string); isVisionContentType(typeVal) {
			return true
		}
		for _, item := range v {
			if ValueHasVisionContent(item) {
				return true
			}
		}
	case []interface{}:
		for _, item := range v {
			if ValueHasVisionContent(item) {
				return true
			}
		}
	case []types.ResponsesItem:
		for _, item := range v {
			if ResponsesItemHasVisionContent(item) {
				return true
			}
		}
	case []types.ContentBlock:
		for _, block := range v {
			if isVisionContentType(block.Type) {
				return true
			}
		}
	case []types.ClaudeContent:
		for _, block := range v {
			if isVisionContentType(block.Type) {
				return true
			}
		}
	case types.ResponsesItem:
		return ResponsesItemHasVisionContent(v)
	case types.ContentBlock:
		return isVisionContentType(v.Type)
	case types.ClaudeContent:
		return isVisionContentType(v.Type)
	}

	return false
}

// NormalizeContentBlocks 将 content 统一规整为 map block 列表。
func NormalizeContentBlocks(content interface{}) []map[string]interface{} {
	switch v := content.(type) {
	case []interface{}:
		blocks := make([]map[string]interface{}, 0, len(v))
		for _, item := range v {
			block, ok := item.(map[string]interface{})
			if ok {
				blocks = append(blocks, block)
			}
		}
		return blocks
	case []map[string]interface{}:
		return v
	case []types.ContentBlock:
		blocks := make([]map[string]interface{}, 0, len(v))
		for _, block := range v {
			blocks = append(blocks, map[string]interface{}{
				"type": block.Type,
				"text": block.Text,
			})
		}
		return blocks
	case []types.ClaudeContent:
		blocks := make([]map[string]interface{}, 0, len(v))
		for _, block := range v {
			normalized := map[string]interface{}{
				"type": block.Type,
			}
			if block.Text != "" {
				normalized["text"] = block.Text
			}
			if block.ID != "" {
				normalized["id"] = block.ID
			}
			if block.Name != "" {
				normalized["name"] = block.Name
			}
			if block.Input != nil {
				normalized["input"] = block.Input
			}
			if block.ToolUseID != "" {
				normalized["tool_use_id"] = block.ToolUseID
			}
			blocks = append(blocks, normalized)
		}
		return blocks
	default:
		return nil
	}
}

// ExtractTextFromBlock 提取通用文本 block 的文本。
func ExtractTextFromBlock(block map[string]interface{}) (string, bool) {
	if block == nil {
		return "", false
	}

	typeVal, _ := block["type"].(string)
	switch typeVal {
	case "text", "input_text", "output_text":
		text, ok := block["text"].(string)
		if ok && text != "" {
			return text, true
		}
	}

	return "", false
}

// ToOpenAIImageContentBlock 将通用图片块转换为 OpenAI Chat content block。
func ToOpenAIImageContentBlock(block map[string]interface{}) (map[string]interface{}, bool) {
	if block == nil {
		return nil, false
	}

	typeVal, _ := block["type"].(string)
	url, ok := extractImageURL(block, typeVal)
	if !ok || url == "" {
		return nil, false
	}

	return map[string]interface{}{
		"type": "image_url",
		"image_url": map[string]interface{}{
			"url": url,
		},
	}, true
}

// ToClaudeImageContentBlock 将通用图片块转换为 Claude image block。
func ToClaudeImageContentBlock(block map[string]interface{}) (map[string]interface{}, bool) {
	if block == nil {
		return nil, false
	}

	typeVal, _ := block["type"].(string)
	switch typeVal {
	case "image":
		if source, ok := block["source"].(map[string]interface{}); ok {
			sourceType, _ := source["type"].(string)
			switch sourceType {
			case "base64":
				data, dataOK := source["data"].(string)
				mediaType, _ := source["media_type"].(string)
				if dataOK && data != "" {
					if mediaType == "" {
						mediaType = "image/png"
					}
					return map[string]interface{}{
						"type": "image",
						"source": map[string]interface{}{
							"type":       "base64",
							"media_type": mediaType,
							"data":       data,
						},
					}, true
				}
			case "url":
				url, urlOK := source["url"].(string)
				if urlOK && url != "" {
					return map[string]interface{}{
						"type": "image",
						"source": map[string]interface{}{
							"type": "url",
							"url":  url,
						},
					}, true
				}
			}
		}
	}

	url, ok := extractImageURL(block, typeVal)
	if ok && url != "" {
		if mediaType, data, parsed := parseDataURL(url); parsed {
			return map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{
					"type":       "base64",
					"media_type": mediaType,
					"data":       data,
				},
			}, true
		}

		return map[string]interface{}{
			"type": "image",
			"source": map[string]interface{}{
				"type": "url",
				"url":  url,
			},
		}, true
	}

	return nil, false
}

func isVisionContentType(typeVal string) bool {
	typeVal = strings.ToLower(strings.TrimSpace(typeVal))
	_, ok := visionContentTypes[typeVal]
	return ok
}

func extractImageURL(block map[string]interface{}, typeVal string) (string, bool) {
	switch typeVal {
	case "image":
		if source, ok := block["source"].(map[string]interface{}); ok {
			sourceType, _ := source["type"].(string)
			switch sourceType {
			case "base64":
				mediaType, _ := source["media_type"].(string)
				data, _ := source["data"].(string)
				if data == "" {
					return "", false
				}
				if mediaType == "" {
					mediaType = "image/png"
				}
				return fmt.Sprintf("data:%s;base64,%s", mediaType, data), true
			case "url":
				url, ok := source["url"].(string)
				return url, ok && url != ""
			}
		}
	case "image_url":
		if nested, ok := block["image_url"].(map[string]interface{}); ok {
			if url, ok := nested["url"].(string); ok && url != "" {
				return url, true
			}
		}
		if url, ok := block["image_url"].(string); ok && url != "" {
			return url, true
		}
		if url, ok := block["url"].(string); ok && url != "" {
			return url, true
		}
	case "input_image":
		if nested, ok := block["image_url"].(map[string]interface{}); ok {
			if url, ok := nested["url"].(string); ok && url != "" {
				return url, true
			}
		}
		if url, ok := block["image_url"].(string); ok && url != "" {
			return url, true
		}
		if url, ok := block["url"].(string); ok && url != "" {
			return url, true
		}
		if mediaType, data, ok := extractBase64Payload(block); ok {
			return fmt.Sprintf("data:%s;base64,%s", mediaType, data), true
		}
	}

	return "", false
}

func extractBase64Payload(block map[string]interface{}) (string, string, bool) {
	mediaType, _ := block["media_type"].(string)
	data, _ := block["data"].(string)
	if data == "" {
		if source, ok := block["source"].(map[string]interface{}); ok {
			mediaType, _ = source["media_type"].(string)
			data, _ = source["data"].(string)
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

func parseDataURL(url string) (string, string, bool) {
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
