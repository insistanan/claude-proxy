package utils

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/BenedictKing/claude-proxy/internal/types"
)

// Default context compact limits for full-history clients with large tool outputs.
// Only mutates messages sent upstream; client session / request logs stay original.
//
// Claude clients (Cursor / Claude Code) replay full history every turn.
// Truncating tool_result alone is not enough for long sessions (logs: 17k -> 184k).
// Layered policy:
//  1) sliding message window (tool-pair safe)
//  2) tool_result truncation (recent vs old)
//  3) tool_use argument truncation
//  4) old plain-text block truncation
const (
	// defaultKeepRecentToolResults keeps the last N tool_result items more complete.
	defaultKeepRecentToolResults = 1
	// defaultRecentToolResultMaxRunes is the hard cap for recent tool_result content.
	defaultRecentToolResultMaxRunes = 1200
	// defaultOldToolResultMaxRunes is the hard cap for older tool_result content.
	defaultOldToolResultMaxRunes = 80
	// defaultKeepRecentMessages keeps last N Claude messages after pair-safe windowing.
	// System is separate. Keep a short window so agent tool loops cannot grow without bound.
	defaultKeepRecentMessages = 8
	// defaultOldToolUseArgsMaxRunes caps older tool_use input JSON.
	defaultOldToolUseArgsMaxRunes = 200
	// defaultRecentToolUseArgsMaxRunes caps recent tool_use input JSON.
	defaultRecentToolUseArgsMaxRunes = 1000
	// defaultOldTextBlockMaxRunes caps text in older messages inside the window.
	defaultOldTextBlockMaxRunes = 500
	// defaultRecentTextBlockMaxRunes soft-caps text in recent half of the window.
	defaultRecentTextBlockMaxRunes = 2500
)

type compactBlockRef struct {
	messageIndex int
	blockIndex   int
}

// CompactClaudeMessagesForUpstream compresses historical messages before
// Messages/Responses conversion to Chat/Claude/Responses upstreams.
func CompactClaudeMessagesForUpstream(messages []types.ClaudeMessage) []types.ClaudeMessage {
	return CompactClaudeMessagesForUpstreamWithLimits(
		messages,
		defaultKeepRecentToolResults,
		defaultRecentToolResultMaxRunes,
		defaultOldToolResultMaxRunes,
	)
}

