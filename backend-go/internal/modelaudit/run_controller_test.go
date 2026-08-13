package modelaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/gin-gonic/gin"
)

func TestBuiltinAuditRunControllerCapabilityPresetsAndBudget(t *testing.T) {
	tests := []struct {
		name             string
		mode             CapabilityPresetMode
		maxRequests      int
		expectedRequests int
		expectedRun      AuditRunStatus
		expectedSummary  AuditSummaryStatus
		expectedReport   CapabilityAggregationStatus
		expectedFormal   bool
	}{
		{name: "quick provisional", mode: CapabilityPresetQuick, maxRequests: 100, expectedRequests: 7, expectedRun: AuditRunCompleted, expectedSummary: AuditSummaryInsufficientEvidence, expectedReport: CapabilityAggregationProvisional},
		{name: "standard formal", mode: CapabilityPresetStandard, maxRequests: 100, expectedRequests: 14, expectedRun: AuditRunCompleted, expectedSummary: AuditSummaryComplete, expectedReport: CapabilityAggregationComplete, expectedFormal: true},
		{name: "budget partial", mode: CapabilityPresetStandard, maxRequests: 3, expectedRequests: 3, expectedRun: AuditRunPartial, expectedSummary: AuditSummaryPartial, expectedReport: CapabilityAggregationPartialBudget},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, _, controller, job := auditRunControllerFixture(t, "https://fixture.invalid", capabilityFixtureDoer{})
			job = updateAuditJobForCapability(t, store, job, test.mode, test.maxRequests)
			started, err := controller.StartManual(context.Background(), job)
			if err != nil {
				t.Fatal(err)
			}
			terminal := waitForStoredAuditRun(t, store, started.ID)
			if terminal.Status != test.expectedRun || terminal.Usage.Requests != test.expectedRequests {
				t.Fatalf("能力运行终态 = %#v", terminal)
			}
			summary := waitForAuditSummaryReport(t, store, job.Targets[0], terminal.FinishedAt.Add(time.Minute))
			if summary.PrimaryStatus != test.expectedSummary || summary.Capability == nil || summary.Capability.Status != test.expectedReport ||
				summary.Capability.Formal != test.expectedFormal {
				t.Fatalf("能力摘要状态 = %q，能力报告 = %#v", summary.PrimaryStatus, summary.Capability)
			}
			if test.expectedFormal && (summary.Capability.Index == nil || *summary.Capability.Index != 100 || summary.Capability.ScoredDimensions != 7) {
				t.Fatalf("正式能力摘要 = %#v", summary.Capability)
			}
		})
	}
}

