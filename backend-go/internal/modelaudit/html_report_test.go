package modelaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestSelfContainedAuditHTMLExportPersistsRedactedArtifactMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	store, err := NewAuditSQLiteStore(filepath.Join(t.TempDir(), "html-report.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	report := auditHTMLStoredReportFixture(t, ctx, store)
	renderer, err := NewSelfContainedAuditHTMLRenderer()
	if err != nil {
		t.Fatal(err)
	}
	management, err := NewAuditManagementService(store, nil, renderer)
	if err != nil {
		t.Fatal(err)
	}
	generatedAt := report.CreatedAt.Add(2 * time.Hour)
	management.now = func() time.Time { return generatedAt }
	management.newArtifactID = func() (string, error) { return "artifact-html-export", nil }

	router := gin.New()
	if err := RegisterRoutes(router.Group("/api"), &Service{}, management); err != nil {
		t.Fatal(err)
	}
	detailResponse := performAuditRequest(router, http.MethodGet, "/api/model-audit/reports/"+report.ID+"?samplePage=1&samplePageSize=10&strategyPage=1&strategyPageSize=10", nil)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("报告详情状态 = %d body=%s", detailResponse.Code, detailResponse.Body.String())
	}
	var detail AuditReportDetailResponse
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Result.Detail.Report.ID != report.ID || detail.Result.Detail.Run.ID != report.RunID {
		t.Fatalf("报告详情响应 = %#v", detail)
	}
	response := performAuditRequest(router, http.MethodGet, "/api/model-audit/reports/"+report.ID+"/export.html", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("导出状态 = %d body=%s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Fatalf("导出 Content-Type = %q", contentType)
	}
	if disposition := response.Header().Get("Content-Disposition"); disposition != `attachment; filename="report-html-export.html"` {
		t.Fatalf("导出 Content-Disposition = %q", disposition)
	}

	content := response.Body.Bytes()
	lower := strings.ToLower(string(content))
	for _, forbidden := range []string{"http://", "https://", "<script", "<link", "@import"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("自包含 HTML 含禁止引用 %q", forbidden)
		}
	}
	for _, required := range []string{
		"摘要", "身份与混用", "能力评分", "证据引用", "版本与脱敏", "限制声明",
		"api_key", "authorization", "reserved_answers", "unselected_input", "raw_evidence_payload", "failure_detail",
		"audit.target-report", "capability.aggregate.weighted", "capability.pack.standard",
	} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("HTML 缺少 %q", required)
		}
	}
	if strings.Contains(string(content), `<script>alert("audit")</script>`) ||
		!strings.Contains(string(content), `&lt;script&gt;alert(&#34;audit&#34;)&lt;/script&gt;`) {
		t.Fatalf("渠道名称未被 HTML 转义: %s", content)
	}

	artifact, found, err := store.GetArtifact(ctx, "artifact-html-export")
	if err != nil || !found {
		t.Fatalf("读取 HTML 制品元数据: found=%v err=%v", found, err)
	}
	digest := sha256.Sum256(content)
	if artifact.ReportID != report.ID || artifact.RunID != report.RunID || artifact.FileName != report.ID+".html" ||
		artifact.ContentSHA256 != hex.EncodeToString(digest[:]) || artifact.SizeBytes != int64(len(content)) ||
		!artifact.CreatedAt.Equal(generatedAt) {
		t.Fatalf("HTML 制品元数据 = %#v", artifact)
	}
}

func auditHTMLStoredReportFixture(t *testing.T, ctx context.Context, store *AuditSQLiteStore) AuditTargetReport {
	t.Helper()
	job := auditJobFixture(t)
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	startedAt := job.Schedule.StartAt.Add(time.Hour)
	limits := AuditRunStartLimits{MaximumActiveRuns: 2, LeaseDurationMs: (10 * time.Minute).Milliseconds()}
	start, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "run-html-export", Trigger: AuditRunManual,
		OwnerID: "instance-html-export", LeaseToken: "lease-html-export", Now: startedAt, Limits: limits,
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
	running, err = RecordAuditRunUsage(running, AuditRunUsage{Requests: 1, InputTokens: 1200, OutputTokens: 800, TotalTokens: 2000})
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

	identityConfig, identityInput := identityAggregationFixture(t)
	identity, err := AggregateIdentity(identityConfig, identityInput)
	if err != nil {
		t.Fatal(err)
	}
	capabilityConfig, capabilityInput := capabilityAggregationFixture(t)
	ordinal := 0
	for index, plan := range capabilityInput.Plan {
		for repeat := 0; repeat < plan.PlannedInstances; repeat++ {
			capabilityInput.Results = append(capabilityInput.Results, capabilityResultFixture(
				plan, capabilityInput.Package.Package.Tasks[index].Scorer, ordinal, CapabilityTaskScored, 1,
			))
			ordinal++
		}
	}
	capability, err := AggregateCapability(capabilityConfig, capabilityInput)
	if err != nil {
		t.Fatal(err)
	}
	reportedAt := finishedAt.Add(time.Second)
	target := completed.Targets[0]
	target.Resolved.ChannelName = `fixture <script>alert("audit")</script>`
	report := AuditTargetReport{
		Schema:            VersionedRef{ID: AuditTargetReportSchemaID, SemanticVersion: AuditTargetReportSchemaVersion, ImplementationVersion: "fixture-1"},
		ID:                "report-html-export",
		RunID:             completed.ID,
		JobID:             completed.JobID,
		JobRevision:       completed.JobRevision,
		JobSnapshotSHA256: completed.JobSnapshotSHA256,
		Target:            target,
		ResultStatus:      AuditReportComplete,
		RunStatus:         completed.Status,
		StopReason:        completed.StopReason,
		Usage:             completed.Usage,
		SampleCount:       identity.SampleCount + len(capabilityInput.Results),
		Identity:          &identity,
		Capability:        &capability,
		Evidence: []EvidenceReference{{
			ID: "evidence-html-export", Kind: "redacted_fixture", SHA256: testSignalDigest("html-export"), Redacted: true,
		}},
		FreshnessPolicy: AuditFreshnessPolicy{
			Ref:      VersionedRef{ID: "freshness.html", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
			MaxAgeMs: (7 * 24 * time.Hour).Milliseconds(),
		},
		CreatedAt: reportedAt,
	}
	if _, err := store.SaveTargetReport(ctx, report); err != nil {
		t.Fatal(err)
	}
	return report
}
