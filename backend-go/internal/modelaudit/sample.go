package modelaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

type ApplicabilityStatus string

const (
	ApplicabilityApplicable  ApplicabilityStatus = "applicable"
	ApplicabilitySkipped     ApplicabilityStatus = "skipped"
	ApplicabilityUnsupported ApplicabilityStatus = "unsupported"
)

func (s ApplicabilityStatus) Valid() bool {
	return s == ApplicabilityApplicable || s == ApplicabilitySkipped || s == ApplicabilityUnsupported
}

type StrategyEvaluationStatus string

const (
	EvaluationCompleted            StrategyEvaluationStatus = "completed"
	EvaluationSkipped              StrategyEvaluationStatus = "skipped"
	EvaluationInsufficientEvidence StrategyEvaluationStatus = "insufficient_evidence"
	EvaluationUnsupported          StrategyEvaluationStatus = "unsupported"
	EvaluationFailed               StrategyEvaluationStatus = "failed"
)

func (s StrategyEvaluationStatus) Valid() bool {
	return s == EvaluationCompleted || s == EvaluationSkipped || s == EvaluationInsufficientEvidence ||
		s == EvaluationUnsupported || s == EvaluationFailed
}

type StrategyPlanInput struct {
	RunID          string                 `json:"runId"`
	Target         TargetSnapshot         `json:"target"`
	Config         StrategyConfigSnapshot `json:"config"`
	BaseSeed       uint64                 `json:"baseSeed"`
	MaximumSamples int                    `json:"maximumSamples"`
}

type SamplePlan struct {
	SampleID      string        `json:"sampleId"`
	Strategy      VersionedRef  `json:"strategy"`
	Ordinal       int           `json:"ordinal"`
	Seed          uint64        `json:"seed"`
	DelayBeforeMs int64         `json:"delayBeforeMs,omitempty"`
	Execution     ExecutionSpec `json:"execution"`
}

func (p SamplePlan) Validate() error {
	if strings.TrimSpace(p.SampleID) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "样本计划缺少 ID")
	}
	if err := p.Strategy.Validate("样本策略"); err != nil {
		return err
	}
	if p.Ordinal < 0 || p.DelayBeforeMs < 0 || p.DelayBeforeMs > maximumAuditSampleDelayMillis {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "样本序号或请求前等待时间无效")
	}
	return p.Execution.Validate()
}

type StrategySample struct {
	SampleID      string              `json:"sampleId"`
	RunID         string              `json:"runId"`
	Strategy      VersionedRef        `json:"strategy"`
	ConfigSHA256  string              `json:"configSha256"`
	Ordinal       int                 `json:"ordinal"`
	Seed          uint64              `json:"seed"`
	Applicability ApplicabilityStatus `json:"applicability"`
	Reason        string              `json:"reason,omitempty"`
	Execution     *ExecutionResult    `json:"execution,omitempty"`
	CapturedAt    time.Time           `json:"capturedAt"`
}

func (s StrategySample) Validate() error {
	if strings.TrimSpace(s.SampleID) == "" || strings.TrimSpace(s.RunID) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "策略样本缺少 sampleId 或 runId")
	}
	if err := s.Strategy.Validate("样本策略"); err != nil {
		return err
	}
	if !validSHA256(s.ConfigSHA256) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "策略样本配置哈希无效")
	}
	if s.Ordinal < 0 || s.CapturedAt.IsZero() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "策略样本序号或采集时间无效")
	}
	if !s.Applicability.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "策略样本适用性状态无效")
	}
	if s.Applicability == ApplicabilityApplicable {
		if s.Execution == nil || !s.Execution.Status.Terminal() {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "适用样本必须包含终态执行结果")
		}
		if err := s.Execution.Validate(); err != nil {
			return err
		}
	} else {
		if strings.TrimSpace(s.Reason) == "" {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "跳过或不支持样本必须说明原因")
		}
		if s.Execution != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "跳过或不支持样本不能伪装成已执行样本")
		}
	}
	return nil
}

