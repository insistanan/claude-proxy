package config

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/utils"
)

// getFirstActive 从上游列表中选择第一个 active 且可调度的渠道
func getFirstActive(upstreams []UpstreamConfig, label string) (*UpstreamConfig, error) {
	if len(upstreams) == 0 {
		return nil, fmt.Errorf("未配置任何%s渠道", label)
	}
	for i := range upstreams {
		if GetChannelStatus(&upstreams[i]) == ChannelStatusActive && IsChannelSchedulable(&upstreams[i]) {
			return &upstreams[i], nil
		}
	}
	for i := range upstreams {
		if IsChannelSchedulable(&upstreams[i]) {
			return &upstreams[i], nil
		}
	}
	return nil, fmt.Errorf("没有可用的%s渠道", label)
}

// getFirstActiveWithIndex 从上游列表中选择第一个 active 且可调度的渠道（带索引）
func getFirstActiveWithIndex(upstreams []UpstreamConfig, label string) (*UpstreamConfig, int, error) {
	if len(upstreams) == 0 {
		return nil, -1, fmt.Errorf("未配置任何%s渠道", label)
	}
	for i := range upstreams {
		if GetChannelStatus(&upstreams[i]) == ChannelStatusActive && IsChannelSchedulable(&upstreams[i]) {
			return &upstreams[i], i, nil
		}
	}
	for i := range upstreams {
		if IsChannelSchedulable(&upstreams[i]) {
			return &upstreams[i], i, nil
		}
	}
	return nil, -1, fmt.Errorf("没有可用的%s渠道", label)
}

// prepareNewUpstream 准备新增渠道的公共字段
func prepareNewUpstream(upstream *UpstreamConfig) {
	if upstream.Status == "" {
		upstream.Status = "active"
	}
	prepareUpstreamLifecycle(upstream, time.Now())
	upstream.APIKeys = deduplicateStrings(upstream.APIKeys)
	upstream.BaseURLs = deduplicateBaseURLs(upstream.BaseURLs)
}

// applyCommonUpdates 应用通用的 UpstreamUpdate 字段到上游配置
// 返回 shouldResetMetrics 标识是否需要重置指标
func applyCommonUpdates(upstream *UpstreamConfig, index int, updates UpstreamUpdate, label string) (bool, error) {
	shouldResetMetrics := false

	if updates.Name != nil {
		upstream.Name = *updates.Name
	}
	if updates.BaseURL != nil {
		upstream.BaseURL = *updates.BaseURL
		if updates.BaseURLs == nil {
			upstream.BaseURLs = nil
		}
	}
	if updates.BaseURLs != nil {
		upstream.BaseURLs = deduplicateBaseURLs(updates.BaseURLs)
	}
	if updates.ServiceType != nil {
		upstream.ServiceType = *updates.ServiceType
	}
	if updates.Description != nil {
		upstream.Description = *updates.Description
	}
	if updates.Website != nil {
		upstream.Website = *updates.Website
	}
	if updates.APIKeys != nil {
		shouldResetMetrics = applyAPIKeysUpdate(upstream, index, updates.APIKeys, label)
	}
	if updates.ModelMapping != nil {
		upstream.ModelMapping = updates.ModelMapping
	}
	if updates.DefaultModel != nil {
		upstream.DefaultModel = strings.TrimSpace(*updates.DefaultModel)
	}
	if updates.InsecureSkipVerify != nil {
		upstream.InsecureSkipVerify = *updates.InsecureSkipVerify
	}
	if updates.Priority != nil {
		upstream.Priority = *updates.Priority
	}
	if updates.Status != nil {
		if err := setUpstreamStatus(upstream, *updates.Status, time.Now()); err != nil {
			return false, err
		}
	}
	if updates.PromotionUntil != nil {
		upstream.PromotionUntil = updates.PromotionUntil
	}
	if updates.PromotionCount != nil {
		upstream.PromotionCount = *updates.PromotionCount
	}
	if updates.LowQuality != nil {
		upstream.LowQuality = *updates.LowQuality
	}
	if updates.VisionCapable != nil {
		upstream.VisionCapable = *updates.VisionCapable
	}
	if updates.Temporary != nil {
		upstream.Temporary = *updates.Temporary
	}
	if updates.TemporaryUntil != nil {
		upstream.TemporaryUntil = updates.TemporaryUntil
	}
	if updates.DeprecatedAt != nil {
		upstream.DeprecatedAt = updates.DeprecatedAt
	}
	prepareUpstreamLifecycle(upstream, time.Now())
	if updates.InjectDummyThoughtSignature != nil {
		upstream.InjectDummyThoughtSignature = *updates.InjectDummyThoughtSignature
	}
	if updates.StripThoughtSignature != nil {
		upstream.StripThoughtSignature = *updates.StripThoughtSignature
	}
	return shouldResetMetrics, nil
}

