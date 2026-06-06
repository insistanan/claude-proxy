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
	if !strings.Contains(got, `"input_tokens":40`) {
		t.Fatalf("missing adjusted input_tokens; events:\n%s", got)
	}
	if !strings.Contains(got, `"cache_read_input_tokens":80`) {
		t.Fatalf("missing cache_read_input_tokens; events:\n%s", got)
	}
	if !strings.Contains(got, `"cached_tokens":80`) {
		t.Fatalf("missing input_tokens_details.cached_tokens; events:\n%s", got)
	}
}
