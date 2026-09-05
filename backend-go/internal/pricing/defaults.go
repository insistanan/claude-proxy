package pricing

// DefaultBuiltinPricing 内置主流模型默认价格表（美元/百万Token）
// 数据依据官方公布及主流参考口径
var DefaultBuiltinPricing = []ModelPricing{
	// --- Anthropic / Claude ---
	{
		ModelID:               "claude-3-7-sonnet",
		DisplayName:           "Claude 3.7 Sonnet",
		InputCostPerM:         3.0,
		OutputCostPerM:        15.0,
		CacheReadCostPerM:     0.30,
		CacheCreationCostPerM: 3.75,
	},
	{
		ModelID:               "claude-3-5-sonnet",
		DisplayName:           "Claude 3.5 Sonnet",
		InputCostPerM:         3.0,
		OutputCostPerM:        15.0,
		CacheReadCostPerM:     0.30,
		CacheCreationCostPerM: 3.75,
	},
	{
		ModelID:               "claude-3-5-haiku",
		DisplayName:           "Claude 3.5 Haiku",
		InputCostPerM:         0.80,
		OutputCostPerM:        4.0,
		CacheReadCostPerM:     0.08,
		CacheCreationCostPerM: 1.0,
	},
	{
		ModelID:               "claude-3-opus",
		DisplayName:           "Claude 3 Opus",
		InputCostPerM:         15.0,
		OutputCostPerM:        75.0,
		CacheReadCostPerM:     1.50,
		CacheCreationCostPerM: 18.75,
	},

	// --- OpenAI ---
	{
		ModelID:               "gpt-4o",
		DisplayName:           "GPT-4o",
		InputCostPerM:         2.50,
		OutputCostPerM:        10.0,
		CacheReadCostPerM:     1.25,
		CacheCreationCostPerM: 2.50,
	},
	{
		ModelID:               "gpt-4o-mini",
		DisplayName:           "GPT-4o mini",
		InputCostPerM:         0.15,
		OutputCostPerM:        0.60,
		CacheReadCostPerM:     0.075,
		CacheCreationCostPerM: 0.15,
	},
	{
		ModelID:               "o1",
		DisplayName:           "OpenAI o1",
		InputCostPerM:         15.0,
		OutputCostPerM:        60.0,
		CacheReadCostPerM:     7.50,
		CacheCreationCostPerM: 15.0,
	},
	{
		ModelID:               "o3-mini",
		DisplayName:           "OpenAI o3-mini",
		InputCostPerM:         1.10,
		OutputCostPerM:        4.40,
		CacheReadCostPerM:     0.55,
		CacheCreationCostPerM: 1.10,
	},

	// --- Google Gemini ---
	{
		ModelID:               "gemini-2.0-flash",
		DisplayName:           "Gemini 2.0 Flash",
		InputCostPerM:         0.10,
		OutputCostPerM:        0.40,
		CacheReadCostPerM:     0.025,
		CacheCreationCostPerM: 0.10,
	},
	{
		ModelID:               "gemini-2.0-pro",
		DisplayName:           "Gemini 2.0 Pro",
		InputCostPerM:         2.50,
		OutputCostPerM:        10.0,
		CacheReadCostPerM:     0.625,
		CacheCreationCostPerM: 2.50,
	},
	{
		ModelID:               "gemini-1.5-pro",
		DisplayName:           "Gemini 1.5 Pro",
		InputCostPerM:         1.25,
		OutputCostPerM:        5.0,
		CacheReadCostPerM:     0.3125,
		CacheCreationCostPerM: 1.25,
	},
	{
		ModelID:               "gemini-1.5-flash",
		DisplayName:           "Gemini 1.5 Flash",
		InputCostPerM:         0.075,
		OutputCostPerM:        0.30,
		CacheReadCostPerM:     0.01875,
		CacheCreationCostPerM: 0.075,
	},

	// --- DeepSeek ---
	{
		ModelID:               "deepseek-chat",
		DisplayName:           "DeepSeek V3",
		InputCostPerM:         0.14,
		OutputCostPerM:        0.28,
		CacheReadCostPerM:     0.014,
		CacheCreationCostPerM: 0.14,
	},
	{
		ModelID:               "deepseek-reasoner",
		DisplayName:           "DeepSeek R1",
		InputCostPerM:         0.55,
		OutputCostPerM:        2.19,
		CacheReadCostPerM:     0.14,
		CacheCreationCostPerM: 0.55,
	},
}
