package common

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/conversation"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
)

func ObserveConversation(
	channelScheduler *scheduler.ChannelScheduler,
	kind scheduler.ChannelKind,
	conversationID string,
	model string,
	firstPrompt string,
	stream bool,
) string {
	return ObserveConversationPrompts(channelScheduler, kind, conversationID, model, []string{firstPrompt}, stream)
}

func ObserveConversationPrompts(
	channelScheduler *scheduler.ChannelScheduler,
	kind scheduler.ChannelKind,
	conversationID string,
	model string,
	prompts []string,
	stream bool,
) string {
	if channelScheduler == nil {
		return conversationID
	}
	registry := channelScheduler.GetConversationRegistry()
	if registry == nil {
		return conversationID
	}

	firstPrompt := ""
	if len(prompts) > 0 {
		firstPrompt = prompts[0]
	}
	fallbackKey := buildConversationFallbackKey(model, firstPrompt)
	record := registry.ObserveRequest(conversation.Observation{
		APIKind:        string(kind),
		Model:          model,
		Stream:         stream,
		ConversationID: conversationID,
		FallbackKey:    fallbackKey,
		FirstPrompt:    firstPrompt,
		Prompts:        prompts,
	})
	if record == nil {
		return conversationID
	}
	return record.ID
}

func buildConversationFallbackKey(model string, firstPrompt string) string {
	model = strings.TrimSpace(model)
	firstPrompt = strings.TrimSpace(firstPrompt)
	if model == "" && firstPrompt == "" {
		return ""
	}
	sum := sha1.Sum([]byte(model + "\n" + firstPrompt))
	return "fallback_" + hex.EncodeToString(sum[:8])
}

func MarkConversationSuccess(channelScheduler *scheduler.ChannelScheduler, conversationID string, kind scheduler.ChannelKind, channelIndex int, channelName string) {
	if channelScheduler == nil || conversationID == "" {
		return
	}
	channelScheduler.MarkConversationSuccess(conversationID, kind, channelIndex, channelName)
}

func MarkConversationComplete(channelScheduler *scheduler.ChannelScheduler, conversationID string, kind scheduler.ChannelKind) {
	if channelScheduler == nil || conversationID == "" {
		return
	}
	channelScheduler.MarkConversationComplete(conversationID, kind)
}

func MarkConversationFailure(channelScheduler *scheduler.ChannelScheduler, conversationID string, kind scheduler.ChannelKind, err error) {
	if channelScheduler == nil || conversationID == "" || err == nil {
		return
	}
	channelScheduler.MarkConversationFailure(conversationID, kind, err.Error())
}
