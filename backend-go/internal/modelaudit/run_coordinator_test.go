package modelaudit

import (
	"testing"
	"time"
)

func TestPrepareAuditRunStartCreatesPendingRunAndLease(t *testing.T) {
	job := auditJobFixture(t)
	now := job.Schedule.StartAt.Add(time.Hour)
	start, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "run-manual", Trigger: AuditRunManual,
		OwnerID: "instance-1", LeaseToken: "lease-1", Now: now, ActiveRuns: 0,
		Limits: AuditRunStartLimits{MaximumActiveRuns: 2, LeaseDurationMs: (5 * time.Minute).Milliseconds()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if start.Job.Status != AuditJobRunning || start.Job.Revision != job.Revision+1 || start.Run.Status != AuditRunPending ||
		start.Run.JobRevision != job.Revision || start.Run.Trigger != AuditRunManual || start.Run.ScheduledFor != nil ||
		start.Lease.JobID != job.ID || start.Lease.RunID != start.Run.ID || !start.Lease.ExpiresAt.After(now) {
		t.Fatalf("审计运行启动结果 = %#v", start)
	}
	if start.Job.Schedule != job.Schedule {
		t.Fatal("手动运行改变了任务调度")
	}
}

func TestPrepareScheduledAuditRunRequiresDueSlot(t *testing.T) {
	job := auditJobFixture(t)
	now := job.Schedule.StartAt.Add(time.Hour)
	future, err := NextAuditScheduleSlot(job.ID, job.Schedule, now)
	if err != nil {
		t.Fatal(err)
	}
	input := AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "run-scheduled", Trigger: AuditRunScheduled, ScheduledSlot: future,
		OwnerID: "instance-1", LeaseToken: "lease-1", Now: now,
		Limits: AuditRunStartLimits{MaximumActiveRuns: 2, LeaseDurationMs: (5 * time.Minute).Milliseconds()},
	}
	if _, err := PrepareAuditRunStart(input); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("未来槽位启动错误 = %v", err)
	}
	due, err := NextAuditScheduleSlot(job.ID, job.Schedule, job.Schedule.StartAt.Add(-time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	input.ScheduledSlot = due
	start, err := PrepareAuditRunStart(input)
	if err != nil {
		t.Fatal(err)
	}
	if start.Run.ScheduledFor == nil || !start.Run.ScheduledFor.Equal(due.ScheduledAt) {
		t.Fatalf("定时运行槽位 = %#v", start.Run.ScheduledFor)
	}
}

func TestAuditRunStartRejectsActiveLeaseAndGlobalLimit(t *testing.T) {
	job := auditJobFixture(t)
	now := job.Schedule.StartAt.Add(time.Hour)
	existing := AuditRunLease{
		JobID: job.ID, RunID: "run-existing", OwnerID: "instance-other", Token: "lease-existing",
		AcquiredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
	}
	input := AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "run-new", Trigger: AuditRunManual,
		OwnerID: "instance-1", LeaseToken: "lease-new", Now: now, ExistingLease: &existing,
		Limits: AuditRunStartLimits{MaximumActiveRuns: 2, LeaseDurationMs: (5 * time.Minute).Milliseconds()},
	}
	if _, err := PrepareAuditRunStart(input); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("活动租约重入错误 = %v", err)
	}

	input.ExistingLease = nil
	input.ActiveRuns = 2
	if _, err := PrepareAuditRunStart(input); ErrorCodeOf(err) != ErrorCodeConcurrencyLimited {
		t.Fatalf("全局并发上限错误 = %v", err)
	}
}

func TestExpiredAuditLeaseCanBeReplacedButNotRenewed(t *testing.T) {
	now := time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)
	expired := AuditRunLease{
		JobID: "job-1", RunID: "run-old", OwnerID: "instance-old", Token: "lease-old",
		AcquiredAt: now.Add(-10 * time.Minute), ExpiresAt: now,
	}
	candidate := AuditRunLease{
		JobID: "job-1", RunID: "run-new", OwnerID: "instance-new", Token: "lease-new",
		AcquiredAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}
	acquired, err := AcquireAuditRunLease(&expired, candidate, now)
	if err != nil {
		t.Fatal(err)
	}
	if acquired != candidate {
		t.Fatalf("替换后的租约 = %#v", acquired)
	}
	if _, err := RenewAuditRunLease(expired, expired.OwnerID, expired.Token, now, time.Minute.Milliseconds()); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("过期租约续期错误 = %v", err)
	}
}

func TestAuditLeaseRenewAndReleaseRequireMatchingOwnership(t *testing.T) {
	now := time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)
	lease := AuditRunLease{
		JobID: "job-1", RunID: "run-1", OwnerID: "instance-1", Token: "lease-1",
		AcquiredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
	}
	renewed, err := RenewAuditRunLease(lease, lease.OwnerID, lease.Token, now, (5 * time.Minute).Milliseconds())
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.ExpiresAt.After(lease.ExpiresAt) {
		t.Fatal("租约续期没有延长有效期")
	}
	if _, err := RenewAuditRunLease(lease, "other", lease.Token, now, (5 * time.Minute).Milliseconds()); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("错误 owner 续期错误 = %v", err)
	}
	if err := ValidateAuditRunLeaseRelease(lease, lease.RunID, lease.OwnerID, "other-token"); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("错误 token 释放错误 = %v", err)
	}
	if err := ValidateAuditRunLeaseRelease(lease, lease.RunID, lease.OwnerID, lease.Token); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareAuditRunFinishRestoresScheduleState(t *testing.T) {
	job := auditJobFixture(t)
	now := job.Schedule.StartAt.Add(time.Hour)
	start, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "run-finish", Trigger: AuditRunManual,
		OwnerID: "instance-1", LeaseToken: "lease-1", Now: now,
		Limits: AuditRunStartLimits{MaximumActiveRuns: 2, LeaseDurationMs: (5 * time.Minute).Milliseconds()},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := start.Run
	startedAt := now.Add(time.Second)
	finishedAt := startedAt.Add(time.Minute)
	run.Status = AuditRunCompleted
	run.StartedAt = &startedAt
	run.FinishedAt = &finishedAt
	run.Targets = auditRunFixture(t, AuditRunCompleted).Targets
	run.Targets[0].Resolved.CapturedAt = startedAt
	coordination, err := PrepareAuditRunFinish(start.Job, start.Job.Revision, run, start.Lease, finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if coordination.Job.Status != AuditJobEnabled || coordination.ReleaseToken != start.Lease.Token {
		t.Fatalf("运行完成协调 = %#v", coordination)
	}

	endAt := finishedAt
	start.Job.Schedule.EndAt = &endAt
	coordination, err = PrepareAuditRunFinish(start.Job, start.Job.Revision, run, start.Lease, finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if coordination.Job.Status != AuditJobExpired {
		t.Fatalf("到期运行完成后的任务状态 = %q", coordination.Job.Status)
	}
}
