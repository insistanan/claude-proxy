package logger

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRetentionCutoffKeepsTodayAndYesterday(t *testing.T) {
	now := time.Date(2026, 10, 2, 15, 4, 0, 0, time.Local)
	cutoff := RetentionCutoff(now)
	year, month, day := cutoff.Date()
	if year != 2026 || month != time.October || day != 1 {
		t.Fatalf("cutoff=%s, want 2026-10-01 00:00:00 local", cutoff)
	}
	if cutoff.Hour() != 0 || cutoff.Minute() != 0 || cutoff.Second() != 0 {
		t.Fatalf("cutoff should be start of yesterday, got %s", cutoff)
	}
}

func TestCleanupDeletesOlderThanYesterday(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	now := time.Now()
	keepToday := now
	keepYesterday := RetentionCutoff(now).Add(time.Hour)
	dropOld := RetentionCutoff(now).Add(-time.Hour)

	store.RecordAppLog(AppLog{UnixMilli: dropOld.UnixMilli(), Message: "old"})
	store.RecordAppLog(AppLog{UnixMilli: keepYesterday.UnixMilli(), Message: "yesterday"})
	store.RecordAppLog(AppLog{UnixMilli: keepToday.UnixMilli(), Message: "today"})
	store.Flush()
	store.cleanupExpired()

	logs, err := store.QueryAppLogs(context.Background(), QueryOptions{
		From: dropOld.Add(-time.Hour).UnixMilli(),
		To:   keepToday.Add(time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(logs))
	for _, entry := range logs {
		got = append(got, entry.Message)
	}
	if len(got) != 2 || got[0] != "yesterday" || got[1] != "today" {
		t.Fatalf("got %v, want [yesterday today]", got)
	}
}

func TestListSystemLogSummariesGroupsByRequestIDWithoutBody(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	now := time.Now()
	older := now.Add(-time.Minute)
	store.RecordTraffic(TrafficLog{
		UnixMilli: older.UnixMilli(),
		RequestID: "req-old",
		Phase:     PhaseClientRequest,
		APIType:   "Chat",
		Body:      `{"secret":"should-not-appear-in-list"}`,
	})
	store.RecordTraffic(TrafficLog{
		UnixMilli: now.UnixMilli(),
		RequestID: "req-new",
		Phase:     PhaseClientRequest,
		APIType:   "Messages",
		Body:      `{"messages":[{"role":"user","content":"hi"}]}`,
	})
	store.RecordTraffic(TrafficLog{
		UnixMilli: now.Add(time.Millisecond).UnixMilli(),
		RequestID: "req-new",
		Phase:     PhaseUpstreamRequest,
		APIType:   "Messages",
		Body:      `{"model":"claude"}`,
	})
	store.RecordTraffic(TrafficLog{
		UnixMilli:  now.Add(2 * time.Millisecond).UnixMilli(),
		RequestID:  "req-new",
		Phase:      PhaseUpstreamResponse,
		APIType:    "Messages",
		StatusCode: 200,
		Stream:     true,
		Body:       `{"ok":true}`,
	})
	store.RecordRequest(RequestLogRecord{
		UnixMilli: now.UnixMilli(),
		RequestID: "req-new",
		APIType:   "messages",
		Payload:   []byte(`{"channelName":"prod","resolvedModel":"claude-sonnet","status":"completed"}`),
	})
	store.Flush()

	summaries, err := store.ListSystemLogSummaries(context.Background(), SystemLogListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 {
		t.Fatalf("got %d summaries, want 2", len(summaries))
	}
	if summaries[0].RequestID != "req-new" {
		t.Fatalf("first requestId=%s, want req-new", summaries[0].RequestID)
	}
	if summaries[0].StatusCode != 200 || !summaries[0].Stream {
		t.Fatalf("summary=%+v, want status 200 stream", summaries[0])
	}
	if summaries[0].ChannelName != "prod" || summaries[0].Model != "claude-sonnet" {
		t.Fatalf("joined meta=%+v", summaries[0])
	}
	if len(summaries[0].MissingPhases) != 0 {
		t.Fatalf("missingPhases=%v, want empty", summaries[0].MissingPhases)
	}
	if len(summaries[1].MissingPhases) != 2 {
		t.Fatalf("old missingPhases=%v, want upstream request+response", summaries[1].MissingPhases)
	}

	filtered, err := store.ListSystemLogSummaries(context.Background(), SystemLogListOptions{
		APIType: "messages",
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].RequestID != "req-new" {
		t.Fatalf("filtered=%+v, want only req-new", filtered)
	}

	detail, err := store.GetSystemLogDetail(context.Background(), "req-new")
	if err != nil {
		t.Fatal(err)
	}
	if detail == nil || len(detail.TrafficLogs) != 3 {
		t.Fatalf("detail=%+v, want 3 traffic rows", detail)
	}
	if detail.TrafficLogs[0].Body == "" {
		t.Fatal("detail should include body")
	}
}