type FeatureSnapshot struct {
	Schema FeatureSchemaRef `json:"schema"`
	Values json.RawMessage  `json:"values"`
	SHA256 string           `json:"sha256"`
}

func NewFeatureSnapshot(schema FeatureSchemaRef, values json.RawMessage) (FeatureSnapshot, error) {
	if !stableIDPattern.MatchString(schema.ID) || !semanticVersionPattern.MatchString(schema.Version) {
		return FeatureSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "特征 Schema 引用无效")
	}
	canonical, digest, err := canonicalJSONObject(values)
	if err != nil {
		return FeatureSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "特征值必须是 JSON 对象", err)
	}
	return FeatureSnapshot{Schema: schema, Values: canonical, SHA256: digest}, nil
}

type StrategyEvaluationInput struct {
	Config    StrategyConfigSnapshot `json:"config"`
	Samples   []StrategySample       `json:"samples"`
	Baselines []BaselineSnapshot     `json:"baselines,omitempty"`
}

type StrategyEvaluation struct {
	Strategy                VersionedRef             `json:"strategy"`
	Status                  StrategyEvaluationStatus `json:"status"`
	Reason                  string                   `json:"reason,omitempty"`
	Formal                  bool                     `json:"formal"`
	SampleCount             int                      `json:"sampleCount"`
	ApplicableSampleCount   int                      `json:"applicableSampleCount"`
	Features                []FeatureSnapshot        `json:"features,omitempty"`
	Metrics                 json.RawMessage          `json:"metrics,omitempty"`
	Baselines               []BaselineRef            `json:"baselines,omitempty"`
	Evidence                []EvidenceReference      `json:"evidence,omitempty"`
	AlternativeExplanations []string                 `json:"alternativeExplanations,omitempty"`
}

