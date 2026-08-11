package common

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/sensitive"
)

const (
	safetySourceUser         = "user"
	safetySourceSystem       = "system"
	safetySourceToolResult   = "tool_result"
	safetySourceToolArgument = "tool_argument"
)

// SafetySegment 是协议请求中一个经过来源分类的文本片段。Mutable 为 false
// 的片段只允许审计或阻断，不会被内容安全逻辑改写。
type SafetySegment struct {
	Protocol string
	Source   string
	Path     string
	Text     string
	Mutable  bool
	replace  func(string)
}

func extractSafetySegments(apiType string, root map[string]interface{}) ([]SafetySegment, error) {
	switch strings.ToLower(apiType) {
	case "messages":
		return extractMessagesSafetySegments(root), nil
	case "chat":
		return extractChatSafetySegments(root), nil
	case "responses":
		return extractResponsesSafetySegments(root), nil
	case "gemini":
		return extractGeminiSafetySegments(root), nil
	default:
		return nil, fmt.Errorf("内容安全不支持协议 %q", apiType)
	}
}

func extractMessagesSafetySegments(root map[string]interface{}) []SafetySegment {
	segments := make([]SafetySegment, 0, 8)
	appendRootTextSegments(&segments, "messages", safetySourceSystem, "system", root, "system")
	messages, _ := root["messages"].([]interface{})
	for messageIndex, rawMessage := range messages {
		message, ok := rawMessage.(map[string]interface{})
		if !ok {
			continue
		}
		role := strings.ToLower(stringValue(message["role"]))
		content, exists := message["content"]
		if !exists {
			continue
		}
		path := fmt.Sprintf("messages[%d].content", messageIndex)
		if role == "assistant" {
			blocks, _ := content.([]interface{})
			for blockIndex, rawBlock := range blocks {
				block, ok := rawBlock.(map[string]interface{})
				if !ok || !equalString(block["type"], "tool_use") {
					continue
				}
				if input, exists := block["input"]; exists {
					appendImmutableJSONSegment(&segments, "messages", safetySourceToolArgument,
						fmt.Sprintf("%s[%d].input", path, blockIndex), input)
				}
			}
			continue
		}
		if role != "user" {
			continue
		}
		if text, ok := content.(string); ok {
			appendMapStringSegment(&segments, "messages", safetySourceUser, path, message, "content", text)
			continue
		}
		blocks, _ := content.([]interface{})
		for blockIndex, rawBlock := range blocks {
			block, ok := rawBlock.(map[string]interface{})
			if !ok {
				continue
			}
			blockPath := fmt.Sprintf("%s[%d]", path, blockIndex)
			switch stringValue(block["type"]) {
			case "tool_result":
				if value, exists := block["content"]; exists {
					appendImmutableJSONSegment(&segments, "messages", safetySourceToolResult, blockPath+".content", value)
				}
			case "", "text", "input_text":
				appendMapTextField(&segments, "messages", safetySourceUser, blockPath, block)
			}
		}
	}
	return segments
}

func extractChatSafetySegments(root map[string]interface{}) []SafetySegment {
	segments := make([]SafetySegment, 0, 8)
	messages, _ := root["messages"].([]interface{})
	for messageIndex, rawMessage := range messages {
		message, ok := rawMessage.(map[string]interface{})
		if !ok {
			continue
		}
		role := strings.ToLower(stringValue(message["role"]))
		source := ""
		switch role {
		case "user":
			source = safetySourceUser
		case "system", "developer":
			source = safetySourceSystem
		case "tool", "function":
			source = safetySourceToolResult
		case "assistant":
			appendChatMessageToolArguments(&segments, "messages", messageIndex, message)
			continue
		default:
			continue
		}
		content, exists := message["content"]
		if !exists {
			continue
		}
		path := fmt.Sprintf("messages[%d].content", messageIndex)
		if source == safetySourceToolResult {
			appendImmutableJSONSegment(&segments, "chat", source, path, content)
			continue
		}
		appendKnownTextContent(&segments, "chat", source, path, content, func(value string) {
			message["content"] = value
		})
	}
	return segments
}

