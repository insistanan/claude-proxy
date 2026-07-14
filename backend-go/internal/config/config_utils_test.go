package config

import (
	"encoding/json"
	"testing"
)

func TestStripContextSuffix(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantModel     string
		wantHasSuffix bool
	}{
		{
			name:          "opus with [1m] suffix",
			input:         "opus[1m]",
			wantModel:     "opus",
			wantHasSuffix: true,
		},
		{
			name:          "sonnet with [1m] suffix",
			input:         "sonnet[1m]",
			wantModel:     "sonnet",
			wantHasSuffix: true,
		},
		{
			name:          "full model name with [1m] suffix",
			input:         "claude-opus-4-8[1m]",
			wantModel:     "claude-opus-4-8",
			wantHasSuffix: true,
		},
		{
			name:          "model without suffix",
			input:         "opus",
			wantModel:     "opus",
			wantHasSuffix: false,
		},
		{
			name:          "model with whitespace and suffix",
			input:         "  opus[1m]  ",
			wantModel:     "opus",
			wantHasSuffix: true,
		},
		{
			name:          "fable model",
			input:         "fable",
			wantModel:     "fable",
			wantHasSuffix: false,
		},
		{
			name:          "deepseek model",
			input:         "deepseek-v4-pro",
			wantModel:     "deepseek-v4-pro",
			wantHasSuffix: false,
		},
		{
			name:          "empty string",
			input:         "",
			wantModel:     "",
			wantHasSuffix: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModel, gotHasSuffix := StripContextSuffix(tt.input)
			if gotModel != tt.wantModel {
				t.Errorf("StripContextSuffix() gotModel = %v, want %v", gotModel, tt.wantModel)
			}
			if gotHasSuffix != tt.wantHasSuffix {
				t.Errorf("StripContextSuffix() gotHasSuffix = %v, want %v", gotHasSuffix, tt.wantHasSuffix)
			}
		})
	}
}

func TestResolveUpstreamModelWithSuffix(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		upstream *UpstreamConfig
		want     string
	}{
		{
			name:  "opus[1m] with exact mapping preserves suffix",
			model: "opus[1m]",
			upstream: &UpstreamConfig{
				ModelMapping: map[string][]string{
					"opus[1m]": {"deepseek-v4-pro"},
				},
			},
			want: "deepseek-v4-pro",
		},
		{
			name:  "opus[1m] fallback to opus mapping",
			model: "opus[1m]",
			upstream: &UpstreamConfig{
				ModelMapping: map[string][]string{
					"opus": {"deepseek-v4-pro"},
				},
			},
			want: "deepseek-v4-pro",
		},
		{
			name:  "claude-opus-4-8[1m] without mapping strips suffix",
			model: "claude-opus-4-8[1m]",
			upstream: &UpstreamConfig{
				ModelMapping: map[string][]string{},
			},
			want: "claude-opus-4-8[1m]", // 保留原样，没有映射就不处理
		},
		{
			name:  "sonnet[1m] with default model",
			model: "sonnet[1m]",
			upstream: &UpstreamConfig{
				DefaultModel: "gpt-5.4",
			},
			want: "gpt-5.4",
		},
		{
			name:     "fable without suffix",
			model:    "fable",
			upstream: nil,
			want:     "fable",
		},
		{
			name:  "opus[1m] fuzzy match after strip",
			model: "opus[1m]",
			upstream: &UpstreamConfig{
				ModelMapping: map[string][]string{
					"claude-opus": {"deepseek-v4-pro"},
				},
			},
			want: "deepseek-v4-pro",
		},
		{
			name:  "wildcard mapping handles every model",
			model: "custom-model-2026",
			upstream: &UpstreamConfig{
				ModelMapping: map[string][]string{
					"*": {"gpt-5.4"},
				},
			},
			want: "gpt-5.4",
		},
		{
			name:  "wildcard mapping takes precedence over specific mapping",
			model: "gpt-5.4-codex",
			upstream: &UpstreamConfig{
				ModelMapping: map[string][]string{
					"*":     {"gpt-5.4"},
					"codex": {"gpt-5.4-codex"},
				},
			},
			want: "gpt-5.4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveUpstreamModel(tt.model, tt.upstream)
			if got != tt.want {
				t.Errorf("ResolveUpstreamModel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUpstreamConfigUnmarshalLegacyModelMapping(t *testing.T) {
	input := []byte(`{
		"baseUrl": "https://api.example.com",
		"apiKeys": ["sk-test"],
		"serviceType": "openai",
		"modelMapping": {
			"opus": "kimi-2.6",
			"sonnet": ["gpt-4o", "gpt-4o-mini"]
		}
	}`)

	var upstream UpstreamConfig
	if err := json.Unmarshal(input, &upstream); err != nil {
		t.Fatalf("json.Unmarshal() err = %v", err)
	}

	assertStringSlice(t, upstream.ModelMapping["opus"], []string{"kimi-2.6"})
	assertStringSlice(t, upstream.ModelMapping["sonnet"], []string{"gpt-4o", "gpt-4o-mini"})
}

func TestUpstreamUpdateUnmarshalLegacyModelMapping(t *testing.T) {
	input := []byte(`{
		"modelMapping": {
			"opus": "kimi-2.6",
			"sonnet": ["gpt-4o"]
		}
	}`)

	var update UpstreamUpdate
	if err := json.Unmarshal(input, &update); err != nil {
		t.Fatalf("json.Unmarshal() err = %v", err)
	}

	assertStringSlice(t, update.ModelMapping["opus"], []string{"kimi-2.6"})
	assertStringSlice(t, update.ModelMapping["sonnet"], []string{"gpt-4o"})
}

func assertStringSlice(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d; got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q; got=%v", i, got[i], want[i], got)
		}
	}
}
