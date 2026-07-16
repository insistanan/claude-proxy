package common

import (
	"log"

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
	return ObserveConversationPrompts(channelScheduler, kind, conversationID, model, []string{firstPrompt}, nil, stream)
}

func ObserveConversationPrompts(
	channelScheduler *scheduler.ChannelScheduler,
	kind scheduler.ChannelKind,
	conversationID string,
	model string,
	prompts []string,
	imageFingerprints []string,
	stream bool,
) string {
	return ObserveConversationRequest(
		channelScheduler,
		kind,
		conversation.Identity{ExplicitID: conversationID, Source: "legacy_explicit"},
		conversation.Transcript{},
		model,
		prompts,
		imageFingerprints,
		stream,
	)
}

func ObserveConversationRequest(
	channelScheduler *scheduler.ChannelScheduler,
	kind scheduler.ChannelKind,
	identity conversation.Identity,
	transcript conversation.Transcript,
	model string,
	prompts []string,
	imageFingerprints []string,
	stream bool,
) string {
	if channelScheduler == nil {
		return identity.ExplicitID
	}
	registry := channelScheduler.GetConversationRegistry()
	if registry == nil {
		return identity.ExplicitID
	}

	firstPrompt := ""
	if len(prompts) > 0 {
		firstPrompt = prompts[0]
	}
	record := registry.ObserveRequest(conversation.Observation{
		APIKind:           string(kind),
		Model:             model,
		Stream:            stream,
		ConversationID:    identity.ExplicitID,
		Identity:          identity,
		Transcript:        transcript,
		FirstPrompt:       firstPrompt,
		Prompts:           prompts,
		ImageFingerprints: imageFingerprints,
	})
	if record == nil {
		return identity.ExplicitID
	}
	return record.ID
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

func AssociateConversationExternalID(channelScheduler *scheduler.ChannelScheduler, conversationID string, kind scheduler.ChannelKind, externalID string) {
	if channelScheduler == nil || conversationID == "" || externalID == "" {
		return
	}
	registry := channelScheduler.GetConversationRegistry()
	if registry == nil {
		return
	}
	if err := registry.AssociateExternalID(conversationID, string(kind), externalID); err != nil {
		log.Printf("[Conversation] 关联外部对话 ID 失败: %v", err)
	}
}
