package modelaudit

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

type IdentityConclusion string

const (
	IdentityMatched               IdentityConclusion = "matched"
	IdentitySuspectedSubstitution IdentityConclusion = "suspected_substitution"
	IdentitySuspectedMixture      IdentityConclusion = "suspected_mixture"
	IdentityUnknown               IdentityConclusion = "unknown"
	IdentityInsufficientEvidence  IdentityConclusion = "insufficient_evidence"
	IdentityUnsupported           IdentityConclusion = "unsupported"
)

func (c IdentityConclusion) Valid() bool {
	return c == IdentityMatched || c == IdentitySuspectedSubstitution || c == IdentitySuspectedMixture ||
		c == IdentityUnknown || c == IdentityInsufficientEvidence || c == IdentityUnsupported
}

type IdentitySignalKind string

const (
	SignalJuiceBehavior       IdentitySignalKind = "juice_behavior"
	SignalRewriteBehavior     IdentitySignalKind = "rewrite_behavior"
	SignalSyntheticCoverage   IdentitySignalKind = "synthetic_coverage"
	SignalProbabilityBehavior IdentitySignalKind = "probability_behavior"
	SignalModeDifference      IdentitySignalKind = "mode_difference"
	SignalMetadataConsistency IdentitySignalKind = "metadata_consistency"
	SignalVariationStability  IdentitySignalKind = "variation_stability"
	SignalCustomMod           IdentitySignalKind = "custom_mod"
)

func (k IdentitySignalKind) Valid() bool {
	switch k {
	case SignalJuiceBehavior, SignalRewriteBehavior, SignalSyntheticCoverage, SignalProbabilityBehavior,
		SignalModeDifference, SignalMetadataConsistency, SignalVariationStability, SignalCustomMod:
		return true
	default:
		return false
	}
}

type SignalStatus string

const (
	SignalObserved             SignalStatus = "observed"
	SignalSkipped              SignalStatus = "skipped"
	SignalInsufficientEvidence SignalStatus = "insufficient_evidence"
	SignalUnsupported          SignalStatus = "unsupported"
	SignalFailed               SignalStatus = "failed"
)

func (s SignalStatus) Valid() bool {
	return s == SignalObserved || s == SignalSkipped || s == SignalInsufficientEvidence ||
		s == SignalUnsupported || s == SignalFailed
}

type IdentitySignal struct {
	ID               string              `json:"id"`
	Kind             IdentitySignalKind  `json:"kind"`
	CorrelationGroup string              `json:"correlationGroup"`
	Status           SignalStatus        `json:"status"`
	Reason           string              `json:"reason,omitempty"`
	SampleCount      int                 `json:"sampleCount"`
	Reliability      float64             `json:"reliability"`
	Score            *float64            `json:"score,omitempty"`
	CandidateScores  map[string]float64  `json:"candidateScores,omitempty"`
	Features         json.RawMessage     `json:"features,omitempty"`
	Baseline         *BaselineRef        `json:"baseline,omitempty"`
	Evidence         []EvidenceReference `json:"evidence,omitempty"`
}

