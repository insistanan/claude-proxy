package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestExtractImageFingerprints(t *testing.T) {
	body := []byte(`{
		"messages":[{"content":[
			{"type":"image","source":{"type":"base64","data":"aGVsbG8="}},
			{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}},
			{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}
		]}]
	}`)

	fingerprints := ExtractImageFingerprints(body)
	helloSum := sha256.Sum256([]byte("hello"))
	urlSum := sha256.Sum256([]byte("https://example.com/image.png"))
	wantImage := "sha256:" + hex.EncodeToString(helloSum[:])
	wantURL := "url-sha256:" + hex.EncodeToString(urlSum[:])

	if len(fingerprints) != 2 {
		t.Fatalf("fingerprint count = %d, want 2: %#v", len(fingerprints), fingerprints)
	}
	if fingerprints[0] != wantImage || fingerprints[1] != wantURL {
		t.Fatalf("fingerprints = %#v, want [%q %q]", fingerprints, wantImage, wantURL)
	}
}