func TestBuiltinAuditRunControllerStopsSystematicCapabilityFailure(t *testing.T) {
	store, _, controller, job := auditRunControllerFixture(t, "https://fixture.invalid", capabilityBadGatewayDoer{})
	job = updateAuditJobForCapability(t, store, job, CapabilityPresetQuick, 100)
	started, err := controller.StartManual(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitForStoredAuditRun(t, store, started.ID)
	if terminal.Status != AuditRunFailed || terminal.StopReason != AuditRunStopFailure || terminal.Usage.Requests != capabilitySystemFailureConsecutiveThreshold {
		t.Fatalf("系统性能力故障终态 = %#v", terminal)
	}
	if !strings.Contains(terminal.Failure, "HTTP 502 Bad Gateway") {
		t.Fatalf("系统性能力故障原因未保留安全摘要: %q", terminal.Failure)
	}
	summary := waitForAuditSummaryReport(t, store, job.Targets[0], terminal.FinishedAt.Add(time.Minute))
	if summary.PrimaryStatus != AuditSummaryFailed || summary.Capability == nil {
		t.Fatalf("系统性能力故障摘要 = %#v", summary)
	}
	detail, found, err := store.GetReportDetail(context.Background(), summary.ReportID, AuditReportDetailOptions{
		Now: terminal.FinishedAt.Add(time.Minute), Samples: AuditPageRequest{Page: 1, PageSize: 10}, StrategyResults: AuditPageRequest{Page: 1, PageSize: 10},
	})
	if err != nil || !found {
		t.Fatalf("读取系统性能力故障报告: found=%v err=%v", found, err)
	}
	if detail.Detail.Report.Capability == nil || detail.Detail.Report.Capability.Counts.ExecutionFailed != capabilitySystemFailureConsecutiveThreshold || len(detail.Detail.Report.Capability.Reasons) == 0 ||
		!strings.Contains(strings.Join(detail.Detail.Report.Capability.Reasons, " "), "HTTP 502 Bad Gateway") {
		t.Fatalf("能力故障报告未展示具体原因: %#v", detail.Detail.Report.Capability)
	}
}

func TestAuditManagementCatalogAndManualRunUseAvailableAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-api","model":"gpt-5.6-sol","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`))
	}))
	defer upstream.Close()

	store, service, controller, job := auditRunControllerFixture(t, upstream.URL, upstream.Client())
	management, err := NewAuditManagementService(store, controller, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	if err := RegisterRoutes(router.Group("/api"), service, management); err != nil {
		t.Fatal(err)
	}
	strategiesResponse := performAuditRequest(router, http.MethodGet, "/api/model-audit/strategies", nil)
	if strategiesResponse.Code != http.StatusOK {
		t.Fatalf("策略清单状态 = %d body=%s", strategiesResponse.Code, strategiesResponse.Body.String())
	}
	var strategies AuditStrategyCatalogResponse
	if err := json.Unmarshal(strategiesResponse.Body.Bytes(), &strategies); err != nil {
		t.Fatal(err)
	}
	if !strategies.Available || len(strategies.Strategies) != 1 || strategies.Strategies[0].Ref.ID != BuiltinMetadataStrategyID {
		t.Fatalf("策略清单 = %#v", strategies)
	}
	capabilityResponse := performAuditRequest(router, http.MethodGet, "/api/model-audit/capability-presets", nil)
	if capabilityResponse.Code != http.StatusOK {
		t.Fatalf("能力清单状态 = %d body=%s", capabilityResponse.Code, capabilityResponse.Body.String())
	}
	var capability AuditCapabilityCatalogResponse
	if err := json.Unmarshal(capabilityResponse.Body.Bytes(), &capability); err != nil {
		t.Fatal(err)
	}
	if !capability.Catalog.Available || capability.Catalog.Reason != "" || len(capability.Catalog.Packages) != 1 || len(capability.Catalog.Presets) != 3 {
		t.Fatalf("能力清单 = %#v", capability)
	}
	unknownDefinition := auditJobDefinitionFromFixture(job)
	unknownPreset := VersionedRef{ID: "capability.preset.unknown", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"}
	unknownDefinition.Workload, err = CanonicalizeAuditWorkload(AuditWorkload{Kind: AuditWorkloadCapability, CapabilityPreset: &unknownPreset})
	if err != nil {
		t.Fatal(err)
	}
	unknownBody, err := json.Marshal(CreateAuditJobRequest{Definition: unknownDefinition, InitialStatus: AuditJobEnabled})
	if err != nil {
		t.Fatal(err)
	}
	unknownResponse := performAuditRequest(router, http.MethodPost, "/api/model-audit/jobs", unknownBody)
	assertAuditAPIError(t, unknownResponse, http.StatusUnprocessableEntity, ErrorCodeUnsupported)
	requestBody, err := json.Marshal(StartManualAuditRunRequest{ExpectedRevision: job.Revision})
	if err != nil {
		t.Fatal(err)
	}
	startedResponse := performAuditRequest(router, http.MethodPost, "/api/model-audit/jobs/"+job.ID+"/runs", requestBody)
	if startedResponse.Code != http.StatusAccepted {
		t.Fatalf("手动运行状态 = %d body=%s", startedResponse.Code, startedResponse.Body.String())
	}
	var started AuditRunResponse
	if err := json.Unmarshal(startedResponse.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.Run.ID == "" || started.Run.Status != AuditRunPending || started.Run.FailureHidden {
		t.Fatalf("手动运行安全视图 = %#v", started)
	}
	waitForStoredAuditRun(t, store, started.Run.ID)
}

func TestBuiltinAuditRunControllerCompletesIdentityRunAndReport(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-audit","model":"gpt-5.6-sol","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"我可以处理分析任务。"}]}],"usage":{"input_tokens":12,"output_tokens":8,"total_tokens":20}}`))
	}))
	defer upstream.Close()

	store, service, controller, job := auditRunControllerFixture(t, upstream.URL, upstream.Client())
	started, err := controller.StartManual(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitForStoredAuditRun(t, store, started.ID)
	if terminal.Status != AuditRunCompleted || terminal.Usage.Requests != 2 || terminal.Usage.TotalTokens != 40 {
		t.Fatalf("身份审计终态 = %#v", terminal)
	}
	summary := waitForAuditSummaryReport(t, store, job.Targets[0], terminal.FinishedAt.Add(time.Minute))
	if summary.PrimaryStatus != AuditSummaryInsufficientEvidence || summary.Identity == nil || summary.SampleCount != 2 {
		t.Fatalf("身份审计摘要 = %#v", summary)
	}
	detail, found, err := store.GetReportDetail(context.Background(), summary.ReportID, AuditReportDetailOptions{
		Now: terminal.FinishedAt.Add(time.Minute), Samples: AuditPageRequest{Page: 1, PageSize: 10},
		StrategyResults: AuditPageRequest{Page: 1, PageSize: 10},
	})
	if err != nil || !found {
		t.Fatalf("读取身份审计详情: found=%v err=%v", found, err)
	}
	if detail.Samples.Total != 2 || detail.StrategyResults.Total != 1 || detail.Detail.Report.Identity == nil ||
		detail.Detail.Report.Identity.Formal {
		t.Fatalf("身份审计详情 = %#v", detail)
	}
	if _, found, err := store.GetLease(context.Background(), job.ID); err != nil || found {
		t.Fatalf("完成后租约: found=%v err=%v", found, err)
	}
	_ = service
}