func extractResponsesSafetySegments(root map[string]interface{}) []SafetySegment {
	segments := make([]SafetySegment, 0, 8)
	appendRootTextSegments(&segments, "responses", safetySourceSystem, "instructions", root, "instructions")
	input, exists := root["input"]
	if !exists {
		return segments
	}
	if text, ok := input.(string); ok {
		appendMapStringSegment(&segments, "responses", safetySourceUser, "input", root, "input", text)
		return segments
	}
	items, _ := input.([]interface{})
	for itemIndex, rawItem := range items {
		item, ok := rawItem.(map[string]interface{})
		if !ok {
			continue
		}
		itemType := strings.ToLower(stringValue(item["type"]))
		path := fmt.Sprintf("input[%d]", itemIndex)
		switch itemType {
		case "function_call_output", "custom_tool_call_output", "tool_search_output":
			if output, exists := item["output"]; exists {
				appendImmutableJSONSegment(&segments, "responses", safetySourceToolResult, path+".output", output)
			}
			continue
		case "function_call", "custom_tool_call", "tool_search_call":
			if arguments, exists := item["arguments"]; exists {
				appendImmutableJSONSegment(&segments, "responses", safetySourceToolArgument, path+".arguments", arguments)
			} else if input, exists := item["input"]; exists {
				appendImmutableJSONSegment(&segments, "responses", safetySourceToolArgument, path+".input", input)
			}
			continue
		case "input_text":
			appendMapTextField(&segments, "responses", safetySourceUser, path, item)
			continue
		}
		role := strings.ToLower(stringValue(item["role"]))
		source := ""
		switch role {
		case "user":
			source = safetySourceUser
		case "system", "developer":
			source = safetySourceSystem
		default:
			continue
		}
		if content, exists := item["content"]; exists {
			appendKnownTextContent(&segments, "responses", source, path+".content", content, func(value string) {
				item["content"] = value
			})
		}
	}
	return segments
}

func extractGeminiSafetySegments(root map[string]interface{}) []SafetySegment {
	segments := make([]SafetySegment, 0, 8)
	if instruction, ok := root["systemInstruction"].(map[string]interface{}); ok {
		appendGeminiTextParts(&segments, safetySourceSystem, "systemInstruction.parts", instruction["parts"])
	}
	contents, _ := root["contents"].([]interface{})
	for contentIndex, rawContent := range contents {
		content, ok := rawContent.(map[string]interface{})
		if !ok {
			continue
		}
		role := strings.ToLower(stringValue(content["role"]))
		parts, _ := content["parts"].([]interface{})
		for partIndex, rawPart := range parts {
			part, ok := rawPart.(map[string]interface{})
			if !ok {
				continue
			}
			path := fmt.Sprintf("contents[%d].parts[%d]", contentIndex, partIndex)
			if functionResponse, exists := part["functionResponse"]; exists {
				if response, ok := functionResponse.(map[string]interface{}); ok {
					if value, exists := response["response"]; exists {
						appendImmutableJSONSegment(&segments, "gemini", safetySourceToolResult, path+".functionResponse.response", value)
					}
				}
				continue
			}
			if functionCall, exists := part["functionCall"]; exists {
				if call, ok := functionCall.(map[string]interface{}); ok {
					if args, exists := call["args"]; exists {
						appendImmutableJSONSegment(&segments, "gemini", safetySourceToolArgument, path+".functionCall.args", args)
					}
				}
				continue
			}
			if role == "" || role == "user" {
				appendMapTextField(&segments, "gemini", safetySourceUser, path, part)
			}
		}
	}
	return segments
}

