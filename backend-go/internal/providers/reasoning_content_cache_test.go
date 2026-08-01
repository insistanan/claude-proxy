package providers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/types"
)

func TestReasoningContentCache_PutGet(t *testing.T) {
	clearReasoningContentCacheForTest()
	defer clearReasoningContentCacheForTest()

	storeReasoning("hello world", "think step by step")
	if got := lookupReasoning("hello world"); got != "think step by step" {
		t.Fatalf("lookupReasoning() = %q, want %q", got, "think step by step")
	}
	if got := lookupReasoning("another message"); got != "" {
		t.Fatalf("lookupReasoning(unknown) = %q, want empty", got)
	}

	// 空值不保存、不查询
	storeReasoning("", "should not store")
	storeReasoning("should not store", "")
	if got := lookupReasoning(""); got != "" {
		t.Fatalf("lookupReasoning(empty) = %q, want empty", got)
	}
	if got := lookupReasoning("should not store"); got != "" {
		t.Fatalf("lookupReasoning(empty reasoning) = %q, want empty", got)
	}
}

func TestReasoningContentCache_EvictsOldest(t *testing.T) {
	c := newReasoningContentCache(2)
	c.put("a", "1")
	c.put("b", "2")
	c.put("c", "3")
	if got := c.get("a"); got != "" {
		t.Fatalf("oldest entry not evicted, got %q", got)
	}
	if got := c.get("b"); got != "2" {
		t.Fatalf("b = %q, want 2", got)
	}
	if got := c.get("c"); got != "3" {
		t.Fatalf("c = %q, want 3", got)
	}
}

// TestReasoningContentRoundTrip_NonStream 覆盖非流式链路：
// 上游返回 reasoning_content → 代理转 Claude thinking → 客户端回传历史丢失明文 thinking
// → messages→chat 转换时自动补回 reasoning_content。
func TestReasoningContentRoundTrip_NonStream(t *testing.T) {
	clearReasoningContentCacheForTest()
	defer clearReasoningContentCacheForTest()

	// 1. 上游返回 reasoning_content
	providerResp := &types.ProviderResponse{
		Body: []byte(`{
			"id":"chatcmpl_test",
			"choices":[{"finish_reason":"stop","message":{
				"role":"assistant",
				"reasoning_content":"need inspect files",
				"content":"I will inspect the files."
			}}]
		}`),
	}
	claudeResp, err := (&OpenAIProvider{}).ConvertToClaudeResponse(providerResp)
	if err != nil {
		t.Fatalf("ConvertToClaudeResponse() err = %v", err)
	}
	if len(claudeResp.Content) != 2 || claudeResp.Content[0].Type != "thinking" {
		t.Fatalf("claude content = %#v", claudeResp.Content)
	}

	// 2. 下一轮客户端回传历史，assistant 消息只剩 text（明文 thinking 已丢失）
	c := newGinContext(http.MethodPost, "/v1/messages", []byte(`{
		"model":"reasoning-model",
		"messages":[
			{"role":"user","content":"inspect now"},
			{"role":"assistant","content":[{"type":"text","text":"I will inspect the files."}]}
		]
	}`), nil)
	upstream := &config.UpstreamConfig{BaseURL: "https://api.example.com", ServiceType: "openai"}

	req, _, err := (&OpenAIProvider{}).ConvertToProviderRequest(c, upstream, "sk-test")
	if err != nil {
		t.Fatalf("ConvertToProviderRequest() err = %v", err)
	}
	var got types.OpenAIRequest
	if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages = %#v", got.Messages)
	}
	assistant := got.Messages[1]
	if assistant.ReasoningContent != "need inspect files" {
		t.Fatalf("assistant reasoning_content = %q, want %q", assistant.ReasoningContent, "need inspect files")
	}
	if assistant.Content != "I will inspect the files." {
		t.Fatalf("assistant content = %#v", assistant.Content)
	}
}

