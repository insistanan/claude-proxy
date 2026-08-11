package modelaudit

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditPresentationStoreProjectsHistoricalSummaryAndPagedDetail(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "presentation.db")
	store, err := NewAuditSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	job := auditJobFixture(t)
	job.Targets = append(job.Targets, AuditJobTarget{
		ID: "target-history-2", ChannelID: "channel-chat-history", ChannelKind: ChannelKindChat, Protocol: ProtocolChat,
		Model: "gpt-5.6-sol", Thinking: ThinkingMedium, RequestProfile: "chat.standard.v1",
	})
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	startedAt := job.Schedule.StartAt.Add(time.Hour)
	limits := AuditRunStartLimits{MaximumActiveRuns: 4, LeaseDurationMs: (10 * time.Minute).Milliseconds()}
	start, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "run-presentation-history", Trigger: AuditRunManual,
		OwnerID: "instance-presentation", LeaseToken: "lease-presentation-history", Now: startedAt, Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRunStart(ctx, start, limits.MaximumActiveRuns); err != nil {
		t.Fatal(err)
	}
	runningAt := startedAt.Add(time.Second)
	running, err := ActivateAuditRun(start.Run, start.Job, auditResolvedTargetsForJob(t, start.Job, runningAt), runningAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRun(ctx, running, AuditRunPending, start.Lease, runningAt); err != nil {
		t.Fatal(err)
	}
	running, err = RecordAuditRunUsage(running, AuditRunUsage{Requests: 1, InputTokens: 12, OutputTokens: 8, TotalTokens: 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRun(ctx, running, AuditRunRunning, start.Lease, runningAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	finishedAt := startedAt.Add(time.Minute)
	completed, err := CompleteAuditRun(running, finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	finish, err := PrepareAuditRunFinish(start.Job, start.Job.Revision, completed, start.Lease, finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRunFinish(ctx, finish, completed, start.Lease, start.Job.Revision); err != nil {
		t.Fatal(err)
	}

	targetID := completed.Targets[0].TargetID
	evidenceSchema := VersionedRef{ID: "audit.evidence", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"}
	sampleOne := presentationPayloadFixture(t, "sample-presentation-1", completed.ID, targetID, AuditStoredSample, evidenceSchema, `{"sequence":1}`, finishedAt.Add(time.Second))
	sampleTwo := presentationPayloadFixture(t, "sample-presentation-2", completed.ID, targetID, AuditStoredSample, evidenceSchema, `{"sequence":2}`, finishedAt.Add(2*time.Second))
	strategyResult := presentationPayloadFixture(t, "strategy-presentation-1", completed.ID, targetID, AuditStoredStrategyResult, evidenceSchema, `{"status":"completed"}`, finishedAt.Add(3*time.Second))
	for _, payload := range []AuditStoredPayload{sampleOne, sampleTwo, strategyResult} {
		if err := store.SavePayload(ctx, payload); err != nil {
			t.Fatal(err)
		}
	}

	identityConfig, identityInput := identityAggregationFixture(t)
	identity, err := AggregateIdentity(identityConfig, identityInput)
	if err != nil {
		t.Fatal(err)
	}
	reportedAt := finishedAt.Add(4 * time.Second)
	report := AuditTargetReport{
		Schema:            VersionedRef{ID: AuditTargetReportSchemaID, SemanticVersion: AuditTargetReportSchemaVersion, ImplementationVersion: "fixture-1"},
		ID:                "report-presentation-history",
		RunID:             completed.ID,
		JobID:             completed.JobID,
		JobRevision:       completed.JobRevision,
		JobSnapshotSHA256: completed.JobSnapshotSHA256,
		Target:            completed.Targets[0],
		ResultStatus:      AuditReportComplete,
		RunStatus:         completed.Status,
		StopReason:        completed.StopReason,
		Usage:             completed.Usage,
		SampleCount:       identity.SampleCount,
		Identity:          &identity,
		Evidence: []EvidenceReference{
			{ID: sampleOne.ID, Kind: string(sampleOne.Kind), SHA256: sampleOne.SHA256, Redacted: true},
			{ID: sampleTwo.ID, Kind: string(sampleTwo.Kind), SHA256: sampleTwo.SHA256, Redacted: true},
		},
		FreshnessPolicy: AuditFreshnessPolicy{
			Ref:      VersionedRef{ID: "freshness.presentation", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
			MaxAgeMs: (7 * 24 * time.Hour).Milliseconds(),
		},
		CreatedAt: reportedAt,
	}
	if _, err := store.SaveTargetReport(ctx, report); err != nil {
		t.Fatal(err)
	}
	summary, err := store.GetLatestChannelSummary(ctx, job.Targets[0].ChannelID, job.Targets[0].ChannelKind, reportedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if summary.PrimaryStatus != AuditSummaryComplete || summary.ReportID != report.ID || summary.ActiveRunCount != 0 || summary.Identity == nil {
		t.Fatalf("最新渠道摘要 = %#v", summary)
	}

	activeAt := reportedAt.Add(2 * time.Hour)
	activeStart, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: finish.Job, ExpectedRevision: finish.Job.Revision, RunID: "run-presentation-active", Trigger: AuditRunManual,
		OwnerID: "instance-presentation", LeaseToken: "lease-presentation-active", Now: activeAt, Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRunStart(ctx, activeStart, limits.MaximumActiveRuns); err != nil {
		t.Fatal(err)
	}
	summary, err = store.GetLatestChannelSummary(ctx, job.Targets[0].ChannelID, job.Targets[0].ChannelKind, activeAt)
	if err != nil {
		t.Fatal(err)
	}
	if summary.PrimaryStatus != AuditSummaryRunning || summary.ActiveRunID != activeStart.Run.ID || summary.ActiveRunCount != 1 || summary.ReportID != report.ID {
		t.Fatalf("运行中渠道摘要 = %#v", summary)
	}
	cancelledAt := activeAt.Add(time.Minute)
	cancelled, err := StopAuditRun(activeStart.Run, AuditRunStopCancelled, "", cancelledAt)
	if err != nil {
		t.Fatal(err)
	}
	activeFinish, err := PrepareAuditRunFinish(activeStart.Job, activeStart.Job.Revision, cancelled, activeStart.Lease, cancelledAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRunFinish(ctx, activeFinish, cancelled, activeStart.Lease, activeStart.Job.Revision); err != nil {
		t.Fatal(err)
	}

	definition := auditJobDefinitionFromFixture(activeFinish.Job)
	definition.Targets = []AuditJobTarget{activeFinish.Job.Targets[1]}
	updated, err := UpdateAuditJob(activeFinish.Job, activeFinish.Job.Revision, definition, cancelledAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateJob(ctx, updated, activeFinish.Job.Revision); err != nil {
		t.Fatal(err)
	}
	summary, err = store.GetLatestChannelSummary(ctx, job.Targets[0].ChannelID, job.Targets[0].ChannelKind, cancelledAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if summary.PrimaryStatus != AuditSummaryComplete || summary.ReportID != report.ID || summary.ActiveRunCount != 0 {
		t.Fatalf("移除任务目标后的历史摘要 = %#v", summary)
	}

	detail, found, err := store.GetReportDetail(ctx, report.ID, AuditReportDetailOptions{
		Now: cancelledAt.Add(2 * time.Minute), Samples: AuditPageRequest{Page: 1, PageSize: 1},
		StrategyResults: AuditPageRequest{Page: 1, PageSize: 1},
	})
	if err != nil || !found {
		t.Fatalf("读取审计详情: found=%v err=%v", found, err)
	}
	if detail.Samples.Total != 2 || len(detail.Samples.Evidence) != 1 || detail.StrategyResults.Total != 1 ||
		len(detail.StrategyResults.Evidence) != 1 || detail.Detail.Report.ID != report.ID {
		t.Fatalf("审计详情分页 = %#v", detail)
	}
	secondSamplePage, err := store.ListEvidenceMetadata(ctx, AuditEvidenceListOptions{
		Page: AuditPageRequest{Page: 2, PageSize: 1}, RunID: report.RunID, TargetID: targetID, Kind: AuditStoredSample,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondSamplePage.Evidence) != 1 || secondSamplePage.Evidence[0].ID != sampleTwo.ID {
		t.Fatalf("第二页样本证据 = %#v", secondSamplePage)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewAuditSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	summary, err = store.GetLatestChannelSummary(ctx, job.Targets[0].ChannelID, job.Targets[0].ChannelKind, cancelledAt.Add(3*time.Minute))
	if err != nil || summary.ReportID != report.ID {
		t.Fatalf("重开数据库后的渠道摘要 = %#v err=%v", summary, err)
	}
}

func TestAuditSQLiteSchemaMigratesPresentationIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	store, err := NewAuditSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE audit_report_index`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE audit_schema_meta SET value = '1' WHERE key = 'schema_version'`); err != nil {
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
	var version string
	if err := store.db.QueryRow(`SELECT value FROM audit_schema_meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "2" {
		t.Fatalf("迁移后的 Schema 版本 = %q", version)
	}
	var indexTable string
	if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'audit_report_index'`).Scan(&indexTable); err != nil {
		t.Fatal(err)
	}
}

func presentationPayloadFixture(
	t *testing.T,
	id string,
	runID string,
	targetID string,
	kind AuditStoredPayloadKind,
	schema VersionedRef,
	payload string,
	createdAt time.Time,
) AuditStoredPayload {
	t.Helper()
	result, err := NewAuditStoredPayload(id, runID, targetID, kind, schema, []byte(payload), createdAt)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
