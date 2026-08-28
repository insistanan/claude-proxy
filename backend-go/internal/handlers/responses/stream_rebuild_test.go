package responses

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
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

// TestStripAccumulatedCache 锁定透传分支累积式缓存统计剥离契约：
// grok-4.6 等 OpenAI 兼容上游的 input_tokens_details.cached_tokens 是跨请求
// 单调递增的累积命中量，原样下发会让 Cursor 误判上下文一直满、反复触发压缩。
func TestStripAccumulatedCache(t *testing.T) {
	t.Run("流式: 剥离 OpenAI 累积 cached_tokens 与派生 cache_read", func(t *testing.T) {
		// 上游返回 input_tokens=205071(含缓存), cached_tokens=204800 → 代理已减为 271
		event := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":271,\"output_tokens\":188,\"total_tokens\":459,\"input_tokens_details\":{\"cached_tokens\":204800}}}}\n\n"
		out := stripAccumulatedCacheFromCompletedEvent(event)

		data := extractCompletedPayload(t, out)
		usage := data["response"].(map[string]interface{})["usage"].(map[string]interface{})
		if _, exists := usage["input_tokens_details"]; exists {
			t.Errorf("input_tokens_details 应被剥离")
		}
		if _, exists := usage["cache_read_input_tokens"]; exists {
			t.Errorf("派生的 cache_read_input_tokens 应被剥离")
		}
		if got, _ := usage["input_tokens"].(float64); int(got) != 271 {
			t.Errorf("input_tokens 被改动: %v, want 271", usage["input_tokens"])
		}
		if got, _ := usage["output_tokens"].(float64); int(got) != 188 {
			t.Errorf("output_tokens 被改动: %v, want 188", usage["output_tokens"])
		}
		// 行结构保持（换行数量不变，不产生多余换行）
		assertSameLineStructure(t, event, out)
	})

	t.Run("流式: Claude 原生缓存保留 cache_read", func(t *testing.T) {
		event := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":100,\"output_tokens\":50,\"cache_creation_input_tokens\":200,\"cache_read_input_tokens\":150}}}\n\n"
		out := stripAccumulatedCacheFromCompletedEvent(event)

		usage := extractCompletedPayload(t, out)["response"].(map[string]interface{})["usage"].(map[string]interface{})
		if got, _ := usage["cache_read_input_tokens"].(float64); int(got) != 150 {
			t.Errorf("Claude cache_read 应保留: %v", usage["cache_read_input_tokens"])
		}
		if got, _ := usage["cache_creation_input_tokens"].(float64); int(got) != 200 {
			t.Errorf("Claude cache_creation 应保留: %v", usage["cache_creation_input_tokens"])
		}
	})

	t.Run("流式: 无缓存字段原样返回", func(t *testing.T) {
		event := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n"
		out := stripAccumulatedCacheFromCompletedEvent(event)
		if out != event {
			t.Fatalf("无缓存字段应原样返回:\n got  %q\n want %q", out, event)
		}
	})

	t.Run("流式: 非 completed 事件原样返回", func(t *testing.T) {
		event := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"
		out := stripAccumulatedCacheFromCompletedEvent(event)
		if out != event {
			t.Fatalf("非 completed 事件应原样返回")
		}
	})

	t.Run("非流式: 剥离 OpenAI 累积缓存", func(t *testing.T) {
		resp := &types.ResponsesResponse{
			Usage: types.ResponsesUsage{
				InputTokens:          271,
				OutputTokens:         188,
				CacheReadInputTokens: 204800, // 从 cached_tokens 派生
				InputTokensDetails:   &types.InputTokensDetails{CachedTokens: 204800},
			},
		}
		stripAccumulatedCacheFromResponse(resp)
		if resp.Usage.InputTokensDetails != nil {
			t.Errorf("InputTokensDetails 应被剥离")
		}
		if resp.Usage.CacheReadInputTokens != 0 {
			t.Errorf("派生的 CacheReadInputTokens 应被剥离: %d", resp.Usage.CacheReadInputTokens)
		}
		if resp.Usage.InputTokens != 271 {
			t.Errorf("InputTokens 被改动: %d", resp.Usage.InputTokens)
		}
	})

	t.Run("非流式: Claude 原生缓存保留", func(t *testing.T) {
		resp := &types.ResponsesResponse{
			Usage: types.ResponsesUsage{
				InputTokens:              100,
				CacheCreationInputTokens: 200,
				CacheReadInputTokens:     150,
			},
		}
		stripAccumulatedCacheFromResponse(resp)
		if resp.Usage.CacheReadInputTokens != 150 {
			t.Errorf("Claude cache_read 应保留: %d", resp.Usage.CacheReadInputTokens)
		}
		if resp.Usage.CacheCreationInputTokens != 200 {
			t.Errorf("Claude cache_creation 应保留: %d", resp.Usage.CacheCreationInputTokens)
		}
	})
}

