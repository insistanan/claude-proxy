package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/sensitive"
	"github.com/gin-gonic/gin"
)

func TestBlockedLogsHandlersCRUD(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := sensitive.NewBlockedStore(filepath.Join(t.TempDir(), "blocked.db"))
	if err != nil {
		t.Fatalf("创建存储失败: %v", err)
	}
	defer store.Close()
	first, err := store.Record(context.Background(), sensitive.BlockedLog{
		Timestamp: time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC), APIType: "messages",
		BlockType: sensitive.BlockTypeSensitiveWord, RuleName: "gambling", RequestID: "req-1",
	})
	if err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	if _, err := store.Record(context.Background(), sensitive.BlockedLog{
		Timestamp: time.Date(2026, 8, 10, 11, 0, 0, 0, time.UTC), APIType: "responses",
		BlockType: sensitive.BlockTypeDangerousCmd, RuleName: "destructive", RequestID: "req-2",
	}); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	if _, err := store.Record(context.Background(), sensitive.BlockedLog{
		Timestamp: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC), APIType: "chat",
		BlockType: sensitive.BlockTypeCredential, RuleName: "api_key", RequestID: "req-3",
	}); err != nil {
		t.Fatalf("写入凭据记录失败: %v", err)
	}

	router := gin.New()
	router.GET("/blocked-logs", GetBlockedLogs(store))
	router.GET("/blocked-logs/:id", GetBlockedLog(store))
	router.DELETE("/blocked-logs/:id", DeleteBlockedLog(store))
	router.DELETE("/blocked-logs", ClearBlockedLogs(store))

	response := performBlockedLogRequest(router, http.MethodGet, "/blocked-logs?page=1&pageSize=1&blockType=dangerous_cmd")
	if response.Code != http.StatusOK {
		t.Fatalf("列表状态码错误: %d %s", response.Code, response.Body.String())
	}
	var page sensitive.BlockedLogPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	if page.Total != 1 || len(page.Logs) != 1 || page.Logs[0].RequestID != "req-2" {
		t.Fatalf("列表结果错误: %+v", page)
	}

	response = performBlockedLogRequest(router, http.MethodGet, "/blocked-logs?page=1&pageSize=20&blockType=credential")
	if response.Code != http.StatusOK {
		t.Fatalf("凭据筛选状态码错误: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析凭据筛选列表失败: %v", err)
	}
	if page.Total != 1 || len(page.Logs) != 1 || page.Logs[0].RequestID != "req-3" {
		t.Fatalf("凭据筛选结果错误: %+v", page)
	}

	response = performBlockedLogRequest(router, http.MethodGet, "/blocked-logs?page=1&pageSize=20&groupBy=conversation")
	if response.Code != http.StatusOK {
		t.Fatalf("会话分组状态码错误: %d %s", response.Code, response.Body.String())
	}
	var groups sensitive.BlockedLogGroupPage
	if err := json.Unmarshal(response.Body.Bytes(), &groups); err != nil {
		t.Fatalf("解析会话分组失败: %v", err)
	}
	if groups.Total != 3 || groups.TotalGroups != 3 || len(groups.Groups) != 3 {
		t.Fatalf("会话分组结果错误: %+v", groups)
	}
	// 分组列表只返回摘要，每组携带最新代表记录而非全量明细。
	for _, group := range groups.Groups {
		if group.Count != 1 || group.LatestLog == nil {
			t.Fatalf("会话分组摘要错误: %+v", group)
		}
	}

	// groupKey 查询组内明细分页。
	groupKey := "request:" + groups.Groups[0].LatestLog.RequestID
	detailResponse := performBlockedLogRequest(router, http.MethodGet, "/blocked-logs?page=1&pageSize=20&groupKey="+groupKey)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("组内明细状态码错误: %d %s", detailResponse.Code, detailResponse.Body.String())
	}
	var details sensitive.BlockedLogPage
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &details); err != nil {
		t.Fatalf("解析组内明细失败: %v", err)
	}
	if details.Total != 1 || len(details.Logs) != 1 || details.Logs[0].RequestID != groups.Groups[0].LatestLog.RequestID {
		t.Fatalf("组内明细结果错误: %+v", details)
	}

	// 无效 groupKey 返回 400。
	badResponse := performBlockedLogRequest(router, http.MethodGet, "/blocked-logs?groupKey=bad-key")
	if badResponse.Code != http.StatusBadRequest {
		t.Fatalf("无效 groupKey 状态码错误: %d", badResponse.Code)
	}

	response = performBlockedLogRequest(router, http.MethodGet, "/blocked-logs/"+formatBlockedLogID(first.ID))
	if response.Code != http.StatusOK {
		t.Fatalf("详情状态码错误: %d %s", response.Code, response.Body.String())
	}
	var detail sensitive.BlockedLog
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil || detail.RequestID != "req-1" {
		t.Fatalf("详情结果错误: %+v %v", detail, err)
	}

	response = performBlockedLogRequest(router, http.MethodDelete, "/blocked-logs/"+formatBlockedLogID(first.ID))
	if response.Code != http.StatusNoContent {
		t.Fatalf("删除状态码错误: %d %s", response.Code, response.Body.String())
	}
	response = performBlockedLogRequest(router, http.MethodGet, "/blocked-logs/"+formatBlockedLogID(first.ID))
	if response.Code != http.StatusNotFound {
		t.Fatalf("删除后详情状态码错误: %d", response.Code)
	}

	response = performBlockedLogRequest(router, http.MethodDelete, "/blocked-logs")
	if response.Code != http.StatusOK {
		t.Fatalf("清空状态码错误: %d %s", response.Code, response.Body.String())
	}
	page, err = store.List(context.Background(), sensitive.BlockedLogListOptions{})
	if err != nil || page.Total != 0 {
		t.Fatalf("清空后仍有记录: %+v %v", page, err)
	}
}

func TestBlockedLogsHandlersValidateQueriesAndStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/blocked-logs", GetBlockedLogs(nil))
	response := performBlockedLogRequest(router, http.MethodGet, "/blocked-logs")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("未初始化存储状态码错误: %d", response.Code)
	}

	store, err := sensitive.NewBlockedStore(filepath.Join(t.TempDir(), "blocked.db"))
	if err != nil {
		t.Fatalf("创建存储失败: %v", err)
	}
	defer store.Close()
	router = gin.New()
	router.GET("/blocked-logs", GetBlockedLogs(store))
	for _, path := range []string{
		"/blocked-logs?page=0",
		"/blocked-logs?pageSize=101",
		"/blocked-logs?apiType=unknown",
		"/blocked-logs?blockType=unknown",
		"/blocked-logs?groupBy=request",
		"/blocked-logs?from=invalid",
		"/blocked-logs?from=2026-08-11T00:00:00Z&to=2026-08-10T00:00:00Z",
	} {
		response = performBlockedLogRequest(router, http.MethodGet, path)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("无效参数 %q 状态码错误: %d %s", path, response.Code, response.Body.String())
		}
	}
}

func performBlockedLogRequest(router http.Handler, method, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func formatBlockedLogID(id int64) string {
	return strconv.FormatInt(id, 10)
}
