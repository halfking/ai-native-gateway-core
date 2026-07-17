package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestOpenAIToMinimaxImageConversion 测试从OpenAI格式到MiniMax的图片转换
// 验证多模态图片数据在经过网关转换后是否完整保留
func TestOpenAIToMinimaxImageConversion(t *testing.T) {
	// 模拟OpenAI格式的图片请求（base64图片）
	openaiBody := []byte(`{
		"model": "gpt-4-vision",
		"messages": [{
			"role": "user",
			"content": [{
				"type": "text",
				"text": "What is in this image?"
			}, {
				"type": "image_url",
				"image_url": {
					"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
				}
			}]
		}]
	}`)

	// 1. 解析OpenAI请求
	irReq, err := ParseOpenAI(openaiBody)
	if err != nil {
		t.Fatalf("ParseOpenAI failed: %v", err)
	}

	// 验证IR中包含图片数据
	if len(irReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(irReq.Messages))
	}
	msg := irReq.Messages[0]
	if len(msg.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(msg.Content))
	}

	// 检查第二个块是否为图片
	imageBlock := msg.Content[1]
	if imageBlock.Type != "image" {
		t.Fatalf("expected image block, got %s", imageBlock.Type)
	}
	if imageBlock.Image == nil {
		t.Fatalf("image block has nil Image")
	}

	// 验证图片数据是否正确解析
	if imageBlock.Image.Type != "base64" {
		t.Errorf("expected Type=base64, got %s", imageBlock.Image.Type)
	}
	if imageBlock.Image.MediaType != "image/png" {
		t.Errorf("expected MediaType=image/png, got %s", imageBlock.Image.MediaType)
	}
	expectedData := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	if imageBlock.Image.Data != expectedData {
		t.Errorf("image data mismatch.\nExpected: %s\nGot: %s", expectedData, imageBlock.Image.Data)
	}

	// 2. 设置目标为MiniMax，序列化为Anthropic格式
	irReq.Model = "minimax-m3"
	irReq.MaxTokens = 1024
	irReq.TargetProvider = "minimax"

	anthBody, err := SerializeAnthropic(irReq)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}

	// 3. 验证序列化后的Anthropic格式包含完整的图片数据
	var anthReq map[string]any
	if err := json.Unmarshal(anthBody, &anthReq); err != nil {
		t.Fatalf("json.Unmarshal anthropic body failed: %v", err)
	}

	messages, ok := anthReq["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("expected 1 message in anthropic format, got %d", len(messages))
	}

	firstMsg, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("message is not a map")
	}

	content, ok := firstMsg["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("expected 2 content blocks in anthropic format, got %d", len(content))
	}

	// 检查图片块
	imageBlockMap, ok := content[1].(map[string]any)
	if !ok {
		t.Fatalf("image block is not a map")
	}

	if imageBlockMap["type"] != "image" {
		t.Fatalf("expected type=image, got %v", imageBlockMap["type"])
	}

	source, ok := imageBlockMap["source"].(map[string]any)
	if !ok {
		t.Fatalf("image block missing source field")
	}

	// 验证source字段完整性
	if source["type"] != "base64" {
		t.Errorf("expected source.type=base64, got %v", source["type"])
	}
	if source["media_type"] != "image/png" {
		t.Errorf("expected source.media_type=image/png, got %v", source["media_type"])
	}
	
	sourceData, ok := source["data"].(string)
	if !ok {
		t.Fatalf("source.data is not a string")
	}
	if sourceData != expectedData {
		t.Errorf("source.data mismatch.\nExpected: %s\nGot: %s", expectedData, sourceData)
	}

	t.Logf("✅ Image conversion test passed. Anthropic body:\n%s", string(anthBody))
}

