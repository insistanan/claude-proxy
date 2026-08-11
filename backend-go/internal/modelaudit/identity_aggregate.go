package modelaudit

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

type IdentitySignalRule struct {
	SignalID       string              `json:"signalId"`
	MinimumSamples int                 `json:"minimumSamples"`
	FormalEligible bool                `json:"formalEligible"`
	Strategy       VersionedRef        `json:"strategy"`
	Baseline       BaselineRequirement `json:"baseline"`
}

func (r IdentitySignalRule) validate() error {
	if !stableIDPattern.MatchString(r.SignalID) || r.MinimumSamples <= 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份信号聚合规则无效")
	}
	if err := r.Strategy.Validate("身份信号策略"); err != nil {
		return err
	}
	if r.Baseline.Required {
		if !stableIDPattern.MatchString(r.Baseline.BaselineID) || !semanticVersionPattern.MatchString(r.Baseline.BaselineVersion) || r.Baseline.MinimumSamples <= 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份信号基线要求无效")
		}
	} else if r.Baseline.BaselineID != "" || r.Baseline.BaselineVersion != "" || r.Baseline.MinimumSamples != 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "非必需基线规则不能携带基线要求")
	}
	return nil
}

type IdentityAggregationConfig struct {
	Aggregator                         VersionedRef         `json:"aggregator"`
	DeclaredCandidate                  string               `json:"declaredCandidate"`
	MinimumSamples                     int                  `json:"minimumSamples"`
	MinimumApplicableSignals           int                  `json:"minimumApplicableSignals"`
	MinimumFormalSignals               int                  `json:"minimumFormalSignals"`
	MinimumCorrelationGroups           int                  `json:"minimumCorrelationGroups"`
	MatchedThreshold                   float64              `json:"matchedThreshold"`
	SubstitutionThreshold              float64              `json:"substitutionThreshold"`
	MixtureComponentLowerBound         float64              `json:"mixtureComponentLowerBound"`
	RequireCompatibleBaselineForFormal bool                 `json:"requireCompatibleBaselineForFormal"`
	RequireCandidateFitForMatch        bool                 `json:"requireCandidateFitForMatch"`
	RequireMixtureEstimateForMatch     bool                 `json:"requireMixtureEstimateForMatch"`
	RequireTemporalAssessmentForMatch  bool                 `json:"requireTemporalAssessmentForMatch"`
	GroupWeights                       map[string]float64   `json:"groupWeights"`
	SignalRules                        []IdentitySignalRule `json:"signalRules"`
}

func (c IdentityAggregationConfig) validate() error {
	if err := c.Aggregator.Validate("身份聚合器"); err != nil {
		return err
	}
	if strings.TrimSpace(c.DeclaredCandidate) == "" || c.MinimumSamples <= 0 || c.MinimumApplicableSignals <= 0 ||
		c.MinimumFormalSignals <= 0 || c.MinimumCorrelationGroups <= 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份聚合最低要求无效")
	}
	if !unitInterval(c.MatchedThreshold) || !unitInterval(c.SubstitutionThreshold) || c.SubstitutionThreshold >= c.MatchedThreshold ||
		!unitInterval(c.MixtureComponentLowerBound) || c.MixtureComponentLowerBound <= 0 || c.MixtureComponentLowerBound > 0.5 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份聚合阈值无效")
	}
	if len(c.GroupWeights) == 0 || len(c.SignalRules) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份聚合缺少相关组权重或信号规则")
	}
	for group, weight := range c.GroupWeights {
		if strings.TrimSpace(group) == "" || !finiteNumber(weight) || weight <= 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份相关组权重无效")
		}
	}
	seen := make(map[string]struct{}, len(c.SignalRules))
	requiresBaseline := false
	for _, rule := range c.SignalRules {
		if err := rule.validate(); err != nil {
			return err
		}
		if _, duplicate := seen[rule.SignalID]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("身份信号规则 %q 重复", rule.SignalID))
		}
		seen[rule.SignalID] = struct{}{}
		requiresBaseline = requiresBaseline || rule.Baseline.Required
	}
	if c.RequireCompatibleBaselineForFormal && !requiresBaseline {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "正式结论要求兼容基线，但没有信号声明基线要求")
	}
	return nil
}

