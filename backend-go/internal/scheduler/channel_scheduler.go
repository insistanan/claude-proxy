package scheduler

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/conversation"
	"github.com/BenedictKing/claude-proxy/internal/metrics"
	"github.com/BenedictKing/claude-proxy/internal/session"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/urlhealth"
)

// ChannelScheduler 多渠道调度器
type ChannelScheduler struct {
	mu                       sync.RWMutex
	configManager            *config.ConfigManager
	messagesMetricsManager   *metrics.MetricsManager // Messages 渠道指标
	responsesMetricsManager  *metrics.MetricsManager // Responses 渠道指标
	geminiMetricsManager     *metrics.MetricsManager // Gemini 渠道指标
	chatMetricsManager       *metrics.MetricsManager // Chat 渠道指标
	messagesChannelLogStore  *metrics.ChannelLogStore
	responsesChannelLogStore *metrics.ChannelLogStore
	geminiChannelLogStore    *metrics.ChannelLogStore
	chatChannelLogStore      *metrics.ChannelLogStore
	requestLogStore          *metrics.RequestLogStore
	traceAffinity            *session.TraceAffinityManager
	conversationRegistry     *conversation.Registry
	urlManager               *urlhealth.URLManager   // URL 管理器（非阻塞，动态排序）
	profileManager           *metrics.ProfileManager // 性能画像管理器
	adaptiveScheduler        *AdaptiveScheduler      // 自适应调度器
}

// ChannelKind 标识调度器所处理的渠道类型
// 注意：这里的 kind 与 upstream.ServiceType（openai/claude/gemini）不同，
// kind 对应的是本代理对外暴露的一等公民入口：messages / responses / gemini / chat。
type ChannelKind string

const (
	ChannelKindMessages  ChannelKind = "messages"
	ChannelKindResponses ChannelKind = "responses"
	ChannelKindGemini    ChannelKind = "gemini"
	ChannelKindChat      ChannelKind = "chat"
)

// NewChannelScheduler 创建多渠道调度器
func NewChannelScheduler(
	cfgManager *config.ConfigManager,
	messagesMetrics *metrics.MetricsManager,
	responsesMetrics *metrics.MetricsManager,
	geminiMetrics *metrics.MetricsManager,
	chatMetrics *metrics.MetricsManager,
	traceAffinity *session.TraceAffinityManager,
	urlMgr *urlhealth.URLManager,
) *ChannelScheduler {
	return &ChannelScheduler{
		configManager:            cfgManager,
		messagesMetricsManager:   messagesMetrics,
		responsesMetricsManager:  responsesMetrics,
		geminiMetricsManager:     geminiMetrics,
		chatMetricsManager:       chatMetrics,
		messagesChannelLogStore:  metrics.NewChannelLogStore(),
		responsesChannelLogStore: metrics.NewChannelLogStore(),
		geminiChannelLogStore:    metrics.NewChannelLogStore(),
		chatChannelLogStore:      metrics.NewChannelLogStore(),
		traceAffinity:            traceAffinity,
		urlManager:               urlMgr,
	}
}

// getMetricsManager 根据类型获取对应的指标管理器
func (s *ChannelScheduler) getMetricsManager(kind ChannelKind) *metrics.MetricsManager {
	switch kind {
	case ChannelKindResponses:
		return s.responsesMetricsManager
	case ChannelKindGemini:
		return s.geminiMetricsManager
	case ChannelKindChat:
		return s.chatMetricsManager
	default:
		return s.messagesMetricsManager
	}
}

func (s *ChannelScheduler) GetChannelLogStore(kind ChannelKind) *metrics.ChannelLogStore {
	switch kind {
	case ChannelKindResponses:
		return s.responsesChannelLogStore
	case ChannelKindGemini:
		return s.geminiChannelLogStore
	case ChannelKindChat:
		return s.chatChannelLogStore
	default:
		return s.messagesChannelLogStore
	}
}

func (s *ChannelScheduler) SetRequestLogStore(store *metrics.RequestLogStore) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestLogStore = store
}

func (s *ChannelScheduler) GetRequestLogStore() *metrics.RequestLogStore {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.requestLogStore
}

func (s *ChannelScheduler) SetConversationRegistry(registry *conversation.Registry) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conversationRegistry = registry
}