func extractResponseToolArgumentSegments(apiType string, root map[string]interface{}) []SafetySegment {
	segments := make([]SafetySegment, 0, 4)
	switch strings.ToLower(apiType) {
	case "messages":
		blocks, _ := root["content"].([]interface{})
		for index, rawBlock := range blocks {
			block, ok := rawBlock.(map[string]interface{})
			if ok && equalString(block["type"], "tool_use") {
				appendImmutableJSONSegment(&segments, apiType, safetySourceToolArgument, fmt.Sprintf("content[%d].input", index), block["input"])
			}
		}
	case "chat":
		choices, _ := root["choices"].([]interface{})
		for choiceIndex, rawChoice := range choices {
			choice, _ := rawChoice.(map[string]interface{})
			message, _ := choice["message"].(map[string]interface{})
			calls, _ := message["tool_calls"].([]interface{})
			for callIndex, rawCall := range calls {
				call, _ := rawCall.(map[string]interface{})
				function, _ := call["function"].(map[string]interface{})
				if arguments, exists := function["arguments"]; exists {
					appendImmutableJSONSegment(&segments, apiType, safetySourceToolArgument, fmt.Sprintf("choices[%d].message.tool_calls[%d].function.arguments", choiceIndex, callIndex), arguments)
				}
			}
			if functionCall, ok := message["function_call"].(map[string]interface{}); ok {
				if arguments, exists := functionCall["arguments"]; exists {
					appendImmutableJSONSegment(&segments, apiType, safetySourceToolArgument, fmt.Sprintf("choices[%d].message.function_call.arguments", choiceIndex), arguments)
				}
			}
		}
	case "responses":
		outputs, _ := root["output"].([]interface{})
		for index, rawOutput := range outputs {
			output, ok := rawOutput.(map[string]interface{})
			if !ok {
				continue
			}
			switch strings.ToLower(stringValue(output["type"])) {
			case "function_call", "custom_tool_call", "tool_search_call":
				if arguments, exists := output["arguments"]; exists {
					appendImmutableJSONSegment(&segments, apiType, safetySourceToolArgument, fmt.Sprintf("output[%d].arguments", index), arguments)
				} else if input, exists := output["input"]; exists {
					appendImmutableJSONSegment(&segments, apiType, safetySourceToolArgument, fmt.Sprintf("output[%d].input", index), input)
				}
			}
		}
	case "gemini":
		candidates, _ := root["candidates"].([]interface{})
		for candidateIndex, rawCandidate := range candidates {
			candidate, _ := rawCandidate.(map[string]interface{})
			content, _ := candidate["content"].(map[string]interface{})
			parts, _ := content["parts"].([]interface{})
			for partIndex, rawPart := range parts {
				part, _ := rawPart.(map[string]interface{})
				if call, ok := part["functionCall"].(map[string]interface{}); ok {
					appendImmutableJSONSegment(&segments, apiType, safetySourceToolArgument, fmt.Sprintf("candidates[%d].content.parts[%d].functionCall.args", candidateIndex, partIndex), call["args"])
				}
			}
		}
	}
	return segments
}

func appendGeminiTextParts(segments *[]SafetySegment, source, path string, rawParts interface{}) {
	parts, _ := rawParts.([]interface{})
	for index, rawPart := range parts {
		part, ok := rawPart.(map[string]interface{})
		if !ok {
			continue
		}
		appendMapTextField(segments, "gemini", source, fmt.Sprintf("%s[%d]", path, index), part)
	}
}

func appendChatMessageToolArguments(segments *[]SafetySegment, pathPrefix string, messageIndex int, message map[string]interface{}) {
	path := fmt.Sprintf("%s[%d]", pathPrefix, messageIndex)
	toolCalls, _ := message["tool_calls"].([]interface{})
	for callIndex, rawCall := range toolCalls {
		call, _ := rawCall.(map[string]interface{})
		function, _ := call["function"].(map[string]interface{})
		if arguments, exists := function["arguments"]; exists {
			appendImmutableJSONSegment(segments, "chat", safetySourceToolArgument,
				fmt.Sprintf("%s.tool_calls[%d].function.arguments", path, callIndex), arguments)
		}
	}
	if functionCall, ok := message["function_call"].(map[string]interface{}); ok {
		if arguments, exists := functionCall["arguments"]; exists {
			appendImmutableJSONSegment(segments, "chat", safetySourceToolArgument, path+".function_call.arguments", arguments)
		}
	}
}

