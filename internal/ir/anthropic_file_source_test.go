package ir

import (
	"encoding/json"
	"testing"
)

// Anthropic Files API source.type="file" must survive the Parse → Serialize
// round trip for both images and documents. Regression for the 2026-09-05
// audit P0: parse dropped source.file_id and serialize emitted a truncated
// {"source":{"type":"file"}} that upstream rejects with HTTP 400.
func TestAnthropicFileSourceRoundTrip(t *testing.T) {
	raw := []byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 128,
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "image", "source": {"type": "file", "file_id": "file_img_abc123"}},
					{"type": "document", "source": {"type": "file", "file_id": "file_doc_def456"}, "title": "Spec"},
					{"type": "tool_result", "tool_use_id": "toolu_01", "content": [
						{"type": "image", "source": {"type": "file", "file_id": "file_img_ghi789"}}
					]}
				]
			}
		]
	}`)

	req, err := ParseAnthropic(raw)
	if err != nil {
		t.Fatalf("ParseAnthropic failed: %v", err)
	}

	if len(req.Messages) != 1 || len(req.Messages[0].Content) != 3 {
		t.Fatalf("unexpected message shape: %+v", req.Messages)
	}

	img := req.Messages[0].Content[0].Image
	if img == nil || img.Type != "file" || img.FileID != "file_img_abc123" {
		t.Fatalf("image file source not parsed into IR: %+v", img)
	}
	doc := req.Messages[0].Content[1].Document
	if doc == nil || doc.Source == nil || doc.Source.Type != "file" || doc.Source.FileID != "file_doc_def456" {
		t.Fatalf("document file source not parsed into IR: %+v", doc)
	}
	tr := req.Messages[0].Content[2].ToolResult
	if tr == nil || len(tr.Content) != 1 || tr.Content[0].Image == nil || tr.Content[0].Image.FileID != "file_img_ghi789" {
		t.Fatalf("tool_result multimodal content not parsed into IR: %+v", tr)
	}

	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}

	var wire struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("re-unmarshal serialized request: %v", err)
	}
	blocks := wire.Messages[0].Content
	if len(blocks) != 3 {
		t.Fatalf("expected 3 content blocks after round trip, got %d", len(blocks))
	}

	imgSrc, _ := blocks[0]["source"].(map[string]any)
	if imgSrc["type"] != "file" || imgSrc["file_id"] != "file_img_abc123" {
		t.Fatalf("image file_id lost/reshaped on serialize: %+v", imgSrc)
	}
	docSrc, _ := blocks[1]["source"].(map[string]any)
	if docSrc["type"] != "file" || docSrc["file_id"] != "file_doc_def456" {
		t.Fatalf("document file_id lost/reshaped on serialize: %+v", docSrc)
	}

	// tool_result content must keep the image block (audit A-#2)
	trContent, ok := blocks[2]["content"].([]any)
	if !ok || len(trContent) != 1 {
		t.Fatalf("tool_result multimodal content dropped on serialize: %+v", blocks[2]["content"])
	}
	trImg, _ := trContent[0].(map[string]any)
	trImgSrc, _ := trImg["source"].(map[string]any)
	if trImg["type"] != "image" || trImgSrc == nil || trImgSrc["file_id"] != "file_img_ghi789" {
		t.Fatalf("tool_result image file_id lost on serialize: %+v", trImg)
	}
}

// The legacy single-text tool_result wire shape (content as plain string)
// must remain untouched.
func TestAnthropicToolResultTextShorthandPreserved(t *testing.T) {
	raw := []byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 64,
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_01", "content": "42"}
				]
			}
		]
	}`)

	req, err := ParseAnthropic(raw)
	if err != nil {
		t.Fatalf("ParseAnthropic failed: %v", err)
	}
	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}

	var wire struct {
		Messages []struct {
			Content []struct {
				Content any `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("re-unmarshal serialized request: %v", err)
	}
	if s, ok := wire.Messages[0].Content[0].Content.(string); !ok || s != "42" {
		t.Fatalf("single text tool_result should serialize as string shorthand, got %#v", wire.Messages[0].Content[0].Content)
	}
}
