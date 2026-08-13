package modelaudit

import (
	"encoding/json"
	"fmt"
)

// IdentityPresetLevel 定义身份审计的档位级别
type IdentityPresetLevel string

const (
	IdentityPresetLow    IdentityPresetLevel = "low"
	IdentityPresetMedium IdentityPresetLevel = "medium"
	IdentityPresetHigh   IdentityPresetLevel = "high"
)

func (l IdentityPresetLevel) Valid() bool {
	return l == IdentityPresetLow || l == IdentityPresetMedium || l == IdentityPresetHigh
}

// IdentityPresetDescriptor 描述一个身份审计预设档位
type IdentityPresetDescriptor struct {
	Ref            VersionedRef        `json:"ref"`
	Level          IdentityPresetLevel `json:"level"`
	Description    string              `json:"description"`
	FormalEligible bool                `json:"formalEligible"`
	Strategies     []StrategySelection `json:"strategies"`
	EstimateTokens int                 `json:"estimateTokens"`
	EstimateReqs   int                 `json:"estimateRequests"`
}

func (d IdentityPresetDescriptor) Validate() error {
	if err := d.Ref.Validate("身份审计预设"); err != nil {
		return err
	}
	if !d.Level.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份审计预设档位无效")
	}
	if len(d.Strategies) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份审计预设策略列表为空")
	}
	return nil
}

// BuiltinIdentityPresets 返回内置的三个身份审计档位预设
func BuiltinIdentityPresets() []IdentityPresetDescriptor {
	return []IdentityPresetDescriptor{
		buildLowPreset(),
		buildMediumPreset(),
		buildHighPreset(),
	}
}

