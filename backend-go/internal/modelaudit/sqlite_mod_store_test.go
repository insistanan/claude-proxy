package modelaudit

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditSQLiteStorePersistsModVersionAndAnalysis(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewAuditSQLiteStore(filepath.Join(root, "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	modDirectory := filepath.Join(root, "mods", "sqlite-mod")
	writeAuditModFixture(t, modDirectory, auditModLLMFixture("sqlite-mod"))
	bundle, err := loadAuditModBundle(modDirectory, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	bundle.SnapshotPath = filepath.Join(root, "cache", bundle.Reference.ContentSHA256)
	storedVersion, err := store.SaveModVersion(ctx, bundle)
	if err != nil {
		t.Fatal(err)
	}
	loadedVersion, found, err := store.GetModVersion(ctx, bundle.Reference)
	if err != nil || !found || loadedVersion.Reference != storedVersion.Reference {
		t.Fatalf("GetModVersion() = %#v, %v, %v", loadedVersion, found, err)
	}

	job := auditJobFixture(t)
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	startedAt := job.Schedule.StartAt.Add(time.Hour)
	start, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "run-mod-analysis", Trigger: AuditRunManual,
		OwnerID: "instance-mod-analysis", LeaseToken: "lease-mod-analysis", Now: startedAt,
		Limits: AuditRunStartLimits{MaximumActiveRuns: 2, LeaseDurationMs: (5 * time.Minute).Milliseconds()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRunStart(ctx, start, 2); err != nil {
		t.Fatal(err)
	}

	analysis, err := NewAuditModAnalysisRecord(
		"analysis-sqlite-mod", start.Run.ID, job.Targets[0].ID, bundle.Reference, AuditModAnalysisLLM,
		json.RawMessage(`{"schema":"audit.mod-analysis-input.v1","samples":[]}`), startedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	analysis.Analyzer = &AuditModAnalyzerTarget{
		ChannelID: job.Targets[0].ChannelID, ChannelKind: job.Targets[0].ChannelKind,
		Model: "gpt-5.6-sol", Protocol: ProtocolResponses, Thinking: ThinkingMedium, RequestProfile: "responses.standard",
	}
	if err := store.CreateModAnalysis(ctx, analysis); err != nil {
		t.Fatal(err)
	}

	running := analysis
	running.Revision++
	running.Status = AuditModAnalysisRunning
	running.StartedAt = auditModAnalysisTime(startedAt.Add(time.Second))
	if err := store.UpdateModAnalysis(ctx, running, analysis.Revision); err != nil {
		t.Fatal(err)
	}
	completed := running
	completed.Revision++
	completed.Status = AuditModAnalysisCompleted
	completed.FinishedAt = auditModAnalysisTime(startedAt.Add(2 * time.Second))
	completed.Result, completed.ResultSHA256, err = canonicalJSONObject(json.RawMessage(`{"schema":"audit.mod-analysis-result.v1","verdict":"inconclusive","confidence":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateModAnalysis(ctx, completed, running.Revision); err != nil {
		t.Fatal(err)
	}

	loaded, found, err := store.GetModAnalysis(ctx, analysis.ID)
	if err != nil || !found || loaded.Status != AuditModAnalysisCompleted || loaded.ResultSHA256 != completed.ResultSHA256 {
		t.Fatalf("GetModAnalysis() = %#v, %v, %v", loaded, found, err)
	}
	page, err := store.ListModAnalyses(ctx, AuditModAnalysisListOptions{RunID: start.Run.ID})
	if err != nil || page.Total != 1 || len(page.Analyses) != 1 {
		t.Fatalf("ListModAnalyses() = %#v, %v", page, err)
	}
}

func TestAuditSQLiteStoreRejectsInvalidModAnalysisTransition(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewAuditSQLiteStore(filepath.Join(root, "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	modDirectory := filepath.Join(root, "mods", "transition-mod")
	writeAuditModFixture(t, modDirectory, auditModLLMFixture("transition-mod"))
	bundle, err := loadAuditModBundle(modDirectory, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	bundle.SnapshotPath = filepath.Join(root, "cache", bundle.Reference.ContentSHA256)
	if _, err := store.SaveModVersion(ctx, bundle); err != nil {
		t.Fatal(err)
	}

	job := auditJobFixture(t)
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	startedAt := job.Schedule.StartAt.Add(time.Hour)
	start, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "run-mod-transition", Trigger: AuditRunManual,
		OwnerID: "instance-mod-transition", LeaseToken: "lease-mod-transition", Now: startedAt,
		Limits: AuditRunStartLimits{MaximumActiveRuns: 2, LeaseDurationMs: (5 * time.Minute).Milliseconds()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRunStart(ctx, start, 2); err != nil {
		t.Fatal(err)
	}
	analysis, err := NewAuditModAnalysisRecord(
		"analysis-transition", start.Run.ID, job.Targets[0].ID, bundle.Reference, AuditModAnalysisLLM,
		json.RawMessage(`{"schema":"audit.mod-analysis-input.v1"}`), startedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateModAnalysis(ctx, analysis); err != nil {
		t.Fatal(err)
	}
	completed := analysis
	completed.Revision++
	completed.Status = AuditModAnalysisCompleted
	completed.FinishedAt = auditModAnalysisTime(startedAt.Add(time.Second))
	completed.Result, completed.ResultSHA256, err = canonicalJSONObject(json.RawMessage(`{"verdict":"invalid-transition"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateModAnalysis(ctx, completed, analysis.Revision); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("UpdateModAnalysis() error = %v, want conflict", err)
	}
}