func (e StrategyEvaluation) Validate() error {
	if err := e.Strategy.Validate("策略评估"); err != nil {
		return err
	}
	if !e.Status.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "策略评估状态无效")
	}
	if e.SampleCount < 0 || e.ApplicableSampleCount < 0 || e.ApplicableSampleCount > e.SampleCount {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "策略评估样本计数无效")
	}
	if e.Formal && e.Status != EvaluationCompleted {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "只有完成状态可以生成正式结论")
	}
	if e.Status != EvaluationCompleted && strings.TrimSpace(e.Reason) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "非完成策略评估必须说明原因")
	}
	for _, feature := range e.Features {
		if !stableIDPattern.MatchString(feature.Schema.ID) || !semanticVersionPattern.MatchString(feature.Schema.Version) || !validSHA256(feature.SHA256) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "策略评估包含无效特征快照")
		}
		_, digest, err := canonicalJSONObject(feature.Values)
		if err != nil || digest != feature.SHA256 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "策略评估特征快照哈希不匹配", err)
		}
	}
	if len(e.Metrics) > 0 {
		if _, _, err := canonicalJSONObject(e.Metrics); err != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "策略评估指标必须是 JSON 对象", err)
		}
	}
	for _, baseline := range e.Baselines {
		if err := baseline.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type FrozenReportInput struct {
	RunID              string                   `json:"runId"`
	StrategySetVersion string                   `json:"strategySetVersion"`
	Targets            []TargetSnapshot         `json:"targets"`
	Strategies         []StrategyConfigSnapshot `json:"strategies"`
	Baselines          []BaselineSnapshot       `json:"baselines,omitempty"`
	Samples            []StrategySample         `json:"samples"`
	Evaluations        []StrategyEvaluation     `json:"evaluations"`
	CreatedAt          time.Time                `json:"createdAt"`
	SnapshotSHA256     string                   `json:"snapshotSha256"`
}

func FreezeReportInput(input FrozenReportInput) (FrozenReportInput, error) {
	input.RunID = strings.TrimSpace(input.RunID)
	if input.RunID == "" || !semanticVersionPattern.MatchString(input.StrategySetVersion) || input.CreatedAt.IsZero() {
		return FrozenReportInput{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "报告输入缺少运行 ID、策略集版本或创建时间")
	}
	for _, target := range input.Targets {
		if strings.TrimSpace(target.ChannelID) == "" || !target.ChannelKind.Valid() || !target.WireProtocol.Valid() {
			return FrozenReportInput{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "报告输入包含无效目标快照")
		}
	}
	strategies := make([]StrategyConfigSnapshot, len(input.Strategies))
	for index, snapshot := range input.Strategies {
		if err := snapshot.Ref.Validate("报告策略"); err != nil {
			return FrozenReportInput{}, err
		}
		canonical, digest, err := canonicalJSONObject(snapshot.Config)
		if err != nil || digest != snapshot.ConfigSHA256 {
			return FrozenReportInput{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "报告策略配置哈希不匹配", err)
		}
		snapshot.Config = canonical
		strategies[index] = snapshot
	}
	baselines := make([]BaselineSnapshot, len(input.Baselines))
	for index, baseline := range input.Baselines {
		if err := baseline.Validate(); err != nil {
			return FrozenReportInput{}, err
		}
		baselines[index] = cloneBaselineSnapshot(baseline)
	}
	samples := make([]StrategySample, len(input.Samples))
	for index, sample := range input.Samples {
		if err := sample.Validate(); err != nil {
			return FrozenReportInput{}, err
		}
		samples[index] = cloneStrategySample(sample)
	}
	evaluations := make([]StrategyEvaluation, len(input.Evaluations))
	for index, evaluation := range input.Evaluations {
		if err := evaluation.Validate(); err != nil {
			return FrozenReportInput{}, err
		}
		evaluations[index] = cloneStrategyEvaluation(evaluation)
	}
	input.Targets = append([]TargetSnapshot(nil), input.Targets...)
	input.Strategies = strategies
	input.Baselines = baselines
	input.Samples = samples
	input.Evaluations = evaluations
	input.CreatedAt = input.CreatedAt.UTC()
	input.SnapshotSHA256 = ""
	encoded, err := json.Marshal(input)
	if err != nil {
		return FrozenReportInput{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "序列化报告输入失败", err)
	}
	digest := sha256.Sum256(encoded)
	input.SnapshotSHA256 = hex.EncodeToString(digest[:])
	return input, nil
}

func cloneBaselineSnapshot(snapshot BaselineSnapshot) BaselineSnapshot {
	snapshot.TargetModels = append([]string(nil), snapshot.TargetModels...)
	snapshot.Protocols = append([]Protocol(nil), snapshot.Protocols...)
	snapshot.ThinkingLevels = append([]ThinkingLevel(nil), snapshot.ThinkingLevels...)
	snapshot.CompatibleStrategies = append([]VersionedRef(nil), snapshot.CompatibleStrategies...)
	return snapshot
}

func cloneStrategySample(sample StrategySample) StrategySample {
	if sample.Execution != nil {
		result := cloneExecutionResult(*sample.Execution)
		sample.Execution = &result
	}
	return sample
}

func cloneStrategyEvaluation(evaluation StrategyEvaluation) StrategyEvaluation {
	evaluation.Features = append([]FeatureSnapshot(nil), evaluation.Features...)
	for index := range evaluation.Features {
		evaluation.Features[index].Values = append(json.RawMessage(nil), evaluation.Features[index].Values...)
	}
	evaluation.Metrics = append(json.RawMessage(nil), evaluation.Metrics...)
	evaluation.Baselines = append([]BaselineRef(nil), evaluation.Baselines...)
	evaluation.Evidence = append([]EvidenceReference(nil), evaluation.Evidence...)
	evaluation.AlternativeExplanations = append([]string(nil), evaluation.AlternativeExplanations...)
	return evaluation
}
