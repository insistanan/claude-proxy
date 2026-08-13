package modelaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const BuiltinMetadataStrategyID = "identity.metadata-consistency"

type MetadataConsistencyStrategyConfig struct {
	SampleCount int `json:"sampleCount"`
}

type MetadataConsistencyStrategy struct {
	descriptor StrategyDescriptor
}

func NewMetadataConsistencyStrategy() *MetadataConsistencyStrategy {
	return &MetadataConsistencyStrategy{descriptor: StrategyDescriptor{
		Ref:          VersionedRef{ID: BuiltinMetadataStrategyID, SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		Kind:         StrategyKindIdentity,
		Description:  "检查返回模型、协议终态、usage 和响应结构的一致性；不依赖外部基线，不单独构成正式身份认证。",
		ConfigSchema: json.RawMessage(`{"type":"object","properties":{"sampleCount":{"type":"integer","minimum":1,"maximum":5}},"required":["sampleCount"],"additionalProperties":false}`),
		Applicability: StrategyApplicability{
			ModelFamilies: []string{"gpt"},
			Protocols:     []Protocol{ProtocolMessages, ProtocolResponses, ProtocolChat, ProtocolGemini},
		},
		Pipeline: StrategyPipelineDescriptor{
			RequestGenerator: "identity.metadata.request.builtin-1", ResponseNormalizer: "modelaudit.execution-result.v1",
			FeatureExtractor: "identity.metadata.extractor.builtin-1", Scorer: "identity.metadata.consistency.builtin-1",
			FailureClassifier: "modelaudit.execution-status.v1",
		},
		SeedRuleVersion: "sha256-run-target-strategy-ordinal-v1", MaximumSamples: 5, EstimatedMaxTokens: 640,
		FormalEligible: false, CombinationPolicy: CombinationAllowAny, ParallelSafe: false, DisclosureRisk: DisclosureRiskLow,
	}}
}

func (s *MetadataConsistencyStrategy) Descriptor() StrategyDescriptor {
	if s == nil {
		return StrategyDescriptor{}
	}
	return cloneStrategyDescriptor(s.descriptor)
}

func (s *MetadataConsistencyStrategy) ValidateConfig(raw json.RawMessage) error {
	config, err := decodeMetadataConsistencyStrategyConfig(raw)
	if err != nil {
		return err
	}
	if config.SampleCount < 1 || config.SampleCount > s.descriptor.MaximumSamples {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "元数据一致性策略样本数必须为 1-5")
	}
	return nil
}

func (s *MetadataConsistencyStrategy) Plan(_ context.Context, input StrategyPlanInput) ([]SamplePlan, error) {
	if s == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "元数据一致性策略未初始化")
	}
	if input.Config.Ref != s.descriptor.Ref {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "元数据一致性策略配置版本不匹配")
	}
	config, err := decodeMetadataConsistencyStrategyConfig(input.Config.Config)
	if err != nil {
		return nil, err
	}
	if err := s.ValidateConfig(input.Config.Config); err != nil {
		return nil, err
	}
	if !metadataStrategySupportsTarget(input.Target) {
		return nil, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "元数据一致性策略仅支持 GPT 文本协议目标")
	}
	count := config.SampleCount
	if input.MaximumSamples > 0 && count > input.MaximumSamples {
		count = input.MaximumSamples
	}
	plans := make([]SamplePlan, count)
	for ordinal := 0; ordinal < count; ordinal++ {
		prompt := fmt.Sprintf("请仅用一句简短自然语言回答：你能处理哪一类任务？样本编号 %d。", ordinal+1)
		plans[ordinal] = SamplePlan{
			SampleID: fmt.Sprintf("%s.%s.%d", input.RunID, BuiltinMetadataStrategyID, ordinal),
			Strategy: s.descriptor.Ref, Ordinal: ordinal, Seed: input.BaseSeed + uint64(ordinal),
			Execution: ExecutionSpec{
				Purpose:  PurposeIdentityProbe,
				Target:   ChannelTarget{ChannelID: input.Target.ChannelID, ChannelKind: input.Target.ChannelKind},
				Protocol: input.Target.Protocol, Model: input.Target.RequestedModel, Thinking: input.Target.Thinking,
				RequestProfile: input.Target.RequestProfile, Stream: false, TimeoutMillis: auditSampleTimeoutMillis, MaxOutputTokens: 128,
				Input: ExecutionInput{Prompt: prompt}, Redaction: RedactionDigest,
			},
		}
		if err := plans[ordinal].Validate(); err != nil {
			return nil, err
		}
	}
	return plans, nil
}

