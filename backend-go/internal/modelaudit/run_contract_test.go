package modelaudit

import (
	"testing"
	"time"
)

func TestAuditRunLifecycleContracts(t *testing.T) {
	pending := auditRunFixture(t, AuditRunPending)
	if err := pending.Validate(); err != nil {
		t.Fatal(err)
	}

	running := auditRunFixture(t, AuditRunRunning)
	if err := running.Validate(); err != nil {
		t.Fatal(err)
	}

	completed := auditRunFixture(t, AuditRunCompleted)
	completed.Usage = AuditRunUsage{Requests: 1, InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	if err := completed.Validate(); err != nil {
		t.Fatal(err)
	}

	partial := completed
	partial.Status = AuditRunPartial
	partial.StopReason = AuditRunStopBudget
	if err := partial.Validate(); err != nil {
		t.Fatal(err)
	}
	partial.Failure = "不应存在"
	if err := partial.Validate(); err == nil {
		t.Fatal("预算停止的部分运行不应携带失败文本")
	}

	cancelled := completed
	cancelled.Status = AuditRunCancelled
	cancelled.StopReason = AuditRunStopCancelled
	cancelled.Usage = AuditRunUsage{}
	if err := cancelled.Validate(); err != nil {
		t.Fatal(err)
	}
	cancelled.Usage = AuditRunUsage{InputTokens: 1, TotalTokens: 1}
	if err := cancelled.Validate(); err == nil {
		t.Fatal("已有请求的取消运行应保存为 partial，而不是 cancelled")
	}

	failed := completed
	failed.Status = AuditRunFailed
	failed.StopReason = AuditRunStopFailure
	failed.Failure = "目标解析失败"
	if err := failed.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAuditRunTargetPersistsDefaultResolutionOrExplicitFailure(t *testing.T) {
	run := auditRunFixture(t, AuditRunRunning)
	resolved := run.Targets[0]
	if resolved.Requested.Model != "" || resolved.Resolved == nil || resolved.Resolved.DefaultModel != "gpt-5.6-sol" ||
		resolved.Resolved.ResolvedModel != "gpt-5.6-sol" {
		t.Fatalf("默认模型解析快照 = %#v", resolved)
	}

	failed := resolved
	failed.Resolved = nil
	failed.ResolutionError = "渠道没有默认模型"
	run.Targets[0] = failed
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}
	failed.ResolutionError = ""
	if err := failed.Validate(); err == nil {
		t.Fatal("目标既无解析结果也无错误时应被拒绝")
	}
}

func TestAuditRunRejectsBudgetOverflowAndInvalidTerminalState(t *testing.T) {
	run := auditRunFixture(t, AuditRunCompleted)
	run.Usage = AuditRunUsage{
		Requests: run.Budget.MaxRequests + 1,
	}
	if err := run.Validate(); err == nil {
		t.Fatal("运行用量超过冻结预算时应被拒绝")
	}

	run = auditRunFixture(t, AuditRunPartial)
	run.StopReason = AuditRunStopFailure
	if err := run.Validate(); err == nil {
		t.Fatal("部分运行使用失败停止原因时应被拒绝")
	}
}

func TestAuditLeaseAndStoredPayloadContracts(t *testing.T) {
	acquiredAt := time.Date(2026, 8, 11, 16, 5, 0, 0, time.UTC)
	lease := AuditRunLease{
		JobID: "job-1", RunID: "run-1", OwnerID: "instance-1", Token: "lease-token",
		AcquiredAt: acquiredAt, ExpiresAt: acquiredAt.Add(5 * time.Minute),
	}
	if err := lease.Validate(); err != nil {
		t.Fatal(err)
	}
	lease.ExpiresAt = acquiredAt
	if err := lease.Validate(); err == nil {
		t.Fatal("无有效期的租约应被拒绝")
	}

	payload, err := NewAuditStoredPayload(
		"sample-1", "run-1", "target-1", AuditStoredSample,
		VersionedRef{ID: "audit.sample", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		[]byte(`{ "status": "completed", "score": 1 }`), acquiredAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload.Payload) != `{"score":1,"status":"completed"}` || !validSHA256(payload.SHA256) {
		t.Fatalf("规范化持久化载荷 = %#v", payload)
	}
	payload.Payload = []byte(`{"score":0,"status":"completed"}`)
	if err := payload.Validate(); err == nil {
		t.Fatal("持久化载荷被篡改后应被拒绝")
	}
}

func TestAuditJobSnapshotHashChangesWithRevision(t *testing.T) {
	job := auditJobFixture(t)
	first, err := AuditJobSnapshotSHA256(job)
	if err != nil {
		t.Fatal(err)
	}
	job.Revision++
	job.UpdatedAt = job.UpdatedAt.Add(time.Second)
	second, err := AuditJobSnapshotSHA256(job)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("任务版本变化后快照哈希不应保持不变")
	}
}

func auditRunFixture(t *testing.T, status AuditRunStatus) AuditRun {
	t.Helper()
	job := auditJobFixture(t)
	hash, err := AuditJobSnapshotSHA256(job)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := job.CreatedAt.Add(5 * time.Minute)
	startedAt := createdAt.Add(time.Second)
	finishedAt := startedAt.Add(time.Minute)
	resolved := TargetSnapshot{
		ChannelID: job.Targets[0].ChannelID, ChannelKind: job.Targets[0].ChannelKind, ChannelName: "Responses",
		ChannelStatus: "enabled", ServiceType: "responses", Protocol: job.Targets[0].Protocol, WireProtocol: ProtocolResponses,
		RequestedModel: "", DefaultModel: "gpt-5.6-sol", ResolvedModel: "gpt-5.6-sol",
		Thinking: job.Targets[0].Thinking, RequestProfile: job.Targets[0].RequestProfile, CapturedAt: startedAt,
	}
	resolved.ThinkingMap, err = ResolveThinking(resolved.WireProtocol, resolved.Thinking)
	if err != nil {
		t.Fatal(err)
	}
	run := AuditRun{
		ID: "run-1", JobID: job.ID, JobRevision: job.Revision, JobSnapshotSHA256: hash,
		Trigger: AuditRunManual, Status: status, Budget: job.Budget, CreatedAt: createdAt,
	}
	if status != AuditRunPending {
		run.StartedAt = &startedAt
		run.Targets = []AuditRunTargetSnapshot{{TargetID: job.Targets[0].ID, Requested: job.Targets[0], Resolved: &resolved}}
	}
	if status.Terminal() {
		run.FinishedAt = &finishedAt
	}
	switch status {
	case AuditRunPartial:
		run.StopReason = AuditRunStopBudget
	case AuditRunFailed:
		run.StopReason = AuditRunStopFailure
		run.Failure = "fixture failure"
	case AuditRunCancelled:
		run.StopReason = AuditRunStopCancelled
	}
	return run
}
