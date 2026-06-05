package common

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
)

type RequestSource struct {
	ClientName    string
	ClientVersion string
	SessionID     string
	RequestID     string
	Confidence    string
}

func DetectRequestSource(c *gin.Context, bodyBytes []byte, fallbackSessionID string) RequestSource {
	source := RequestSource{
		ClientName: "unknown",
		SessionID:  fallbackSessionID,
		Confidence: "low",
	}
	if c == nil || c.Request == nil {
		return source
	}

	userAgent := c.GetHeader("User-Agent")
	lowerUA := strings.ToLower(userAgent)
	allHeaders := strings.ToLower(strings.Join([]string{
		userAgent,
		c.GetHeader("X-Client-Name"),
		c.GetHeader("X-Client-Version"),
		c.GetHeader("X-Claude-Code-Session-Id"),
	}, " "))

	source.ClientVersion = firstNonEmpty(
		c.GetHeader("X-Client-Version"),
		extractVersionFromUserAgent(userAgent),
	)
	source.RequestID = firstNonEmpty(
		c.GetHeader("X-Request-Id"),
		c.GetHeader("X-Client-Request-Id"),
		c.GetHeader("Request-Id"),
	)
	source.SessionID = firstNonEmpty(
		c.GetHeader("X-Claude-Code-Session-Id"),
		c.GetHeader("Conversation_id"),
		c.GetHeader("Session_id"),
		c.GetHeader("X-Gemini-Api-Privileged-User-Id"),
		extractBodySessionID(bodyBytes),
		fallbackSessionID,
	)

	if explicit := strings.TrimSpace(c.GetHeader("X-Client-Name")); explicit != "" {
		source.ClientName = strings.ToLower(explicit)
		source.Confidence = "high"
		return source
	}

	switch {
	case strings.Contains(allHeaders, "claude-code") || c.GetHeader("X-Claude-Code-Session-Id") != "":
		source.ClientName = "claude-code"
		source.Confidence = "high"
	case strings.Contains(lowerUA, "cursor"):
		source.ClientName = "cursor"
		source.Confidence = "high"
	case strings.Contains(lowerUA, "codex") || strings.Contains(allHeaders, "openai-codex"):
		source.ClientName = "codex-cli"
		source.Confidence = "high"
	case strings.Contains(lowerUA, "claude"):
		source.ClientName = "claude"
		source.Confidence = "medium"
	case strings.Contains(lowerUA, "openai"):
		source.ClientName = "openai-compatible"
		source.Confidence = "medium"
	}

	return source
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func extractVersionFromUserAgent(userAgent string) string {
	parts := strings.Fields(userAgent)
	for _, part := range parts {
		if idx := strings.Index(part, "/"); idx > 0 && idx < len(part)-1 {
			return part[idx+1:]
		}
	}
	return ""
}

func extractBodySessionID(bodyBytes []byte) string {
	if len(bodyBytes) == 0 {
		return ""
	}

	var data struct {
		PromptCacheKey string `json:"prompt_cache_key"`
		Metadata       struct {
			UserID         string `json:"user_id"`
			SessionID      string `json:"session_id"`
			ConversationID string `json:"conversation_id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		return ""
	}

	return firstNonEmpty(data.Metadata.SessionID, data.Metadata.ConversationID, data.PromptCacheKey, data.Metadata.UserID)
}