func (s *MetadataConsistencyStrategy) Evaluate(_ context.Context, input StrategyEvaluationInput) (StrategyEvaluation, error) {
	if s == nil {
		return StrategyEvaluation{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "元数据一致性策略未初始化")
	}
	if input.Config.Ref != s.descriptor.Ref {
		return StrategyEvaluation{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "元数据一致性策略评估版本不匹配")
	}
	config, err := decodeMetadataConsistencyStrategyConfig(input.Config.Config)
	if err != nil {
		return StrategyEvaluation{}, err
	}
	observations := make([]MetadataObservation, 0, len(input.Samples))
	for _, sample := range input.Samples {
		if err := sample.Validate(); err != nil {
			return StrategyEvaluation{}, err
		}
		if sample.Execution == nil {
			continue
		}
		structureValid := sample.Execution.Status == StatusCompleted && sample.Execution.ProtocolTerminal == ProtocolTerminalCompleted
		capabilityValid := sample.Execution.Target.Protocol.Valid() && sample.Execution.Target.WireProtocol.Valid()
		observations = append(observations, MetadataObservationFromExecution(*sample.Execution, &structureValid, &capabilityValid))
	}
	signal, err := ExtractMetadataConsistencySignal(MetadataConsistencyConfig{
		Signal:               IdentitySignalSpec{ID: BuiltinMetadataStrategyID, CorrelationGroup: "metadata", MinimumSamples: config.SampleCount, Reliability: 0.55},
		RequireReturnedModel: false, RequireUsage: false, RequireStructureAssessment: true, RequireCapabilityAssessment: true,
	}, observations)
	if err != nil {
		return StrategyEvaluation{}, err
	}
	encodedMetrics, err := json.Marshal(struct {
		Signal IdentitySignal `json:"signal"`
	}{Signal: signal})
	if err != nil {
		return StrategyEvaluation{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码元数据一致性策略指标失败", err)
	}
	metrics, _, err := canonicalJSONObject(encodedMetrics)
	if err != nil {
		return StrategyEvaluation{}, err
	}
	evaluation := StrategyEvaluation{
		Strategy: s.descriptor.Ref, Formal: false, SampleCount: len(input.Samples), ApplicableSampleCount: len(observations),
		Metrics: metrics, Evidence: append([]EvidenceReference(nil), signal.Evidence...),
		AlternativeExplanations: []string{"上游可能重写模型名或省略 usage。", "协议转换层异常可能造成元数据矛盾。"},
	}
	if signal.Status == SignalObserved {
		evaluation.Status = EvaluationCompleted
		if len(signal.Features) > 0 {
			feature, err := NewFeatureSnapshot(FeatureSchemaRef{ID: "identity.metadata.features", Version: "1.0.0"}, signal.Features)
			if err != nil {
				return StrategyEvaluation{}, err
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

func decodeMetadataConsistencyStrategyConfig(raw json.RawMessage) (MetadataConsistencyStrategyConfig, error) {
	var config MetadataConsistencyStrategyConfig
	if err := decodeStrictObject(raw, &config); err != nil {
		return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "元数据一致性策略配置无效", err)
	}
	return config, nil
}

func metadataStrategySupportsTarget(target TargetSnapshot) bool {
	if target.Protocol == ProtocolImages || !strings.HasPrefix(strings.ToLower(target.ResolvedModel), "gpt") {
		return false
	}
	for _, protocol := range []Protocol{ProtocolMessages, ProtocolResponses, ProtocolChat, ProtocolGemini} {
		if target.Protocol == protocol {
			return true
		}
	}
	return false
}

var _ Strategy = (*MetadataConsistencyStrategy)(nil)
