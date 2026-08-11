package modelaudit

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditSQLiteStorePersistsAtomicLifecycleAndHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := NewAuditSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	job := auditJobFixture(t)
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateJob(ctx, job); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("重复任务错误 = %v", err)
	}
	page, err := store.ListJobs(ctx, AuditJobListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Page != 1 || page.PageSize != AuditDefaultPageSize || len(page.Jobs) != 1 {
		t.Fatalf("审计任务分页 = %#v", page)
	}

	definition := auditJobDefinitionFromFixture(job)
	definition.Name = "updated persisted audit"
	updated, err := UpdateAuditJob(job, job.Revision, definition, job.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateJob(ctx, updated, job.Revision); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateJob(ctx, updated, job.Revision); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("持久化任务 CAS 错误 = %v", err)
	}
	job = updated

	now := job.Schedule.StartAt.Add(time.Hour)
	start, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "run-sqlite", Trigger: AuditRunManual,
		OwnerID: "instance-1", LeaseToken: "lease-sqlite", Now: now,
		Limits: AuditRunStartLimits{MaximumActiveRuns: 2, LeaseDurationMs: (5 * time.Minute).Milliseconds()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRunStart(ctx, start, 2); err != nil {
		t.Fatal(err)
	}
	storedJob, found, err := store.GetJob(ctx, job.ID)
	if err != nil || !found || storedJob.Status != AuditJobRunning {
		t.Fatalf("原子启动后的任务: found=%v job=%#v err=%v", found, storedJob, err)
	}
	storedRun, found, err := store.GetRun(ctx, start.Run.ID)
	if err != nil || !found || storedRun.Status != AuditRunPending {
		t.Fatalf("原子启动后的运行: found=%v run=%#v err=%v", found, storedRun, err)
	}
	lease, found, err := store.GetLease(ctx, job.ID)
	if err != nil || !found || lease.Token != start.Lease.Token {
		t.Fatalf("原子启动后的租约: found=%v lease=%#v err=%v", found, lease, err)
	}

	startedAt := now.Add(time.Second)
	targets := auditResolvedTargetsForJob(t, job, startedAt)
	running, err := ActivateAuditRun(storedRun, storedJob, targets, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRun(ctx, running, AuditRunPending, lease, startedAt); err != nil {
		t.Fatal(err)
	}
	running, err = RecordAuditRunUsage(running, AuditRunUsage{Requests: 1, InputTokens: 10, OutputTokens: 5, TotalTokens: 15})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRun(ctx, running, AuditRunRunning, lease, startedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	createdAt := startedAt.Add(2 * time.Second)
	sample, err := NewAuditStoredPayload(
		"sample-sqlite", running.ID, job.Targets[0].ID, AuditStoredSample,
		VersionedRef{ID: "audit.sample", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		[]byte(`{"status":"completed"}`), createdAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SavePayload(ctx, sample); err != nil {
		t.Fatal(err)
	}
	report, err := NewAuditStoredPayload(
		"report-sqlite", running.ID, job.Targets[0].ID, AuditStoredReport,
		VersionedRef{ID: "audit.report", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		[]byte(`{"status":"complete"}`), createdAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SavePayload(ctx, report); err != nil {
		t.Fatal(err)
	}
	artifact := AuditArtifact{
		ID: "artifact-sqlite", RunID: running.ID, ReportID: report.ID, FileName: "report.html",
		ContentSHA256: testSignalDigest("html"), SizeBytes: 128, CreatedAt: createdAt.Add(2 * time.Second),
	}
	if err := store.SaveArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	payloadPage, err := store.ListPayloads(ctx, AuditPayloadListOptions{RunID: running.ID, Kind: AuditStoredSample})
	if err != nil || payloadPage.Total != 1 || len(payloadPage.Payloads) != 1 || payloadPage.Payloads[0].SHA256 != sample.SHA256 {
		t.Fatalf("审计载荷分页 = %#v err=%v", payloadPage, err)
	}

	finishedAt := startedAt.Add(time.Minute)
	completed, err := CompleteAuditRun(running, finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	coordination, err := PrepareAuditRunFinish(storedJob, storedJob.Revision, completed, lease, finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRunFinish(ctx, coordination, completed, lease, storedJob.Revision); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetLease(ctx, job.ID); err != nil || found {
		t.Fatalf("终态提交后租约仍存在: found=%v err=%v", found, err)
	}
	runPage, err := store.ListRuns(ctx, AuditRunListOptions{JobID: job.ID, Status: AuditRunCompleted})
	if err != nil || runPage.Total != 1 || len(runPage.Runs) != 1 {
		t.Fatalf("审计运行分页 = %#v err=%v", runPage, err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewAuditSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if persisted, found, err := store.GetJob(ctx, job.ID); err != nil || !found || persisted.Status != AuditJobEnabled {
		t.Fatalf("重开数据库后的任务: found=%v job=%#v err=%v", found, persisted, err)
	}
	deleted, err := store.DeleteTerminalRunsBefore(ctx, finishedAt.Add(time.Second), 100)
	if err != nil || deleted != 1 {
		t.Fatalf("清理终态运行: deleted=%d err=%v", deleted, err)
	}
	if _, found, err := store.GetRun(ctx, completed.ID); err != nil || found {
		t.Fatalf("清理后运行仍存在: found=%v err=%v", found, err)
	}
	if _, found, err := store.GetPayload(ctx, AuditStoredSample, sample.ID); err != nil || found {
		t.Fatalf("清理后样本仍存在: found=%v err=%v", found, err)
	}
	if _, found, err := store.GetArtifact(ctx, artifact.ID); err != nil || found {
		t.Fatalf("清理后制品仍存在: found=%v err=%v", found, err)
	}
	if _, found, err := store.GetJob(ctx, job.ID); err != nil || !found {
		t.Fatalf("清理历史不应删除任务: found=%v err=%v", found, err)
	}
}

func TestAuditSQLiteStoreLoadsAndReclaimsRecoveryState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recovery.db")
	store, err := NewAuditSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	job := auditJobFixture(t)
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	now := job.Schedule.StartAt.Add(time.Hour)
	start, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "run-recovery", Trigger: AuditRunManual,
		OwnerID: "instance-old", LeaseToken: "lease-old", Now: now,
		Limits: AuditRunStartLimits{MaximumActiveRuns: 2, LeaseDurationMs: time.Minute.Milliseconds()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRunStart(ctx, start, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewAuditSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	entries, err := store.LoadRecoveryState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Run.ID != start.Run.ID || entries[0].Lease == nil || entries[0].Lease.Token != start.Lease.Token {
		t.Fatalf("重启恢复状态 = %#v", entries)
	}
	activeLeaseTime := start.Lease.ExpiresAt.Add(-time.Second)
	activeLeaseReplacement := AuditRunLease{
		JobID: job.ID, RunID: start.Run.ID, OwnerID: "instance-early", Token: "lease-early",
		AcquiredAt: activeLeaseTime, ExpiresAt: activeLeaseTime.Add(5 * time.Minute),
	}
	if err := store.ClaimRecoveryLease(ctx, activeLeaseReplacement, activeLeaseTime); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("未过期租约被提前回收: %v", err)
	}
	reclaimedAt := start.Lease.ExpiresAt
	replacement := AuditRunLease{
		JobID: job.ID, RunID: start.Run.ID, OwnerID: "instance-new", Token: "lease-new",
		AcquiredAt: reclaimedAt, ExpiresAt: reclaimedAt.Add(5 * time.Minute),
	}
	if err := store.ClaimRecoveryLease(ctx, replacement, reclaimedAt); err != nil {
		t.Fatal(err)
	}
	lease, found, err := store.GetLease(ctx, job.ID)
	if err != nil || !found || lease.Token != replacement.Token || lease.OwnerID != replacement.OwnerID {
		t.Fatalf("回收后的租约: found=%v lease=%#v err=%v", found, lease, err)
	}
	startedAt := reclaimedAt.Add(time.Second)
	targets := auditResolvedTargetsForJob(t, entries[0].Job, startedAt)
	running, err := ActivateAuditRun(entries[0].Run, entries[0].Job, targets, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRun(ctx, running, AuditRunPending, replacement, startedAt); err != nil {
		t.Fatal(err)
	}
	running, err = RecordAuditRunUsage(running, AuditRunUsage{Requests: 1, InputTokens: 10, OutputTokens: 5, TotalTokens: 15})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRun(ctx, running, AuditRunRunning, replacement, startedAt.Add(time.Second)); err != nil {
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
	if len(entries) != 1 || entries[0].Run.Status != AuditRunRunning {
		t.Fatalf("运行中重启恢复状态 = %#v", entries)
	}
	runningReplacementTime := replacement.ExpiresAt
	runningReplacement := AuditRunLease{
		JobID: job.ID, RunID: running.ID, OwnerID: "instance-replay", Token: "lease-replay",
		AcquiredAt: runningReplacementTime, ExpiresAt: runningReplacementTime.Add(5 * time.Minute),
	}
	if err := store.ClaimRecoveryLease(ctx, runningReplacement, runningReplacementTime); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("running 运行不应被重新认领: %v", err)
	}
	decision, err := PlanAuditRecovery(entries[0], replacement.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Run.Status != AuditRunPartial || decision.Action != AuditRecoveryFinalizeInterrupted {
		t.Fatalf("运行中断恢复决策 = %#v", decision)
	}
	if err := store.CommitInterruptedRecovery(ctx, decision, replacement.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	storedRun, found, err := store.GetRun(ctx, running.ID)
	if err != nil || !found || storedRun.Status != AuditRunPartial || storedRun.StopReason != AuditRunStopInterrupted {
		t.Fatalf("中断恢复后的运行: found=%v run=%#v err=%v", found, storedRun, err)
	}
	storedJob, found, err := store.GetJob(ctx, job.ID)
	if err != nil || !found || storedJob.Status != AuditJobEnabled {
		t.Fatalf("中断恢复后的任务: found=%v job=%#v err=%v", found, storedJob, err)
	}
}

func auditResolvedTargetsForJob(t *testing.T, job AuditJob, capturedAt time.Time) []AuditRunTargetSnapshot {
	t.Helper()
	result := make([]AuditRunTargetSnapshot, len(job.Targets))
	for index, requested := range job.Targets {
		resolved := TargetSnapshot{
			ChannelID: requested.ChannelID, ChannelKind: requested.ChannelKind, ChannelName: "fixture", ChannelStatus: "enabled",
			ServiceType: string(requested.Protocol), Protocol: requested.Protocol, WireProtocol: requested.Protocol,
			RequestedModel: requested.Model, DefaultModel: "gpt-5.6-sol", ResolvedModel: "gpt-5.6-sol",
			Thinking: requested.Thinking, RequestProfile: requested.RequestProfile, CapturedAt: capturedAt,
		}
		if requested.Model != "" {
			resolved.ResolvedModel = requested.Model
		}
		var err error
		resolved.ThinkingMap, err = ResolveThinking(resolved.WireProtocol, resolved.Thinking)
		if err != nil {
			t.Fatal(err)
		}
		result[index] = AuditRunTargetSnapshot{TargetID: requested.ID, Requested: requested, Resolved: &resolved}
	}
	return result
}
