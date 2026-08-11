package modelaudit

import (
	"testing"
	"time"
)

func TestAuditFreshnessAndSummaryStatusBoundaries(t *testing.T) {
	policy := AuditFreshnessPolicy{
		Ref:      VersionedRef{ID: "freshness.standard", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
		MaxAgeMs: (24 * time.Hour).Milliseconds(),
	}
	reportedAt := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	fresh, err := EvaluateAuditFreshness(policy, reportedAt, reportedAt.Add(24*time.Hour-time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	stale, err := EvaluateAuditFreshness(policy, reportedAt, reportedAt.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if fresh.State != AuditFreshnessFresh || stale.State != AuditFreshnessStale {
		t.Fatalf("新鲜度边界: fresh=%#v stale=%#v", fresh, stale)
	}
	if _, err := EvaluateAuditFreshness(policy, reportedAt, reportedAt.Add(-time.Second)); err == nil {
		t.Fatal("早于报告时间的新鲜度评估应被拒绝")
	}

	tests := []struct {
		name        string
		activeRunID string
		hasReport   bool
		result      AuditReportResultStatus
		freshness   AuditFreshnessState
		expected    AuditSummaryStatus
	}{
		{name: "not detected", freshness: AuditFreshnessUnavailable, expected: AuditSummaryNotDetected},
		{name: "running overrides failed", activeRunID: "run-active-summary", hasReport: true, result: AuditReportFailed, freshness: AuditFreshnessStale, expected: AuditSummaryRunning},
		{name: "failed", hasReport: true, result: AuditReportFailed, freshness: AuditFreshnessStale, expected: AuditSummaryFailed},
		{name: "partial", hasReport: true, result: AuditReportPartial, freshness: AuditFreshnessStale, expected: AuditSummaryPartial},
		{name: "unsupported", hasReport: true, result: AuditReportUnsupported, freshness: AuditFreshnessStale, expected: AuditSummaryUnsupported},
		{name: "insufficient", hasReport: true, result: AuditReportInsufficientEvidence, freshness: AuditFreshnessStale, expected: AuditSummaryInsufficientEvidence},
		{name: "stale complete", hasReport: true, result: AuditReportComplete, freshness: AuditFreshnessStale, expected: AuditSummaryStale},
		{name: "fresh complete", hasReport: true, result: AuditReportComplete, freshness: AuditFreshnessFresh, expected: AuditSummaryComplete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := ResolveAuditSummaryStatus(test.activeRunID, test.hasReport, test.result, test.freshness)
			if err != nil {
				t.Fatal(err)
			}
			if actual != test.expected {
				t.Fatalf("主状态 = %q，期望 %q", actual, test.expected)
			}
		})
	}
}

func TestAuditPresentationContractsFreezeSafeReportDTOs(t *testing.T) {
	identityConfig, identityInput := identityAggregationFixture(t)
	identity, err := AggregateIdentity(identityConfig, identityInput)
	if err != nil {
		t.Fatal(err)
	}
	identitySummary, err := SummarizeAuditIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	job, pending, targets, startedAt := auditPendingRunWithTargetsFixture(t)
	running, err := ActivateAuditRun(pending, job, targets, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	running, err = RecordAuditRunUsage(running, AuditRunUsage{Requests: 1, InputTokens: 12, OutputTokens: 8, TotalTokens: 20})
	if err != nil {
		t.Fatal(err)
	}
	finishedAt := startedAt.Add(time.Minute)
	completed, err := CompleteAuditRun(running, finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	policy := AuditFreshnessPolicy{
		Ref:      VersionedRef{ID: "freshness.identity.standard", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
		MaxAgeMs: (7 * 24 * time.Hour).Milliseconds(),
	}
	reportedAt := finishedAt.Add(time.Second)
	report := AuditTargetReport{
		Schema: VersionedRef{ID: "audit.target-report", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
		ID:     "report-presentation", RunID: completed.ID, JobID: completed.JobID, JobRevision: completed.JobRevision,
		JobSnapshotSHA256: completed.JobSnapshotSHA256, Target: completed.Targets[0], ResultStatus: AuditReportComplete,
		RunStatus: completed.Status, StopReason: completed.StopReason, Usage: completed.Usage, SampleCount: identity.SampleCount, Identity: &identity,
		Evidence:        []EvidenceReference{{ID: "evidence-presentation", Kind: "identity_signal", SHA256: testSignalDigest("presentation"), Redacted: true}},
		FreshnessPolicy: policy, CreatedAt: reportedAt,
	}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	freshness, err := EvaluateAuditFreshness(policy, reportedAt, reportedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	primary, err := ResolveAuditSummaryStatus("", true, report.ResultStatus, freshness.State)
	if err != nil {
		t.Fatal(err)
	}
	summary := AuditChannelSummary{
		ChannelID: report.Target.Requested.ChannelID, ChannelKind: report.Target.Requested.ChannelKind,
		PrimaryStatus: primary, ReportID: report.ID, RunID: report.RunID, ResultStatus: report.ResultStatus,
		Protocol: report.Target.Requested.Protocol, ResolvedModel: report.Target.Resolved.ResolvedModel,
		Thinking: report.Target.Requested.Thinking, SampleCount: identity.SampleCount, Freshness: &freshness,
		Identity: &identitySummary, ReportedAt: cloneTimePointer(&reportedAt),
	}
	if err := summary.Validate(); err != nil {
		t.Fatal(err)
	}
	runView, err := NewAuditRunPresentation(completed)
	if err != nil {
		t.Fatal(err)
	}
	detail := AuditReportDetail{
		Schema:  VersionedRef{ID: "audit.report-detail", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
		Summary: summary,
		Run:     runView,
		Report:  report,
	}
	if err := detail.Validate(); err != nil {
		t.Fatal(err)
	}
	export := AuditHTMLReportDTO{
		Schema:      VersionedRef{ID: "audit.html-report", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
		Detail:      detail,
		Redaction:   DefaultAuditRedactionManifest(),
		Limitations: []string{"黑盒行为证据不是密码学身份证明。", "过期结果不代表渠道当前状态。"},
		GeneratedAt: reportedAt.Add(2 * time.Hour),
	}
	if err := export.Validate(); err != nil {
		t.Fatal(err)
	}

	tampered := export
	tampered.Redaction.Excluded = append([]AuditSensitiveFieldClass(nil), tampered.Redaction.Excluded[:len(tampered.Redaction.Excluded)-1]...)
	if err := tampered.Validate(); err == nil {
		t.Fatal("HTML 导出不能放宽默认脱敏清单")
	}
	staleFreshness, err := EvaluateAuditFreshness(policy, reportedAt, reportedAt.Add(8*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	staleSummary := summary
	staleSummary.Freshness = &staleFreshness
	staleSummary.PrimaryStatus = AuditSummaryStale
	if err := staleSummary.Validate(); err != nil {
		t.Fatal(err)
	}
}