func (s *ChannelScheduler) GetConversationRegistry() *conversation.Registry {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.conversationRegistry
}

// SetProfileManager 设置性能画像管理器
func (s *ChannelScheduler) SetProfileManager(pm *metrics.ProfileManager) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profileManager = pm
}

// GetProfileManager 获取性能画像管理器
func (s *ChannelScheduler) GetProfileManager() *metrics.ProfileManager {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.profileManager
}

// SetAdaptiveScheduler 设置自适应调度器
func (s *ChannelScheduler) SetAdaptiveScheduler(as *AdaptiveScheduler) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adaptiveScheduler = as
}

// GetAdaptiveScheduler 获取自适应调度器
func (s *ChannelScheduler) GetAdaptiveScheduler() *AdaptiveScheduler {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.adaptiveScheduler
}

// SelectionResult 渠道选择结果
type SelectionResult struct {
	Upstream     *config.UpstreamConfig
	ChannelIndex int
	Reason       string // 选择原因（用于日志）
}

// SelectChannel 选择最佳渠道
// 优先级: 促销期渠道 > Trace亲和（促销渠道失败时回退） > 渠道优先级顺序
func (s *ChannelScheduler) SelectChannel(
	ctx context.Context,
	userID string,
	failedChannels map[int]bool,
	kind ChannelKind,
	requestedModel string,
	hasImage bool,
) (*SelectionResult, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}

	// 仅在读取 conversationRegistry 时短暂持锁，提取引用后立即释放
	s.mu.RLock()
	registry := s.conversationRegistry
	s.mu.RUnlock()

	// 获取活跃渠道列表（configManager 自身有独立锁保护）
	activeChannels := s.getActiveChannels(kind)
	if len(activeChannels) == 0 {
		switch kind {
		case ChannelKindGemini:
			return nil, fmt.Errorf("没有可用的活跃 Gemini 渠道")
		case ChannelKindResponses:
			return nil, fmt.Errorf("没有可用的活跃 Responses 渠道")
		case ChannelKindChat:
			return nil, fmt.Errorf("没有可用的活跃 Chat 渠道")
		default:
			return nil, fmt.Errorf("没有可用的活跃 Messages 渠道")
		}
	}

	originalChannels := activeChannels
	if hasImage {
		visionChannels := s.filterVisionChannels(activeChannels, kind)
		prefix := kindSchedulerLogPrefix(kind)
		if len(visionChannels) > 0 {
			activeChannels = visionChannels
			log.Printf("[%s-Vision] 检测到含图请求，Vision 渠道候选数: %d/%d", prefix, len(visionChannels), len(originalChannels))
		} else {
			log.Printf("[%s-Vision] 警告: 检测到含图请求，但没有可用 Vision 渠道，回退全渠道", prefix)
		}
	}

	// 获取对应类型的指标管理器
	metricsManager := s.getMetricsManager(kind)

	if userID != "" && registry != nil {
		if override, ok := registry.GetRouteOverride(userID); ok {
			if override.Kind != string(kind) {
				return nil, fmt.Errorf("该对话已固定到 %s 渠道池，当前请求为 %s", override.Kind, kind)
			}
			for _, ch := range originalChannels {
				if ch.Index != override.ChannelIndex {
					continue
				}
				if failedChannels[ch.Index] {
					return nil, fmt.Errorf("对话固定渠道 [%d] %s 已在本次请求中失败", ch.Index, ch.Name)
				}
				if ch.Status != "active" {
					return nil, fmt.Errorf("对话固定渠道 [%d] %s 当前状态为 %s", ch.Index, ch.Name, ch.Status)
				}
				upstream := s.getUpstreamByIndex(ch.Index, kind)
				if upstream == nil || len(upstream.APIKeys) == 0 {
					return nil, fmt.Errorf("对话固定渠道 [%d] %s 没有可用 API 密钥", ch.Index, ch.Name)
				}
				prefix := kindSchedulerLogPrefix(kind)
				log.Printf("[%s-Override] 对话固定渠道: [%d] %s", prefix, ch.Index, ch.Name)
				return &SelectionResult{
					Upstream:     upstream,
					ChannelIndex: ch.Index,
					Reason:       "conversation_route_override",
				}, nil
			}
			return nil, fmt.Errorf("对话固定渠道 [%d] 当前不可用", override.ChannelIndex)
		}
	}

	// 1. 检查 Trace 亲和性（会话粘性优先，保证同一会话连续性）
	if userID != "" {
		if preferredIdx, ok := s.traceAffinity.GetPreferredChannel(userID); ok {
			foundPreferredChannel := false
			affinityInvalidated := false
			for _, ch := range activeChannels {
				if ch.Index == preferredIdx && !failedChannels[preferredIdx] {
					foundPreferredChannel = true
					// 检查渠道状态：只有 active 状态才使用亲和性
					if ch.Status != "active" {
						prefix := kindSchedulerLogPrefix(kind)
						log.Printf("[%s-Affinity] 跳过亲和渠道 [%d] %s: 状态为 %s (user: %s)", prefix, preferredIdx, ch.Name, ch.Status, maskUserID(userID))
						affinityInvalidated = true
						continue
					}
					// 检查渠道是否健康
					upstream := s.getUpstreamByIndex(preferredIdx, kind)
					if upstream != nil && metricsManager.IsChannelHealthyWithKeys(upstream.BaseURL, upstream.APIKeys) {
						prefix := kindSchedulerLogPrefix(kind)
						log.Printf("[%s-Affinity] Trace亲和选择渠道: [%d] %s (user: %s)", prefix, preferredIdx, upstream.Name, maskUserID(userID))
						return &SelectionResult{
							Upstream:     upstream,
							ChannelIndex: preferredIdx,
							Reason:       "trace_affinity",
						}, nil
					}
					// 亲和渠道不健康，标记为失效
					affinityInvalidated = true
				}
			}
			// 亲和渠道不健康或状态异常时，主动清除亲和记录避免后续请求重复空转
			if affinityInvalidated {
				s.traceAffinity.Remove(userID)
				prefix := kindSchedulerLogPrefix(kind)
				log.Printf("[%s-Affinity] 亲和渠道 [%d] 不可用，已清除亲和记录 (user: %s)", prefix, preferredIdx, maskUserID(userID))
			}
			if hasImage && !foundPreferredChannel {
				prefix := kindSchedulerLogPrefix(kind)
				log.Printf("[%s-Vision] 跳过亲和渠道 [%d]：不在 Vision 候选池中 (user: %s)", prefix, preferredIdx, maskUserID(userID))
			}
		}
	}

	// 2. 检查促销期渠道（仅对新会话或亲和渠道失败后生效）
	promotedChannels := s.findPromotedChannels(activeChannels, kind)
	for _, promotedCh := range promotedChannels {
		if failedChannels[promotedCh.Index] {
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-Promotion] 警告: 促销渠道 [%d] %s 已在本次请求中失败，跳过", prefix, promotedCh.Index, promotedCh.Name)
			continue
		}
		upstream := s.getUpstreamByIndex(promotedCh.Index, kind)
		if upstream == nil || len(upstream.APIKeys) == 0 {
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-Promotion] 警告: 促销渠道 [%d] %s 无可用密钥，跳过", prefix, promotedCh.Index, promotedCh.Name)
			continue
		}
		failureRate := metricsManager.CalculateChannelFailureRate(upstream.BaseURL, upstream.APIKeys)
		prefix := kindSchedulerLogPrefix(kind)
		log.Printf("[%s-Promotion] 促销期优先选择渠道: [%d] %s (失败率: %.1f%%, 无会话亲和或亲和失败)", prefix, promotedCh.Index, upstream.Name, failureRate*100)
		return &SelectionResult{
			Upstream:     upstream,
			ChannelIndex: promotedCh.Index,
			Reason:       "promotion_priority",
		}, nil
	}

	// 3. 使用自适应调度器（如果可用），按模型级别性能画像选择最佳渠道
	s.mu.RLock()
	adaptiveScheduler := s.adaptiveScheduler
	s.mu.RUnlock()

	if adaptiveScheduler != nil {
		result := adaptiveScheduler.SelectBestChannel(
			activeChannels,
			failedChannels,
			kind,
			requestedModel,
			metricsManager.IsChannelHealthyMultiURL,
			s.getUpstreamByIndex,
		)
		if result != nil {
			return result, nil
		}
		// 自适应调度未找到可用渠道（可能所有渠道都不支持该模型），降级到原有逻辑
		prefix := kindSchedulerLogPrefix(kind)
		log.Printf("[%s-Adaptive] 自适应调度未找到可用渠道，降级到优先级调度", prefix)
	}

	// 4. 按优先级遍历活跃渠道（降级方案）
	for _, ch := range activeChannels {
		// 跳过本次请求已经失败的渠道
		if failedChannels[ch.Index] {
			continue
		}

		// 跳过非 active 状态的渠道（suspended 等）
		if ch.Status != "active" {
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-Channel] 跳过非活跃渠道: [%d] %s (状态: %s)", prefix, ch.Index, ch.Name, ch.Status)
			continue
		}

		upstream := s.getUpstreamByIndex(ch.Index, kind)
		if upstream == nil || len(upstream.APIKeys) == 0 {
			continue
		}
		if requestedModel != "" && len(config.ResolveUpstreamModelList(requestedModel, upstream)) == 0 {
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-Channel] 跳过不支持模型的渠道: [%d] %s (模型: %s)", prefix, ch.Index, ch.Name, requestedModel)
			continue
		}

		// 跳过失败率过高的渠道（已熔断或即将熔断）
		if !metricsManager.IsChannelHealthyWithKeys(upstream.BaseURL, upstream.APIKeys) {
			failureRate := metricsManager.CalculateChannelFailureRate(upstream.BaseURL, upstream.APIKeys)
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-Channel] 警告: 跳过不健康渠道: [%d] %s (失败率: %.1f%%)", prefix, ch.Index, ch.Name, failureRate*100)
			continue
		}

		prefix := kindSchedulerLogPrefix(kind)
		log.Printf("[%s-Channel] 选择渠道: [%d] %s (配置优先级: %d, 动态分数: %.1f)", prefix, ch.Index, upstream.Name, ch.Priority, ch.Score)
		return &SelectionResult{
			Upstream:     upstream,
			ChannelIndex: ch.Index,
			Reason:       "priority_order",
		}, nil
	}

	// 3. 所有健康渠道都失败，选择失败率最低的作为降级
	return s.selectFallbackChannel(activeChannels, failedChannels, kind)
}

