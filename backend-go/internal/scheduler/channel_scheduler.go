package scheduler

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
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
	imagesMetricsManager     *metrics.MetricsManager // Images 渠道指标
	messagesChannelLogStore  *metrics.ChannelLogStore
	responsesChannelLogStore *metrics.ChannelLogStore
	geminiChannelLogStore    *metrics.ChannelLogStore
	chatChannelLogStore      *metrics.ChannelLogStore
	imagesChannelLogStore    *metrics.ChannelLogStore
	requestLogStore          *metrics.RequestLogStore
	traceAffinity            *session.TraceAffinityManager
	baseURLAffinity          *session.BaseURLAffinityManager
	conversationRegistry     *conversation.Registry
	urlManager               *urlhealth.URLManager   // URL 管理器（非阻塞，动态排序）
	profileManager           *metrics.ProfileManager // 性能画像管理器
	adaptiveScheduler        *AdaptiveScheduler      // 自适应调度器
	// inFlightByKind 记录选渠后、真正发出上游请求前的在途预留。
	// 让并发对话在 StartRequest 之前就能看到彼此占用，从而分摊到不同供应商。
	inFlightByKind map[ChannelKind]map[int]int64
}

// ChannelKind 标识调度器所处理的渠道类型
// 注意：这里的 kind 与 upstream.ServiceType（openai/claude/gemini）不同，
// kind 对应的是本代理对外暴露的一等公民入口：messages / responses / gemini / chat / images。
type ChannelKind string

const (
	ChannelKindMessages  ChannelKind = "messages"
	ChannelKindResponses ChannelKind = "responses"
	ChannelKindGemini    ChannelKind = "gemini"
	ChannelKindChat      ChannelKind = "chat"
	ChannelKindImages    ChannelKind = "images"
)

// NewChannelScheduler 创建多渠道调度器
func NewChannelScheduler(
	cfgManager *config.ConfigManager,
	messagesMetrics *metrics.MetricsManager,
	responsesMetrics *metrics.MetricsManager,
	geminiMetrics *metrics.MetricsManager,
	chatMetrics *metrics.MetricsManager,
	imagesMetrics *metrics.MetricsManager,
	traceAffinity *session.TraceAffinityManager,
	urlMgr *urlhealth.URLManager,
) *ChannelScheduler {
	return &ChannelScheduler{
		configManager:            cfgManager,
		messagesMetricsManager:   messagesMetrics,
		responsesMetricsManager:  responsesMetrics,
		geminiMetricsManager:     geminiMetrics,
		chatMetricsManager:       chatMetrics,
		imagesMetricsManager:     imagesMetrics,
		messagesChannelLogStore:  metrics.NewChannelLogStore(),
		responsesChannelLogStore: metrics.NewChannelLogStore(),
		geminiChannelLogStore:    metrics.NewChannelLogStore(),
		chatChannelLogStore:      metrics.NewChannelLogStore(),
		imagesChannelLogStore:    metrics.NewChannelLogStore(),
		traceAffinity:            traceAffinity,
		baseURLAffinity:          session.NewBaseURLAffinityManager(),
		urlManager:               urlMgr,
		inFlightByKind:           make(map[ChannelKind]map[int]int64),
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
	case ChannelKindImages:
		return s.imagesMetricsManager
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
	case ChannelKindImages:
		return s.imagesChannelLogStore
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
	// Kind 记录选择时的协议类型，便于调用方正确释放/转移在途预留。
	Kind ChannelKind
	// Reserved 表示本次选择是否已占用 in-flight 预留。
	// 调用方在失败重试前应 ReleaseChannelReservation；真正发出上游请求期间应保持预留，请求结束后再释放。
	Reserved bool
}

// reserveAndReturn 原子预留选中渠道的在途计数，避免并发选渠时都看到相同负载。
func (s *ChannelScheduler) reserveAndReturn(result *SelectionResult, kind ChannelKind) *SelectionResult {
	if result == nil {
		return nil
	}
	result.Kind = kind
	if s == nil {
		return result
	}
	s.ReserveChannel(kind, result.ChannelIndex)
	result.Reserved = true
	return result
}

// ReserveChannel 增加渠道在途预留（选渠后、请求结束前）
func (s *ChannelScheduler) ReserveChannel(kind ChannelKind, channelIndex int) {
	if s == nil || channelIndex < 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlightByKind == nil {
		s.inFlightByKind = make(map[ChannelKind]map[int]int64)
	}
	byChannel := s.inFlightByKind[kind]
	if byChannel == nil {
		byChannel = make(map[int]int64)
		s.inFlightByKind[kind] = byChannel
	}
	byChannel[channelIndex]++
}

// ReleaseChannelReservation 释放选渠预留
func (s *ChannelScheduler) ReleaseChannelReservation(kind ChannelKind, channelIndex int) {
	if s == nil || channelIndex < 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	byChannel := s.inFlightByKind[kind]
	if byChannel == nil {
		return
	}
	if byChannel[channelIndex] <= 1 {
		delete(byChannel, channelIndex)
		if len(byChannel) == 0 {
			delete(s.inFlightByKind, kind)
		}
		return
	}
	byChannel[channelIndex]--
}

// GetChannelInFlight 返回选渠预留的在途计数（不含性能画像中的真实 ActiveRequests）
func (s *ChannelScheduler) GetChannelInFlight(kind ChannelKind, channelIndex int) int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if byChannel := s.inFlightByKind[kind]; byChannel != nil {
		return byChannel[channelIndex]
	}
	return 0
}