func appendRootTextSegments(segments *[]SafetySegment, protocol, source, path string, root map[string]interface{}, key string) {
	value, exists := root[key]
	if !exists {
		return
	}
	appendKnownTextContent(segments, protocol, source, path, value, func(text string) {
		root[key] = text
	})
}

func appendKnownTextContent(segments *[]SafetySegment, protocol, source, path string, value interface{}, replaceString func(string)) {
	if text, ok := value.(string); ok {
		appendSafetySegment(segments, protocol, source, path, text, replaceString)
		return
	}
	blocks, _ := value.([]interface{})
	for index, rawBlock := range blocks {
		if text, ok := rawBlock.(string); ok {
			blockIndex := index
			appendSafetySegment(segments, protocol, source, fmt.Sprintf("%s[%d]", path, index), text, func(masked string) {
				blocks[blockIndex] = masked
			})
			continue
		}
		block, ok := rawBlock.(map[string]interface{})
		if !ok {
			continue
		}
		blockType := strings.ToLower(stringValue(block["type"]))
		if blockType == "" || blockType == "text" || blockType == "input_text" {
			appendMapTextField(segments, protocol, source, fmt.Sprintf("%s[%d]", path, index), block)
		}
	}
}

func appendMapTextField(segments *[]SafetySegment, protocol, source, path string, value map[string]interface{}) {
	for _, key := range []string{"text", "input_text"} {
		if text, ok := value[key].(string); ok {
			appendMapStringSegment(segments, protocol, source, path+"."+key, value, key, text)
			return
		}
	}
}

func appendMapStringSegment(segments *[]SafetySegment, protocol, source, path string, value map[string]interface{}, key, text string) {
	appendSafetySegment(segments, protocol, source, path, text, func(masked string) {
		value[key] = masked
	})
}

func appendSafetySegment(segments *[]SafetySegment, protocol, source, path, value string, replace func(string)) {
	if value == "" {
		return
	}
	*segments = append(*segments, SafetySegment{
		Protocol: protocol,
		Source:   source,
		Path:     path,
		Text:     value,
		Mutable:  replace != nil,
		replace:  replace,
	})
}

func appendImmutableJSONSegment(segments *[]SafetySegment, protocol, source, path string, value interface{}) {
	text, ok := value.(string)
	if !ok {
		body, err := json.Marshal(value)
		if err != nil {
			return
		}
		text = string(body)
	}
	appendSafetySegment(segments, protocol, source, path, text, nil)
}