// CompactClaudeMessagesForUpstreamWithLimits is the parameterized form of
// CompactClaudeMessagesForUpstream for tests and custom tool_result limits.
// Message-window / tool_use / text limits use package defaults (stable policy).
func CompactClaudeMessagesForUpstreamWithLimits(
	messages []types.ClaudeMessage,
	keepRecentToolResults int,
	recentMaxRunes int,
	oldMaxRunes int,
) []types.ClaudeMessage {
	if len(messages) == 0 {
		return messages
	}
	if keepRecentToolResults < 0 {
		keepRecentToolResults = 0
	}
	if recentMaxRunes <= 0 {
		recentMaxRunes = defaultRecentToolResultMaxRunes
	}
	if oldMaxRunes <= 0 {
		oldMaxRunes = defaultOldToolResultMaxRunes
	}
	if oldMaxRunes > recentMaxRunes {
		oldMaxRunes = recentMaxRunes
	}

	// Layer 1: sliding window so multi-turn full-history clients stop linear growth.
	windowed := applyClaudeMessageSlidingWindow(messages, defaultKeepRecentMessages)

	var toolResultRefs []compactBlockRef
	var toolUseRefs []compactBlockRef
	for messageIndex, message := range windowed {
		blocks := NormalizeContentBlocks(message.Content)
		for blockIndex, block := range blocks {
			blockType, _ := block["type"].(string)
			switch blockType {
			case "tool_result":
				toolResultRefs = append(toolResultRefs, compactBlockRef{messageIndex: messageIndex, blockIndex: blockIndex})
			case "tool_use":
				toolUseRefs = append(toolUseRefs, compactBlockRef{messageIndex: messageIndex, blockIndex: blockIndex})
			}
		}
	}

	recentToolResultSet := buildRecentBlockKeySet(toolResultRefs, keepRecentToolResults)
	recentToolUseSet := buildRecentBlockKeySet(toolUseRefs, keepRecentToolResults)

	// Within the window, only the latter half is treated as "recent" for text caps.
	recentMessageStart := 0
	if len(windowed) > 1 {
		recentMessageStart = len(windowed) / 2
	}

	out := make([]types.ClaudeMessage, len(windowed))
	for messageIndex, message := range windowed {
		isRecentMessage := messageIndex >= recentMessageStart
		blocks := NormalizeContentBlocks(message.Content)
		if len(blocks) == 0 {
			if str, ok := message.Content.(string); ok {
				maxRunes := defaultOldTextBlockMaxRunes
				if isRecentMessage {
					maxRunes = defaultRecentTextBlockMaxRunes
				}
				truncated, did := TruncateTextKeepHeadTail(str, maxRunes)
				if did {
					out[messageIndex] = types.ClaudeMessage{Role: message.Role, Content: truncated}
					continue
				}
			}
			out[messageIndex] = message
			continue
		}

		changed := false
		newBlocks := make([]map[string]interface{}, 0, len(blocks))
		for blockIndex, block := range blocks {
			copiedBlock := CloneStringInterfaceMap(block)
			blockType, _ := copiedBlock["type"].(string)
			key := fmt.Sprintf("%d:%d", messageIndex, blockIndex)

			switch blockType {
			case "tool_result":
				maxRunes := oldMaxRunes
				if _, isRecent := recentToolResultSet[key]; isRecent {
					maxRunes = recentMaxRunes
				} else {
					// Non-recent tool outputs are the main agent context bloat source.
					// Collapse them to a short stub instead of keeping hundreds of runes each.
					maxRunes = 80
				}
				truncatedContent, didTruncate := TruncateToolResultContent(copiedBlock["content"], maxRunes)
				if didTruncate {
					copiedBlock["content"] = truncatedContent
					changed = true
				}
			case "tool_use":
				maxRunes := defaultOldToolUseArgsMaxRunes
				if _, isRecent := recentToolUseSet[key]; isRecent {
					maxRunes = defaultRecentToolUseArgsMaxRunes
				}
				if truncatedInput, did := truncateToolUseInput(copiedBlock["input"], maxRunes); did {
					copiedBlock["input"] = truncatedInput
					changed = true
				}
			case "text":
				maxRunes := defaultOldTextBlockMaxRunes
				if isRecentMessage {
					maxRunes = defaultRecentTextBlockMaxRunes
				}
				if text, ok := copiedBlock["text"].(string); ok {
					truncated, did := TruncateTextKeepHeadTail(text, maxRunes)
					if did {
						copiedBlock["text"] = truncated
						changed = true
					}
				}
			case "thinking", "redacted_thinking":
				// Drop history thinking from upstream payload. Cursor stores thinking
				// blocks and re-sends them; replaying multi-turn reasoning explodes occupancy.
				changed = true
				continue
			}
			newBlocks = append(newBlocks, copiedBlock)
		}

		if !changed {
			out[messageIndex] = message
			continue
		}
		out[messageIndex] = types.ClaudeMessage{
			Role:    message.Role,
			Content: newBlocks,
		}
	}
	return out
}

func buildRecentBlockKeySet(refs []compactBlockRef, keepRecent int) map[string]struct{} {
	recentSet := make(map[string]struct{})
	if keepRecent <= 0 || len(refs) == 0 {
		return recentSet
	}
	recentStart := 0
	if len(refs) > keepRecent {
		recentStart = len(refs) - keepRecent
	}
	for refIndex := recentStart; refIndex < len(refs); refIndex++ {
		ref := refs[refIndex]
		recentSet[fmt.Sprintf("%d:%d", ref.messageIndex, ref.blockIndex)] = struct{}{}
	}
	return recentSet
}