func (s *ChannelScheduler) filterVisionChannels(channels []ChannelInfo, kind ChannelKind) []ChannelInfo {
	visionChannels := make([]ChannelInfo, 0, len(channels))
	for _, ch := range channels {
		upstream := s.getUpstreamByIndex(ch.Index, kind)
		if upstream != nil && upstream.VisionCapable {
			visionChannels = append(visionChannels, ch)
		}
	}
	return visionChannels
}

// findPromotedChannels 查找所有处于促销期的渠道（按优先级排序）
func (s *ChannelScheduler) findPromotedChannels(activeChannels []ChannelInfo, kind ChannelKind) []ChannelInfo {
	var promoted []ChannelInfo
	for i := range activeChannels {
		ch := &activeChannels[i]
		if ch.Status != "active" {
			continue
		}
		upstream := s.getUpstreamByIndex(ch.Index, kind)
		if upstream != nil && config.IsChannelInPromotion(upstream) {
			promoted = append(promoted, *ch)
		}
	}
	return promoted
}

// selectFallbackChannel 选择降级渠道（失败率最低的）
func (s *ChannelScheduler) selectFallbackChannel(
	activeChannels []ChannelInfo,
	failedChannels map[int]bool,
	kind ChannelKind,
) (*SelectionResult, error) {
	metricsManager := s.getMetricsManager(kind)
	var bestChannel *ChannelInfo
	var bestUpstream *config.UpstreamConfig
	bestFailureRate := float64(2) // 初始化为不可能的值

	for i := range activeChannels {
		ch := &activeChannels[i]
		if failedChannels[ch.Index] {
			continue
		}
		// 跳过非 active 状态的渠道
		if ch.Status != "active" {
			continue
		}

		upstream := s.getUpstreamByIndex(ch.Index, kind)
		if upstream == nil || len(upstream.APIKeys) == 0 {
			continue
		}

		failureRate := metricsManager.CalculateChannelFailureRate(upstream.BaseURL, upstream.APIKeys)
		if failureRate < bestFailureRate {
			bestFailureRate = failureRate
			bestChannel = ch
			bestUpstream = upstream
		}
	}

	if bestChannel != nil && bestUpstream != nil {
		prefix := kindSchedulerLogPrefix(kind)
		log.Printf("[%s-Fallback] 警告: 降级选择渠道: [%d] %s (失败率: %.1f%%)",
			prefix, bestChannel.Index, bestUpstream.Name, bestFailureRate*100)
		return &SelectionResult{
			Upstream:     bestUpstream,
			ChannelIndex: bestChannel.Index,
			Reason:       "fallback",
		}, nil
	}

	return nil, fmt.Errorf("所有渠道都不可用")
}

