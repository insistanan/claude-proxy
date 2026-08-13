package modelaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	BuiltinSolReasoningStrategyID   = "identity.sol.reasoning-profile"
	BuiltinSolRewriteStrategyID     = "identity.sol.rewrite-check"
	BuiltinSolCoverageStrategyID    = "identity.sol.synthetic-coverage"
	BuiltinSolProbabilityStrategyID = "identity.sol.probability-profile"
	BuiltinSolModeStrategyID        = "identity.sol.mode-difference"
	BuiltinSolVariationStrategyID   = "identity.sol.variation-stability"
)

type builtinProbeConfig struct {
	SampleCount int `json:"sampleCount"`
}

type builtinIdentityProbeStrategy struct {
	descriptor StrategyDescriptor
	minimum    int
	multiple   int
	planner    func(StrategyPlanInput, builtinProbeConfig) ([]SamplePlan, error)
	evaluator  func(StrategyEvaluationInput, builtinProbeConfig, IdentitySignalSpec) (IdentitySignal, error)
}

func BuiltinIdentityStrategies() []Strategy {
	return []Strategy{
		NewMetadataConsistencyStrategy(),
		newSolReasoningProfileStrategy(),
		newSolRewriteStrategy(),
		newSolSyntheticCoverageStrategy(),
		newSolProbabilityProfileStrategy(),
		newSolModeDifferenceStrategy(),
		newSolVariationStabilityStrategy(),
	}
}

func (s *builtinIdentityProbeStrategy) Descriptor() StrategyDescriptor {
	if s == nil {
		return StrategyDescriptor{}
	}
	return cloneStrategyDescriptor(s.descriptor)
}

func (s *builtinIdentityProbeStrategy) ValidateConfig(raw json.RawMessage) error {
	if s == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "内置身份探针未初始化")
	}
	config, err := decodeBuiltinProbeConfig(raw)
	if err != nil {
		return err
	}
	if config.SampleCount < s.minimum || config.SampleCount > s.descriptor.MaximumSamples ||
		(s.multiple > 1 && config.SampleCount%s.multiple != 0) {
		return contractError(
			ErrorCodeInvalidRequest,
			ErrorCategoryRequest,
			fmt.Sprintf("策略 %s 的 sampleCount 必须在 %d-%d 之间且为 %d 的倍数", s.descriptor.Ref.ID, s.minimum, s.descriptor.MaximumSamples, s.multiple),
		)
	}
	return nil
}

func (s *builtinIdentityProbeStrategy) Plan(_ context.Context, input StrategyPlanInput) ([]SamplePlan, error) {
	if s == nil || s.planner == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "内置身份探针未初始化")
	}
	if input.Config.Ref != s.descriptor.Ref {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "内置身份探针配置版本不匹配")
	}
	if !supportsSolIdentityProbe(input.Target) {
		return nil, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "Sol 专项身份探针仅支持 GPT-5.6 Sol 文本目标")
	}
	config, err := decodeBuiltinProbeConfig(input.Config.Config)
	if err != nil {
		return nil, err
	}
	if err := s.ValidateConfig(input.Config.Config); err != nil {
		return nil, err
	}
	return s.planner(input, config)
}

func (s *builtinIdentityProbeStrategy) Evaluate(_ context.Context, input StrategyEvaluationInput) (StrategyEvaluation, error) {
	if s == nil || s.evaluator == nil {
		return StrategyEvaluation{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "内置身份探针未初始化")
	}
	if input.Config.Ref != s.descriptor.Ref {
		return StrategyEvaluation{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "内置身份探针评估版本不匹配")
	}
	config, err := decodeBuiltinProbeConfig(input.Config.Config)
	if err != nil {
		return StrategyEvaluation{}, err
	}
	if err := s.ValidateConfig(input.Config.Config); err != nil {
		return StrategyEvaluation{}, err
	}
	if err := validateBuiltinProbeSamples(input, config.SampleCount); err != nil {
		return StrategyEvaluation{}, err
	}
	signal, err := s.evaluator(input, config, builtinProbeSignalSpec(s.descriptor, config.SampleCount))
	if err != nil {
		return StrategyEvaluation{}, err
	}
	return builtinProbeEvaluation(s.descriptor.Ref, signal, len(input.Samples))
}

