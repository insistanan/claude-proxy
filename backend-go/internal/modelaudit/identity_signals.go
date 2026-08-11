package modelaudit

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// IdentitySignalSpec freezes the shared contract for one signal extractor.
// Observed scores use one direction: 1 means fully consistent with the
// declared identity's expected behavior, while 0 means fully inconsistent.
type IdentitySignalSpec struct {
	ID               string  `json:"id"`
	CorrelationGroup string  `json:"correlationGroup"`
	MinimumSamples   int     `json:"minimumSamples"`
	Reliability      float64 `json:"reliability"`
}

func (s IdentitySignalSpec) validate() error {
	if !stableIDPattern.MatchString(s.ID) || strings.TrimSpace(s.CorrelationGroup) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份信号规格缺少有效 ID 或相关组")
	}
	if s.MinimumSamples <= 0 || !unitInterval(s.Reliability) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份信号最低样本数或可靠度无效")
	}
	return nil
}

type JuiceBand struct {
	Profile string  `json:"profile"`
	Minimum float64 `json:"minimum"`
	Maximum float64 `json:"maximum"`
	Rank    int     `json:"rank"`
}

type JuiceSignalConfig struct {
	Signal IdentitySignalSpec `json:"signal"`
	Bands  []JuiceBand        `json:"bands"`
}

type JuiceObservation struct {
	Profile  string              `json:"profile"`
	Value    float64             `json:"value"`
	Evidence []EvidenceReference `json:"evidence,omitempty"`
}

type juiceProfileFeature struct {
	Profile        string  `json:"profile"`
	Rank           int     `json:"rank"`
	Count          int     `json:"count"`
	Minimum        float64 `json:"minimum"`
	Maximum        float64 `json:"maximum"`
	Mean           float64 `json:"mean"`
	InRangeRate    float64 `json:"inRangeRate"`
	StabilityScore float64 `json:"stabilityScore"`
}

func ExtractJuiceSignal(config JuiceSignalConfig, observations []JuiceObservation) (IdentitySignal, error) {
	if err := config.Signal.validate(); err != nil {
		return IdentitySignal{}, err
	}
	if len(config.Bands) == 0 {
		return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Juice 信号至少需要一个档位范围")
	}
	bands := make(map[string]JuiceBand, len(config.Bands))
	for _, band := range config.Bands {
		band.Profile = strings.TrimSpace(band.Profile)
		if band.Profile == "" || band.Rank < 0 || !finiteNumber(band.Minimum) || !finiteNumber(band.Maximum) || band.Minimum > band.Maximum {
			return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Juice 档位范围无效")
		}
		if _, duplicate := bands[band.Profile]; duplicate {
			return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("Juice 档位 %q 重复", band.Profile))
		}
		bands[band.Profile] = band
	}

	values := make(map[string][]float64, len(bands))
	var evidence []EvidenceReference
	for index, observation := range observations {
		profile := strings.TrimSpace(observation.Profile)
		if _, ok := bands[profile]; !ok || !finiteNumber(observation.Value) {
			return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("第 %d 个 Juice 观察值无效", index+1))
		}
		values[profile] = append(values[profile], observation.Value)
		var err error
		evidence, err = mergeSignalEvidence(evidence, observation.Evidence)
		if err != nil {
			return IdentitySignal{}, err
		}
	}
	if len(observations) < config.Signal.MinimumSamples {
		return insufficientSignal(config.Signal, SignalJuiceBehavior, len(observations), "Juice 观察样本不足", evidence), nil
	}

	profiles := make([]juiceProfileFeature, 0, len(values))
	means := make(map[string]float64, len(values))
	rangeTotal, stabilityTotal := 0.0, 0.0
	stabilityCount := 0
	for profile, samples := range values {
		band := bands[profile]
		sort.Float64s(samples)
		inRange := 0
		for _, value := range samples {
			if value >= band.Minimum && value <= band.Maximum {
				inRange++
			}
		}
		mean := meanNumbers(samples)
		means[profile] = mean
		stability := 1.0
		if len(samples) > 1 {
			width := band.Maximum - band.Minimum
			span := samples[len(samples)-1] - samples[0]
			if width == 0 {
				if span > 0 {
					stability = 0
				}
			} else {
				stability = clampUnit(1 - span/width)
			}
			stabilityTotal += stability
			stabilityCount++
		}
		rate := float64(inRange) / float64(len(samples))
		rangeTotal += rate * float64(len(samples))
		profiles = append(profiles, juiceProfileFeature{
			Profile: profile, Rank: band.Rank, Count: len(samples), Minimum: samples[0], Maximum: samples[len(samples)-1],
			Mean: mean, InRangeRate: rate, StabilityScore: stability,
		})
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].Rank == profiles[j].Rank {
			return profiles[i].Profile < profiles[j].Profile
		}
		return profiles[i].Rank < profiles[j].Rank
	})
	orderingComparisons, orderingPassed := 0, 0
	for i := 0; i < len(profiles); i++ {
		for j := i + 1; j < len(profiles); j++ {
			if profiles[i].Rank == profiles[j].Rank {
				continue
			}
			orderingComparisons++
			if means[profiles[j].Profile] > means[profiles[i].Profile] {
				orderingPassed++
			}
		}
	}
	components := []float64{rangeTotal / float64(len(observations))}
	if stabilityCount > 0 {
		components = append(components, stabilityTotal/float64(stabilityCount))
	}
	if orderingComparisons > 0 {
		components = append(components, float64(orderingPassed)/float64(orderingComparisons))
	}
	features := struct {
		Profiles            []juiceProfileFeature `json:"profiles"`
		RangeScore          float64               `json:"rangeScore"`
		StabilityScore      *float64              `json:"stabilityScore,omitempty"`
		OrderingComparisons int                   `json:"orderingComparisons"`
		OrderingPassed      int                   `json:"orderingPassed"`
	}{Profiles: profiles, RangeScore: components[0], OrderingComparisons: orderingComparisons, OrderingPassed: orderingPassed}
	if stabilityCount > 0 {
		value := stabilityTotal / float64(stabilityCount)
		features.StabilityScore = &value
	}
	return observedSignal(config.Signal, SignalJuiceBehavior, len(observations), meanNumbers(components), features, evidence)
}