type IdentityAggregationInput struct {
	DeclaredModel  string                   `json:"declaredModel"`
	RequestedModel string                   `json:"requestedModel,omitempty"`
	ReturnedModels []string                 `json:"returnedModels,omitempty"`
	Protocol       Protocol                 `json:"protocol"`
	Thinking       ThinkingLevel            `json:"thinking,omitempty"`
	SampleCount    int                      `json:"sampleCount"`
	Signals        []IdentitySignal         `json:"signals"`
	CandidateFit   *CandidateClassification `json:"candidateFit,omitempty"`
	Mixture        *MixtureEstimate         `json:"mixture,omitempty"`
	Temporal       *TemporalAssessment      `json:"temporal,omitempty"`
	Baselines      []BaselineSnapshot       `json:"baselines,omitempty"`
}

type IdentityGroupScore struct {
	Group       string   `json:"group"`
	SignalIDs   []string `json:"signalIds"`
	Score       float64  `json:"score"`
	Reliability float64  `json:"reliability"`
	Weight      float64  `json:"weight"`
}

func (g IdentityGroupScore) Validate() error {
	if strings.TrimSpace(g.Group) == "" || len(g.SignalIDs) == 0 || !unitInterval(g.Score) ||
		!unitInterval(g.Reliability) || !unitInterval(g.Weight) || g.Weight <= 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份相关组分数无效")
	}
	for _, signalID := range g.SignalIDs {
		if !stableIDPattern.MatchString(signalID) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份相关组包含无效信号 ID")
		}
	}
	return nil
}

type BaselineCompatibility struct {
	SignalID   string       `json:"signalId"`
	Required   bool         `json:"required"`
	Baseline   *BaselineRef `json:"baseline,omitempty"`
	Compatible bool         `json:"compatible"`
	Reasons    []string     `json:"reasons,omitempty"`
}

func (c BaselineCompatibility) Validate() error {
	if !stableIDPattern.MatchString(c.SignalID) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "基线兼容检查缺少有效信号 ID")
	}
	if c.Baseline != nil {
		if err := c.Baseline.Validate(); err != nil {
			return err
		}
	}
	if c.Compatible && (c.Baseline == nil || len(c.Reasons) > 0) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "兼容基线检查结果自相矛盾")
	}
	if !c.Compatible && len(c.Reasons) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "不兼容基线检查必须说明原因")
	}
	return nil
}

type FormalQualification struct {
	Eligible            bool     `json:"eligible"`
	EligibleSignals     int      `json:"eligibleSignals"`
	CorrelationGroups   int      `json:"correlationGroups"`
	CompatibleBaselines int      `json:"compatibleBaselines"`
	Reasons             []string `json:"reasons,omitempty"`
}

func (q FormalQualification) Validate() error {
	if q.EligibleSignals < 0 || q.CorrelationGroups < 0 || q.CompatibleBaselines < 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "正式结论资格计数无效")
	}
	if q.Eligible && len(q.Reasons) > 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "已满足正式资格时不能保留不满足原因")
	}
	return nil
}

