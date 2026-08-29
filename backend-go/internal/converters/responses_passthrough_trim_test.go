package converters

import (
	"encoding/json"
	"testing"

	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/tidwall/gjson"
)

// TestResponsesRawItemsCarryOwnHistory 锁定“input 是否自带完整历史”的判据。
// 判据必须保守：漏判只是保留 previous_response_id（等于修复前的行为），
// 误判会删掉服务端链而 input 其实只是后缀，导致整段上下文丢失。
func TestResponsesRawItemsCarryOwnHistory(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "developer 会话根",
			input: `[{"type":"message","role":"developer","content":"sys"},{"type":"message","role":"user","content":"hi"}]`,
			want:  true,
		},
		{
			name:  "system 会话根",
			input: `[{"type":"message","role":"system","content":"sys"},{"type":"message","role":"user","content":"hi"}]`,
			want:  true,
		},
		{
			name:  "user 开头且带 assistant 产出",
			input: `[{"type":"message","role":"user","content":"hi"},{"type":"reasoning","summary":[]},{"type":"custom_tool_call","call_id":"c1","name":"exec","input":"ls"}]`,
			want:  true,
		},
		{
			name:  "只有工具结果后缀",
			input: `[{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}]`,
			want:  false,
		},
		{
			name:  "只有一条新 user 输入",
			input: `[{"type":"message","role":"user","content":"下一步"}]`,
			want:  false,
		},
		{
			name:  "reasoning 加工具结果但无会话根",
			input: `[{"type":"reasoning","summary":[]},{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}]`,
			want:  false,
		},
		{
			name:  "空数组",
			input: `[]`,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rawItems []json.RawMessage
			if err := json.Unmarshal([]byte(tt.input), &rawItems); err != nil {
				t.Fatalf("构造 input 失败: %v", err)
			}
			if got := responsesRawItemsCarryOwnHistory(rawItems); got != tt.want {
				t.Errorf("responsesRawItemsCarryOwnHistory() = %v, 期望 %v", got, tt.want)
			}
		})
	}
}

// TestTrimResponsesPassthroughInputWithoutLocalSession 覆盖本地 session 为空的场景：
// 进程重启、TTL 过期、LRU 驱逐或首次代理该会话都会走到这里。此前该分支无条件原样
// 转发，previous_response_id 与完整历史会同时到达上游，同一段历史被算两次。
func TestTrimResponsesPassthroughInputWithoutLocalSession(t *testing.T) {
	const fullHistory = `[{"type":"message","role":"developer","content":"sys"},{"type":"message","role":"user","content":"hi"},{"type":"message","role":"assistant","content":"yo"},{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}]`
	const toolSuffix = `[{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}]`

	tests := []struct {
		name         string
		body         string
		sess         *types.Session
		wantChain    string
		wantInputLen int
	}{
		{
			name:         "session 为 nil 且 input 自带完整历史时切断旧链",
			body:         `{"model":"gpt-5","previous_response_id":"resp_123","input":` + fullHistory + `}`,
			sess:         nil,
			wantChain:    "",
			wantInputLen: 4,
		},
		{
			name:         "session 存在但历史为空时同样切断旧链",
			body:         `{"model":"gpt-5","previous_response_id":"resp_123","input":` + fullHistory + `}`,
			sess:         &types.Session{ID: "sess_empty"},
			wantChain:    "",
			wantInputLen: 4,
		},
		{
			name:         "增量工具结果后缀保留旧链",
			body:         `{"model":"gpt-5","previous_response_id":"resp_123","input":` + toolSuffix + `}`,
			sess:         nil,
			wantChain:    "resp_123",
			wantInputLen: 1,
		},
		{
			name:         "无 previous_response_id 的完整历史不受影响",
			body:         `{"model":"gpt-5","input":` + fullHistory + `}`,
			sess:         nil,
			wantChain:    "",
			wantInputLen: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req types.ResponsesRequest
			if err := json.Unmarshal([]byte(tt.body), &req); err != nil {
				t.Fatalf("解析测试请求失败: %v", err)
			}
			got, err := convertResponsesPassthroughRequestWithSession("", []byte(tt.body), nil, tt.sess, &req)
			if err != nil {
				t.Fatalf("convertResponsesPassthroughRequestWithSession() 返回错误: %v", err)
			}
			if chain := gjson.GetBytes(got, "previous_response_id").String(); chain != tt.wantChain {
				t.Errorf("previous_response_id = %q, 期望 %q", chain, tt.wantChain)
			}
			// 切断旧链时只能删链，绝不能同时裁剪 input——否则上下文两头都丢。
			if inputLen := len(gjson.GetBytes(got, "input").Array()); inputLen != tt.wantInputLen {
				t.Errorf("input 长度 = %d, 期望 %d", inputLen, tt.wantInputLen)
			}
			if model := gjson.GetBytes(got, "model").String(); model != "gpt-5" {
				t.Errorf("model 被意外改写为 %q", model)
			}
		})
	}
}