type RewriteExpectation struct {
	InputValue int `json:"inputValue"`
	OutputMin  int `json:"outputMin"`
	OutputMax  int `json:"outputMax"`
}

type RewriteSignalConfig struct {
	Signal       IdentitySignalSpec   `json:"signal"`
	Expectations []RewriteExpectation `json:"expectations"`
}

type RewriteObservation struct {
	InputValue  int                 `json:"inputValue"`
	OutputValue int                 `json:"outputValue"`
	Evidence    []EvidenceReference `json:"evidence,omitempty"`
}

func ExtractRewriteSignal(config RewriteSignalConfig, observations []RewriteObservation) (IdentitySignal, error) {
	if err := config.Signal.validate(); err != nil {
		return IdentitySignal{}, err
	}
	expectations := make(map[int]RewriteExpectation, len(config.Expectations))
	for _, expectation := range config.Expectations {
		if expectation.InputValue < 0 || expectation.OutputMin < 0 || expectation.OutputMin > expectation.OutputMax {
			return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "改写期望范围无效")
		}
		if _, duplicate := expectations[expectation.InputValue]; duplicate {
			return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "改写期望包含重复输入")
		}
		expectations[expectation.InputValue] = expectation
	}
	if len(expectations) == 0 {
		return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "改写信号至少需要一个期望")
	}

	matched := 0
	counts := make(map[int][2]int, len(expectations))
	var evidence []EvidenceReference
	for index, observation := range observations {
		expectation, ok := expectations[observation.InputValue]
		if !ok || observation.OutputValue < 0 {
			return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("第 %d 个改写观察值无效", index+1))
		}
		entry := counts[observation.InputValue]
		entry[0]++
		if observation.OutputValue >= expectation.OutputMin && observation.OutputValue <= expectation.OutputMax {
			matched++
			entry[1]++
		}
		counts[observation.InputValue] = entry
		var err error
		evidence, err = mergeSignalEvidence(evidence, observation.Evidence)
		if err != nil {
			return IdentitySignal{}, err
		}
	}
	if len(observations) < config.Signal.MinimumSamples {
		return insufficientSignal(config.Signal, SignalRewriteBehavior, len(observations), "改写行为样本不足", evidence), nil
	}
	type rewriteFeature struct {
		InputValue int     `json:"inputValue"`
		OutputMin  int     `json:"outputMin"`
		OutputMax  int     `json:"outputMax"`
		Count      int     `json:"count"`
		MatchRate  float64 `json:"matchRate"`
	}
	items := make([]rewriteFeature, 0, len(counts))
	for input, count := range counts {
		expectation := expectations[input]
		items = append(items, rewriteFeature{InputValue: input, OutputMin: expectation.OutputMin, OutputMax: expectation.OutputMax, Count: count[0], MatchRate: float64(count[1]) / float64(count[0])})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].InputValue < items[j].InputValue })
	features := struct {
		Expectations []rewriteFeature `json:"expectations"`
		Matched      int              `json:"matched"`
	}{Expectations: items, Matched: matched}
	return observedSignal(config.Signal, SignalRewriteBehavior, len(observations), float64(matched)/float64(len(observations)), features, evidence)
}

