package visionlayer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/BenedictKing/claude-proxy/internal/scheduler"
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
	payload := map[string]interface{}{
		"messages": []interface{}{map[string]interface{}{
			"role":    "user",
			"content": []interface{}{first, second},
		}},
	}
	descriptions := map[string]string{
		utils.ImageFingerprintForBlock(first):  "第一张图片：你好",
		utils.ImageFingerprintForBlock(second): "第二张图片：世界",
	}

	transformed, err := transformImagesForUpstream(payload, "claude", descriptions)
	if err != nil {
		t.Fatalf("transformImagesForUpstream() error = %v", err)
	}

	result := transformed.(map[string]interface{})
	message := result["messages"].([]interface{})[0].(map[string]interface{})
	items := message["content"].([]interface{})
	firstText := items[0].(map[string]interface{})["text"].(string)
	secondText := items[1].(map[string]interface{})["text"].(string)
	if !strings.Contains(firstText, "第一张图片：你好") || strings.Contains(firstText, "第二张图片：世界") {
		t.Fatalf("first image replacement = %q", firstText)
	}
	if !strings.Contains(secondText, "第二张图片：世界") || strings.Contains(secondText, "第一张图片：你好") {
		t.Fatalf("second image replacement = %q", secondText)
	}
}

func TestImageCacheKeyIncludesVisionProfile(t *testing.T) {
	fingerprint := "sha256:example"
	first := buildImageCacheKey(fingerprint, scheduler.ChannelKindMessages, "vision-channel", "vision-model-a")
	second := buildImageCacheKey(fingerprint, scheduler.ChannelKindMessages, "vision-channel", "vision-model-a")
	changedModel := buildImageCacheKey(fingerprint, scheduler.ChannelKindMessages, "vision-channel", "vision-model-b")
	if first != second {
		t.Fatalf("same vision profile should have stable cache key: %q %q", first, second)
	}
	if first == changedModel {
		t.Fatalf("different vision models should not share cache key: %q", first)
	}
}

func TestTransformImagesForTargetProtocols(t *testing.T) {
	tests := []struct {
		name        string
		serviceType string
		image       map[string]interface{}
		payload     func(map[string]interface{}) map[string]interface{}
	}{
		{
			name:        "Claude Messages",
			serviceType: "claude",
			image: map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{
					"type": "base64",
					"data": "aGVsbG8=",
				},
			},
			payload: func(image map[string]interface{}) map[string]interface{} {
				return map[string]interface{}{"messages": []interface{}{map[string]interface{}{
					"role": "user", "content": []interface{}{image},
				}}}
			},
		},
		{
			name:        "OpenAI Chat",
			serviceType: "openai",
			image: map[string]interface{}{
				"type":      "image_url",
				"image_url": map[string]interface{}{"url": "https://example.com/image.png"},
			},
			payload: func(image map[string]interface{}) map[string]interface{} {
				return map[string]interface{}{"messages": []interface{}{map[string]interface{}{
					"role": "user", "content": []interface{}{image},
				}}}
			},
		},
		{
			name:        "OpenAI Responses",
			serviceType: "responses",
			image: map[string]interface{}{
				"type":      "input_image",
				"image_url": "https://example.com/image.png",
			},
			payload: func(image map[string]interface{}) map[string]interface{} {
				return map[string]interface{}{"input": []interface{}{map[string]interface{}{
					"type": "message", "role": "user", "content": []interface{}{image},
				}}}
			},
		},
		{
			name:        "Gemini",
			serviceType: "gemini",
			image: map[string]interface{}{
				"inlineData": map[string]interface{}{"mimeType": "image/png", "data": "aGVsbG8="},
			},
			payload: func(image map[string]interface{}) map[string]interface{} {
				return map[string]interface{}{"contents": []interface{}{map[string]interface{}{
					"role": "user", "parts": []interface{}{image},
				}}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := tt.payload(tt.image)
			fingerprint := utils.ImageFingerprintForBlock(tt.image)
			transformed, err := transformImagesForUpstream(payload, tt.serviceType, map[string]string{
				fingerprint: "稳定的图片描述",
			})
			if err != nil {
				t.Fatalf("transformImagesForUpstream() error = %v", err)
			}
			images, err := collectImagesForUpstream(transformed, tt.serviceType)
			if err != nil {
				t.Fatalf("collectImagesForUpstream() error = %v", err)
			}
			if len(images) != 0 {
				t.Fatalf("transformed payload still contains %d image blocks", len(images))
			}
			if text := extractUserText(transformed); !strings.Contains(text, "稳定的图片描述") {
				t.Fatalf("transformed payload text = %q", text)
			}
		})
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

func TestVisionMemoryCacheHasHardLimit(t *testing.T) {
	ClearCache()
	for index := 0; index < 600; index++ {
		storeMemoryCache(fmt.Sprintf("key-%d", index), "result")
	}
	visionCache.Lock()
	count := len(visionCache.items)
	visionCache.Unlock()
	if count != 512 {
		t.Fatalf("memory cache size = %d, want 512", count)
	}
	ClearCache()
}

func TestClaimAnalysisDeduplicatesConcurrentImage(t *testing.T) {
	key := "conversation\x00image"
	ClearCache()
	visionCache.Lock()
	visionCache.inflight = make(map[string]*visionInflightCall)
	visionCache.Unlock()

	_, cached, ownerCall, owner := claimAnalysis(key)
	if cached || !owner || ownerCall == nil {
		t.Fatalf("first claim = cached:%v owner:%v call:%v", cached, owner, ownerCall)
	}
	_, cached, waiterCall, owner := claimAnalysis(key)
	if cached || owner || waiterCall != ownerCall {
		t.Fatalf("second claim = cached:%v owner:%v sameCall:%v", cached, owner, waiterCall == ownerCall)
	}

	storeMemoryCache(key, "shared result")
	finishAnalysis(key, ownerCall, "shared result", nil)
	<-waiterCall.done
	if waiterCall.err != nil || waiterCall.result != "shared result" {
		t.Fatalf("waiter result = (%q, %v)", waiterCall.result, waiterCall.err)
	}
	result, cached, _, owner := claimAnalysis(key)
	if !cached || owner || result != "shared result" {
		t.Fatalf("completed claim = (%q, cached:%v, owner:%v)", result, cached, owner)
	}
	ClearCache()
}

func TestToClaudeImageBlockSupportsGeminiFileData(t *testing.T) {
	block, ok := toClaudeImageBlock(map[string]interface{}{
		"fileData": map[string]interface{}{
			"mimeType": "image/png",
			"fileUri":  "https://example.com/image.png",
		},
	})
	if !ok {
		t.Fatal("toClaudeImageBlock() ok = false")
	}
	source := block["source"].(map[string]interface{})
	if source["type"] != "url" || source["url"] != "https://example.com/image.png" {
		t.Fatalf("source = %#v", source)
	}
}
