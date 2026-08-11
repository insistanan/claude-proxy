package modelaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/gin-gonic/gin"
)

type staticConfigProvider struct {
	cfg config.Config
}

func (p staticConfigProvider) GetConfig() config.Config {
	return p.cfg
}

func newTestService(t *testing.T, cfg config.Config, doer HTTPDoer, serviceConfig ...ServiceConfig) *Service {
	t.Helper()
	resolver, err := NewConfigTargetResolver(staticConfigProvider{cfg: cfg})
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultServiceConfig()
	if len(serviceConfig) > 0 {
		limits = serviceConfig[0]
	}
	service, err := NewService(resolver, StaticHTTPClientFactory{Doer: doer}, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return service
}

func waitForTerminalResult(t *testing.T, service *Service, executionID string) ExecutionResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		detail, ok := service.Get(executionID)
		if !ok {
			t.Fatalf("执行 %s 不存在", executionID)
		}
		if detail.Result.Status.Terminal() {
			return detail.Result
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待执行 %s 终态超时", executionID)
	return ExecutionResult{}
}

func TestUnifiedServiceExecutesAllFiveProtocols(t *testing.T) {
	expectedPaths := map[string]bool{
		"/v1/messages":         false,
		"/v1/responses":        false,
		"/v1/chat/completions": false,
		"/v1beta/models/test-model:generateContent": false,
		"/v1/images/generations":                    false,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := expectedPaths[r.URL.Path]; !ok {
			t.Errorf("意外请求路径 %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		expectedPaths[r.URL.Path] = true
		if r.URL.Path == "/v1beta/models/test-model:generateContent" {
			if r.Header.Get("X-Goog-Api-Key") != "test-key" {
				t.Errorf("Gemini 认证头错误: %v", r.Header)
			}
		} else if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("认证头错误: %v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/messages":
			_, _ = w.Write([]byte(`{"id":"msg-1","model":"test-model","content":[{"type":"text","text":"messages ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
		case "/v1/responses":
			_, _ = w.Write([]byte(`{"id":"resp-1","model":"test-model","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"responses ok"}]}],"usage":{"input_tokens":2,"output_tokens":3}}`))
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"id":"chat-1","model":"test-model","choices":[{"message":{"content":"chat ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`))
		case "/v1beta/models/test-model:generateContent":
			_, _ = w.Write([]byte(`{"responseId":"gemini-1","modelVersion":"test-model","candidates":[{"content":{"parts":[{"text":"gemini ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3}}`))
		case "/v1/images/generations":
			_, _ = w.Write([]byte(`{"data":[{"url":"https://example.invalid/image.png"}]}`))
		}
	}))
	defer server.Close()

	cfg := config.Config{
		Upstream:          []config.UpstreamConfig{{ID: "messages-1", BaseURL: server.URL, APIKeys: []string{"test-key"}, ServiceType: "claude", DefaultModel: "test-model"}},
		ResponsesUpstream: []config.UpstreamConfig{{ID: "responses-1", BaseURL: server.URL, APIKeys: []string{"test-key"}, ServiceType: "responses", DefaultModel: "test-model"}},
		ChatUpstream:      []config.UpstreamConfig{{ID: "chat-1", BaseURL: server.URL, APIKeys: []string{"test-key"}, ServiceType: "openai", DefaultModel: "test-model"}},
		GeminiUpstream:    []config.UpstreamConfig{{ID: "gemini-1", BaseURL: server.URL, APIKeys: []string{"test-key"}, ServiceType: "gemini", DefaultModel: "test-model"}},
		ImagesUpstream:    []config.UpstreamConfig{{ID: "images-1", BaseURL: server.URL, APIKeys: []string{"test-key"}, ServiceType: "openai", DefaultModel: "test-model"}},
	}
	service := newTestService(t, cfg, server.Client())

	cases := []struct {
		protocol Protocol
		channel  string
		text     string
	}{
		{ProtocolMessages, "messages-1", "messages ok"},
		{ProtocolResponses, "responses-1", "responses ok"},
		{ProtocolChat, "chat-1", "chat ok"},
		{ProtocolGemini, "gemini-1", "gemini ok"},
		{ProtocolImages, "images-1", ""},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.protocol), func(t *testing.T) {
			spec := validSpec(testCase.protocol, testCase.channel)
			spec.Stream = false
			started, err := service.Start(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			result := waitForTerminalResult(t, service, started.ExecutionID)
			if result.Status != StatusCompleted {
				t.Fatalf("状态 = %s，错误 = %#v", result.Status, result.Failure)
			}
			if result.Text != testCase.text {
				t.Fatalf("正文 = %q，期望 %q", result.Text, testCase.text)
			}
			if result.ProtocolTerminal != ProtocolTerminalCompleted {
				t.Fatalf("协议终态 = %q", result.ProtocolTerminal)
			}
		})
	}
	for path, called := range expectedPaths {
		if !called {
			t.Errorf("端点 %s 未被调用", path)
		}
	}
}

func TestHandlerExecutionLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-handler","model":"test-model","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"handler ok"}]}]}`))
	}))
	defer upstream.Close()

	cfg := config.Config{ResponsesUpstream: []config.UpstreamConfig{{
		ID: "responses-1", BaseURL: upstream.URL, APIKeys: []string{"test-key"}, ServiceType: "responses", DefaultModel: "test-model",
	}}}
	service := newTestService(t, cfg, upstream.Client())
	router := gin.New()
	if err := RegisterRoutes(router.Group("/api"), service); err != nil {
		t.Fatal(err)
	}

	capabilitiesRecorder := httptest.NewRecorder()
	router.ServeHTTP(capabilitiesRecorder, httptest.NewRequest(http.MethodGet, "/api/model-audit/capabilities", nil))
	if capabilitiesRecorder.Code != http.StatusOK {
		t.Fatalf("能力 API 状态码 = %d，响应 = %s", capabilitiesRecorder.Code, capabilitiesRecorder.Body.String())
	}
	var capabilities CapabilitiesResponse
	if err := json.Unmarshal(capabilitiesRecorder.Body.Bytes(), &capabilities); err != nil || len(capabilities.Protocols) != 5 {
		t.Fatalf("能力 API 响应无效: %v, %#v", err, capabilities)
	}

	spec := validSpec(ProtocolResponses, "responses-1")
	payload, err := json.Marshal(CreateExecutionRequest{
		Purpose: spec.Purpose, ChannelID: spec.Target.ChannelID, ChannelKind: spec.Target.ChannelKind,
		Protocol: spec.Protocol, Model: spec.Model, Thinking: spec.Thinking, RequestProfile: spec.RequestProfile,
		Stream: false, TimeoutMillis: spec.TimeoutMillis, MaxOutputTokens: spec.MaxOutputTokens,
		Features: spec.Features, Input: spec.Input, Conversation: spec.Conversation, Redaction: spec.Redaction,
	})
	if err != nil {
		t.Fatal(err)
	}
	createRecorder := httptest.NewRecorder()
	createRequest := httptest.NewRequest(http.MethodPost, "/api/model-audit/executions", bytes.NewReader(payload))
	createRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusAccepted {
		t.Fatalf("创建 API 状态码 = %d，响应 = %s", createRecorder.Code, createRecorder.Body.String())
	}
	var created CreateExecutionResponse
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &created); err != nil || created.ExecutionID == "" {
		t.Fatalf("创建 API 响应无效: %v, %#v", err, created)
	}
	waitForTerminalResult(t, service, created.ExecutionID)

	detailRecorder := httptest.NewRecorder()
	router.ServeHTTP(detailRecorder, httptest.NewRequest(http.MethodGet, "/api/model-audit/executions/"+created.ExecutionID, nil))
	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("详情 API 状态码 = %d，响应 = %s", detailRecorder.Code, detailRecorder.Body.String())
	}
	var detail ExecutionDetailResponse
	if err := json.Unmarshal(detailRecorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Result.Status != StatusCompleted || detail.Result.Text != "handler ok" || detail.Result.RequestSummary.Profile == "" {
		t.Fatalf("详情 API 响应 = %#v", detail.Result)
	}

	invalidRecorder := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(http.MethodPost, "/api/model-audit/executions", strings.NewReader(`{"unknown":true}`))
	invalidRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(invalidRecorder, invalidRequest)
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("非法请求状态码 = %d，响应 = %s", invalidRecorder.Code, invalidRecorder.Body.String())
	}
	var apiError APIErrorResponse
	if err := json.Unmarshal(invalidRecorder.Body.Bytes(), &apiError); err != nil || apiError.Error.Message == "" {
		t.Fatalf("错误 API 响应无效: %v, %#v", err, apiError)
	}
}

func TestResponsesSSETerminalMatrix(t *testing.T) {
	completedResponse := `{"id":"resp-1","model":"test-model","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}`
	event := func(sequence int, eventType string, fields string) string {
		if fields != "" {
			fields = "," + fields
		}
		return fmt.Sprintf("data: {\"type\":%q,\"sequence_number\":%d%s}\n\n", eventType, sequence, fields)
	}
	delta := event(1, "response.output_text.delta", `"delta":"hello"`)
	cases := []struct {
		name       string
		stream     string
		wantStatus ExecutionStatus
		wantError  bool
	}{
		{
			name: "completed",
			stream: event(0, "response.created", `"response":{"id":"resp-1"}`) + delta +
				event(2, "response.completed", `"response":`+completedResponse),
			wantStatus: StatusCompleted,
		},
		{
			name:       "failed",
			stream:     event(0, "response.failed", `"response":{"status":"failed","error":{"message":"upstream failed"}}`),
			wantStatus: StatusFailed,
		},
		{
			name:       "incomplete",
			stream:     event(0, "response.incomplete", `"response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`),
			wantStatus: StatusIncomplete,
		},
		{
			name:       "empty output",
			stream:     event(0, "response.completed", `"response":{"id":"resp-empty","status":"completed","output":[]}`),
			wantStatus: StatusEmptyOutput,
		},
		{
			name:       "no terminal",
			stream:     delta,
			wantStatus: StatusStreamTruncated,
		},
		{
			name:       "duplicate exact event",
			stream:     delta + delta + event(2, "response.completed", `"response":`+completedResponse),
			wantStatus: StatusCompleted,
		},
		{
			name: "out of order",
			stream: event(2, "response.in_progress", `"response":{"id":"resp-1"}`) +
				event(1, "response.output_text.delta", `"delta":"hello"`),
			wantError: true,
		},
		{
			name: "terminal text mismatch",
			stream: event(0, "response.output_text.delta", `"delta":"different"`) +
				event(1, "response.completed", `"response":`+completedResponse),
			wantError: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := ExecutionResult{}
			err := parseResponsesSSE(strings.NewReader(testCase.stream), &result)
			if (err != nil) != testCase.wantError {
				t.Fatalf("错误 = %v，wantError=%v", err, testCase.wantError)
			}
			if !testCase.wantError && result.Status != testCase.wantStatus {
				t.Fatalf("状态 = %s，期望 %s，错误=%#v", result.Status, testCase.wantStatus, result.Failure)
			}
		})
	}
}

