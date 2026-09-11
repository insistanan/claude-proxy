package metrics

// 本文件是花费计量的读侧：把 request_records 里已有的模型名 + 四项 Token 用量，
// 按**当前**单价表实时折算成美元。花费一律不落盘，理由是单一口径：
// 单价改了，历史花费同步修正；否则用户补上一个模型的单价后会看到
// “新请求有花费、旧请求永远 0 元”，而 request_records 里已经有 model 与 api_type，
// 再加一列成本只会多出一个与价表随时可能矛盾的真相源。

import (
	"sort"
	"time"

	"github.com/BenedictKing/api-proxy/internal/pricing"
)

// CostStats 一段区间内的花费聚合（美元）。
// UnpricedRequests 必须与花费一起展示：单价表里查不到的模型只计数、不摊进
// TotalCostUSD——按 0 元并进总额只会让账单凭空变少，界面上还看不出少在哪。
type CostStats struct {
	TotalCostUSD      float64 `json:"totalCostUsd"`
	InputCostUSD      float64 `json:"inputCostUsd"`
	OutputCostUSD     float64 `json:"outputCostUsd"`
	CacheReadCostUSD  float64 `json:"cacheReadCostUsd"`
	CacheWriteCostUSD float64 `json:"cacheWriteCostUsd"`
	PricedRequests    int64   `json:"pricedRequests"`
	UnpricedRequests  int64   `json:"unpricedRequests"`
}

// recordTokenUsage 把 RequestRecord 的四项用量转成 pricing 口径。
func recordTokenUsage(record RequestRecord) pricing.TokenUsage {
	return pricing.TokenUsage{
		InputTokens:         record.InputTokens,
		OutputTokens:        record.OutputTokens,
		CacheReadTokens:     record.CacheReadInputTokens,
		CacheCreationTokens: record.CacheCreationInputTokens,
	}
}

// hasTokenUsage 判断记录是否带用量。失败、客户端取消、以及还没回写 usage 的记录
// 四项全 0：这类记录既不计价也不计入"未计价"，否则未计价数会被失败请求灌满，
// 失去"该补单价了"的指示作用。
func hasTokenUsage(usage pricing.TokenUsage) bool {
	return usage.InputTokens > 0 || usage.OutputTokens > 0 ||
		usage.CacheReadTokens > 0 || usage.CacheCreationTokens > 0
}

// add 折算单条用量并累加，返回本次明细与是否命中单价，供调用方复用同一次查表结果
// 继续做按模型拆分。调用方需自行保证 usage 非空（见 hasTokenUsage）。
// costMultiplier 固定传 1：本项目还没有渠道级折扣配置，先不引入没有出处的倍率。
func (c *CostStats) add(prices *pricing.Store, apiType, model string, usage pricing.TokenUsage) (pricing.CostBreakdown, bool) {
	breakdown, ok := prices.CostFor(model, usage, apiType, 1)
	if !ok {
		c.UnpricedRequests++
		return pricing.CostBreakdown{}, false
	}
	c.PricedRequests++
	c.InputCostUSD += breakdown.InputCost
	c.OutputCostUSD += breakdown.OutputCost
	c.CacheReadCostUSD += breakdown.CacheReadCost
	c.CacheWriteCostUSD += breakdown.CacheCreationCost
	c.TotalCostUSD += breakdown.TotalCost
	return breakdown, true
}

// addRecord 按记录归属的模型累加花费。定价一律用 record.Model（客户端请求的模型名），
// 与 Token 的归属口径保持一致——同一行里 Token 按请求模型统计、花费按上游映射后的
// 模型统计，单价怎么核都对不上。模型映射后想按目标模型计价，给目标模型名单独配一条单价。
func (c *CostStats) addRecord(prices *pricing.Store, apiType string, record RequestRecord) {
	usage := recordTokenUsage(record)
	if !hasTokenUsage(usage) {
		return
	}
	c.add(prices, apiType, record.Model, usage)
}

// merge 把分桶聚合并入总聚合，避免为了总计再查一遍单价表。
func (c *CostStats) merge(other CostStats) {
	c.TotalCostUSD += other.TotalCostUSD
	c.InputCostUSD += other.InputCostUSD
	c.OutputCostUSD += other.OutputCostUSD
	c.CacheReadCostUSD += other.CacheReadCostUSD
	c.CacheWriteCostUSD += other.CacheWriteCostUSD
	c.PricedRequests += other.PricedRequests
	c.UnpricedRequests += other.UnpricedRequests
}

// ModelCostStats 单个模型在统计区间内的用量与花费。Priced=false 表示单价表里没有
// 这个模型，此时 CostUSD 恒为 0——界面必须显式标成"未计价"，不能当成 0 美元展示。
type ModelCostStats struct {
	Model               string  `json:"model"`
	RequestCount        int64   `json:"requestCount"`
	InputTokens         int64   `json:"inputTokens"`
	OutputTokens        int64   `json:"outputTokens"`
	CacheCreationTokens int64   `json:"cacheCreationTokens"`
	CacheReadTokens     int64   `json:"cacheReadTokens"`
	Priced              bool    `json:"priced"`
	CostUSD             float64 `json:"costUsd"`
}

// ModelCostReport 按模型拆分的花费构成 + 区间总计。
type ModelCostReport struct {
	APIType  string           `json:"apiType"`
	Duration string           `json:"duration"`
	Models   []ModelCostStats `json:"models"`
	Summary  CostStats        `json:"summary"`
}

// GetModelCostStats 按模型汇总统计区间内的用量与花费，供"花费构成"视图使用。
// 数据源是内存里的 24 小时请求历史（进程启动时由 request_records 回填），
// 因此 duration 超过 24 小时不会得到更多数据。
func (m *MetricsManager) GetModelCostStats(duration time.Duration, prices *pricing.Store) ModelCostReport {
	report := ModelCostReport{
		APIType:  m.apiType,
		Duration: duration.String(),
		Models:   []ModelCostStats{},
	}
	if duration <= 0 {
		return report
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	cutoff := time.Now().Add(-duration)
	byModel := make(map[string]*ModelCostStats)
	for _, keyMetrics := range m.keyMetrics {
		for _, record := range keyMetrics.requestHistory {
			if !record.Timestamp.After(cutoff) {
				continue
			}
			usage := recordTokenUsage(record)
			if !hasTokenUsage(usage) {
				continue
			}

			entry, exists := byModel[record.Model]
			if !exists {
				entry = &ModelCostStats{Model: record.Model}
				byModel[record.Model] = entry
			}
			entry.RequestCount++
			entry.InputTokens += usage.InputTokens
			entry.OutputTokens += usage.OutputTokens
			entry.CacheCreationTokens += usage.CacheCreationTokens
			entry.CacheReadTokens += usage.CacheReadTokens

			if breakdown, ok := report.Summary.add(prices, m.apiType, record.Model, usage); ok {
				entry.Priced = true
				entry.CostUSD += breakdown.TotalCost
			}
		}
	}

	for _, entry := range byModel {
		report.Models = append(report.Models, *entry)
	}
	// 花费降序 → 请求数降序 → 模型名升序：同花费（含全部未计价条目）时顺序仍然确定。
	sort.Slice(report.Models, func(i, j int) bool {
		left, right := report.Models[i], report.Models[j]
		if left.CostUSD != right.CostUSD {
			return left.CostUSD > right.CostUSD
		}
		if left.RequestCount != right.RequestCount {
			return left.RequestCount > right.RequestCount
		}
		return left.Model < right.Model
	})
	return report
}
