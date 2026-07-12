package scheduler

import (
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/metrics"
)

// AdaptiveScheduler 自适应负载均衡调度器
type AdaptiveScheduler struct {
	profileManager *metrics.ProfileManager
}

// NewAdaptiveScheduler 创建自适应调度器
func NewAdaptiveScheduler(profileManager *metrics.ProfileManager) *AdaptiveScheduler {
	return &AdaptiveScheduler{
		profileManager: profileManager,
	}
}

// channelCandidate 渠道候选者（用于排序）
type channelCandidate struct {
	channel       ChannelInfo
	upstream      *config.UpstreamConfig
	profile       metrics.PerformanceSnapshotView
	resolvedModel string // 实际使用的模型名（经过 modelMapping 后）
	healthScore   float64
	activeReqs    int64   // 含预留的有效在途负载
	loadScore     float64 // 负载评分（越低越好）
	finalScore    float64 // 综合评分（越高越好）
}

// 同优先级内，综合评分差距在此阈值内时优先按负载分摊
const adaptiveScoreLoadTieThreshold = 8.0

// SelectBestChannel 基于性能画像选择最佳渠道（支持模型过滤）
// 策略：优先级分组 + 模型支持过滤 + 健康评分 + 负载均衡（含选渠预留）
func (as *AdaptiveScheduler) SelectBestChannel(
	activeChannels []ChannelInfo,
	failedChannels map[int]bool,
	kind ChannelKind,
	requestedModel string, // 用户请求的模型名
	isHealthyFunc func(baseURLs []string, apiKeys []string) bool,
	getUpstreamFunc func(int, ChannelKind) *config.UpstreamConfig,
	getInFlightFunc func(channelIndex int) int64,
) *SelectionResult {
	if as == nil || as.profileManager == nil {
		return nil
	}

	// 按优先级分组
	priorityGroups, priorities := as.groupByPriority(activeChannels)

	// 在每个优先级组内进行自适应选择
	for _, priority := range priorities {
		group := priorityGroups[priority]
		selected := as.selectFromGroup(group, failedChannels, kind, requestedModel, isHealthyFunc, getUpstreamFunc, getInFlightFunc)
		if selected != nil {
			prefix := kindSchedulerLogPrefix(kind)
			log.Printf("[%s-Adaptive] 选择渠道 [%d] %s (优先级: %d, 模型: %s, 健康评分: %.1f, 负载: %d, 等级: %s)",
				prefix, selected.ChannelIndex, selected.Upstream.Name, priority,
				selected.ResolvedModel, selected.HealthScore, selected.ActiveLoad, selected.PerformanceTier)
			return &SelectionResult{
				Upstream:     selected.Upstream,
				ChannelIndex: selected.ChannelIndex,
				Reason:       fmt.Sprintf("adaptive_lb(model=%s,score=%.1f,load=%d)", selected.ResolvedModel, selected.HealthScore, selected.ActiveLoad),
			}
		}
	}

	return nil
}

// AdaptiveSelectionResult 增强的选择结果
type AdaptiveSelectionResult struct {
	Upstream        *config.UpstreamConfig
	ChannelIndex    int
	ResolvedModel   string // 实际使用的模型名
	HealthScore     float64
	ActiveLoad      int64
	PerformanceTier string
}