func extractCompletedPayload(t *testing.T, event string) map[string]interface{} {
	t.Helper()
	for _, line := range strings.Split(event, "\n") {
		payload, isData := utilsParseSSEData(line)
		if !isData {
			continue
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			t.Fatalf("data 载荷解析失败: %v", err)
		}
		return data
	}
	t.Fatalf("事件无 data 行: %q", event)
	return nil
}

func utilsParseSSEData(line string) (string, bool) {
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	payload := strings.TrimPrefix(line, "data:")
	payload = strings.TrimPrefix(payload, " ")
	return payload, true
}

// makeLargeResponsesRequestBody 造一个长上下文请求体，保证本地估算超过校正下限。
// 用全英文填充（估算按 3.5 字符/token）：140000 字符约 40k tokens，稳超 20000 下限。
func makeLargeResponsesRequestBody(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"model": "grok-4.6",
		"input": []interface{}{
			map[string]interface{}{"role": "user", "content": strings.Repeat("abcdefgh", 17500)},
		},
	})
	if err != nil {
		t.Fatalf("构造请求体失败: %v", err)
	}
	if estimated := utils.EstimateResponsesRequestTokens(body); estimated < minInputTokensForCorrection {
		t.Fatalf("测试请求体估算 %d 未达校正下限 %d，需调大填充", estimated, minInputTokensForCorrection)
	}
	return body
}

