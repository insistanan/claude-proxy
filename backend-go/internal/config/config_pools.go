package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

const DefaultChannelPoolID = "default"

// ChannelPool 是同一 API 类型内独立执行故障转移的渠道子池。
// ModelMatcher 为单个大小写无关的 contains 规则；* 为默认兜底池。
type ChannelPool struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ModelMatcher string `json:"modelMatcher"`
	Priority     int    `json:"priority"`
}

type ChannelPoolUpdate struct {
	Name         *string `json:"name"`
	ModelMatcher *string `json:"modelMatcher"`
}

func defaultChannelPool() ChannelPool {
	return ChannelPool{ID: DefaultChannelPoolID, Name: "默认子池", ModelMatcher: "*", Priority: 1}
}

func normalizePoolMatcher(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeChannelPools(pools []ChannelPool) ([]ChannelPool, error) {
	result := make([]ChannelPool, 0, len(pools)+1)
	seenIDs := make(map[string]struct{}, len(pools)+1)
	seenNames := make(map[string]struct{}, len(pools)+1)
	seenMatchers := make(map[string]struct{}, len(pools)+1)
	hasDefault := false

	for _, pool := range pools {
		pool.ID = strings.TrimSpace(pool.ID)
		pool.Name = strings.TrimSpace(pool.Name)
		pool.ModelMatcher = normalizePoolMatcher(pool.ModelMatcher)
		if pool.ID == "" || pool.Name == "" || pool.ModelMatcher == "" {
			return nil, fmt.Errorf("子池的名称和捕获规则不能为空")
		}
		if _, exists := seenIDs[pool.ID]; exists {
			return nil, fmt.Errorf("子池 ID 重复: %s", pool.ID)
		}
		nameKey := strings.ToLower(pool.Name)
		if _, exists := seenNames[nameKey]; exists {
			return nil, fmt.Errorf("子池名称重复: %s", pool.Name)
		}
		if _, exists := seenMatchers[pool.ModelMatcher]; exists {
			return nil, fmt.Errorf("子池捕获规则重复: %s", pool.ModelMatcher)
		}
		if pool.ModelMatcher == "*" {
			if pool.ID != DefaultChannelPoolID || hasDefault {
				return nil, fmt.Errorf("捕获规则 * 仅允许用于默认子池")
			}
			hasDefault = true
		}
		seenIDs[pool.ID] = struct{}{}
		seenNames[nameKey] = struct{}{}
		seenMatchers[pool.ModelMatcher] = struct{}{}
		result = append(result, pool)
	}

	if !hasDefault {
		if _, exists := seenIDs[DefaultChannelPoolID]; exists {
			return nil, fmt.Errorf("默认子池 ID 被占用")
		}
		result = append(result, defaultChannelPool())
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].ModelMatcher == "*" {
			return false
		}
		if result[j].ModelMatcher == "*" {
			return true
		}
		if result[i].Priority != result[j].Priority {
			return result[i].Priority < result[j].Priority
		}
		return result[i].ID < result[j].ID
	})
	for index := range result {
		result[index].Priority = index + 1
	}
	return result, nil
}

// SelectChannelPool 使用最长模糊匹配选择子池；* 仅作没有具体命中时的兜底。
func SelectChannelPool(pools []ChannelPool, model string) (*ChannelPool, error) {
	normalized, err := normalizeChannelPools(pools)
	if err != nil {
		return nil, err
	}
	model = strings.ToLower(strings.TrimSpace(model))
	var selected *ChannelPool
	for index := range normalized {
		pool := &normalized[index]
		if pool.ModelMatcher == "*" || !strings.Contains(model, pool.ModelMatcher) {
			continue
		}
		if selected == nil || len(pool.ModelMatcher) > len(selected.ModelMatcher) ||
			(len(pool.ModelMatcher) == len(selected.ModelMatcher) && pool.Priority < selected.Priority) {
			selected = pool
		}
	}
	if selected != nil {
		copy := *selected
		return &copy, nil
	}
	for index := range normalized {
		if normalized[index].ModelMatcher == "*" {
			copy := normalized[index]
			return &copy, nil
		}
	}
	return nil, fmt.Errorf("未找到默认子池")
}

func newChannelPoolID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err == nil {
		return "pool_" + hex.EncodeToString(buf)
	}
	return "pool_fallback"
}

func (cm *ConfigManager) GetChannelPools(kind string) []ChannelPool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return append([]ChannelPool(nil), cm.channelPoolsLocked(kind)...)
}

func (cm *ConfigManager) channelPoolsLocked(kind string) []ChannelPool {
	switch kind {
	case "messages":
		return cm.config.MessagePools
	case "responses":
		return cm.config.ResponsesPools
	case "gemini":
		return cm.config.GeminiPools
	case "chat":
		return cm.config.ChatPools
	case "images":
		return cm.config.ImagesPools
	default:
		return nil
	}
}

func (cm *ConfigManager) setChannelPoolsLocked(kind string, pools []ChannelPool) error {
	switch kind {
	case "messages":
		cm.config.MessagePools = pools
	case "responses":
		cm.config.ResponsesPools = pools
	case "gemini":
		cm.config.GeminiPools = pools
	case "chat":
		cm.config.ChatPools = pools
	case "images":
		cm.config.ImagesPools = pools
	default:
		return fmt.Errorf("不支持的渠道类型: %s", kind)
	}
	return nil
}