type SyntheticCoverageConfig struct {
	Signal                  IdentitySignalSpec `json:"signal"`
	Targets                 []float64          `json:"targets"`
	Tolerance               float64            `json:"tolerance"`
	MinimumDistinctControls int                `json:"minimumDistinctControls"`
}

type SyntheticCoverageObservation struct {
	ControlID string              `json:"controlId"`
	Value     float64             `json:"value"`
	Evidence  []EvidenceReference `json:"evidence,omitempty"`
}

func ExtractSyntheticCoverageSignal(config SyntheticCoverageConfig, observations []SyntheticCoverageObservation) (IdentitySignal, error) {
	if err := config.Signal.validate(); err != nil {
		return IdentitySignal{}, err
	}
	if len(config.Targets) == 0 || !finiteNumber(config.Tolerance) || config.Tolerance < 0 || config.MinimumDistinctControls <= 0 {
		return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "合成覆盖配置无效")
	}
	for _, target := range config.Targets {
		if !finiteNumber(target) {
			return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "合成覆盖目标值无效")
		}
	}
	controls := make(map[string]struct{}, len(observations))
	coverage := make([]int, len(config.Targets))
	var evidence []EvidenceReference
	for index, observation := range observations {
		controlID := strings.TrimSpace(observation.ControlID)
		if controlID == "" || !finiteNumber(observation.Value) {
			return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("第 %d 个合成覆盖观察值无效", index+1))
		}
		controls[controlID] = struct{}{}
		for targetIndex, target := range config.Targets {
			if math.Abs(observation.Value-target) <= config.Tolerance {
				coverage[targetIndex]++
			}
		}
		var err error
		evidence, err = mergeSignalEvidence(evidence, observation.Evidence)
		if err != nil {
			return IdentitySignal{}, err
		}
	}
	if len(observations) < config.Signal.MinimumSamples || len(controls) < config.MinimumDistinctControls {
		return insufficientSignal(config.Signal, SignalSyntheticCoverage, len(observations), "合成覆盖的样本或独立控制条件不足", evidence), nil
	}
	type targetFeature struct {
		Target   float64 `json:"target"`
		Count    int     `json:"count"`
		Coverage float64 `json:"coverage"`
	}
	items := make([]targetFeature, len(config.Targets))
	maximumCoverage := 0.0
	for index, target := range config.Targets {
		rate := float64(coverage[index]) / float64(len(observations))
		items[index] = targetFeature{Target: target, Count: coverage[index], Coverage: rate}
		maximumCoverage = math.Max(maximumCoverage, rate)
	}
	features := struct {
		Targets          []targetFeature `json:"targets"`
		DistinctControls int             `json:"distinctControls"`
		MaximumCoverage  float64         `json:"maximumCoverage"`
	}{Targets: items, DistinctControls: len(controls), MaximumCoverage: maximumCoverage}
	return observedSignal(config.Signal, SignalSyntheticCoverage, len(observations), 1-maximumCoverage, features, evidence)
}

type ProbeMode string

const (
	ProbeModeNormal          ProbeMode = "normal"
	ProbeModeCodexCompatible ProbeMode = "codex_compatible"
	ProbeModeNativeCodex     ProbeMode = "native_codex"
)

func (m ProbeMode) Valid() bool {
	return m == ProbeModeNormal || m == ProbeModeCodexCompatible || m == ProbeModeNativeCodex
}