// TestOpenAIToMinimaxImageURL 测试URL类型的图片转换
func TestOpenAIToMinimaxImageURL(t *testing.T) {
	openaiBody := []byte(`{
		"model": "gpt-4-vision",
		"messages": [{
			"role": "user",
			"content": [{
				"type": "text",
				"text": "Describe this image"
			}, {
				"type": "image_url",
				"image_url": {
					"url": "https://example.com/image.jpg"
				}
			}]
		}]
	}`)

	irReq, err := ParseOpenAI(openaiBody)
	if err != nil {
		t.Fatalf("ParseOpenAI failed: %v", err)
	}

	imageBlock := irReq.Messages[0].Content[1]
	if imageBlock.Type != "image" {
		t.Fatalf("expected image block")
	}
	if imageBlock.Image.Type != "url" {
		t.Errorf("expected Type=url, got %s", imageBlock.Image.Type)
	}
	if imageBlock.Image.URL != "https://example.com/image.jpg" {
		t.Errorf("URL mismatch: %s", imageBlock.Image.URL)
	}

	irReq.Model = "minimax-m3"
	irReq.MaxTokens = 1024
	irReq.TargetProvider = "minimax"

	anthBody, err := SerializeAnthropic(irReq)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}

	var anthReq map[string]any
	json.Unmarshal(anthBody, &anthReq)

	messages := anthReq["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	imageBlockMap := content[1].(map[string]any)
	source := imageBlockMap["source"].(map[string]any)

	if source["type"] != "url" {
		t.Errorf("expected source.type=url, got %v", source["type"])
	}
	if source["url"] != "https://example.com/image.jpg" {
		t.Errorf("expected source.url=https://example.com/image.jpg, got %v", source["url"])
	}

	// URL类型不应该有data和media_type字段
	if _, hasData := source["data"]; hasData {
		t.Errorf("URL image should not have data field")
	}

	t.Logf("✅ URL image conversion test passed")
}

// TestMinimaxDirectImageRequest 测试直接发送Anthropic格式到MiniMax
func TestMinimaxDirectImageRequest(t *testing.T) {
	// 直接使用Anthropic格式的请求
	anthBody := []byte(`{
		"model": "minimax-m3",
		"max_tokens": 1024,
		"messages": [{
			"role": "user",
			"content": [{
				"type": "text",
				"text": "What is in this image?"
			}, {
				"type": "image",
				"source": {
					"type": "base64",
					"media_type": "image/jpeg",
					"data": "/9j/4AAQSkZJRgABAQEASABIAAD/2wBDAAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB"
				}
			}]
		}]
	}`)

	irReq, err := ParseAnthropic(anthBody)
	if err != nil {
		t.Fatalf("ParseAnthropic failed: %v", err)
	}

	imageBlock := irReq.Messages[0].Content[1]
	if imageBlock.Type != "image" {
		t.Fatalf("expected image block")
	}
	if imageBlock.Image == nil {
		t.Fatalf("image is nil")
	}
	if imageBlock.Image.Type != "base64" {
		t.Errorf("expected Type=base64, got %s", imageBlock.Image.Type)
	}
	if imageBlock.Image.MediaType != "image/jpeg" {
		t.Errorf("expected MediaType=image/jpeg, got %s", imageBlock.Image.MediaType)
	}

	// 重新序列化
	irReq.TargetProvider = "minimax"
	newBody, err := SerializeAnthropic(irReq)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}

	// 验证图片数据保留
	if !strings.Contains(string(newBody), `"type":"image"`) {
		t.Errorf("serialized body missing image type")
	}
	if !strings.Contains(string(newBody), `"type":"base64"`) {
		t.Errorf("serialized body missing base64 type")
	}
	if !strings.Contains(string(newBody), `"media_type":"image/jpeg"`) {
		t.Errorf("serialized body missing media_type")
	}
	if !strings.Contains(string(newBody), `"data":"/9j/4AAQSkZJRgABAQEASABIAAD/2wBDAAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB"`) {
		t.Errorf("serialized body missing image data")
	}

	t.Logf("✅ Direct Anthropic format test passed")
}