func (s IdentitySignal) Validate() error {
	if !stableIDPattern.MatchString(s.ID) || !s.Kind.Valid() || strings.TrimSpace(s.CorrelationGroup) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份信号身份或类型无效")
	}
	if !s.Status.Valid() || s.SampleCount < 0 || !unitInterval(s.Reliability) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份信号状态、样本数或可靠度无效")
	}
	if s.Status == SignalObserved {
		if s.Score == nil || !unitInterval(*s.Score) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "已观察身份信号必须提供 0-1 分数")
		}
	} else if strings.TrimSpace(s.Reason) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "未观察身份信号必须说明原因")
	}
	for candidate, score := range s.CandidateScores {
		if strings.TrimSpace(candidate) == "" || !unitInterval(score) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份信号候选分数无效")
		}
	}
	if len(s.Features) > 0 {
		if _, _, err := canonicalJSONObject(s.Features); err != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份信号特征必须是 JSON 对象", err)
		}
	}
	if s.Baseline != nil {
		if err := s.Baseline.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type CandidateFit struct {
	Candidate     string  `json:"candidate"`
	JSD           float64 `json:"jsd"`
	LogLikelihood float64 `json:"logLikelihood"`
}

type CandidateClassification struct {
	BestCandidate string         `json:"bestCandidate,omitempty"`
	OOD           bool           `json:"ood"`
	Fits          []CandidateFit `json:"fits"`
}

type IdentityReport struct {
	Aggregator              VersionedRef            `json:"aggregator"`
	DeclaredModel           string                  `json:"declaredModel,omitempty"`
	RequestedModel          string                  `json:"requestedModel,omitempty"`
	ReturnedModels          []string                `json:"returnedModels,omitempty"`
	Conclusion              IdentityConclusion      `json:"conclusion"`
	Formal                  bool                    `json:"formal"`
	Confidence              float64                 `json:"confidence"`
	ConsistencyScore        float64                 `json:"consistencyScore"`
	SampleCount             int                     `json:"sampleCount"`
	ApplicableSignals       int                     `json:"applicableSignals"`
	CorrelationGroups       int                     `json:"correlationGroups"`
	Signals                 []IdentitySignal        `json:"signals"`
	GroupScores             []IdentityGroupScore    `json:"groupScores,omitempty"`
	CandidateFit            CandidateClassification `json:"candidateFit"`
	Mixture                 *MixtureEstimate        `json:"mixture,omitempty"`
	Temporal                *TemporalAssessment     `json:"temporal,omitempty"`
	Qualification           FormalQualification     `json:"qualification"`
	BaselineChecks          []BaselineCompatibility `json:"baselineChecks,omitempty"`
	AlternativeExplanations []string                `json:"alternativeExplanations,omitempty"`
}

func (r IdentityReport) Validate() error {
	if err := r.Aggregator.Validate("身份聚合器"); err != nil {
		return err
	}
	if !r.Conclusion.Valid() || !unitInterval(r.Confidence) || !unitInterval(r.ConsistencyScore) ||
		r.SampleCount < 0 || r.ApplicableSignals < 0 || r.CorrelationGroups < 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份报告结论、置信度或计数无效")
	}
	if r.Formal && (r.Conclusion == IdentityInsufficientEvidence || r.Conclusion == IdentityUnsupported) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "证据不足或不支持结论不能标记为正式")
	}
	if r.Formal && !r.Qualification.Eligible {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "正式身份报告缺少正式结论资格")
	}
	for _, signal := range r.Signals {
		if err := signal.Validate(); err != nil {
			return err
		}
	}
	if r.CorrelationGroups != len(r.GroupScores) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份报告相关组计数与明细不一致")
	}
	signalIDs := make(map[string]struct{}, len(r.Signals))
	for _, signal := range r.Signals {
		signalIDs[signal.ID] = struct{}{}
	}
	groupNames := make(map[string]struct{}, len(r.GroupScores))
	groupSignalIDs := make(map[string]struct{})
	groupWeightTotal := 0.0
	for _, group := range r.GroupScores {
		if err := group.Validate(); err != nil {
			return err
		}
		if _, duplicate := groupNames[group.Group]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份报告包含重复相关组")
		}
		groupNames[group.Group] = struct{}{}
		groupWeightTotal += group.Weight
		for _, signalID := range group.SignalIDs {
			if _, exists := signalIDs[signalID]; !exists {
				return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份相关组引用了不存在的信号")
			}
			if _, duplicate := groupSignalIDs[signalID]; duplicate {
				return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份信号被重复计入相关组")
			}
			groupSignalIDs[signalID] = struct{}{}
		}
	}
	if r.ApplicableSignals != len(groupSignalIDs) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份报告适用信号计数与相关组明细不一致")
	}
	if len(r.GroupScores) > 0 && math.Abs(groupWeightTotal-1) > 1e-9 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份报告相关组权重未归一化")
	}
	if err := r.Qualification.Validate(); err != nil {
		return err
	}
	for _, check := range r.BaselineChecks {
		if err := check.Validate(); err != nil {
			return err
		}
	}
	if err := validateCandidateClassification(r.CandidateFit); err != nil {
		return err
	}
	if r.Mixture != nil {
		if err := validateMixtureEstimate(*r.Mixture); err != nil {
			return err
		}
	}
	if r.Temporal != nil {
		if err := r.Temporal.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func SmoothDistribution(counts []float64, alpha float64) ([]float64, error) {
	if len(counts) == 0 || math.IsNaN(alpha) || math.IsInf(alpha, 0) || alpha <= 0 {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "概率平滑需要非空计数和正 alpha")
	}
	total := 0.0
	for _, count := range counts {
		if math.IsNaN(count) || math.IsInf(count, 0) || count < 0 {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "概率计数包含无效值")
		}
		total += count
	}
	denominator := total + alpha*float64(len(counts))
	probabilities := make([]float64, len(counts))
	for index, count := range counts {
		probabilities[index] = (count + alpha) / denominator
	}
	return probabilities, nil
}

