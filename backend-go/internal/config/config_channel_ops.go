package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/utils"
)

// AddedUpstream 记录新增渠道的稳定标识和当前位置。
type AddedUpstream struct {
	ID    string
	Index int
}

// getFirstActive 从上游列表中选择第一个 active 且可调度的渠道
func getFirstActive(upstreams []UpstreamConfig, label string) (*UpstreamConfig, error) {
	if len(upstreams) == 0 {
		return nil, fmt.Errorf("未配置任何%s渠道", label)
	}
	if index := bestUpstreamIndex(upstreams, true); index >= 0 {
		return &upstreams[index], nil
	}
	if index := bestUpstreamIndex(upstreams, false); index >= 0 {
		return &upstreams[index], nil
	}
	return nil, fmt.Errorf("没有可用的%s渠道", label)
}

// getFirstActiveWithIndex 从上游列表中选择第一个 active 且可调度的渠道（带索引）
func getFirstActiveWithIndex(upstreams []UpstreamConfig, label string) (*UpstreamConfig, int, error) {
	if len(upstreams) == 0 {
		return nil, -1, fmt.Errorf("未配置任何%s渠道", label)
	}
	if index := bestUpstreamIndex(upstreams, true); index >= 0 {
		return &upstreams[index], index, nil
	}
	if index := bestUpstreamIndex(upstreams, false); index >= 0 {
		return &upstreams[index], index, nil
	}
	return nil, -1, fmt.Errorf("没有可用的%s渠道", label)
}

func bestUpstreamIndex(upstreams []UpstreamConfig, activeOnly bool) int {
	bestIndex := -1
	bestPriority := 0
	for index := range upstreams {
		upstream := &upstreams[index]
		if !IsChannelSchedulable(upstream) {
			continue
		}
		if activeOnly && GetChannelStatus(upstream) != ChannelStatusActive {
			continue
		}
		priority := GetChannelPriority(upstream, index)
		if bestIndex < 0 || priority < bestPriority || (priority == bestPriority && index < bestIndex) {
			bestIndex = index
			bestPriority = priority
		}
	}
	return bestIndex
}

// prepareNewUpstream 准备新增渠道的公共字段
func prepareNewUpstream(upstream *UpstreamConfig) {
	if strings.TrimSpace(upstream.ID) == "" {
		upstream.ID = newUpstreamID()
	}
	if upstream.Status == "" {
		upstream.Status = "active"
	}
	prepareUpstreamLifecycle(upstream, time.Now())
	upstream.APIKeys = deduplicateStrings(upstream.APIKeys)
	upstream.BaseURLs = deduplicateBaseURLs(upstream.BaseURLs)
	upstream.ModelMapping = normalizeModelMapping(upstream.ModelMapping)
}

func normalizeModelMapping(mapping map[string][]string) map[string][]string {
	if mapping == nil {
		return nil
	}

	normalized := make(map[string][]string, len(mapping))
	rawSources := make([]string, 0, len(mapping))
	for source := range mapping {
		rawSources = append(rawSources, source)
	}
	sort.Strings(rawSources)
	for _, rawSource := range rawSources {
		source := strings.TrimSpace(rawSource)
		if source == "" {
			continue
		}
		targets := mapping[rawSource]
		seen := make(map[string]struct{}, len(targets)+len(normalized[source]))
		for _, target := range normalized[source] {
			seen[target] = struct{}{}
		}
		for _, target := range targets {
			target = strings.TrimSpace(target)
			if target == "" {
				continue
			}
			if _, exists := seen[target]; exists {
				continue
			}
			seen[target] = struct{}{}
			normalized[source] = append(normalized[source], target)
		}
		if len(normalized[source]) == 0 {
			delete(normalized, source)
		}
	}
	return normalized
}

func newUpstreamID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err == nil {
		return "ch_" + hex.EncodeToString(buf)
	}
	// crypto/rand 在受支持的平台上不应失败；这里保留一个可追踪的降级值，避免新增渠道没有身份标识。
	return fmt.Sprintf("ch_%d", time.Now().UnixNano())
}

// normalizeUpstreamPriorities 为整个渠道池建立连续且唯一的优先级。
// 优先级不能只覆盖可调度子集，否则禁用渠道恢复后会与现有渠道发生冲突。
func normalizeUpstreamPriorities(upstreams []UpstreamConfig) bool {
	if len(upstreams) == 0 {
		return false
	}

	order := make([]int, len(upstreams))
	for i := range upstreams {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		left, right := order[i], order[j]
		leftPriority := upstreams[left].Priority
		rightPriority := upstreams[right].Priority
		if leftPriority <= 0 {
			leftPriority = left + 1
		}
		if rightPriority <= 0 {
			rightPriority = right + 1
		}
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return left < right
	})

	changed := false
	for priority, index := range order {
		wanted := priority + 1
		if upstreams[index].Priority != wanted {
			upstreams[index].Priority = wanted
			changed = true
		}
	}
	return changed
}

func ensureUpstreamIDs(upstreams []UpstreamConfig) bool {
	seen := make(map[string]struct{}, len(upstreams))
	changed := false
	for i := range upstreams {
		id := strings.TrimSpace(upstreams[i].ID)
		if id == "" {
			id = newUpstreamID()
		} else if _, exists := seen[id]; exists {
			id = newUpstreamID()
		}
		if upstreams[i].ID != id {
			upstreams[i].ID = id
			changed = true
		}
		seen[id] = struct{}{}
	}
	return changed
}

