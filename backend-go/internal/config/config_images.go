package config

import (
	"fmt"
	"log"
	"strings"
	"time"
)

func (cm *ConfigManager) GetCurrentImagesUpstream() (*UpstreamConfig, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getFirstActive(cm.config.ImagesUpstream, "Images")
}

func (cm *ConfigManager) GetCurrentImagesUpstreamWithIndex() (*UpstreamConfig, int, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getFirstActiveWithIndex(cm.config.ImagesUpstream, "Images")
}

func (cm *ConfigManager) GetCurrentImagesUpstreamWithIndexForModel(model string) (*UpstreamConfig, int, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getFirstActiveWithIndexForModel(cm.config.ImagesUpstream, cm.config.ImagesPools, "Images", model)
}

func (cm *ConfigManager) AddImagesUpstream(upstream UpstreamConfig) error {
	_, err := cm.AddImagesUpstreamWithResult(upstream)
	return err
}

func (cm *ConfigManager) AddImagesUpstreamWithResult(upstream UpstreamConfig) (AddedUpstream, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	upstream.DefaultModel = strings.TrimSpace(upstream.DefaultModel)
	previous := cm.config.ImagesUpstream
	next, result, err := addValidatedUpstreamOp(previous, cm.config.ImagesPools, upstream)
	if err != nil {
		return AddedUpstream{}, err
	}
	cm.config.ImagesUpstream = next

	if err := cm.saveConfigLocked(cm.config); err != nil {
		cm.config.ImagesUpstream = previous
		return AddedUpstream{}, err
	}
	log.Printf("[Config-Upstream] 已添加 Images 上游（优先级1）: %s", cm.config.ImagesUpstream[result.Index].Name)
	return result, nil
}

func (cm *ConfigManager) UpdateImagesUpstream(index int, updates UpstreamUpdate) (shouldResetMetrics bool, err error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if index < 0 || index >= len(cm.config.ImagesUpstream) {
		return false, fmt.Errorf("无效的 Images 上游索引: %d", index)
	}

	previous := cm.config.ImagesUpstream
	next := cloneUpstreamList(previous)
	shouldResetMetrics, err = applyCommonUpdatesToList(next, cm.config.ImagesPools, index, updates, "Images")
	if err != nil {
		return false, err
	}

	cm.config.ImagesUpstream = next
	if err := cm.saveConfigLocked(cm.config); err != nil {
		cm.config.ImagesUpstream = previous
		return false, err
	}
	log.Printf("[Config-Upstream] 已更新 Images 上游: [%d] %s", index, cm.config.ImagesUpstream[index].Name)
	return shouldResetMetrics, nil
}

func (cm *ConfigManager) RemoveImagesUpstream(index int) (*UpstreamConfig, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	newSlice, removed, err := removeFromSlice(cm.config.ImagesUpstream, index, "Images")
	if err != nil {
		return nil, err
	}
	cm.config.ImagesUpstream = newSlice
	cm.clearFailedKeysForUpstream(removed, "Images")

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return nil, err
	}
	log.Printf("[Config-Upstream] 已删除 Images 上游: %s", removed.Name)
	return removed, nil
}

func (cm *ConfigManager) AddImagesAPIKey(index int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := addAPIKeyOp(cm.config.ImagesUpstream, index, apiKey, "Images"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) RemoveImagesAPIKey(index int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := removeAPIKeyOp(cm.config.ImagesUpstream, index, apiKey, "Images"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) GetNextImagesAPIKey(upstream *UpstreamConfig, failedKeys map[string]bool) (string, error) {
	return cm.GetNextAPIKey(upstream, failedKeys, "Images")
}

func (cm *ConfigManager) SetImagesLoadBalance(strategy string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := validateLoadBalanceStrategy(strategy); err != nil {
		return err
	}
	cm.config.ImagesLoadBalance = strategy

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return err
	}
	log.Printf("[Config-LoadBalance] 已设置 Images 负载均衡策略: %s", strategy)
	return nil
}

func (cm *ConfigManager) MoveImagesAPIKeyToTop(upstreamIndex int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := moveKeyToTopOp(cm.config.ImagesUpstream, upstreamIndex, apiKey); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) MoveImagesAPIKeyToBottom(upstreamIndex int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := moveKeyToBottomOp(cm.config.ImagesUpstream, upstreamIndex, apiKey); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) ReorderImagesUpstreams(order []int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := reorderOp(cm.config.ImagesUpstream, order, "Images"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) SetImagesChannelStatus(index int, status string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := setStatusOp(cm.config.ImagesUpstream, index, status, "Images"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) SetImagesChannelPromotion(index int, duration time.Duration, count int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := setPromotionOp(cm.config.ImagesUpstream, index, duration, count, "Images"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) GetPromotedImagesChannel() (int, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getPromotedOp(cm.config.ImagesUpstream)
}