func TestBuiltinAuditRunControllerKeepsPartialIdentityEvidence(t *testing.T) {
	store, _, controller, job := auditRunControllerFixture(t, "https://fixture.invalid", &identityPartialFailureDoer{})
	started, err := controller.StartManual(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitForStoredAuditRun(t, store, started.ID)
	if terminal.Status != AuditRunCompleted || terminal.Usage.Requests != 2 {
		t.Fatalf("部分身份证据运行终态 = %#v", terminal)
	}
	summary := waitForAuditSummaryReport(t, store, job.Targets[0], terminal.FinishedAt.Add(time.Minute))
	if summary.PrimaryStatus != AuditSummaryInsufficientEvidence || summary.Identity == nil {
		t.Fatalf("部分身份证据摘要 = %#v", summary)
	}
	detail, found, err := store.GetReportDetail(context.Background(), summary.ReportID, AuditReportDetailOptions{
		Now: terminal.FinishedAt.Add(time.Minute), Samples: AuditPageRequest{Page: 1, PageSize: 10}, StrategyResults: AuditPageRequest{Page: 1, PageSize: 10},
	})
	if err != nil || !found {
		t.Fatalf("读取部分身份报告: found=%v err=%v", found, err)
	}
	reasons := make([]string, 0, len(detail.Detail.Report.Identity.Signals))
	for _, signal := range detail.Detail.Report.Identity.Signals {
		reasons = append(reasons, signal.Reason)
	}
	if !strings.Contains(strings.Join(reasons, " "), "HTTP 504 Gateway Timeout") {
		t.Fatalf("身份报告未展示失败样本原因: %#v", detail.Detail.Report.Identity.Signals)
	}
}

func TestBuiltinAuditRunControllerCancelsActiveRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		select {
		case <-request.Context().Done():
		case <-releaseRequest:
		}
	}))
	defer upstream.Close()
	defer close(releaseRequest)

	store, service, controller, job := auditRunControllerFixture(t, upstream.URL, upstream.Client())
	management, err := NewAuditManagementService(store, controller, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	if err := RegisterRoutes(router.Group("/api"), service, management); err != nil {
		t.Fatal(err)
	}
	started, err := controller.StartManual(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("本地模拟审计请求未启动")
	}
	getResponse := performAuditRequest(router, http.MethodGet, "/api/model-audit/runs/"+started.ID, nil)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("运行详情状态 = %d body=%s", getResponse.Code, getResponse.Body.String())
	}
	listResponse := performAuditRequest(router, http.MethodGet, "/api/model-audit/runs?jobId="+job.ID+"&status=running&page=1&pageSize=10", nil)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("运行列表状态 = %d body=%s", listResponse.Code, listResponse.Body.String())
	}
	var listed AuditRunPageResponse
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Page.Total != 1 || len(listed.Page.Runs) != 1 || listed.Page.Runs[0].ID != started.ID {
		t.Fatalf("运行列表响应 = %#v", listed)
	}
	cancelResponse := performAuditRequest(router, http.MethodPost, "/api/model-audit/runs/"+started.ID+"/cancel", nil)
	if cancelResponse.Code != http.StatusAccepted {
		t.Fatalf("取消运行状态 = %d body=%s", cancelResponse.Code, cancelResponse.Body.String())
	}
	var cancelled AuditRunResponse
	if err := json.Unmarshal(cancelResponse.Body.Bytes(), &cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled.Run.Status != AuditRunPartial || cancelled.Run.StopReason != AuditRunStopCancelled || cancelled.Run.Usage.Requests != 1 || cancelled.Run.FailureHidden {
		t.Fatalf("取消后的审计运行安全视图 = %#v", cancelled)
	}
	storedJob, found, err := store.GetJob(context.Background(), job.ID)
	if err != nil || !found || storedJob.Status != AuditJobEnabled {
		t.Fatalf("取消后的审计任务: found=%v job=%#v err=%v", found, storedJob, err)
	}
	if _, found, err := store.GetLease(context.Background(), job.ID); err != nil || found {
		t.Fatalf("取消后租约: found=%v err=%v", found, err)
	}
}