// ChannelInfo 渠道信息（用于排序）
type ChannelInfo struct {
	Index    int
	Name     string
	Priority int
	Status   string
	Score    float64 // 动态分数（基于失败率），分数越高优先级越高
}

// calculateChannelScore 计算渠道分数（基于失败率）
// 分数范围: 0-100，分数越高优先级越高
// - 无历史数据：100 分（新渠道给满分）
// - 失败率 0%：100 分
// - 失败率 20%：80 分
// - 失败率 50%：50 分
// - 失败率 80%：20 分
// - 失败率 100%：0 分
func (s *ChannelScheduler) calculateChannelScore(upstream *config.UpstreamConfig, kind ChannelKind) float64 {
	if upstream == nil || len(upstream.APIKeys) == 0 {
		return 0
	}

	metricsManager := s.getMetricsManager(kind)
	failureRate := metricsManager.CalculateChannelFailureRate(upstream.BaseURL, upstream.APIKeys)

	// 计算基础分数：100 * (1 - failureRate)
	baseScore := 100 * (1 - failureRate)

	// 检查请求数，如果请求数小于 5，给予新渠道奖励（避免因样本不足被低估）
	requestCount := metricsManager.GetChannelRequestCount(upstream.BaseURL, upstream.APIKeys)
	if requestCount < 5 {
		// 新渠道给予满分
		return 100
	}

	return baseScore
}

