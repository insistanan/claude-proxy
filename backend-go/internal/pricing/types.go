package pricing

import (
	"math/big"
	"strings"
)

// CostBreakdown 单次请求或聚合周期的详细费用明细（单位：美元 USD）
type CostBreakdown struct {
	InputCost         float64 `json:"inputCost"`
	OutputCost        float64 `json:"outputCost"`
	CacheReadCost     float64 `json:"cacheReadCost"`
	CacheCreationCost float64 `json:"cacheCreationCost"`
	TotalCost         float64 `json:"totalCost"`
}

// ModelPricing 模型单价定义（单位：美元 / 每百万 Token，即 USD / 1M Tokens）
type ModelPricing struct {
	ModelID               string  `json:"modelId"`                     // 模型标识符，如 "claude-3-7-sonnet-20250219" 或 "gpt-4o"
	DisplayName           string  `json:"displayName,omitempty"`       // 人类可读名称
	InputCostPerM         float64 `json:"inputCostPerMillion"`         // 每百万输入 Token 单价 (USD)
	OutputCostPerM        float64 `json:"outputCostPerMillion"`        // 每百万输出 Token 单价 (USD)
	CacheReadCostPerM     float64 `json:"cacheReadCostPerMillion"`     // 每百万缓存读取 Token 单价 (USD)
	CacheCreationCostPerM float64 `json:"cacheCreationCostPerMillion"` // 每百万缓存创建/写入 Token 单价 (USD)
}

// TokenUsage 输入与输出 Token 用量
type TokenUsage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
}

// CostCalculator 高精度费用计算器
type CostCalculator struct{}

// Calculate 根据 Token 用量、定价配置与倍率计算成本。
// isCacheInclusive 表示 input_tokens 是否已包含缓存 Token：
// - 对于 Codex / Responses / Gemini，上游上报的 input_tokens 往往包含缓存部分，需要扣除后计算 fresh input 成本；
// - 对于 Claude / Anthropic，上报的 input_tokens 已经是纯 fresh input，无需再次扣除。
func Calculate(usage TokenUsage, pricing *ModelPricing, costMultiplier float64, isCacheInclusive bool) CostBreakdown {
	if pricing == nil {
		return CostBreakdown{}
	}
	if costMultiplier <= 0 {
		costMultiplier = 1.0
	}

	billableInput := usage.InputTokens
	if isCacheInclusive {
		billableInput = billableInput - usage.CacheReadTokens - usage.CacheCreationTokens
		if billableInput < 0 {
			billableInput = 0
		}
	}

	// 使用 big.Rat 进行高精度计算，防止微小 token 下 float64 累积精度失真
	million := big.NewRat(1000000, 1)

	calcItem := func(tokens int64, pricePerM float64) float64 {
		if tokens <= 0 || pricePerM <= 0 {
			return 0
		}
		rTokens := new(big.Rat).SetInt64(tokens)
		rPrice := new(big.Rat).SetFloat64(pricePerM)
		if rPrice == nil {
			return 0
		}
		// tokens * pricePerM / 1_000_000
		num := new(big.Rat).Mul(rTokens, rPrice)
		res := new(big.Rat).Quo(num, million)
		f, _ := res.Float64()
		return f
	}

	inputCost := calcItem(billableInput, pricing.InputCostPerM)
	outputCost := calcItem(usage.OutputTokens, pricing.OutputCostPerM)
	cacheReadCost := calcItem(usage.CacheReadTokens, pricing.CacheReadCostPerM)
	cacheCreationCost := calcItem(usage.CacheCreationTokens, pricing.CacheCreationCostPerM)

	baseTotal := inputCost + outputCost + cacheReadCost + cacheCreationCost
	totalCost := baseTotal * costMultiplier

	return CostBreakdown{
		InputCost:         inputCost,
		OutputCost:        outputCost,
		CacheReadCost:     cacheReadCost,
		CacheCreationCost: cacheCreationCost,
		TotalCost:         totalCost,
	}
}

// IsCacheInclusiveAPIType 根据 API 协议类型判断其输入 Token 是否包含缓存 Token
func IsCacheInclusiveAPIType(apiType string) bool {
	switch strings.ToLower(strings.TrimSpace(apiType)) {
	case "responses", "codex", "gemini", "chat", "openai":
		return true
	default:
		// "messages" (Claude / Anthropic) 默认为 false
		return false
	}
}