type ModeExpectation struct {
	FromMode         ProbeMode `json:"fromMode"`
	ToMode           ProbeMode `json:"toMode"`
	FromContextClass string    `json:"fromContextClass"`
	ToContextClass   string    `json:"toContextClass"`
	MinimumDelta     float64   `json:"minimumDelta"`
	MaximumDelta     float64   `json:"maximumDelta"`
}

type ModeDifferenceConfig struct {
	Signal             IdentitySignalSpec `json:"signal"`
	Expectations       []ModeExpectation  `json:"expectations"`
	MinimumComparisons int                `json:"minimumComparisons"`
}

type ModeObservation struct {
	ProbeID      string              `json:"probeId"`
	Mode         ProbeMode           `json:"mode"`
	ContextClass string              `json:"contextClass"`
	Value        float64             `json:"value"`
	Evidence     []EvidenceReference `json:"evidence,omitempty"`
}

func ExtractModeDifferenceSignal(config ModeDifferenceConfig, observations []ModeObservation) (IdentitySignal, error) {
	if err := config.Signal.validate(); err != nil {
		return IdentitySignal{}, err
	}
	if len(config.Expectations) == 0 || config.MinimumComparisons <= 0 {
		return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "模式差分配置无效")
	}
	expectationKeys := make(map[string]struct{}, len(config.Expectations))
	for _, expectation := range config.Expectations {
		fromContext := strings.TrimSpace(expectation.FromContextClass)
		toContext := strings.TrimSpace(expectation.ToContextClass)
		if !expectation.FromMode.Valid() || !expectation.ToMode.Valid() || fromContext == "" || toContext == "" ||
			(expectation.FromMode == expectation.ToMode && fromContext == toContext) || !finiteNumber(expectation.MinimumDelta) ||
			!finiteNumber(expectation.MaximumDelta) || expectation.MinimumDelta > expectation.MaximumDelta {
			return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "模式差分期望无效")
		}
		key := string(expectation.FromMode) + "\x00" + fromContext + "\x00" + string(expectation.ToMode) + "\x00" + toContext
		if _, duplicate := expectationKeys[key]; duplicate {
			return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "模式差分期望重复")
		}
		expectationKeys[key] = struct{}{}
	}
	type modeKey struct {
		probeID string
		mode    ProbeMode
		context string
	}
	values := make(map[modeKey][]float64, len(observations))
	var evidence []EvidenceReference
	for index, observation := range observations {
		observation.ProbeID = strings.TrimSpace(observation.ProbeID)
		observation.ContextClass = strings.TrimSpace(observation.ContextClass)
		if observation.ProbeID == "" || !observation.Mode.Valid() || observation.ContextClass == "" || !finiteNumber(observation.Value) {
			return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("第 %d 个模式观察值无效", index+1))
		}
		key := modeKey{probeID: observation.ProbeID, mode: observation.Mode, context: observation.ContextClass}
		values[key] = append(values[key], observation.Value)
		var err error
		evidence, err = mergeSignalEvidence(evidence, observation.Evidence)
		if err != nil {
			return IdentitySignal{}, err
		}
	}
	type comparisonFeature struct {
		ProbeID          string    `json:"probeId"`
		FromMode         ProbeMode `json:"fromMode"`
		ToMode           ProbeMode `json:"toMode"`
		FromContextClass string    `json:"fromContextClass"`
		ToContextClass   string    `json:"toContextClass"`
		Delta            float64   `json:"delta"`
		MinimumDelta     float64   `json:"minimumDelta"`
		MaximumDelta     float64   `json:"maximumDelta"`
		Matched          bool      `json:"matched"`
	}
	probeIDs := make(map[string]struct{})
	for key := range values {
		probeIDs[key.probeID] = struct{}{}
	}
	comparisons := make([]comparisonFeature, 0)
	passed := 0
	for probeID := range probeIDs {
		for _, expectation := range config.Expectations {
			fromContext := strings.TrimSpace(expectation.FromContextClass)
			toContext := strings.TrimSpace(expectation.ToContextClass)
			from := values[modeKey{probeID: probeID, mode: expectation.FromMode, context: fromContext}]
			to := values[modeKey{probeID: probeID, mode: expectation.ToMode, context: toContext}]
			if len(from) == 0 || len(to) == 0 {
				continue
			}
			delta := meanNumbers(to) - meanNumbers(from)
			matched := delta >= expectation.MinimumDelta && delta <= expectation.MaximumDelta
			if matched {
				passed++
			}
			comparisons = append(comparisons, comparisonFeature{
				ProbeID: probeID, FromMode: expectation.FromMode, ToMode: expectation.ToMode,
				FromContextClass: fromContext, ToContextClass: toContext,
				Delta: delta, MinimumDelta: expectation.MinimumDelta, MaximumDelta: expectation.MaximumDelta, Matched: matched,
			})
		}
	}
	sort.Slice(comparisons, func(i, j int) bool {
		if comparisons[i].ProbeID == comparisons[j].ProbeID {
			if comparisons[i].FromContextClass == comparisons[j].FromContextClass {
				if comparisons[i].ToContextClass == comparisons[j].ToContextClass {
					if comparisons[i].FromMode == comparisons[j].FromMode {
						return comparisons[i].ToMode < comparisons[j].ToMode
					}
					return comparisons[i].FromMode < comparisons[j].FromMode
				}
				return comparisons[i].ToContextClass < comparisons[j].ToContextClass
			}
			return comparisons[i].FromContextClass < comparisons[j].FromContextClass
		}
		return comparisons[i].ProbeID < comparisons[j].ProbeID
	})
	if len(observations) < config.Signal.MinimumSamples || len(comparisons) < config.MinimumComparisons {
		return insufficientSignal(config.Signal, SignalModeDifference, len(observations), "模式差分的样本或配对比较不足", evidence), nil
	}
	features := struct {
		Comparisons []comparisonFeature `json:"comparisons"`
		Passed      int                 `json:"passed"`
	}{Comparisons: comparisons, Passed: passed}
	return observedSignal(config.Signal, SignalModeDifference, len(observations), float64(passed)/float64(len(comparisons)), features, evidence)
}

