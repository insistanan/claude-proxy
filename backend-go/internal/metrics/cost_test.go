package metrics

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/BenedictKing/api-proxy/internal/pricing"
	"github.com/BenedictKing/api-proxy/internal/types"
)

// 测试模型名刻意都不含内置价表里的任何键（gpt-4o / o1 / claude-3-5-sonnet …），
// 且互不为子串：查表走单向包含匹配，一旦某个名字包含另一个，本该"未计价"的模型
// 会静默按前者的价格计费，测试就测不出该测的东西了。
const (
	costModelPriced    = "alpha-test-model"
	costModelUnpriced  = "beta-test-model"
	costModelCheap     = "delta-test-model"
	costModelZeroUsage = "gamma-test-model"

	costTestBaseURL = "https://example.com"
	costTestAPIKey  = "k1"
)

// 单条 costTestUsage 在两种口径下的花费：
// messages（input 不含缓存）= 输入 1000 + 输出 1000 + 缓存读 40 + 缓存写 150；
// responses（input 已含缓存）= 计费输入只剩 500k，输入费 500，其余三项不变。
const (
	costPerRequestExclusive = 2190.0
	costPerRequestInclusive = 1690.0
)

// costTestUsage 用量取整十万，配合整千单价让金额落在整数上，可以直接用 == 断言。
func costTestUsage() *types.Usage {
	return &types.Usage{
		InputTokens:              1_000_000,
		OutputTokens:             500_000,
		CacheReadInputTokens:     400_000,
		CacheCreationInputTokens: 100_000,
	}
}

// newCostTestPrices 建单价表：只写测试模型的改写，内置价原样留着（测试模型名不会命中它们）。
func newCostTestPrices(t *testing.T) *pricing.Store {
	t.Helper()
	store, err := pricing.NewStore(filepath.Join(t.TempDir(), "pricing.json"))
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	if err := store.Upsert(
		pricing.ModelPricing{
			ModelID:               costModelPriced,
			InputCostPerM:         1000,
			OutputCostPerM:        2000,
			CacheReadCostPerM:     100,
			CacheCreationCostPerM: 1500,
		},
		// 只配输出价，让它的花费远低于 costModelPriced，用来验证按花费降序排。
		pricing.ModelPricing{ModelID: costModelCheap, OutputCostPerM: 2},
	); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	return store
}

// newCostTestManager 建不带持久化的指标管理器；apiType 决定 input_tokens 是否已含缓存。
func newCostTestManager(t *testing.T, apiType string) *MetricsManager {
	t.Helper()
	m := NewMetricsManagerWithPersistence(10, 0.5, nil, apiType)
	t.Cleanup(m.Stop)
	return m
}

// seedCostRecord 走公开 API 造一条成功记录：连接时落时间戳与模型名，结束时回写用量。
// usage 传 nil 就是"四项全 0"的记录——失败重试、客户端取消、上游没回 usage 都长这样。
func seedCostRecord(t *testing.T, m *MetricsManager, model string, at time.Time, usage *types.Usage) {
	t.Helper()
	id := m.RecordRequestConnectedAt(costTestBaseURL, costTestAPIKey, 0, model, at)
	m.RecordRequestFinalizeSuccess(costTestBaseURL, costTestAPIKey, 0, id, usage)
}

// modelCostByName 把报表按模型名索引，避免断言依赖切片下标。
func modelCostByName(report ModelCostReport) map[string]ModelCostStats {
	byModel := make(map[string]ModelCostStats, len(report.Models))
	for _, entry := range report.Models {
		byModel[entry.Model] = entry
	}
	return byModel
}

