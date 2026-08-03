// Package images 提供 Images API 的渠道管理
package images

import (
	"net/http"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/core/channelcrud"
	"github.com/BenedictKing/claude-proxy/internal/httpclient"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/gin-gonic/gin"
)

func Crud(cfgManager *config.ConfigManager, sch *scheduler.ChannelScheduler) *channelcrud.Handlers {
	return channelcrud.New(channelcrud.Ops{
		Kind: scheduler.ChannelKindImages,
		List: func() []config.UpstreamConfig {
			return cfgManager.GetConfig().ImagesUpstream
		},
		LoadBalance: func() string {
			return cfgManager.GetConfig().ImagesLoadBalance
		},
		GetClient: func(ch *config.UpstreamConfig, timeout time.Duration) (*http.Client, error) {
			return httpclient.GetManager().GetStandardClientForUpstream(timeout, cfgManager, ch)
		},
		Add:            cfgManager.AddImagesUpstreamWithResult,
		Update:         cfgManager.UpdateImagesUpstream,
		Remove:         cfgManager.RemoveImagesUpstream,
		AddKey:         cfgManager.AddImagesAPIKey,
		RemoveKey:      cfgManager.RemoveImagesAPIKey,
		MoveKeyTop:     cfgManager.MoveImagesAPIKeyToTop,
		MoveKeyBottom:  cfgManager.MoveImagesAPIKeyToBottom,
		Reorder:        cfgManager.ReorderImagesUpstreams,
		SetStatus:      cfgManager.SetImagesChannelStatus,
		SetPromotion:   cfgManager.SetImagesChannelPromotion,
		SetLoadBalance: cfgManager.SetImagesLoadBalance,
	}, sch)
}

// GetUpstreams 获取上游列表
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

// ReorderChannels 重新排序渠道
func ReorderChannels(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).ReorderChannels
}

// SetChannelStatus 设置渠道状态
func SetChannelStatus(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).SetChannelStatus
}

// SetChannelPromotion 设置渠道促销期
func SetChannelPromotion(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).SetChannelPromotion
}

// UpdateLoadBalance 更新负载均衡策略
func UpdateLoadBalance(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).UpdateLoadBalance
}

// PingChannel Ping 单个渠道
func PingChannel(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).PingChannel
}

// PingAllChannels Ping 所有渠道
func PingAllChannels(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).PingAllChannels
}
