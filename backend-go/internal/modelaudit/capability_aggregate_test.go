package modelaudit

import (
	"math"
	"testing"
	"time"
)

func TestAggregateCapabilityComplete(t *testing.T) {
	config, input := capabilityAggregationFixture(t)
	config.FormalCoverageThreshold = 1
	ordinal := 0
	for index, plan := range input.Plan {
		for repeat := 0; repeat < plan.PlannedInstances; repeat++ {
			input.Results = append(input.Results, capabilityResultFixture(plan, input.Package.Package.Tasks[index].Scorer, ordinal, CapabilityTaskScored, 1))
			ordinal++
		}
	}
	report, err := AggregateCapability(config, input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CapabilityAggregationComplete || !report.Formal || report.Index == nil || *report.Index != 100 ||
		report.IndexInterval == nil || report.IndexInterval.Lower != 100 || report.IndexInterval.Upper != 100 || math.Abs(report.Coverage-1) > 1e-12 {
		t.Fatalf("完整能力报告 = %#v", report)
	}
	if report.ScoredDimensions != 7 || report.FormalDimensions != 7 || len(report.Dimensions) != 7 {
		t.Fatalf("完整能力维度 = %#v", report.Dimensions)
	}
}

func TestAggregateCapabilityDoesNotScoreUnsupportedAsZero(t *testing.T) {
	config, input := capabilityAggregationFixture(t)
	input.Results = append(input.Results,
		capabilityResultFixture(input.Plan[0], input.Package.Package.Tasks[0].Scorer, 0, CapabilityTaskScored, 0.5),
		capabilityResultFixture(input.Plan[0], input.Package.Package.Tasks[0].Scorer, 1, CapabilityTaskScored, 0.5),
	)
	ordinal := 2
	for index, plan := range input.Plan[1:] {
		for repeat := 0; repeat < plan.PlannedInstances; repeat++ {
			input.Results = append(input.Results, capabilityResultFixture(plan, input.Package.Package.Tasks[index+1].Scorer, ordinal, CapabilityTaskUnsupported, 0))
			ordinal++
		}
	}
	report, err := AggregateCapability(config, input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CapabilityAggregationProvisional || report.Formal || report.Index == nil || math.Abs(*report.Index-50) > 1e-12 {
		t.Fatalf("部分能力报告 = %#v", report)
	}
	if math.Abs(report.Coverage-1.0/7.0) > 1e-12 || report.Counts.Unsupported != 12 {
		t.Fatalf("不支持任务覆盖率或计数 = %#v", report)
	}
	for _, dimension := range report.Dimensions {
		if dimension.Dimension != input.Plan[0].Dimension && dimension.Score != nil {
			t.Fatalf("不支持维度被静默记分: %#v", dimension)
		}
	}
}

func TestAggregateCapabilityInsufficientAndMissing(t *testing.T) {
	config, input := capabilityAggregationFixture(t)
	for index, plan := range input.Plan {
		if index == 0 {
			continue
		}
		for repeat := 0; repeat < plan.PlannedInstances; repeat++ {
			input.Results = append(input.Results, capabilityResultFixture(plan, input.Package.Package.Tasks[index].Scorer, index*2+repeat, CapabilityTaskUnsupported, 0))
		}
	}
	report, err := AggregateCapability(config, input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CapabilityAggregationInsufficientEvidence || report.Index != nil || report.Counts.Missing != 2 || len(report.Reasons) == 0 {
		t.Fatalf("证据不足能力报告 = %#v", report)
	}
}

func TestAggregateCapabilityOmitsIntervalWhenSamplesAreInsufficient(t *testing.T) {
	config, input := capabilityAggregationFixture(t)
	input.Results = []CapabilityTaskResult{
		capabilityResultFixture(input.Plan[0], input.Package.Package.Tasks[0].Scorer, 0, CapabilityTaskScored, 0.8),
	}
	report, err := AggregateCapability(config, input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Index == nil || report.IndexInterval != nil || report.Formal || report.Status != CapabilityAggregationProvisional {
		t.Fatalf("区间样本不足能力报告 = %#v", report)
	}
}

func TestAggregateCapabilityBootstrapIsIndependentOfResultOrder(t *testing.T) {
	config, input := capabilityAggregationFixture(t)
	ordinal := 0
	for index, plan := range input.Plan {
		input.Results = append(input.Results,
			capabilityResultFixture(plan, input.Package.Package.Tasks[index].Scorer, ordinal, CapabilityTaskScored, 0.2),
			capabilityResultFixture(plan, input.Package.Package.Tasks[index].Scorer, ordinal+1, CapabilityTaskScored, 0.8),
		)
		ordinal += 2
	}
	forward, err := AggregateCapability(config, input)
	if err != nil {
		t.Fatal(err)
	}
	for left, right := 0, len(input.Results)-1; left < right; left, right = left+1, right-1 {
		input.Results[left], input.Results[right] = input.Results[right], input.Results[left]
	}
	reversed, err := AggregateCapability(config, input)
	if err != nil {
		t.Fatal(err)
	}
	if *forward.IndexInterval != *reversed.IndexInterval {
		t.Fatalf("结果顺序改变总区间: forward=%#v reversed=%#v", forward.IndexInterval, reversed.IndexInterval)
	}
	for index := range forward.Dimensions {
		if *forward.Dimensions[index].Interval != *reversed.Dimensions[index].Interval {
			t.Fatalf("结果顺序改变维度 %s 区间", forward.Dimensions[index].Dimension)
		}
	}
}

func TestAggregateCapabilityRejectsDuplicateInstance(t *testing.T) {
	config, input := capabilityAggregationFixture(t)
	result := capabilityResultFixture(input.Plan[0], input.Package.Package.Tasks[0].Scorer, 0, CapabilityTaskScored, 1)
	input.Results = []CapabilityTaskResult{result, result}
	if _, err := AggregateCapability(config, input); err == nil {
		t.Fatal("重复能力实例结果应显式失败")
	}
}

func capabilityAggregationFixture(t *testing.T) (CapabilityAggregationConfig, CapabilityAggregationInput) {
	t.Helper()
	snapshot, err := NewCapabilityTaskPackageSnapshot(capabilityTaskPackageFixture())
	if err != nil {
		t.Fatal(err)
	}
	plan := make([]CapabilityTaskCoveragePlan, len(snapshot.Package.Tasks))
	for index, task := range snapshot.Package.Tasks {
		plan[index] = CapabilityTaskCoveragePlan{Task: task.Ref, Dimension: task.Dimension, PlannedInstances: 2}
	}
	return CapabilityAggregationConfig{
		Aggregator:                         VersionedRef{ID: "capability.aggregate.weighted", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
		MinimumScoredInstancesPerDimension: 1, MinimumDimensionsForIndex: 1, MinimumFormalDimensions: 7, FormalCoverageThreshold: 0.9,
		BootstrapSamples: 200, ConfidenceLevel: 0.95, BootstrapSeed: 42, MinimumScoredInstancesForInterval: 2,
	}, CapabilityAggregationInput{Package: snapshot, Plan: plan}
}

func capabilityResultFixture(plan CapabilityTaskCoveragePlan, scorer VersionedRef, ordinal int, status CapabilityTaskResultStatus, scoreValue float64) CapabilityTaskResult {
	result := CapabilityTaskResult{
		InstanceID: "capability.result." + string(rune('a'+ordinal)), Task: plan.Task, Dimension: plan.Dimension,
		Status: status, CapturedAt: time.Date(2026, 8, 11, 14, ordinal, 0, 0, time.UTC),
	}
	if status == CapabilityTaskScored {
		result.Score = &scoreValue
		result.Scorer = &scorer
		result.ExecutionID = "execution-" + result.InstanceID
		earned := scoreValue
		result.Assertions = []CapabilityAssertionResult{{
			ID: "score", Passed: scoreValue == 1, Weight: 1, Earned: earned, Reason: "部分正确",
		}}
		if scoreValue == 1 {
			result.Assertions[0].Reason = ""
		} else {
			result.ErrorClass = "partial_answer"
		}
	} else {
		result.Reason = "固定夹具未产生可判分结果"
	}
	return result
}