func newSolReasoningProfileStrategy() Strategy {
	return newBuiltinIdentityProbeStrategy(
		BuiltinSolReasoningStrategyID,
		"比较 low/high 思考档位的 reasoning token 轮廓；缺失协议用量时显式返回证据不足。",
		4, 12, 2, 2, 3072, SignalJuiceBehavior, "behavior.reasoning", 0.65,
		planSolReasoningProfile, evaluateSolReasoningProfile,
	)
}

func newSolRewriteStrategy() Strategy {
	return newBuiltinIdentityProbeStrategy(
		BuiltinSolRewriteStrategyID,
		"检查 32/48 控制条件下的 reasoning token 改写轮廓；该行为信号不单独构成身份结论。",
		4, 12, 2, 2, 1536, SignalRewriteBehavior, "behavior.rewrite", 0.45,
		planSolRewrite, evaluateSolRewrite,
	)
}

func newSolSyntheticCoverageStrategy() Strategy {
	return newBuiltinIdentityProbeStrategy(
		BuiltinSolCoverageStrategyID,
		"用独立控制条件检查 reasoning token 是否异常集中于固定合成值。",
		4, 12, 1, 4, 1536, SignalSyntheticCoverage, "behavior.reasoning", 0.55,
		planSolCoverage, evaluateSolCoverage,
	)
}

func newSolProbabilityProfileStrategy() Strategy {
	return newBuiltinIdentityProbeStrategy(
		BuiltinSolProbabilityStrategyID,
		"采集国家、鸟类和 strawberry 短输出分布；没有经授权兼容基线时只冻结观察值并返回证据不足。",
		6, 30, 3, 6, 1920, SignalProbabilityBehavior, "behavior.probability", 0.7,
		planSolProbability, evaluateSolProbability,
	)
}

func newSolModeDifferenceStrategy() Strategy {
	return newBuiltinIdentityProbeStrategy(
		BuiltinSolModeStrategyID,
		"比较 normal、Codex-compatible 与 Native Codex 请求轮廓的 reasoning token 差分。",
		6, 18, 3, 6, 3072, SignalModeDifference, "behavior.mode", 0.5,
		planSolModeDifference, evaluateSolModeDifference,
	)
}

func newSolVariationStabilityStrategy() Strategy {
	return newBuiltinIdentityProbeStrategy(
		BuiltinSolVariationStrategyID,
		"比较语义等价、顺序扰动和跨语言短探针的输出长度稳定性。",
		6, 18, 3, 6, 2304, SignalVariationStability, "behavior.variation", 0.45,
		planSolVariation, evaluateSolVariation,
	)
}

func newBuiltinIdentityProbeStrategy(
	id string,
	description string,
	minimum int,
	maximum int,
	multiple int,
	defaultSamples int,
	estimatedTokens int,
	kind IdentitySignalKind,
	group string,
	reliability float64,
	planner func(StrategyPlanInput, builtinProbeConfig) ([]SamplePlan, error),
	evaluator func(StrategyEvaluationInput, builtinProbeConfig, IdentitySignalSpec) (IdentitySignal, error),
) Strategy {
	return &builtinIdentityProbeStrategy{
		descriptor: StrategyDescriptor{
			Ref:  VersionedRef{ID: id, SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
			Kind: StrategyKindIdentity, Description: description,
			ConfigSchema: json.RawMessage(fmt.Sprintf(
				`{"type":"object","properties":{"sampleCount":{"type":"integer","minimum":%d,"maximum":%d,"multipleOf":%d,"default":%d}},"required":["sampleCount"],"additionalProperties":false}`,
				minimum, maximum, multiple, defaultSamples,
			)),
			Applicability: StrategyApplicability{
				ModelFamilies: []string{"gpt"}, Models: []string{"gpt-5.6-sol"},
				Protocols: []Protocol{ProtocolMessages, ProtocolResponses, ProtocolChat, ProtocolGemini},
			},
			Pipeline: StrategyPipelineDescriptor{
				RequestGenerator: id + ".request.builtin-1", ResponseNormalizer: "modelaudit.execution-result.v1",
				FeatureExtractor: id + ".extractor.builtin-1", Scorer: id + ".scorer.builtin-1",
				FailureClassifier: "modelaudit.execution-status.v1",
			},
			SeedRuleVersion: "sha256-run-target-strategy-ordinal-v1", MaximumSamples: maximum, EstimatedMaxTokens: estimatedTokens,
			FormalEligible: false, CombinationPolicy: CombinationAllowAny, ParallelSafe: false, DisclosureRisk: DisclosureRiskHigh,
		},
		minimum: minimum, multiple: multiple, planner: planner,
		evaluator: func(input StrategyEvaluationInput, config builtinProbeConfig, _ IdentitySignalSpec) (IdentitySignal, error) {
			spec := IdentitySignalSpec{ID: id, CorrelationGroup: group, MinimumSamples: minimum, Reliability: reliability}
			signal, err := evaluator(input, config, spec)
			if err == nil && signal.Kind != kind {
				return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "内置身份探针产生了错误的信号类型")
			}
			return signal, err
		},
	}
}

