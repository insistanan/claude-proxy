package modelaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditFrontendJSONContractExposesFrozenMetadataOnly(t *testing.T) {
	ctx := context.Background()
	store, err := NewAuditSQLiteStore(filepath.Join(t.TempDir(), "frontend-contract.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	report := auditHTMLStoredReportFixture(t, ctx, store)
	schema := VersionedRef{ID: "audit.frontend-evidence", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"}
	for _, payload := range []AuditStoredPayload{
		presentationPayloadFixture(t, "sample-frontend-contract", report.RunID, report.Target.TargetID, AuditStoredSample, schema, `{"secret":"sample-must-not-leak"}`, report.CreatedAt),
		presentationPayloadFixture(t, "strategy-frontend-contract", report.RunID, report.Target.TargetID, AuditStoredStrategyResult, schema, `{"secret":"strategy-must-not-leak"}`, report.CreatedAt),
	} {
		if err := store.SavePayload(ctx, payload); err != nil {
			t.Fatal(err)
		}
	}
	management, err := NewAuditManagementService(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	management.now = func() time.Time { return report.CreatedAt.Add(time.Hour) }
	detail, err := management.GetReportDetail(ctx, report.ID, AuditPageRequest{Page: 1, PageSize: 10}, AuditPageRequest{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(AuditReportDetailResponse{Result: detail})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{[]byte(`"failure":`), []byte("sample-must-not-leak"), []byte("strategy-must-not-leak")} {
		if bytes.Contains(encoded, forbidden) {
			t.Fatalf("前端详情 JSON 泄露禁止字段或载荷 %q", forbidden)
		}
	}

	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	paths := [][]string{
		{"result", "detail", "schema", "semanticVersion"},
		{"result", "detail", "summary", "primaryStatus"},
		{"result", "detail", "summary", "identity", "confidence"},
		{"result", "detail", "summary", "capability", "indexInterval", "level"},
		{"result", "detail", "summary", "capability", "indexInterval", "resamples"},
		{"result", "detail", "report", "target", "requested", "channelId"},
		{"result", "detail", "report", "target", "resolved", "resolvedModel"},
		{"result", "detail", "report", "identity", "signals"},
		{"result", "detail", "report", "capability", "dimensions"},
		{"result", "detail", "report", "capability", "packageSha256"},
		{"result", "detail", "run", "failureHidden"},
		{"result", "samples", "evidence"},
		{"result", "samples", "pageSize"},
		{"result", "strategyResults", "evidence"},
	}
	for _, path := range paths {
		if value, found := auditJSONPath(document, path...); !found || value == nil {
			t.Fatalf("前端详情 JSON 缺少路径 %v：%s", path, encoded)
		}
	}
	samples, _ := auditJSONPath(document, "result", "samples", "evidence")
	strategies, _ := auditJSONPath(document, "result", "strategyResults", "evidence")
	if len(samples.([]any)) != 1 || len(strategies.([]any)) != 1 {
		t.Fatalf("前端详情证据分页不完整：samples=%#v strategies=%#v", samples, strategies)
	}
}

func TestAuditRunPresentationIncludesItsCapabilityResult(t *testing.T) {
	ctx := context.Background()
	store, err := NewAuditSQLiteStore(filepath.Join(t.TempDir(), "run-result-contract.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	report := auditHTMLStoredReportFixture(t, ctx, store)
	management, err := NewAuditManagementService(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := management.GetRun(ctx, report.RunID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := management.PresentRun(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if view.Result == nil || view.Result.ReportID != report.ID || view.Result.Capability == nil ||
		view.Result.Capability.Index == nil || *view.Result.Capability.Index != 100 {
		t.Fatalf("运行能力结果摘要 = %#v", view.Result)
	}

	latest, err := management.ListLatestRunPresentations(ctx, []string{report.JobID})
	if err != nil {
		t.Fatal(err)
	}
	if latest[report.JobID].Result == nil || latest[report.JobID].Result.ReportID != report.ID {
		t.Fatalf("任务最新运行结果 = %#v", latest[report.JobID])
	}
}

func auditJSONPath(document map[string]any, path ...string) (any, bool) {
	var current any = document
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