// selectFromGroup 在同优先级组内选择最佳渠道
func (as *AdaptiveScheduler) selectFromGroup(
	group []ChannelInfo,
	failedChannels map[int]bool,
	kind ChannelKind,
	requestedModel string,
	isHealthyFunc func(baseURLs []string, apiKeys []string) bool,
	getUpstreamFunc func(int, ChannelKind) *config.UpstreamConfig,
	getInFlightFunc func(channelIndex int) int64,
) *AdaptiveSelectionResult {

	var candidates []*channelCandidate

	for _, ch := range group {
		if failedChannels[ch.Index] || ch.Status != "active" {
			continue
		}

		upstream := getUpstreamFunc(ch.Index, kind)
		if upstream == nil || len(upstream.APIKeys) == 0 {
			continue
		}
		baseURLs := upstream.GetAllBaseURLs()
		if isHealthyFunc != nil && !isHealthyFunc(baseURLs, upstream.APIKeys) {
			continue
		}

		// 关键过滤：检查该渠道是否支持请求的模型
		targetModels := config.ResolveUpstreamModelList(requestedModel, upstream)
		if len(targetModels) == 0 {
			continue
		}

		reservedLoad := int64(0)
		if getInFlightFunc != nil {
			reservedLoad = getInFlightFunc(ch.Index)
		}

		resolvedModel, profile, loadScore, finalScore := as.selectBestModelProfile(
			baseURLs,
			upstream.APIKeys,
			targetModels,
			ch.Index,
			reservedLoad,
		)

		candidates = append(candidates, &channelCandidate{
			channel:       ch,
			upstream:      upstream,
			profile:       profile,
			resolvedModel: resolvedModel,
			healthScore:   profile.HealthScore,
			activeReqs:    profile.ActiveRequests + reservedLoad,
			loadScore:     loadScore,
			finalScore:    finalScore,
		})
	}

	if len(candidates) == 0 {
		return nil
	}

	// 综合评分接近时优先负载更低的渠道，便于同协议多对话分摊到不同供应商
	sort.Slice(candidates, func(i, j int) bool {
		scoreDiff := candidates[i].finalScore - candidates[j].finalScore
		if scoreDiff > adaptiveScoreLoadTieThreshold {
			return true
		}
		if scoreDiff < -adaptiveScoreLoadTieThreshold {
			return false
		}
		if candidates[i].activeReqs != candidates[j].activeReqs {
			return candidates[i].activeReqs < candidates[j].activeReqs
		}
		if candidates[i].finalScore != candidates[j].finalScore {
			return candidates[i].finalScore > candidates[j].finalScore
		}
		// 最终稳定次序，避免并发下总是落到 slice 中靠前的同一家
		return candidates[i].channel.Index < candidates[j].channel.Index
	})

	// 选择评分最高的
	best := candidates[0]

	return &AdaptiveSelectionResult{
		Upstream:        best.upstream,
		ChannelIndex:    best.channel.Index,
		ResolvedModel:   best.resolvedModel,
		HealthScore:     best.healthScore,
		ActiveLoad:      best.activeReqs,
		PerformanceTier: best.profile.PerformanceTier,
	}
}

// selectBestModelProfile 在渠道支持的模型列表中选择性能最优的模型画像。
// reservedLoad 为选渠阶段的预留在途数，用于并发请求分摊。
func (as *AdaptiveScheduler) selectBestModelProfile(
	baseURLs []string,
	apiKeys []string,
	targetModels []string,
	channelIdx int,
	reservedLoad int64,
) (string, metrics.PerformanceSnapshotView, float64, float64) {
	if len(targetModels) == 0 {
		return "", metrics.PerformanceSnapshotView{}, 100, 0
	}

	bestModel := targetModels[0]
	bestProfile := as.profileManager.GetAggregateProfileSnapshot(baseURLs, apiKeys, bestModel, channelIdx)
	bestLoadScore := as.calculateLoadScoreWithReserved(bestProfile, reservedLoad)
	bestFinalScore := bestProfile.HealthScore - bestLoadScore*0.3

	for i := 1; i < len(targetModels); i++ {
		model := targetModels[i]
		profile := as.profileManager.GetAggregateProfileSnapshot(baseURLs, apiKeys, model, channelIdx)
		loadScore := as.calculateLoadScoreWithReserved(profile, reservedLoad)
		finalScore := profile.HealthScore - loadScore*0.3

		// 同分时优先在途更少的模型画像
		if finalScore > bestFinalScore ||
			(finalScore == bestFinalScore && profile.ActiveRequests+reservedLoad < bestProfile.ActiveRequests+reservedLoad) {
			bestModel = model
			bestProfile = profile
			bestLoadScore = loadScore
			bestFinalScore = finalScore
		}
	}

	return bestModel, bestProfile, bestLoadScore, bestFinalScore
}

// calculateLoadScore 计算负载评分（0-100，越低越好）
func (as *AdaptiveScheduler) calculateLoadScore(profile metrics.PerformanceSnapshotView) float64 {
	return as.calculateLoadScoreWithReserved(profile, 0)
}