// applyClaudeMessageSlidingWindow keeps the last keepCount messages, but expands
// backward so we do not split an assistant tool_use from its following user tool_result.
// When the head is dropped, a short notice message is prepended for the model.
func applyClaudeMessageSlidingWindow(messages []types.ClaudeMessage, keepCount int) []types.ClaudeMessage {
	if keepCount <= 0 || len(messages) <= keepCount {
		return messages
	}

	startIndex := len(messages) - keepCount
	// Expand left while the window starts mid tool-call pair:
	// user tool_result without the preceding assistant tool_use would break pairing.
	for startIndex > 0 {
		if !messageStartsWithOrphanToolResult(messages[startIndex]) {
			break
		}
		startIndex--
		// Do not grow far beyond the window; orphan tool pairs may expand a little.
		if len(messages)-startIndex > keepCount+4 {
			break
		}
	}

	if startIndex <= 0 {
		return messages
	}

	notice := types.ClaudeMessage{
		Role: "user",
		Content: []map[string]interface{}{
			{
				"type": "text",
				"text": fmt.Sprintf(
					"[context compacted by claude-proxy: dropped %d older messages to limit context growth]",
					startIndex,
				),
			},
		},
	}
	out := make([]types.ClaudeMessage, 0, 1+len(messages)-startIndex)
	out = append(out, notice)
	out = append(out, messages[startIndex:]...)
	return out
}

func messageStartsWithOrphanToolResult(message types.ClaudeMessage) bool {
	if message.Role != "user" && message.Role != "tool" {
		return false
	}
	blocks := NormalizeContentBlocks(message.Content)
	if len(blocks) == 0 {
		return false
	}
	// If the first block is tool_result, this message is almost certainly the
	// second half of a tool pair and should not be the window head alone.
	blockType, _ := blocks[0]["type"].(string)
	return blockType == "tool_result"
}

func truncateToolUseInput(input interface{}, maxRunes int) (interface{}, bool) {
	if maxRunes <= 0 || input == nil {
		return input, false
	}
	raw, err := json.Marshal(input)
	if err != nil {
		text := fmt.Sprint(input)
		truncated, did := TruncateTextKeepHeadTail(text, maxRunes)
		if !did {
			return input, false
		}
		return map[string]interface{}{
			"_truncated": true,
			"preview":   truncated,
		}, true
	}
	if utf8.RuneCountInString(string(raw)) <= maxRunes {
		return input, false
	}
	truncated, _ := TruncateTextKeepHeadTail(string(raw), maxRunes)
	// Keep as a small object so JSON tools still see structured input.
	return map[string]interface{}{
		"_truncated": true,
		"preview":   truncated,
	}, true
}

// CompactOpenAIChatMessagesForUpstream truncates long tool-role message content
// for Responses -> OpenAI Chat upstream requests (same policy as Claude tool_result),
// and applies a sliding window on the message list.
func CompactOpenAIChatMessagesForUpstream(messages []interface{}) []interface{} {
	return CompactOpenAIChatMessagesForUpstreamWithLimits(
		messages,
		defaultKeepRecentToolResults,
		defaultRecentToolResultMaxRunes,
		defaultOldToolResultMaxRunes,
	)
}