func TestGetModelCostStatsSplitsPricedAndUnpriced(t *testing.T) {
	prices := newCostTestPrices(t)
	m := newCostTestManager(t, "messages")

	now := time.Now()
	seedCostRecord(t, m, costModelPriced, now, costTestUsage())
	seedCostRecord(t, m, costModelPriced, now, costTestUsage())
	seedCostRecord(t, m, costModelUnpriced, now, costTestUsage())
	// 用量全 0 的记录既不计价也不记未计价，否则失败请求会把"该补单价了"这个信号灌满。
	seedCostRecord(t, m, costModelZeroUsage, now, nil)

	report := m.GetModelCostStats(time.Hour, prices)
	if report.APIType != "messages" {
		t.Fatalf("expected apiType=messages, got %q", report.APIType)
	}
	if len(report.Models) != 2 {
		t.Fatalf("expected 2 models (zero-usage record must be skipped), got %+v", report.Models)
	}

	byModel := modelCostByName(report)
	priced, ok := byModel[costModelPriced]
	if !ok {
		t.Fatalf("expected %q in report, got %+v", costModelPriced, report.Models)
	}
	if !priced.Priced || priced.CostUSD != 2*costPerRequestExclusive {
		t.Fatalf("expected priced cost=%v, got %+v", 2*costPerRequestExclusive, priced)
	}
	if priced.RequestCount != 2 || priced.InputTokens != 2_000_000 || priced.OutputTokens != 1_000_000 ||
		priced.CacheReadTokens != 800_000 || priced.CacheCreationTokens != 200_000 {
		t.Fatalf("token aggregation mismatch: %+v", priced)
	}

	unpriced, ok := byModel[costModelUnpriced]
	if !ok {
		t.Fatalf("expected %q in report, got %+v", costModelUnpriced, report.Models)
	}
	// 查不到单价必须表现为 Priced=false，不能按 0 元摊进总额。
	if unpriced.Priced || unpriced.CostUSD != 0 {
		t.Fatalf("expected unpriced entry with zero cost, got %+v", unpriced)
	}
	// Token 照常统计：未计价只影响金额，不该让用量凭空消失。
	if unpriced.RequestCount != 1 || unpriced.InputTokens != 1_000_000 {
		t.Fatalf("unpriced model must still aggregate tokens, got %+v", unpriced)
	}

	summary := report.Summary
	if summary.PricedRequests != 2 || summary.UnpricedRequests != 1 {
		t.Fatalf("expected 2 priced / 1 unpriced, got %+v", summary)
	}
	if summary.TotalCostUSD != 2*costPerRequestExclusive {
		t.Fatalf("expected summary total=%v, got %v", 2*costPerRequestExclusive, summary.TotalCostUSD)
	}
	if summary.InputCostUSD != 2000 || summary.OutputCostUSD != 2000 ||
		summary.CacheReadCostUSD != 80 || summary.CacheWriteCostUSD != 300 {
		t.Fatalf("cost breakdown mismatch: %+v", summary)
	}
}

// apiType 决定 input_tokens 是否已含缓存，直接改变输入费。这也是 MetricsManager
// 必须无条件拿到 apiType 的原因：空串会被当成 messages 口径，
// 在 responses / gemini / chat 上把缓存 Token 再收一次输入费。
func TestGetModelCostStatsCacheInclusiveFollowsAPIType(t *testing.T) {
	prices := newCostTestPrices(t)

	cases := map[string]float64{
		"messages":  costPerRequestExclusive,
		"responses": costPerRequestInclusive,
	}
	for apiType, want := range cases {
		t.Run(apiType, func(t *testing.T) {
			m := newCostTestManager(t, apiType)
			seedCostRecord(t, m, costModelPriced, time.Now(), costTestUsage())

			report := m.GetModelCostStats(time.Hour, prices)
			if report.Summary.TotalCostUSD != want {
				t.Fatalf("expected total=%v, got %v", want, report.Summary.TotalCostUSD)
			}
			// 只有输入费随口径变，另外三项在两种口径下必须一致。
			if report.Summary.OutputCostUSD != 1000 || report.Summary.CacheReadCostUSD != 40 ||
				report.Summary.CacheWriteCostUSD != 150 {
				t.Fatalf("cache flag must only affect input cost: %+v", report.Summary)
			}
		})
	}
}

// 花费降序 → 请求数降序 → 模型名升序。前端表格直接按返回顺序渲染，
// 排序不确定会让同一份数据每次刷新都换行序；未计价条目花费都是 0，
// 没有后两级兜底就完全靠 map 遍历顺序。
func TestGetModelCostStatsSortsDeterministically(t *testing.T) {
	prices := newCostTestPrices(t)
	m := newCostTestManager(t, "messages")

	now := time.Now()
	seedCostRecord(t, m, costModelPriced, now, costTestUsage()) // 花费 2190
	seedCostRecord(t, m, costModelCheap, now, costTestUsage())  // 花费 1
	seedCostRecord(t, m, costModelUnpriced, now, costTestUsage())
	seedCostRecord(t, m, costModelUnpriced, now, costTestUsage()) // 未计价，2 次请求
	seedCostRecord(t, m, "epsilon-test-model", now, costTestUsage())
	seedCostRecord(t, m, "zeta-test-model", now, costTestUsage())

	want := []string{
		costModelPriced,
		costModelCheap,
		costModelUnpriced,
		"epsilon-test-model",
		"zeta-test-model",
	}
	report := m.GetModelCostStats(time.Hour, prices)
	if len(report.Models) != len(want) {
		t.Fatalf("expected %d models, got %+v", len(want), report.Models)
	}
	for i, model := range want {
		if report.Models[i].Model != model {
			t.Fatalf("position %d: expected %q, got %q (full order %+v)", i, model, report.Models[i].Model, report.Models)
		}
	}
}

