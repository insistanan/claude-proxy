// Package messages 提供 Claude Messages API 的渠道管理
package messages

import (
	"net/http"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/core/channelcrud"
	"github.com/BenedictKing/claude-proxy/internal/httpclient"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/gin-gonic/gin"
)

// crud 返回本协议专用的渠道管理 handler 集合。
// 渠道 CRUD / key 管理 / reorder / status / promotion / 负载均衡 / Ping 的实现
// 全部收敛在 core/channelcrud，本包只提供"绑定了 Messages 切片与 ChannelKind"的操作集合。
func Crud(cfgManager *config.ConfigManager, sch *scheduler.ChannelScheduler) *channelcrud.Handlers {
	return channelcrud.New(channelcrud.Ops{
		Kind: scheduler.ChannelKindMessages,
		List: func() []config.UpstreamConfig {
			return cfgManager.GetConfig().Upstream
		},
		LoadBalance: func() string {
			return cfgManager.GetConfig().LoadBalance
		},
		GetClient: func(ch *config.UpstreamConfig, timeout time.Duration) (*http.Client, error) {
			return httpclient.GetManager().GetStandardClientForUpstream(timeout, cfgManager, ch)
		},
		Add:           cfgManager.AddUpstreamWithResult,
		Update:        cfgManager.UpdateUpstream,
		Remove:        cfgManager.RemoveUpstream,
		AddKey:        cfgManager.AddAPIKey,
		RemoveKey:     cfgManager.RemoveAPIKey,
		MoveKeyTop:    cfgManager.MoveAPIKeyToTop,
		MoveKeyBottom: cfgManager.MoveAPIKeyToBottom,
		Reorder:       cfgManager.ReorderUpstreams,
		SetStatus:     cfgManager.SetChannelStatus,
		SetPromotion:  cfgManager.SetChannelPromotion,
		SetLoadBalance: cfgManager.SetLoadBalance,
	}, sch)
}

// GetUpstreams 获取上游列表 (兼容前端 channels 字段名)
func GetUpstreams(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).GetUpstreams
}

// AddUpstream 添加上游
func AddUpstream(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).AddUpstream
}

// UpdateUpstream 更新上游
func UpdateUpstream(cfgManager *config.ConfigManager, sch *scheduler.ChannelScheduler) gin.HandlerFunc {
	return Crud(cfgManager, sch).UpdateUpstream
}

// DeleteUpstream 删除上游
func DeleteUpstream(cfgManager *config.ConfigManager, sch *scheduler.ChannelScheduler) gin.HandlerFunc {
	return Crud(cfgManager, sch).DeleteUpstream
}

// AddApiKey 添加 API 密钥
func AddApiKey(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).AddApiKey
}

// DeleteApiKey 删除 API 密钥
func DeleteApiKey(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).DeleteApiKey
}

// MoveApiKeyToTop 将 API 密钥移到顶部
func MoveApiKeyToTop(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).MoveApiKeyToTop
}

// MoveApiKeyToBottom 将 API 密钥移到底部
func MoveApiKeyToBottom(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).MoveApiKeyToBottom
}

// UpdateLoadBalance 更新负载均衡策略
func UpdateLoadBalance(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).UpdateLoadBalance
}

// ReorderChannels 重新排序渠道
func ReorderChannels(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).ReorderChannels
}

// SetChannelStatus 设置渠道状态
func SetChannelStatus(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).SetChannelStatus
}

// SetChannelPromotion 设置渠道促销期
// 促销期内的渠道会被优先选择，忽略 trace 亲和性
func SetChannelPromotion(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).SetChannelPromotion
}

// PingChannel Ping单个渠道
func PingChannel(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).PingChannel
}

// PingAllChannels Ping所有渠道
func PingAllChannels(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).PingAllChannels
}
