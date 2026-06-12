package config

import (
	"fmt"
	"log"
	"time"
)

// ============== Messages 渠道方法 ==============

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

func (cm *ConfigManager) AddUpstream(upstream UpstreamConfig) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	prepareNewUpstream(&upstream)
	
	// 新渠道优先级设为 1（最高）
	upstream.Priority = 1
	
	// 所有现有渠道的优先级 +1
	for i := range cm.config.Upstream {
		if cm.config.Upstream[i].Priority == 0 {
			cm.config.Upstream[i].Priority = i + 2 // 原来是索引，现在变成索引+2
		} else {
			cm.config.Upstream[i].Priority++ // 原有优先级 +1
		}
	}
	
	// 插入到开头
	cm.config.Upstream = append([]UpstreamConfig{upstream}, cm.config.Upstream...)

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return err
	}
	log.Printf("[Config-Upstream] 已添加上游（优先级1）: %s", upstream.Name)
	return nil
}

func (cm *ConfigManager) UpdateUpstream(index int, updates UpstreamUpdate) (shouldResetMetrics bool, err error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if index < 0 || index >= len(cm.config.Upstream) {
		return false, fmt.Errorf("无效的上游索引: %d", index)
	}

	shouldResetMetrics, err = applyCommonUpdates(&cm.config.Upstream[index], index, updates, "Messages")
	if err != nil {
		return false, err
	}

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return false, err
	}
	log.Printf("[Config-Upstream] 已更新上游: [%d] %s", index, cm.config.Upstream[index].Name)
	return shouldResetMetrics, nil
}

func (cm *ConfigManager) RemoveUpstream(index int) (*UpstreamConfig, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	newSlice, removed, err := removeFromSlice(cm.config.Upstream, index, "")
	if err != nil {
		return nil, err
	}
	cm.config.Upstream = newSlice
	cm.clearFailedKeysForUpstream(removed, "Messages")

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return nil, err
	}
	log.Printf("[Config-Upstream] 已删除上游: %s", removed.Name)
	return removed, nil
}

func (cm *ConfigManager) AddAPIKey(index int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := addAPIKeyOp(cm.config.Upstream, index, apiKey, "Messages"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) RemoveAPIKey(index int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := removeAPIKeyOp(cm.config.Upstream, index, apiKey, "Messages"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) SetLoadBalance(strategy string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := validateLoadBalanceStrategy(strategy); err != nil {
		return err
	}
	cm.config.LoadBalance = strategy

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return err
	}
	log.Printf("[Config-LoadBalance] 已设置负载均衡策略: %s", strategy)
	return nil
}

func (cm *ConfigManager) MoveAPIKeyToTop(upstreamIndex int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := moveKeyToTopOp(cm.config.Upstream, upstreamIndex, apiKey); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) MoveAPIKeyToBottom(upstreamIndex int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := moveKeyToBottomOp(cm.config.Upstream, upstreamIndex, apiKey); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) ReorderUpstreams(order []int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := reorderOp(cm.config.Upstream, order, "Messages"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) SetChannelStatus(index int, status string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := setStatusOp(cm.config.Upstream, index, status, "Messages"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

// ConsumePromotionCount 消费促销请求次数
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
	default:
		upstreams = cm.config.Upstream
	}

	if consumePromotionOp(upstreams, channelIndex, channelType) {
		_ = cm.saveConfigLocked(cm.config)
	}
}

func (cm *ConfigManager) SetChannelPromotion(index int, duration time.Duration, count int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := setPromotionOp(cm.config.Upstream, index, duration, count, "Messages"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) GetPromotedChannel() (int, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getPromotedOp(cm.config.Upstream)
}