func AggregateIdentity(config IdentityAggregationConfig, input IdentityAggregationInput) (IdentityReport, error) {
	if err := config.validate(); err != nil {
		return IdentityReport{}, err
	}
	input.DeclaredModel = strings.TrimSpace(input.DeclaredModel)
	if input.DeclaredModel == "" || !input.Protocol.Valid() || !input.Thinking.Valid() || input.SampleCount < 0 {
		return IdentityReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份聚合输入目标或样本计数无效")
	}
	if input.CandidateFit != nil {
		if err := validateCandidateClassification(*input.CandidateFit); err != nil {
			return IdentityReport{}, err
		}
	}
	if input.Mixture != nil {
		if err := validateMixtureEstimate(*input.Mixture); err != nil {
			return IdentityReport{}, err
		}
	}
	if input.Temporal != nil {
		if err := input.Temporal.Validate(); err != nil {
			return IdentityReport{}, err
		}
	}
	baselineByRef := make(map[string]BaselineSnapshot, len(input.Baselines))
	for _, baseline := range input.Baselines {
		if err := baseline.Validate(); err != nil {
			return IdentityReport{}, err
		}
		key := baselineRefKey(baseline.Ref)
		if _, duplicate := baselineByRef[key]; duplicate {
			return IdentityReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份聚合包含重复基线快照")
		}
		baselineByRef[key] = baseline
	}
	rules := make(map[string]IdentitySignalRule, len(config.SignalRules))
	for _, rule := range config.SignalRules {
		rules[rule.SignalID] = rule
	}

	type groupAccumulator struct {
		signalIDs       []string
		weightedScore   float64
		reliabilitySum  float64
		reliabilityPeak float64
	}
	groups := make(map[string]*groupAccumulator)
	formalSignals := 0
	compatibleBaselines := 0
	requiredBaselineSignals := 0
	baselineChecks := make([]BaselineCompatibility, 0)
	alternatives := make([]string, 0)
	seenSignals := make(map[string]struct{}, len(input.Signals))
	signals := make([]IdentitySignal, len(input.Signals))
	for index, signal := range input.Signals {
		if err := signal.Validate(); err != nil {
			return IdentityReport{}, err
		}
		if _, duplicate := seenSignals[signal.ID]; duplicate {
			return IdentityReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("身份信号 %q 重复", signal.ID))
		}
		seenSignals[signal.ID] = struct{}{}
		rule, ok := rules[signal.ID]
		if !ok {
			return IdentityReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("身份信号 %q 缺少聚合规则", signal.ID))
		}
		signals[index] = cloneIdentitySignal(signal)
		if signal.Status != SignalObserved {
			alternatives = append(alternatives, fmt.Sprintf("信号 %s 未参与聚合：%s", signal.ID, signal.Reason))
			continue
		}
		if signal.SampleCount < rule.MinimumSamples {
			alternatives = append(alternatives, fmt.Sprintf("信号 %s 样本数 %d 低于最低要求 %d", signal.ID, signal.SampleCount, rule.MinimumSamples))
			continue
		}
		if signal.Reliability <= 0 {
			alternatives = append(alternatives, fmt.Sprintf("信号 %s 可靠度为 0", signal.ID))
			continue
		}
		groupWeight, configured := config.GroupWeights[signal.CorrelationGroup]
		if !configured || groupWeight <= 0 {
			return IdentityReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("相关组 %q 缺少有效权重", signal.CorrelationGroup))
		}
		group := groups[signal.CorrelationGroup]
		if group == nil {
			group = &groupAccumulator{}
			groups[signal.CorrelationGroup] = group
		}
		group.signalIDs = append(group.signalIDs, signal.ID)
		group.weightedScore += *signal.Score * signal.Reliability
		group.reliabilitySum += signal.Reliability
		group.reliabilityPeak = math.Max(group.reliabilityPeak, signal.Reliability)

		baselineCompatible := !rule.Baseline.Required
		if rule.Baseline.Required {
			requiredBaselineSignals++
			check := checkSignalBaseline(rule, signal, input, baselineByRef)
			baselineChecks = append(baselineChecks, check)
			baselineCompatible = check.Compatible
			if check.Compatible {
				compatibleBaselines++
			} else {
				for _, reason := range check.Reasons {
					alternatives = append(alternatives, fmt.Sprintf("信号 %s 基线不兼容：%s", signal.ID, reason))
				}
			}
		}
		if rule.FormalEligible && baselineCompatible {
			formalSignals++
		}
	}

	groupScores := make([]IdentityGroupScore, 0, len(groups))
	totalGroupWeight := 0.0
	for group := range groups {
		totalGroupWeight += config.GroupWeights[group]
	}
	for groupName, accumulator := range groups {
		sort.Strings(accumulator.signalIDs)
		groupScores = append(groupScores, IdentityGroupScore{
			Group: groupName, SignalIDs: append([]string(nil), accumulator.signalIDs...),
			Score:       accumulator.weightedScore / accumulator.reliabilitySum,
			Reliability: accumulator.reliabilityPeak,
			Weight:      config.GroupWeights[groupName] / totalGroupWeight,
		})
	}
	sort.Slice(groupScores, func(i, j int) bool { return groupScores[i].Group < groupScores[j].Group })
	consistencyScore := 0.0
	applicableSignals := 0
	for _, group := range groupScores {
		consistencyScore += group.Score * group.Weight
		applicableSignals += len(group.SignalIDs)
	}

	qualificationReasons := make([]string, 0)
	if input.SampleCount < config.MinimumSamples {
		qualificationReasons = append(qualificationReasons, fmt.Sprintf("运行样本数 %d 低于最低要求 %d", input.SampleCount, config.MinimumSamples))
	}
	if applicableSignals < config.MinimumApplicableSignals {
		qualificationReasons = append(qualificationReasons, fmt.Sprintf("适用信号数 %d 低于最低要求 %d", applicableSignals, config.MinimumApplicableSignals))
	}
	if len(groupScores) < config.MinimumCorrelationGroups {
		qualificationReasons = append(qualificationReasons, fmt.Sprintf("独立相关组数 %d 低于最低要求 %d", len(groupScores), config.MinimumCorrelationGroups))
	}
	if formalSignals < config.MinimumFormalSignals {
		qualificationReasons = append(qualificationReasons, fmt.Sprintf("正式资格信号数 %d 低于最低要求 %d", formalSignals, config.MinimumFormalSignals))
	}
	if config.RequireCompatibleBaselineForFormal && requiredBaselineSignals > 0 && compatibleBaselines == 0 {
		qualificationReasons = append(qualificationReasons, "没有适用于正式结论的兼容基线")
	}

	conclusion := classifyIdentity(config, input, consistencyScore)
	if conclusion == IdentityMatched {
		if config.RequireCandidateFitForMatch && !candidateMatchesDeclared(input.CandidateFit, config.DeclaredCandidate) {
			qualificationReasons = append(qualificationReasons, "缺少支持声明候选的有效候选拟合")
		}
		if config.RequireMixtureEstimateForMatch && (input.Mixture == nil || !input.Mixture.Converged) {
			qualificationReasons = append(qualificationReasons, "缺少收敛的混合比例估计")
		}
		if config.RequireTemporalAssessmentForMatch && (input.Temporal == nil || input.Temporal.Pattern == TemporalInsufficientEvidence) {
			qualificationReasons = append(qualificationReasons, "缺少充分的时序判定")
		}
		if input.Temporal != nil && input.Temporal.Pattern == TemporalShortAnomaly {
			qualificationReasons = append(qualificationReasons, "存在短期异常窗口，不能生成正式未混用结论")
		}
	}
	qualificationReasons = uniqueSortedStrings(qualificationReasons)
	qualification := FormalQualification{
		Eligible: len(qualificationReasons) == 0, EligibleSignals: formalSignals,
		CorrelationGroups: len(groupScores), CompatibleBaselines: compatibleBaselines, Reasons: qualificationReasons,
	}
	formal := qualification.Eligible && conclusion != IdentityInsufficientEvidence && conclusion != IdentityUnsupported
	if conclusion == IdentityMatched && !qualification.Eligible {
		conclusion = IdentityInsufficientEvidence
		formal = false
	}
	if input.SampleCount < config.MinimumSamples || applicableSignals < config.MinimumApplicableSignals || len(groupScores) < config.MinimumCorrelationGroups {
		conclusion = IdentityInsufficientEvidence
		formal = false
	}

	if input.Temporal != nil && input.Temporal.Pattern == TemporalShortAnomaly {
		alternatives = append(alternatives, "存在短期异常窗口，可能是瞬时渠道波动而非持续替换")
	}
	if input.CandidateFit != nil && input.CandidateFit.OOD {
		alternatives = append(alternatives, "观测分布不符合任何已知候选基线")
	}
	alternatives = uniqueSortedStrings(append(alternatives, qualificationReasons...))
	report := IdentityReport{
		Aggregator: config.Aggregator, DeclaredModel: input.DeclaredModel, RequestedModel: strings.TrimSpace(input.RequestedModel),
		ReturnedModels: uniqueSortedStrings(input.ReturnedModels), Conclusion: conclusion, Formal: formal,
		Confidence: conclusionConfidence(conclusion, consistencyScore, input.Mixture, input.Temporal), ConsistencyScore: consistencyScore,
		SampleCount: input.SampleCount, ApplicableSignals: applicableSignals, CorrelationGroups: len(groupScores),
		Signals: signals, GroupScores: groupScores, Qualification: qualification, BaselineChecks: baselineChecks,
		AlternativeExplanations: alternatives,
	}
	if input.CandidateFit != nil {
		report.CandidateFit = cloneCandidateClassification(*input.CandidateFit)
	}
	if input.Mixture != nil {
		mixture := cloneMixtureEstimate(*input.Mixture)
		report.Mixture = &mixture
	}
	if input.Temporal != nil {
		temporal := cloneTemporalAssessment(*input.Temporal)
		report.Temporal = &temporal
	}
	if err := report.Validate(); err != nil {
		return IdentityReport{}, err
	}
	return report, nil
}

