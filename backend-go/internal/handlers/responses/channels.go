// Package responses 提供 Responses API 的渠道管理
package responses

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
		Kind: scheduler.ChannelKindResponses,
		List: func() []config.UpstreamConfig {
			return cfgManager.GetConfig().ResponsesUpstream
		},
		LoadBalance: func() string {
			return cfgManager.GetConfig().ResponsesLoadBalance
		},
		GetClient: func(ch *config.UpstreamConfig, timeout time.Duration) (*http.Client, error) {
			return httpclient.GetManager().GetStandardClientForUpstream(timeout, cfgManager, ch)
		},
		Add:            cfgManager.AddResponsesUpstreamWithResult,
		Update:         cfgManager.UpdateResponsesUpstream,
		Remove:         cfgManager.RemoveResponsesUpstream,
		AddKey:         cfgManager.AddResponsesAPIKey,
		RemoveKey:      cfgManager.RemoveResponsesAPIKey,
		MoveKeyTop:     cfgManager.MoveResponsesAPIKeyToTop,
		MoveKeyBottom:  cfgManager.MoveResponsesAPIKeyToBottom,
		Reorder:        cfgManager.ReorderResponsesUpstreams,
		SetStatus:      cfgManager.SetResponsesChannelStatus,
		SetPromotion:   cfgManager.SetResponsesChannelPromotion,
		SetLoadBalance: cfgManager.SetResponsesLoadBalance,
	}, sch)
}

// GetUpstreams 获取 Responses 上游列表
func GetUpstreams(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).GetUpstreams
}

// AddUpstream 添加 Responses 上游
func AddUpstream(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).AddUpstream
}

// UpdateUpstream 更新 Responses 上游
func UpdateUpstream(cfgManager *config.ConfigManager, sch *scheduler.ChannelScheduler) gin.HandlerFunc {
	return Crud(cfgManager, sch).UpdateUpstream
}

// DeleteUpstream 删除 Responses 上游
func DeleteUpstream(cfgManager *config.ConfigManager, sch *scheduler.ChannelScheduler) gin.HandlerFunc {
	return Crud(cfgManager, sch).DeleteUpstream
}

// AddApiKey 添加 Responses 渠道 API 密钥
func AddApiKey(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).AddApiKey
}

// DeleteApiKey 删除 Responses 渠道 API 密钥
func DeleteApiKey(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).DeleteApiKey
}

// MoveApiKeyToTop 将 Responses 渠道 API 密钥移到最前面
func MoveApiKeyToTop(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).MoveApiKeyToTop
}

// MoveApiKeyToBottom 将 Responses 渠道 API 密钥移到最后面
func MoveApiKeyToBottom(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).MoveApiKeyToBottom
}

// UpdateLoadBalance 更新 Responses 负载均衡策略
func UpdateLoadBalance(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).UpdateLoadBalance
}

// ReorderChannels 重新排序 Responses 渠道优先级
func ReorderChannels(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).ReorderChannels
}

// SetChannelStatus 设置 Responses 渠道状态
func SetChannelStatus(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).SetChannelStatus
}

// SetChannelPromotion 设置 Responses 渠道促销期
func SetChannelPromotion(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).SetChannelPromotion
}

// PingChannel 测试 Responses 渠道连通性
func PingChannel(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).PingChannel
}

// PingAllChannels 测试所有 Responses 渠道连通性
func PingAllChannels(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return Crud(cfgManager, nil).PingAllChannels
}