// getActiveChannels 获取活跃渠道列表（按动态分数排序）
func (s *ChannelScheduler) getActiveChannels(kind ChannelKind) []ChannelInfo {
	cfg := s.configManager.GetConfig()

	var upstreams []config.UpstreamConfig
	switch kind {
	case ChannelKindResponses:
		upstreams = cfg.ResponsesUpstream
	case ChannelKindGemini:
		upstreams = cfg.GeminiUpstream
	case ChannelKindChat:
		upstreams = cfg.ChatUpstream
	default:
		upstreams = cfg.Upstream
	}

	// 筛选活跃渠道
	var activeChannels []ChannelInfo
	for i, upstream := range upstreams {
		status := upstream.Status
		if status == "" {
			status = "active" // 默认为活跃
		}

		// 只选择故障转移序列中的渠道（suspended 也显示在序列中，备用/弃用/删除占位排除）
		if config.IsChannelSchedulable(&upstream) {
			priority := upstream.Priority
			if priority == 0 {
				priority = i // 默认优先级为索引
			}

			// 计算渠道动态分数
			upstreamCopy := upstream
			score := s.calculateChannelScore(&upstreamCopy, kind)

			activeChannels = append(activeChannels, ChannelInfo{
				Index:    i,
				Name:     upstream.Name,
				Priority: priority,
				Status:   status,
				Score:    score,
			})
		}
	}

	// 按动态分数排序（分数越高优先级越高），分数相同时按配置优先级排序
	sort.Slice(activeChannels, func(i, j int) bool {
		// 优先按分数排序（降序）
		if activeChannels[i].Score != activeChannels[j].Score {
			return activeChannels[i].Score > activeChannels[j].Score
		}
		// 分数相同时按配置优先级排序（升序）
		return activeChannels[i].Priority < activeChannels[j].Priority
	})

	return activeChannels
}

// getUpstreamByIndex 根据索引获取上游配置
// 注意：返回的是副本，避免指向 slice 元素的指针在 slice 重分配后失效
func (s *ChannelScheduler) getUpstreamByIndex(index int, kind ChannelKind) *config.UpstreamConfig {
	cfg := s.configManager.GetConfig()

	var upstreams []config.UpstreamConfig
	switch kind {
	case ChannelKindResponses:
		upstreams = cfg.ResponsesUpstream
	case ChannelKindGemini:
		upstreams = cfg.GeminiUpstream
	case ChannelKindChat:
		upstreams = cfg.ChatUpstream
	default:
		upstreams = cfg.Upstream
	}

	if index >= 0 && index < len(upstreams) {
		// 返回副本，避免返回指向 slice 元素的指针
		upstream := upstreams[index]
		return &upstream
	}
	return nil
}

