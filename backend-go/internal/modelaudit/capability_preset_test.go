package modelaudit

import (
	"math"
	"reflect"
	"testing"
)

func TestPrepareCapabilityRunPresets(t *testing.T) {
	snapshot := capabilityPackageSnapshotFixture(t)

	quick := capabilityPresetFixture(snapshot, CapabilityPresetQuick, snapshot.Package.Tasks[:2])
	quick.FormalEligible = false
	quick.Limits = CapabilityBudgetLimits{Requests: 2, InputTokens: 512, OutputTokens: 256, TotalTokens: 768}
	plan, err := PrepareCapabilityRun(quick, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != CapabilityPresetQuick || plan.FormalEligible || plan.Estimate.Requests != 2 || plan.Estimate.TotalTokens != 768 {
		t.Fatalf("快速能力计划 = %#v", plan)
	}

	standard := capabilityPresetFixture(snapshot, CapabilityPresetStandard, snapshot.Package.Tasks)
	plan, err = PrepareCapabilityRun(standard, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.FormalEligible || plan.Estimate.Requests != 14 || plan.Estimate.InputTokens != 3584 || plan.Estimate.OutputTokens != 1792 {
		t.Fatalf("标准能力计划 = %#v", plan)
	}

	deep := capabilityPresetFixture(snapshot, CapabilityPresetDeep, snapshot.Package.Tasks)
	plan, err = PrepareCapabilityRun(deep, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != CapabilityPresetDeep || !plan.FormalEligible || plan.Preset != deep.Ref || plan.Estimate.TotalTokens != 5376 {
		t.Fatalf("深入能力计划 = %#v", plan)
	}
}

func TestPrepareCapabilityRunRejectsBudgetAndCoverageViolations(t *testing.T) {
	snapshot := capabilityPackageSnapshotFixture(t)
	standard := capabilityPresetFixture(snapshot, CapabilityPresetStandard, snapshot.Package.Tasks)
	standard.Limits.Requests = 13
	if _, err := PrepareCapabilityRun(standard, snapshot); err == nil {
		t.Fatal("预设最坏请求数超过预算时应被拒绝")
	}

	standard = capabilityPresetFixture(snapshot, CapabilityPresetStandard, snapshot.Package.Tasks[:6])
	if _, err := PrepareCapabilityRun(standard, snapshot); err == nil {
		t.Fatal("正式预设缺少维度时应被拒绝")
	}
}

func TestCheckCapabilityBudget(t *testing.T) {
	snapshot := capabilityPackageSnapshotFixture(t)
	preset := capabilityPresetFixture(snapshot, CapabilityPresetStandard, snapshot.Package.Tasks)
	plan, err := PrepareCapabilityRun(preset, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	nextTask := snapshot.Package.Tasks[0]
	usage := CapabilityBudgetUsage{
		Requests:     plan.Limits.Requests - 1,
		InputTokens:  plan.Limits.InputTokens - int64(nextTask.MaximumInputTokens),
		OutputTokens: plan.Limits.OutputTokens - int64(nextTask.MaximumOutputTokens),
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	decision, err := CheckCapabilityBudget(plan, usage, nextTask)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allowed || len(decision.Reasons) != 0 {
		t.Fatalf("预算边界内的任务被拒绝: %#v", decision)
	}

	decision, err = CheckCapabilityBudget(plan, decision.Projected, nextTask)
	if err != nil {
		t.Fatal(err)
	}
	wantReasons := []CapabilityBudgetLimit{
		CapabilityBudgetRequestLimit,
		CapabilityBudgetInputLimit,
		CapabilityBudgetOutputLimit,
		CapabilityBudgetTotalLimit,
	}
	if decision.Allowed || !reflect.DeepEqual(decision.Reasons, wantReasons) {
		t.Fatalf("预算用尽后仍允许任务: %#v", decision)
	}
}

func TestCapabilityBudgetUsageRejectsTokenOverflow(t *testing.T) {
	usage := CapabilityBudgetUsage{
		InputTokens:  math.MaxInt64,
		OutputTokens: 1,
		TotalTokens:  math.MaxInt64,
	}
	if err := usage.Validate(); err == nil {
		t.Fatal("输入与输出 token 相加溢出时应被拒绝")
	}
}

func TestAggregateCapabilityMarksBudgetPartialResult(t *testing.T) {
	config, input := capabilityAggregationFixture(t)
	input.Results = []CapabilityTaskResult{
		capabilityResultFixture(input.Plan[0], input.Package.Package.Tasks[0].Scorer, 0, CapabilityTaskScored, 0.8),
	}
	input.StopReason = CapabilityRunStoppedByBudget
	report, err := AggregateCapability(config, input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CapabilityAggregationPartialBudget || report.StopReason != CapabilityRunStoppedByBudget || report.Formal || len(report.Reasons) == 0 {
		t.Fatalf("预算部分能力报告 = %#v", report)
	}
}

func capabilityPackageSnapshotFixture(t *testing.T) CapabilityTaskPackageSnapshot {
	t.Helper()
	snapshot, err := NewCapabilityTaskPackageSnapshot(capabilityTaskPackageFixture())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func capabilityPresetFixture(snapshot CapabilityTaskPackageSnapshot, mode CapabilityPresetMode, tasks []CapabilityTaskDefinition) CapabilityRunPreset {
	selections := make([]CapabilityPresetTask, len(tasks))
	for index, task := range tasks {
		repetitions := task.Repetitions
		if mode == CapabilityPresetQuick {
			repetitions = 1
		}
		selections[index] = CapabilityPresetTask{TaskID: task.Ref.ID, Repetitions: repetitions}
	}
	formalEligible := mode != CapabilityPresetQuick
	return CapabilityRunPreset{
		Ref:  VersionedRef{ID: "capability.preset." + string(mode), SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
		Mode: mode, Package: snapshot.Package.Ref, FormalEligible: formalEligible, Tasks: selections,
		Limits: CapabilityBudgetLimits{Requests: 14, InputTokens: 3584, OutputTokens: 1792, TotalTokens: 5376},
	}
}
