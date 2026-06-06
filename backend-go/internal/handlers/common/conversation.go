package common

import (
	"github.com/BenedictKing/claude-proxy/internal/conversation"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
)

func ObserveConversation(
	channelScheduler *scheduler.ChannelScheduler,
	kind scheduler.ChannelKind,
	conversationID string,
	model string,
	stream bool,
) string {
	if channelScheduler == nil || conversationID == "" {
		return conversationID
	}
	registry := channelScheduler.GetConversationRegistry()
	if registry == nil {
		return conversationID
	}

	record := registry.ObserveRequest(conversation.Observation{
		APIKind:       string(kind),
		Model:         model,
		Stream:        stream,
		ConversationID: conversationID,
		FallbackKey:   conversationID,
	})
	if record == nil {
		return conversationID
	}
	return record.ID
}

func MarkConversationSuccess(channelScheduler *scheduler.ChannelScheduler, conversationID string, kind scheduler.ChannelKind, channelIndex int, channelName string) {
	if channelScheduler == nil || conversationID == "" {
		return
	}
	channelScheduler.MarkConversationSuccess(conversationID, kind, channelIndex, channelName)
}

func MarkConversationFailure(channelScheduler *scheduler.ChannelScheduler, conversationID string, kind scheduler.ChannelKind, err error) {
	if channelScheduler == nil || conversationID == "" || err == nil {
		return
	}
	channelScheduler.MarkConversationFailure(conversationID, kind, err.Error())
}
