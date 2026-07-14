package streaming

import "testing"

func TestConvertBlockMessage_PreservesTextAroundImage(t *testing.T) {
	message := convertBlockMessage("user", []any{
		map[string]any{"type": "text", "text": "describe this"},
		map[string]any{"type": "image", "source": map[string]any{
			"type": "base64", "media_type": "image/png", "data": "Zm9v",
		}},
	})
	parts, ok := message["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("content = %#v, want two content parts", message["content"])
	}
	if parts[0].(map[string]any)["text"] != "describe this" {
		t.Errorf("text block lost: %v", parts[0])
	}
	if parts[1].(map[string]any)["type"] != "image_url" {
		t.Errorf("image block lost: %v", parts[1])
	}
}

func TestConvertBridgeChatMessageToAnthropic_DataURIUsesBase64Source(t *testing.T) {
	message := convertBridgeChatMessageToAnthropic(map[string]any{
		"role": "user",
		"content": []any{map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": "data:image/png;base64,Zm9v"},
		}},
	})
	parts := message["content"].([]any)
	source := parts[0].(map[string]any)["source"].(map[string]any)
	if source["type"] != "base64" || source["data"] != "Zm9v" {
		t.Errorf("data URI was not preserved: %v", source)
	}
}