func TestStreamingToolCalls(t *testing.T) {
	cases := []struct {
		name     string
		stream   string
		parse    func(io.Reader, *ExecutionResult) error
		toolName string
	}{
		{
			name: "messages",
			stream: "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-tool\",\"model\":\"test-model\"}}\n\n" +
				"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool-1\",\"name\":\"weather\",\"input\":{}}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"Shanghai\\\"}\"}}\n\n" +
				"data: {\"type\":\"message_stop\"}\n\n",
			parse:    parseMessagesSSE,
			toolName: "weather",
		},
		{
			name: "chat",
			stream: "data: {\"id\":\"chat-tool\",\"model\":\"test-model\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"function\":{\"name\":\"weather\",\"arguments\":\"{\\\"city\\\":\"}}]}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"Shanghai\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n",
			parse:    parseChatSSE,
			toolName: "weather",
		},
		{
			name:     "gemini",
			stream:   "data: {\"responseId\":\"gemini-tool\",\"modelVersion\":\"test-model\",\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"weather\",\"args\":{\"city\":\"Shanghai\"}}}]},\"finishReason\":\"STOP\"}]}\n\n",
			parse:    parseGeminiSSE,
			toolName: "weather",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := ExecutionResult{}
			if err := testCase.parse(strings.NewReader(testCase.stream), &result); err != nil {
				t.Fatal(err)
			}
			if result.Status != StatusCompleted {
				t.Fatalf("状态 = %s，错误 = %#v", result.Status, result.Failure)
			}
			if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != testCase.toolName {
				t.Fatalf("工具调用 = %#v", result.ToolCalls)
			}
			var arguments map[string]interface{}
			if err := json.Unmarshal(result.ToolCalls[0].Arguments, &arguments); err != nil {
				t.Fatalf("工具参数不是 JSON 对象: %s: %v", result.ToolCalls[0].Arguments, err)
			}
			if arguments["city"] != "Shanghai" {
				t.Fatalf("工具参数 = %#v", arguments)
			}
		})
	}
}

