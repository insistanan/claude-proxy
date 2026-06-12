package handlers

import (
	"net/http"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/conversation"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/gin-gonic/gin"
)

type routeOverrideRequest struct {
	Kind         string `json:"kind"`
	ChannelIndex int    `json:"channelIndex"`
}

type routeOptionChannel struct {
	Kind         string              `json:"kind"`
	ChannelIndex int                 `json:"channelIndex"`
	ChannelName  string              `json:"channelName"`
	ServiceType  string              `json:"serviceType"`
	Status       string              `json:"status"`
	DefaultModel string              `json:"defaultModel,omitempty"`
	ModelMapping map[string][]string `json:"modelMapping,omitempty"`
}

type routeOptionGroup struct {
	Kind     string               `json:"kind"`
	Label    string               `json:"label"`
	Channels []routeOptionChannel `json:"channels"`
}

func ListConversations(channelScheduler *scheduler.ChannelScheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		registry := getConversationRegistry(channelScheduler)
		if registry == nil {
			c.JSON(http.StatusOK, gin.H{"conversations": []*conversation.Record{}})
			return
		}

		query := strings.ToLower(strings.TrimSpace(c.Query("q")))
		apiKind := strings.ToLower(strings.TrimSpace(c.Query("kind")))

		items := registry.List()
		filtered := make([]*conversation.Record, 0, len(items))
		for _, item := range items {
			if apiKind != "" && strings.ToLower(item.APIKind) != apiKind {
				continue
			}
			if query != "" && !matchConversationQuery(item, query) {
				continue
			}
			filtered = append(filtered, item)
		}

		c.JSON(http.StatusOK, gin.H{"conversations": filtered})
	}
}

func GetConversation(channelScheduler *scheduler.ChannelScheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		registry := getConversationRegistry(channelScheduler)
		if registry == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "conversation not found"})
			return
		}

		item, ok := registry.Get(c.Param("id"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "conversation not found"})
			return
		}
		c.JSON(http.StatusOK, item)
	}
}

func SetConversationRouteOverride(channelScheduler *scheduler.ChannelScheduler, cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		registry := getConversationRegistry(channelScheduler)
		if registry == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "conversation registry is not initialized"})
			return
		}

		var req routeOverrideRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
		item, ok := registry.Get(c.Param("id"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "conversation not found"})
			return
		}
		req.Kind = strings.TrimSpace(strings.ToLower(req.Kind))
		if req.Kind == "" {
			req.Kind = strings.ToLower(item.APIKind)
		}
		if !isValidConversationKind(req.Kind) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid channel kind"})
			return
		}
		if req.Kind != strings.ToLower(item.APIKind) {
			c.JSON(http.StatusConflict, gin.H{"error": "route override kind must match conversation kind"})
			return
		}
		channelName, ok := lookupChannelName(cfgManager, scheduler.ChannelKind(req.Kind), req.ChannelIndex)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}

		item, err := registry.SetRouteOverride(c.Param("id"), req.Kind, req.ChannelIndex, channelName)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, item)
	}
}

func ClearConversationRouteOverride(channelScheduler *scheduler.ChannelScheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		registry := getConversationRegistry(channelScheduler)
		if registry == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "conversation registry is not initialized"})
			return
		}

		item, err := registry.ClearRouteOverride(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, item)
	}
}

func GetConversationRouteOptions(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"kinds": []routeOptionGroup{
				buildRouteOptionGroup(cfgManager, scheduler.ChannelKindMessages, "Claude"),
				buildRouteOptionGroup(cfgManager, scheduler.ChannelKindResponses, "Codex"),
				buildRouteOptionGroup(cfgManager, scheduler.ChannelKindGemini, "Gemini"),
				buildRouteOptionGroup(cfgManager, scheduler.ChannelKindChat, "Chat"),
			},
		})
	}
}

func getConversationRegistry(channelScheduler *scheduler.ChannelScheduler) *conversation.Registry {
	if channelScheduler == nil {
		return nil
	}
	return channelScheduler.GetConversationRegistry()
}

func buildRouteOptionGroup(cfgManager *config.ConfigManager, kind scheduler.ChannelKind, label string) routeOptionGroup {
	cfg := cfgManager.GetConfig()
	upstreams := getConfigUpstreams(cfg, kind)
	channels := make([]routeOptionChannel, 0, len(upstreams))
	for index, upstream := range upstreams {
		if config.GetChannelStatus(&upstream) == config.ChannelStatusDeleted {
			continue
		}
		channels = append(channels, routeOptionChannel{
			Kind:         string(kind),
			ChannelIndex: index,
			ChannelName:  upstream.Name,
			ServiceType:  upstream.ServiceType,
			Status:       upstream.Status,
			DefaultModel: upstream.DefaultModel,
			ModelMapping: upstream.ModelMapping,
		})
	}
	return routeOptionGroup{
		Kind:     string(kind),
		Label:    label,
		Channels: channels,
	}
}

func lookupChannelName(cfgManager *config.ConfigManager, kind scheduler.ChannelKind, channelIndex int) (string, bool) {
	cfg := cfgManager.GetConfig()
	upstreams := getConfigUpstreams(cfg, kind)
	if channelIndex < 0 || channelIndex >= len(upstreams) {
		return "", false
	}
	return upstreams[channelIndex].Name, true
}

func getConfigUpstreams(cfg config.Config, kind scheduler.ChannelKind) []config.UpstreamConfig {
	switch kind {
	case scheduler.ChannelKindResponses:
		return cfg.ResponsesUpstream
	case scheduler.ChannelKindGemini:
		return cfg.GeminiUpstream
	case scheduler.ChannelKindChat:
		return cfg.ChatUpstream
	default:
		return cfg.Upstream
	}
}

func isValidConversationKind(kind string) bool {
	switch kind {
	case string(scheduler.ChannelKindMessages), string(scheduler.ChannelKindResponses), string(scheduler.ChannelKindGemini), string(scheduler.ChannelKindChat):
		return true
	default:
		return false
	}
}

func matchConversationQuery(item *conversation.Record, query string) bool {
	values := []string{
		item.ID,
		item.APIKind,
		item.LastModel,
		item.FirstPrompt,
		item.RouteOverrideString(),
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}