func TestGetModelCostStatsWindowAndDuration(t *testing.T) {
	prices := newCostTestPrices(t)
	m := newCostTestManager(t, "messages")

	now := time.Now()
	seedCostRecord(t, m, costModelPriced, now, costTestUsage())
	// 区间外的记录不能计入，否则"最近 1 小时花费"会把更早的账算进来。
	seedCostRecord(t, m, costModelPriced, now.Add(-2*time.Hour), costTestUsage())

	report := m.GetModelCostStats(time.Hour, prices)
	if report.Summary.PricedRequests != 1 || report.Summary.TotalCostUSD != costPerRequestExclusive {
		t.Fatalf("expected only the in-window record, got %+v", report.Summary)
	}

	// duration <= 0 直接返回空报表：Models 是非 nil 空切片，序列化成 [] 而不是 null。
	empty := m.GetModelCostStats(0, prices)
	if empty.Models == nil {
		t.Fatalf("Models must serialize as [], got nil")
	}
	if len(empty.Models) != 0 || empty.Summary != (CostStats{}) {
		t.Fatalf("expected empty report for non-positive duration, got %+v", empty)
	}
}

// 单价表未注入（nil）时全部记入未计价，而不是 panic、也不是静默按 0 元入账。
func TestGetModelCostStatsWithNilPricingStore(t *testing.T) {
	m := newCostTestManager(t, "messages")
	seedCostRecord(t, m, costModelPriced, time.Now(), costTestUsage())

	var nilPrices *pricing.Store
	report := m.GetModelCostStats(time.Hour, nilPrices)
	if len(report.Models) != 1 || report.Models[0].Priced {
		t.Fatalf("expected a single unpriced model, got %+v", report.Models)
	}
	if report.Summary.PricedRequests != 0 || report.Summary.UnpricedRequests != 1 ||
		report.Summary.TotalCostUSD != 0 {
		t.Fatalf("expected everything unpriced, got %+v", report.Summary)
	}
}

func TestCostStatsMergeAddsEveryField(t *testing.T) {
	total := CostStats{
		TotalCostUSD:      1,
		InputCostUSD:      2,
		OutputCostUSD:     3,
		CacheReadCostUSD:  4,
		CacheWriteCostUSD: 5,
		PricedRequests:    6,
		UnpricedRequests:  7,
	}
	total.merge(CostStats{
		TotalCostUSD:      10,
		InputCostUSD:      20,
		OutputCostUSD:     30,
		CacheReadCostUSD:  40,
		CacheWriteCostUSD: 50,
		PricedRequests:    60,
		UnpricedRequests:  70,
	})

	// 漏掉任一字段都会让"分桶之和 ≠ 汇总"，界面上表现为曲线与总额对不上。
	want := CostStats{
		TotalCostUSD:      11,
		InputCostUSD:      22,
		OutputCostUSD:     33,
		CacheReadCostUSD:  44,
		CacheWriteCostUSD: 55,
		PricedRequests:    66,
		UnpricedRequests:  77,
	}
	if total != want {
		t.Fatalf("expected %+v, got %+v", want, total)
	}
}

// 全局历史统计的汇总花费由分桶结果 merge 得到（每条记录只查一次单价表），
// 所以各桶花费之和必须严格等于汇总花费。
func TestGlobalHistoricalStatsCostMatchesBuckets(t *testing.T) {
	prices := newCostTestPrices(t)
	m := newCostTestManager(t, "messages")

	now := time.Now()
	seedCostRecord(t, m, costModelPriced, now, costTestUsage())
	seedCostRecord(t, m, costModelPriced, now.Add(-30*time.Minute), costTestUsage())
	seedCostRecord(t, m, costModelUnpriced, now, costTestUsage())
	seedCostRecord(t, m, costModelZeroUsage, now, nil)

	resp := m.GetGlobalHistoricalStatsWithTokens(time.Hour, 5*time.Minute, prices)

	var bucketTotal float64
	for _, point := range resp.DataPoints {
		bucketTotal += point.CostUSD
	}
	if bucketTotal != resp.Summary.Cost.TotalCostUSD {
		t.Fatalf("bucket sum %v must equal summary %v", bucketTotal, resp.Summary.Cost.TotalCostUSD)
	}
	if resp.Summary.Cost.TotalCostUSD != 2*costPerRequestExclusive {
		t.Fatalf("expected total=%v, got %v", 2*costPerRequestExclusive, resp.Summary.Cost.TotalCostUSD)
	}
	if resp.Summary.Cost.PricedRequests != 2 || resp.Summary.Cost.UnpricedRequests != 1 {
		t.Fatalf("expected 2 priced / 1 unpriced, got %+v", resp.Summary.Cost)
	}
	// 用量全 0 的那条仍然计入请求数，只是不参与计价。
	if resp.Summary.TotalRequests != 4 {
		t.Fatalf("expected 4 requests, got %d", resp.Summary.TotalRequests)
	}
}
