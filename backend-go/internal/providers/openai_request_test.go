package providers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/types"
)

func TestOpenAIProviderConvertToProviderRequest_UsesSingleTokenField(t *testing.T) {
	t.Run("generic openai keeps max_tokens only", func(t *testing.T) {
		c := newGinContext(http.MethodPost, "/v1/messages", []byte(`{"model":"gpt-4o","max_tokens":123,"messages":[]}`), nil)
		upstream := &config.UpstreamConfig{
			BaseURL:     "https://api.example.com",
			ServiceType: "openai",
		}

		p := &OpenAIProvider{}
		req, _, err := p.ConvertToProviderRequest(c, upstream, "sk-test")
		if err != nil {
			t.Fatalf("ConvertToProviderRequest() err = %v", err)
		}

		var got types.OpenAIRequest
		if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		if got.MaxTokens != 123 {
			t.Fatalf("MaxTokens = %d, want 123", got.MaxTokens)
		}
		if got.MaxCompletionTokens != 0 {
			t.Fatalf("MaxCompletionTokens = %d, want 0", got.MaxCompletionTokens)
		}
	})

	t.Run("kimi target prefers max_completion_tokens", func(t *testing.T) {
		c := newGinContext(http.MethodPost, "/v1/messages", []byte(`{"model":"claude-sonnet-4-5-20250929","max_tokens":64000,"messages":[]}`), nil)
		upstream := &config.UpstreamConfig{
			BaseURL:      "https://api.example.com",
			ServiceType:  "openai",
			ModelMapping: map[string]string{"claude-sonnet-4-5-20250929": "kimi-2.6"},
		}

		p := &OpenAIProvider{}
		req, _, err := p.ConvertToProviderRequest(c, upstream, "sk-test")
		if err != nil {
			t.Fatalf("ConvertToProviderRequest() err = %v", err)
		}

		var got types.OpenAIRequest
		if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		if got.Model != "kimi-2.6" {
			t.Fatalf("Model = %q, want kimi-2.6", got.Model)
		}
		if got.MaxCompletionTokens != 64000 {
			t.Fatalf("MaxCompletionTokens = %d, want 64000", got.MaxCompletionTokens)
		}
		if got.MaxTokens != 0 {
			t.Fatalf("MaxTokens = %d, want 0", got.MaxTokens)
		}
	})

	t.Run("stream requests include usage chunk", func(t *testing.T) {
		c := newGinContext(http.MethodPost, "/v1/messages", []byte(`{"model":"gpt-4o","stream":true,"messages":[]}`), nil)
		upstream := &config.UpstreamConfig{
			BaseURL:     "https://api.example.com",
			ServiceType: "openai",
		}

		p := &OpenAIProvider{}
		req, _, err := p.ConvertToProviderRequest(c, upstream, "sk-test")
		if err != nil {
			t.Fatalf("ConvertToProviderRequest() err = %v", err)
		}

		var got map[string]interface{}
		if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		streamOptions, ok := got["stream_options"].(map[string]interface{})
		if !ok {
			t.Fatalf("stream_options missing: %#v", got["stream_options"])
		}
		if includeUsage, ok := streamOptions["include_usage"].(bool); !ok || !includeUsage {
			t.Fatalf("stream_options.include_usage = %#v, want true", streamOptions["include_usage"])
		}
	})
}