func auditRunControllerFixture(
	t *testing.T,
	upstreamURL string,
	doer HTTPDoer,
) (*AuditSQLiteStore, *Service, *BuiltinAuditRunController, AuditJob) {
	t.Helper()
	store, err := NewAuditSQLiteStore(filepath.Join(t.TempDir(), "run-controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Config{ResponsesUpstream: []config.UpstreamConfig{{
		ID: "channel-responses", Name: "Local audit fixture", BaseURL: upstreamURL, APIKeys: []string{"fixture-key"},
		ServiceType: "responses", DefaultModel: "gpt-5.6-sol",
	}}}
	service := newTestService(t, cfg, doer)
	fixedAt := time.Date(2026, 8, 11, 18, 0, 0, 0, time.UTC)
	registry, err := NewStrategyRegistry(NewMetadataConsistencyStrategy())
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewBuiltinAuditRunController(store, service, registry, DefaultAuditRunControllerConfig())
	if err != nil {
		t.Fatal(err)
	}
	var clockMu sync.Mutex
	clockTick := 0
	nextTime := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		clockTick++
		return fixedAt.Add(time.Duration(clockTick) * time.Millisecond)
	}
	controller.now = nextTime
	service.now = nextTime
	service.resolver.(*ConfigTargetResolver).now = nextTime
	controller.newID = func(prefix string) (string, error) { return prefix + ".fixture", nil }
	workload, err := CanonicalizeAuditWorkload(AuditWorkload{
		Kind: AuditWorkloadIdentity, StrategySetVersion: "1.0.0",
		Strategies: []StrategySelection{{StrategyID: BuiltinMetadataStrategyID, Enabled: true, Config: []byte(`{"sampleCount":2}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	job := auditJobFixture(t)
	job.Workload = workload
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	return store, service, controller, job
}

func updateAuditJobForCapability(
	t *testing.T,
	store *AuditSQLiteStore,
	job AuditJob,
	mode CapabilityPresetMode,
	maxRequests int,
) AuditJob {
	t.Helper()
	definition := auditJobDefinitionFromFixture(job)
	definition.Workload = AuditWorkload{
		Kind: AuditWorkloadCapability,
		CapabilityPreset: &VersionedRef{
			ID: "capability.preset.builtin." + string(mode), SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1",
		},
	}
	definition.Budget.MaxRequests = maxRequests
	if definition.Budget.MaxConcurrentRequests > maxRequests {
		definition.Budget.MaxConcurrentRequests = maxRequests
	}
	updated, err := UpdateAuditJob(job, job.Revision, definition, job.UpdatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateJob(context.Background(), updated, job.Revision); err != nil {
		t.Fatal(err)
	}
	return updated
}

type capabilityFixtureDoer struct{}

type capabilityBadGatewayDoer struct{}

type identityPartialFailureDoer struct {
	mu    sync.Mutex
	calls int
}

func (d *identityPartialFailureDoer) Do(request *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.calls++
	call := d.calls
	d.mu.Unlock()
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	if call == 1 {
		return &http.Response{
			StatusCode: http.StatusOK, Header: header,
			Body: io.NopCloser(strings.NewReader(`{"id":"resp-audit","model":"gpt-5.6-sol","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)), Request: request,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusGatewayTimeout, Header: header,
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"sensitive timeout detail"}}`)), Request: request,
	}, nil
}

func (capabilityBadGatewayDoer) Do(request *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: http.StatusBadGateway, Header: header,
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"sensitive upstream detail","type":"upstream_error"}}`)), Request: request,
	}, nil
}

