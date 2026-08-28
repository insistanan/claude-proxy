package scheduler

import (
	"context"
	"fmt"
	"log"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/conversation"
	"github.com/BenedictKing/claude-proxy/internal/metrics"
)

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
// 优先级: 对话路由覆盖 > 促销期渠道（忽略亲和） > 对话级亲和（粘滞，负载未过载时沿用）
//
//	> 自适应 + 对话散列（同优先级/评分接近候选间按稳定散列分摊） > 用户级 Trace 亲和（兜底） > 按优先级降级
//
// 同一协议下并发新对话会尽量分摊到不同供应商，同时仍遵循优先级、健康与促销规则。
//
// 图片不参与选渠：是否直接处理图片或启用图片理解层，由被选中渠道的配置在
// 请求发送前决定（见 prepareRequestForUpstream → visionlayer.PrepareRequest）。
func (s *ChannelScheduler) SelectChannel(
	ctx context.Context,
	conversationID string,
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

	if selected, hasOverride, err := s.selectConversationRouteOverride(conversationID, kind, routedChannels, failedChannels, registry); err != nil {
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

	// 2. 对话级亲和（粘滞：多轮会话复用最近成功的渠道；负载未过载时才沿用，
	//    过载则放行给负载均衡处理。用于"不来回乱切"）
	if selected := s.selectConversationAffinity(activeChannels, failedChannels, kind, conversationID, metricsManager); selected != nil {
		return s.reserveAndReturn(selected, kind), nil
	}

	// 3. 尝试使用自适应调度器（基于性能画像 + 在途预留；同优先级/评分接近候选间
	//    按对话稳定散列分摊，让不同对话固定摊到不同供应商）
	if selected := s.selectAdaptiveChannel(activeChannels, failedChannels, kind, requestedModel, conversationID, metricsManager); selected != nil {
		return s.reserveAndReturn(selected, kind), nil
	}

	// 4. 会话级 Trace 亲和（兜底：同一会话尚无对话级亲和时，沿用既有偏好渠道）
	if selected := s.selectTraceAffinity(activeChannels, failedChannels, kind, conversationID, metricsManager); selected != nil {
		return s.reserveAndReturn(selected, kind), nil
	}

	// 5. 按优先级遍历活跃渠道（降级方案）
	// 同优先级内收集候选，再按 in-flight 选负载最低者，避免并发新对话全打到第一家供应商。
	if selected := s.selectPriorityChannel(activeChannels, failedChannels, kind, requestedModel, metricsManager); selected != nil {
		return s.reserveAndReturn(selected, kind), nil
	}

	// 6. 当前分组所有健康渠道都失败，选择失败率最低的作为降级
	fallback, err := s.selectFallbackChannel(activeChannels, failedChannels, kind)
	if err != nil {
		return nil, err
	}
	return s.reserveAndReturn(fallback, kind), nil
}

func (s *ChannelScheduler) selectConversationRouteOverride(
	conversationID string,
	kind ChannelKind,
	routedChannels []ChannelInfo,
	failedChannels map[int]bool,
	registry *conversation.Registry,
) (*SelectionResult, bool, error) {
	if conversationID == "" || registry == nil {
		return nil, false, nil
	}
	override, ok := registry.GetRouteOverride(conversationID)
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

// selectConversationAffinity 对话级敏感：若对话已有"最近成功/尝试"命中的渠道，
// 且该渠道在本次分组中仍可用、健康且在途负载未过载，则沿用（保证同一对话不来回乱切）。
// 一旦渠道过载 / 不健康 / 已失败 / 不可用，便放行给后续负载均衡处理。
func (s *ChannelScheduler) selectConversationAffinity(
	activeChannels []ChannelInfo,
	failedChannels map[int]bool,
	kind ChannelKind,
	conversationID string,
	metricsManager *metrics.MetricsManager,
) *SelectionResult {
	if conversationID == "" {
		return nil
	}
	last, ok := s.GetConversationLastResolved(conversationID)
	if !ok || last == nil || last.Kind != string(kind) {
		return nil
	}
	preferredIdx := last.ChannelIndex

	for _, ch := range activeChannels {
		if ch.Index != preferredIdx || failedChannels[preferredIdx] {
			continue
		}
		if ch.Status != "active" {
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-ConvAffinity] 跳过对话亲和渠道 [%d] %s: 状态为 %s (conversation: %s)", prefix, preferredIdx, ch.Name, ch.Status, maskConversationID(conversationID))
			return nil
		}
		upstream := s.getUpstreamByIndex(preferredIdx, kind)
		if upstream == nil || len(upstream.APIKeys) == 0 {
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-ConvAffinity] 跳过对话亲和渠道 [%d]: 无可用密钥 (conversation: %s)", prefix, preferredIdx, maskConversationID(conversationID))
			return nil
		}
		if !metricsManager.IsChannelHealthyWithKeys(upstream.BaseURL, upstream.APIKeys, preferredIdx) {
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-ConvAffinity] 跳过对话亲和渠道 [%d] %s: 不健康 (conversation: %s)", prefix, preferredIdx, ch.Name, maskConversationID(conversationID))
			return nil
		}
		// 负载未过载才粘滞；过载时放行给负载均衡，避免单渠道被多对话打满。
		if !s.channelWithinAffinityLoad(kind, preferredIdx, upstream) {
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-ConvAffinity] 对话亲和渠道 [%d] %s 负载过载，放行给负载均衡 (inFlight: %d, conversation: %s)",
				prefix, preferredIdx, ch.Name, s.GetChannelInFlight(kind, preferredIdx), maskConversationID(conversationID))
			return nil
		}
		prefix := kindSchedulerLogPrefix(kind)
		log.Printf("[%s-ConvAffinity] 使用对话亲和渠道: [%d] %s (conversation: %s)", prefix, preferredIdx, ch.Name, maskConversationID(conversationID))
		return &SelectionResult{
			Upstream:     upstream,
			ChannelIndex: preferredIdx,
			Reason:       "conversation_affinity",
		}
	}
	return nil
}