func classifyIdentity(config IdentityAggregationConfig, input IdentityAggregationInput, consistencyScore float64) IdentityConclusion {
	if input.CandidateFit != nil && input.CandidateFit.OOD {
		return IdentityUnknown
	}
	if mixtureHasMultipleComponents(input.Mixture, config.MixtureComponentLowerBound) {
		return IdentitySuspectedMixture
	}
	if input.Temporal != nil {
		switch input.Temporal.Pattern {
		case TemporalStageSwitch, TemporalIntermittent:
			return IdentitySuspectedMixture
		case TemporalStableShifted:
			return IdentitySuspectedSubstitution
		}
	}
	if input.CandidateFit != nil && input.CandidateFit.BestCandidate != "" &&
		normalizeModelName(input.CandidateFit.BestCandidate) != normalizeModelName(config.DeclaredCandidate) {
		return IdentitySuspectedSubstitution
	}
	if consistencyScore >= config.MatchedThreshold {
		return IdentityMatched
	}
	if consistencyScore <= config.SubstitutionThreshold {
		return IdentitySuspectedSubstitution
	}
	return IdentityUnknown
}

func checkSignalBaseline(rule IdentitySignalRule, signal IdentitySignal, input IdentityAggregationInput, baselines map[string]BaselineSnapshot) BaselineCompatibility {
	check := BaselineCompatibility{SignalID: signal.ID, Required: true}
	if signal.Baseline == nil {
		check.Reasons = []string{"信号未冻结基线引用"}
		return check
	}
	reference := *signal.Baseline
	check.Baseline = &reference
	baseline, found := baselines[baselineRefKey(reference)]
	if !found {
		check.Reasons = []string{"未提供信号引用的基线快照"}
		return check
	}
	reasons := make([]string, 0)
	if baseline.Ref.ID != rule.Baseline.BaselineID || baseline.Ref.Version != rule.Baseline.BaselineVersion {
		reasons = append(reasons, "基线 ID 或版本不满足策略要求")
	}
	if baseline.SampleSize < rule.Baseline.MinimumSamples {
		reasons = append(reasons, fmt.Sprintf("基线样本数 %d 低于最低要求 %d", baseline.SampleSize, rule.Baseline.MinimumSamples))
	}
	if !containsFold(baseline.TargetModels, input.DeclaredModel) {
		reasons = append(reasons, "基线不适用于声明模型")
	}
	if !containsProtocol(baseline.Protocols, input.Protocol) {
		reasons = append(reasons, "基线不适用于请求协议")
	}
	if len(baseline.ThinkingLevels) > 0 && !containsThinkingLevel(baseline.ThinkingLevels, input.Thinking) {
		reasons = append(reasons, "基线不适用于思考档位")
	}
	if !containsVersionedRef(baseline.CompatibleStrategies, rule.Strategy) {
		reasons = append(reasons, "基线不兼容信号策略版本")
	}
	check.Reasons = uniqueSortedStrings(reasons)
	check.Compatible = len(check.Reasons) == 0
	return check
}