// buildLowPreset 低档：快速排查，成本最低，仅基础 Juice 和元数据检查
// 参考 gpt56: 14 请求，检测 high/low Juice、32/48 输出完整性、Juice 显式覆盖
func buildLowPreset() IdentityPresetDescriptor {
	metadataConfig := map[string]interface{}{"sampleCount": 1}
	reasoningConfig := map[string]interface{}{"sampleCount": 6} // high 4 + low 2
	rewriteConfig := map[string]interface{}{"sampleCount": 4}   // 合同要求 4-12 的偶数
	coverageConfig := map[string]interface{}{"sampleCount": 4}  // 显式覆盖检查

	return IdentityPresetDescriptor{
		Ref:            VersionedRef{ID: "identity.preset.low", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		Level:          IdentityPresetLow,
		Description:    "低档：快速排查，仅基础 Juice 和元数据检查，约 15 请求，不生成正式身份结论",
		FormalEligible: false,
		Strategies: []StrategySelection{
			{StrategyID: BuiltinMetadataStrategyID, Enabled: true, Config: mustMarshalJSON(metadataConfig)},
			{StrategyID: BuiltinSolReasoningStrategyID, Enabled: true, Config: mustMarshalJSON(reasoningConfig)},
			{StrategyID: BuiltinSolRewriteStrategyID, Enabled: true, Config: mustMarshalJSON(rewriteConfig)},
			{StrategyID: BuiltinSolCoverageStrategyID, Enabled: true, Config: mustMarshalJSON(coverageConfig)},
			{StrategyID: BuiltinSolProbabilityStrategyID, Enabled: false, Config: json.RawMessage(`{"sampleCount":6}`)},
			{StrategyID: BuiltinSolModeStrategyID, Enabled: false, Config: json.RawMessage(`{"sampleCount":6}`)},
			{StrategyID: BuiltinSolVariationStrategyID, Enabled: false, Config: json.RawMessage(`{"sampleCount":6}`)},
		},
		EstimateTokens: 8_000,
		EstimateReqs:   15,
	}
}

// buildMediumPreset 中档：日常完整检测，能给出正式行为指纹结论
// 参考 gpt56: 64 请求，更多 Juice 档位 + 固定随机国家 + B80 字符题
func buildMediumPreset() IdentityPresetDescriptor {
	metadataConfig := map[string]interface{}{"sampleCount": 1}
	reasoningConfig := map[string]interface{}{"sampleCount": 12} // high 12 + low/xhigh/max 各6
	rewriteConfig := map[string]interface{}{"sampleCount": 4}
	coverageConfig := map[string]interface{}{"sampleCount": 4}
	probabilityConfig := map[string]interface{}{"sampleCount": 24} // 国家20 + B80 10，简化为24
	modeConfig := map[string]interface{}{"sampleCount": 6}
	variationConfig := map[string]interface{}{"sampleCount": 6}

	return IdentityPresetDescriptor{
		Ref:            VersionedRef{ID: "identity.preset.medium", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		Level:          IdentityPresetMedium,
		Description:    "中档：日常完整检测，包含行为指纹探针，约 57 请求，能生成正式身份结论",
		FormalEligible: true,
		Strategies: []StrategySelection{
			{StrategyID: BuiltinMetadataStrategyID, Enabled: true, Config: mustMarshalJSON(metadataConfig)},
			{StrategyID: BuiltinSolReasoningStrategyID, Enabled: true, Config: mustMarshalJSON(reasoningConfig)},
			{StrategyID: BuiltinSolRewriteStrategyID, Enabled: true, Config: mustMarshalJSON(rewriteConfig)},
			{StrategyID: BuiltinSolCoverageStrategyID, Enabled: true, Config: mustMarshalJSON(coverageConfig)},
			{StrategyID: BuiltinSolProbabilityStrategyID, Enabled: true, Config: mustMarshalJSON(probabilityConfig)},
			{StrategyID: BuiltinSolModeStrategyID, Enabled: true, Config: mustMarshalJSON(modeConfig)},
			{StrategyID: BuiltinSolVariationStrategyID, Enabled: true, Config: mustMarshalJSON(variationConfig)},
		},
		EstimateTokens: 35_000,
		EstimateReqs:   57,
	}
}

// buildHighPreset 高档：排查客户端识别和长上下文差异，组合两种请求格式和上下文
// 参考 gpt56: 202 请求，全部内置探针 + 两种请求格式 + 短/32K 两种上下文
func buildHighPreset() IdentityPresetDescriptor {
	metadataConfig := map[string]interface{}{"sampleCount": 2}
	reasoningConfig := map[string]interface{}{"sampleCount": 12}
	rewriteConfig := map[string]interface{}{"sampleCount": 4}
	coverageConfig := map[string]interface{}{"sampleCount": 8}
	probabilityConfig := map[string]interface{}{"sampleCount": 30}
	modeConfig := map[string]interface{}{"sampleCount": 18}
	variationConfig := map[string]interface{}{"sampleCount": 18}

	return IdentityPresetDescriptor{
		Ref:            VersionedRef{ID: "identity.preset.high", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		Level:          IdentityPresetHigh,
		Description:    "高档：深度审计，覆盖全部内置探针，约 92 请求，生成正式结论",
		FormalEligible: true,
		Strategies: []StrategySelection{
			{StrategyID: BuiltinMetadataStrategyID, Enabled: true, Config: mustMarshalJSON(metadataConfig)},
			{StrategyID: BuiltinSolReasoningStrategyID, Enabled: true, Config: mustMarshalJSON(reasoningConfig)},
			{StrategyID: BuiltinSolRewriteStrategyID, Enabled: true, Config: mustMarshalJSON(rewriteConfig)},
			{StrategyID: BuiltinSolCoverageStrategyID, Enabled: true, Config: mustMarshalJSON(coverageConfig)},
			{StrategyID: BuiltinSolProbabilityStrategyID, Enabled: true, Config: mustMarshalJSON(probabilityConfig)},
			{StrategyID: BuiltinSolModeStrategyID, Enabled: true, Config: mustMarshalJSON(modeConfig)},
			{StrategyID: BuiltinSolVariationStrategyID, Enabled: true, Config: mustMarshalJSON(variationConfig)},
		},
		EstimateTokens: 120_000,
		EstimateReqs:   92,
	}
}

func mustMarshalJSON(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustMarshalJSON 失败: %v", err))
	}
	return data
}