func TestJSONToolCallArguments(t *testing.T) {
	cases := []struct {
		protocol Protocol
		body     string
	}{
		{
			protocol: ProtocolMessages,
			body:     `{"id":"msg-tool","content":[{"type":"tool_use","id":"tool-1","name":"weather","input":{"city":"Shanghai"}}]}`,
		},
		{
			protocol: ProtocolResponses,
			body:     `{"id":"resp-tool","status":"completed","output":[{"type":"function_call","call_id":"call-1","name":"weather","arguments":"{\"city\":\"Shanghai\"}"}]}`,
		},
		{
			protocol: ProtocolChat,
			body:     `{"id":"chat-tool","choices":[{"message":{"tool_calls":[{"id":"call-1","function":{"name":"weather","arguments":"{\"city\":\"Shanghai\"}"}}]},"finish_reason":"tool_calls"}]}`,
		},
		{
			protocol: ProtocolGemini,
			body:     `{"responseId":"gemini-tool","candidates":[{"content":{"parts":[{"functionCall":{"name":"weather","args":{"city":"Shanghai"}}}]},"finishReason":"STOP"}]}`,
		},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.protocol), func(t *testing.T) {
			result := ExecutionResult{}
			if err := parseJSONResponse([]byte(testCase.body), testCase.protocol, &result); err != nil {
				t.Fatal(err)
			}
			if result.Status != StatusCompleted || len(result.ToolCalls) != 1 {
				t.Fatalf("结果 = %#v", result)
			}
			var arguments map[string]interface{}
			if err := json.Unmarshal(result.ToolCalls[0].Arguments, &arguments); err != nil {
				t.Fatalf("工具参数不是 JSON 对象: %s: %v", result.ToolCalls[0].Arguments, err)
			}
			if arguments["city"] != "Shanghai" {
				t.Fatalf("工具参数 = %#v", arguments)
			}
		})
	}
}