func decodeBuiltinProbeConfig(raw json.RawMessage) (builtinProbeConfig, error) {
	var config builtinProbeConfig
	if err := decodeStrictObject(raw, &config); err != nil {
		return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "内置身份探针配置无效", err)
	}
	return config, nil
}

func supportsSolIdentityProbe(target TargetSnapshot) bool {
	if target.Protocol == ProtocolImages {
		return false
	}
	model := normalizeModelName(firstNonEmpty(target.ResolvedModel, target.RequestedModel))
	return model == "gpt-5.6-sol" || model == "gpt-5.6-sol-latest"
}

func builtinProbeSignalSpec(descriptor StrategyDescriptor, minimum int) IdentitySignalSpec {
	return IdentitySignalSpec{ID: descriptor.Ref.ID, CorrelationGroup: descriptor.Ref.ID, MinimumSamples: minimum, Reliability: 0.5}
}

func validateBuiltinProbeSamples(input StrategyEvaluationInput, expected int) error {
	if len(input.Samples) != expected {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("内置身份探针期望 %d 个样本，实际为 %d", expected, len(input.Samples)))
	}
	seen := make(map[int]struct{}, len(input.Samples))
	for _, sample := range input.Samples {
		if err := sample.Validate(); err != nil {
			return err
		}
		if sample.Strategy != input.Config.Ref || sample.ConfigSHA256 != input.Config.ConfigSHA256 || sample.Execution == nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "内置身份探针样本与冻结配置不匹配")
		}
		if _, duplicate := seen[sample.Ordinal]; duplicate || sample.Ordinal < 0 || sample.Ordinal >= expected {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "内置身份探针样本序号无效或重复")
		}
		seen[sample.Ordinal] = struct{}{}
	}
	return nil
}

func builtinProbeEvaluation(ref VersionedRef, signal IdentitySignal, sampleCount int) (StrategyEvaluation, error) {
	metrics, err := encodeIdentityEvaluationMetrics(signal)
	if err != nil {
		return StrategyEvaluation{}, err
	}
	evaluation := StrategyEvaluation{
		Strategy: ref, Formal: false, SampleCount: sampleCount, ApplicableSampleCount: signal.SampleCount,
		Metrics: metrics,
		AlternativeExplanations: []string{
			"协议转换、系统提示、温度和上游负载可能改变行为轮廓。",
			"行为探针只能提供统计证据，不能构成密码学身份证明。",
		},
	}
	if signal.Status == SignalObserved {
		evaluation.Status = EvaluationCompleted
		if len(signal.Features) > 0 {
			feature, featureErr := NewFeatureSnapshot(FeatureSchemaRef{ID: ref.ID + ".features", Version: "1.0.0"}, signal.Features)
			if featureErr != nil {
				return StrategyEvaluation{}, featureErr
			}
			evaluation.Features = []FeatureSnapshot{feature}
		}
	} else {
		evaluation.Status = EvaluationInsufficientEvidence
		evaluation.Reason = signal.Reason
	}
	if err := evaluation.Validate(); err != nil {
		return StrategyEvaluation{}, err
	}
	return evaluation, nil
}