// applyAPIKeysUpdate 处理 API Keys 更新逻辑（历史记录、自动激活）
func applyAPIKeysUpdate(upstream *UpstreamConfig, index int, newAPIKeys []string, label string) bool {
	shouldResetMetrics := false
	newKeys := make(map[string]bool)
	for _, key := range newAPIKeys {
		newKeys[key] = true
	}

	for _, key := range upstream.APIKeys {
		if !newKeys[key] {
			alreadyInHistory := false
			for _, hk := range upstream.HistoricalAPIKeys {
				if hk == key {
					alreadyInHistory = true
					break
				}
			}
			if !alreadyInHistory {
				upstream.HistoricalAPIKeys = append(upstream.HistoricalAPIKeys, key)
				log.Printf("[Config-Upstream] %s渠道 [%d] %s: Key %s 已移入历史列表", label, index, upstream.Name, utils.MaskAPIKey(key))
			}
		}
	}

	var newHistoricalKeys []string
	for _, hk := range upstream.HistoricalAPIKeys {
		if !newKeys[hk] {
			newHistoricalKeys = append(newHistoricalKeys, hk)
		} else {
			log.Printf("[Config-Upstream] %s渠道 [%d] %s: Key %s 已从历史列表恢复", label, index, upstream.Name, utils.MaskAPIKey(hk))
		}
	}
	upstream.HistoricalAPIKeys = newHistoricalKeys

	if len(upstream.APIKeys) == 1 && len(newAPIKeys) == 1 && upstream.APIKeys[0] != newAPIKeys[0] {
		shouldResetMetrics = true
		if upstream.Status == "suspended" {
			upstream.Status = "active"
			log.Printf("[Config-Upstream] %s渠道 [%d] %s 已从暂停状态自动激活（单 key 更换）", label, index, upstream.Name)
		}
	}
	upstream.APIKeys = deduplicateStrings(newAPIKeys)
	return shouldResetMetrics
}

// removeFromSlice 从上游切片中删除指定索引的元素
func removeFromSlice(upstreams []UpstreamConfig, index int, label string) ([]UpstreamConfig, *UpstreamConfig, error) {
	if index < 0 || index >= len(upstreams) {
		return upstreams, nil, fmt.Errorf("无效的%s上游索引: %d", label, index)
	}
	removed := upstreams[index]
	upstreams = append(upstreams[:index], upstreams[index+1:]...)
	return upstreams, &removed, nil
}

// addAPIKeyOp 向指定渠道添加 API 密钥（调用方必须持锁）
func addAPIKeyOp(upstreams []UpstreamConfig, index int, apiKey string, label string) error {
	if index < 0 || index >= len(upstreams) {
		return fmt.Errorf("无效的上游索引: %d", index)
	}
	for _, key := range upstreams[index].APIKeys {
		if key == apiKey {
			return fmt.Errorf("API密钥已存在")
		}
	}
	upstreams[index].APIKeys = append(upstreams[index].APIKeys, apiKey)

	var newHistoricalKeys []string
	for _, hk := range upstreams[index].HistoricalAPIKeys {
		if hk != apiKey {
			newHistoricalKeys = append(newHistoricalKeys, hk)
		} else {
			log.Printf("[%s-Key] 上游 [%d] %s: Key %s 已从历史列表恢复", label, index, upstreams[index].Name, utils.MaskAPIKey(hk))
		}
	}
	upstreams[index].HistoricalAPIKeys = newHistoricalKeys

	log.Printf("[%s-Key] 已添加API密钥到上游 [%d] %s", label, index, upstreams[index].Name)
	return nil
}

