package providers

import (
	"testing"

	"github.com/BenedictKing/claude-proxy/internal/types"
)

func TestOpenAIProviderConvertToClaudeResponse_PreservesReasoningContent(t *testing.T) {
	providerResp := &types.ProviderResponse{
		Body: []byte(`{
			"id":"chatcmpl_test",
			"choices":[{
				"finish_reason":"stop",
				"message":{
					"role":"assistant",
					"reasoning_content":"need inspect files",
					"content":"I will inspect the files."
				}
			}]
		}`),
	}

	got, err := (&OpenAIProvider{}).ConvertToClaudeResponse(providerResp)
	if err != nil {
		t.Fatalf("ConvertToClaudeResponse() err = %v", err)
	}
	if len(got.Content) != 2 {
		t.Fatalf("content = %#v", got.Content)
	}
	if got.Content[0].Type != "thinking" || got.Content[0].Thinking != "need inspect files" {
		t.Fatalf("thinking content = %#v", got.Content[0])
	}
	if got.Content[1].Type != "text" || got.Content[1].Text != "I will inspect the files." {
		t.Fatalf("text content = %#v", got.Content[1])
	}
}