// RecordSuccess 记录渠道成功（使用 baseURL + apiKey）
func (s *ChannelScheduler) RecordSuccess(baseURL, apiKey string, kind ChannelKind) {
	s.getMetricsManager(kind).RecordSuccess(baseURL, apiKey)
}

// RecordSuccessWithUsage 记录渠道成功（带 Usage 数据）
func (s *ChannelScheduler) RecordSuccessWithUsage(baseURL, apiKey string, usage *types.Usage, kind ChannelKind) {
	s.getMetricsManager(kind).RecordSuccessWithUsage(baseURL, apiKey, usage)
}

// RecordFailure 记录渠道失败（使用 baseURL + apiKey）
func (s *ChannelScheduler) RecordFailure(baseURL, apiKey string, kind ChannelKind) {
	s.getMetricsManager(kind).RecordFailure(baseURL, apiKey)
}

// RecordRequestStart 记录请求开始
func (s *ChannelScheduler) RecordRequestStart(baseURL, apiKey string, kind ChannelKind) {
	s.getMetricsManager(kind).RecordRequestStart(baseURL, apiKey)
}

// RecordRequestEnd 记录请求结束
func (s *ChannelScheduler) RecordRequestEnd(baseURL, apiKey string, kind ChannelKind) {
	s.getMetricsManager(kind).RecordRequestEnd(baseURL, apiKey)
}

// SetTraceAffinity 设置 Trace 亲和
func (s *ChannelScheduler) SetTraceAffinity(userID string, channelIndex int) {
	if userID != "" {
		s.traceAffinity.SetPreferredChannel(userID, channelIndex)
	}
}

// ConsumePromotionCount 消费促销请求次数
// 在请求成功后调用，递减促销计数，到 0 时自动清除促销状态
func (s *ChannelScheduler) ConsumePromotionCount(channelIndex int, kind ChannelKind) {
	channelType := "messages"
	switch kind {
	case ChannelKindResponses:
		channelType = "responses"
	case ChannelKindGemini:
		channelType = "gemini"
	case ChannelKindChat:
		channelType = "chat"
	}
	s.configManager.ConsumePromotionCount(channelIndex, channelType)
}

// UpdateTraceAffinity 更新 Trace 亲和时间（续期）
func (s *ChannelScheduler) UpdateTraceAffinity(userID string) {
	if userID != "" {
		s.traceAffinity.UpdateLastUsed(userID)
	}
}

func (s *ChannelScheduler) ValidateFixedChannel(userID string, kind ChannelKind, channelIndex int) error {
	if userID == "" || s == nil {
		return nil
	}
	s.mu.RLock()
	registry := s.conversationRegistry
	s.mu.RUnlock()
	if registry == nil {
		return nil
	}
	override, ok := registry.GetRouteOverride(userID)
	if !ok {
		return nil
	}
	if override.Kind != string(kind) {
		return fmt.Errorf("该对话已固定到 %s 渠道池，当前请求为 %s", override.Kind, kind)
	}
	if override.ChannelIndex != channelIndex {
		return fmt.Errorf("该对话已固定到渠道 [%d]，当前命中的渠道为 [%d]", override.ChannelIndex, channelIndex)
	}
	return nil
}

func (s *ChannelScheduler) MarkConversationSuccess(userID string, kind ChannelKind, channelIndex int, channelName string) {
	if userID == "" || s == nil {
		return
	}
	s.mu.RLock()
	registry := s.conversationRegistry
	s.mu.RUnlock()
	if registry == nil {
		return
	}
	registry.MarkSuccess(userID, string(kind), channelIndex, channelName)
}

func (s *ChannelScheduler) MarkConversationAttempt(userID string, kind ChannelKind, channelIndex int, channelName string, requestedModel string, resolvedModel string, stream bool) {
	if userID == "" || s == nil {
		return
	}
	s.mu.RLock()
	registry := s.conversationRegistry
	s.mu.RUnlock()
	if registry == nil {
		return
	}
	registry.MarkAttempt(userID, string(kind), channelIndex, channelName, requestedModel, resolvedModel, stream)
}

