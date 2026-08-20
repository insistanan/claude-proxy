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
	mu                   sync.RWMutex
	configManager        *config.ConfigManager
	metricsManagers      map[ChannelKind]*metrics.MetricsManager // 各渠道类型对应的指标管理器
	channelLogStores     map[ChannelKind]*metrics.ChannelLogStore
	requestLogStore      *metrics.RequestLogStore
	traceAffinity        *session.TraceAffinityManager
	baseURLAffinity      *session.BaseURLAffinityManager
	conversationRegistry *conversation.Registry
	urlManager           *urlhealth.URLManager   // URL 管理器（非阻塞，动态排序）
	profileManager       *metrics.ProfileManager // 性能画像管理器
	adaptiveScheduler    *AdaptiveScheduler      // 自适应调度器
	// inFlightByKind 记录选渠后、真正发出上游请求前的在途预留。
	// 让并发对话在 StartRequest 之前就能看到彼此占用，从而分摊到不同供应商。
	inFlightByKind map[ChannelKind]map[int]int64

	// stopOnce 保证 Stop 幂等：下属组件的 Stop（BaseURLAffinityManager 除外）
	// 是裸 close(channel)，重复调用会 panic。
	stopOnce sync.Once
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
		configManager: cfgManager,
		metricsManagers: map[ChannelKind]*metrics.MetricsManager{
			ChannelKindMessages:  messagesMetrics,
			ChannelKindResponses: responsesMetrics,
			ChannelKindGemini:    geminiMetrics,
			ChannelKindChat:      chatMetrics,
			ChannelKindImages:    imagesMetrics,
		},
		channelLogStores: map[ChannelKind]*metrics.ChannelLogStore{
			ChannelKindMessages:  metrics.NewChannelLogStore(),
			ChannelKindResponses: metrics.NewChannelLogStore(),
			ChannelKindGemini:    metrics.NewChannelLogStore(),
			ChannelKindChat:      metrics.NewChannelLogStore(),
			ChannelKindImages:    metrics.NewChannelLogStore(),
		},
		traceAffinity:   traceAffinity,
		baseURLAffinity: session.NewBaseURLAffinityManager(),
		urlManager:      urlMgr,
		inFlightByKind:  make(map[ChannelKind]map[int]int64),
	}
}

// getMetricsManager 根据类型获取对应的指标管理器
func (s *ChannelScheduler) getMetricsManager(kind ChannelKind) *metrics.MetricsManager {
	return s.metricsManagers[kind]
}

// Stop 停止调度器聚合的后台组件（各 MetricsManager 清理循环、Trace/BaseURL
// 亲和清理、性能画像更新），消除进程关闭后残留的 ticker goroutine。
// 幂等，可安全多次调用；必须在 main 关闭持久化存储（metricsStore 等）之前
// 调用，保证"先停数据生产者、再关存储"的顺序。
// 不含 conversationRegistry / sessionManager 等由 main 直接创建并 defer
// 停止的组件；AdaptiveScheduler 与 URLManager 为纯内存计算，无后台循环。
func (s *ChannelScheduler) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		for _, mm := range s.metricsManagers {
			if mm != nil {
				mm.Stop()
			}
		}
		if s.traceAffinity != nil {
			s.traceAffinity.Stop()
		}
		if s.baseURLAffinity != nil {
			s.baseURLAffinity.Stop()
		}
		if s.profileManager != nil {
			s.profileManager.Stop()
		}
	})
}

