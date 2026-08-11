package modelaudit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestDueAuditScheduleSlotReturnsLatestWindowSlot(t *testing.T) {
	start := time.Date(2026, 8, 11, 17, 0, 0, 0, time.UTC)
	schedule := AuditSchedule{
		StartAt: start, IntervalMs: AuditMinimumInterval.Milliseconds(), TimeZone: "UTC",
	}
	after := start.Add(-30 * time.Minute)
	now := start.Add(12*time.Hour + time.Minute)
	slot, err := DueAuditScheduleSlot("job-due", schedule, after, now)
	if err != nil {
		t.Fatal(err)
	}
	if slot == nil || slot.Ordinal != 2 || !slot.ScheduledAt.Equal(start.Add(12*time.Hour)) {
		t.Fatalf("延迟扫描应只返回最新槽位: %#v", slot)
	}
	slot, err = DueAuditScheduleSlot("job-due", schedule, start.Add(12*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if slot != nil {
		t.Fatalf("扫描边界之前或等于边界的槽位不应补跑: %#v", slot)
	}
}

func TestAuditSchedulerLoopTriggersLatestSlotAndCloses(t *testing.T) {
	upstream := newAuditSchedulerUpstream(t)
	defer upstream.Close()
	store, service, controller, job := auditRunControllerFixture(t, upstream.URL, upstream.Client())
	start := time.Date(2026, 8, 11, 17, 0, 0, 0, time.UTC)
	job = updateAuditJobSchedule(t, store, job, AuditSchedule{
		StartAt: start, IntervalMs: AuditMinimumInterval.Milliseconds(), TimeZone: "UTC",
	})
	clock := &auditSchedulerClock{value: start.Add(-30 * time.Minute)}
	configureAuditSchedulerClock(controller, service, clock)
	scheduler, err := NewAuditScheduler(store, controller, AuditSchedulerConfig{PollInterval: 5 * time.Millisecond, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	scheduler.now = clock.Now
	if err := scheduler.Start(); err != nil {
		t.Fatal(err)
	}
	clock.Set(start.Add(12*time.Hour + time.Minute))
	run := waitForAuditRunCount(t, store, 1).Runs[0]
	terminal := waitForStoredAuditRun(t, store, run.ID)
	if terminal.Trigger != AuditRunScheduled || terminal.ScheduledFor == nil ||
		!terminal.ScheduledFor.Equal(start.Add(12*time.Hour)) {
		t.Fatalf("调度器没有只触发最新到期槽位: %#v", terminal)
	}
	closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scheduler.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	clock.Set(start.Add(18*time.Hour + time.Minute))
	time.Sleep(20 * time.Millisecond)
	page, err := store.ListRuns(context.Background(), AuditRunListOptions{})
	if err != nil || page.Total != 1 {
		t.Fatalf("调度器关闭后仍创建运行: total=%d err=%v", page.Total, err)
	}
	_ = job
}

func TestAuditSchedulerStartupSkipsSlotsBeforeBoot(t *testing.T) {
	store, service, controller, job := auditRunControllerFixture(t, "https://fixture.invalid", capabilityFixtureDoer{})
	start := time.Date(2026, 8, 11, 17, 0, 0, 0, time.UTC)
	job = updateAuditJobSchedule(t, store, job, AuditSchedule{
		StartAt: start, IntervalMs: AuditMinimumInterval.Milliseconds(), TimeZone: "UTC",
	})
	clock := &auditSchedulerClock{value: start.Add(12*time.Hour + 30*time.Minute)}
	configureAuditSchedulerClock(controller, service, clock)
	scheduler, err := NewAuditScheduler(store, controller, AuditSchedulerConfig{PollInterval: 5 * time.Millisecond, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	scheduler.now = clock.Now
	if err := scheduler.Start(); err != nil {
		t.Fatal(err)
	}
	clock.Set(start.Add(12*time.Hour + 31*time.Minute))
	time.Sleep(20 * time.Millisecond)
	closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scheduler.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListRuns(context.Background(), AuditRunListOptions{})
	if err != nil || page.Total != 0 {
		t.Fatalf("调度器启动后补跑了停机期间槽位: total=%d err=%v", page.Total, err)
	}
	_ = job
}

func TestAuditSchedulerReconcilesPendingAndRunningRecovery(t *testing.T) {
	t.Run("resume expired pending", func(t *testing.T) {
		upstream := newAuditSchedulerUpstream(t)
		defer upstream.Close()
		store, service, controller, job := auditRunControllerFixture(t, upstream.URL, upstream.Client())
		start := prepareStoredAuditRecoveryRun(t, store, job, false)
		clock := &auditSchedulerClock{value: start.Lease.ExpiresAt}
		configureAuditSchedulerClock(controller, service, clock)
		scheduler, err := NewAuditScheduler(store, controller, DefaultAuditSchedulerConfig())
		if err != nil {
			t.Fatal(err)
		}
		scheduler.now = clock.Now
		if err := scheduler.Start(); err != nil {
			t.Fatal(err)
		}
		terminal := waitForStoredAuditRun(t, store, start.Run.ID)
		if terminal.Status != AuditRunCompleted {
			t.Fatalf("pending 恢复终态 = %#v", terminal)
		}
		if err := scheduler.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("finalize expired running", func(t *testing.T) {
		store, service, controller, job := auditRunControllerFixture(t, "https://fixture.invalid", capabilityFixtureDoer{})
		start := prepareStoredAuditRecoveryRun(t, store, job, true)
		clock := &auditSchedulerClock{value: start.Lease.ExpiresAt}
		configureAuditSchedulerClock(controller, service, clock)
		scheduler, err := NewAuditScheduler(store, controller, DefaultAuditSchedulerConfig())
		if err != nil {
			t.Fatal(err)
		}
		scheduler.now = clock.Now
		if err := scheduler.Start(); err != nil {
			t.Fatal(err)
		}
		terminal, found, err := store.GetRun(context.Background(), start.Run.ID)
		if err != nil || !found || terminal.Status != AuditRunFailed || terminal.StopReason != AuditRunStopInterrupted {
			t.Fatalf("running 恢复终态: found=%v run=%#v err=%v", found, terminal, err)
		}
		if err := scheduler.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

type auditSchedulerClock struct {
	mu    sync.RWMutex
	value time.Time
}

func (c *auditSchedulerClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.value
}

func (c *auditSchedulerClock) Set(value time.Time) {
	c.mu.Lock()
	c.value = value
	c.mu.Unlock()
}

func configureAuditSchedulerClock(controller *BuiltinAuditRunController, service *Service, clock *auditSchedulerClock) {
	controller.now = clock.Now
	service.now = clock.Now
	service.resolver.(*ConfigTargetResolver).now = clock.Now
}

func updateAuditJobSchedule(t *testing.T, store *AuditSQLiteStore, job AuditJob, schedule AuditSchedule) AuditJob {
	t.Helper()
	definition := auditJobDefinitionFromFixture(job)
	definition.Schedule = schedule
	updated, err := UpdateAuditJob(job, job.Revision, definition, job.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateJob(context.Background(), updated, job.Revision); err != nil {
		t.Fatal(err)
	}
	return updated
}

func newAuditSchedulerUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-scheduled","model":"gpt-5.6-sol","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"我可以处理分析任务。"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`))
	}))
}

func waitForAuditRunCount(t *testing.T, store *AuditSQLiteStore, expected int64) AuditRunPage {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		page, err := store.ListRuns(context.Background(), AuditRunListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if page.Total == expected {
			return page
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待审计运行数量 %d 超时，当前 %d", expected, page.Total)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func prepareStoredAuditRecoveryRun(t *testing.T, store *AuditSQLiteStore, job AuditJob, running bool) AuditRunStart {
	t.Helper()
	now := time.Date(2026, 8, 11, 18, 0, 0, 0, time.UTC)
	limits := AuditRunStartLimits{MaximumActiveRuns: 2, LeaseDurationMs: 100}
	start, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: "audit-run.recovery", Trigger: AuditRunManual,
		OwnerID: "audit-instance.old", LeaseToken: "audit-lease.old", Now: now, Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRunStart(context.Background(), start, limits.MaximumActiveRuns); err != nil {
		t.Fatal(err)
	}
	if running {
		runningAt := now.Add(10 * time.Millisecond)
		activated, err := ActivateAuditRun(start.Run, start.Job, auditResolvedTargetsForJob(t, start.Job, runningAt), runningAt)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveRun(context.Background(), activated, AuditRunPending, start.Lease, runningAt); err != nil {
			t.Fatal(err)
		}
		start.Run = activated
	}
	return start
}