func TestResponsesPayloadIncludesPreviousResponseID(t *testing.T) {
	spec := validSpec(ProtocolResponses, "responses-1")
	spec.Conversation.PreviousResponseID = "resp-previous"
	target := TargetSnapshot{ResolvedModel: "test-model", WireProtocol: ProtocolResponses}
	body, endpoint, err := buildResponsesPayload(spec, target)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "/responses" {
		t.Fatalf("端点 = %s", endpoint)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["previous_response_id"] != "resp-previous" {
		t.Fatalf("多轮 ID 未写入请求: %s", body)
	}
}

type blockingDoer struct{}

func (blockingDoer) Do(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func TestServiceCancellation(t *testing.T) {
	cfg := config.Config{ResponsesUpstream: []config.UpstreamConfig{{
		ID: "responses-1", BaseURL: "https://example.invalid", APIKeys: []string{"test-key"}, ServiceType: "responses", DefaultModel: "test-model",
	}}}
	service := newTestService(t, cfg, blockingDoer{})
	spec := validSpec(ProtocolResponses, "responses-1")
	started, err := service.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Cancel(context.Background(), started.ExecutionID); err != nil {
		t.Fatal(err)
	}
	result := waitForTerminalResult(t, service, started.ExecutionID)
	if result.Status != StatusCancelled {
		t.Fatalf("取消后状态 = %s，错误=%#v", result.Status, result.Failure)
	}
}
