package logger

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPrepareBodyReplacesAnthropicImage(t *testing.T) {
	payload := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "image",
						"source": map[string]any{
							"type":       "base64",
							"media_type": "image/png",
							"data":       "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+ip1s=",
						},
					},
				},
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	body, originalBytes, truncated := PrepareBody(raw)
	if originalBytes != len(raw) {
		t.Fatalf("originalBytes=%d, want %d", originalBytes, len(raw))
	}
	if truncated {
		t.Fatal("small payload should not truncate")
	}
	if strings.Contains(body, "iVBORw0KGgo") {
		t.Fatalf("image payload still present: %s", body)
	}
	if !strings.Contains(body, "[omitted image; media_type=image/png;") {
		t.Fatalf("placeholder missing: %s", body)
	}
}

func TestPrepareBodyReplacesDataURL(t *testing.T) {
	payload := map[string]any{
		"image_url": map[string]any{
			"url": "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQAAAQABAAD",
		},
	}
	raw, _ := json.Marshal(payload)
	body, _, _ := PrepareBody(raw)
	if strings.Contains(body, "/9j/") {
		t.Fatalf("data url still present: %s", body)
	}
	if !strings.Contains(body, "media_type=image/jpeg") {
		t.Fatalf("placeholder missing: %s", body)
	}
}

func TestPrepareBodyReplacesGeminiInlineData(t *testing.T) {
	payload := map[string]any{
		"inlineData": map[string]any{
			"mimeType": "image/webp",
			"data":     "UklGRiQAAABXRUJQVlA4IBgAAAAwAQCdASoBAAEAAwA0JaQAA3AA/vuUAAA=",
		},
	}
	raw, _ := json.Marshal(payload)
	body, _, _ := PrepareBody(raw)
	if strings.Contains(body, "UklGRiQ") {
		t.Fatalf("inline data still present: %s", body)
	}
	if !strings.Contains(body, "media_type=image/webp") {
		t.Fatalf("placeholder missing: %s", body)
	}
}

func TestPrepareBodyTruncatesOverCap(t *testing.T) {
	raw := []byte(`{"text":"` + strings.Repeat("a", MaxBodyBytes+64) + `"}`)
	body, originalBytes, truncated := PrepareBody(raw)
	if originalBytes != len(raw) {
		t.Fatalf("originalBytes=%d, want %d", originalBytes, len(raw))
	}
	if !truncated {
		t.Fatal("expected truncation")
	}
	if len(body) != MaxBodyBytes {
		t.Fatalf("len(body)=%d, want %d", len(body), MaxBodyBytes)
	}
}

func TestPrepareBodyRemovesSensitivePlaceholderID(t *testing.T) {
	placeholder := "[[MASKED:SECRET:7K4M2P9QAB2CD]]"
	barePlaceholder := "MASKED:API_TOKEN:MN2UKMT4Y62X2"
	body, _, _ := PrepareBody([]byte(`{"text":"` + placeholder + ` ` + barePlaceholder + `"}`))
	if strings.Contains(body, placeholder) || strings.Contains(body, barePlaceholder) ||
		!strings.Contains(body, "[[MASKED:SECRET:REDACTED]]") || !strings.Contains(body, "MASKED:API_TOKEN:REDACTED") {
		t.Fatalf("流量日志未移除占位符映射 ID: %s", body)
	}
}