type MetadataConsistencyConfig struct {
	Signal                        IdentitySignalSpec `json:"signal"`
	AcceptedModels                []string           `json:"acceptedModels,omitempty"`
	RequireReturnedModel          bool               `json:"requireReturnedModel"`
	RequireUsage                  bool               `json:"requireUsage"`
	RequireReasoningWhenRequested bool               `json:"requireReasoningWhenRequested"`
	RequireStructureAssessment    bool               `json:"requireStructureAssessment"`
	RequireCapabilityAssessment   bool               `json:"requireCapabilityAssessment"`
}

type MetadataObservation struct {
	ObservationID           string              `json:"observationId"`
	RequestedModel          string              `json:"requestedModel,omitempty"`
	DeclaredModel           string              `json:"declaredModel,omitempty"`
	ReturnedModel           string              `json:"returnedModel,omitempty"`
	Protocol                Protocol            `json:"protocol"`
	WireProtocol            Protocol            `json:"wireProtocol"`
	Status                  ExecutionStatus     `json:"status"`
	Terminal                ProtocolTerminal    `json:"terminal,omitempty"`
	Usage                   Usage               `json:"usage"`
	ReasoningRequested      bool                `json:"reasoningRequested"`
	ResponseStructureValid  *bool               `json:"responseStructureValid,omitempty"`
	ProtocolCapabilityValid *bool               `json:"protocolCapabilityValid,omitempty"`
	Evidence                []EvidenceReference `json:"evidence,omitempty"`
}

func MetadataObservationFromExecution(result ExecutionResult, structureValid, capabilityValid *bool) MetadataObservation {
	return MetadataObservation{
		ObservationID: result.ExecutionID, RequestedModel: result.Target.RequestedModel, DeclaredModel: result.DeclaredModel,
		ReturnedModel: result.ReturnedModel, Protocol: result.Target.Protocol, WireProtocol: result.Target.WireProtocol,
		Status: result.Status, Terminal: result.ProtocolTerminal, Usage: result.Usage,
		ReasoningRequested:     result.Target.Thinking != ThinkingUnset && result.Target.Thinking != ThinkingOff,
		ResponseStructureValid: structureValid, ProtocolCapabilityValid: capabilityValid,
		Evidence: append([]EvidenceReference(nil), result.Evidence...),
	}
}

