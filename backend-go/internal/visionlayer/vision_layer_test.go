package visionlayer

import (
	"strings"
	"testing"

	"github.com/BenedictKing/claude-proxy/internal/utils"
)

func TestReplaceImagesUsesEachImageOwnDescription(t *testing.T) {
	first := map[string]interface{}{
		"type": "image",
		"source": map[string]interface{}{
			"type": "base64",
			"data": "aGVsbG8=",
		},
	}
	second := map[string]interface{}{
		"type": "image",
		"source": map[string]interface{}{
			"type": "base64",
			"data": "d29ybGQ=",
		},
	}
	payload := map[string]interface{}{"messages": []interface{}{first, second}}
	descriptions := map[string]string{
		utils.ImageFingerprintForBlock(first):  "第一张图片：你好",
		utils.ImageFingerprintForBlock(second): "第二张图片：世界",
	}

	transformed, replaced, err := replaceImages(payload, descriptions)
	if err != nil {
		t.Fatalf("replaceImages() error = %v", err)
	}
	if replaced != 2 {
		t.Fatalf("replaced = %d, want 2", replaced)
	}

	result := transformed.(map[string]interface{})
	items := result["messages"].([]interface{})
	firstText := items[0].(map[string]interface{})["text"].(string)
	secondText := items[1].(map[string]interface{})["text"].(string)
	if !strings.Contains(firstText, "第一张图片：你好") || strings.Contains(firstText, "第二张图片：世界") {
		t.Fatalf("first image replacement = %q", firstText)
	}
	if !strings.Contains(secondText, "第二张图片：世界") || strings.Contains(secondText, "第一张图片：你好") {
		t.Fatalf("second image replacement = %q", secondText)
	}
}

func TestImageCacheKeyDoesNotChangeWithVisionModel(t *testing.T) {
	fingerprint := "sha256:example"
	if first, second := buildImageCacheKey(fingerprint), buildImageCacheKey(fingerprint); first != second || first != fingerprint {
		t.Fatalf("image cache key should only depend on fingerprint: %q %q", first, second)
	}
}

func TestParseVisionBatchResponseMatchesImagesByID(t *testing.T) {
	images := []visionImage{
		{id: "image_1"},
		{id: "image_2"},
	}
	raw := "```json\n{\"images\":[{\"id\":\"image_2\",\"description\":\"第二张\"},{\"id\":\"image_1\",\"description\":\"第一张\"}]}\n```"

	result, err := parseVisionBatchResponse(raw, images)
	if err != nil {
		t.Fatalf("parseVisionBatchResponse() error = %v", err)
	}
	if result["image_1"] != "第一张" || result["image_2"] != "第二张" {
		t.Fatalf("parseVisionBatchResponse() = %#v", result)
	}
}

func TestParseVisionBatchResponseAcceptsSingleImageMarkdown(t *testing.T) {
	images := []visionImage{{id: "image_1"}}
	raw := "```markdown\n这是一张终端截图，能看到一行错误信息。\n```"

	result, err := parseVisionBatchResponse(raw, images)
	if err != nil {
		t.Fatalf("parseVisionBatchResponse() error = %v", err)
	}
	if result["image_1"] != "这是一张终端截图，能看到一行错误信息。" {
		t.Fatalf("parseVisionBatchResponse() = %#v", result)
	}
}

func TestParseVisionBatchResponseAcceptsNamedMarkdownSections(t *testing.T) {
	images := []visionImage{{id: "image_1"}, {id: "image_2"}}
	raw := "```markdown\n[image_1]\n第一张图片的结果\n\n## 图片 2：第二张图片的结果\n```"

	result, err := parseVisionBatchResponse(raw, images)
	if err != nil {
		t.Fatalf("parseVisionBatchResponse() error = %v", err)
	}
	if result["image_1"] != "第一张图片的结果" || result["image_2"] != "第二张图片的结果" {
		t.Fatalf("parseVisionBatchResponse() = %#v", result)
	}
}

func TestParseVisionBatchResponseRejectsUnlabeledMultipleImages(t *testing.T) {
	images := []visionImage{{id: "image_1"}, {id: "image_2"}}
	if _, err := parseVisionBatchResponse("两张图片看起来都像终端截图。", images); err == nil {
		t.Fatal("parseVisionBatchResponse() error = nil")
	}
}

func TestParseVisionBatchResponseRejectsInvalidResults(t *testing.T) {
	images := []visionImage{
		{id: "image_1"},
		{id: "image_2"},
	}
	tests := []struct {
		name string
		raw  string
	}{
		{name: "缺少编号", raw: `{"images":[{"id":"image_1","description":"第一张"}]}`},
		{name: "重复编号", raw: `{"images":[{"id":"image_1","description":"第一张"},{"id":"image_1","description":"仍是第一张"}]}`},
		{name: "未知编号", raw: `{"images":[{"id":"image_1","description":"第一张"},{"id":"image_3","description":"第三张"}]}`},
		{name: "空描述", raw: `{"images":[{"id":"image_1","description":"第一张"},{"id":"image_2","description":""}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseVisionBatchResponse(tt.raw, images); err == nil {
				t.Fatal("parseVisionBatchResponse() error = nil")
			}
		})
	}
}