// calculateLoadScoreWithReserved 计算含预留在途的负载评分（0-100，越低越好）。
// 前几个并发请求的惩罚更陡，鼓励同优先级下把对话摊到不同供应商。
func (as *AdaptiveScheduler) calculateLoadScoreWithReserved(profile metrics.PerformanceSnapshotView, reservedLoad int64) float64 {
	effectiveActive := profile.ActiveRequests + reservedLoad
	if effectiveActive < 0 {
		effectiveActive = 0
	}

	// 活跃请求数影响（0-55分）：前 3 个请求快速抬升，便于瞬时分摊
	activeScore := float64(0)
	switch {
	case effectiveActive == 0:
		activeScore = 0
	case effectiveActive == 1:
		activeScore = 12
	case effectiveActive == 2:
		activeScore = 22
	case effectiveActive == 3:
		activeScore = 30
	case effectiveActive <= 10:
		activeScore = 30 + float64(effectiveActive-3)*2 // 32-44
	case effectiveActive <= 20:
		activeScore = 44 + float64(effectiveActive-10)*0.8 // 44-52
	default:
		activeScore = 52 + float64(effectiveActive-20)*0.15
		if activeScore > 55 {
			activeScore = 55
		}
	}

	// TPS 利用率影响（0-45分）
	tpsScore := float64(0)
	if profile.Peak1MinTPS > 0 {
		utilization := profile.RecentTPS / profile.Peak1MinTPS
		switch {
		case utilization < 0.5:
			tpsScore = utilization * 20 // 0-10分
		case utilization < 0.8:
			tpsScore = 10 + (utilization-0.5)*50 // 10-25分
		case utilization < 1.0:
			tpsScore = 25 + (utilization-0.8)*75 // 25-40分
		default:
			tpsScore = 40 + (utilization-1.0)*25 // 40-45分
			if tpsScore > 45 {
				tpsScore = 45
			}
		}
	}

	return activeScore + tpsScore
}

// groupByPriority 按优先级分组渠道
func (as *AdaptiveScheduler) groupByPriority(channels []ChannelInfo) (map[int][]ChannelInfo, []int) {
	groups := make(map[int][]ChannelInfo)
	for _, ch := range channels {
		groups[ch.Priority] = append(groups[ch.Priority], ch)
	}

	// 优先级数值越小越优先
	priorities := make([]int, 0, len(groups))
	for p := range groups {
		priorities = append(priorities, p)
	}
	sort.Ints(priorities)

	return groups, priorities
}

// GetChannelPerformanceReport 获取所有渠道的性能报告
func (as *AdaptiveScheduler) GetChannelPerformanceReport() []ChannelPerformanceReport {
	if as == nil || as.profileManager == nil {
		return nil
	}
	profiles := as.profileManager.GetAllProfileSnapshots()

	reports := make([]ChannelPerformanceReport, len(profiles))
	for i, p := range profiles {
		reports[i] = ChannelPerformanceReport{
			ChannelIndex:      p.ChannelIdx,
			BaseURL:           p.BaseURL,
			Model:             p.Model,
			HealthScore:       p.HealthScore,
			PerformanceTier:   p.PerformanceTier,
			ActiveRequests:    p.ActiveRequests,
			RecentTPS:         p.RecentTPS,
			Peak1MinTPS:       p.Peak1MinTPS,
			AvgTTFB:           p.AvgTTFB,
			AvgResponseTime:   p.AvgResponseTime,
			P50Latency:        p.P50Latency,
			P95Latency:        p.P95Latency,
			P99Latency:        p.P99Latency,
			AvgTokenSpeed:     p.AvgTokenSpeed,
			SuccessRate:       p.SuccessRate,
			RecentSuccessRate: p.RecentSuccessRate,
			LastScoreUpdate:   p.LastScoreUpdate.Format(time.RFC3339),
		}
	}

	// 按健康评分排序
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].HealthScore > reports[j].HealthScore
	})

	return reports
}

// ChannelPerformanceReport 渠道性能报告（API 返回）
type ChannelPerformanceReport struct {
	ChannelIndex      int     `json:"channelIndex"`
	BaseURL           string  `json:"baseUrl"`
	Model             string  `json:"model"` // 模型名
	HealthScore       float64 `json:"healthScore"`
	PerformanceTier   string  `json:"performanceTier"`
	ActiveRequests    int64   `json:"activeRequests"`
	RecentTPS         float64 `json:"recentTps"`
	Peak1MinTPS       float64 `json:"peak1MinTps"`
	AvgTTFB           float64 `json:"avgTtfb"`
	AvgResponseTime   float64 `json:"avgResponseTime"`
	P50Latency        float64 `json:"p50Latency"`
	P95Latency        float64 `json:"p95Latency"`
	P99Latency        float64 `json:"p99Latency"`
	AvgTokenSpeed     float64 `json:"avgTokenSpeed"`
	SuccessRate       float64 `json:"successRate"`
	RecentSuccessRate float64 `json:"recentSuccessRate"`
	LastScoreUpdate   string  `json:"lastScoreUpdate"`
}
