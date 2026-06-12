package config

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// ============== 工具函数 ==============

const (
	ChannelStatusActive     = "active"
	ChannelStatusSuspended  = "suspended"
	ChannelStatusDisabled   = "disabled"
	ChannelStatusDeprecated = "deprecated"
	ChannelStatusDeleted    = "deleted"

	temporaryChannelTTL       = 24 * time.Hour
	deprecatedChannelTTL      = 72 * time.Hour
	channelLifecycleTick      = time.Hour
	deprecatedCleanupInterval = 12 * time.Hour
)

// deduplicateStrings 去重字符串切片，保持原始顺序
func deduplicateStrings(items []string) []string {
	if len(items) <= 1 {
		return items
	}
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		if _, exists := seen[item]; !exists {
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}

// deduplicateBaseURLs 去重 BaseURLs，忽略末尾 / 和 # 差异
func deduplicateBaseURLs(urls []string) []string {
	if len(urls) <= 1 {
		return urls
	}
	seen := make(map[string]struct{}, len(urls))
	result := make([]string, 0, len(urls))
	for _, url := range urls {
		normalized := strings.TrimRight(url, "/#")
		if _, exists := seen[normalized]; !exists {
			seen[normalized] = struct{}{}
			result = append(result, url)
		}
	}
	return result
}

// validateLoadBalanceStrategy 验证负载均衡策略
func validateLoadBalanceStrategy(strategy string) error {
	// 只接受 failover 策略（round-robin 和 random 已移除）
	// 为兼容旧配置，仍允许旧值但静默忽略
	if strategy != "failover" && strategy != "round-robin" && strategy != "random" {
		return &ConfigError{Message: "无效的负载均衡策略: " + strategy}
	}
	return nil
}

// ConfigError 配置错误
type ConfigError struct {
	Message string
}

func (e *ConfigError) Error() string {
	return e.Message
}

// ============== 模型重定向 ==============

// StripContextSuffix 剥离 Claude Code 的上下文窗口后缀（如 [1m]）
// 返回：(原始模型名, 是否有后缀)
// 示例：
//
//	"opus[1m]" -> ("opus", true)
//	"claude-opus-4-8[1m]" -> ("claude-opus-4-8", true)
//	"opus" -> ("opus", false)
func StripContextSuffix(model string) (string, bool) {
	model = strings.TrimSpace(model)
	if strings.HasSuffix(model, "[1m]") {
		return strings.TrimSuffix(model, "[1m]"), true
	}
	return model, false
}

// RedirectModelList 模型重定向（返回模型列表，支持多个备选）
// 返回：[]string 重定向后的模型列表（如果没有映射则返回包含原模型的列表）
func RedirectModelList(model string, upstream *UpstreamConfig) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	if upstream.ModelMapping == nil || len(upstream.ModelMapping) == 0 {
		return []string{model}
	}

	// 1. 先尝试精确匹配原始模型名（包括后缀）
	if mapped, ok := upstream.ModelMapping[model]; ok && len(mapped) > 0 {
		return mapped
	}

	// 2. 如果有后缀，尝试剥离后缀后再匹配
	strippedModel, hasSuffix := StripContextSuffix(model)
	if hasSuffix {
		if mapped, ok := upstream.ModelMapping[strippedModel]; ok && len(mapped) > 0 {
			return mapped
		}
	}

	// 3. 模糊匹配：按源模型长度从长到短排序，确保最长匹配优先
	// 例如：同时配置 "codex" 和 "gpt-5.1-codex" 时，"gpt-5.1-codex" 应该先匹配
	type mapping struct {
		source string
		target []string
	}
	mappings := make([]mapping, 0, len(upstream.ModelMapping))
	for source, target := range upstream.ModelMapping {
		mappings = append(mappings, mapping{source, target})
	}
	// 按源模型长度降序排序
	sort.Slice(mappings, func(i, j int) bool {
		return len(mappings[i].source) > len(mappings[j].source)
	})

	// 按排序后的顺序进行模糊匹配（先匹配原始模型，再匹配剥离后的）
	modelToMatch := model
	if hasSuffix {
		modelToMatch = strippedModel
	}

	for _, m := range mappings {
		if strings.Contains(modelToMatch, m.source) || strings.Contains(m.source, modelToMatch) {
			if len(m.target) > 0 {
				return m.target
			}
		}
	}

	return []string{model}
}