func ExtractMetadataConsistencySignal(config MetadataConsistencyConfig, observations []MetadataObservation) (IdentitySignal, error) {
	if err := config.Signal.validate(); err != nil {
		return IdentitySignal{}, err
	}
	accepted := make(map[string]struct{}, len(config.AcceptedModels))
	for _, model := range config.AcceptedModels {
		normalized := normalizeModelName(model)
		if normalized == "" {
			return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "元数据允许模型名不能为空")
		}
		accepted[normalized] = struct{}{}
	}
	type contradiction struct {
		ObservationID string `json:"observationId"`
		Check         string `json:"check"`
	}
	checkCounts := make(map[string][2]int)
	contradictions := make([]contradiction, 0)
	var evidence []EvidenceReference
	addCheck := func(observationID, name string, passed bool) {
		count := checkCounts[name]
		count[0]++
		if passed {
			count[1]++
		} else {
			contradictions = append(contradictions, contradiction{ObservationID: observationID, Check: name})
		}
		checkCounts[name] = count
	}
	for index, observation := range observations {
		observation.ObservationID = strings.TrimSpace(observation.ObservationID)
		if observation.ObservationID == "" || invalidUsage(observation.Usage) {
			return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("第 %d 个元数据观察值无效", index+1))
		}
		requested := normalizeModelName(observation.RequestedModel)
		declared := normalizeModelName(observation.DeclaredModel)
		returned := normalizeModelName(observation.ReturnedModel)
		if requested != "" && declared != "" {
			addCheck(observation.ObservationID, "requested_declared_model", requested == declared || modelAccepted(accepted, requested, declared))
		}
		if config.RequireReturnedModel || returned != "" {
			passed := returned != ""
			if passed && len(accepted) > 0 {
				_, passed = accepted[returned]
			} else if passed && declared != "" {
				passed = returned == declared
			} else if passed && requested != "" {
				passed = returned == requested
			}
			addCheck(observation.ObservationID, "returned_model", passed)
		}
		addCheck(observation.ObservationID, "protocol", observation.Protocol.Valid() && observation.WireProtocol.Valid())
		addCheck(observation.ObservationID, "terminal", metadataTerminalConsistent(observation.Status, observation.Terminal))
		usagePresent := observation.Usage.InputTokens > 0 || observation.Usage.OutputTokens > 0 || observation.Usage.TotalTokens > 0 ||
			observation.Usage.ReasoningTokens > 0 || observation.Usage.CachedTokens > 0
		if config.RequireUsage || usagePresent {
			usageConsistent := usagePresent && observation.Usage.CachedTokens <= observation.Usage.InputTokens
			if observation.Usage.TotalTokens > 0 {
				usageConsistent = usageConsistent && observation.Usage.TotalTokens >= observation.Usage.InputTokens+observation.Usage.OutputTokens
			}
			usageConsistent = usageConsistent && observation.Usage.ReasoningTokens <= observation.Usage.OutputTokens
			addCheck(observation.ObservationID, "usage", usageConsistent)
		}
		if config.RequireReasoningWhenRequested && observation.ReasoningRequested {
			addCheck(observation.ObservationID, "reasoning_tokens", observation.Usage.ReasoningTokens > 0)
		}
		if config.RequireStructureAssessment || observation.ResponseStructureValid != nil {
			addCheck(observation.ObservationID, "response_structure", observation.ResponseStructureValid != nil && *observation.ResponseStructureValid)
		}
		if config.RequireCapabilityAssessment || observation.ProtocolCapabilityValid != nil {
			addCheck(observation.ObservationID, "protocol_capability", observation.ProtocolCapabilityValid != nil && *observation.ProtocolCapabilityValid)
		}
		var err error
		evidence, err = mergeSignalEvidence(evidence, observation.Evidence)
		if err != nil {
			return IdentitySignal{}, err
		}
	}
	if len(observations) < config.Signal.MinimumSamples {
		return insufficientSignal(config.Signal, SignalMetadataConsistency, len(observations), "元数据一致性样本不足", evidence), nil
	}
	type checkFeature struct {
		Check  string  `json:"check"`
		Count  int     `json:"count"`
		Passed int     `json:"passed"`
		Rate   float64 `json:"rate"`
	}
	checks := make([]checkFeature, 0, len(checkCounts))
	total, passed := 0, 0
	for name, count := range checkCounts {
		checks = append(checks, checkFeature{Check: name, Count: count[0], Passed: count[1], Rate: float64(count[1]) / float64(count[0])})
		total += count[0]
		passed += count[1]
	}
	if total == 0 {
		return insufficientSignal(config.Signal, SignalMetadataConsistency, len(observations), "元数据观察未形成可判定检查项", evidence), nil
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].Check < checks[j].Check })
	sort.Slice(contradictions, func(i, j int) bool {
		if contradictions[i].ObservationID == contradictions[j].ObservationID {
			return contradictions[i].Check < contradictions[j].Check
		}
		return contradictions[i].ObservationID < contradictions[j].ObservationID
	})
	features := struct {
		Checks         []checkFeature  `json:"checks"`
		Contradictions []contradiction `json:"contradictions"`
	}{Checks: checks, Contradictions: contradictions}
	return observedSignal(config.Signal, SignalMetadataConsistency, len(observations), float64(passed)/float64(total), features, evidence)
}

