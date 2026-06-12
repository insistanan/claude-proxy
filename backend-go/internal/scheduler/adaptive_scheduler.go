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
	activeReqs    int64
	loadScore     float64 // 负载评分（越低越好）
	finalScore    float64 // 综合评分（越高越好）
}

// SelectBestChannel 基于性能画像选择最佳渠道（支持模型过滤）
// 策略：优先级分组 + 模型支持过滤 + 健康评分 + 负载均衡
func (as *AdaptiveScheduler) SelectBestChannel(
	activeChannels []ChannelInfo,
	failedChannels map[int]bool,
	kind ChannelKind,
	requestedModel string, // 用户请求的模型名
	isHealthyFunc func(baseURLs []string, apiKeys []string) bool,
	getUpstreamFunc func(int, ChannelKind) *config.UpstreamConfig,
) *SelectionResult {
	if as == nil || as.profileManager == nil {
		return nil
	}

	// 按优先级分组
	priorityGroups, priorities := as.groupByPriority(activeChannels)

	// 在每个优先级组内进行自适应选择
	for _, priority := range priorities {
		group := priorityGroups[priority]
		selected := as.selectFromGroup(group, failedChannels, kind, requestedModel, isHealthyFunc, getUpstreamFunc)
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

		resolvedModel, profile, loadScore, finalScore := as.selectBestModelProfile(baseURLs, upstream.APIKeys, targetModels, ch.Index)

		candidates = append(candidates, &channelCandidate{
			channel:       ch,
			upstream:      upstream,
			profile:       profile,
			resolvedModel: resolvedModel,
			healthScore:   profile.HealthScore,
			activeReqs:    profile.ActiveRequests,
			loadScore:     loadScore,
			finalScore:    finalScore,
		})
	}

	if len(candidates) == 0 {
		return nil
	}

	// 按综合评分排序（降序）
	sort.Slice(candidates, func(i, j int) bool {
		// 综合评分相同时，选活跃请求少的
		if candidates[i].finalScore == candidates[j].finalScore {
			return candidates[i].activeReqs < candidates[j].activeReqs
		}
		return candidates[i].finalScore > candidates[j].finalScore
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

func (as *AdaptiveScheduler) selectBestModelProfile(baseURLs []string, apiKeys []string, targetModels []string, channelIndex int) (string, metrics.PerformanceSnapshotView, float64, float64) {
	var bestModel string
	var bestProfile metrics.PerformanceSnapshotView
	var bestLoadScore float64
	var bestFinalScore float64

	for modelIdx, targetModel := range targetModels {
		profile := as.profileManager.GetAggregateProfileSnapshot(baseURLs, apiKeys, targetModel, channelIndex)
		loadScore := as.calculateLoadScore(profile)
		finalScore := as.calculateFinalScore(profile, loadScore)
		if modelIdx == 0 || finalScore > bestFinalScore || (finalScore == bestFinalScore && profile.ActiveRequests < bestProfile.ActiveRequests) {
			bestModel = targetModel
			bestProfile = profile
			bestLoadScore = loadScore
			bestFinalScore = finalScore
		}
	}

	return bestModel, bestProfile, bestLoadScore, bestFinalScore
}

// calculateLoadScore 计算负载评分（0-100，越低越好）
func (as *AdaptiveScheduler) calculateLoadScore(profile metrics.PerformanceSnapshotView) float64 {
	// 活跃请求数影响（0-50分）
	activeScore := float64(0)
	switch {
	case profile.ActiveRequests == 0:
		activeScore = 0 // 无负载
	case profile.ActiveRequests <= 5:
		activeScore = float64(profile.ActiveRequests) * 2 // 0-10分
	case profile.ActiveRequests <= 20:
		activeScore = 10 + float64(profile.ActiveRequests-5)*1.5 // 10-32.5分
	case profile.ActiveRequests <= 50:
		activeScore = 32.5 + float64(profile.ActiveRequests-20)*0.5 // 32.5-47.5分
	default:
		activeScore = 50 // 高负载
	}

	// TPS 接近峰值时惩罚（0-50分）
	tpsScore := float64(0)
	if profile.Peak1MinTPS > 0 && profile.RecentTPS > 0 {
		utilization := profile.RecentTPS / profile.Peak1MinTPS
		switch {
		case utilization < 0.5:
			tpsScore = 0 // 低利用率
		case utilization < 0.7:
			tpsScore = (utilization - 0.5) / 0.2 * 20 // 0-20分
		case utilization < 0.9:
			tpsScore = 20 + (utilization-0.7)/0.2*20 // 20-40分
		default:
			tpsScore = 40 + (utilization-0.9)/0.1*10 // 40-50分（接近峰值）
		}
	}

	return activeScore + tpsScore
}

// calculateFinalScore 计算综合评分（0-100，越高越好）
func (as *AdaptiveScheduler) calculateFinalScore(profile metrics.PerformanceSnapshotView, loadScore float64) float64 {
	// 综合评分 = 健康评分(60%) + 负载反向评分(40%)
	// 健康评分越高越好，负载评分越低越好
	healthWeight := 0.6
	loadWeight := 0.4

	// 将负载评分反转（100-loadScore）使其越低分数越高
	invertedLoadScore := 100.0 - loadScore

	finalScore := profile.HealthScore*healthWeight + invertedLoadScore*loadWeight

	return finalScore
}

// groupByPriority 按优先级分组（优先级低的数字排前面）
func (as *AdaptiveScheduler) groupByPriority(channels []ChannelInfo) (map[int][]ChannelInfo, []int) {
	groups := make(map[int][]ChannelInfo)
	for _, ch := range channels {
		groups[ch.Priority] = append(groups[ch.Priority], ch)
	}
	priorities := make([]int, 0, len(groups))
	for priority := range groups {
		priorities = append(priorities, priority)
	}
	sort.Ints(priorities)
	return groups, priorities
}

// GetChannelPerformanceReport 获取渠道性能报告（用于 Dashboard）
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
