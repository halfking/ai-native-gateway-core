package transformation

import (
	"encoding/json"
	"testing"
)

// TestFixAnthropicMessages_PreservesImageBlocks 测试 FixAnthropicMessages 是否保留图片块
func TestFixAnthropicMessages_PreservesImageBlocks(t *testing.T) {
	// 包含图片的 Anthropic 请求
	body := []byte(`{
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
					"media_type": "image/png",
					"data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
				}
			}]
		}, {
			"role": "assistant",
			"content": "I see a small image."
		}]
	}`)

	fixed, err := FixAnthropicMessages(body)
	if err != nil {
		t.Fatalf("FixAnthropicMessages failed: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(fixed, &result); err != nil {
		t.Fatalf("unmarshal fixed body failed: %v", err)
	}

	messages := result["messages"].([]any)
	firstMsg := messages[0].(map[string]any)
	content := firstMsg["content"].([]any)

	// 验证有两个内容块：text 和 image
	if len(content) != 2 {
		t.Errorf("expected 2 content blocks, got %d", len(content))
	}

	// 验证第一个是 text
	textBlock := content[0].(map[string]any)
	if textBlock["type"] != "text" {
		t.Errorf("expected first block type=text, got %v", textBlock["type"])
	}

	// 验证第二个是 image
	imageBlock := content[1].(map[string]any)
	if imageBlock["type"] != "image" {
		t.Errorf("expected second block type=image, got %v", imageBlock["type"])
	}

	source := imageBlock["source"].(map[string]any)
	if source["type"] != "base64" {
		t.Errorf("expected source.type=base64, got %v", source["type"])
	}
	if source["data"] != "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==" {
		t.Errorf("image data was lost or corrupted")
	}

	t.Logf("✅ Image blocks preserved after FixAnthropicMessages")
}

// TestFixAnthropicMessages_MergeConsecutiveWithImages 测试合并连续消息时保留图片
func TestFixAnthropicMessages_MergeConsecutiveWithImages(t *testing.T) {
	// 两条连续的 user 消息，其中一条包含图片
	body := []byte(`{
		"model": "minimax-m3",
		"max_tokens": 1024,
		"messages": [{
			"role": "user",
			"content": [{
				"type": "text",
				"text": "First message"
			}]
		}, {
			"role": "user",
			"content": [{
				"type": "text",
				"text": "Second message with image"
			}, {
				"type": "image",
				"source": {
					"type": "base64",
					"media_type": "image/jpeg",
					"data": "/9j/4AAQSkZJRg"
				}
			}]
		}]
	}`)

	fixed, err := FixAnthropicMessages(body)
	if err != nil {
		t.Fatalf("FixAnthropicMessages failed: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(fixed, &result); err != nil {
		t.Fatalf("unmarshal fixed body failed: %v", err)
	}

	messages := result["messages"].([]any)
	
	// 应该合并成一条消息
	if len(messages) != 1 {
		t.Fatalf("expected 1 merged message, got %d", len(messages))
	}

	msg := messages[0].(map[string]any)
	content := msg["content"].([]any)

	// 验证有3个内容块：text1, text2, image
	if len(content) != 3 {
		t.Errorf("expected 3 content blocks after merge, got %d", len(content))
	}

	// 验证第三个块是图片
	imageBlock := content[2].(map[string]any)
	if imageBlock["type"] != "image" {
		t.Errorf("expected third block type=image, got %v", imageBlock["type"])
	}

	source := imageBlock["source"].(map[string]any)
	if source["data"] != "/9j/4AAQSkZJRg" {
		t.Errorf("image data was lost during merge")
	}

	t.Logf("✅ Image blocks preserved after merging consecutive messages")
}
