package config

import (
	"time"
)

// ============== Messages 渠道方法（薄委托） ==============

func (cm *ConfigManager) GetCurrentUpstream() (*UpstreamConfig, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getFirstActive(cm.config.Upstream, "上游")
}

func (cm *ConfigManager) GetCurrentUpstreamWithIndex() (*UpstreamConfig, int, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getFirstActiveWithIndex(cm.config.Upstream, "上游")
}

func (cm *ConfigManager) GetCurrentUpstreamWithIndexForModel(model string) (*UpstreamConfig, int, error) {
	return cm.currentChannelForModel("messages", model)
}

func (cm *ConfigManager) AddUpstream(upstream UpstreamConfig) error {
	_, err := cm.AddUpstreamWithResult(upstream)
	return err
}

func (cm *ConfigManager) AddUpstreamWithResult(upstream UpstreamConfig) (AddedUpstream, error) {
	return cm.addChannel("messages", upstream)
}

func (cm *ConfigManager) UpdateUpstream(index int, updates UpstreamUpdate) (shouldResetMetrics bool, err error) {
	return cm.updateChannel("messages", index, updates)
}

func (cm *ConfigManager) RemoveUpstream(index int) (*UpstreamConfig, error) {
	return cm.removeChannel("messages", index)
}

func (cm *ConfigManager) AddAPIKey(index int, apiKey string) error {
	return cm.addChannelKey("messages", index, apiKey)
}

func (cm *ConfigManager) RemoveAPIKey(index int, apiKey string) error {
	return cm.removeChannelKey("messages", index, apiKey)
}

func (cm *ConfigManager) SetLoadBalance(strategy string) error {
	return cm.setLoadBalanceFor("messages", strategy)
}

func (cm *ConfigManager) MoveAPIKeyToTop(upstreamIndex int, apiKey string) error {
	return cm.moveChannelKeyTop("messages", upstreamIndex, apiKey)
}

func (cm *ConfigManager) MoveAPIKeyToBottom(upstreamIndex int, apiKey string) error {
	return cm.moveChannelKeyBottom("messages", upstreamIndex, apiKey)
}

func (cm *ConfigManager) ReorderUpstreams(order []int) error {
	return cm.reorderChannels("messages", order)
}

func (cm *ConfigManager) SetChannelStatus(index int, status string) error {
	return cm.setChannelStatusFor("messages", index, status)
}

// ConsumePromotionCount 消费促销请求次数（多 kind 共享方法）
func (cm *ConfigManager) ConsumePromotionCount(channelIndex int, channelType string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	var upstreams []UpstreamConfig
	switch channelType {
	case "responses":
		upstreams = cm.config.ResponsesUpstream
	case "gemini":
		upstreams = cm.config.GeminiUpstream
	case "chat":
		upstreams = cm.config.ChatUpstream
	case "images":
		upstreams = cm.config.ImagesUpstream
	default:
		upstreams = cm.config.Upstream
	}

	if consumePromotionOp(upstreams, channelIndex, channelType) {
		_ = cm.saveConfigLocked(cm.config)
	}
}

func (cm *ConfigManager) SetChannelPromotion(index int, duration time.Duration, count int) error {
	return cm.setChannelPromotionFor("messages", index, duration, count)
}

func (cm *ConfigManager) GetPromotedChannel() (int, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getPromotedOp(cm.config.Upstream)
}