// TestCorrectUnderreportedInputTokens 锁定"上游少报 input_tokens"的校正契约。
//
// 背景：grok-4.6 对长上下文只回报未命中缓存的增量（实测 549KB 请求体只报 7723）。
// 剥离累积式缓存字段后 input_tokens 是客户端判断上下文占用的唯一依据，这个假小值会让
// Cursor 认为上下文为空、永不触发压缩，一路堆到撞上游请求体大小限制返回 413。
//
// 校正必须严格：只在证据确凿（估算值达下限且超上游报数 2 倍）时改，其余一律不动。
func TestCorrectUnderreportedInputTokens(t *testing.T) {
	envCfg := &config.EnvConfig{CorrectResponsesInputTokens: true}
	largeBody := makeLargeResponsesRequestBody(t)
	estimated := utils.EstimateResponsesRequestTokens(largeBody)

	t.Run("流式: 上游严重少报则校正并同步 total", func(t *testing.T) {
		event := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":7723,\"output_tokens\":188,\"total_tokens\":7911}}}\n\n"
		out := correctUnderreportedInputTokensInCompletedEvent(event, largeBody, envCfg)

		usage := extractCompletedPayload(t, out)["response"].(map[string]interface{})["usage"].(map[string]interface{})
		if got, _ := usage["input_tokens"].(float64); int(got) != estimated {
			t.Errorf("input_tokens 应校正为 %d, got %v", estimated, usage["input_tokens"])
		}
		if got, _ := usage["total_tokens"].(float64); int(got) != estimated+188 {
			t.Errorf("total_tokens 应同步为 %d, got %v", estimated+188, usage["total_tokens"])
		}
		if got, _ := usage["output_tokens"].(float64); int(got) != 188 {
			t.Errorf("output_tokens 不应被改动: %v", usage["output_tokens"])
		}
		assertSameLineStructure(t, event, out)
	})

	t.Run("流式: 上游回报可信则原样返回", func(t *testing.T) {
		// 上游报数与本地估算同量级（未达 2 倍差），不得改动
		event := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":" +
			strconv.Itoa(estimated) + ",\"output_tokens\":188}}}\n\n"
		out := correctUnderreportedInputTokensInCompletedEvent(event, largeBody, envCfg)
		if out != event {
			t.Fatalf("可信回报应原样返回:\n got  %q\n want %q", out, event)
		}
	})

	t.Run("流式: 小请求体不进入校正路径", func(t *testing.T) {
		smallBody := []byte(`{"model":"grok-4.6","input":"hello"}`)
		event := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":1,\"output_tokens\":5}}}\n\n"
		out := correctUnderreportedInputTokensInCompletedEvent(event, smallBody, envCfg)
		if out != event {
			t.Fatalf("小请求体不应触发校正:\n got  %q\n want %q", out, event)
		}
	})

	t.Run("流式: 开关关闭则原样返回", func(t *testing.T) {
		off := &config.EnvConfig{CorrectResponsesInputTokens: false}
		event := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":7723,\"output_tokens\":188}}}\n\n"
		out := correctUnderreportedInputTokensInCompletedEvent(event, largeBody, off)
		if out != event {
			t.Fatalf("开关关闭应原样返回:\n got  %q\n want %q", out, event)
		}
	})

	t.Run("非流式: 上游严重少报则校正并同步 total", func(t *testing.T) {
		resp := &types.ResponsesResponse{
			Usage: types.ResponsesUsage{InputTokens: 7723, OutputTokens: 188, TotalTokens: 7911},
		}
		correctUnderreportedInputTokensInResponse(resp, largeBody, envCfg)
		if resp.Usage.InputTokens != estimated {
			t.Errorf("InputTokens 应校正为 %d, got %d", estimated, resp.Usage.InputTokens)
		}
		if resp.Usage.TotalTokens != estimated+188 {
			t.Errorf("TotalTokens 应同步为 %d, got %d", estimated+188, resp.Usage.TotalTokens)
		}
		if resp.Usage.OutputTokens != 188 {
			t.Errorf("OutputTokens 不应被改动: %d", resp.Usage.OutputTokens)
		}
	})

	t.Run("非流式: 上游回报可信则不动", func(t *testing.T) {
		resp := &types.ResponsesResponse{
			Usage: types.ResponsesUsage{InputTokens: estimated, OutputTokens: 188, TotalTokens: estimated + 188},
		}
		correctUnderreportedInputTokensInResponse(resp, largeBody, envCfg)
		if resp.Usage.InputTokens != estimated {
			t.Errorf("可信回报不应被改动: got %d, want %d", resp.Usage.InputTokens, estimated)
		}
		if resp.Usage.TotalTokens != estimated+188 {
			t.Errorf("TotalTokens 不应被改动: got %d", resp.Usage.TotalTokens)
		}
	})

	// 以下两例锁定「缓存必须计入判定基数」：Claude 语义下 input_tokens 不含缓存部分，
	// 若只拿 input_tokens 比对估算值，会把正确分开报数的上游误判为少报（271 vs 205071），
	// 校正后 input 与保留下来的 cache_read 相加就成了双倍上下文。
	t.Run("流式: Claude 语义分开报缓存不得误判为少报", func(t *testing.T) {
		// input 很小但 cache 很大，两者之和与估算同量级 → 上游其实报得准，必须原样返回
		cacheRead := estimated - 1000
		event := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":271,\"output_tokens\":188," +
			"\"cache_read_input_tokens\":" + strconv.Itoa(cacheRead) + ",\"cache_creation_input_tokens\":500}}}\n\n"
		out := correctUnderreportedInputTokensInCompletedEvent(event, largeBody, envCfg)
		if out != event {
			t.Fatalf("Claude 语义分开报缓存应原样返回:\n got  %q\n want %q", out, event)
		}
	})

	t.Run("非流式: 校正值扣掉上游已单独回报的缓存量", func(t *testing.T) {
		// 缓存很小、确实少报 → 校正，但校正值必须扣掉缓存，避免与 cache 字段重叠
		resp := &types.ResponsesResponse{
			Usage: types.ResponsesUsage{
				InputTokens:          100,
				OutputTokens:         188,
				CacheReadInputTokens: 1000,
			},
		}
		correctUnderreportedInputTokensInResponse(resp, largeBody, envCfg)
		want := estimated - 1000
		if resp.Usage.InputTokens != want {
			t.Errorf("校正值应扣掉缓存量: got %d, want %d", resp.Usage.InputTokens, want)
		}
		if resp.Usage.CacheReadInputTokens != 1000 {
			t.Errorf("缓存字段不应被改动: %d", resp.Usage.CacheReadInputTokens)
		}
		// input + cache 之和回到真实上下文规模，不双倍
		if resp.Usage.InputTokens+resp.Usage.CacheReadInputTokens != estimated {
			t.Errorf("input+cache 应等于估算总量 %d, got %d",
				estimated, resp.Usage.InputTokens+resp.Usage.CacheReadInputTokens)
		}
	})
}