// channelWithinAffinityLoad 判定某渠道是否仍在上限内，允许对话亲和沿用。
// 结合真实 ActiveRequests（性能画像）与选渠预留 in-flight，避免"空转排队"也不换渠道。
func (s *ChannelScheduler) channelWithinAffinityLoad(
	kind ChannelKind,
	channelIndex int,
	upstream *config.UpstreamConfig,
) bool {
	baseURLs := upstream.GetAllBaseURLs()
	inflight := s.GetChannelInFlight(kind, channelIndex)

	// profileManager 提供真实并发余量感知；其 ActiveRequests 按 baseURL+model 聚合，
	// 这里用聚合快照(全 model)近似。没有画像时退化为仅看选渠预留。
	if s.profileManager != nil {
		agg := s.profileManager.GetAggregateProfileSnapshot(baseURLs, upstream.APIKeys, "", channelIndex)
		if agg.ActiveRequests+inflight >= int64(affinityLoadThreshold) {
			return false
		}
	} else if inflight >= int64(affinityLoadThreshold) {
		return false
	}
	return true
}

// affinityLoadThreshold 对话亲和过载阈值：在途(预留+真实)达到该值即放行给负载均衡。
const affinityLoadThreshold = 3

func (s *ChannelScheduler) selectTraceAffinity(
	activeChannels []ChannelInfo,
	failedChannels map[int]bool,
	kind ChannelKind,
	conversationID string,
	metricsManager *metrics.MetricsManager,
) *SelectionResult {
	if conversationID == "" || s.traceAffinity == nil {
		return nil
	}
	preferredIdx, ok := s.traceAffinity.GetPreferredChannelForKind(string(kind), conversationID)
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
			log.Printf("[%s-Affinity] 跳过亲和渠道 [%d] %s: 状态为 %s (conversation: %s)", prefix, preferredIdx, ch.Name, ch.Status, maskConversationID(conversationID))
			affinityInvalidated = true
			continue
		}
		upstream := s.getUpstreamByIndex(preferredIdx, kind)
		if upstream == nil || len(upstream.APIKeys) == 0 {
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-Affinity] 跳过亲和渠道 [%d]: 无可用密钥 (conversation: %s)", prefix, preferredIdx, maskConversationID(conversationID))
			affinityInvalidated = true
			continue
		}
		if !metricsManager.IsChannelHealthyWithKeys(upstream.BaseURL, upstream.APIKeys, preferredIdx) {
			failureRate := metricsManager.CalculateChannelFailureRate(upstream.BaseURL, upstream.APIKeys, preferredIdx)
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-Affinity] 跳过亲和渠道 [%d] %s: 不健康 (失败率: %.1f%%, conversation: %s)", prefix, preferredIdx, ch.Name, failureRate*100, maskConversationID(conversationID))
			affinityInvalidated = true
			continue
		}
		prefix := kindSchedulerLogPrefix(kind)
		log.Printf("[%s-Affinity] 使用 Trace 亲和渠道: [%d] %s (conversation: %s)", prefix, preferredIdx, ch.Name, maskConversationID(conversationID))
		return &SelectionResult{
			Upstream:     upstream,
			ChannelIndex: preferredIdx,
			Reason:       "trace_affinity",
		}
	}

	if !foundPreferredChannel || affinityInvalidated {
		s.traceAffinity.RemoveForKind(string(kind), conversationID)
		prefix := kindSchedulerLogPrefix(kind)
		if affinityInvalidated {
			log.Printf("[%s-Affinity] 清除失效的 Trace 亲和性: conversation=%s, channel=%d (渠道不健康或状态异常)", prefix, maskConversationID(conversationID), preferredIdx)
		} else {
			log.Printf("[%s-Affinity] 清除失效的 Trace 亲和性: conversation=%s, channel=%d (渠道不存在)", prefix, maskConversationID(conversationID), preferredIdx)
		}
	}
	return nil
}

func (s *ChannelScheduler) selectAdaptiveChannel(
	activeChannels []ChannelInfo,
	failedChannels map[int]bool,
	kind ChannelKind,
	requestedModel string,
	conversationID string,
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
		conversationID,
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

// maskConversationID 掩码内部 conversation record ID（保护隐私）。
func maskConversationID(conversationID string) string {
	if len(conversationID) <= 16 {
		return "***"
	}
	return conversationID[:8] + "***" + conversationID[len(conversationID)-4:]
}
