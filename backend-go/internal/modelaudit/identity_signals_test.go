package modelaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"testing"
)

func TestExtractJuiceSignal(t *testing.T) {
	config := JuiceSignalConfig{
		Signal: testIdentitySignalSpec("identity.juice", "behavior.juice", 4),
		Bands: []JuiceBand{
			{Profile: "low", Minimum: 10, Maximum: 20, Rank: 0},
			{Profile: "high", Minimum: 30, Maximum: 50, Rank: 1},
		},
	}
	observations := []JuiceObservation{
		{Profile: "low", Value: 12, Evidence: []EvidenceReference{testSignalEvidence("juice-low-1")}},
		{Profile: "low", Value: 14, Evidence: []EvidenceReference{testSignalEvidence("juice-low-2")}},
		{Profile: "high", Value: 40, Evidence: []EvidenceReference{testSignalEvidence("juice-high-1")}},
		{Profile: "high", Value: 42, Evidence: []EvidenceReference{testSignalEvidence("juice-high-2")}},
	}

	signal, err := ExtractJuiceSignal(config, observations)
	if err != nil {
		t.Fatal(err)
	}
	if signal.Status != SignalObserved || signal.Score == nil || *signal.Score < 0.9 || len(signal.Evidence) != 4 {
		t.Fatalf("Juice 信号 = %#v", signal)
	}
	if err := signal.Validate(); err != nil {
		t.Fatal(err)
	}
	var features struct {
		Profiles            []juiceProfileFeature `json:"profiles"`
		OrderingComparisons int                   `json:"orderingComparisons"`
		OrderingPassed      int                   `json:"orderingPassed"`
	}
	if err := json.Unmarshal(signal.Features, &features); err != nil {
		t.Fatal(err)
	}
	if len(features.Profiles) != 2 || features.OrderingComparisons != 1 || features.OrderingPassed != 1 {
		t.Fatalf("Juice 特征 = %#v", features)
	}
}