func (cm *ConfigManager) CreateChannelPool(kind string, pool ChannelPool) (ChannelPool, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	previous := append([]ChannelPool(nil), cm.channelPoolsLocked(kind)...)
	pool.ID = newChannelPoolID()
	pool.Priority = len(previous) + 1
	pools, err := normalizeChannelPools(append(previous, pool))
	if err != nil {
		return ChannelPool{}, err
	}
	if err := cm.setChannelPoolsLocked(kind, pools); err != nil {
		return ChannelPool{}, err
	}
	if err := cm.saveConfigLocked(cm.config); err != nil {
		_ = cm.setChannelPoolsLocked(kind, previous)
		return ChannelPool{}, err
	}
	for _, item := range pools {
		if item.ID == pool.ID {
			return item, nil
		}
	}
	return ChannelPool{}, fmt.Errorf("创建子池失败")
}

func (cm *ConfigManager) UpdateChannelPool(kind string, id string, update ChannelPoolUpdate) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	pools := append([]ChannelPool(nil), cm.channelPoolsLocked(kind)...)
	previous := append([]ChannelPool(nil), pools...)
	for index := range pools {
		if pools[index].ID != id {
			continue
		}
		if update.Name != nil {
			pools[index].Name = *update.Name
		}
		if update.ModelMatcher != nil {
			if id == DefaultChannelPoolID {
				return fmt.Errorf("默认子池的捕获规则固定为 *")
			}
			pools[index].ModelMatcher = *update.ModelMatcher
		}
		normalized, err := normalizeChannelPools(pools)
		if err != nil {
			return err
		}
		if err := cm.setChannelPoolsLocked(kind, normalized); err != nil {
			return err
		}
		if err := cm.saveConfigLocked(cm.config); err != nil {
			_ = cm.setChannelPoolsLocked(kind, previous)
			return err
		}
		return nil
	}
	return fmt.Errorf("子池不存在")
}

func (cm *ConfigManager) DeleteChannelPool(kind string, id string) error {
	if id == DefaultChannelPoolID {
		return fmt.Errorf("默认子池不能删除")
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	previous := append([]ChannelPool(nil), cm.channelPoolsLocked(kind)...)
	for _, upstream := range cm.channelUpstreamsLocked(kind) {
		if upstream.PoolID == id {
			return fmt.Errorf("子池仍包含渠道，无法删除")
		}
	}
	pools := cm.channelPoolsLocked(kind)
	filtered := make([]ChannelPool, 0, len(pools)-1)
	found := false
	for _, pool := range pools {
		if pool.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, pool)
	}
	if !found {
		return fmt.Errorf("子池不存在")
	}
	normalized, err := normalizeChannelPools(filtered)
	if err != nil {
		return err
	}
	if err := cm.setChannelPoolsLocked(kind, normalized); err != nil {
		return err
	}
	if err := cm.saveConfigLocked(cm.config); err != nil {
		_ = cm.setChannelPoolsLocked(kind, previous)
		return err
	}
	return nil
}

func (cm *ConfigManager) channelUpstreamsLocked(kind string) []UpstreamConfig {
	switch kind {
	case "messages":
		return cm.config.Upstream
	case "responses":
		return cm.config.ResponsesUpstream
	case "gemini":
		return cm.config.GeminiUpstream
	case "chat":
		return cm.config.ChatUpstream
	case "images":
		return cm.config.ImagesUpstream
	default:
		return nil
	}
}

func ensurePoolsAndAssignments(pools []ChannelPool, upstreams []UpstreamConfig) ([]ChannelPool, bool, error) {
	normalized, err := normalizeChannelPools(pools)
	if err != nil {
		return nil, false, err
	}
	changed := !channelPoolsEqual(normalized, pools)
	validIDs := make(map[string]struct{}, len(normalized))
	for _, pool := range normalized {
		validIDs[pool.ID] = struct{}{}
	}
	for index := range upstreams {
		if _, exists := validIDs[upstreams[index].PoolID]; !exists {
			upstreams[index].PoolID = DefaultChannelPoolID
			changed = true
		}
	}
	return normalized, changed, nil
}

func channelPoolsEqual(left []ChannelPool, right []ChannelPool) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func getFirstActiveWithIndexForModel(upstreams []UpstreamConfig, pools []ChannelPool, label string, model string) (*UpstreamConfig, int, error) {
	pool, err := SelectChannelPool(pools, model)
	if err != nil {
		return nil, -1, err
	}
	bestIndex := -1
	bestPriority := 0
	for index := range upstreams {
		upstream := &upstreams[index]
		poolID := strings.TrimSpace(upstream.PoolID)
		if poolID == "" {
			poolID = DefaultChannelPoolID
		}
		if poolID != pool.ID || !IsChannelSchedulable(upstream) || upstream.ExcludeFromConversation {
			continue
		}
		if GetChannelStatus(upstream) != ChannelStatusActive {
			continue
		}
		priority := GetChannelPriority(upstream, index)
		if bestIndex < 0 || priority < bestPriority || (priority == bestPriority && index < bestIndex) {
			bestIndex = index
			bestPriority = priority
		}
	}
	if bestIndex < 0 {
		return nil, -1, fmt.Errorf("子池 %q 没有可用的%s渠道", pool.Name, label)
	}
	return &upstreams[bestIndex], bestIndex, nil
}

func poolExists(pools []ChannelPool, id string) bool {
	for _, pool := range pools {
		if pool.ID == id {
			return true
		}
	}
	return false
}

// ValidateChannelPoolAssignment 确保渠道只能归属当前 API 类型中存在的子池。
func ValidateChannelPoolAssignment(pools []ChannelPool, poolID string) error {
	if !poolExists(pools, strings.TrimSpace(poolID)) {
		return fmt.Errorf("指定的子池不存在")
	}
	return nil
}
