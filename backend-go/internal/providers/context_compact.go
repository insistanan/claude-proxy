package providers

import (
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
)

// compactClaudeMessagesForUpstream delegates to shared utils implementation.
func compactClaudeMessagesForUpstream(messages []types.ClaudeMessage) []types.ClaudeMessage {
	return utils.CompactClaudeMessagesForUpstream(messages)
}

func compactClaudeMessagesForUpstreamWithLimits(
	messages []types.ClaudeMessage,
	keepRecentToolResults int,
	recentMaxRunes int,
	oldMaxRunes int,
) []types.ClaudeMessage {
	return utils.CompactClaudeMessagesForUpstreamWithLimits(messages, keepRecentToolResults, recentMaxRunes, oldMaxRunes)
}
