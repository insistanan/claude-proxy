package modelaudit

import (
	"testing"
	"time"
)

func TestPlanAuditRecoveryWaitsOrResumesPendingByLeaseExpiry(t *testing.T) {
	job := auditJobFixture(t)
	now := job.Schedule.StartAt.Add(time.Hour)
	start, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "run-recovery-plan", Trigger: AuditRunManual,
		OwnerID: "instance-1", LeaseToken: "lease-1", Now: now,
		Limits: AuditRunStartLimits{MaximumActiveRuns: 2, LeaseDurationMs: time.Minute.Milliseconds()},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := AuditRecoveryEntry{Job: start.Job, Run: start.Run, Lease: &start.Lease}
	decision, err := PlanAuditRecovery(entry, now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != AuditRecoveryWait {
		t.Fatalf("未过期 pending 恢复动作 = %q", decision.Action)
	}
	decision, err = PlanAuditRecovery(entry, start.Lease.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != AuditRecoveryResumePending || decision.Run.Status != AuditRunPending {
		t.Fatalf("过期 pending 恢复动作 = %#v", decision)
	}
}

func TestPlanAuditRecoveryFinalizesAmbiguousRunningState(t *testing.T) {
	job, pending, targets, startedAt := auditPendingRunWithTargetsFixture(t)
	running, err := ActivateAuditRun(pending, job, targets, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	now := startedAt.Add(time.Minute)
	decision, err := PlanAuditRecovery(AuditRecoveryEntry{Job: job, Run: running}, now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != AuditRecoveryFinalizeInterrupted || decision.Run.Status != AuditRunFailed ||
		decision.Run.StopReason != AuditRunStopInterrupted || decision.Job.Status != AuditJobEnabled {
		t.Fatalf("无已保存请求的中断恢复 = %#v", decision)
	}

	running, err = RecordAuditRunUsage(running, AuditRunUsage{Requests: 1, InputTokens: 10, OutputTokens: 5, TotalTokens: 15})
	if err != nil {
		t.Fatal(err)
	}
	decision, err = PlanAuditRecovery(AuditRecoveryEntry{Job: job, Run: running}, now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Run.Status != AuditRunPartial || decision.Run.StopReason != AuditRunStopInterrupted || decision.Run.Usage.Requests != 1 {
		t.Fatalf("已有请求的中断恢复 = %#v", decision)
	}
}