// TestReasoningContentRoundTrip_Stream 覆盖流式链路（Claude Code 默认 stream=true）。
func TestReasoningContentRoundTrip_Stream(t *testing.T) {
	clearReasoningContentCacheForTest()
	defer clearReasoningContentCacheForTest()

	// 1. 上游流式返回 reasoning_content + content
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_test","model":"reasoning-model","choices":[{"index":0,"delta":{"reasoning_content":"need inspect"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl_test","model":"reasoning-model","choices":[{"index":0,"delta":{"content":"I will inspect."},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl_test","model":"reasoning-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	p := &OpenAIProvider{}
	eventChan, errChan, err := p.HandleStreamResponse(io.NopCloser(strings.NewReader(body)))
	if err != nil {
		t.Fatalf("HandleStreamResponse() err = %v", err)
	}
	for range eventChan {
	}
	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("stream err = %v", err)
		}
	default:
	}

	// 2. 下一轮补回（assistant 历史丢失明文 thinking）
	c := newGinContext(http.MethodPost, "/v1/messages", []byte(`{
		"model":"reasoning-model",
		"messages":[
			{"role":"user","content":"go"},
			{"role":"assistant","content":[{"type":"text","text":"I will inspect."}]}
		]
	}`), nil)
	upstream := &config.UpstreamConfig{BaseURL: "https://api.example.com", ServiceType: "openai"}

	req, _, err := p.ConvertToProviderRequest(c, upstream, "sk-test")
	if err != nil {
		t.Fatalf("ConvertToProviderRequest() err = %v", err)
	}
	var got types.OpenAIRequest
	if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages = %#v", got.Messages)
	}
	assistant := got.Messages[1]
	if assistant.ReasoningContent != "need inspect" {
		t.Fatalf("assistant reasoning_content = %q, want %q", assistant.ReasoningContent, "need inspect")
	}
	if assistant.Content != "I will inspect." {
		t.Fatalf("assistant content = %#v", assistant.Content)
	}
}

// TestConvertMessage_PrefersExplicitThinkingOverCache 确认补回不会覆盖客户端正常回传的明文 thinking。
func TestConvertMessage_PrefersExplicitThinkingOverCache(t *testing.T) {
	clearReasoningContentCacheForTest()
	defer clearReasoningContentCacheForTest()

	// 缓存里预置一条会命中同文本的 reasoning
	storeReasoning("I will inspect the files.", "cached reasoning")

	c := newGinContext(http.MethodPost, "/v1/messages", []byte(`{
		"model":"reasoning-model",
		"messages":[{"role":"assistant","content":[
			{"type":"thinking","thinking":"explicit reasoning"},
			{"type":"text","text":"I will inspect the files."}
		]}]
	}`), nil)
	upstream := &config.UpstreamConfig{BaseURL: "https://api.example.com", ServiceType: "openai"}

	req, _, err := (&OpenAIProvider{}).ConvertToProviderRequest(c, upstream, "sk-test")
	if err != nil {
		t.Fatalf("ConvertToProviderRequest() err = %v", err)
	}
	var got types.OpenAIRequest
	if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("messages = %#v", got.Messages)
	}
	if got.Messages[0].ReasoningContent != "explicit reasoning" {
		t.Fatalf("reasoning_content = %q, want explicit reasoning (cache must not override)", got.Messages[0].ReasoningContent)
	}
}

// TestConvertMessage_DoesNotBackfillToolCalls 确认纯工具调用 assistant 消息不触发补回（无文本键可匹配）。
func TestConvertMessage_DoesNotBackfillToolCalls(t *testing.T) {
	clearReasoningContentCacheForTest()
	defer clearReasoningContentCacheForTest()

	storeReasoning("I will inspect the files.", "cached reasoning")

	c := newGinContext(http.MethodPost, "/v1/messages", []byte(`{
		"model":"reasoning-model",
		"messages":[{"role":"assistant","content":[
			{"type":"tool_use","id":"call_1","name":"lookup","input":{}}
		]}]
	}`), nil)
	upstream := &config.UpstreamConfig{BaseURL: "https://api.example.com", ServiceType: "openai"}

	req, _, err := (&OpenAIProvider{}).ConvertToProviderRequest(c, upstream, "sk-test")
	if err != nil {
		t.Fatalf("ConvertToProviderRequest() err = %v", err)
	}
	var got types.OpenAIRequest
	if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("messages = %#v", got.Messages)
	}
	if got.Messages[0].ReasoningContent != "" {
		t.Fatalf("reasoning_content = %q, want empty for tool-only message", got.Messages[0].ReasoningContent)
	}
}
