package eval

import "testing"

func TestExtractClaudeTextSkipsThinking(t *testing.T) {
	raw := &RawResponse{JSON: map[string]interface{}{
		"model": "claude-sonnet-4-5",
		"content": []interface{}{
			map[string]interface{}{"type": "thinking", "thinking": "secret", "signature": "sig"},
			map[string]interface{}{"type": "text", "text": "pong"},
		},
		"usage": map[string]interface{}{"output_tokens": float64(3)},
	}}
	extracted, err := ExtractFromResponse(ExtractProtocol, raw)
	if err != nil {
		t.Fatal(err)
	}
	if extracted.Text != "pong" {
		t.Fatalf("应抽出 pong，得到 %q", extracted.Text)
	}
	if !extracted.HasSignature || !extracted.HasThinking {
		t.Fatalf("应观察到 thinking/signature: %+v", extracted)
	}
}

func TestExtractSVG(t *testing.T) {
	raw := &RawResponse{JSON: map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"message": map[string]interface{}{
					"content": "here\n<svg viewBox=\"0 0 100 100\"><circle r=\"40\"/></svg>\n",
				},
			},
		},
	}}
	extracted, err := ExtractFromResponse(ExtractSVG, raw)
	if err != nil {
		t.Fatal(err)
	}
	if extracted.SVG == "" {
		t.Fatal("应抽出 SVG")
	}
}
