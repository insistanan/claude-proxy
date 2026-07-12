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
	// prompt_tokens=120 with cached_tokens=80: Claude client must see total occupancy 120,
	// not uncached-only 40 (Cursor auto-compact depends on total-style input_tokens).
	if !strings.Contains(got, `"input_tokens":120`) {
		t.Fatalf("missing total input_tokens (prompt includes cache); events:\n%s", got)
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
