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
