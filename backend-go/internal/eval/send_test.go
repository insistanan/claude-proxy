package eval

import (
	"testing"

	"github.com/BenedictKing/api-proxy/internal/config"
)

func TestResolveEvalModelOverrideDoesNotUseDefault(t *testing.T) {
	upstream := config.UpstreamConfig{
		Name:         "mapped",
		DefaultModel: "claude-sonnet-4",
		ModelMapping: map[string][]string{
			"gpt-4o": {"openai/gpt-4o"},
		},
	}
	got, err := resolveEvalModel(upstream, "gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	if got != "openai/gpt-4o" {
		t.Fatalf("覆盖值应走映射且不被 defaultModel 吞掉，得到 %s", got)
	}

	fallback, err := resolveEvalModel(upstream, "")
	if err != nil {
		t.Fatal(err)
	}
	if fallback != "claude-sonnet-4" {
		t.Fatalf("空覆盖应使用 defaultModel，得到 %s", fallback)
	}
}

func TestResolveEvalModelRequiresDefault(t *testing.T) {
	_, err := resolveEvalModel(config.UpstreamConfig{Name: "empty"}, "")
	if err == nil {
		t.Fatal("没有 defaultModel 且没有覆盖时应报错")
	}
}