func (s *ChannelScheduler) MarkConversationFailure(userID string, kind ChannelKind, errorMessage string) {
	if userID == "" || s == nil {
		return
	}
	s.mu.RLock()
	registry := s.conversationRegistry
	s.mu.RUnlock()
	if registry == nil {
		return
	}
	registry.MarkFailure(userID, string(kind), errorMessage)
}

func (s *ChannelScheduler) MarkConversationComplete(userID string, kind ChannelKind) {
	if userID == "" || s == nil {
		return
	}
	s.mu.RLock()
	registry := s.conversationRegistry
	s.mu.RUnlock()
	if registry == nil {
		return
	}
	registry.MarkComplete(userID, string(kind))
}

// GetMessagesMetricsManager 获取 Messages 渠道指标管理器
func (s *ChannelScheduler) GetMessagesMetricsManager() *metrics.MetricsManager {
	return s.messagesMetricsManager
}

// GetResponsesMetricsManager 获取 Responses 渠道指标管理器
func (s *ChannelScheduler) GetResponsesMetricsManager() *metrics.MetricsManager {
	return s.responsesMetricsManager
}

// GetGeminiMetricsManager 获取 Gemini 渠道指标管理器
func (s *ChannelScheduler) GetGeminiMetricsManager() *metrics.MetricsManager {
	return s.geminiMetricsManager
}

// GetChatMetricsManager 获取 Chat 渠道指标管理器
func (s *ChannelScheduler) GetChatMetricsManager() *metrics.MetricsManager {
	return s.chatMetricsManager
}

// GetTraceAffinityManager 获取 Trace 亲和性管理器
func (s *ChannelScheduler) GetTraceAffinityManager() *session.TraceAffinityManager {
	return s.traceAffinity
}

// ResetChannelMetrics 重置渠道所有 Key 的熔断/失败状态（保留历史统计）
// 用于：1) 手动恢复熔断 2) 更换 API Key 后重置熔断状态
func (s *ChannelScheduler) ResetChannelMetrics(channelIndex int, kind ChannelKind) {
	upstream := s.getUpstreamByIndex(channelIndex, kind)
	if upstream == nil {
		return
	}
	metricsManager := s.getMetricsManager(kind)
	for _, baseURL := range upstream.GetAllBaseURLs() {
		for _, apiKey := range upstream.APIKeys {
			metricsManager.ResetKeyFailureState(baseURL, apiKey)
		}
	}
	prefix := kindSchedulerLogPrefix(kind)
	log.Printf("[%s-Reset] 渠道 [%d] %s 的熔断状态已重置（保留历史统计）", prefix, channelIndex, upstream.Name)
}

// ResetKeyMetrics 重置单个 Key 的指标
func (s *ChannelScheduler) ResetKeyMetrics(baseURL, apiKey string, kind ChannelKind) {
	s.getMetricsManager(kind).ResetKey(baseURL, apiKey)
}

// DeleteChannelMetrics 删除渠道的所有指标数据（内存 + 持久化）
// 用于删除渠道时清理相关的统计数据
func (s *ChannelScheduler) DeleteChannelMetrics(upstream *config.UpstreamConfig, kind ChannelKind) {
	if upstream == nil {
		return
	}
	metricsManager := s.getMetricsManager(kind)
	// 合并活跃 Key 和历史 Key，一起清理
	allKeys := append([]string{}, upstream.APIKeys...)
	allKeys = append(allKeys, upstream.HistoricalAPIKeys...)
	// MetricsManager 内部已有 apiType，无需外部传递
	metricsManager.DeleteChannelMetrics(upstream.GetAllBaseURLs(), allKeys)
	prefix := kindSchedulerLogPrefix(kind)
	log.Printf("[%s-Delete] 渠道 %s 的指标数据已清理", prefix, upstream.Name)
}

// GetActiveChannelCount 获取活跃渠道数量
func (s *ChannelScheduler) GetActiveChannelCount(kind ChannelKind) int {
	return len(s.getActiveChannels(kind))
}

