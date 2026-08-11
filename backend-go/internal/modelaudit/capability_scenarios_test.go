package modelaudit

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestCapabilityEndToEndScenarios(t *testing.T) {
	tests := []struct {
		name            string
		kind            CapabilityScorerKind
		config          json.RawMessage
		expected        json.RawMessage
		observed        CapabilityObservedOutput
		unsupported     bool
		scoredPerTask   int
		wantStatus      CapabilityAggregationStatus
		wantIndex       *float64
		wantCoverage    float64
		wantErrorClass  string
		wantUnsupported int
		wantMissing     int
	}{
		{
			name: "excellent", kind: CapabilityScorerExactMatch,
			config:   []byte(`{"caseSensitive":true,"trimSpace":true,"collapseWhitespace":false}`),
			expected: []byte(`"expected"`), observed: CapabilityObservedOutput{Text: "expected"}, scoredPerTask: 2,
			wantStatus: CapabilityAggregationComplete, wantIndex: float64Pointer(100), wantCoverage: 1,
		},
		{
			name: "partial", kind: CapabilityScorerSetMatch,
			config:   []byte(`{"caseSensitive":true,"trimSpace":true}`),
			expected: []byte(`["a","b"]`), observed: CapabilityObservedOutput{StructuredOutput: []byte(`["a","c"]`)}, scoredPerTask: 2,
			wantStatus: CapabilityAggregationComplete, wantIndex: float64Pointer(50), wantCoverage: 1, wantErrorClass: "set_mismatch",
		},
		{
			name: "random", kind: CapabilityScorerExactMatch,
			config:   []byte(`{"caseSensitive":true,"trimSpace":true,"collapseWhitespace":false}`),
			expected: []byte(`"expected"`), observed: CapabilityObservedOutput{Text: "unrelated-random-output"}, scoredPerTask: 2,
			wantStatus: CapabilityAggregationComplete, wantIndex: float64Pointer(0), wantCoverage: 1, wantErrorClass: "incorrect_answer",
		},
		{
			name: "malformed", kind: CapabilityScorerJSONStructure,
			config:   []byte(`{"requiredPaths":["/answer"],"forbiddenPaths":[],"types":{"/answer":"string"},"exactPaths":["/answer"],"allowAdditionalTopLevel":true,"allowedTopLevel":[]}`),
			expected: []byte(`{"answer":"ok"}`), observed: CapabilityObservedOutput{Text: "not-json"}, scoredPerTask: 2,
			wantStatus: CapabilityAggregationComplete, wantIndex: float64Pointer(0), wantCoverage: 1, wantErrorClass: "invalid_format",
		},
		{
			name: "unsupported", kind: CapabilityScorerExactMatch,
			config:   []byte(`{"caseSensitive":true,"trimSpace":true,"collapseWhitespace":false}`),
			expected: []byte(`"expected"`), unsupported: true,
			wantStatus: CapabilityAggregationInsufficientEvidence, wantCoverage: 0, wantUnsupported: 14,
		},
		{
			name: "insufficient_samples", kind: CapabilityScorerExactMatch,
			config:   []byte(`{"caseSensitive":true,"trimSpace":true,"collapseWhitespace":false}`),
			expected: []byte(`"expected"`), observed: CapabilityObservedOutput{Text: "expected"}, scoredPerTask: 1,
			wantStatus: CapabilityAggregationProvisional, wantIndex: float64Pointer(100), wantCoverage: 0.5, wantMissing: 7,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, results := runCapabilityScenario(t, test.name, test.kind, test.config, test.expected, test.observed, test.unsupported, test.scoredPerTask)
			if report.Status != test.wantStatus || math.Abs(report.Coverage-test.wantCoverage) > 1e-12 ||
				report.Counts.Unsupported != test.wantUnsupported || report.Counts.Missing != test.wantMissing {
				t.Fatalf("场景 %s 能力报告 = %#v", test.name, report)
			}
			if (report.Index == nil) != (test.wantIndex == nil) || (report.Index != nil && math.Abs(*report.Index-*test.wantIndex) > 1e-12) {
				t.Fatalf("场景 %s 能力指数 = %#v，期望 %#v", test.name, report.Index, test.wantIndex)
			}
			if test.wantErrorClass != "" {
				for _, result := range results {
					if result.Status == CapabilityTaskScored && result.ErrorClass != test.wantErrorClass {
						t.Fatalf("场景 %s 错误分类 = %q，期望 %q", test.name, result.ErrorClass, test.wantErrorClass)
					}
				}
			}
			if test.name == "insufficient_samples" && (report.Formal || report.IndexInterval != nil) {
				t.Fatalf("样本不足场景不能生成正式区间: %#v", report)
			}
		})
	}
}