func (s *ChannelScheduler) GetChannelLogStore(kind ChannelKind) *metrics.ChannelLogStore {
	return s.channelLogStores[kind]
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
//
// 图片不参与选渠：是否直接处理图片或启用图片理解层，由被选中渠道的配置在
// 请求发送前决定（见 prepareRequestForUpstream → visionlayer.PrepareRequest）。
func (s *ChannelScheduler) SelectChannel(
	ctx context.Context,
	userID string,
	failedChannels map[int]bool,
	kind ChannelKind,
	requestedModel string,
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

	poolRoute, err := config.SelectChannelPoolRoute(s.getChannelPools(kind), requestedModel)
	if err != nil {
		return nil, fmt.Errorf("选择 %s 分组失败: %w", kind, err)
	}

	// 严格按“命中分组 -> 兜底分组”的顺序选择。只有前一分组没有可实际尝试的渠道时，
	// 才进入下一分组，避免兜底分组中的促销或低负载渠道抢占正常模型路由。
	poolIDs := make([]string, 0, len(poolRoute))
	for _, pool := range poolRoute {
		poolIDs = append(poolIDs, pool.ID)
	}
	channelsByPool := s.getActiveChannelsByPool(kind, poolIDs)
	selectedPool := poolRoute[len(poolRoute)-1]
	var activeChannels []ChannelInfo
	var routedChannels []ChannelInfo
	foundAttemptableGroup := false
	for _, pool := range poolRoute {
		channels := channelsByPool[pool.ID]
		routedChannels = append(routedChannels, channels...)
		if !foundAttemptableGroup && s.hasAttemptableChannel(channels, failedChannels, kind, requestedModel) {
			selectedPool = pool
			activeChannels = channels
			foundAttemptableGroup = true
		}
	}
	if !foundAttemptableGroup {
		activeChannels = channelsByPool[selectedPool.ID]
	}
	if len(poolRoute) > 1 && selectedPool.ID == config.DefaultChannelPoolID {
		prefix := kindSchedulerLogPrefix(kind)
		log.Printf("[%s-GroupFallback] 命中分组已无可用渠道，切换到兜底分组", prefix)
	}

	// 图片不参与选渠：是否直接处理图片或启用图片理解层，由被选中渠道的配置在
	// 请求发送前决定（见 prepareRequestForUpstream → visionlayer.PrepareRequest）。

	// 获取对应类型的指标管理器
	metricsManager := s.getMetricsManager(kind)

	if selected, hasOverride, err := s.selectConversationRouteOverride(userID, kind, routedChannels, failedChannels, registry); err != nil {
		return nil, err
	} else if hasOverride {
		return s.reserveAndReturn(selected, kind), nil
	}

	// 1. 检查当前分组的促销渠道（促销期优先，忽略 Trace 亲和性；同优先级内按在途负载分摊）
	if selected := s.selectPromotedChannel(activeChannels, failedChannels, kind, "promotion_priority"); selected != nil {
		return s.reserveAndReturn(selected, kind), nil
	}

	if len(activeChannels) == 0 {
		return nil, noActiveChannelError(kind)
	}

	// 2. 检查 Trace 亲和性（仅在无促销渠道时生效，保证同一会话连续性）
	if selected := s.selectTraceAffinity(activeChannels, failedChannels, kind, userID, metricsManager); selected != nil {
		return s.reserveAndReturn(selected, kind), nil
	}

	// 3. 尝试使用自适应调度器（基于性能画像 + 在途预留）
	if selected := s.selectAdaptiveChannel(activeChannels, failedChannels, kind, requestedModel, metricsManager); selected != nil {
		return s.reserveAndReturn(selected, kind), nil
	}

	// 4. 按优先级遍历活跃渠道（降级方案）
	// 同优先级内收集候选，再按 in-flight 选负载最低者，避免并发新对话全打到第一家供应商。
	if selected := s.selectPriorityChannel(activeChannels, failedChannels, kind, requestedModel, metricsManager); selected != nil {
		return s.reserveAndReturn(selected, kind), nil
	}

	// 5. 当前分组所有健康渠道都失败，选择失败率最低的作为降级
	fallback, err := s.selectFallbackChannel(activeChannels, failedChannels, kind)
	if err != nil {
		return nil, err
	}
	return s.reserveAndReturn(fallback, kind), nil
}

func (s *ChannelScheduler) selectConversationRouteOverride(
	userID string,
	kind ChannelKind,
	routedChannels []ChannelInfo,
	failedChannels map[int]bool,
	registry *conversation.Registry,
) (*SelectionResult, bool, error) {
	if userID == "" || registry == nil {
		return nil, false, nil
	}
	override, ok := registry.GetRouteOverride(userID)
	if !ok {
		return nil, false, nil
	}
	if override.Kind != string(kind) {
		return nil, true, fmt.Errorf("该对话已固定到 %s 渠道池，当前请求为 %s", override.Kind, kind)
	}
	for _, ch := range routedChannels {
		if ch.Index != override.ChannelIndex {
			continue
		}
		if failedChannels[ch.Index] {
			return nil, true, fmt.Errorf("对话固定渠道 [%d] %s 已在本次请求中失败", ch.Index, ch.Name)
		}
		if ch.Status != "active" {
			return nil, true, fmt.Errorf("对话固定渠道 [%d] %s 当前状态为 %s", ch.Index, ch.Name, ch.Status)
		}
		upstream := s.getUpstreamByIndex(ch.Index, kind)
		if upstream == nil || len(upstream.APIKeys) == 0 {
			return nil, true, fmt.Errorf("对话固定渠道 [%d] %s 没有可用 API 密钥", ch.Index, ch.Name)
		}
		prefix := kindSchedulerLogPrefix(kind)
		log.Printf("[%s-Override] 对话固定渠道: [%d] %s", prefix, ch.Index, ch.Name)
		return &SelectionResult{
			Upstream:     upstream,
			ChannelIndex: ch.Index,
			Reason:       "conversation_route_override",
		}, true, nil
	}
	return nil, true, fmt.Errorf("对话固定渠道 [%d] 当前不可用", override.ChannelIndex)
}

func noActiveChannelError(kind ChannelKind) error {
	switch kind {
	case ChannelKindMessages:
		return fmt.Errorf("没有可用的活跃 Messages 渠道")
	case ChannelKindGemini:
		return fmt.Errorf("没有可用的活跃 Gemini 渠道")
	case ChannelKindResponses:
		return fmt.Errorf("没有可用的活跃 Responses 渠道")
	case ChannelKindChat:
		return fmt.Errorf("没有可用的活跃 Chat 渠道")
	case ChannelKindImages:
		return fmt.Errorf("没有可用的活跃 Images 渠道")
	default:
		return fmt.Errorf("不支持的渠道类型: %s", kind)
	}
}

func (s *ChannelScheduler) selectTraceAffinity(
	activeChannels []ChannelInfo,
	failedChannels map[int]bool,
	kind ChannelKind,
	userID string,
	metricsManager *metrics.MetricsManager,
) *SelectionResult {
	if userID == "" {
		return nil
	}
	preferredIdx, ok := s.traceAffinity.GetPreferredChannelForKind(string(kind), userID)
	if !ok {
		return nil
	}

	foundPreferredChannel := false
	affinityInvalidated := false
	for _, ch := range activeChannels {
		if ch.Index != preferredIdx || failedChannels[preferredIdx] {
			continue
		}
		foundPreferredChannel = true
		if ch.Status != "active" {
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-Affinity] 跳过亲和渠道 [%d] %s: 状态为 %s (user: %s)", prefix, preferredIdx, ch.Name, ch.Status, maskUserID(userID))
			affinityInvalidated = true
			continue
		}
		upstream := s.getUpstreamByIndex(preferredIdx, kind)
		if upstream == nil || len(upstream.APIKeys) == 0 {
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-Affinity] 跳过亲和渠道 [%d]: 无可用密钥 (user: %s)", prefix, preferredIdx, maskUserID(userID))
			affinityInvalidated = true
			continue
		}
		if !metricsManager.IsChannelHealthyWithKeys(upstream.BaseURL, upstream.APIKeys, preferredIdx) {
			failureRate := metricsManager.CalculateChannelFailureRate(upstream.BaseURL, upstream.APIKeys, preferredIdx)
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-Affinity] 跳过亲和渠道 [%d] %s: 不健康 (失败率: %.1f%%, user: %s)", prefix, preferredIdx, ch.Name, failureRate*100, maskUserID(userID))
			affinityInvalidated = true
			continue
		}
		prefix := kindSchedulerLogPrefix(kind)
		log.Printf("[%s-Affinity] 使用 Trace 亲和渠道: [%d] %s (user: %s)", prefix, preferredIdx, ch.Name, maskUserID(userID))
		return &SelectionResult{
			Upstream:     upstream,
			ChannelIndex: preferredIdx,
			Reason:       "trace_affinity",
		}
	}

	if !foundPreferredChannel || affinityInvalidated {
		s.traceAffinity.RemoveForKind(string(kind), userID)
		prefix := kindSchedulerLogPrefix(kind)
		if affinityInvalidated {
			log.Printf("[%s-Affinity] 清除失效的 Trace 亲和性: user=%s, channel=%d (渠道不健康或状态异常)", prefix, maskUserID(userID), preferredIdx)
		} else {
			log.Printf("[%s-Affinity] 清除失效的 Trace 亲和性: user=%s, channel=%d (渠道不存在)", prefix, maskUserID(userID), preferredIdx)
		}
	}
	return nil
}

