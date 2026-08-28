package providers

import (
	"io"
	"strings"
	"testing"
)

func TestOpenAIProviderHandleStreamResponse_EmptyToolCallsKeepsSingleTextBlock(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_test","model":"z-ai/glm-5.1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl_test","model":"z-ai/glm-5.1","choices":[{"index":0,"delta":{"content":"哈哈","tool_calls":[]},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl_test","model":"z-ai/glm-5.1","choices":[{"index":0,"delta":{"content":"，谢谢夸奖","tool_calls":[]},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl_test","model":"z-ai/glm-5.1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	p := &OpenAIProvider{}
	eventChan, errChan, err := p.HandleStreamResponse(io.NopCloser(strings.NewReader(body)))
	if err != nil {
		t.Fatalf("HandleStreamResponse() err = %v", err)
	}

	var events strings.Builder
	for event := range eventChan {
		events.WriteString(event)
	}
	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("stream err = %v", err)
		}
	default:
	}

	got := strings.Count(events.String(), "event: content_block_start")
	if got != 1 {
		t.Fatalf("content_block_start count = %d, want 1; events:\n%s", got, events.String())
	}

	if !strings.Contains(events.String(), `"text":"哈哈，谢谢夸奖"`) {
		t.Fatalf("missing expected text deltas; events:\n%s", events.String())
	}
}

func TestOpenAIProviderHandleStreamResponse_EmitsReasoningAsThinking(t *testing.T) {
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

	var events strings.Builder
	for event := range eventChan {
		events.WriteString(event)
	}
	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("stream err = %v", err)
		}
	default:
	}

	got := events.String()
	if !strings.Contains(got, `"type":"thinking"`) || !strings.Contains(got, `"type":"thinking_delta"`) {
		t.Fatalf("missing thinking events:\n%s", got)
	}
	if !strings.Contains(got, `"thinking":"need inspect"`) {
		t.Fatalf("missing reasoning delta:\n%s", got)
	}
	if !strings.Contains(got, `"text":"I will inspect."`) {
		t.Fatalf("missing text events:\n%s", got)
	}
}

func TestOpenAIProviderHandleStreamResponse_EmitsEmbeddedThinkingAsThinking(t *testing.T) {
	body := strings.Join([]string{
		`data: {"model":"reasoning-model","choices":[{"delta":{"content":"<think>private"}}]}`,
		`data: {"model":"reasoning-model","choices":[{"delta":{"content":" reasoning</think>Final answer."}}]}`,
		`data: {"model":"reasoning-model","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	eventChan, _, err := (&OpenAIProvider{}).HandleStreamResponse(io.NopCloser(strings.NewReader(body)))
	if err != nil {
		t.Fatalf("HandleStreamResponse() err = %v", err)
	}
	var events strings.Builder
	for event := range eventChan {
		events.WriteString(event)
	}
	got := events.String()
	if !strings.Contains(got, `"type":"thinking_delta"`) || !strings.Contains(got, "private reasoning") {
		t.Fatalf("missing thinking events:\n%s", got)
	}
	if !strings.Contains(got, `"text":"Final answer."`) || strings.Contains(got, `"text":"<think>`) {
		t.Fatalf("text events leaked thinking tags:\n%s", got)
	}
}

func TestOpenAIProviderHandleStreamResponse_MapsCachedUsage(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_test","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl_test","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: {"id":"chatcmpl_test","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":120,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":80}}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	p := &OpenAIProvider{}
	eventChan, errChan, err := p.HandleStreamResponse(io.NopCloser(strings.NewReader(body)))
	if err != nil {
		t.Fatalf("HandleStreamResponse() err = %v", err)
	}

	var events strings.Builder
	for event := range eventChan {
		events.WriteString(event)
	}
	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("stream err = %v", err)
		}
	default:
	}

	got := events.String()
	// prompt_tokens=120 且 prompt_tokens_details.cached_tokens=80（OpenAI 语义：cached ⊆ prompt）。
	// Anthropic 格式出口必须把 input_tokens 报成 uncached 余量 40，缓存量另由
	// cache_read_input_tokens 单列 —— 客户端按契约求和 40+80 才回到真实占用 120。
	// 若这里改回 120，规范客户端会算成 200，上下文满度虚高 67%。
	if !strings.Contains(got, `"input_tokens":40`) {
		t.Fatalf("input_tokens 应为 uncached 余量 40（120-80）; events:\n%s", got)
	}
	if !strings.Contains(got, `"cache_read_input_tokens":80`) {
		t.Fatalf("missing cache_read_input_tokens; events:\n%s", got)
	}
	if !strings.Contains(got, `"cached_tokens":80`) {
		t.Fatalf("missing input_tokens_details.cached_tokens; events:\n%s", got)
	}
}

func TestOpenAIProviderHandleStreamResponse_StreamsToolCallArguments(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_test","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl_test","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"run_command","arguments":"{"}}]},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl_test","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"cmd\":\"pwd\""}}]},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl_test","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl_test","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		``,
	}, "\n")

	p := &OpenAIProvider{}
	eventChan, errChan, err := p.HandleStreamResponse(io.NopCloser(strings.NewReader(body)))
	if err != nil {
		t.Fatalf("HandleStreamResponse() err = %v", err)
	}

	var events strings.Builder
	for event := range eventChan {
		events.WriteString(event)
	}
	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("stream err = %v", err)
		}
	default:
	}

	got := events.String()
	if !strings.Contains(got, `"type":"tool_use"`) {
		t.Fatalf("missing streamed tool_use start event:\n%s", got)
	}
	if !strings.Contains(got, `"input":{}`) {
		t.Fatalf("missing initial tool_use input object:\n%s", got)
	}
	if count := strings.Count(got, `"type":"input_json_delta"`); count != 3 {
		t.Fatalf("input_json_delta count = %d, want 3; events:\n%s", count, got)
	}
	if strings.Index(got, `"type":"tool_use"`) > strings.Index(got, `"type":"input_json_delta"`) {
		t.Fatalf("tool_use start should precede argument deltas:\n%s", got)
	}
}

func TestProcessToolUsePartIncludesInitialInputObject(t *testing.T) {
	events := strings.Join(processToolUsePart("call_1", "edit_file", map[string]interface{}{"path": "main.go"}, 0), "")

	if !strings.Contains(events, `"type":"tool_use"`) {
		t.Fatalf("missing tool_use start event:\n%s", events)
	}
	if !strings.Contains(events, `"input":{}`) {
		t.Fatalf("missing initial tool_use input object:\n%s", events)
	}
	if !strings.Contains(events, `"type":"input_json_delta"`) {
		t.Fatalf("missing input_json_delta event:\n%s", events)
	}
}