func runCapabilityScenario(
	t *testing.T,
	name string,
	kind CapabilityScorerKind,
	config json.RawMessage,
	expected json.RawMessage,
	observed CapabilityObservedOutput,
	unsupported bool,
	scoredPerTask int,
) (CapabilityReport, []CapabilityTaskResult) {
	t.Helper()
	scorer, err := NewBuiltinCapabilityScorer(kind)
	if err != nil {
		t.Fatal(err)
	}
	taskPackage := capabilityTaskPackageFixture()
	for index := range taskPackage.Tasks {
		taskPackage.Tasks[index].Scorer = scorer.Descriptor().Ref
		taskPackage.Tasks[index].ScorerKind = kind
		taskPackage.Tasks[index].ScorerConfig = append(json.RawMessage(nil), config...)
	}
	packageSnapshot, err := NewCapabilityTaskPackageSnapshot(taskPackage)
	if err != nil {
		t.Fatal(err)
	}
	plan := make([]CapabilityTaskCoveragePlan, len(packageSnapshot.Package.Tasks))
	for index, task := range packageSnapshot.Package.Tasks {
		plan[index] = CapabilityTaskCoveragePlan{Task: task.Ref, Dimension: task.Dimension, PlannedInstances: 2}
	}
	input := CapabilityAggregationInput{Package: packageSnapshot, Plan: plan}
	results := make([]CapabilityTaskResult, 0, len(plan)*2)
	ordinal := 0
	for index, taskPlan := range plan {
		task := packageSnapshot.Package.Tasks[index]
		repetitions := scoredPerTask
		if unsupported {
			repetitions = taskPlan.PlannedInstances
		}
		for repetition := 0; repetition < repetitions; repetition++ {
			instanceID := fmtCapabilityScenarioInstanceID(name, index, repetition)
			if unsupported {
				results = append(results, CapabilityTaskResult{
					InstanceID: instanceID, Task: task.Ref, Dimension: task.Dimension, Status: CapabilityTaskUnsupported,
					Reason: "协议不支持该任务", CapturedAt: time.Date(2026, 8, 11, 15, 0, ordinal, 0, time.UTC),
				})
				ordinal++
				continue
			}
			spec := validSpec(ProtocolResponses, "channel-capability-scenario")
			spec.Purpose = PurposeCapabilityEval
			instance := CapabilityTaskInstance{
				InstanceID: instanceID, Package: packageSnapshot.Package.Ref, Task: task.Ref, Dimension: task.Dimension,
				Seed: uint64(index*10 + repetition), Repetition: repetition, InputSHA256: testSignalDigest(instanceID),
				ExpectedOutput: append(json.RawMessage(nil), expected...), Execution: spec,
				CreatedAt: time.Date(2026, 8, 11, 15, 0, ordinal, 0, time.UTC),
			}
			evidence := []EvidenceReference{testSignalEvidence(instanceID)}
			score, err := scorer.Score(context.Background(), CapabilityScoringInput{
				Task: task, Instance: instance, ExpectedOutput: expected, Observed: observed, Evidence: evidence,
			})
			if err != nil {
				t.Fatal(err)
			}
			scoreValue := score.Score
			scorerRef := scorer.Descriptor().Ref
			results = append(results, CapabilityTaskResult{
				InstanceID: instanceID, Task: task.Ref, Dimension: task.Dimension, Status: CapabilityTaskScored,
				ErrorClass: score.ErrorClass, Score: &scoreValue, Scorer: &scorerRef, Assertions: score.Assertions,
				ExecutionID: "execution." + instanceID, Evidence: evidence,
				CapturedAt: time.Date(2026, 8, 11, 15, 0, ordinal, 0, time.UTC),
			})
			ordinal++
		}
	}
	input.Results = results
	aggregationConfig := CapabilityAggregationConfig{
		Aggregator:                         VersionedRef{ID: "capability.aggregate.weighted", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
		MinimumScoredInstancesPerDimension: 1, MinimumDimensionsForIndex: 1, MinimumFormalDimensions: 7, FormalCoverageThreshold: 0.9,
		BootstrapSamples: 200, ConfidenceLevel: 0.95, BootstrapSeed: 42, MinimumScoredInstancesForInterval: 2,
	}
	report, err := AggregateCapability(aggregationConfig, input)
	if err != nil {
		t.Fatal(err)
	}
	return report, results
}

func fmtCapabilityScenarioInstanceID(name string, taskIndex, repetition int) string {
	return "capability.scenario." + name + "." + string(rune('a'+taskIndex)) + "." + string(rune('a'+repetition))
}

func float64Pointer(value float64) *float64 {
	return &value
}