func (s *ChannelScheduler) selectAdaptiveChannel(
	activeChannels []ChannelInfo,
	failedChannels map[int]bool,
	kind ChannelKind,
	requestedModel string,
	metricsManager *metrics.MetricsManager,
) *SelectionResult {
	s.mu.RLock()
	adaptiveScheduler := s.adaptiveScheduler
	s.mu.RUnlock()
	if adaptiveScheduler == nil {
		return nil
	}

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
		return result
	}
	prefix := kindSchedulerLogPrefix(kind)
	log.Printf("[%s-Adaptive] 自适应调度未找到可用渠道，降级到优先级调度", prefix)
	return nil
}

func (s *ChannelScheduler) selectPriorityChannel(
	activeChannels []ChannelInfo,
	failedChannels map[int]bool,
	kind ChannelKind,
	requestedModel string,
	metricsManager *metrics.MetricsManager,
) *SelectionResult {
	type priorityCandidate struct {
		channel  ChannelInfo
		upstream *config.UpstreamConfig
	}
	var samePriorityCandidates []priorityCandidate
	currentPriority := -1

	for _, ch := range activeChannels {
		if failedChannels[ch.Index] {
			continue
		}
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
		if !metricsManager.IsChannelHealthyWithKeys(upstream.BaseURL, upstream.APIKeys, ch.Index) {
			failureRate := metricsManager.CalculateChannelFailureRate(upstream.BaseURL, upstream.APIKeys, ch.Index)
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-Channel] 警告: 跳过不健康渠道: [%d] %s (失败率: %.1f%%)", prefix, ch.Index, ch.Name, failureRate*100)
			continue
		}

		if len(samePriorityCandidates) == 0 {
			currentPriority = ch.Priority
		} else if ch.Priority != currentPriority {
			break
		}
		samePriorityCandidates = append(samePriorityCandidates, priorityCandidate{
			channel:  ch,
			upstream: upstream,
		})
	}

	if len(samePriorityCandidates) == 0 {
		return nil
	}
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
	return &SelectionResult{
		Upstream:     best.upstream,
		ChannelIndex: best.channel.Index,
		Reason:       "priority_order",
	}
}