type VariationStabilityConfig struct {
	Signal                  IdentitySignalSpec `json:"signal"`
	Scale                   float64            `json:"scale"`
	MinimumVariantsPerGroup int                `json:"minimumVariantsPerGroup"`
	MinimumGroups           int                `json:"minimumGroups"`
}

type VariationObservation struct {
	ProbeGroup     string              `json:"probeGroup"`
	VariantID      string              `json:"variantId"`
	Transformation string              `json:"transformation"`
	Value          float64             `json:"value"`
	Evidence       []EvidenceReference `json:"evidence,omitempty"`
}

func ExtractVariationStabilitySignal(config VariationStabilityConfig, observations []VariationObservation) (IdentitySignal, error) {
	if err := config.Signal.validate(); err != nil {
		return IdentitySignal{}, err
	}
	if !finiteNumber(config.Scale) || config.Scale <= 0 || config.MinimumVariantsPerGroup < 2 || config.MinimumGroups <= 0 {
		return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "变形稳定性配置无效")
	}
	type variantValue struct {
		variant        string
		transformation string
		value          float64
	}
	groups := make(map[string][]variantValue)
	seen := make(map[string]struct{})
	var evidence []EvidenceReference
	for index, observation := range observations {
		observation.ProbeGroup = strings.TrimSpace(observation.ProbeGroup)
		observation.VariantID = strings.TrimSpace(observation.VariantID)
		observation.Transformation = strings.TrimSpace(observation.Transformation)
		if observation.ProbeGroup == "" || observation.VariantID == "" || observation.Transformation == "" || !finiteNumber(observation.Value) {
			return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("第 %d 个变形观察值无效", index+1))
		}
		key := observation.ProbeGroup + "\x00" + observation.VariantID
		if _, duplicate := seen[key]; duplicate {
			return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "同一探针组包含重复变形 ID")
		}
		seen[key] = struct{}{}
		groups[observation.ProbeGroup] = append(groups[observation.ProbeGroup], variantValue{variant: observation.VariantID, transformation: observation.Transformation, value: observation.Value})
		var err error
		evidence, err = mergeSignalEvidence(evidence, observation.Evidence)
		if err != nil {
			return IdentitySignal{}, err
		}
	}
	type groupFeature struct {
		ProbeGroup      string   `json:"probeGroup"`
		Variants        []string `json:"variants"`
		Transformations []string `json:"transformations"`
		Minimum         float64  `json:"minimum"`
		Maximum         float64  `json:"maximum"`
		Span            float64  `json:"span"`
		Score           float64  `json:"score"`
	}
	featuresByGroup := make([]groupFeature, 0, len(groups))
	groupScores := make([]float64, 0, len(groups))
	for group, values := range groups {
		if len(values) < config.MinimumVariantsPerGroup {
			continue
		}
		sort.Slice(values, func(i, j int) bool { return values[i].variant < values[j].variant })
		minimum, maximum := values[0].value, values[0].value
		variants := make([]string, len(values))
		transformations := make([]string, len(values))
		for index, value := range values {
			minimum = math.Min(minimum, value.value)
			maximum = math.Max(maximum, value.value)
			variants[index] = value.variant
			transformations[index] = value.transformation
		}
		span := maximum - minimum
		score := clampUnit(1 - span/config.Scale)
		groupScores = append(groupScores, score)
		featuresByGroup = append(featuresByGroup, groupFeature{
			ProbeGroup: group, Variants: variants, Transformations: transformations,
			Minimum: minimum, Maximum: maximum, Span: span, Score: score,
		})
	}
	sort.Slice(featuresByGroup, func(i, j int) bool { return featuresByGroup[i].ProbeGroup < featuresByGroup[j].ProbeGroup })
	if len(observations) < config.Signal.MinimumSamples || len(groupScores) < config.MinimumGroups {
		return insufficientSignal(config.Signal, SignalVariationStability, len(observations), "等价变形的样本或完整探针组不足", evidence), nil
	}
	features := struct {
		Scale  float64        `json:"scale"`
		Groups []groupFeature `json:"groups"`
	}{Scale: config.Scale, Groups: featuresByGroup}
	return observedSignal(config.Signal, SignalVariationStability, len(observations), meanNumbers(groupScores), features, evidence)
}

