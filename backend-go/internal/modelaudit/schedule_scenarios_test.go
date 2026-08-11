package modelaudit

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditM6FixedScenarioCoversSchedulingPersistenceAndStops(t *testing.T) {
	ctx := context.Background()
	job := auditJobFixture(t)
	job.Targets = append(job.Targets, AuditJobTarget{
		ID: "target-2", ChannelID: "channel-chat", ChannelKind: ChannelKindChat, Protocol: ProtocolChat,
		Model: "gpt-5.6-sol", Thinking: ThinkingMedium, RequestProfile: "chat.standard.v1",
	})
	job.Budget = AuditRunBudget{
		MaxRequests: 2, MaxInputTokens: 20, MaxOutputTokens: 20, MaxTotalTokens: 40, MaxConcurrentRequests: 1,
	}
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "m6-scenario.db")
	store, err := NewAuditSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	missedAfter := job.Schedule.StartAt.Add(2*AuditDefaultInterval + AuditDefaultInterval/2)
	slot, err := NextAuditScheduleSlot(job.ID, job.Schedule, missedAfter)
	if err != nil {
		t.Fatal(err)
	}
	if slot == nil || slot.Ordinal != 3 || !slot.ScheduledAt.After(missedAfter) {
		t.Fatalf("停机后应直接选择未来槽位: %#v", slot)
	}
	limits := AuditRunStartLimits{MaximumActiveRuns: 4, LeaseDurationMs: (10 * time.Minute).Milliseconds()}
	scheduledStart, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "run-scenario-scheduled", Trigger: AuditRunScheduled,
		ScheduledSlot: slot, OwnerID: "instance-scenario", LeaseToken: "lease-scheduled", Now: slot.ScheduledAt, Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRunStart(ctx, scheduledStart, limits.MaximumActiveRuns); err != nil {
		t.Fatal(err)
	}

	staleOverlap, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "run-scenario-overlap", Trigger: AuditRunManual,
		OwnerID: "instance-overlap", LeaseToken: "lease-overlap", Now: slot.ScheduledAt, Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRunStart(ctx, staleOverlap, limits.MaximumActiveRuns); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("同一任务重入应由数据库 CAS 拒绝: %v", err)
	}
	if _, found, err := store.GetRun(ctx, staleOverlap.Run.ID); err != nil || found {
		t.Fatalf("失败的重入事务不应留下运行: found=%v err=%v", found, err)
	}

	scheduledRunningAt := slot.ScheduledAt.Add(time.Second)
	scheduledRunning, err := ActivateAuditRun(
		scheduledStart.Run,
		scheduledStart.Job,
		auditResolvedTargetsForJob(t, scheduledStart.Job, scheduledRunningAt),
		scheduledRunningAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(scheduledRunning.Targets) != 2 || scheduledRunning.Targets[0].Resolved == nil || scheduledRunning.Targets[1].Resolved == nil {
		t.Fatalf("多渠道解析快照不完整: %#v", scheduledRunning.Targets)
	}
	if err := store.SaveRun(ctx, scheduledRunning, AuditRunPending, scheduledStart.Lease, scheduledRunningAt); err != nil {
		t.Fatal(err)
	}
	scheduledRunning, err = RecordAuditRunUsage(scheduledRunning, AuditRunUsage{
		Requests: 1, InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRun(ctx, scheduledRunning, AuditRunRunning, scheduledStart.Lease, scheduledRunningAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	budgetDecision, err := CheckAuditRunBudget(scheduledRunning, AuditRequestBudget{MaximumInputTokens: 11, MaximumOutputTokens: 5})
	if err != nil {
		t.Fatal(err)
	}
	if budgetDecision.Allowed || len(budgetDecision.Reasons) != 1 || budgetDecision.Reasons[0] != AuditBudgetInputLimit {
		t.Fatalf("预算预检未阻止下一请求: %#v", budgetDecision)
	}
	scheduledFinishedAt := slot.ScheduledAt.Add(2 * time.Minute)
	scheduledPartial, err := StopAuditRun(scheduledRunning, AuditRunStopBudget, "", scheduledFinishedAt)
	if err != nil {
		t.Fatal(err)
	}
	scheduledFinish, err := PrepareAuditRunFinish(
		scheduledStart.Job, scheduledStart.Job.Revision, scheduledPartial, scheduledStart.Lease, scheduledFinishedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRunFinish(ctx, scheduledFinish, scheduledPartial, scheduledStart.Lease, scheduledStart.Job.Revision); err != nil {
		t.Fatal(err)
	}

	manualStartedAt := scheduledFinishedAt.Add(time.Minute)
	manualStart, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: scheduledFinish.Job, ExpectedRevision: scheduledFinish.Job.Revision, RunID: "run-scenario-manual", Trigger: AuditRunManual,
		OwnerID: "instance-scenario", LeaseToken: "lease-manual", Now: manualStartedAt, Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	if manualStart.Run.ScheduledFor != nil {
		t.Fatalf("手动运行不应绑定定时槽位: %#v", manualStart.Run.ScheduledFor)
	}
	if err := store.CommitRunStart(ctx, manualStart, limits.MaximumActiveRuns); err != nil {
		t.Fatal(err)
	}
	manualRunningAt := manualStartedAt.Add(time.Second)
	manualRunning, err := ActivateAuditRun(
		manualStart.Run,
		manualStart.Job,
		auditResolvedTargetsForJob(t, manualStart.Job, manualRunningAt),
		manualRunningAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRun(ctx, manualRunning, AuditRunPending, manualStart.Lease, manualRunningAt); err != nil {
		t.Fatal(err)
	}
	manualRunning, err = RecordAuditRunUsage(manualRunning, AuditRunUsage{
		Requests: 1, InputTokens: 4, OutputTokens: 3, TotalTokens: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRun(ctx, manualRunning, AuditRunRunning, manualStart.Lease, manualRunningAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	manualFinishedAt := manualStartedAt.Add(2 * time.Minute)
	manualPartial, err := StopAuditRun(manualRunning, AuditRunStopCancelled, "", manualFinishedAt)
	if err != nil {
		t.Fatal(err)
	}
	manualFinish, err := PrepareAuditRunFinish(manualStart.Job, manualStart.Job.Revision, manualPartial, manualStart.Lease, manualFinishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRunFinish(ctx, manualFinish, manualPartial, manualStart.Lease, manualStart.Job.Revision); err != nil {
		t.Fatal(err)
	}
	expectedNext, err := NextAuditScheduleSlot(job.ID, job.Schedule, manualFinishedAt)
	if err != nil {
		t.Fatal(err)
	}
	actualNext, err := NextAuditScheduleSlot(manualFinish.Job.ID, manualFinish.Job.Schedule, manualFinishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if expectedNext == nil || actualNext == nil || expectedNext.Ordinal != actualNext.Ordinal || !expectedNext.ScheduledAt.Equal(actualNext.ScheduledAt) {
		t.Fatalf("手动运行改变了定时计划: expected=%#v actual=%#v", expectedNext, actualNext)
	}

	recoveryStartedAt := manualFinishedAt.Add(time.Minute)
	recoveryLimits := AuditRunStartLimits{MaximumActiveRuns: 4, LeaseDurationMs: time.Minute.Milliseconds()}
	recoveryStart, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: manualFinish.Job, ExpectedRevision: manualFinish.Job.Revision, RunID: "run-scenario-recovery", Trigger: AuditRunManual,
		OwnerID: "instance-before-restart", LeaseToken: "lease-before-restart", Now: recoveryStartedAt, Limits: recoveryLimits,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRunStart(ctx, recoveryStart, recoveryLimits.MaximumActiveRuns); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewAuditSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.LoadRecoveryState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("pending 重启恢复数量 = %d", len(entries))
	}
	reclaimedAt := recoveryStart.Lease.ExpiresAt
	resumeDecision, err := PlanAuditRecovery(entries[0], reclaimedAt)
	if err != nil {
		t.Fatal(err)
	}
	if resumeDecision.Action != AuditRecoveryResumePending {
		t.Fatalf("pending 重启恢复动作 = %#v", resumeDecision)
	}
	replacement := AuditRunLease{
		JobID: recoveryStart.Job.ID, RunID: recoveryStart.Run.ID, OwnerID: "instance-after-restart", Token: "lease-after-restart",
		AcquiredAt: reclaimedAt, ExpiresAt: reclaimedAt.Add(5 * time.Minute),
	}
	if err := store.ClaimRecoveryLease(ctx, replacement, reclaimedAt); err != nil {
		t.Fatal(err)
	}
	recoveredRunningAt := reclaimedAt.Add(time.Second)
	recoveredRunning, err := ActivateAuditRun(
		resumeDecision.Run,
		resumeDecision.Job,
		auditResolvedTargetsForJob(t, resumeDecision.Job, recoveredRunningAt),
		recoveredRunningAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRun(ctx, recoveredRunning, AuditRunPending, replacement, recoveredRunningAt); err != nil {
		t.Fatal(err)
	}
	recoveredRunning, err = RecordAuditRunUsage(recoveredRunning, AuditRunUsage{
		Requests: 1, InputTokens: 6, OutputTokens: 4, TotalTokens: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRun(ctx, recoveredRunning, AuditRunRunning, replacement, recoveredRunningAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewAuditSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err = store.LoadRecoveryState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("running 重启恢复数量 = %d", len(entries))
	}
	interruptedDecision, err := PlanAuditRecovery(entries[0], replacement.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if interruptedDecision.Action != AuditRecoveryFinalizeInterrupted || interruptedDecision.Run.Status != AuditRunPartial ||
		interruptedDecision.Run.StopReason != AuditRunStopInterrupted {
		t.Fatalf("running 重启恢复应以部分中断收口: %#v", interruptedDecision)
	}
	if err := store.CommitInterruptedRecovery(ctx, interruptedDecision, replacement.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	finalJob, found, err := store.GetJob(ctx, job.ID)
	if err != nil || !found || finalJob.Status != AuditJobEnabled || finalJob.Revision != recoveryStart.Run.JobRevision+2 {
		t.Fatalf("恢复后的任务状态: found=%v job=%#v err=%v", found, finalJob, err)
	}
	finalRun, found, err := store.GetRun(ctx, recoveryStart.Run.ID)
	if err != nil || !found || finalRun.Status != AuditRunPartial || finalRun.Usage.Requests != 1 || len(finalRun.Targets) != 2 {
		t.Fatalf("恢复后的运行状态: found=%v run=%#v err=%v", found, finalRun, err)
	}
}
