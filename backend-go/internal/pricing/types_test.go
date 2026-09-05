package pricing

import (
	"math"
	"testing"
)

// testPrice 单价刻意取整千，方便直接用整数校验金额：
// 1M token * 1000 USD/1M = 1000 USD。
var testPrice = &ModelPricing{
	ModelID:               "m",
	InputCostPerM:         1000,
	OutputCostPerM:        2000,
	CacheReadCostPerM:     100,
	CacheCreationCostPerM: 1500,
}

func TestCalculateCacheInclusiveSemantics(t *testing.T) {
	usage := TokenUsage{
		InputTokens:         1_000_000,
		OutputTokens:        500_000,
		CacheReadTokens:     400_000,
		CacheCreationTokens: 100_000,
	}

	// messages（Anthropic）口径：input_tokens 已是纯新增输入，不再扣缓存。
	exclusive := Calculate(usage, testPrice, 1, false)
	if exclusive.InputCost != 1000 {
		t.Fatalf("expected inputCost=1000, got %v", exclusive.InputCost)
	}

	// responses / gemini / chat 口径：input_tokens 含缓存，扣掉后才是计费输入。
	inclusive := Calculate(usage, testPrice, 1, true)
	if inclusive.InputCost != 500 {
		t.Fatalf("expected inputCost=500, got %v", inclusive.InputCost)
	}

	// 其余三项与口径无关，两次结果必须一致。
	if exclusive.OutputCost != inclusive.OutputCost ||
		exclusive.CacheReadCost != inclusive.CacheReadCost ||
		exclusive.CacheCreationCost != inclusive.CacheCreationCost {
		t.Fatalf("cache flag must only affect input cost: %+v vs %+v", exclusive, inclusive)
	}
	if inclusive.TotalCost != 500+1000+40+150 {
		t.Fatalf("expected total=1690, got %v", inclusive.TotalCost)
	}
}

func TestCalculateEdgeCases(t *testing.T) {
	t.Run("cache inclusive input clamps at zero", func(t *testing.T) {
		// 上游偶尔上报 cached > input（累积式统计），扣成负数会把花费算成负的。
		usage := TokenUsage{InputTokens: 100_000, CacheReadTokens: 400_000}
		got := Calculate(usage, testPrice, 1, true)
		if got.InputCost != 0 {
			t.Fatalf("expected inputCost=0, got %v", got.InputCost)
		}
	})

	t.Run("non positive multiplier falls back to 1", func(t *testing.T) {
		usage := TokenUsage{OutputTokens: 1_000_000}
		for _, multiplier := range []float64{0, -1} {
			got := Calculate(usage, testPrice, multiplier, false)
			if got.TotalCost != 2000 {
				t.Fatalf("multiplier %v: expected total=2000, got %v", multiplier, got.TotalCost)
			}
		}
	})

	t.Run("multiplier scales total only", func(t *testing.T) {
		usage := TokenUsage{OutputTokens: 1_000_000}
		got := Calculate(usage, testPrice, 0.5, false)
		if got.OutputCost != 2000 {
			t.Fatalf("expected outputCost to stay unscaled, got %v", got.OutputCost)
		}
		if got.TotalCost != 1000 {
			t.Fatalf("expected total=1000, got %v", got.TotalCost)
		}
	})

	t.Run("nil pricing and non positive tokens cost nothing", func(t *testing.T) {
		if got := Calculate(TokenUsage{InputTokens: 1_000_000}, nil, 1, false); got.TotalCost != 0 {
			t.Fatalf("expected zero breakdown, got %+v", got)
		}
		zero := Calculate(TokenUsage{InputTokens: -5}, testPrice, 1, false)
		if zero.TotalCost != 0 {
			t.Fatalf("expected zero total, got %+v", zero)
		}
	})

	t.Run("NaN price is skipped instead of poisoning the total", func(t *testing.T) {
		// Store 会拦住 NaN 单价，但 Calculate 也可能被直接调用；
		// 一个 NaN 会让总额变 NaN，界面上表现为整段账单消失。
		poisoned := &ModelPricing{ModelID: "m", InputCostPerM: math.NaN(), OutputCostPerM: 2000}
		got := Calculate(TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000}, poisoned, 1, false)
		if math.IsNaN(got.TotalCost) {
			t.Fatalf("total must not be NaN, got %+v", got)
		}
		if got.TotalCost != 2000 {
			t.Fatalf("expected total=2000, got %v", got.TotalCost)
		}
	})
}

func TestIsCacheInclusiveAPIType(t *testing.T) {
	inclusive := []string{"responses", "codex", "gemini", "chat", "openai", " OpenAI ", "CODEX"}
	for _, apiType := range inclusive {
		if !IsCacheInclusiveAPIType(apiType) {
			t.Fatalf("%q must be cache inclusive", apiType)
		}
	}
	// messages 之外的未知值也按 messages 处理；这也是 apiType 必须显式注入的原因，
	// 空串会被当成 Anthropic 口径，在 responses 上把缓存重复计一次输入费。
	for _, apiType := range []string{"messages", "claude", "", "images"} {
		if IsCacheInclusiveAPIType(apiType) {
			t.Fatalf("%q must not be cache inclusive", apiType)
		}
	}
}