// RedirectModel 模型重定向（兼容旧代码，返回第一个匹配的模型）
func RedirectModel(model string, upstream *UpstreamConfig) string {
	models := RedirectModelList(model, upstream)
	if len(models) > 0 {
		return models[0]
	}
	return model
}

func ResolveUpstreamModel(model string, upstream *UpstreamConfig) string {
	model = strings.TrimSpace(model)

	if upstream == nil {
		return model
	}
	if strings.TrimSpace(upstream.DefaultModel) != "" {
		return strings.TrimSpace(upstream.DefaultModel)
	}

	// RedirectModel 内部会处理后缀匹配逻辑
	return RedirectModel(model, upstream)
}

// ResolveUpstreamModelList 解析上游模型列表（支持多个备选）
func ResolveUpstreamModelList(model string, upstream *UpstreamConfig) []string {
	model = strings.TrimSpace(model)

	if upstream == nil {
		return []string{model}
	}
	if strings.TrimSpace(upstream.DefaultModel) != "" {
		return []string{strings.TrimSpace(upstream.DefaultModel)}
	}

	// RedirectModelList 内部会处理后缀匹配逻辑
	return RedirectModelList(model, upstream)
}

// ============== 渠道状态与优先级辅助函数 ==============

// GetChannelStatus 获取渠道状态（带默认值处理）
func GetChannelStatus(upstream *UpstreamConfig) string {
	if upstream.Status == "" {
		return "active"
	}
	return upstream.Status
}

// GetChannelPriority 获取渠道优先级（带默认值处理）
func GetChannelPriority(upstream *UpstreamConfig, index int) int {
	if upstream.Priority == 0 {
		return index
	}
	return upstream.Priority
}

func IsChannelSchedulable(upstream *UpstreamConfig) bool {
	status := GetChannelStatus(upstream)
	return status != ChannelStatusDisabled && status != ChannelStatusDeprecated && status != ChannelStatusDeleted
}

// IsChannelInPromotion 检查渠道是否处于促销期
func IsChannelInPromotion(upstream *UpstreamConfig) bool {
	if upstream.PromotionUntil != nil && time.Now().Before(*upstream.PromotionUntil) {
		return true
	}
	if upstream.PromotionCount > 0 {
		return true
	}
	return false
}

func normalizeChannelStatus(status string) (string, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case "", ChannelStatusActive:
		return ChannelStatusActive, nil
	case ChannelStatusSuspended, ChannelStatusDisabled, ChannelStatusDeprecated, ChannelStatusDeleted:
		return status, nil
	default:
		return "", fmt.Errorf("无效的状态: %s (允许值: active, suspended, disabled, deprecated)", status)
	}
}

func prepareUpstreamLifecycle(upstream *UpstreamConfig, now time.Time) {
	if upstream == nil {
		return
	}
	if upstream.Status == "" {
		upstream.Status = ChannelStatusActive
	}
	if upstream.Status == ChannelStatusDeprecated {
		upstream.Temporary = false
		upstream.TemporaryUntil = nil
		if upstream.DeprecatedAt == nil {
			deprecatedAt := now
			upstream.DeprecatedAt = &deprecatedAt
		}
		return
	}
	if upstream.Status == ChannelStatusDeleted {
		upstream.Temporary = false
		upstream.TemporaryUntil = nil
		upstream.PromotionUntil = nil
		upstream.PromotionCount = 0
		return
	}
	if upstream.Temporary {
		if upstream.TemporaryUntil == nil {
			until := now.Add(temporaryChannelTTL)
			upstream.TemporaryUntil = &until
		}
	} else {
		upstream.TemporaryUntil = nil
	}
	if upstream.Status != ChannelStatusDeprecated {
		upstream.DeprecatedAt = nil
	}
}