func applyContentSafetySegments(
	ctx context.Context,
	metadata HookContext,
	segments []SafetySegment,
	snapshot *contentSafetySnapshot,
	recorder BlockedLogRecorder,
	prompts *[]string,
) (bool, error) {
	changed := false
	for index := range segments {
		segment := &segments[index]
		if segment.Source == safetySourceUser || segment.Source == safetySourceSystem {
			if match, ok := snapshot.words.FindFirst(segment.Text); ok {
				return changed, &ContentSafetyError{
					BlockType: sensitive.BlockTypeSensitiveWord,
					RuleName:  string(match.Category),
					Snippet:   safetyEventSnippet("block", *segment),
				}
			}
		}

		credentialMode := ""
		switch segment.Source {
		case safetySourceUser, safetySourceSystem:
			credentialMode = snapshot.settings.Credential.UserInputMode
		case safetySourceToolResult:
			credentialMode = snapshot.settings.Credential.ToolResultMode
		case safetySourceToolArgument:
			credentialMode = snapshot.settings.Credential.ToolArgumentMode
		}
		credentialMatches := snapshot.credential.FindAll(segment.Text)
		if len(credentialMatches) > 0 {
			if credentialMode == config.ContentSafetyModeBlock || !segment.Mutable && credentialMode == config.ContentSafetyModeMask {
				return changed, &ContentSafetyError{
					BlockType: sensitive.BlockTypeCredential,
					RuleName:  credentialMatches[0].Rule,
					Snippet:   safetyEventSnippet("block", *segment),
				}
			}
			if credentialMode == config.ContentSafetyModeMask {
				segment.Text, credentialMatches = snapshot.credential.Mask(segment.Text)
				changed = true
			}
			if err := recordSafetyMatches(ctx, metadata, recorder, sensitive.BlockTypeCredential, credentialMode, *segment, credentialRuleNames(credentialMatches)); err != nil {
				return changed, err
			}
		}

		if segment.Source == safetySourceUser || segment.Source == safetySourceSystem {
			infoMatches := snapshot.info.FindAll(segment.Text)
			if len(infoMatches) > 0 {
				mode := snapshot.settings.SensitiveInfo.Mode
				if mode == config.ContentSafetyModeBlock {
					return changed, &ContentSafetyError{
						BlockType: sensitive.BlockTypeSensitiveInfo,
						RuleName:  infoMatches[0].Rule,
						Snippet:   safetyEventSnippet("block", *segment),
					}
				}
				if mode == config.ContentSafetyModeMask {
					segment.Text, infoMatches = snapshot.info.Mask(segment.Text)
					changed = true
				}
				if err := recordSafetyMatches(ctx, metadata, recorder, sensitive.BlockTypeSensitiveInfo, mode, *segment, infoRuleNames(infoMatches)); err != nil {
					return changed, err
				}
			}
		}

		if segment.Mutable && segment.Text != "" {
			segment.replace(segment.Text)
		}
		if (segment.Source == safetySourceUser || segment.Source == safetySourceSystem) && strings.TrimSpace(segment.Text) != "" {
			*prompts = append(*prompts, segment.Text)
		}
	}
	return changed, nil
}

func recordSafetyMatches(ctx context.Context, metadata HookContext, recorder BlockedLogRecorder, blockType, mode string, segment SafetySegment, rules []string) error {
	if len(rules) == 0 || mode == "" || mode == config.ContentSafetyModeBlock {
		return nil
	}
	if recorder == nil {
		if mode == config.ContentSafetyModeAudit {
			return fmt.Errorf("内容安全审计模式未配置记录器")
		}
		return nil
	}
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if _, exists := seen[rule]; exists {
			continue
		}
		seen[rule] = struct{}{}
		contentHash := sha256.Sum256([]byte(segment.Text))
		eventKey := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%x", blockType, mode, segment.Source, segment.Path, rule, contentHash)
		if metadata.eventDeduper.contains(eventKey) {
			continue
		}
		if _, err := recorder.Record(ctx, sensitive.BlockedLog{
			APIType:       metadata.APIType,
			BlockType:     blockType,
			RuleName:      rule,
			PromptSnippet: safetyEventSnippet(mode, segment),
			ChannelName:   metadata.ChannelName,
			Model:         metadata.Model,
			RequestID:     metadata.RequestID,
		}); err != nil {
			return fmt.Errorf("写入内容安全%s记录失败: %w", mode, err)
		}
		metadata.eventDeduper.mark(eventKey)
	}
	return nil
}

func credentialRuleNames(matches []sensitive.CredentialMatch) []string {
	rules := make([]string, 0, len(matches))
	for _, match := range matches {
		rules = append(rules, match.Rule)
	}
	return rules
}

func infoRuleNames(matches []sensitive.InfoMatch) []string {
	rules := make([]string, 0, len(matches))
	for _, match := range matches {
		rules = append(rules, match.Rule)
	}
	return rules
}

func safetyEventSnippet(mode string, segment SafetySegment) string {
	return fmt.Sprintf("mode=%s source=%s path=%s", mode, segment.Source, segment.Path)
}

func stringValue(value interface{}) string {
	text, _ := value.(string)
	return text
}

func equalString(value interface{}, expected string) bool {
	return strings.EqualFold(stringValue(value), expected)
}
