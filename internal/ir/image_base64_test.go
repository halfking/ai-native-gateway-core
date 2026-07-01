package ir

import (
	"encoding/base64"
	"strings"
	"testing"
)

// 测试 OpenAI 格式的 base64 图片解析：data URI 应被识别为 base64 类型，
// MediaType 和 Data 被正确填充（2026-07-01 附件修复的回归测试）。
func TestParseOpenAIImageBlock_Base64(t *testing.T) {
	pngB64 := base64.StdEncoding.EncodeToString([]byte("fake-png-data"))
	block := map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": "data:image/png;base64," + pngB64,
		},
	}

	img := parseOpenAIImageBlock(block)
	if img.Type != "base64" {
		t.Errorf("Type = %q, want base64", img.Type)
	}
	if img.MediaType != "image/png" {
		t.Errorf("MediaType = %q, want image/png", img.MediaType)
	}
	if img.Data != pngB64 {
		t.Errorf("Data not set correctly")
	}
	// URL 应保留原始 data URI
	if !strings.HasPrefix(img.URL, "data:image/png") {
		t.Errorf("URL should preserve original data URI, got prefix %q", img.URL[:20])
	}
}

// HTTP URL 应识别为 url 类型，不填充 Data。
func TestParseOpenAIImageBlock_HTTPURL(t *testing.T) {
	block := map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": "https://example.com/image.png",
		},
	}
	img := parseOpenAIImageBlock(block)
	if img.Type != "url" {
		t.Errorf("Type = %q, want url", img.Type)
	}
	if img.Data != "" {
		t.Errorf("Data should be empty for HTTP URL, got %q", img.Data)
	}
	if img.URL != "https://example.com/image.png" {
		t.Errorf("URL mismatch")
	}
}

// 关键回归测试：OpenAI base64 图片 → IR → Anthropic 序列化，
// 确保上游 Claude 能收到正确的 source.base64 结构（修复前的 bug：
// base64 图片转换后丢失，上游 LLM 收不到可用图片）。
func TestOpenAIBase64Image_To_Anthropic(t *testing.T) {
	pngB64 := base64.StdEncoding.EncodeToString([]byte("fake-png-bytes"))
	openAIBody := []byte(`{
		"model": "claude-3-5-sonnet",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "describe"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,` + pngB64 + `"}}
			]
		}],
		"max_tokens": 100
	}`)

	irReq, err := ParseOpenAI(openAIBody)
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}

	// 验证 IR 正确解析为 base64
	if len(irReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(irReq.Messages))
	}
	blocks := irReq.Messages[0].Content
	var imgBlock *ContentBlock
	for i := range blocks {
		if blocks[i].Type == "image" {
			imgBlock = &blocks[i]
			break
		}
	}
	if imgBlock == nil {
		t.Fatal("no image block found")
	}
	if imgBlock.Image.Type != "base64" {
		t.Errorf("IR image type = %q, want base64", imgBlock.Image.Type)
	}
	if imgBlock.Image.Data != pngB64 {
		t.Error("IR image data not preserved")
	}

	// 序列化为 Anthropic
	anthropicBody, err := SerializeAnthropic(irReq)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}
	bodyStr := string(anthropicBody)

	// Anthropic 应输出 source.type=base64 + media_type + data
	if !strings.Contains(bodyStr, `"type":"base64"`) {
		t.Errorf("Anthropic output missing source.type=base64:\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"media_type":"image/png"`) {
		t.Errorf("Anthropic output missing media_type")
	}
	if !strings.Contains(bodyStr, pngB64) {
		t.Errorf("Anthropic output missing base64 data")
	}
	// 关键：base64 类型不应带 url 字段（修复前会错误地输出 url）
	if strings.Contains(bodyStr, `"url":"data:`) {
		t.Errorf("Anthropic base64 source should NOT contain url field:\n%s", bodyStr)
	}
}

// 反向：Anthropic base64 → IR → OpenAI 序列化，
// 确保 OpenAI 上游收到重建的 data URI。
func TestAnthropicBase64Image_To_OpenAI(t *testing.T) {
	pngB64 := base64.StdEncoding.EncodeToString([]byte("reverse-bytes"))
	anthropicBody := []byte(`{
		"model": "gpt-4o",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "` + pngB64 + `"}},
				{"type": "text", "text": "describe"}
			]
		}],
		"max_tokens": 100
	}`)

	irReq, err := ParseAnthropic(anthropicBody)
	if err != nil {
		t.Fatalf("ParseAnthropic: %v", err)
	}

	openAIBody, err := SerializeOpenAI(irReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	bodyStr := string(openAIBody)

	// OpenAI 应输出 image_url 带 data URI
	if !strings.Contains(bodyStr, `"type":"image_url"`) {
		t.Errorf("OpenAI output missing image_url type")
	}
	if !strings.Contains(bodyStr, "data:image/png;base64,"+pngB64) {
		t.Errorf("OpenAI output missing reconstructed data URI:\n%s", bodyStr)
	}
}
