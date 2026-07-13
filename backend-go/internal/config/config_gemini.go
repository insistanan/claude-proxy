package config

import (
	"fmt"
	"log"
	"time"
)

// ============== Gemini 渠道方法 ==============

func (cm *ConfigManager) GetCurrentGeminiUpstream() (*UpstreamConfig, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getFirstActive(cm.config.GeminiUpstream, "Gemini")
}

func (cm *ConfigManager) GetCurrentGeminiUpstreamWithIndex() (*UpstreamConfig, int, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getFirstActiveWithIndex(cm.config.GeminiUpstream, "Gemini")
}

func (cm *ConfigManager) AddGeminiUpstream(upstream UpstreamConfig) error {
	_, err := cm.AddGeminiUpstreamWithResult(upstream)
	return err
}

func (cm *ConfigManager) AddGeminiUpstreamWithResult(upstream UpstreamConfig) (AddedUpstream, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	var result AddedUpstream
	cm.config.GeminiUpstream, result = addUpstreamOp(cm.config.GeminiUpstream, upstream)

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return AddedUpstream{}, err
	}
	log.Printf("[Config-Upstream] 已添加 Gemini 上游（优先级1）: %s", cm.config.GeminiUpstream[result.Index].Name)
	return result, nil
}

func (cm *ConfigManager) UpdateGeminiUpstream(index int, updates UpstreamUpdate) (shouldResetMetrics bool, err error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if index < 0 || index >= len(cm.config.GeminiUpstream) {
		return false, fmt.Errorf("无效的 Gemini 上游索引: %d", index)
	}

	shouldResetMetrics, err = applyCommonUpdatesToList(cm.config.GeminiUpstream, index, updates, "Gemini")
	if err != nil {
		return false, err
	}

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return false, err
	}
	log.Printf("[Config-Upstream] 已更新 Gemini 上游: [%d] %s", index, cm.config.GeminiUpstream[index].Name)
	return shouldResetMetrics, nil
}

func (cm *ConfigManager) RemoveGeminiUpstream(index int) (*UpstreamConfig, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	newSlice, removed, err := removeFromSlice(cm.config.GeminiUpstream, index, "Gemini")
	if err != nil {
		return nil, err
	}
	cm.config.GeminiUpstream = newSlice
	cm.clearFailedKeysForUpstream(removed, "Gemini")

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return nil, err
	}
	log.Printf("[Config-Upstream] 已删除 Gemini 上游: %s", removed.Name)
	return removed, nil
}

func (cm *ConfigManager) AddGeminiAPIKey(index int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := addAPIKeyOp(cm.config.GeminiUpstream, index, apiKey, "Gemini"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) RemoveGeminiAPIKey(index int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := removeAPIKeyOp(cm.config.GeminiUpstream, index, apiKey, "Gemini"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

// GetNextGeminiAPIKey 获取下一个 API 密钥
func (cm *ConfigManager) GetNextGeminiAPIKey(upstream *UpstreamConfig, failedKeys map[string]bool) (string, error) {
	return cm.GetNextAPIKey(upstream, failedKeys, "Gemini")
}

func (cm *ConfigManager) MoveGeminiAPIKeyToTop(upstreamIndex int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := moveKeyToTopOp(cm.config.GeminiUpstream, upstreamIndex, apiKey); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) MoveGeminiAPIKeyToBottom(upstreamIndex int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := moveKeyToBottomOp(cm.config.GeminiUpstream, upstreamIndex, apiKey); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) ReorderGeminiUpstreams(order []int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := reorderOp(cm.config.GeminiUpstream, order, "Gemini"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) SetGeminiChannelStatus(index int, status string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := setStatusOp(cm.config.GeminiUpstream, index, status, "Gemini"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) SetGeminiChannelPromotion(index int, duration time.Duration, count int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := setPromotionOp(cm.config.GeminiUpstream, index, duration, count, "Gemini"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) GetPromotedGeminiChannel() (int, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getPromotedOp(cm.config.GeminiUpstream)
}

func (cm *ConfigManager) SetGeminiLoadBalance(strategy string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := validateLoadBalanceStrategy(strategy); err != nil {
		return err
	}
	cm.config.GeminiLoadBalance = strategy

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return err
	}
	log.Printf("[Config-LoadBalance] 已设置 Gemini 负载均衡策略: %s", strategy)
	return nil
}