func setUpstreamStatus(upstream *UpstreamConfig, status string, now time.Time) error {
	normalized, err := normalizeChannelStatus(status)
	if err != nil {
		return err
	}
	upstream.Status = normalized
	if normalized == ChannelStatusDeprecated {
		upstream.Temporary = false
		upstream.TemporaryUntil = nil
		if upstream.DeprecatedAt == nil {
			deprecatedAt := now
			upstream.DeprecatedAt = &deprecatedAt
		}
	} else {
		upstream.DeprecatedAt = nil
	}
	if normalized == ChannelStatusSuspended || normalized == ChannelStatusDeprecated || normalized == ChannelStatusDeleted {
		upstream.PromotionUntil = nil
		upstream.PromotionCount = 0
	}
	prepareUpstreamLifecycle(upstream, now)
	return nil
}

func reorderProblemChannelsStable(upstreams []UpstreamConfig) bool {
	normal := make([]UpstreamConfig, 0, len(upstreams))
	problem := make([]UpstreamConfig, 0, len(upstreams))
	for _, upstream := range upstreams {
		status := GetChannelStatus(&upstream)
		if status == ChannelStatusSuspended {
			problem = append(problem, upstream)
			continue
		}
		normal = append(normal, upstream)
	}
	reordered := append(normal, problem...)
	changed := false
	for i := range upstreams {
		if upstreams[i].Name != reordered[i].Name || upstreams[i].Status != reordered[i].Status || upstreams[i].Priority != reordered[i].Priority {
			changed = true
		}
		reordered[i].Priority = i + 1
		upstreams[i] = reordered[i]
	}
	return changed
}

func insertClonedUpstream(upstreams []UpstreamConfig, index int, now time.Time) ([]UpstreamConfig, error) {
	if index < 0 || index >= len(upstreams) {
		return nil, fmt.Errorf("无效的上游索引: %d", index)
	}
	clone := *upstreams[index].Clone()
	clone.Name = clone.Name + " - 副本"
	clone.PromotionUntil = nil
	clone.PromotionCount = 0
	if clone.Status == ChannelStatusDeprecated {
		clone.Status = ChannelStatusDisabled
		clone.DeprecatedAt = nil
	}
	prepareUpstreamLifecycle(&clone, now)

	ordered := make([]int, 0, len(upstreams))
	for i := range upstreams {
		ordered = append(ordered, i)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return GetChannelPriority(&upstreams[ordered[i]], ordered[i]) < GetChannelPriority(&upstreams[ordered[j]], ordered[j])
	})

	targetOrder := len(ordered)
	for i, idx := range ordered {
		if idx == index {
			targetOrder = i + 1
			break
		}
	}

	clone.Priority = targetOrder + 1
	next := append([]UpstreamConfig(nil), upstreams...)
	for i := range next {
		next[i].Priority = sortPriorityForDuplicate(next[i].Priority, i, targetOrder)
	}
	next = append(next, clone)
	return next, nil
}

func sortPriorityForDuplicate(priority int, fallbackIndex int, targetOrder int) int {
	if priority == 0 {
		priority = fallbackIndex + 1
	}
	if priority > targetOrder {
		return priority + 1
	}
	return priority
}

