package responses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BenedictKing/claude-proxy/internal/config"
)

// TestResponsesEventRebuildPreservesLineStructure 锁定 Responses 侧两个事件改写函数的保真契约：
// 收敛前它们逐行补写 "\n"，会让以 "\n\n" 结尾的事件多出一个换行。
func TestResponsesEventRebuildPreservesLineStructure(t *testing.T) {
	envCfg := &config.EnvConfig{}
	requestBody := []byte(`{"model":"m","input":"hello"}`)

	t.Run("injectResponsesUsageToCompletedEvent", func(t *testing.T) {
		event := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n"
		out, in, outTok := injectResponsesUsageToCompletedEvent(event, requestBody, "hi there", envCfg)

		assertSameLineStructure(t, event, out)
		assertResponsesUsage(t, out, in, outTok)
	})

	t.Run("patchResponsesCompletedEventUsage", func(t *testing.T) {
		event := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"
		collected := &responsesStreamUsage{}
		out := patchResponsesCompletedEventUsage(event, requestBody, "hi there", collected, envCfg)

		assertSameLineStructure(t, event, out)
		if collected.InputTokens <= 0 || collected.OutputTokens <= 0 {
			t.Fatalf("collected usage 未被修补: %+v", collected)
		}
		assertResponsesUsage(t, out, collected.InputTokens, collected.OutputTokens)
	})

	t.Run("非 completed 事件原样返回", func(t *testing.T) {
		event := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"
		out, _, _ := injectResponsesUsageToCompletedEvent(event, requestBody, "hi", envCfg)
		if out != event {
			t.Fatalf("事件被改动:\n got  %q\n want %q", out, event)
		}
	})
}

// TestInjectUsageIntoMultiLineDataEvent 覆盖 JSON 被拆到多条连续 data 行的兜底路径。
// 收敛前该分支的 jsonEnd 默认为 0：当 data 行区间后没有空行时，
// 重建会从第 0 行起把整个事件再追加一遍，客户端会收到一条被替换过的
// data 行 + 一份原始（usage 未注入）的重复事件。
func TestInjectUsageIntoMultiLineDataEvent(t *testing.T) {
	t.Run("data 行后有空行", func(t *testing.T) {
		event := "event: response.completed\ndata: {\"type\":\"response.completed\",\ndata: \"response\":{\"id\":\"resp_1\"}}\n\n"
		out, ok := injectUsageIntoMultiLineDataEvent(event, 11, 7, 18)
		if !ok {
			t.Fatal("ok = false, want true")
		}

		want := "event: response.completed\ndata: {\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":11,\"output_tokens\":7,\"total_tokens\":18}},\"type\":\"response.completed\"}\n\n"
		if out != want {
			t.Fatalf("got  %q\nwant %q", out, want)
		}
	})

	t.Run("data 行后无空行时不重复整段", func(t *testing.T) {
		event := "event: response.completed\ndata: {\"type\":\"response.completed\",\ndata: \"response\":{\"id\":\"resp_1\"}}"
		out, ok := injectUsageIntoMultiLineDataEvent(event, 11, 7, 18)
		if !ok {
			t.Fatal("ok = false, want true")
		}

		if n := strings.Count(out, "data:"); n != 1 {
			t.Fatalf("data 行数量 = %d, want 1（多行载荷应合并为一行，且不重复原始事件）\n out %q", n, out)
		}
		assertResponsesUsage(t, out, 11, 7)
	})

	t.Run("无 data 行返回 false", func(t *testing.T) {
		if _, ok := injectUsageIntoMultiLineDataEvent("event: ping\n\n", 1, 2, 3); ok {
			t.Fatal("ok = true, want false")
		}
	})

	t.Run("拼不出合法 JSON 返回 false", func(t *testing.T) {
		if _, ok := injectUsageIntoMultiLineDataEvent("data: {broken\n\n", 1, 2, 3); ok {
			t.Fatal("ok = true, want false")
		}
	})
}

func assertSameLineStructure(t *testing.T, in, out string) {
	t.Helper()
	if gotN, wantN := strings.Count(out, "\n"), strings.Count(in, "\n"); gotN != wantN {
		t.Fatalf("换行数量 = %d, want %d\n in  %q\n out %q", gotN, wantN, in, out)
	}
	if strings.HasSuffix(out, "\n\n\n") {
		t.Fatalf("结果多出一个换行: %q", out)
	}
}

func assertResponsesUsage(t *testing.T, event string, wantInput, wantOutput int) {
	t.Helper()

	var payload string
	for _, line := range strings.Split(event, "\n") {
		if strings.HasPrefix(line, "data:") {
			payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			break
		}
	}
	if payload == "" {
		t.Fatalf("事件中没有 data 行: %q", event)
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		t.Fatalf("data 载荷无法解析: %v (%q)", err, payload)
	}
	response, ok := data["response"].(map[string]interface{})
	if !ok {
		t.Fatalf("缺少 response: %#v", data)
	}
	usage, ok := response["usage"].(map[string]interface{})
	if !ok {
		t.Fatalf("缺少 response.usage: %#v", response)
	}
	if got, _ := usage["input_tokens"].(float64); int(got) != wantInput {
		t.Errorf("input_tokens = %v, want %d", usage["input_tokens"], wantInput)
	}
	if got, _ := usage["output_tokens"].(float64); int(got) != wantOutput {
		t.Errorf("output_tokens = %v, want %d", usage["output_tokens"], wantOutput)
	}
}