// CompactOpenAIChatMessagesForUpstreamWithLimits is the parameterized form for tests.
func CompactOpenAIChatMessagesForUpstreamWithLimits(
	messages []interface{},
	keepRecentToolResults int,
	recentMaxRunes int,
	oldMaxRunes int,
) []interface{} {
	if len(messages) == 0 {
		return messages
	}
	if keepRecentToolResults < 0 {
		keepRecentToolResults = 0
	}
	if recentMaxRunes <= 0 {
		recentMaxRunes = defaultRecentToolResultMaxRunes
	}
	if oldMaxRunes <= 0 {
		oldMaxRunes = defaultOldToolResultMaxRunes
	}
	if oldMaxRunes > recentMaxRunes {
		oldMaxRunes = recentMaxRunes
	}

	// Preserve leading system message(s), window the rest.
	messages = applyOpenAIChatSlidingWindow(messages, defaultKeepRecentMessages)

	toolIndexes := make([]int, 0)
	for messageIndex, rawMessage := range messages {
		message, ok := rawMessage.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		if role == "tool" {
			toolIndexes = append(toolIndexes, messageIndex)
		}
	}
	if len(toolIndexes) == 0 {
		return messages
	}

	recentStart := 0
	if len(toolIndexes) > keepRecentToolResults {
		recentStart = len(toolIndexes) - keepRecentToolResults
	}
	recentSet := make(map[int]struct{}, keepRecentToolResults)
	for refIndex := recentStart; refIndex < len(toolIndexes); refIndex++ {
		recentSet[toolIndexes[refIndex]] = struct{}{}
	}

	out := make([]interface{}, len(messages))
	for messageIndex, rawMessage := range messages {
		message, ok := rawMessage.(map[string]interface{})
		if !ok {
			out[messageIndex] = rawMessage
			continue
		}
		role, _ := message["role"].(string)
		if role != "tool" {
			out[messageIndex] = rawMessage
			continue
		}
		maxRunes := oldMaxRunes
		if _, isRecent := recentSet[messageIndex]; isRecent {
			maxRunes = recentMaxRunes
		}
		truncatedContent, didTruncate := TruncateToolResultContent(message["content"], maxRunes)
		if !didTruncate {
			out[messageIndex] = rawMessage
			continue
		}
		copied := CloneStringInterfaceMap(message)
		copied["content"] = truncatedContent
		out[messageIndex] = copied
	}
	return out
}

func applyOpenAIChatSlidingWindow(messages []interface{}, keepCount int) []interface{} {
	if keepCount <= 0 || len(messages) <= keepCount {
		return messages
	}

	// Keep all leading system messages outside the window budget.
	systemPrefix := 0
	for systemPrefix < len(messages) {
		message, ok := messages[systemPrefix].(map[string]interface{})
		if !ok {
			break
		}
		role, _ := message["role"].(string)
		if role != "system" {
			break
		}
		systemPrefix++
	}

	body := messages[systemPrefix:]
	if len(body) <= keepCount {
		return messages
	}

	startIndex := len(body) - keepCount
	// Expand left if we would start on a lone tool message.
	for startIndex > 0 {
		message, ok := body[startIndex].(map[string]interface{})
		if !ok {
			break
		}
		role, _ := message["role"].(string)
		if role != "tool" {
			break
		}
		startIndex--
		if len(body)-startIndex > keepCount*2 {
			break
		}
	}

	if startIndex <= 0 {
		return messages
	}

	notice := map[string]interface{}{
		"role": "user",
		"content": fmt.Sprintf(
			"[context compacted by claude-proxy: dropped %d older messages to limit context growth]",
			startIndex,
		),
	}
	out := make([]interface{}, 0, systemPrefix+1+len(body)-startIndex)
	out = append(out, messages[:systemPrefix]...)
	out = append(out, notice)
	out = append(out, body[startIndex:]...)
	return out
}