// SelectChannel 选择最佳渠道
// 优先级: 促销期渠道（忽略Trace亲和） > Trace亲和（仅在无促销时生效） > 自适应调度 > 渠道优先级顺序
// 同一协议下并发新对话会尽量分摊到不同供应商，同时仍遵循优先级、健康与促销规则。
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
		case ChannelKindImages:
			return nil, fmt.Errorf("没有可用的活跃 Images 渠道")
		default:
			return nil, fmt.Errorf("不支持的渠道类型: %s", kind)
		}
	}

	// 图片不再改变最终回答渠道的选择。是否直接处理图片或启用图片理解层，
	// 由选中的渠道配置在请求发送前决定。
	_ = hasImage

	// 获取对应类型的指标管理器
	metricsManager := s.getMetricsManager(kind)

	if userID != "" && registry != nil {
		if override, ok := registry.GetRouteOverride(userID); ok {
			if override.Kind != string(kind) {
				return nil, fmt.Errorf("该对话已固定到 %s 渠道池，当前请求为 %s", override.Kind, kind)
			}
			for _, ch := range activeChannels {
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
				return s.reserveAndReturn(&SelectionResult{
					Upstream:     upstream,
					ChannelIndex: ch.Index,
					Reason:       "conversation_route_override",
				}, kind), nil
			}
			return nil, fmt.Errorf("对话固定渠道 [%d] 当前不可用", override.ChannelIndex)
		}
	}

	// 1. 检查促销期渠道（促销期优先，忽略 Trace 亲和性；同优先级内按在途负载分摊）
	promotedChannels := s.findPromotedChannels(activeChannels, kind)
	var promotedCandidates []ChannelInfo
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
		promotedCandidates = append(promotedCandidates, promotedCh)
	}
	if len(promotedCandidates) > 0 {
		selected := s.pickLeastLoadedChannel(promotedCandidates, kind)
		upstream := s.getUpstreamByIndex(selected.Index, kind)
		failureRate := metricsManager.CalculateChannelFailureRate(upstream.BaseURL, upstream.APIKeys)
		prefix := kindSchedulerLogPrefix(kind)
		log.Printf("[%s-Promotion] 促销期优先选择渠道: [%d] %s (失败率: %.1f%%, inFlight: %d, candidates: %d)",
			prefix, selected.Index, upstream.Name, failureRate*100, s.GetChannelInFlight(kind, selected.Index), len(promotedCandidates))
		return s.reserveAndReturn(&SelectionResult{
			Upstream:     upstream,
			ChannelIndex: selected.Index,
			Reason:       "promotion_priority",
		}, kind), nil
	}

	// 2. 检查 Trace 亲和性（仅在无促销渠道时生效，保证同一会话连续性）
	if userID != "" {
		if preferredIdx, ok := s.traceAffinity.GetPreferredChannelForKind(string(kind), userID); ok {
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
					if upstream == nil || len(upstream.APIKeys) == 0 {
						prefix := kindSchedulerLogPrefix(kind)
						log.Printf("[%s-Affinity] 跳过亲和渠道 [%d]: 无可用密钥 (user: %s)", prefix, preferredIdx, maskUserID(userID))
						affinityInvalidated = true
						continue
					}
					if !metricsManager.IsChannelHealthyWithKeys(upstream.BaseURL, upstream.APIKeys) {
						failureRate := metricsManager.CalculateChannelFailureRate(upstream.BaseURL, upstream.APIKeys)
						prefix := kindSchedulerLogPrefix(kind)
						log.Printf("[%s-Affinity] 跳过亲和渠道 [%d] %s: 不健康 (失败率: %.1f%%, user: %s)", prefix, preferredIdx, ch.Name, failureRate*100, maskUserID(userID))
						affinityInvalidated = true
						continue
					}
					// 渠道健康，使用 Trace 亲和性
					prefix := kindSchedulerLogPrefix(kind)
					log.Printf("[%s-Affinity] 使用 Trace 亲和渠道: [%d] %s (user: %s)", prefix, preferredIdx, ch.Name, maskUserID(userID))
					return s.reserveAndReturn(&SelectionResult{
						Upstream:     upstream,
						ChannelIndex: preferredIdx,
						Reason:       "trace_affinity",
					}, kind), nil
				}
			}
			// 亲和渠道不存在或已失效，清除亲和性记录
			if !foundPreferredChannel || affinityInvalidated {
				s.traceAffinity.RemoveForKind(string(kind), userID)
				prefix := kindSchedulerLogPrefix(kind)
				if affinityInvalidated {
					log.Printf("[%s-Affinity] 清除失效的 Trace 亲和性: user=%s, channel=%d (渠道不健康或状态异常)", prefix, maskUserID(userID), preferredIdx)
				} else {
					log.Printf("[%s-Affinity] 清除失效的 Trace 亲和性: user=%s, channel=%d (渠道不存在)", prefix, maskUserID(userID), preferredIdx)
				}
			}
		}
	}

	// 3. 尝试使用自适应调度器（基于性能画像 + 在途预留）
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
			func(channelIndex int) int64 {
				return s.GetChannelInFlight(kind, channelIndex)
			},
		)
		if result != nil {
			return s.reserveAndReturn(result, kind), nil
		}
		// 自适应调度未找到可用渠道（可能所有渠道都不支持该模型），降级到原有逻辑
		prefix := kindSchedulerLogPrefix(kind)
		log.Printf("[%s-Adaptive] 自适应调度未找到可用渠道，降级到优先级调度", prefix)
	}

	// 4. 按优先级遍历活跃渠道（降级方案）
	// 同优先级内收集候选，再按 in-flight 选负载最低者，避免并发新对话全打到第一家供应商。
	type priorityCandidate struct {
		channel  ChannelInfo
		upstream *config.UpstreamConfig
	}
	var samePriorityCandidates []priorityCandidate
	currentPriority := -1

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

		if len(samePriorityCandidates) == 0 {
			currentPriority = ch.Priority
		} else if ch.Priority != currentPriority {
			// activeChannels 已按优先级排序，遇到下一优先级时停止收集
			break
		}
		samePriorityCandidates = append(samePriorityCandidates, priorityCandidate{
			channel:  ch,
			upstream: upstream,
		})
	}

	if len(samePriorityCandidates) > 0 {
		best := samePriorityCandidates[0]
		bestLoad := s.GetChannelInFlight(kind, best.channel.Index)
		for _, candidate := range samePriorityCandidates[1:] {
			candidateLoad := s.GetChannelInFlight(kind, candidate.channel.Index)
			if candidateLoad < bestLoad || (candidateLoad == bestLoad && candidate.channel.Index < best.channel.Index) {
				best = candidate
				bestLoad = candidateLoad
			}
		}
		prefix := kindSchedulerLogPrefix(kind)
		log.Printf("[%s-Channel] 选择渠道: [%d] %s (配置优先级: %d, 动态分数: %.1f, inFlight: %d, samePriorityCandidates: %d)",
			prefix, best.channel.Index, best.upstream.Name, best.channel.Priority, best.channel.Score, bestLoad, len(samePriorityCandidates))
		return s.reserveAndReturn(&SelectionResult{
			Upstream:     best.upstream,
			ChannelIndex: best.channel.Index,
			Reason:       "priority_order",
		}, kind), nil
	}

	// 5. 所有健康渠道都失败，选择失败率最低的作为降级
	fallback, err := s.selectFallbackChannel(activeChannels, failedChannels, kind)
	if err != nil {
		return nil, err
	}
	return s.reserveAndReturn(fallback, kind), nil
}