// addUpstreamOp 新增渠道但不改变已有渠道的数组位置。
// 位置索引仍被会话、日志和 URL 健康状态使用，新增只能通过优先级置顶。
func addUpstreamOp(upstreams []UpstreamConfig, upstream UpstreamConfig) ([]UpstreamConfig, AddedUpstream) {
	prepareNewUpstream(&upstream)
	normalizeUpstreamPriorities(upstreams)
	for i := range upstreams {
		upstreams[i].Priority++
	}
	upstream.Priority = 1
	upstreams = append(upstreams, upstream)
	return upstreams, AddedUpstream{ID: upstream.ID, Index: len(upstreams) - 1}
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
		upstream.ModelMapping = normalizeModelMapping(updates.ModelMapping)
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
	if updates.IncludeHistoryThinking != nil {
		upstream.IncludeHistoryThinking = *updates.IncludeHistoryThinking
	}
	if updates.DisablePromptCacheKey != nil {
		upstream.DisablePromptCacheKey = *updates.DisablePromptCacheKey
	}
	if updates.EnablePreviousResponseID != nil {
		upstream.EnablePreviousResponseID = *updates.EnablePreviousResponseID
	}
	return shouldResetMetrics, nil
}

// applyCommonUpdatesToList 应用渠道更新，并在状态池发生变化时同步调整优先级。
func applyCommonUpdatesToList(upstreams []UpstreamConfig, index int, updates UpstreamUpdate, label string) (bool, error) {
	previousPool := channelStatusPool(&upstreams[index])
	shouldResetMetrics, err := applyCommonUpdates(&upstreams[index], index, updates, label)
	if err != nil {
		return false, err
	}
	if previousPool != channelStatusPool(&upstreams[index]) {
		moveChannelToStatusPoolTail(upstreams, index)
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

// removeFromSlice 将渠道替换为删除占位，保留索引稳定性。
// 渠道索引被会话路由、日志和 URL 健康缓存引用，物理删除会使这些状态错绑到后续渠道。
func removeFromSlice(upstreams []UpstreamConfig, index int, label string) ([]UpstreamConfig, *UpstreamConfig, error) {
	if index < 0 || index >= len(upstreams) {
		return upstreams, nil, fmt.Errorf("无效的%s上游索引: %d", label, index)
	}
	if GetChannelStatus(&upstreams[index]) == ChannelStatusDeleted {
		return upstreams, nil, fmt.Errorf("无效的%s上游索引: %d（已删除）", label, index)
	}
	removed := upstreams[index]
	upstreams[index] = UpstreamConfig{
		ID:       removed.ID,
		Name:     removed.Name,
		Priority: removed.Priority,
		Status:   ChannelStatusDeleted,
	}
	moveChannelToStatusPoolTail(upstreams, index)
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
	// UI 仅传入故障转移序列。将未提交渠道按当前顺序补齐，保证全量优先级连续且唯一。
	allOrder := make([]int, 0, len(upstreams))
	allOrder = append(allOrder, order...)
	remaining := make([]int, 0, len(upstreams)-len(order))
	for index := range upstreams {
		if !seen[index] {
			remaining = append(remaining, index)
		}
	}
	sort.SliceStable(remaining, func(i, j int) bool {
		left, right := remaining[i], remaining[j]
		leftPriority := GetChannelPriority(&upstreams[left], left)
		rightPriority := GetChannelPriority(&upstreams[right], right)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return left < right
	})
	allOrder = append(allOrder, remaining...)
	for i, idx := range allOrder {
		upstreams[idx].Priority = i + 1
	}
	log.Printf("[Config-Reorder] 已更新 %s 渠道优先级顺序 (%d 个显式渠道，%d 个总渠道)", label, len(order), len(allOrder))
	return nil
}

func moveChannelToStatusPoolTail(upstreams []UpstreamConfig, targetIndex int) {
	ordered := make([]int, 0, len(upstreams)-1)
	for index := range upstreams {
		if index != targetIndex {
			ordered = append(ordered, index)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		leftPriority := GetChannelPriority(&upstreams[left], left)
		rightPriority := GetChannelPriority(&upstreams[right], right)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return left < right
	})

	active := make([]int, 0, len(ordered)+1)
	suspended := make([]int, 0, len(ordered)+1)
	inactive := make([]int, 0, len(ordered)+1)
	for _, index := range ordered {
		switch channelStatusPool(&upstreams[index]) {
		case 0:
			active = append(active, index)
		case 1:
			suspended = append(suspended, index)
		default:
			inactive = append(inactive, index)
		}
	}
	switch channelStatusPool(&upstreams[targetIndex]) {
	case 0:
		active = append(active, targetIndex)
	case 1:
		suspended = append(suspended, targetIndex)
	default:
		inactive = append(inactive, targetIndex)
	}
	ordered = append(active, suspended...)
	ordered = append(ordered, inactive...)
	for priority, index := range ordered {
		upstreams[index].Priority = priority + 1
	}
}

func channelStatusPool(upstream *UpstreamConfig) int {
	switch GetChannelStatus(upstream) {
	case ChannelStatusSuspended:
		return 1
	case ChannelStatusDisabled, ChannelStatusDeprecated, ChannelStatusDeleted:
		return 2
	default:
		return 0
	}
}

// setStatusOp 设置渠道状态（调用方必须持锁）
func setStatusOp(upstreams []UpstreamConfig, index int, status string, label string) error {
	if index < 0 || index >= len(upstreams) {
		return fmt.Errorf("无效的上游索引: %d", index)
	}
	previousPool := channelStatusPool(&upstreams[index])
	if err := setUpstreamStatus(&upstreams[index], status, time.Now()); err != nil {
		return err
	}
	if previousPool != channelStatusPool(&upstreams[index]) {
		moveChannelToStatusPoolTail(upstreams, index)
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
