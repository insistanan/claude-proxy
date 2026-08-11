package modelaudit

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
)

func validSpec(protocol Protocol, channelID string) ExecutionSpec {
	profile := map[Protocol]string{
		ProtocolMessages:  "messages.standard.v1",
		ProtocolResponses: "responses.standard.v1",
		ProtocolGemini:    "gemini.standard.v1",
		ProtocolChat:      "chat.standard.v1",
		ProtocolImages:    "images.generation.v1",
	}[protocol]
	return ExecutionSpec{
		Purpose:         PurposeQuickTest,
		Target:          ChannelTarget{ChannelID: channelID, ChannelKind: protocol.ChannelKind()},
		Protocol:        protocol,
		RequestProfile:  profile,
		TimeoutMillis:   30_000,
		MaxOutputTokens: 256,
		Redaction:       RedactionDigest,
		Input:           ExecutionInput{Prompt: "请用一句完整的话描述今天的天气。"},
	}
}

func TestProtocolDescriptorsDeclareFiveProtocols(t *testing.T) {
	descriptors := ProtocolDescriptors()
	if len(descriptors) != 5 {
		t.Fatalf("协议数量 = %d，期望 5", len(descriptors))
	}

	seen := make(map[Protocol]bool, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor.Protocol.ChannelKind() != descriptor.ChannelKind {
			t.Fatalf("协议 %q 的渠道类型不一致", descriptor.Protocol)
		}
		if len(descriptor.Profiles) == 0 || len(descriptor.CompletionSignals) == 0 {
			t.Fatalf("协议 %q 缺少轮廓或终态声明", descriptor.Protocol)
		}
		seen[descriptor.Protocol] = true
	}
	for _, protocol := range []Protocol{ProtocolMessages, ProtocolResponses, ProtocolGemini, ProtocolChat, ProtocolImages} {
		if !seen[protocol] {
			t.Fatalf("缺少协议 %q", protocol)
		}
	}
}

