package modelaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

type AuditModStrategyConfig struct {
	SampleCount      int   `json:"sampleCount"`
	SampleIntervalMs int64 `json:"sampleIntervalMs"`
}

type AuditModPromptData struct {
	RunID          string `json:"runId"`
	Ordinal        int    `json:"ordinal"`
	SampleNumber   int    `json:"sampleNumber"`
	SampleCount    int    `json:"sampleCount"`
	Seed           uint64 `json:"seed"`
	ChannelID      string `json:"channelId"`
	Protocol       string `json:"protocol"`
	RequestedModel string `json:"requestedModel"`
	ResolvedModel  string `json:"resolvedModel"`
	Thinking       string `json:"thinking"`
}

type DeclarativeAuditModStrategy struct {
	bundle     AuditModBundle
	descriptor StrategyDescriptor
	prompt     *template.Template
	rules      AuditModRuleSet
}

func NewDeclarativeAuditModStrategy(bundle AuditModBundle) (*DeclarativeAuditModStrategy, error) {
	if err := bundle.Reference.Validate(); err != nil {
		return nil, err
	}
	if err := bundle.Manifest.Validate(); err != nil {
		return nil, err
	}
	probePath, _ := normalizeAuditModRelativePath(bundle.Manifest.ProbeFile, "探针提示词")
	promptContent, found := bundle.Files[probePath]
	if !found {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 探针提示词快照不存在")
	}
	prompt, err := template.New(bundle.Manifest.ID).Option("missingkey=error").Parse(string(promptContent))
	if err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "解析 Mod 探针提示词模板失败", err)
	}
	rulesPath, _ := normalizeAuditModRelativePath(bundle.Manifest.RulesFile, "规则文件")
	rules, err := decodeAuditModRuleSet(bundle.Files[rulesPath])
	if err != nil {
		return nil, err
	}
	configSchema, err := auditModStrategyConfigSchema(bundle.Manifest.Sampling)
	if err != nil {
		return nil, err
	}
	implementation := "mod-" + bundle.Reference.ContentSHA256
	descriptor := StrategyDescriptor{
		Ref: VersionedRef{
			ID: bundle.Manifest.ID, SemanticVersion: bundle.Manifest.Version, ImplementationVersion: implementation,
		},
		Kind:         StrategyKindIdentity,
		Description:  bundle.Manifest.Description,
		ConfigSchema: configSchema,
		Applicability: StrategyApplicability{Protocols: []Protocol{
			ProtocolMessages, ProtocolResponses, ProtocolChat, ProtocolGemini,
		}},
		Pipeline: StrategyPipelineDescriptor{
			RequestGenerator:   "audit.mod.prompt." + implementation,
			ResponseNormalizer: "audit.mod.response.v1",
			FeatureExtractor:   "audit.mod.parser." + string(bundle.Manifest.Parser.Type) + ".v1",
			Scorer:             "audit.mod.rules.v1",
			FailureClassifier:  "modelaudit.execution-status.v1",
		},
		SeedRuleVersion:    "sha256-run-target-strategy-ordinal-v1",
		MaximumSamples:     bundle.Manifest.Sampling.MaximumSamples,
		EstimatedMaxTokens: auditModEstimatedMaxTokens(bundle.Manifest.Sampling),
		FormalEligible:     false, CombinationPolicy: CombinationAllowAny, ParallelSafe: false, DisclosureRisk: DisclosureRiskHigh,
	}
	if err := validateStrategyDescriptor(descriptor); err != nil {
		return nil, err
	}
	return &DeclarativeAuditModStrategy{bundle: cloneAuditModBundle(bundle), descriptor: descriptor, prompt: prompt, rules: rules}, nil
}

func (s *DeclarativeAuditModStrategy) Descriptor() StrategyDescriptor {
	if s == nil {
		return StrategyDescriptor{}
	}
	return cloneStrategyDescriptor(s.descriptor)
}

func (s *DeclarativeAuditModStrategy) ModReference() AuditModReference {
	if s == nil {
		return AuditModReference{}
	}
	return s.bundle.Reference
}

func (s *DeclarativeAuditModStrategy) Bundle() AuditModBundle {
	if s == nil {
		return AuditModBundle{}
	}
	return cloneAuditModBundle(s.bundle)
}

func (s *DeclarativeAuditModStrategy) ValidateConfig(raw json.RawMessage) error {
	if s == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "声明式 Mod 策略未初始化")
	}
	config, err := decodeAuditModStrategyConfig(raw)
	if err != nil {
		return err
	}
	if config.SampleCount <= 0 || config.SampleCount > s.bundle.Manifest.Sampling.MaximumSamples ||
		config.SampleIntervalMs < 0 || config.SampleIntervalMs > maximumAuditSampleDelayMillis {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 样本数或样本请求间隔无效")
	}
	return nil
}