func observedSignal(spec IdentitySignalSpec, kind IdentitySignalKind, sampleCount int, score float64, features interface{}, evidence []EvidenceReference) (IdentitySignal, error) {
	encoded, err := json.Marshal(features)
	if err != nil {
		return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "身份信号特征编码失败", err)
	}
	canonical, _, err := canonicalJSONObject(encoded)
	if err != nil {
		return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "身份信号特征规范化失败", err)
	}
	score = clampUnit(score)
	signal := IdentitySignal{
		ID: spec.ID, Kind: kind, CorrelationGroup: strings.TrimSpace(spec.CorrelationGroup), Status: SignalObserved,
		SampleCount: sampleCount, Reliability: spec.Reliability, Score: &score, Features: canonical,
		Evidence: append([]EvidenceReference(nil), evidence...),
	}
	if err := signal.Validate(); err != nil {
		return IdentitySignal{}, err
	}
	return signal, nil
}

func insufficientSignal(spec IdentitySignalSpec, kind IdentitySignalKind, sampleCount int, reason string, evidence []EvidenceReference) IdentitySignal {
	return IdentitySignal{
		ID: spec.ID, Kind: kind, CorrelationGroup: strings.TrimSpace(spec.CorrelationGroup), Status: SignalInsufficientEvidence,
		Reason: reason, SampleCount: sampleCount, Reliability: spec.Reliability,
		Evidence: append([]EvidenceReference(nil), evidence...),
	}
}

func mergeSignalEvidence(current, incoming []EvidenceReference) ([]EvidenceReference, error) {
	result := append([]EvidenceReference(nil), current...)
	byID := make(map[string]EvidenceReference, len(result))
	for _, reference := range result {
		byID[reference.ID] = reference
	}
	for _, reference := range incoming {
		reference.ID = strings.TrimSpace(reference.ID)
		reference.Kind = strings.TrimSpace(reference.Kind)
		if reference.ID == "" || reference.Kind == "" || !validSHA256(reference.SHA256) {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份信号证据引用无效")
		}
		if existing, duplicate := byID[reference.ID]; duplicate {
			if existing != reference {
				return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("证据 ID %q 指向冲突内容", reference.ID))
			}
			continue
		}
		byID[reference.ID] = reference
		result = append(result, reference)
	}
	return result, nil
}

func metadataTerminalConsistent(status ExecutionStatus, terminal ProtocolTerminal) bool {
	if !status.Valid() {
		return false
	}
	switch status {
	case StatusCompleted, StatusEmptyOutput:
		return terminal == ProtocolTerminalCompleted
	case StatusFailed:
		return terminal == ProtocolTerminalFailed
	case StatusIncomplete:
		return terminal == ProtocolTerminalIncomplete
	case StatusStreamTruncated:
		return terminal == ProtocolTerminalNone || terminal == ProtocolTerminalIncomplete
	default:
		return terminal == ProtocolTerminalNone
	}
}

func invalidUsage(usage Usage) bool {
	return usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.ReasoningTokens < 0 || usage.CachedTokens < 0 || usage.TotalTokens < 0
}

func modelAccepted(accepted map[string]struct{}, names ...string) bool {
	if len(accepted) == 0 {
		return false
	}
	for _, name := range names {
		if _, ok := accepted[name]; !ok {
			return false
		}
	}
	return true
}

func normalizeModelName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func finiteNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func clampUnit(value float64) float64 {
	return math.Max(0, math.Min(1, value))
}

func meanNumbers(values []float64) float64 {
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}