// CloneStringInterfaceMap shallow-copies a string-keyed map.
func CloneStringInterfaceMap(source map[string]interface{}) map[string]interface{} {
	if source == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

// TruncateToolResultContent truncates tool_result / tool content by rune budget,
// preferring head+tail retention for easier debugging. Returns (new, truncated?).
func TruncateToolResultContent(content interface{}, maxRunes int) (interface{}, bool) {
	if maxRunes <= 0 || content == nil {
		return content, false
	}

	switch typed := content.(type) {
	case string:
		truncated, did := TruncateTextKeepHeadTail(typed, maxRunes)
		return truncated, did
	case []interface{}:
		// content may be a content-block array (Claude multi-part tool_result)
		totalRunes := EstimateInterfaceRunes(typed)
		if totalRunes <= maxRunes {
			return content, false
		}
		// Prefer truncating nested text fields; fall back to JSON string truncation.
		changed := false
		outBlocks := make([]interface{}, 0, len(typed))
		remaining := maxRunes
		for _, item := range typed {
			block, ok := item.(map[string]interface{})
			if !ok {
				outBlocks = append(outBlocks, item)
				continue
			}
			copied := CloneStringInterfaceMap(block)
			if text, ok := copied["text"].(string); ok && remaining >= 0 {
				truncated, did := TruncateTextKeepHeadTail(text, remaining)
				if did {
					copied["text"] = truncated
					changed = true
				}
				remaining -= utf8.RuneCountInString(truncated)
				if remaining < 0 {
					remaining = 0
				}
			}
			outBlocks = append(outBlocks, copied)
		}
		if changed {
			return outBlocks, true
		}
		raw, err := json.Marshal(typed)
		if err != nil {
			text := fmt.Sprint(typed)
			truncated, did := TruncateTextKeepHeadTail(text, maxRunes)
			return truncated, did
		}
		truncated, did := TruncateTextKeepHeadTail(string(raw), maxRunes)
		return truncated, did
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			text := fmt.Sprint(typed)
			truncated, did := TruncateTextKeepHeadTail(text, maxRunes)
			return truncated, did
		}
		truncated, did := TruncateTextKeepHeadTail(string(raw), maxRunes)
		return truncated, did
	}
}

// EstimateInterfaceRunes estimates rune count of nested content for budget checks.
func EstimateInterfaceRunes(value interface{}) int {
	switch typed := value.(type) {
	case string:
		return utf8.RuneCountInString(typed)
	case []interface{}:
		total := 0
		for _, item := range typed {
			total += EstimateInterfaceRunes(item)
		}
		return total
	case map[string]interface{}:
		if text, ok := typed["text"].(string); ok {
			return utf8.RuneCountInString(text)
		}
		raw, err := json.Marshal(typed)
		if err != nil {
			return utf8.RuneCountInString(fmt.Sprint(typed))
		}
		return utf8.RuneCountInString(string(raw))
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return utf8.RuneCountInString(fmt.Sprint(typed))
		}
		return utf8.RuneCountInString(string(raw))
	}
}

// TruncateTextKeepHeadTail keeps head and tail, inserting a truncation marker in the middle.
func TruncateTextKeepHeadTail(text string, maxRunes int) (string, bool) {
	if maxRunes <= 0 {
		return text, false
	}
	runeCount := utf8.RuneCountInString(text)
	if runeCount <= maxRunes {
		return text, false
	}

	// Tiny budgets: keep prefix only.
	if maxRunes < 80 {
		return TakeRunes(text, maxRunes) + "…", true
	}

	marker := fmt.Sprintf("\n…[truncated by claude-proxy: original %d chars]…\n", runeCount)
	markerRunes := utf8.RuneCountInString(marker)
	budget := maxRunes - markerRunes
	if budget < 16 {
		return TakeRunes(text, maxRunes) + "…", true
	}

	headRunes := budget * 2 / 3
	tailRunes := budget - headRunes
	if tailRunes < 8 {
		tailRunes = 8
		headRunes = budget - tailRunes
	}
	if headRunes < 8 {
		headRunes = 8
		tailRunes = budget - headRunes
	}

	head := TakeRunes(text, headRunes)
	tail := TakeLastRunes(text, tailRunes)
	return head + marker + tail, true
}

// TakeRunes returns the first count runes of text.
func TakeRunes(text string, count int) string {
	if count <= 0 {
		return ""
	}
	runes := []rune(text)
	if count >= len(runes) {
		return text
	}
	return string(runes[:count])
}

// TakeLastRunes returns the last count runes of text.
func TakeLastRunes(text string, count int) string {
	if count <= 0 {
		return ""
	}
	runes := []rune(text)
	if count >= len(runes) {
		return text
	}
	return string(runes[len(runes)-count:])
}