func validateCandidateClassification(classification CandidateClassification) error {
	if len(classification.Fits) == 0 {
		if classification.BestCandidate != "" || classification.OOD {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "空候选拟合不能携带分类结论")
		}
		return nil
	}
	seen := make(map[string]struct{}, len(classification.Fits))
	bestPresent := false
	for _, fit := range classification.Fits {
		if strings.TrimSpace(fit.Candidate) == "" || !unitInterval(fit.JSD) || !finiteNumber(fit.LogLikelihood) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "候选拟合值无效")
		}
		if _, duplicate := seen[fit.Candidate]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "候选拟合包含重复候选")
		}
		seen[fit.Candidate] = struct{}{}
		bestPresent = bestPresent || fit.Candidate == classification.BestCandidate
	}
	if classification.OOD {
		if classification.BestCandidate != "" {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "OOD 候选拟合不能指定最佳候选")
		}
	} else if classification.BestCandidate == "" || !bestPresent {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "非 OOD 候选拟合缺少有效最佳候选")
	}
	return nil
}

func validateMixtureEstimate(estimate MixtureEstimate) error {
	if len(estimate.Components) < 2 || estimate.Iterations <= 0 || !finiteNumber(estimate.LogLikelihood) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "混合估计合同无效")
	}
	total := 0.0
	seen := make(map[string]struct{}, len(estimate.Components))
	for _, component := range estimate.Components {
		if strings.TrimSpace(component.Candidate) == "" || !unitInterval(component.Proportion) || !unitInterval(component.Lower) ||
			!unitInterval(component.Upper) || component.Lower > component.Proportion || component.Proportion > component.Upper {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "混合估计分量无效")
		}
		if _, duplicate := seen[component.Candidate]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "混合估计包含重复候选")
		}
		seen[component.Candidate] = struct{}{}
		total += component.Proportion
	}
	if math.Abs(total-1) > 1e-6 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "混合估计分量比例之和必须为 1")
	}
	return nil
}

