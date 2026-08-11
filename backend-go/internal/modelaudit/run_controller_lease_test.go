package modelaudit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestBuiltinAuditRunControllerRenewsLeaseAcrossLongRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var blockFirst sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		blockFirst.Do(func() {
			close(requestStarted)
			select {
			case <-releaseRequest:
			case <-request.Context().Done():
			}
		})
		if request.Context().Err() != nil {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-lease","model":"gpt-5.6-sol","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`))
	}))
	defer upstream.Close()

	store, service, controller, job := auditRunControllerFixture(t, upstream.URL, upstream.Client())
	configureShortAuditLeaseClock(controller, service, 90*time.Millisecond)
	started, err := controller.StartManual(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("长运行租约夹具未启动请求")
	}
	initial, found, err := store.GetLease(context.Background(), job.ID)
	if err != nil || !found {
		t.Fatalf("读取初始租约: found=%v err=%v", found, err)
	}
	active := currentActiveAuditRun(t, controller, started.ID)
	deadline := time.Now().Add(2 * time.Second)
	for {
		renewed, found, err := store.GetLease(context.Background(), job.ID)
		if err != nil || !found {
			t.Fatalf("读取续期租约: found=%v err=%v", found, err)
		}
		if renewed.ExpiresAt.After(initial.ExpiresAt) && !controller.now().Before(initial.ExpiresAt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("租约未跨过初始到期点续期: initial=%s current=%s", initial.ExpiresAt, renewed.ExpiresAt)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(releaseRequest)
	terminal := waitForStoredAuditRun(t, store, started.ID)
	if terminal.Status != AuditRunCompleted {
		t.Fatalf("跨租约长请求终态 = %#v", terminal)
	}
	if _, found, err := store.GetLease(context.Background(), job.ID); err != nil || found {
		t.Fatalf("运行完成后租约: found=%v err=%v", found, err)
	}
	select {
	case <-active.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("等待活动运行 %q 退出超时", started.ID)
	}
	select {
	case <-active.renewalDone:
	default:
		t.Fatal("运行完成后续租循环仍未退出")
	}
}

func TestBuiltinAuditRunControllerCancelsOnLeaseCASLoss(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCancelled := make(chan struct{})
	doer := leaseLossBlockingDoer{started: requestStarted, cancelled: requestCancelled}
	store, service, controller, job := auditRunControllerFixture(t, "https://fixture.invalid", doer)
	configureShortAuditLeaseClock(controller, service, 90*time.Millisecond)
	started, err := controller.StartManual(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("失租夹具未启动请求")
	}
	active := currentActiveAuditRun(t, controller, started.ID)
	lease, found, err := store.GetLease(context.Background(), job.ID)
	if err != nil || !found {
		t.Fatalf("读取待替换租约: found=%v err=%v", found, err)
	}
	replacementExpiresAt := lease.ExpiresAt.Add(time.Second)
	if _, err := store.db.ExecContext(context.Background(), `UPDATE audit_run_leases
		SET owner_id = ?, token = ?, expires_at = ? WHERE job_id = ?`,
		"audit-instance.external", "audit-lease.external", auditUnixMillis(replacementExpiresAt), job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("租约 CAS 失败后活动请求未取消")
	}
	select {
	case <-active.done:
	case <-time.After(2 * time.Second):
		t.Fatal("租约 CAS 失败后运行协程未退出")
	}
	if active.leaseLoss() == nil {
		t.Fatal("租约 CAS 失败未记录失租原因")
	}
	stored, found, err := store.GetRun(context.Background(), started.ID)
	if err != nil || !found || stored.Status != AuditRunRunning {
		t.Fatalf("失租后等待持久化恢复的运行: found=%v run=%#v err=%v", found, stored, err)
	}
	entries, err := store.LoadRecoveryState(context.Background())
	if err != nil || len(entries) != 1 {
		t.Fatalf("读取失租恢复状态: entries=%d err=%v", len(entries), err)
	}
	recoveredAt := replacementExpiresAt
	decision, err := PlanAuditRecovery(entries[0], recoveredAt)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != AuditRecoveryFinalizeInterrupted || decision.Run.StopReason != AuditRunStopInterrupted {
		t.Fatalf("失租恢复决策 = %#v", decision)
	}
	if err := store.CommitInterruptedRecovery(context.Background(), decision, recoveredAt); err != nil {
		t.Fatal(err)
	}
}

type leaseLossBlockingDoer struct {
	started   chan<- struct{}
	cancelled chan<- struct{}
}

func (d leaseLossBlockingDoer) Do(request *http.Request) (*http.Response, error) {
	close(d.started)
	<-request.Context().Done()
	close(d.cancelled)
	return nil, request.Context().Err()
}

func configureShortAuditLeaseClock(controller *BuiltinAuditRunController, service *Service, duration time.Duration) {
	startedAt := time.Now()
	base := time.Date(2026, 8, 11, 18, 0, 0, 0, time.UTC)
	now := func() time.Time { return base.Add(time.Since(startedAt)) }
	controller.config.LeaseDuration = duration
	controller.now = now
	service.now = now
	service.resolver.(*ConfigTargetResolver).now = now
}

func currentActiveAuditRun(t *testing.T, controller *BuiltinAuditRunController, runID string) *activeAuditRun {
	t.Helper()
	controller.mu.Lock()
	defer controller.mu.Unlock()
	active := controller.active[runID]
	if active == nil {
		t.Fatalf("活动运行 %q 不存在", runID)
	}
	return active
}
