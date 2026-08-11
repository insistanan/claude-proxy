package modelaudit

import (
	"reflect"
	"testing"
	"time"
)

func TestActivateAuditRunFreezesEveryJobTarget(t *testing.T) {
	job, pending, targets, startedAt := auditPendingRunWithTargetsFixture(t)
	running, err := ActivateAuditRun(pending, job, targets, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	if running.Status != AuditRunRunning || running.StartedAt == nil || len(running.Targets) != len(job.Targets) {
		t.Fatalf("启动后的审计运行 = %#v", running)
	}
	targets[0].Resolved.ResolvedModel = "mutated"
	if running.Targets[0].Resolved.ResolvedModel == "mutated" {
		t.Fatal("运行目标快照与调用方指针共享可变状态")
	}

	if _, err := ActivateAuditRun(pending, job, nil, startedAt); err == nil {
		t.Fatal("缺少任务目标解析快照时应拒绝启动")
	}
}

func TestCheckAuditRunBudgetUsesFourWorstCaseLimits(t *testing.T) {
	run := activeAuditRunFixture(t)
	run.Budget = AuditRunBudget{
		MaxRequests: 1, MaxInputTokens: 10, MaxOutputTokens: 5, MaxTotalTokens: 15, MaxConcurrentRequests: 1,
	}
	decision, err := CheckAuditRunBudget(run, AuditRequestBudget{MaximumInputTokens: 10, MaximumOutputTokens: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allowed || decision.Projected != (AuditRunUsage{Requests: 1, InputTokens: 10, OutputTokens: 5, TotalTokens: 15}) {
		t.Fatalf("预算边界决策 = %#v", decision)
	}
	run.Usage = decision.Projected
	decision, err = CheckAuditRunBudget(run, AuditRequestBudget{MaximumInputTokens: 10, MaximumOutputTokens: 5})
	if err != nil {
		t.Fatal(err)
	}
	want := []AuditBudgetLimit{AuditBudgetRequestLimit, AuditBudgetInputLimit, AuditBudgetOutputLimit, AuditBudgetTotalLimit}
	if decision.Allowed || !reflect.DeepEqual(decision.Reasons, want) {
		t.Fatalf("预算用尽后的决策 = %#v", decision)
	}
}

func TestRecordAuditRunUsageAndBudgetStopPreservePartialResult(t *testing.T) {
	run := activeAuditRunFixture(t)
	updated, err := RecordAuditRunUsage(run, AuditRunUsage{Requests: 1, InputTokens: 10, OutputTokens: 5, TotalTokens: 15})
	if err != nil {
		t.Fatal(err)
	}
	finishedAt := updated.StartedAt.Add(time.Minute)
	partial, err := StopAuditRun(updated, AuditRunStopBudget, "", finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status != AuditRunPartial || partial.StopReason != AuditRunStopBudget || partial.Usage != updated.Usage {
		t.Fatalf("预算停止后的部分运行 = %#v", partial)
	}
}

func TestCancelAuditRunDistinguishesNoRequestAndPartial(t *testing.T) {
	job := auditJobFixture(t)
	now := job.Schedule.StartAt.Add(time.Hour)
	start, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "run-cancel-pending", Trigger: AuditRunManual,
		OwnerID: "instance-1", LeaseToken: "lease-1", Now: now,
		Limits: AuditRunStartLimits{MaximumActiveRuns: 2, LeaseDurationMs: (5 * time.Minute).Milliseconds()},
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := StopAuditRun(start.Run, AuditRunStopCancelled, "", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != AuditRunCancelled || cancelled.StartedAt != nil || len(cancelled.Targets) != 0 {
		t.Fatalf("启动前取消的运行 = %#v", cancelled)
	}

	running := activeAuditRunFixture(t)
	running, err = RecordAuditRunUsage(running, AuditRunUsage{Requests: 1, InputTokens: 10, OutputTokens: 5, TotalTokens: 15})
	if err != nil {
		t.Fatal(err)
	}
	partial, err := StopAuditRun(running, AuditRunStopCancelled, "", running.StartedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status != AuditRunPartial || partial.StopReason != AuditRunStopCancelled || partial.Usage.Requests != 1 {
		t.Fatalf("已有请求后的取消运行 = %#v", partial)
	}
}

func TestCompleteAndFailAuditRunUseExplicitTerminalStates(t *testing.T) {
	running := activeAuditRunFixture(t)
	finishedAt := running.StartedAt.Add(time.Minute)
	completed, err := CompleteAuditRun(running, finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != AuditRunCompleted || completed.StopReason != AuditRunStopNone {
		t.Fatalf("完成运行 = %#v", completed)
	}
	failed, err := StopAuditRun(running, AuditRunStopFailure, "上游协议失败", finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != AuditRunFailed || failed.StopReason != AuditRunStopFailure || failed.Failure == "" {
		t.Fatalf("失败运行 = %#v", failed)
	}
}

func activeAuditRunFixture(t *testing.T) AuditRun {
	t.Helper()
	job, pending, targets, startedAt := auditPendingRunWithTargetsFixture(t)
	running, err := ActivateAuditRun(pending, job, targets, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	return running
}

func auditPendingRunWithTargetsFixture(t *testing.T) (AuditJob, AuditRun, []AuditRunTargetSnapshot, time.Time) {
	t.Helper()
	job := auditJobFixture(t)
	now := job.Schedule.StartAt.Add(time.Hour)
	start, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "run-active", Trigger: AuditRunManual,
		OwnerID: "instance-1", LeaseToken: "lease-1", Now: now,
		Limits: AuditRunStartLimits{MaximumActiveRuns: 2, LeaseDurationMs: (5 * time.Minute).Milliseconds()},
	})
	if err != nil {
		t.Fatal(err)
	}
	startedAt := now.Add(time.Second)
	resolved := TargetSnapshot{
		ChannelID: job.Targets[0].ChannelID, ChannelKind: job.Targets[0].ChannelKind, ChannelName: "Responses",
		ChannelStatus: "enabled", ServiceType: "responses", Protocol: job.Targets[0].Protocol, WireProtocol: ProtocolResponses,
		DefaultModel: "gpt-5.6-sol", ResolvedModel: "gpt-5.6-sol", Thinking: job.Targets[0].Thinking,
		RequestProfile: job.Targets[0].RequestProfile, CapturedAt: startedAt,
	}
	resolved.ThinkingMap, err = ResolveThinking(resolved.WireProtocol, resolved.Thinking)
	if err != nil {
		t.Fatal(err)
	}
	targets := []AuditRunTargetSnapshot{{TargetID: job.Targets[0].ID, Requested: job.Targets[0], Resolved: &resolved}}
	return start.Job, start.Run, targets, startedAt
}