// pickLeastLoadedChannel 在候选渠道中选择 in-flight 最低者（用于促销等多候选场景）
func (s *ChannelScheduler) pickLeastLoadedChannel(candidates []ChannelInfo, kind ChannelKind) *ChannelInfo {
	if len(candidates) == 0 {
		return nil
	}
	bestIdx := 0
	bestLoad := s.GetChannelInFlight(kind, candidates[0].Index)
	for i := 1; i < len(candidates); i++ {
		load := s.GetChannelInFlight(kind, candidates[i].Index)
		if load < bestLoad || (load == bestLoad && candidates[i].Index < candidates[bestIdx].Index) {
			bestIdx = i
			bestLoad = load
		}
	}
	return &candidates[bestIdx]
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

// selectFallbackChannel 选择降级渠道（失败率最低的；同失败率时优先 in-flight 更低的）
func (s *ChannelScheduler) selectFallbackChannel(
	activeChannels []ChannelInfo,
	failedChannels map[int]bool,
	kind ChannelKind,
) (*SelectionResult, error) {
	metricsManager := s.getMetricsManager(kind)
	var bestChannel *ChannelInfo
	var bestUpstream *config.UpstreamConfig
	bestFailureRate := float64(2) // 初始化为不可能的值
	bestLoad := int64(1 << 62)

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
		load := s.GetChannelInFlight(kind, ch.Index)
		if failureRate < bestFailureRate ||
			(failureRate == bestFailureRate && load < bestLoad) ||
			(failureRate == bestFailureRate && load == bestLoad && bestChannel != nil && ch.Index < bestChannel.Index) ||
			(failureRate == bestFailureRate && load == bestLoad && bestChannel == nil) {
			bestFailureRate = failureRate
			bestLoad = load
			bestChannel = ch
			bestUpstream = upstream
		}
	}

	if bestChannel != nil && bestUpstream != nil {
		prefix := kindSchedulerLogPrefix(kind)
		log.Printf("[%s-Fallback] 警告: 降级选择渠道: [%d] %s (失败率: %.1f%%, inFlight: %d)",
			prefix, bestChannel.Index, bestUpstream.Name, bestFailureRate*100, bestLoad)
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

// getActiveChannels 获取故障转移序列（按配置优先级排序）。
// 动态分数只用于同优先级渠道，不能越过用户配置的 failover 顺序。
func (s *ChannelScheduler) getActiveChannels(kind ChannelKind) []ChannelInfo {
	cfg := s.configManager.GetConfig()

	var upstreams []config.UpstreamConfig
	switch kind {
	case ChannelKindMessages:
		upstreams = cfg.Upstream
	case ChannelKindResponses:
		upstreams = cfg.ResponsesUpstream
	case ChannelKindGemini:
		upstreams = cfg.GeminiUpstream
	case ChannelKindChat:
		upstreams = cfg.ChatUpstream
	case ChannelKindImages:
		upstreams = cfg.ImagesUpstream
	default:
		return nil
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
				priority = i + 1
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

	// 配置优先级是主顺序；同优先级时再参考动态分数和稳定索引。
	sort.Slice(activeChannels, func(i, j int) bool {
		if activeChannels[i].Priority != activeChannels[j].Priority {
			return activeChannels[i].Priority < activeChannels[j].Priority
		}
		if activeChannels[i].Score != activeChannels[j].Score {
			return activeChannels[i].Score > activeChannels[j].Score
		}
		return activeChannels[i].Index < activeChannels[j].Index
	})

	return activeChannels
}

// getUpstreamByIndex 根据索引获取上游配置
// 注意：返回的是副本，避免指向 slice 元素的指针在 slice 重分配后失效
func (s *ChannelScheduler) getUpstreamByIndex(index int, kind ChannelKind) *config.UpstreamConfig {
	cfg := s.configManager.GetConfig()

	var upstreams []config.UpstreamConfig
	switch kind {
	case ChannelKindMessages:
		upstreams = cfg.Upstream
	case ChannelKindResponses:
		upstreams = cfg.ResponsesUpstream
	case ChannelKindGemini:
		upstreams = cfg.GeminiUpstream
	case ChannelKindChat:
		upstreams = cfg.ChatUpstream
	case ChannelKindImages:
		upstreams = cfg.ImagesUpstream
	default:
		return nil
	}

	if index >= 0 && index < len(upstreams) {
		// 返回副本，避免返回指向 slice 元素的指针
		upstream := upstreams[index]
		return &upstream
	}
	return nil
}

// SelectVisionChannel 通过稳定渠道 ID 解析图片理解层指定的原生图片理解渠道，
// 并为这次内部调用预留在途负载，避免其从渠道调度与实时指标中消失。
func (s *ChannelScheduler) SelectVisionChannel(ctx context.Context, kind ChannelKind, channelID string) (*SelectionResult, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return nil, fmt.Errorf("图片理解层未指定图片理解渠道")
	}

	cfg := s.configManager.GetConfig()
	var upstreams []config.UpstreamConfig
	switch kind {
	case ChannelKindMessages:
		upstreams = cfg.Upstream
	case ChannelKindResponses:
		upstreams = cfg.ResponsesUpstream
	case ChannelKindGemini:
		upstreams = cfg.GeminiUpstream
	case ChannelKindChat:
		upstreams = cfg.ChatUpstream
	case ChannelKindImages:
		upstreams = cfg.ImagesUpstream
	default:
		return nil, fmt.Errorf("不支持的图片理解渠道类型: %s", kind)
	}

	for index := range upstreams {
		upstream := &upstreams[index]
		if upstream.ID != channelID {
			continue
		}
		if !upstream.VisionCapable {
			return nil, fmt.Errorf("渠道 %q 未标记为支持图片理解", upstream.Name)
		}
		if config.GetChannelStatus(upstream) != config.ChannelStatusActive || len(upstream.APIKeys) == 0 {
			return nil, fmt.Errorf("图片理解渠道 %q 当前不可用", upstream.Name)
		}
		metricsManager := s.getMetricsManager(kind)
		if !metricsManager.IsChannelHealthyWithKeys(upstream.BaseURL, upstream.APIKeys) {
			return nil, fmt.Errorf("图片理解渠道 %q 当前不健康", upstream.Name)
		}
		return s.reserveAndReturn(&SelectionResult{
			Upstream:     upstream.Clone(),
			ChannelIndex: index,
			Reason:       "vision_layer",
		}, kind), nil
	}

	return nil, fmt.Errorf("图片理解渠道 %q 不存在", channelID)
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

// RecordRequestConnected 记录已经开始连接上游的请求，用于实时活跃度和调用历史。
func (s *ChannelScheduler) RecordRequestConnected(baseURL, apiKey, model string, kind ChannelKind) uint64 {
	return s.getMetricsManager(kind).RecordRequestConnected(baseURL, apiKey, model)
}

// RecordRequestFinalizeSuccess 回写已连接请求的成功结果与用量。
func (s *ChannelScheduler) RecordRequestFinalizeSuccess(baseURL, apiKey string, requestID uint64, usage *types.Usage, kind ChannelKind) {
	s.getMetricsManager(kind).RecordRequestFinalizeSuccess(baseURL, apiKey, requestID, usage)
}

// RecordRequestFinalizeFailure 回写已连接请求的失败结果。
func (s *ChannelScheduler) RecordRequestFinalizeFailure(baseURL, apiKey string, requestID uint64, kind ChannelKind) {
	s.getMetricsManager(kind).RecordRequestFinalizeFailure(baseURL, apiKey, requestID)
}

// ShouldSuspendKey 返回指定 Key 是否因熔断而不应继续使用。
func (s *ChannelScheduler) ShouldSuspendKey(baseURL, apiKey string, kind ChannelKind) bool {
	return s.getMetricsManager(kind).ShouldSuspendKey(baseURL, apiKey)
}

// SetTraceAffinity 设置 Trace 亲和
func (s *ChannelScheduler) SetTraceAffinity(userID string, channelIndex int) {
	s.SetTraceAffinityForKind(ChannelKindMessages, userID, channelIndex)
}

func (s *ChannelScheduler) SetTraceAffinityForKind(kind ChannelKind, userID string, channelIndex int) {
	if userID != "" {
		s.traceAffinity.SetPreferredChannelForKind(string(kind), userID, channelIndex)
	}
}

// ConsumePromotionCount 消费促销请求次数
// 在请求成功后调用，递减促销计数，到 0 时自动清除促销状态

// GetPreferredBaseURL 获取会话粘滞的 BaseURL（用于 prompt cache 亲和）。
func (s *ChannelScheduler) GetPreferredBaseURL(sessionKey string) (string, bool) {
	if s == nil || s.baseURLAffinity == nil {
		return "", false
	}
	return s.baseURLAffinity.GetPreferredBaseURL(sessionKey)
}

// SetPreferredBaseURL 记录会话最近成功的 BaseURL。
func (s *ChannelScheduler) SetPreferredBaseURL(sessionKey string, baseURL string) {
	if s == nil || s.baseURLAffinity == nil {
		return
	}
	s.baseURLAffinity.SetPreferredBaseURL(sessionKey, baseURL)
}

// PreferBaseURLInResults 将会话偏好的 BaseURL 排到候选列表最前（保持其余相对顺序）。
func PreferBaseURLInResults(results []urlhealth.URLLatencyResult, preferred string) []urlhealth.URLLatencyResult {
	if preferred == "" || len(results) <= 1 {
		return results
	}
	preferredIdx := -1
	for i, result := range results {
		if result.URL == preferred {
			preferredIdx = i
			break
		}
	}
	if preferredIdx <= 0 {
		return results
	}
	reordered := make([]urlhealth.URLLatencyResult, 0, len(results))
	reordered = append(reordered, results[preferredIdx])
	reordered = append(reordered, results[:preferredIdx]...)
	reordered = append(reordered, results[preferredIdx+1:]...)
	return reordered
}

func (s *ChannelScheduler) ConsumePromotionCount(channelIndex int, kind ChannelKind) {
	channelType := "messages"
	switch kind {
	case ChannelKindResponses:
		channelType = "responses"
	case ChannelKindGemini:
		channelType = "gemini"
	case ChannelKindChat:
		channelType = "chat"
	case ChannelKindImages:
		channelType = "images"
	}
	s.configManager.ConsumePromotionCount(channelIndex, channelType)
}

// UpdateTraceAffinity 更新 Trace 亲和时间（续期）
func (s *ChannelScheduler) UpdateTraceAffinity(userID string) {
	s.UpdateTraceAffinityForKind(ChannelKindMessages, userID)
}

func (s *ChannelScheduler) UpdateTraceAffinityForKind(kind ChannelKind, userID string) {
	if userID != "" {
		s.traceAffinity.UpdateLastUsedForKind(string(kind), userID)
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

// GetImagesMetricsManager 获取 Images 渠道指标管理器
func (s *ChannelScheduler) GetImagesMetricsManager() *metrics.MetricsManager {
	return s.imagesMetricsManager
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
	case ChannelKindImages:
		return "Scheduler-Images"
	default:
		return "Scheduler-Unknown"
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
	case ChannelKindImages:
		return 4
	default:
		return -1
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