// IsMultiChannelMode 判断是否为多渠道模式
func (s *ChannelScheduler) IsMultiChannelMode(kind ChannelKind) bool {
	return s.GetActiveChannelCount(kind) > 1
}

// maskUserID 掩码 user_id（保护隐私）
func maskUserID(userID string) string {
	if len(userID) <= 16 {
		return "***"
	}
	return userID[:8] + "***" + userID[len(userID)-4:]
}

// GetSortedURLsForChannel 获取渠道排序后的 URL 列表（非阻塞，立即返回）
// 返回按动态排序的 URL 结果列表，包含原始索引用于指标记录
func (s *ChannelScheduler) GetSortedURLsForChannel(
	kind ChannelKind,
	channelIndex int,
	urls []string,
) []urlhealth.URLLatencyResult {
	if s.urlManager == nil || len(urls) <= 1 {
		// 无 URL 管理器或单 URL，返回默认结果
		results := make([]urlhealth.URLLatencyResult, len(urls))
		for i, url := range urls {
			results[i] = urlhealth.URLLatencyResult{
				URL:         url,
				OriginalIdx: i,
				Success:     true,
			}
		}
		return results
	}
	return s.urlManager.GetSortedURLs(urlManagerChannelKey(kind, channelIndex), urls)
}

// MarkURLSuccess 标记 URL 成功
func (s *ChannelScheduler) MarkURLSuccess(kind ChannelKind, channelIndex int, url string) {
	if s.urlManager != nil {
		s.urlManager.MarkSuccess(urlManagerChannelKey(kind, channelIndex), url)
	}
}

// MarkURLFailure 标记 URL 失败，触发动态排序
func (s *ChannelScheduler) MarkURLFailure(kind ChannelKind, channelIndex int, url string) {
	if s.urlManager != nil {
		s.urlManager.MarkFailure(urlManagerChannelKey(kind, channelIndex), url)
	}
}

// InvalidateURLCache 使渠道 URL 状态失效
func (s *ChannelScheduler) InvalidateURLCache(kind ChannelKind, channelIndex int) {
	if s.urlManager != nil {
		s.urlManager.InvalidateChannel(urlManagerChannelKey(kind, channelIndex))
	}
}

// GetURLManagerStats 获取 URL 管理器统计
func (s *ChannelScheduler) GetURLManagerStats() map[string]interface{} {
	if s.urlManager != nil {
		return s.urlManager.GetStats()
	}
	return nil
}

func kindSchedulerLogPrefix(kind ChannelKind) string {
	switch kind {
	case ChannelKindResponses:
		return "Scheduler-Responses"
	case ChannelKindGemini:
		return "Scheduler-Gemini"
	case ChannelKindChat:
		return "Scheduler-Chat"
	default:
		return "Scheduler"
	}
}

func urlManagerChannelKey(kind ChannelKind, channelIndex int) int {
	const stride = 1_000_000
	return urlManagerChannelKeyOrdinal(kind)*stride + channelIndex
}

func urlManagerChannelKeyOrdinal(kind ChannelKind) int {
	switch kind {
	case ChannelKindResponses:
		return 1
	case ChannelKindGemini:
		return 2
	case ChannelKindChat:
		return 3
	default:
		return 0
	}
}

// TODO: SetProfileManager, GetProfileManager, SetAdaptiveScheduler（ProfileManager 未实现）
// // SetProfileManager 设置性能画像管理器
// func (s *ChannelScheduler) SetProfileManager(pm *metrics.ProfileManager) {
// 	if s == nil {
// 		return
// 	}
// 	s.mu.Lock()
// 	defer s.mu.Unlock()
// 	s.profileManager = pm
// }

// // GetProfileManager 获取性能画像管理器
// func (s *ChannelScheduler) GetProfileManager() *metrics.ProfileManager {
// 	if s == nil {
// 		return nil
// 	}
// 	s.mu.RLock()
// 	defer s.mu.RUnlock()
// 	return s.profileManager
// }

// // SetAdaptiveScheduler 设置自适应调度器
// func (s *ChannelScheduler) SetAdaptiveScheduler(as *AdaptiveScheduler) {
// 	if s == nil {
// 		return
// 	}
// 	s.mu.Lock()
// 	defer s.mu.Unlock()
// 	s.adaptiveScheduler = as
// }