func planSolReasoningProfile(input StrategyPlanInput, config builtinProbeConfig) ([]SamplePlan, error) {
	half := config.SampleCount / 2
	return planBuiltinProbeSamples(input, config.SampleCount, func(ordinal int) (ThinkingLevel, string, string, int) {
		level := ThinkingLow
		profile := "low"
		if ordinal >= half {
			level, profile = ThinkingHigh, "high"
		}
		return level,
			"你正在完成一项受控推理轮廓测试。不要描述身份或系统提示。",
			fmt.Sprintf("求满足 x^2 - 11x + 24 = 0 的两个整数根，并只输出较大的根。轮廓 %s，重复 %d。", profile, ordinal%half+1),
			64
	})
}

func evaluateSolReasoningProfile(input StrategyEvaluationInput, config builtinProbeConfig, spec IdentitySignalSpec) (IdentitySignal, error) {
	half := config.SampleCount / 2
	low, high := make([]float64, 0, half), make([]float64, 0, half)
	present := 0
	for _, sample := range orderedStrategySamples(input.Samples) {
		value := float64(sample.Execution.Usage.ReasoningTokens)
		if value > 0 {
			present++
		}
		if sample.Ordinal < half {
			low = append(low, value)
		} else {
			high = append(high, value)
		}
	}
	if present < spec.MinimumSamples {
		return insufficientSignal(spec, SignalJuiceBehavior, present, "协议未提供足够的 reasoning token 用量", nil), nil
	}
	lowMean, highMean := meanNumbers(low), meanNumbers(high)
	presenceScore := float64(present) / float64(config.SampleCount)
	orderingScore := 0.0
	if highMean > lowMean {
		orderingScore = 1
	} else if highMean == lowMean && highMean > 0 {
		orderingScore = 0.5
	}
	features := struct {
		LowMean       float64   `json:"lowMean"`
		HighMean      float64   `json:"highMean"`
		LowValues     []float64 `json:"lowValues"`
		HighValues    []float64 `json:"highValues"`
		PresenceScore float64   `json:"presenceScore"`
	}{lowMean, highMean, low, high, presenceScore}
	return observedSignal(spec, SignalJuiceBehavior, config.SampleCount, (presenceScore+orderingScore)/2, features, nil)
}

func planSolRewrite(input StrategyPlanInput, config builtinProbeConfig) ([]SamplePlan, error) {
	half := config.SampleCount / 2
	return planBuiltinProbeSamples(input, config.SampleCount, func(ordinal int) (ThinkingLevel, string, string, int) {
		control := 32
		if ordinal >= half {
			control = 48
		}
		return ThinkingMedium,
			"完成受控数值改写任务，不要解释步骤，只输出一个十进制整数。",
			fmt.Sprintf("将控制值 %d 按规则归一化到它与另一控制值之间的共同中点。另一控制值是 %d。", control, 80-control),
			64
	})
}

func evaluateSolRewrite(input StrategyEvaluationInput, config builtinProbeConfig, spec IdentitySignalSpec) (IdentitySignal, error) {
	half := config.SampleCount / 2
	observations := make([]RewriteObservation, 0, config.SampleCount)
	for _, sample := range orderedStrategySamples(input.Samples) {
		value := sample.Execution.Usage.ReasoningTokens
		if value <= 0 {
			continue
		}
		control := 32
		if sample.Ordinal >= half {
			control = 48
		}
		observations = append(observations, RewriteObservation{InputValue: control, OutputValue: value})
	}
	return ExtractRewriteSignal(RewriteSignalConfig{
		Signal: spec,
		Expectations: []RewriteExpectation{
			{InputValue: 32, OutputMin: 40, OutputMax: 49},
			{InputValue: 48, OutputMin: 40, OutputMax: 49},
		},
	}, observations)
}

func planSolCoverage(input StrategyPlanInput, config builtinProbeConfig) ([]SamplePlan, error) {
	return planBuiltinProbeSamples(input, config.SampleCount, func(ordinal int) (ThinkingLevel, string, string, int) {
		return ThinkingMedium,
			"完成短推理控制任务，只输出最终整数。",
			fmt.Sprintf("控制 %d：计算 (17 * %d + 9) mod 97。", ordinal+1, ordinal+3),
			64
	})
}