func TestJuiceSignalKeepsEvidenceWhenInsufficient(t *testing.T) {
	config := JuiceSignalConfig{
		Signal: testIdentitySignalSpec("identity.juice", "behavior.juice", 2),
		Bands:  []JuiceBand{{Profile: "low", Minimum: 10, Maximum: 20, Rank: 0}},
	}
	signal, err := ExtractJuiceSignal(config, []JuiceObservation{{
		Profile: "low", Value: 12, Evidence: []EvidenceReference{testSignalEvidence("juice-only")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if signal.Status != SignalInsufficientEvidence || signal.Score != nil || len(signal.Evidence) != 1 {
		t.Fatalf("样本不足 Juice 信号 = %#v", signal)
	}
	if err := signal.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractRewriteAndSyntheticCoverageSignals(t *testing.T) {
	rewrite, err := ExtractRewriteSignal(
		RewriteSignalConfig{
			Signal: testIdentitySignalSpec("identity.rewrite", "behavior.rewrite", 4),
			Expectations: []RewriteExpectation{
				{InputValue: 32, OutputMin: 40, OutputMax: 49},
				{InputValue: 48, OutputMin: 40, OutputMax: 49},
			},
		},
		[]RewriteObservation{{InputValue: 32, OutputValue: 40}, {InputValue: 32, OutputValue: 44}, {InputValue: 48, OutputValue: 49}, {InputValue: 48, OutputValue: 60}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if rewrite.Score == nil || math.Abs(*rewrite.Score-0.75) > 1e-12 {
		t.Fatalf("改写信号 = %#v", rewrite)
	}

	coverage, err := ExtractSyntheticCoverageSignal(
		SyntheticCoverageConfig{
			Signal:  testIdentitySignalSpec("identity.synthetic", "behavior.juice", 4),
			Targets: []float64{40}, Tolerance: 0.01, MinimumDistinctControls: 4,
		},
		[]SyntheticCoverageObservation{
			{ControlID: "a", Value: 40}, {ControlID: "b", Value: 40},
			{ControlID: "c", Value: 40}, {ControlID: "d", Value: 17},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if coverage.Score == nil || math.Abs(*coverage.Score-0.25) > 1e-12 {
		t.Fatalf("合成覆盖信号 = %#v", coverage)
	}
}

func TestExtractModeDifferenceSignal(t *testing.T) {
	signal, err := ExtractModeDifferenceSignal(
		ModeDifferenceConfig{
			Signal: testIdentitySignalSpec("identity.mode", "behavior.mode", 4),
			Expectations: []ModeExpectation{
				{FromMode: ProbeModeNormal, ToMode: ProbeModeCodexCompatible, FromContextClass: "short", ToContextClass: "short", MinimumDelta: 8, MaximumDelta: 12},
				{FromMode: ProbeModeCodexCompatible, ToMode: ProbeModeNativeCodex, FromContextClass: "short", ToContextClass: "short", MinimumDelta: 8, MaximumDelta: 12},
				{FromMode: ProbeModeNativeCodex, ToMode: ProbeModeNativeCodex, FromContextClass: "short", ToContextClass: "long", MinimumDelta: 4, MaximumDelta: 6},
			},
			MinimumComparisons: 3,
		},
		[]ModeObservation{
			{ProbeID: "probe-a", Mode: ProbeModeNormal, ContextClass: "short", Value: 10},
			{ProbeID: "probe-a", Mode: ProbeModeCodexCompatible, ContextClass: "short", Value: 20},
			{ProbeID: "probe-a", Mode: ProbeModeNativeCodex, ContextClass: "short", Value: 30},
			{ProbeID: "probe-a", Mode: ProbeModeNativeCodex, ContextClass: "long", Value: 35},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if signal.Score == nil || *signal.Score != 1 {
		t.Fatalf("模式差分信号 = %#v", signal)
	}
}

func TestExtractMetadataConsistencySignal(t *testing.T) {
	trueValue := true
	config := MetadataConsistencyConfig{
		Signal:               testIdentitySignalSpec("identity.metadata", "metadata", 1),
		AcceptedModels:       []string{"gpt-5.6-sol", "sol-latest"},
		RequireReturnedModel: true, RequireUsage: true, RequireReasoningWhenRequested: true,
		RequireStructureAssessment: true, RequireCapabilityAssessment: true,
	}
	good, err := ExtractMetadataConsistencySignal(config, []MetadataObservation{{
		ObservationID: "execution-good", RequestedModel: "gpt-5.6-sol", DeclaredModel: "sol-latest", ReturnedModel: "sol-latest",
		Protocol: ProtocolResponses, WireProtocol: ProtocolResponses, Status: StatusCompleted, Terminal: ProtocolTerminalCompleted,
		Usage:              Usage{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 5, CachedTokens: 10, TotalTokens: 120},
		ReasoningRequested: true, ResponseStructureValid: &trueValue, ProtocolCapabilityValid: &trueValue,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if good.Score == nil || *good.Score != 1 {
		t.Fatalf("一致元数据信号 = %#v", good)
	}

	falseValue := false
	bad, err := ExtractMetadataConsistencySignal(config, []MetadataObservation{{
		ObservationID: "execution-bad", RequestedModel: "gpt-5.6-sol", DeclaredModel: "sol-latest", ReturnedModel: "other",
		Protocol: ProtocolResponses, WireProtocol: ProtocolResponses, Status: StatusCompleted, Terminal: ProtocolTerminalFailed,
		Usage: Usage{InputTokens: 5, OutputTokens: 2, CachedTokens: 6, TotalTokens: 6}, ReasoningRequested: true,
		ResponseStructureValid: &falseValue, ProtocolCapabilityValid: &falseValue,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if bad.Score == nil || *bad.Score >= 0.5 {
		t.Fatalf("矛盾元数据信号 = %#v", bad)
	}
	var features struct {
		Contradictions []struct {
			Check string `json:"check"`
		} `json:"contradictions"`
	}
	if err := json.Unmarshal(bad.Features, &features); err != nil {
		t.Fatal(err)
	}
	if len(features.Contradictions) < 5 {
		t.Fatalf("元数据矛盾未独立保留: %#v", features.Contradictions)
	}
}

func TestExtractVariationStabilitySignal(t *testing.T) {
	signal, err := ExtractVariationStabilitySignal(
		VariationStabilityConfig{
			Signal: testIdentitySignalSpec("identity.variation", "behavior.variation", 4),
			Scale:  1, MinimumVariantsPerGroup: 2, MinimumGroups: 2,
		},
		[]VariationObservation{
			{ProbeGroup: "country", VariantID: "base", Transformation: "original", Value: 0.4},
			{ProbeGroup: "country", VariantID: "rewrite", Transformation: "semantic_rewrite", Value: 0.5},
			{ProbeGroup: "bird", VariantID: "base", Transformation: "original", Value: 0.7},
			{ProbeGroup: "bird", VariantID: "order", Transformation: "order_permutation", Value: 0.9},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if signal.Score == nil || math.Abs(*signal.Score-0.85) > 1e-12 {
		t.Fatalf("变形稳定性信号 = %#v", signal)
	}
}

func TestSignalExtractorRejectsConflictingEvidence(t *testing.T) {
	first := testSignalEvidence("shared")
	second := first
	second.SHA256 = testSignalDigest("different")
	_, err := ExtractRewriteSignal(
		RewriteSignalConfig{
			Signal:       testIdentitySignalSpec("identity.rewrite", "behavior.rewrite", 2),
			Expectations: []RewriteExpectation{{InputValue: 32, OutputMin: 40, OutputMax: 49}},
		},
		[]RewriteObservation{
			{InputValue: 32, OutputValue: 40, Evidence: []EvidenceReference{first}},
			{InputValue: 32, OutputValue: 41, Evidence: []EvidenceReference{second}},
		},
	)
	if err == nil {
		t.Fatal("同一证据 ID 指向不同哈希时应显式失败")
	}
}

func testIdentitySignalSpec(id, group string, minimumSamples int) IdentitySignalSpec {
	return IdentitySignalSpec{ID: id, CorrelationGroup: group, MinimumSamples: minimumSamples, Reliability: 0.8}
}

func testSignalEvidence(id string) EvidenceReference {
	return EvidenceReference{ID: id, Kind: "fixture", SHA256: testSignalDigest(id), Redacted: true}
}

func testSignalDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
