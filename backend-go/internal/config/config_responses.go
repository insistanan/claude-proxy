package config

import (
	"fmt"
	"log"
	"time"
)

// ============== Responses 渠道方法 ==============

func (cm *ConfigManager) GetCurrentResponsesUpstream() (*UpstreamConfig, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getFirstActive(cm.config.ResponsesUpstream, "Responses")
}

func (cm *ConfigManager) GetCurrentResponsesUpstreamWithIndex() (*UpstreamConfig, int, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getFirstActiveWithIndex(cm.config.ResponsesUpstream, "Responses")
}

func (cm *ConfigManager) AddResponsesUpstream(upstream UpstreamConfig) error {
	_, err := cm.AddResponsesUpstreamWithResult(upstream)
	return err
}

func (cm *ConfigManager) AddResponsesUpstreamWithResult(upstream UpstreamConfig) (AddedUpstream, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Codex Responses 渠道默认直接支持图片理解。
	upstream.VisionCapable = true
	upstream.VisionLayerEnabled = false
	upstream.VisionLayerChannelID = ""
	upstream.VisionLayerModel = ""
	var result AddedUpstream
	cm.config.ResponsesUpstream, result = addUpstreamOp(cm.config.ResponsesUpstream, upstream)

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return AddedUpstream{}, err
	}
	log.Printf("[Config-Upstream] 已添加 Responses 上游（优先级1）: %s", cm.config.ResponsesUpstream[result.Index].Name)
	return result, nil
}

func (cm *ConfigManager) UpdateResponsesUpstream(index int, updates UpstreamUpdate) (shouldResetMetrics bool, err error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if index < 0 || index >= len(cm.config.ResponsesUpstream) {
		return false, fmt.Errorf("无效的 Responses 上游索引: %d", index)
	}

	shouldResetMetrics, err = applyCommonUpdatesToList(cm.config.ResponsesUpstream, index, updates, "Responses")
	if err != nil {
		return false, err
	}

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return false, err
	}
	log.Printf("[Config-Upstream] 已更新 Responses 上游: [%d] %s", index, cm.config.ResponsesUpstream[index].Name)
	return shouldResetMetrics, nil
}

func (cm *ConfigManager) RemoveResponsesUpstream(index int) (*UpstreamConfig, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	newSlice, removed, err := removeFromSlice(cm.config.ResponsesUpstream, index, "Responses")
	if err != nil {
		return nil, err
	}
	cm.config.ResponsesUpstream = newSlice
	cm.clearFailedKeysForUpstream(removed, "Responses")

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return nil, err
	}
	log.Printf("[Config-Upstream] 已删除 Responses 上游: %s", removed.Name)
	return removed, nil
}

func (cm *ConfigManager) AddResponsesAPIKey(index int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := addAPIKeyOp(cm.config.ResponsesUpstream, index, apiKey, "Responses"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) RemoveResponsesAPIKey(index int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := removeAPIKeyOp(cm.config.ResponsesUpstream, index, apiKey, "Responses"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

// GetNextResponsesAPIKey 获取下一个 API 密钥
func (cm *ConfigManager) GetNextResponsesAPIKey(upstream *UpstreamConfig, failedKeys map[string]bool) (string, error) {
	return cm.GetNextAPIKey(upstream, failedKeys, "Responses")
}

func (cm *ConfigManager) SetResponsesLoadBalance(strategy string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := validateLoadBalanceStrategy(strategy); err != nil {
		return err
	}
	cm.config.ResponsesLoadBalance = strategy

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return err
	}
	log.Printf("[Config-LoadBalance] 已设置 Responses 负载均衡策略: %s", strategy)
	return nil
}

func (cm *ConfigManager) MoveResponsesAPIKeyToTop(upstreamIndex int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := moveKeyToTopOp(cm.config.ResponsesUpstream, upstreamIndex, apiKey); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) MoveResponsesAPIKeyToBottom(upstreamIndex int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := moveKeyToBottomOp(cm.config.ResponsesUpstream, upstreamIndex, apiKey); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) ReorderResponsesUpstreams(order []int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := reorderOp(cm.config.ResponsesUpstream, order, "Responses"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) SetResponsesChannelStatus(index int, status string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := setStatusOp(cm.config.ResponsesUpstream, index, status, "Responses"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) SetResponsesChannelPromotion(index int, duration time.Duration, count int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := setPromotionOp(cm.config.ResponsesUpstream, index, duration, count, "Responses"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) GetPromotedResponsesChannel() (int, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getPromotedOp(cm.config.ResponsesUpstream)
}