func mixtureHasMultipleComponents(estimate *MixtureEstimate, lowerBound float64) bool {
	if estimate == nil || !estimate.Converged {
		return false
	}
	components := 0
	for _, component := range estimate.Components {
		if component.Lower >= lowerBound {
			components++
		}
	}
	return components >= 2
}

func candidateMatchesDeclared(classification *CandidateClassification, declared string) bool {
	return classification != nil && !classification.OOD && normalizeModelName(classification.BestCandidate) == normalizeModelName(declared)
}

func conclusionConfidence(conclusion IdentityConclusion, consistency float64, mixture *MixtureEstimate, temporal *TemporalAssessment) float64 {
	switch conclusion {
	case IdentityMatched:
		return consistency
	case IdentitySuspectedSubstitution:
		return 1 - consistency
	case IdentitySuspectedMixture:
		confidence := 1 - math.Abs(consistency-0.5)*2
		if mixture != nil {
			lowers := make([]float64, len(mixture.Components))
			for index, component := range mixture.Components {
				lowers[index] = component.Lower
			}
			sort.Sort(sort.Reverse(sort.Float64Slice(lowers)))
			if len(lowers) > 1 {
				confidence = math.Max(confidence, clampUnit(lowers[1]*2))
			}
		}
		if temporal != nil && temporal.ChangePoint != nil {
			confidence = math.Max(confidence, math.Abs(temporal.ChangePoint.Delta))
		}
		return clampUnit(confidence)
	case IdentityUnknown:
		return clampUnit(1 - math.Abs(consistency-0.5)*2)
	default:
		return 0
	}
}

func baselineRefKey(reference BaselineRef) string {
	return reference.ID + "\x00" + reference.Version + "\x00" + reference.SHA256
}

func containsFold(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(expected)) {
			return true
		}
	}
	return false
}

func containsProtocol(values []Protocol, expected Protocol) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsThinkingLevel(values []ThinkingLevel, expected ThinkingLevel) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsVersionedRef(values []VersionedRef, expected VersionedRef) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func cloneIdentitySignal(signal IdentitySignal) IdentitySignal {
	signal.CandidateScores = cloneFloatMap(signal.CandidateScores)
	signal.Features = append([]byte(nil), signal.Features...)
	if signal.Baseline != nil {
		baseline := *signal.Baseline
		signal.Baseline = &baseline
	}
	signal.Evidence = append([]EvidenceReference(nil), signal.Evidence...)
	return signal
}

func cloneFloatMap(values map[string]float64) map[string]float64 {
	if values == nil {
		return nil
	}
	cloned := make(map[string]float64, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneCandidateClassification(classification CandidateClassification) CandidateClassification {
	classification.Fits = append([]CandidateFit(nil), classification.Fits...)
	return classification
}

func cloneMixtureEstimate(estimate MixtureEstimate) MixtureEstimate {
	estimate.Components = append([]MixtureComponent(nil), estimate.Components...)
	return estimate
}

func cloneTemporalAssessment(assessment TemporalAssessment) TemporalAssessment {
	assessment.Windows = append([]TemporalWindow(nil), assessment.Windows...)
	for index := range assessment.Windows {
		assessment.Windows[index].SampleIDs = append([]string(nil), assessment.Windows[index].SampleIDs...)
		assessment.Windows[index].Evidence = append([]EvidenceReference(nil), assessment.Windows[index].Evidence...)
	}
	if assessment.ChangePoint != nil {
		changePoint := *assessment.ChangePoint
		assessment.ChangePoint = &changePoint
	}
	assessment.AnomalyWindows = append([]int(nil), assessment.AnomalyWindows...)
	assessment.Evidence = append([]EvidenceReference(nil), assessment.Evidence...)
	return assessment
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