// hasAttemptableChannel 判断当前分组是否仍有渠道值得在本次请求中尝试。
// 健康度不在这里过滤：调度器会先跳过不健康渠道，但仍保留一次组内降级尝试；
// 真正请求失败后 failedChannels 会确保下一次选择进入后续分组。
func (s *ChannelScheduler) hasAttemptableChannel(
	channels []ChannelInfo,
	failedChannels map[int]bool,
	kind ChannelKind,
	requestedModel string,
) bool {
	for _, channel := range channels {
		if failedChannels[channel.Index] || channel.Status != config.ChannelStatusActive {
			continue
		}
		upstream := s.getUpstreamByIndex(channel.Index, kind)
		if upstream == nil || len(upstream.APIKeys) == 0 {
			continue
		}
		if requestedModel != "" && len(config.ResolveUpstreamModelList(requestedModel, upstream)) == 0 {
			continue
		}
		return true
	}
	return false
}

func (s *ChannelScheduler) selectPromotedChannel(
	activeChannels []ChannelInfo,
	failedChannels map[int]bool,
	kind ChannelKind,
	reason string,
) *SelectionResult {
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
		metricsManager := s.getMetricsManager(kind)
		failureRate := metricsManager.CalculateChannelFailureRate(upstream.BaseURL, upstream.APIKeys, selected.Index)
		prefix := kindSchedulerLogPrefix(kind)
		log.Printf("[%s-Promotion] 促销期优先选择渠道: [%d] %s (失败率: %.1f%%, inFlight: %d, candidates: %d)",
			prefix, selected.Index, upstream.Name, failureRate*100, s.GetChannelInFlight(kind, selected.Index), len(promotedCandidates))
		return &SelectionResult{
			Upstream:     upstream,
			ChannelIndex: selected.Index,
			Reason:       reason,
		}
	}
	return nil
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

		failureRate := metricsManager.CalculateChannelFailureRate(upstream.BaseURL, upstream.APIKeys, ch.Index)
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
func (s *ChannelScheduler) calculateChannelScore(upstream *config.UpstreamConfig, channelIndex int, kind ChannelKind) float64 {
	if upstream == nil || len(upstream.APIKeys) == 0 {
		return 0
	}

	metricsManager := s.getMetricsManager(kind)
	failureRate := metricsManager.CalculateChannelFailureRate(upstream.BaseURL, upstream.APIKeys, channelIndex)

	// 计算基础分数：100 * (1 - failureRate)
	baseScore := 100 * (1 - failureRate)

	// 检查请求数，如果请求数小于 5，给予新渠道奖励（避免因样本不足被低估）
	requestCount := metricsManager.GetChannelRequestCount(upstream.BaseURL, upstream.APIKeys, channelIndex)
	if requestCount < 5 {
		// 新渠道给予满分
		return 100
	}

	return baseScore
}

