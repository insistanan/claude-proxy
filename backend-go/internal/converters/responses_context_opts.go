package converters

import (
	"encoding/json"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/types"
)

// extractResponsesReasoningText pulls text from a Responses reasoning item.
func extractResponsesReasoningText(item types.ResponsesItem) string {
	parts := make([]string, 0)
	if item.Summary != nil {
		switch summary := item.Summary.(type) {
		case []interface{}:
			for _, raw := range summary {
				block, ok := raw.(map[string]interface{})
				if !ok {
					continue
				}
				if text, ok := block["text"].(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, strings.TrimSpace(text))
				}
			}
		case []map[string]interface{}:
			for _, block := range summary {
				if text, ok := block["text"].(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, strings.TrimSpace(text))
				}
			}
		}
	}
	if item.Content != nil {
		switch content := item.Content.(type) {
		case string:
			if strings.TrimSpace(content) != "" {
				parts = append(parts, strings.TrimSpace(content))
			}
		case []interface{}:
			for _, raw := range content {
				block, ok := raw.(map[string]interface{})
				if !ok {
					continue
				}
				if text, ok := block["text"].(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, strings.TrimSpace(text))
				}
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// mergeClaudeHistoryMessagesDedup avoids double history when client input already contains session prefix.
func mergeClaudeHistoryMessagesDedup(historyMessages []types.ClaudeMessage, currentMessages []types.ClaudeMessage) []types.ClaudeMessage {
	if len(historyMessages) == 0 {
		return currentMessages
	}
	if len(currentMessages) == 0 {
		return historyMessages
	}

	overlap := 0
	maxCheck := len(historyMessages)
	if len(currentMessages) < maxCheck {
		maxCheck = len(currentMessages)
	}
	for candidate := maxCheck; candidate > 0; candidate-- {
		if claudeMessagePrefixEqual(historyMessages[:candidate], currentMessages[:candidate]) {
			overlap = candidate
			break
		}
	}

	if overlap > 0 && overlap == len(historyMessages) {
		// Current fully includes history: use current only.
		return currentMessages
	}
	if overlap > 0 {
		out := make([]types.ClaudeMessage, 0, len(historyMessages)+len(currentMessages)-overlap)
		out = append(out, historyMessages...)
		out = append(out, currentMessages[overlap:]...)
		return out
	}

	out := make([]types.ClaudeMessage, 0, len(historyMessages)+len(currentMessages))
	out = append(out, historyMessages...)
	out = append(out, currentMessages...)
	return out
}

func claudeMessagePrefixEqual(left []types.ClaudeMessage, right []types.ClaudeMessage) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Role != right[index].Role {
			return false
		}
		leftRaw, _ := json.Marshal(left[index].Content)
		rightRaw, _ := json.Marshal(right[index].Content)
		if string(leftRaw) != string(rightRaw) {
			return false
		}
	}
	return true
}

// responsesItemToOpenAIMessageWithOptions converts a Responses item to Chat message.
// includeHistoryThinking materializes type=reasoning as assistant text; default false skips it.
func responsesItemToOpenAIMessageWithOptions(item types.ResponsesItem, includeHistoryThinking bool) map[string]interface{} {
	if item.Type == "reasoning" {
		if !includeHistoryThinking {
			return nil
		}
		text := extractResponsesReasoningText(item)
		if text == "" {
			return nil
		}
		return map[string]interface{}{
			"role":    "assistant",
			"content": "[thinking]\n" + text,
		}
	}
	// Reuse default conversion for non-reasoning items.
	return responsesItemToOpenAIMessage(item)
}
