package modelaudit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDeclarativeAuditModStrategyPlansAndEvaluatesIndependentSamples(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "random-number")
	manifest := auditModLLMFixture("random-number-mod")
	manifest.Sampling.SampleIntervalMs = 250
	writeAuditModFixture(t, directory, manifest)
	writeAuditModRules(t, directory, `{
		"rules":[{"id":"lower-majority","path":"histogram.lower.rate","operator":"gte","value":0.5,
			"outcome":{"verdict":"lower_bias","score":0.8,"confidence":0.7,"message":"Lower bucket is the majority."}}],
		"fallback":{"verdict":"no_lower_bias","score":0.4,"confidence":0.5,"message":"Lower bucket is not the majority."}
	}`)
	bundle, err := loadAuditModBundle(directory, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := NewDeclarativeAuditModStrategy(bundle)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewStrategyRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceDynamic(strategy); err != nil {
		t.Fatal(err)
	}
	ref := strategy.Descriptor().Ref
	snapshots, err := registry.Freeze([]StrategySelection{{
		StrategyID: ref.ID, Ref: &ref, Enabled: true, Config: json.RawMessage(`{"sampleCount":3,"sampleIntervalMs":250}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	target := builtinProbeTarget(t)
	plans, err := strategy.Plan(context.Background(), StrategyPlanInput{
		RunID: "run.fixture", Target: target, Config: snapshots[0], BaseSeed: 100, MaximumSamples: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 3 || plans[0].DelayBeforeMs != 0 || plans[1].DelayBeforeMs != 250 || plans[2].DelayBeforeMs != 250 {
		t.Fatalf("Plan() delays = %#v", plans)
	}
	samples := builtinProbeSamples(t, snapshots[0], plans, target, func(ordinal int) (string, Usage) {
		return []string{"10", "20", "80"}[ordinal], Usage{}
	})
	evaluation, err := strategy.Evaluate(context.Background(), StrategyEvaluationInput{Config: snapshots[0], Samples: samples})
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.Status != EvaluationCompleted || len(evaluation.Features) != 1 {
		t.Fatalf("Evaluate() = %#v", evaluation)
	}
	var features AuditModEvaluationFeatures
	if err := json.Unmarshal(evaluation.Features[0].Values, &features); err != nil {
		t.Fatal(err)
	}
	if features.Summary.ObservationCount != 3 || features.Summary.Histogram["lower"].Count != 2 || features.RuleResult.MatchedRuleID != "lower-majority" {
		t.Fatalf("features = %#v", features)
	}
}

func TestDeclarativeAuditModStrategyBatchModeProducesOneRequest(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "batch-json")
	manifest := auditModLLMFixture("batch-json-mod")
	manifest.Sampling.Mode = AuditModSamplingBatch
	manifest.Parser = AuditModParser{Type: AuditModParserJSON}
	manifest.Aggregation = AuditModAggregation{Type: AuditModAggregationNumeric}
	writeAuditModFixture(t, directory, manifest)
	bundle, err := loadAuditModBundle(directory, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := NewDeclarativeAuditModStrategy(bundle)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewStrategyRegistry(strategy)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := freezeBuiltinProbe(t, registry, manifest.ID, `{"sampleCount":3,"sampleIntervalMs":0}`)
	target := builtinProbeTarget(t)
	plans, err := strategy.Plan(context.Background(), StrategyPlanInput{
		RunID: "run.fixture", Target: target, Config: snapshot, BaseSeed: 200, MaximumSamples: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("Plan() request count = %d, want 1", len(plans))
	}
	samples := builtinProbeSamples(t, snapshot, plans, target, func(int) (string, Usage) { return `[1,2,3]`, Usage{} })
	evaluation, err := strategy.Evaluate(context.Background(), StrategyEvaluationInput{Config: snapshot, Samples: samples})
	if err != nil {
		t.Fatal(err)
	}
	var features AuditModEvaluationFeatures
	if err := json.Unmarshal(evaluation.Features[0].Values, &features); err != nil {
		t.Fatal(err)
	}
	if features.Summary.RequestCount != 1 || features.Summary.ObservationCount != 3 || features.Summary.Numeric == nil || features.Summary.Numeric.Mean != 2 {
		t.Fatalf("batch summary = %#v", features.Summary)
	}
}

func TestStrategyRegistryRetainsPinnedDynamicVersion(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "versioned")
	manifest := auditModLLMFixture("versioned-mod")
	writeAuditModFixture(t, directory, manifest)
	firstBundle, err := loadAuditModBundle(directory, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewDeclarativeAuditModStrategy(firstBundle)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewStrategyRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceDynamic(first); err != nil {
		t.Fatal(err)
	}
	firstRef := first.Descriptor().Ref

	if err := os.WriteFile(filepath.Join(directory, "probe-prompt.md"), []byte("Return a different integer. Sample {{.SampleNumber}}."), 0644); err != nil {
		t.Fatal(err)
	}
	secondBundle, err := loadAuditModBundle(directory, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewDeclarativeAuditModStrategy(secondBundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceDynamic(second); err != nil {
		t.Fatal(err)
	}
	if _, found := registry.GetVersion(firstRef); !found {
		t.Fatal("old pinned dynamic strategy was removed")
	}
	snapshots, err := registry.Freeze([]StrategySelection{{
		StrategyID: firstRef.ID, Ref: &firstRef, Enabled: true, Config: json.RawMessage(`{"sampleCount":1,"sampleIntervalMs":0}`),
	}})
	if err != nil || len(snapshots) != 1 || snapshots[0].Ref != firstRef {
		t.Fatalf("Freeze(old ref) = %#v, %v", snapshots, err)
	}
}

func writeAuditModRules(t *testing.T, directory, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, "rules.json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