// getActiveChannels 获取故障转移序列（按配置优先级排序）。
// 动态分数只用于同优先级渠道，不能越过用户配置的 failover 顺序。
func (s *ChannelScheduler) getActiveChannels(kind ChannelKind, poolIDs ...string) []ChannelInfo {
	channelsByPool := s.getActiveChannelsByPool(kind, poolIDs)
	if len(poolIDs) == 0 {
		return channelsByPool[""]
	}
	var activeChannels []ChannelInfo
	for _, poolID := range poolIDs {
		activeChannels = append(activeChannels, channelsByPool[poolID]...)
	}
	return activeChannels
}

// getActiveChannelsByPool 从同一份配置快照中收集多个分组的故障转移序列，
// 避免一次选渠为了命中分组和兜底分组重复深拷贝完整配置。
func (s *ChannelScheduler) getActiveChannelsByPool(kind ChannelKind, poolIDs []string) map[string][]ChannelInfo {
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
		return map[string][]ChannelInfo{}
	}

	requestedPools := make(map[string]struct{}, len(poolIDs))
	for _, poolID := range poolIDs {
		requestedPools[poolID] = struct{}{}
	}
	channelsByPool := make(map[string][]ChannelInfo, len(poolIDs)+1)
	if len(poolIDs) == 0 {
		channelsByPool[""] = nil
	} else {
		for _, poolID := range poolIDs {
			channelsByPool[poolID] = nil
		}
	}

	// 筛选故障转移序列中的渠道。
	for i, upstream := range upstreams {
		status := upstream.Status
		if status == "" {
			status = "active" // 默认为活跃
		}

		// 只选择故障转移序列中的渠道（suspended 也显示在序列中，备用/弃用/删除占位排除）
		upstreamPoolID := strings.TrimSpace(upstream.PoolID)
		if upstreamPoolID == "" {
			upstreamPoolID = config.DefaultChannelPoolID
		}
		if !config.IsChannelSchedulable(&upstream) || upstream.ExcludeFromConversation {
			continue
		}
		if len(requestedPools) > 0 {
			if _, requested := requestedPools[upstreamPoolID]; !requested {
				continue
			}
		}

		priority := upstream.Priority
		if priority == 0 {
			priority = i + 1
		}

		// 计算渠道动态分数
		upstreamCopy := upstream
		score := s.calculateChannelScore(&upstreamCopy, i, kind)
		channel := ChannelInfo{
			Index:    i,
			Name:     upstream.Name,
			Priority: priority,
			Status:   status,
			Score:    score,
		}
		if len(requestedPools) == 0 {
			channelsByPool[""] = append(channelsByPool[""], channel)
		} else {
			channelsByPool[upstreamPoolID] = append(channelsByPool[upstreamPoolID], channel)
		}
	}

	for poolID := range channelsByPool {
		channels := channelsByPool[poolID]
		// 配置优先级是主顺序；同优先级时再参考动态分数和稳定索引。
		sort.Slice(channels, func(i, j int) bool {
			if channels[i].Priority != channels[j].Priority {
				return channels[i].Priority < channels[j].Priority
			}
			if channels[i].Score != channels[j].Score {
				return channels[i].Score > channels[j].Score
			}
			return channels[i].Index < channels[j].Index
		})
		channelsByPool[poolID] = channels
	}

	return channelsByPool
}