func (capabilityFixtureDoer) Do(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	prompt := findCapabilityFixturePrompt(payload)
	if prompt == "" {
		return nil, fmt.Errorf("未找到能力任务提示")
	}
	responseBody, err := capabilityFixtureResponse(prompt)
	if err != nil {
		return nil, err
	}
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request,
	}, nil
}

func findCapabilityFixturePrompt(value any) string {
	switch current := value.(type) {
	case string:
		for _, marker := range []string{"只输出十进制整数", "阅读伪代码", "严格只输出", "必须调用 lookup_audit_record", "材料：", "仅依据给定材料"} {
			if strings.Contains(current, marker) {
				return current
			}
		}
	case []any:
		for _, item := range current {
			if prompt := findCapabilityFixturePrompt(item); prompt != "" {
				return prompt
			}
		}
	case map[string]any:
		for _, item := range current {
			if prompt := findCapabilityFixturePrompt(item); prompt != "" {
				return prompt
			}
		}
	}
	return ""
}

func capabilityFixtureResponse(prompt string) (string, error) {
	if strings.Contains(prompt, "lookup_audit_record") {
		var recordID int
		if _, err := fmt.Sscanf(prompt, "必须调用 lookup_audit_record 工具查询记录 %d；", &recordID); err != nil {
			return "", err
		}
		return fmt.Sprintf(`{"id":"cap-tool","model":"gpt-5.6-sol","status":"completed","output":[{"type":"function_call","call_id":"call-1","name":"lookup_audit_record","arguments":"{\"id\":%d}"}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`, recordID), nil
	}
	answer := ""
	switch {
	case strings.Contains(prompt, "17 * 19"):
		answer = "323"
	case strings.HasPrefix(prompt, "只输出十进制整数"):
		var left, right, extra int
		if _, err := fmt.Sscanf(prompt, "只输出十进制整数，不要解释：(%d * %d) + %d = ?", &left, &right, &extra); err != nil {
			return "", err
		}
		answer = fmt.Sprintf("%d", left*right+extra)
	case strings.HasPrefix(prompt, "阅读伪代码"):
		var initial, repeats int
		if _, err := fmt.Sscanf(prompt, "阅读伪代码并只输出最终整数：x=%d; repeat %d times", &initial, &repeats); err != nil {
			return "", err
		}
		answer = fmt.Sprintf("%d", initial+repeats*3)
	case strings.HasPrefix(prompt, "严格只输出"):
		start := strings.Index(prompt, "AUDIT-")
		end := strings.LastIndex(prompt, "\"")
		if start < 0 || end <= start {
			return "", fmt.Errorf("指令跟随提示无固定 token")
		}
		answer = prompt[start:end]
	case strings.Contains(prompt, "材料：A=蓝色"):
		start := strings.Index(prompt, "C=") + 2
		end := strings.Index(prompt[start:], "；")
		if start < 2 || end < 0 {
			return "", fmt.Errorf("长上下文提示无 C 值")
		}
		answer = prompt[start : start+end]
	case strings.Contains(prompt, "虚构行星 Arin"):
		answer = "Noma"
	default:
		return "", fmt.Errorf("未知能力任务提示 %q", prompt)
	}
	encodedAnswer, _ := json.Marshal(answer)
	return fmt.Sprintf(`{"id":"cap-text","model":"gpt-5.6-sol","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":%s}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`, encodedAnswer), nil
}

func waitForStoredAuditRun(t *testing.T, store *AuditSQLiteStore, runID string) AuditRun {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, found, err := store.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if found && run.Status.Terminal() {
			return run
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待审计运行 %s 终态超时", runID)
	return AuditRun{}
}

func waitForAuditSummaryReport(t *testing.T, store *AuditSQLiteStore, target AuditJobTarget, now time.Time) AuditChannelSummary {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		summary, err := store.GetLatestChannelSummary(context.Background(), target.ChannelID, target.ChannelKind, now)
		if err != nil {
			t.Fatal(err)
		}
		if summary.ReportID != "" {
			return summary
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待审计报告摘要超时")
	return AuditChannelSummary{}
}
