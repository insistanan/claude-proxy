// Package chat 提供 Chat Completions API 的渠道管理
package chat

import (
	"net/http"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/core/channelcrud"
	"github.com/BenedictKing/claude-proxy/internal/httpclient"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/gin-gonic/gin"
)

// Crud 返回本协议专用的渠道管理 handler 集合。
// 路由注册见 handlers.RegisterChannelRoutes，它直接消费本函数返回的 *channelcrud.Handlers，
// 因此本包不再提供逐方法的 gin.HandlerFunc 包装。
func Crud(cfgManager *config.ConfigManager, sch *scheduler.ChannelScheduler) *channelcrud.Handlers {
	return channelcrud.New(channelcrud.Ops{
		Kind: scheduler.ChannelKindChat,
		List: func() []config.UpstreamConfig {
			return cfgManager.GetConfig().ChatUpstream
		},
		LoadBalance: func() string {
			return cfgManager.GetConfig().ChatLoadBalance
		},
		GetClient: func(ch *config.UpstreamConfig, timeout time.Duration) (*http.Client, error) {
			return httpclient.GetManager().GetStandardClientForUpstream(timeout, cfgManager, ch)
		},
		Add:            cfgManager.AddChatUpstreamWithResult,
		Update:         cfgManager.UpdateChatUpstream,
		Remove:         cfgManager.RemoveChatUpstream,
		AddKey:         cfgManager.AddChatAPIKey,
		RemoveKey:      cfgManager.RemoveChatAPIKey,
		MoveKeyTop:     cfgManager.MoveChatAPIKeyToTop,
		MoveKeyBottom:  cfgManager.MoveChatAPIKeyToBottom,
		Reorder:        cfgManager.ReorderChatUpstreams,
		SetStatus:      cfgManager.SetChatChannelStatus,
		SetPromotion:   cfgManager.SetChatChannelPromotion,
		SetLoadBalance: cfgManager.SetChatLoadBalance,
	}, sch)
}

// UpdateLoadBalance 更新负载均衡策略（RegisterChannelRoutes 的可选端点）
func UpdateLoadBalance(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).UpdateLoadBalance
}