func (s *ChannelScheduler) getChannelPools(kind ChannelKind) []config.ChannelPool {
	cfg := s.configManager.GetConfig()
	switch kind {
	case ChannelKindMessages:
		return cfg.MessagePools
	case ChannelKindResponses:
		return cfg.ResponsesPools
	case ChannelKindGemini:
		return cfg.GeminiPools
	case ChannelKindChat:
		return cfg.ChatPools
	case ChannelKindImages:
		return cfg.ImagesPools
	default:
		return nil
	}
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
func (s *ChannelScheduler) SelectVisionChannel(ctx context.Context, kind ChannelKind, channelID string, ownerPoolID string) (*SelectionResult, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	channelID = strings.TrimSpace(channelID)
	ownerPoolID = strings.TrimSpace(ownerPoolID)
	if ownerPoolID == "" {
		ownerPoolID = config.DefaultChannelPoolID
	}
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
		targetPoolID := strings.TrimSpace(upstream.PoolID)
		if targetPoolID == "" {
			targetPoolID = config.DefaultChannelPoolID
		}
		if !upstream.ExcludeFromConversation && targetPoolID != ownerPoolID {
			return nil, fmt.Errorf("图片理解渠道 %q 不在当前分组或公用纯图片理解池", upstream.Name)
		}
		if config.GetChannelStatus(upstream) != config.ChannelStatusActive || len(upstream.APIKeys) == 0 {
			return nil, fmt.Errorf("图片理解渠道 %q 当前不可用", upstream.Name)
		}
		metricsManager := s.getMetricsManager(kind)
		if !metricsManager.IsChannelHealthyWithKeys(upstream.BaseURL, upstream.APIKeys, index) {
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

// ListFallbackVisionChannels 返回公共图片理解渠道池中可用的渠道列表，
// 排除已尝试过的渠道 ID。用于图片理解渠道失败后的自动回退。
// 返回的渠道按健康度排序（健康优先）。
func (s *ChannelScheduler) ListFallbackVisionChannels(ctx context.Context, kind ChannelKind, excludeChannelID string, ownerPoolID string) []*SelectionResult {
	if ctx == nil {
		return nil
	}
	channelID := strings.TrimSpace(excludeChannelID)
	ownerPoolID = strings.TrimSpace(ownerPoolID)
	if ownerPoolID == "" {
		ownerPoolID = config.DefaultChannelPoolID
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
		return nil
	}

	var results []*SelectionResult
	for index := range upstreams {
		upstream := &upstreams[index]
		if upstream.ID == channelID {
			continue
		}
		// 必须标记为支持图片理解
		if !upstream.VisionCapable {
			continue
		}
		// 必须是公共图片理解渠道（ExcludeFromConversation=true）或同池渠道
		targetPoolID := strings.TrimSpace(upstream.PoolID)
		if targetPoolID == "" {
			targetPoolID = config.DefaultChannelPoolID
		}
		if !upstream.ExcludeFromConversation && targetPoolID != ownerPoolID {
			continue
		}
		// 必须可用且健康
		if config.GetChannelStatus(upstream) != config.ChannelStatusActive || len(upstream.APIKeys) == 0 {
			continue
		}
		metricsManager := s.getMetricsManager(kind)
		if !metricsManager.IsChannelHealthyWithKeys(upstream.BaseURL, upstream.APIKeys, index) {
			continue
		}
		results = append(results, &SelectionResult{
			Upstream:     upstream.Clone(),
			ChannelIndex: index,
			Reason:       "vision_layer_fallback",
		})
	}
	return results
}

// RecordSuccess 记录渠道成功（使用 baseURL + apiKey + channelIndex）
func (s *ChannelScheduler) RecordSuccess(baseURL, apiKey string, channelIndex int, kind ChannelKind) {
	s.getMetricsManager(kind).RecordSuccess(baseURL, apiKey, channelIndex)
}

// RecordSuccessWithUsage 记录渠道成功（带 Usage 数据）
func (s *ChannelScheduler) RecordSuccessWithUsage(baseURL, apiKey string, channelIndex int, usage *types.Usage, kind ChannelKind) {
	s.getMetricsManager(kind).RecordSuccessWithUsage(baseURL, apiKey, channelIndex, usage)
}

// RecordFailure 记录渠道失败（使用 baseURL + apiKey + channelIndex）
func (s *ChannelScheduler) RecordFailure(baseURL, apiKey string, channelIndex int, kind ChannelKind) {
	s.getMetricsManager(kind).RecordFailure(baseURL, apiKey, channelIndex)
}

// RecordRequestStart 记录请求开始
func (s *ChannelScheduler) RecordRequestStart(baseURL, apiKey string, channelIndex int, kind ChannelKind) {
	s.getMetricsManager(kind).RecordRequestStart(baseURL, apiKey, channelIndex)
}

// RecordRequestEnd 记录请求结束
func (s *ChannelScheduler) RecordRequestEnd(baseURL, apiKey string, channelIndex int, kind ChannelKind) {
	s.getMetricsManager(kind).RecordRequestEnd(baseURL, apiKey, channelIndex)
}

// RecordRequestConnected 记录已经开始连接上游的请求，用于实时活跃度和调用历史。
func (s *ChannelScheduler) RecordRequestConnected(baseURL, apiKey, model string, channelIndex int, kind ChannelKind) uint64 {
	return s.getMetricsManager(kind).RecordRequestConnected(baseURL, apiKey, channelIndex, model)
}

// RecordRequestFinalizeSuccess 回写已连接请求的成功结果与用量。
func (s *ChannelScheduler) RecordRequestFinalizeSuccess(baseURL, apiKey string, channelIndex int, requestID uint64, usage *types.Usage, kind ChannelKind) {
	s.getMetricsManager(kind).RecordRequestFinalizeSuccess(baseURL, apiKey, channelIndex, requestID, usage)
}

// RecordRequestFinalizeFailure 回写已连接请求的失败结果。
func (s *ChannelScheduler) RecordRequestFinalizeFailure(baseURL, apiKey string, channelIndex int, requestID uint64, kind ChannelKind) {
	s.getMetricsManager(kind).RecordRequestFinalizeFailure(baseURL, apiKey, channelIndex, requestID)
}

// ShouldSuspendKey 返回指定 Key 是否因熔断而不应继续使用。
func (s *ChannelScheduler) ShouldSuspendKey(baseURL, apiKey string, channelIndex int, kind ChannelKind) bool {
	return s.getMetricsManager(kind).ShouldSuspendKey(baseURL, apiKey, channelIndex)
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
	return s.getMetricsManager(ChannelKindMessages)
}

// GetResponsesMetricsManager 获取 Responses 渠道指标管理器
func (s *ChannelScheduler) GetResponsesMetricsManager() *metrics.MetricsManager {
	return s.getMetricsManager(ChannelKindResponses)
}

// GetGeminiMetricsManager 获取 Gemini 渠道指标管理器
func (s *ChannelScheduler) GetGeminiMetricsManager() *metrics.MetricsManager {
	return s.getMetricsManager(ChannelKindGemini)
}

// GetChatMetricsManager 获取 Chat 渠道指标管理器
func (s *ChannelScheduler) GetChatMetricsManager() *metrics.MetricsManager {
	return s.getMetricsManager(ChannelKindChat)
}

// GetImagesMetricsManager 获取 Images 渠道指标管理器
func (s *ChannelScheduler) GetImagesMetricsManager() *metrics.MetricsManager {
	return s.getMetricsManager(ChannelKindImages)
}

// MetricsManager 按渠道类型返回对应的指标管理器。
// 调用方不应再用 GetXxxMetricsManager + switch kind 的写法。
func (s *ChannelScheduler) MetricsManager(kind ChannelKind) *metrics.MetricsManager {
	return s.getMetricsManager(kind)
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
			metricsManager.ResetKeyFailureState(baseURL, apiKey, channelIndex)
		}
	}
	prefix := kindSchedulerLogPrefix(kind)
	log.Printf("[%s-Reset] 渠道 [%d] %s 的熔断状态已重置（保留历史统计）", prefix, channelIndex, upstream.Name)
}

// DeleteChannelMetrics 删除渠道的所有指标数据（内存 + 持久化）
// 用于删除渠道时清理相关的统计数据
// channelIndex 用于区分同 URL 同 Key 的不同渠道（指标键的一部分）
func (s *ChannelScheduler) DeleteChannelMetrics(upstream *config.UpstreamConfig, channelIndex int, kind ChannelKind) {
	if upstream == nil {
		return
	}
	metricsManager := s.getMetricsManager(kind)
	// 合并活跃 Key 和历史 Key，一起清理
	allKeys := append([]string{}, upstream.APIKeys...)
	allKeys = append(allKeys, upstream.HistoricalAPIKeys...)
	// MetricsManager 内部已有 apiType，无需外部传递
	metricsManager.DeleteChannelMetrics(upstream.GetAllBaseURLs(), allKeys, channelIndex)
	prefix := kindSchedulerLogPrefix(kind)
	log.Printf("[%s-Delete] 渠道 %s [%d] 的指标数据已清理", prefix, upstream.Name, channelIndex)
}

// GetActiveChannelCount 获取活跃渠道数量
func (s *ChannelScheduler) GetActiveChannelCount(kind ChannelKind) int {
	return len(s.getActiveChannels(kind))
}

// GetActiveChannelCountForModel 返回指定模型可参与故障转移的渠道数，
// 包括命中分组及其后的兜底分组。
func (s *ChannelScheduler) GetActiveChannelCountForModel(kind ChannelKind, model string) int {
	poolRoute, err := config.SelectChannelPoolRoute(s.getChannelPools(kind), model)
	if err != nil {
		return 0
	}
	poolIDs := make([]string, 0, len(poolRoute))
	for _, pool := range poolRoute {
		poolIDs = append(poolIDs, pool.ID)
	}
	channelsByPool := s.getActiveChannelsByPool(kind, poolIDs)
	count := 0
	for _, pool := range poolRoute {
		count += len(channelsByPool[pool.ID])
	}
	return count
}

// IsMultiChannelMode 判断是否为多渠道模式
func (s *ChannelScheduler) IsMultiChannelMode(kind ChannelKind) bool {
	return s.GetActiveChannelCount(kind) > 1
}

func (s *ChannelScheduler) IsMultiChannelModeForModel(kind ChannelKind, model string) bool {
	return s.GetActiveChannelCountForModel(kind, model) > 1
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
