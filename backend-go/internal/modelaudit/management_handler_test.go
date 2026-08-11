package modelaudit

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestAuditManagementHandlerCRUDAndExplicitErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := NewAuditSQLiteStore(filepath.Join(t.TempDir(), "management.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	management, err := NewAuditManagementService(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture := auditJobFixture(t)
	management.now = func() time.Time { return fixture.CreatedAt }
	management.newJobID = func() (string, error) { return "audit-job-handler", nil }
	router := gin.New()
	if err := RegisterRoutes(router.Group("/api"), &Service{}, management); err != nil {
		t.Fatal(err)
	}

	definition := auditJobDefinitionFromFixture(fixture)
	createBody, err := json.Marshal(CreateAuditJobRequest{Definition: definition, InitialStatus: AuditJobEnabled})
	if err != nil {
		t.Fatal(err)
	}
	created := performAuditRequest(router, http.MethodPost, "/api/model-audit/jobs", createBody)
	if created.Code != http.StatusCreated {
		t.Fatalf("创建任务状态 = %d body=%s", created.Code, created.Body.String())
	}
	var createdResponse AuditJobResponse
	if err := json.Unmarshal(created.Body.Bytes(), &createdResponse); err != nil {
		t.Fatal(err)
	}
	if createdResponse.Job.ID != "audit-job-handler" || createdResponse.Job.Status != AuditJobEnabled {
		t.Fatalf("创建任务响应 = %#v", createdResponse)
	}

	listed := performAuditRequest(router, http.MethodGet, "/api/model-audit/jobs?page=1&pageSize=10", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("任务列表状态 = %d body=%s", listed.Code, listed.Body.String())
	}
	var listedResponse AuditJobPageResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &listedResponse); err != nil {
		t.Fatal(err)
	}
	if listedResponse.Page.Total != 1 || len(listedResponse.Page.Jobs) != 1 {
		t.Fatalf("任务列表响应 = %#v", listedResponse)
	}

	missing := performAuditRequest(router, http.MethodGet, "/api/model-audit/jobs/audit-job-missing", nil)
	assertAuditAPIError(t, missing, http.StatusNotFound, ErrorCodeAuditNotFound)
	badPage := performAuditRequest(router, http.MethodGet, "/api/model-audit/jobs?pageSize=201", nil)
	assertAuditAPIError(t, badPage, http.StatusBadRequest, ErrorCodeInvalidRequest)

	conflictBody, err := json.Marshal(UpdateAuditJobRequest{ExpectedRevision: 99, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	conflict := performAuditRequest(router, http.MethodPut, "/api/model-audit/jobs/audit-job-handler", conflictBody)
	assertAuditAPIError(t, conflict, http.StatusConflict, ErrorCodeConflict)

	var createObject map[string]any
	if err := json.Unmarshal(createBody, &createObject); err != nil {
		t.Fatal(err)
	}
	createObject["unexpected"] = true
	unknownBody, err := json.Marshal(createObject)
	if err != nil {
		t.Fatal(err)
	}
	unknown := performAuditRequest(router, http.MethodPost, "/api/model-audit/jobs", unknownBody)
	assertAuditAPIError(t, unknown, http.StatusBadRequest, ErrorCodeInvalidRequest)

	summary := performAuditRequest(
		router,
		http.MethodGet,
		"/api/model-audit/channels/responses/channel-responses/summary",
		nil,
	)
	if summary.Code != http.StatusOK {
		t.Fatalf("渠道摘要状态 = %d body=%s", summary.Code, summary.Body.String())
	}
	var summaryResponse AuditChannelSummaryResponse
	if err := json.Unmarshal(summary.Body.Bytes(), &summaryResponse); err != nil {
		t.Fatal(err)
	}
	if summaryResponse.Summary.PrimaryStatus != AuditSummaryNotDetected {
		t.Fatalf("未检测渠道摘要 = %#v", summaryResponse)
	}

	manualBody, err := json.Marshal(StartManualAuditRunRequest{ExpectedRevision: createdResponse.Job.Revision})
	if err != nil {
		t.Fatal(err)
	}
	manual := performAuditRequest(router, http.MethodPost, "/api/model-audit/jobs/audit-job-handler/runs", manualBody)
	assertAuditAPIError(t, manual, http.StatusUnprocessableEntity, ErrorCodeUnsupported)
	export := performAuditRequest(router, http.MethodGet, "/api/model-audit/reports/report-missing/export.html", nil)
	assertAuditAPIError(t, export, http.StatusUnprocessableEntity, ErrorCodeUnsupported)

	transitionBody, err := json.Marshal(TransitionAuditJobRequest{ExpectedRevision: createdResponse.Job.Revision, Status: AuditJobPaused})
	if err != nil {
		t.Fatal(err)
	}
	transitioned := performAuditRequest(router, http.MethodPost, "/api/model-audit/jobs/audit-job-handler/transition", transitionBody)
	if transitioned.Code != http.StatusOK {
		t.Fatalf("暂停任务状态 = %d body=%s", transitioned.Code, transitioned.Body.String())
	}
	var transitionedResponse AuditJobResponse
	if err := json.Unmarshal(transitioned.Body.Bytes(), &transitionedResponse); err != nil {
		t.Fatal(err)
	}
	if transitionedResponse.Job.Status != AuditJobPaused || transitionedResponse.Job.Revision != createdResponse.Job.Revision+1 {
		t.Fatalf("暂停任务响应 = %#v", transitionedResponse)
	}

	missingDeleteRevision := performAuditRequest(router, http.MethodDelete, "/api/model-audit/jobs/audit-job-handler", nil)
	assertAuditAPIError(t, missingDeleteRevision, http.StatusBadRequest, ErrorCodeInvalidRequest)
	deleted := performAuditRequest(
		router,
		http.MethodDelete,
		"/api/model-audit/jobs/audit-job-handler?expectedRevision=2",
		nil,
	)
	if deleted.Code != http.StatusOK {
		t.Fatalf("删除任务状态 = %d body=%s", deleted.Code, deleted.Body.String())
	}
}

func performAuditRequest(router http.Handler, method, target string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func assertAuditAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code ErrorCode) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("错误状态 = %d，期望 %d，body=%s", response.Code, status, response.Body.String())
	}
	var apiError APIErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &apiError); err != nil {
		t.Fatal(err)
	}
	if apiError.Error.Code != code {
		t.Fatalf("错误代码 = %q，期望 %q，body=%s", apiError.Error.Code, code, response.Body.String())
	}
}