func (s *DeclarativeAuditModStrategy) Plan(_ context.Context, input StrategyPlanInput) ([]SamplePlan, error) {
	if s == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "声明式 Mod 策略未初始化")
	}
	if input.Config.Ref != s.descriptor.Ref {
		return nil, contractError(ErrorCodeConflict, ErrorCategoryRequest, "Mod 策略配置引用与内容快照不一致")
	}
	if input.Target.Protocol == ProtocolImages || !input.Target.Protocol.Valid() {
		return nil, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "提示词 Mod 只支持文本协议")
	}
	config, err := decodeAuditModStrategyConfig(input.Config.Config)
	if err != nil {
		return nil, err
	}
	if err := s.ValidateConfig(input.Config.Config); err != nil {
		return nil, err
	}
	observationCount := config.SampleCount
	if input.MaximumSamples > 0 && observationCount > input.MaximumSamples {
		observationCount = input.MaximumSamples
	}
	requestCount := observationCount
	if s.bundle.Manifest.Sampling.Mode == AuditModSamplingBatch {
		requestCount = 1
	}
	plans := make([]SamplePlan, requestCount)
	for ordinal := 0; ordinal < requestCount; ordinal++ {
		seed := input.BaseSeed + uint64(ordinal)
		data := AuditModPromptData{
			RunID: input.RunID, Ordinal: ordinal, SampleNumber: ordinal + 1, SampleCount: observationCount, Seed: seed,
			ChannelID: input.Target.ChannelID, Protocol: string(input.Target.Protocol), RequestedModel: input.Target.RequestedModel,
			ResolvedModel: input.Target.ResolvedModel, Thinking: string(input.Target.Thinking),
		}
		var rendered bytes.Buffer
		if err := s.prompt.Execute(&rendered, data); err != nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "渲染 Mod 探针提示词失败", err)
		}
		prompt := strings.TrimSpace(rendered.String())
		if prompt == "" {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 探针提示词渲染结果为空")
		}
		delay := int64(0)
		if ordinal > 0 {
			delay = config.SampleIntervalMs
		}
		plans[ordinal] = SamplePlan{
			SampleID: fmt.Sprintf("%s.%s.%d", input.RunID, s.descriptor.Ref.ID, ordinal),
			Strategy: s.descriptor.Ref, Ordinal: ordinal, Seed: seed, DelayBeforeMs: delay,
			Execution: ExecutionSpec{
				Purpose:  PurposeIdentityProbe,
				Target:   ChannelTarget{ChannelID: input.Target.ChannelID, ChannelKind: input.Target.ChannelKind},
				Protocol: input.Target.Protocol, Model: input.Target.RequestedModel, Thinking: input.Target.Thinking,
				RequestProfile: input.Target.RequestProfile, Stream: false, TimeoutMillis: 120_000,
				MaxOutputTokens: s.bundle.Manifest.Sampling.MaxOutputTokens,
				Input:           ExecutionInput{Prompt: prompt}, Redaction: RedactionDigest,
			},
		}
		if err := plans[ordinal].Validate(); err != nil {
			return nil, err
		}
	}
	return plans, nil
}

func (s *DeclarativeAuditModStrategy) Evaluate(_ context.Context, input StrategyEvaluationInput) (StrategyEvaluation, error) {
	if s == nil {
		return StrategyEvaluation{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "声明式 Mod 策略未初始化")
	}
	if input.Config.Ref != s.descriptor.Ref {
		return StrategyEvaluation{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "Mod 策略评估引用与内容快照不一致")
	}
	config, err := decodeAuditModStrategyConfig(input.Config.Config)
	if err != nil {
		return StrategyEvaluation{}, err
	}
	return evaluateDeclarativeAuditMod(s.descriptor, s.bundle, s.rules, config, input)
}

func decodeAuditModStrategyConfig(raw json.RawMessage) (AuditModStrategyConfig, error) {
	var config AuditModStrategyConfig
	if err := decodeStrictObject(raw, &config); err != nil {
		return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 策略配置无效", err)
	}
	return config, nil
}

func auditModStrategyConfigSchema(sampling AuditModSampling) (json.RawMessage, error) {
	encoded, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"sampleCount": map[string]any{
				"type": "integer", "minimum": 1, "maximum": sampling.MaximumSamples, "default": sampling.DefaultSampleCount,
			},
			"sampleIntervalMs": map[string]any{
				"type": "integer", "minimum": 0, "maximum": maximumAuditSampleDelayMillis, "default": sampling.SampleIntervalMs,
			},
		},
		"required": []string{"sampleCount", "sampleIntervalMs"}, "additionalProperties": false,
	})
	if err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码 Mod 策略配置 Schema 失败", err)
	}
	canonical, _, err := canonicalJSONObject(encoded)
	return canonical, err
}

func auditModEstimatedMaxTokens(sampling AuditModSampling) int {
	if sampling.MaximumSamples > int(^uint(0)>>1)/sampling.MaxOutputTokens {
		return int(^uint(0) >> 1)
	}
	return sampling.MaximumSamples * sampling.MaxOutputTokens
}

func AuditModReferenceFromStrategyRef(ref VersionedRef) (AuditModReference, bool) {
	if !strings.HasPrefix(ref.ImplementationVersion, "mod-") {
		return AuditModReference{}, false
	}
	reference := AuditModReference{
		ID: ref.ID, Version: ref.SemanticVersion, ContentSHA256: strings.TrimPrefix(ref.ImplementationVersion, "mod-"),
	}
	return reference, reference.Validate() == nil
}

var _ Strategy = (*DeclarativeAuditModStrategy)(nil)
