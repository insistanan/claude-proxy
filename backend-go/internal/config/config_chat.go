package config

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// ============== Chat 渠道方法 ==============

func (cm *ConfigManager) GetCurrentChatUpstream() (*UpstreamConfig, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getFirstActive(cm.config.ChatUpstream, "Chat")
}

func (cm *ConfigManager) GetCurrentChatUpstreamWithIndex() (*UpstreamConfig, int, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getFirstActiveWithIndex(cm.config.ChatUpstream, "Chat")
}

func (cm *ConfigManager) GetCurrentChatUpstreamWithIndexForModel(model string) (*UpstreamConfig, int, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getFirstActiveWithIndexForModel(cm.config.ChatUpstream, cm.config.ChatPools, "Chat", model)
}

func (cm *ConfigManager) AddChatUpstream(upstream UpstreamConfig) error {
	_, err := cm.AddChatUpstreamWithResult(upstream)
	return err
}

func (cm *ConfigManager) AddChatUpstreamWithResult(upstream UpstreamConfig) (AddedUpstream, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	upstream.DefaultModel = strings.TrimSpace(upstream.DefaultModel)
	previous := cm.config.ChatUpstream
	next, result, err := addValidatedUpstreamOp(previous, cm.config.ChatPools, upstream)
	if err != nil {
		return AddedUpstream{}, err
	}
	cm.config.ChatUpstream = next

	if err := cm.saveConfigLocked(cm.config); err != nil {
		cm.config.ChatUpstream = previous
		return AddedUpstream{}, err
	}
	log.Printf("[Config-Upstream] 已添加 Chat 上游（优先级1）: %s", cm.config.ChatUpstream[result.Index].Name)
	return result, nil
}

func (cm *ConfigManager) UpdateChatUpstream(index int, updates UpstreamUpdate) (shouldResetMetrics bool, err error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if index < 0 || index >= len(cm.config.ChatUpstream) {
		return false, fmt.Errorf("无效的 Chat 上游索引: %d", index)
	}

	previous := cm.config.ChatUpstream
	next := cloneUpstreamList(previous)
	shouldResetMetrics, err = applyCommonUpdatesToList(next, cm.config.ChatPools, index, updates, "Chat")
	if err != nil {
		return false, err
	}

	cm.config.ChatUpstream = next
	if err := cm.saveConfigLocked(cm.config); err != nil {
		cm.config.ChatUpstream = previous
		return false, err
	}
	log.Printf("[Config-Upstream] 已更新 Chat 上游: [%d] %s", index, cm.config.ChatUpstream[index].Name)
	return shouldResetMetrics, nil
}

func (cm *ConfigManager) RemoveChatUpstream(index int) (*UpstreamConfig, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	newSlice, removed, err := removeFromSlice(cm.config.ChatUpstream, index, "Chat")
	if err != nil {
		return nil, err
	}
	cm.config.ChatUpstream = newSlice
	cm.clearFailedKeysForUpstream(removed, "Chat")

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return nil, err
	}
	log.Printf("[Config-Upstream] 已删除 Chat 上游: %s", removed.Name)
	return removed, nil
}

func (cm *ConfigManager) AddChatAPIKey(index int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := addAPIKeyOp(cm.config.ChatUpstream, index, apiKey, "Chat"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) RemoveChatAPIKey(index int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := removeAPIKeyOp(cm.config.ChatUpstream, index, apiKey, "Chat"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

// GetNextChatAPIKey 获取下一个 API 密钥
func (cm *ConfigManager) GetNextChatAPIKey(upstream *UpstreamConfig, failedKeys map[string]bool) (string, error) {
	return cm.GetNextAPIKey(upstream, failedKeys, "Chat")
}

func (cm *ConfigManager) SetChatLoadBalance(strategy string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := validateLoadBalanceStrategy(strategy); err != nil {
		return err
	}
	cm.config.ChatLoadBalance = strategy

	if err := cm.saveConfigLocked(cm.config); err != nil {
		return err
	}
	log.Printf("[Config-LoadBalance] 已设置 Chat 负载均衡策略: %s", strategy)
	return nil
}

func (cm *ConfigManager) MoveChatAPIKeyToTop(upstreamIndex int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := moveKeyToTopOp(cm.config.ChatUpstream, upstreamIndex, apiKey); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) MoveChatAPIKeyToBottom(upstreamIndex int, apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := moveKeyToBottomOp(cm.config.ChatUpstream, upstreamIndex, apiKey); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) ReorderChatUpstreams(order []int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := reorderOp(cm.config.ChatUpstream, order, "Chat"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) SetChatChannelStatus(index int, status string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := setStatusOp(cm.config.ChatUpstream, index, status, "Chat"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) SetChatChannelPromotion(index int, duration time.Duration, count int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := setPromotionOp(cm.config.ChatUpstream, index, duration, count, "Chat"); err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) GetPromotedChatChannel() (int, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return getPromotedOp(cm.config.ChatUpstream)
}