// removeAPIKeyOp 从指定渠道删除 API 密钥（调用方必须持锁）
func removeAPIKeyOp(upstreams []UpstreamConfig, index int, apiKey string, label string) error {
	if index < 0 || index >= len(upstreams) {
		return fmt.Errorf("无效的上游索引: %d", index)
	}
	keys := upstreams[index].APIKeys
	found := false
	for i, key := range keys {
		if key == apiKey {
			upstreams[index].APIKeys = append(keys[:i], keys[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("API密钥不存在")
	}

	alreadyInHistory := false
	for _, hk := range upstreams[index].HistoricalAPIKeys {
		if hk == apiKey {
			alreadyInHistory = true
			break
		}
	}
	if !alreadyInHistory {
		upstreams[index].HistoricalAPIKeys = append(upstreams[index].HistoricalAPIKeys, apiKey)
		log.Printf("[%s-Key] 上游 [%d] %s: Key %s 已移入历史列表", label, index, upstreams[index].Name, utils.MaskAPIKey(apiKey))
	}

	log.Printf("[%s-Key] 已从上游 [%d] %s 删除API密钥", label, index, upstreams[index].Name)
	return nil
}

// moveKeyToTopOp 将密钥移到列表顶部（调用方必须持锁）
func moveKeyToTopOp(upstreams []UpstreamConfig, upstreamIndex int, apiKey string) error {
	if upstreamIndex < 0 || upstreamIndex >= len(upstreams) {
		return fmt.Errorf("无效的上游索引: %d", upstreamIndex)
	}
	upstream := &upstreams[upstreamIndex]
	index := -1
	for i, key := range upstream.APIKeys {
		if key == apiKey {
			index = i
			break
		}
	}
	if index <= 0 {
		return nil
	}
	upstream.APIKeys = append([]string{apiKey}, append(upstream.APIKeys[:index], upstream.APIKeys[index+1:]...)...)
	return nil
}

// moveKeyToBottomOp 将密钥移到列表底部（调用方必须持锁）
func moveKeyToBottomOp(upstreams []UpstreamConfig, upstreamIndex int, apiKey string) error {
	if upstreamIndex < 0 || upstreamIndex >= len(upstreams) {
		return fmt.Errorf("无效的上游索引: %d", upstreamIndex)
	}
	upstream := &upstreams[upstreamIndex]
	index := -1
	for i, key := range upstream.APIKeys {
		if key == apiKey {
			index = i
			break
		}
	}
	if index == -1 || index == len(upstream.APIKeys)-1 {
		return nil
	}
	upstream.APIKeys = append(upstream.APIKeys[:index], upstream.APIKeys[index+1:]...)
	upstream.APIKeys = append(upstream.APIKeys, apiKey)
	return nil
}

// reorderOp 重新排序渠道优先级（调用方必须持锁）
func reorderOp(upstreams []UpstreamConfig, order []int, label string) error {
	if len(order) == 0 {
		return fmt.Errorf("排序数组不能为空")
	}
	seen := make(map[int]bool)
	for _, idx := range order {
		if idx < 0 || idx >= len(upstreams) {
			return fmt.Errorf("无效的渠道索引: %d", idx)
		}
		if seen[idx] {
			return fmt.Errorf("重复的渠道索引: %d", idx)
		}
		seen[idx] = true
	}
	for i, idx := range order {
		upstreams[idx].Priority = i + 1
	}
	log.Printf("[Config-Reorder] 已更新 %s 渠道优先级顺序 (%d 个渠道)", label, len(order))
	return nil
}

// setStatusOp 设置渠道状态（调用方必须持锁）
func setStatusOp(upstreams []UpstreamConfig, index int, status string, label string) error {
	if index < 0 || index >= len(upstreams) {
		return fmt.Errorf("无效的上游索引: %d", index)
	}
	if err := setUpstreamStatus(&upstreams[index], status, time.Now()); err != nil {
		return err
	}
	finalStatus := upstreams[index].Status
	if finalStatus == "suspended" || finalStatus == "deprecated" {
		log.Printf("[Config-Status] 已清除渠道 [%d] %s 的促销期", index, upstreams[index].Name)
	}
	log.Printf("[Config-Status] 已设置 %s 渠道 [%d] %s 状态为: %s", label, index, upstreams[index].Name, finalStatus)
	return nil
}

// setPromotionOp 设置渠道促销期（调用方必须持锁）
func setPromotionOp(upstreams []UpstreamConfig, index int, duration time.Duration, count int, label string) error {
	if index < 0 || index >= len(upstreams) {
		return fmt.Errorf("无效的上游索引: %d", index)
	}
	upstream := &upstreams[index]
	if duration > 0 {
		until := time.Now().Add(duration)
		upstream.PromotionUntil = &until
		upstream.PromotionCount = 0
		log.Printf("[Config-Promotion] 已设置 %s 渠道 [%d] %s 促销期: %v", label, index, upstream.Name, duration)
	} else if count > 0 {
		upstream.PromotionUntil = nil
		upstream.PromotionCount = count
		log.Printf("[Config-Promotion] 已设置 %s 渠道 [%d] %s 促销次数: %d", label, index, upstream.Name, count)
	} else {
		upstream.PromotionUntil = nil
		upstream.PromotionCount = 0
		log.Printf("[Config-Promotion] 已清除 %s 渠道 [%d] %s 的促销期", label, index, upstream.Name)
	}
	return nil
}

// consumePromotionOp 消费促销计数（调用方必须持锁），返回是否需要保存
func consumePromotionOp(upstreams []UpstreamConfig, index int, label string) bool {
	if index < 0 || index >= len(upstreams) {
		return false
	}
	upstream := &upstreams[index]
	if upstream.PromotionCount <= 0 {
		return false
	}
	upstream.PromotionCount--
	if upstream.PromotionCount == 0 {
		upstream.PromotionUntil = nil
		log.Printf("[Config-Promotion] %s渠道 [%d] %s 促销次数已用完，自动清除促销状态", label, index, upstream.Name)
	}
	return true
}

// getPromotedOp 获取处于促销期的渠道索引
func getPromotedOp(upstreams []UpstreamConfig) (int, bool) {
	for i := range upstreams {
		if IsChannelInPromotion(&upstreams[i]) {
			return i, true
		}
	}
	return -1, false
}