func sameTimePtr(a *time.Time, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func lifecycleMetadataChanged(before UpstreamConfig, after UpstreamConfig) bool {
	return before.Status != after.Status ||
		before.Temporary != after.Temporary ||
		!sameTimePtr(before.TemporaryUntil, after.TemporaryUntil) ||
		!sameTimePtr(before.DeprecatedAt, after.DeprecatedAt)
}

func applyLifecycleToUpstreams(upstreams []UpstreamConfig, now time.Time, cleanupDeprecated bool, label string) ([]UpstreamConfig, bool) {
	changed := false
	next := make([]UpstreamConfig, 0, len(upstreams))
	for i := range upstreams {
		upstream := upstreams[i]
		before := upstream
		prepareUpstreamLifecycle(&upstream, now)
		if lifecycleMetadataChanged(before, upstream) {
			changed = true
		}
		if upstream.Temporary && upstream.TemporaryUntil != nil && !now.Before(*upstream.TemporaryUntil) {
			upstream.Status = ChannelStatusDeprecated
			upstream.Temporary = false
			upstream.TemporaryUntil = nil
			deprecatedAt := now
			upstream.DeprecatedAt = &deprecatedAt
			changed = true
			log.Printf("[Config-Lifecycle] %s 渠道 [%d] %s 临时期结束，已移入弃用池", label, i, upstream.Name)
		}
		if cleanupDeprecated && upstream.Status == ChannelStatusDeprecated && upstream.DeprecatedAt != nil && now.Sub(*upstream.DeprecatedAt) > deprecatedChannelTTL {
			upstream.Status = ChannelStatusDeleted
			upstream.Temporary = false
			upstream.TemporaryUntil = nil
			upstream.PromotionUntil = nil
			upstream.PromotionCount = 0
			changed = true
			log.Printf("[Config-Lifecycle] %s 渠道 [%d] %s 已在弃用池超过 3 天，自动清理为删除占位", label, i, upstream.Name)
		}
		next = append(next, upstream)
	}
	return next, changed
}

// ============== UpstreamConfig 方法 ==============

// Clone 深拷贝 UpstreamConfig（用于避免并发修改问题）
// 在多 BaseURL failover 场景下，需要临时修改 BaseURL 字段，
// 使用深拷贝可避免并发请求之间的竞态条件
func (u *UpstreamConfig) Clone() *UpstreamConfig {
	cloned := *u // 浅拷贝

	// 深拷贝切片字段
	if u.BaseURLs != nil {
		cloned.BaseURLs = make([]string, len(u.BaseURLs))
		copy(cloned.BaseURLs, u.BaseURLs)
	}
	if u.APIKeys != nil {
		cloned.APIKeys = make([]string, len(u.APIKeys))
		copy(cloned.APIKeys, u.APIKeys)
	}
	if u.HistoricalAPIKeys != nil {
		cloned.HistoricalAPIKeys = make([]string, len(u.HistoricalAPIKeys))
		copy(cloned.HistoricalAPIKeys, u.HistoricalAPIKeys)
	}
	if u.ModelMapping != nil {
		cloned.ModelMapping = make(map[string][]string, len(u.ModelMapping))
		for k, v := range u.ModelMapping {
			cloned.ModelMapping[k] = make([]string, len(v))
			copy(cloned.ModelMapping[k], v)
		}
	}
	if u.PromotionUntil != nil {
		t := *u.PromotionUntil
		cloned.PromotionUntil = &t
	}
	if u.TemporaryUntil != nil {
		t := *u.TemporaryUntil
		cloned.TemporaryUntil = &t
	}
	if u.DeprecatedAt != nil {
		t := *u.DeprecatedAt
		cloned.DeprecatedAt = &t
	}

	return &cloned
}

// GetEffectiveBaseURL 获取当前应使用的 BaseURL（纯 failover 模式）
// 优先使用 BaseURL 字段（支持调用方临时覆盖），否则从 BaseURLs 数组获取
func (u *UpstreamConfig) GetEffectiveBaseURL() string {
	// 优先使用 BaseURL（可能被调用方临时设置用于指定本次请求的 URL）
	if u.BaseURL != "" {
		return u.BaseURL
	}

	// 回退到 BaseURLs 数组
	if len(u.BaseURLs) > 0 {
		return u.BaseURLs[0]
	}

	return ""
}

// GetAllBaseURLs 获取所有 BaseURL（用于延迟测试）
func (u *UpstreamConfig) GetAllBaseURLs() []string {
	if len(u.BaseURLs) > 0 {
		return u.BaseURLs
	}
	if u.BaseURL != "" {
		return []string{u.BaseURL}
	}
	return nil
}