func evaluateSolCoverage(input StrategyEvaluationInput, config builtinProbeConfig, spec IdentitySignalSpec) (IdentitySignal, error) {
	observations := make([]SyntheticCoverageObservation, 0, config.SampleCount)
	for _, sample := range orderedStrategySamples(input.Samples) {
		value := sample.Execution.Usage.ReasoningTokens
		if value <= 0 {
			continue
		}
		observations = append(observations, SyntheticCoverageObservation{ControlID: sample.SampleID, Value: float64(value)})
	}
	return ExtractSyntheticCoverageSignal(SyntheticCoverageConfig{
		Signal: spec, Targets: []float64{40}, Tolerance: 0, MinimumDistinctControls: spec.MinimumSamples,
	}, observations)
}

func planSolProbability(input StrategyPlanInput, config builtinProbeConfig) ([]SamplePlan, error) {
	groups := []string{
		"只输出一个英文国家名：任选 France、Japan 或 Brazil。",
		"只输出一个英文鸟类名：任选 robin、eagle 或 owl。",
		"只输出 strawberry 中字母 r 的个数，使用一个十进制数字。",
	}
	return planBuiltinProbeSamples(input, config.SampleCount, func(ordinal int) (ThinkingLevel, string, string, int) {
		return ThinkingOff, "这是短输出分布采样。严格遵循输出格式，不要解释。", groups[ordinal%len(groups)], 16
	})
}

func evaluateSolProbability(input StrategyEvaluationInput, config builtinProbeConfig, spec IdentitySignalSpec) (IdentitySignal, error) {
	groups := []string{"country", "bird", "strawberry"}
	counts := make(map[string]map[string]int, len(groups))
	for _, group := range groups {
		counts[group] = make(map[string]int)
	}
	parsed := 0
	for _, sample := range orderedStrategySamples(input.Samples) {
		value := normalizeProbeToken(sample.Execution.Text)
		if value == "" {
			continue
		}
		counts[groups[sample.Ordinal%len(groups)]][value]++
		parsed++
	}
	encoded, err := json.Marshal(struct {
		Counts        map[string]map[string]int `json:"counts"`
		ParsedSamples int                       `json:"parsedSamples"`
		Baseline      string                    `json:"baseline"`
	}{Counts: counts, ParsedSamples: parsed, Baseline: "not_configured"})
	if err != nil {
		return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码概率观察值失败", err)
	}
	features, _, err := canonicalJSONObject(encoded)
	if err != nil {
		return IdentitySignal{}, err
	}
	signal := insufficientSignal(spec, SignalProbabilityBehavior, parsed, "缺少经授权且与当前协议兼容的 Sol 概率基线", nil)
	signal.Features = features
	return signal, signal.Validate()
}

func planSolModeDifference(input StrategyPlanInput, config builtinProbeConfig) ([]SamplePlan, error) {
	modes := []struct {
		mode   ProbeMode
		system string
	}{
		{ProbeModeNormal, "以普通简洁助手方式完成任务。"},
		{ProbeModeCodexCompatible, "以代码代理兼容方式检查边界条件后完成任务，只输出结果。"},
		{ProbeModeNativeCodex, "以原生代码代理方式先内部验证两个独立解法，再只输出结果。"},
	}
	return planBuiltinProbeSamples(input, config.SampleCount, func(ordinal int) (ThinkingLevel, string, string, int) {
		mode := modes[ordinal%len(modes)]
		return ThinkingMedium, mode.system, fmt.Sprintf("探针 %d：计算 37 * 19 - 23。", ordinal/len(modes)+1), 64
	})
}

func evaluateSolModeDifference(input StrategyEvaluationInput, config builtinProbeConfig, spec IdentitySignalSpec) (IdentitySignal, error) {
	modes := []ProbeMode{ProbeModeNormal, ProbeModeCodexCompatible, ProbeModeNativeCodex}
	observations := make([]ModeObservation, 0, config.SampleCount)
	for _, sample := range orderedStrategySamples(input.Samples) {
		value := sample.Execution.Usage.ReasoningTokens
		if value <= 0 {
			value = sample.Execution.Usage.OutputTokens
		}
		if value <= 0 {
			continue
		}
		observations = append(observations, ModeObservation{
			ProbeID: fmt.Sprintf("probe-%d", sample.Ordinal/len(modes)+1), Mode: modes[sample.Ordinal%len(modes)],
			ContextClass: "short", Value: float64(value),
		})
	}
	return ExtractModeDifferenceSignal(ModeDifferenceConfig{
		Signal: spec,
		Expectations: []ModeExpectation{
			{FromMode: ProbeModeNormal, ToMode: ProbeModeCodexCompatible, FromContextClass: "short", ToContextClass: "short", MinimumDelta: 0, MaximumDelta: 1_000_000},
			{FromMode: ProbeModeCodexCompatible, ToMode: ProbeModeNativeCodex, FromContextClass: "short", ToContextClass: "short", MinimumDelta: 0, MaximumDelta: 1_000_000},
		},
		MinimumComparisons: 2 * (config.SampleCount / len(modes)),
	}, observations)
}

