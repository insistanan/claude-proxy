package scheduler

import (
	"fmt"

	"github.com/BenedictKing/claude-proxy/internal/urlhealth"
)

func (s *ChannelScheduler) SetTraceAffinityForKind(kind ChannelKind, userID string, channelIndex int) {
	if userID != "" {
		s.traceAffinity.SetPreferredChannelForKind(string(kind), userID, channelIndex)
	}
}

// GetPreferredBaseURL 获取会话粘滞的 BaseURL（用于 prompt cache 亲和）。
func (s *ChannelScheduler) GetPreferredBaseURL(sessionKey string) (string, bool) {
	if s == nil || s.baseURLAffinity == nil {
		return "", false
	}
	return s.baseURLAffinity.GetPreferredBaseURL(sessionKey)
}

// SetPreferredBaseURL 记录会话最近成功的 BaseURL。
func (s *ChannelScheduler) SetPreferredBaseURL(sessionKey string, baseURL string) {
	if s == nil || s.baseURLAffinity == nil {
		return
	}
	s.baseURLAffinity.SetPreferredBaseURL(sessionKey, baseURL)
}

// PreferBaseURLInResults 将会话偏好的 BaseURL 排到候选列表最前（保持其余相对顺序）。
func PreferBaseURLInResults(results []urlhealth.URLLatencyResult, preferred string) []urlhealth.URLLatencyResult {
	if preferred == "" || len(results) <= 1 {
		return results
	}
	preferredIdx := -1
	for i, result := range results {
		if result.URL == preferred {
			preferredIdx = i
			break
		}
	}
	if preferredIdx <= 0 {
		return results
	}
	reordered := make([]urlhealth.URLLatencyResult, 0, len(results))
	reordered = append(reordered, results[preferredIdx])
	reordered = append(reordered, results[:preferredIdx]...)
	reordered = append(reordered, results[preferredIdx+1:]...)
	return reordered
}

// ConsumePromotionCount 消费促销请求次数
// 在请求成功后调用，递减促销计数，到 0 时自动清除促销状态
func (s *ChannelScheduler) ConsumePromotionCount(channelIndex int, kind ChannelKind) {
	channelType := "messages"
	switch kind {
	case ChannelKindResponses:
		channelType = "responses"
	case ChannelKindGemini:
		channelType = "gemini"
	case ChannelKindChat:
		channelType = "chat"
	case ChannelKindImages:
		channelType = "images"
	}
	s.configManager.ConsumePromotionCount(channelIndex, channelType)
}

func (s *ChannelScheduler) ValidateFixedChannel(userID string, kind ChannelKind, channelIndex int) error {
	if userID == "" || s == nil {
		return nil
	}
	s.mu.RLock()
	registry := s.conversationRegistry
	s.mu.RUnlock()
	if registry == nil {
		return nil
	}
	override, ok := registry.GetRouteOverride(userID)
	if !ok {
		return nil
	}
	if override.Kind != string(kind) {
		return fmt.Errorf("该对话已固定到 %s 渠道池，当前请求为 %s", override.Kind, kind)
	}
	if override.ChannelIndex != channelIndex {
		return fmt.Errorf("该对话已固定到渠道 [%d]，当前命中的渠道为 [%d]", override.ChannelIndex, channelIndex)
	}
	return nil
}

func (s *ChannelScheduler) MarkConversationSuccess(userID string, kind ChannelKind, channelIndex int, channelName string) {
	if userID == "" || s == nil {
		return
	}
	s.mu.RLock()
	registry := s.conversationRegistry
	s.mu.RUnlock()
	if registry == nil {
		return
	}
	registry.MarkSuccess(userID, string(kind), channelIndex, channelName)
}

func (s *ChannelScheduler) MarkConversationAttempt(userID string, kind ChannelKind, channelIndex int, channelName string, requestedModel string, resolvedModel string, stream bool) {
	if userID == "" || s == nil {
		return
	}
	s.mu.RLock()
	registry := s.conversationRegistry
	s.mu.RUnlock()
	if registry == nil {
		return
	}
	registry.MarkAttempt(userID, string(kind), channelIndex, channelName, requestedModel, resolvedModel, stream)
}

func (s *ChannelScheduler) MarkConversationFailure(userID string, kind ChannelKind, errorMessage string) {
	if userID == "" || s == nil {
		return
	}
	s.mu.RLock()
	registry := s.conversationRegistry
	s.mu.RUnlock()
	if registry == nil {
		return
	}
	registry.MarkFailure(userID, string(kind), errorMessage)
}

func (s *ChannelScheduler) MarkConversationComplete(userID string, kind ChannelKind) {
	if userID == "" || s == nil {
		return
	}
	s.mu.RLock()
	registry := s.conversationRegistry
	s.mu.RUnlock()
	if registry == nil {
		return
	}
	registry.MarkComplete(userID, string(kind))
}