func TestThinkingMappingRejectsUnsupportedAndPreservesZeroBudget(t *testing.T) {
	if _, err := ResolveThinking(ProtocolImages, ThinkingLow); ErrorCodeOf(err) != ErrorCodeUnsupported {
		t.Fatalf("Images 思考档位错误 = %v，期望 unsupported", err)
	}

	mapping, err := ResolveThinking(ProtocolGemini, ThinkingOff)
	if err != nil {
		t.Fatal(err)
	}
	if mapping.BudgetTokens == nil || *mapping.BudgetTokens != 0 {
		t.Fatalf("Gemini off 映射 = %#v，期望显式 budget 0", mapping)
	}
	data, err := json.Marshal(mapping)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"budgetTokens":0`) {
		t.Fatalf("Gemini off JSON 丢失 0 预算: %s", data)
	}
}

func TestResolveTargetUsesStableIDAndFreezesDefaultModel(t *testing.T) {
	capturedAt := time.Date(2026, 8, 11, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	cfg := config.Config{Upstream: []config.UpstreamConfig{
		{ID: "channel-a", Name: "A", DefaultModel: "model-a"},
		{
			ID: "channel-b", Name: "B", ServiceType: "responses", DefaultModel: "model-b",
			BaseURL: "https://user:password@example.invalid", APIKeys: []string{"secret-key"},
		},
	}}
	spec := validSpec(ProtocolMessages, "channel-b")

	resolved, err := ResolveTarget(cfg, spec, capturedAt)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ChannelIndex != 1 {
		t.Fatalf("渠道下标 = %d，期望 1", resolved.ChannelIndex)
	}
	if resolved.Snapshot.ResolvedModel != "model-b" || resolved.Snapshot.ChannelID != "channel-b" {
		t.Fatalf("目标快照错误: %#v", resolved.Snapshot)
	}
	if resolved.Snapshot.WireProtocol != ProtocolResponses {
		t.Fatalf("实际线协议 = %q，期望 responses", resolved.Snapshot.WireProtocol)
	}
	if !resolved.Snapshot.CapturedAt.Equal(capturedAt.UTC()) {
		t.Fatalf("快照时间 = %s，期望 %s", resolved.Snapshot.CapturedAt, capturedAt.UTC())
	}

	serialized, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-key", "password", "example.invalid"} {
		if strings.Contains(string(serialized), secret) {
			t.Fatalf("序列化目标泄漏敏感上游数据 %q: %s", secret, serialized)
		}
	}
}

func TestResolveTargetFailsWhenDefaultModelMissing(t *testing.T) {
	cfg := config.Config{ResponsesUpstream: []config.UpstreamConfig{{ID: "responses-1", Name: "R"}}}
	spec := validSpec(ProtocolResponses, "responses-1")

	_, err := ResolveTarget(cfg, spec, time.Now())
	if ErrorCodeOf(err) != ErrorCodeModelRequired {
		t.Fatalf("错误 = %v，期望 model_required", err)
	}
}

func TestResolveTargetRejectsDuplicateStableID(t *testing.T) {
	cfg := config.Config{ChatUpstream: []config.UpstreamConfig{
		{ID: "duplicate", DefaultModel: "model-a"},
		{ID: "duplicate", DefaultModel: "model-b"},
	}}
	spec := validSpec(ProtocolChat, "duplicate")

	_, err := ResolveTarget(cfg, spec, time.Now())
	if ErrorCodeOf(err) != ErrorCodeChannelIDAmbiguous {
		t.Fatalf("错误 = %v，期望 channel_id_ambiguous", err)
	}
}

func TestExecutionSpecRejectsSilentFeatureDrop(t *testing.T) {
	spec := validSpec(ProtocolMessages, "messages-1")
	spec.Input.Tools = []ToolDefinition{{Name: "lookup"}}

	if err := spec.Validate(); ErrorCodeOf(err) != ErrorCodeInvalidRequest {
		t.Fatalf("错误 = %v，期望 invalid_request", err)
	}
}

type countingAuditSink struct {
	calls int
}

func (s *countingAuditSink) SaveAuditResult(context.Context, ExecutionResult) error {
	s.calls++
	return nil
}

func TestEffectPoliciesNeverAllowProductionMutations(t *testing.T) {
	for _, purpose := range []ExecutionPurpose{
		PurposeQuickTest, PurposePlayground, PurposeIdentityProbe, PurposeCapabilityEval,
	} {
		policy, err := PolicyForPurpose(purpose)
		if err != nil {
			t.Fatal(err)
		}
		if err := policy.ValidateIsolation(); err != nil {
			t.Fatalf("目的 %q 的隔离策略无效: %v", purpose, err)
		}
		for mutation, allowed := range policy.Production {
			if allowed {
				t.Fatalf("目的 %q 意外允许生产副作用 %q", purpose, mutation)
			}
		}
	}
}

func TestPersistResultOnlyForAuditPurposes(t *testing.T) {
	sink := &countingAuditSink{}
	quickPolicy, _ := PolicyForPurpose(PurposeQuickTest)
	if err := PersistResultIfRequired(context.Background(), quickPolicy, sink, ExecutionResult{}); err != nil {
		t.Fatal(err)
	}
	if sink.calls != 0 {
		t.Fatalf("快速测试不应持久化审计结果，调用次数 = %d", sink.calls)
	}

	auditPolicy, _ := PolicyForPurpose(PurposeIdentityProbe)
	if err := PersistResultIfRequired(context.Background(), auditPolicy, sink, ExecutionResult{}); err != nil {
		t.Fatal(err)
	}
	if sink.calls != 1 {
		t.Fatalf("身份探针应持久化一次，调用次数 = %d", sink.calls)
	}
}

func TestRedactedRequestSummaryContainsNoValuesOrPrompt(t *testing.T) {
	prompt := []byte("sensitive prompt")
	headers := http.Header{
		"Authorization": []string{"Bearer secret-token"},
		"X-Api-Key":     []string{"secret-key"},
	}
	summary, err := NewRedactedRequestSummary(
		RedactionDigest,
		"responses.standard.v1",
		prompt,
		headers,
		0,
		0,
		[]string{"model", "input"},
	)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(data)
	for _, forbidden := range []string{"sensitive prompt", "secret-token", "secret-key"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("脱敏摘要泄漏 %q: %s", forbidden, serialized)
		}
	}
	if summary.PromptSHA256 == "" || len(summary.HeaderNames) != 2 {
		t.Fatalf("脱敏摘要缺少摘要证据: %#v", summary)
	}
}

func TestCompletedResultRequiresProtocolTerminal(t *testing.T) {
	finishedAt := time.Now()
	result := ExecutionResult{
		ExecutionID: "run-1",
		Purpose:     PurposeQuickTest,
		Status:      StatusCompleted,
		FinishedAt:  &finishedAt,
	}
	if err := result.Validate(); ErrorCodeOf(err) != ErrorCodeProtocol {
		t.Fatalf("错误 = %v，期望 protocol_error", err)
	}
	result.ProtocolTerminal = ProtocolTerminalCompleted
	if err := result.Validate(); err != nil {
		t.Fatalf("完整 completed 结果不应失败: %v", err)
	}
}