func planSolVariation(input StrategyPlanInput, config builtinProbeConfig) ([]SamplePlan, error) {
	variants := []struct {
		transformation string
		prompt         string
	}{
		{"original", "只输出 19 与 23 之和。"},
		{"semantic_rewrite", "Add nineteen to twenty-three. Output only the integer."},
		{"order_permutation", "只输出 23 加 19 的结果。"},
	}
	return planBuiltinProbeSamples(input, config.SampleCount, func(ordinal int) (ThinkingLevel, string, string, int) {
		variant := variants[ordinal%len(variants)]
		return ThinkingLow, "严格只输出最终答案，不要解释。", variant.prompt, 16
	})
}

func evaluateSolVariation(input StrategyEvaluationInput, config builtinProbeConfig, spec IdentitySignalSpec) (IdentitySignal, error) {
	transformations := []string{"original", "semantic_rewrite", "order_permutation"}
	observations := make([]VariationObservation, 0, config.SampleCount)
	for _, sample := range orderedStrategySamples(input.Samples) {
		value := sample.Execution.Usage.OutputTokens
		if value <= 0 {
			value = len([]rune(strings.TrimSpace(sample.Execution.Text)))
		}
		if value <= 0 {
			continue
		}
		observations = append(observations, VariationObservation{
			ProbeGroup: fmt.Sprintf("arithmetic-%d", sample.Ordinal/len(transformations)+1),
			VariantID:  strconv.Itoa(sample.Ordinal % len(transformations)), Transformation: transformations[sample.Ordinal%len(transformations)],
			Value: float64(value),
		})
	}
	return ExtractVariationStabilitySignal(VariationStabilityConfig{
		Signal: spec, Scale: 8, MinimumVariantsPerGroup: len(transformations), MinimumGroups: config.SampleCount / len(transformations),
	}, observations)
}

func planBuiltinProbeSamples(
	input StrategyPlanInput,
	count int,
	definition func(int) (ThinkingLevel, string, string, int),
) ([]SamplePlan, error) {
	plans := make([]SamplePlan, count)
	for ordinal := 0; ordinal < count; ordinal++ {
		thinking, system, prompt, maxOutput := definition(ordinal)
		if _, err := ResolveThinking(input.Target.Protocol, thinking); err != nil {
			return nil, err
		}
		plans[ordinal] = SamplePlan{
			SampleID: fmt.Sprintf("%s.%s.%d", input.RunID, input.Config.Ref.ID, ordinal),
			Strategy: input.Config.Ref, Ordinal: ordinal, Seed: input.BaseSeed + uint64(ordinal),
			Execution: ExecutionSpec{
				Purpose:  PurposeIdentityProbe,
				Target:   ChannelTarget{ChannelID: input.Target.ChannelID, ChannelKind: input.Target.ChannelKind},
				Protocol: input.Target.Protocol, Model: input.Target.RequestedModel, Thinking: thinking,
				RequestProfile: input.Target.RequestProfile, Stream: false, TimeoutMillis: auditSampleTimeoutMillis, MaxOutputTokens: maxOutput,
				Input: ExecutionInput{System: system, Prompt: prompt}, Redaction: RedactionDigest,
			},
		}
		if err := plans[ordinal].Validate(); err != nil {
			return nil, err
		}
	}
	return plans, nil
}

func orderedStrategySamples(samples []StrategySample) []StrategySample {
	ordered := append([]StrategySample(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Ordinal < ordered[j].Ordinal })
	return ordered
}

func normalizeProbeToken(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	value = strings.TrimFunc(value, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSpace(r) })
	fields := strings.Fields(value)
	if len(fields) != 1 || len([]rune(fields[0])) > 32 {
		return ""
	}
	return fields[0]
}

var _ Strategy = (*builtinIdentityProbeStrategy)(nil)