func JensenShannonDivergence(left []float64, right []float64) (float64, error) {
	left, err := normalizedProbabilityVector(left)
	if err != nil {
		return 0, err
	}
	right, err = normalizedProbabilityVector(right)
	if err != nil {
		return 0, err
	}
	if len(left) != len(right) {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "JSD 分布维度不一致")
	}
	middle := make([]float64, len(left))
	for index := range left {
		middle[index] = (left[index] + right[index]) / 2
	}
	jsd := (kullbackLeibler(left, middle) + kullbackLeibler(right, middle)) / 2
	if jsd < 0 && jsd > -1e-12 {
		jsd = 0
	}
	return jsd, nil
}

func ClassifyCandidateDistribution(counts []float64, candidates map[string][]float64, alpha float64, oodThreshold float64) (CandidateClassification, error) {
	if len(candidates) == 0 || !unitInterval(oodThreshold) {
		return CandidateClassification{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "候选分布或 OOD 阈值无效")
	}
	observed, err := SmoothDistribution(counts, alpha)
	if err != nil {
		return CandidateClassification{}, err
	}
	result := CandidateClassification{Fits: make([]CandidateFit, 0, len(candidates))}
	names := make([]string, 0, len(candidates))
	for name := range candidates {
		names = append(names, name)
	}
	sort.Strings(names)
	bestJSD := math.Inf(1)
	for _, name := range names {
		candidate, err := normalizedProbabilityVector(candidates[name])
		if err != nil || len(candidate) != len(observed) {
			return CandidateClassification{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("候选 %q 分布无效", name), err)
		}
		jsd, err := JensenShannonDivergence(observed, candidate)
		if err != nil {
			return CandidateClassification{}, err
		}
		fit := CandidateFit{Candidate: name, JSD: jsd, LogLikelihood: categoricalLogLikelihood(counts, candidate)}
		result.Fits = append(result.Fits, fit)
		if jsd < bestJSD {
			bestJSD, result.BestCandidate = jsd, name
		}
	}
	result.OOD = bestJSD > oodThreshold
	if result.OOD {
		result.BestCandidate = ""
	}
	return result, nil
}

func normalizedProbabilityVector(values []float64) ([]float64, error) {
	if len(values) == 0 {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "概率分布不能为空")
	}
	total := 0.0
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "概率分布包含无效值")
		}
		total += value
	}
	if total <= 0 {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "概率分布总和必须大于 0")
	}
	normalized := make([]float64, len(values))
	for index, value := range values {
		normalized[index] = value / total
	}
	return normalized, nil
}

func kullbackLeibler(left []float64, right []float64) float64 {
	total := 0.0
	for index, probability := range left {
		if probability == 0 {
			continue
		}
		total += probability * math.Log2(probability/right[index])
	}
	return total
}

func categoricalLogLikelihood(counts []float64, probabilities []float64) float64 {
	total := 0.0
	for index, count := range counts {
		if count > 0 {
			probability := probabilities[index]
			if probability == 0 {
				probability = math.SmallestNonzeroFloat64
			}
			total += count * math.Log(probability)
		}
	}
	return total
}

func unitInterval(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}
