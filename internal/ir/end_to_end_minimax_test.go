package ir

import (
	"encoding/json"
	"testing"
)

// TestMiniMaxMultimodalEndToEnd 端到端测试：OpenAI格式 -> IR -> Anthropic/MiniMax格式
// 模拟真实的网关转换流程（不包含transformation层，避免循环依赖）
func TestMiniMaxMultimodalEndToEnd(t *testing.T) {
	// 步骤1：客户端发送 OpenAI 格式的图片请求
	openaiRequest := []byte(`{
		"model": "gpt-4-vision-preview",
		"messages": [{
			"role": "user",
			"content": [{
				"type": "text",
				"text": "请描述这张图片"
			}, {
				"type": "image_url",
				"image_url": {
					"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
					"detail": "high"
				}
			}]
		}],
		"max_tokens": 1024
	}`)

	t.Log("Step 1: Parse OpenAI request")
	irReq, err := ParseOpenAI(openaiRequest)
	if err != nil {
		t.Fatalf("ParseOpenAI failed: %v", err)
	}

	// 验证IR中的图片数据
	if len(irReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(irReq.Messages))
	}
	if len(irReq.Messages[0].Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(irReq.Messages[0].Content))
	}
	
	imageBlock := irReq.Messages[0].Content[1]
	if imageBlock.Type != "image" {
		t.Fatalf("expected image block, got %s", imageBlock.Type)
	}
	if imageBlock.Image == nil {
		t.Fatalf("image is nil")
	}
	if imageBlock.Image.Type != "base64" {
		t.Errorf("expected base64 image, got %s", imageBlock.Image.Type)
	}
	if imageBlock.Image.MediaType != "image/png" {
		t.Errorf("expected image/png, got %s", imageBlock.Image.MediaType)
	}
	if imageBlock.Image.Detail != "high" {
		t.Errorf("expected detail=high, got %s", imageBlock.Image.Detail)
	}
	expectedData := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	if imageBlock.Image.Data != expectedData {
		t.Errorf("image data mismatch")
	}

	t.Log("Step 2: Convert to Anthropic format for MiniMax")
	irReq.Model = "minimax-m3"
	irReq.TargetProvider = "minimax"
	irReq.MaxTokens = 1024

	anthBody, err := SerializeAnthropic(irReq)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}

	t.Log("Step 3: Verify final request body contains image data")
	var finalReq map[string]any
	if err := json.Unmarshal(anthBody, &finalReq); err != nil {
		t.Fatalf("unmarshal final body failed: %v", err)
	}

	messages := finalReq["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}

	msg := messages[0].(map[string]any)
	content := msg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(content))
	}

	// 验证文本块
	textBlock := content[0].(map[string]any)
	if textBlock["type"] != "text" {
		t.Errorf("expected text block, got %v", textBlock["type"])
	}

	// 验证图片块
	finalImageBlock := content[1].(map[string]any)
	if finalImageBlock["type"] != "image" {
		t.Errorf("expected image block, got %v", finalImageBlock["type"])
	}

	source := finalImageBlock["source"].(map[string]any)
	if source["type"] != "base64" {
		t.Errorf("expected source.type=base64, got %v", source["type"])
	}
	if source["media_type"] != "image/png" {
		t.Errorf("expected source.media_type=image/png, got %v", source["media_type"])
	}
	
	finalData, ok := source["data"].(string)
	if !ok {
		t.Fatalf("source.data is not a string")
	}
	if finalData != expectedData {
		t.Errorf("image data was corrupted in final output.\nExpected: %s\nGot: %s", expectedData, finalData)
	}

	// URL字段不应该出现在base64类型的图片中
	if _, hasURL := source["url"]; hasURL {
		t.Errorf("base64 image should not have url field")
	}

	t.Logf("✅ End-to-end test passed. Final MiniMax request:\n%s", string(anthBody))
}

// TestMiniMaxMultimodalWithURLImage 测试URL类型的图片
func TestMiniMaxMultimodalWithURLImage(t *testing.T) {
	openaiRequest := []byte(`{
		"model": "gpt-4-vision-preview",
		"messages": [{
			"role": "user",
			"content": [{
				"type": "text",
				"text": "What's in this image?"
			}, {
				"type": "image_url",
				"image_url": {
					"url": "https://example.com/photo.jpg"
				}
			}]
		}],
		"max_tokens": 500
	}`)

	irReq, err := ParseOpenAI(openaiRequest)
	if err != nil {
		t.Fatalf("ParseOpenAI failed: %v", err)
	}

	irReq.Model = "minimax-m3"
	irReq.TargetProvider = "minimax"

	anthBody, err := SerializeAnthropic(irReq)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}

	var finalReq map[string]any
	json.Unmarshal(anthBody, &finalReq)

	messages := finalReq["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	imageBlock := content[1].(map[string]any)
	source := imageBlock["source"].(map[string]any)

	if source["type"] != "url" {
		t.Errorf("expected source.type=url, got %v", source["type"])
	}
	if source["url"] != "https://example.com/photo.jpg" {
		t.Errorf("URL mismatch: %v", source["url"])
	}

	// URL类型不应该有data和media_type字段
	if _, hasData := source["data"]; hasData {
		t.Errorf("URL image should not have data field")
	}
	if _, hasMediaType := source["media_type"]; hasMediaType {
		t.Errorf("URL image should not have media_type field")
	}

	t.Logf("✅ URL image test passed")
}
