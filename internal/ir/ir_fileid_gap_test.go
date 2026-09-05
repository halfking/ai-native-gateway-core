package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// Regression tests for audit finding A-#18 (2026-09-05 round 2 follow-up):
// the message-level file_id P0 fix left three adjacent gaps.
//
//   - (a) top-level Anthropic documents did not read/emit source.file_id,
//     producing a truncated {"source":{"type":"file"}}.
//   - (b) the OpenAI parser encoded file ids in DocumentSource.Data while the
//     Anthropic parser (and serializers) used DocumentSource.FileID, so an
//     OpenAI file document routed to an Anthropic upstream serialized an
//     empty source {"type":"file_id"}.
//   - (c) file_id images serialized to OpenAI Chat / Responses as
//     image_url:"" even though Responses natively supports
//     input_image.file_id.

// ─── (a) top-level documents file_id ───

// TestAnthropicTopLevelDocumentsFileIDRoundTrip verifies a Files API
// top-level document survives Parse → Serialize with its file_id intact.
func TestAnthropicTopLevelDocumentsFileIDRoundTrip(t *testing.T) {
	raw := []byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 128,
		"documents": [
			{
				"type": "document",
				"source": {"type": "file", "file_id": "file_top_abc123"},
				"title": "Spec",
				"context": "quarterly report"
			},
			{
				"type": "document",
				"source": {"type": "base64", "media_type": "application/pdf", "data": "JVBERi0="},
				"title": "Legacy.pdf"
			}
		],
		"messages": [
			{"role": "user", "content": "summarize the documents"}
		]
	}`)

	req, err := ParseAnthropic(raw)
	if err != nil {
		t.Fatalf("ParseAnthropic failed: %v", err)
	}
	if len(req.Documents) != 2 {
		t.Fatalf("expected 2 top-level documents, got %d", len(req.Documents))
	}
	fileDoc := req.Documents[0]
	if fileDoc.Source.Type != "file" || fileDoc.Source.FileID != "file_top_abc123" {
		t.Fatalf("top-level file document not parsed into IR: %+v", fileDoc.Source)
	}

	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}

	var wire struct {
		Documents []struct {
			Type   string `json:"type"`
			Title  string `json:"title"`
			Source struct {
				Type      string `json:"type"`
				FileID    string `json:"file_id"`
				Data      string `json:"data"`
				MediaType string `json:"media_type"`
			} `json:"source"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("re-unmarshal serialized request: %v", err)
	}
	if len(wire.Documents) != 2 {
		t.Fatalf("expected 2 documents on the wire, got %d", len(wire.Documents))
	}
	if wire.Documents[0].Source.Type != "file" || wire.Documents[0].Source.FileID != "file_top_abc123" {
		t.Fatalf("top-level document file_id lost/reshaped on serialize: %+v", wire.Documents[0].Source)
	}
	if wire.Documents[0].Title != "Spec" {
		t.Errorf("document title lost: %q", wire.Documents[0].Title)
	}
	// The base64 sibling must keep its payload (no file-source leakage).
	if wire.Documents[1].Source.Type != "base64" || wire.Documents[1].Source.Data != "JVBERi0=" {
		t.Errorf("base64 top-level document regressed: %+v", wire.Documents[1].Source)
	}
}

// TestAnthropicTopLevelDocumentsLegacyDataFileID pins the backward-compatible
// read for rows persisted before the unified FileID field (the session
// adapter collapses FileID into Data with Type="file_id").
func TestAnthropicTopLevelDocumentsLegacyDataFileID(t *testing.T) {
	req := &InternalRequest{
		Model: "claude-sonnet-4-5",
		Documents: []Document{
			{Type: "document", Source: DocumentSource{Type: "file_id", Data: "file_legacy_001"}},
		},
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	}
	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}
	if !strings.Contains(string(out), `"file_id":"file_legacy_001"`) {
		t.Fatalf("legacy Data-encoded file id not emitted on the wire: %s", out)
	}
}

// ─── (b) OpenAI file document → Anthropic (dual-track unification) ───

// TestOpenAIFileDocumentFileIDToAnthropic verifies the OpenAI parser now
// populates DocumentSource.FileID and that the document serializes to a
// complete Anthropic file source instead of {"type":"file_id"}.
func TestOpenAIFileDocumentFileIDToAnthropic(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "what does this file say?"},
					{"type": "file", "file": {"filename": "a.pdf", "file_id": "file-abc-123"}}
				]
			}
		]
	}`)

	req, err := ParseOpenAI(raw)
	if err != nil {
		t.Fatalf("ParseOpenAI failed: %v", err)
	}
	blocks := req.Messages[0].Content
	if len(blocks) != 2 || blocks[1].Document == nil || blocks[1].Document.Source == nil {
		t.Fatalf("file block not parsed into IR: %+v", blocks)
	}
	src := blocks[1].Document.Source
	if src.Type != "file_id" || src.FileID != "file-abc-123" {
		t.Fatalf("DocumentSource.FileID not populated by the OpenAI parser: %+v", src)
	}

	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic failed: %v", err)
	}

	var wire struct {
		Messages []struct {
			Content []struct {
				Type   string `json:"type"`
				Source struct {
					Type   string `json:"type"`
					FileID string `json:"file_id"`
				} `json:"source"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("re-unmarshal serialized request: %v", err)
	}
	docBlock := wire.Messages[0].Content[1]
	if docBlock.Type != "document" {
		t.Fatalf("expected document block on Anthropic wire, got %+v", docBlock)
	}
	if docBlock.Source.Type != "file" || docBlock.Source.FileID != "file-abc-123" {
		t.Fatalf("OpenAI file_id lost routing Anthropic: %+v", docBlock.Source)
	}
}

// TestOpenAIFileDocumentFileIDResponsesRoundTrip verifies the same unified
// FileID field round-trips through the Responses input_file shape.
func TestOpenAIFileDocumentFileIDResponsesRoundTrip(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [
					{"type": "input_file", "file_id": "file-resp-99", "filename": "notes.txt"}
				]
			}
		]
	}`)

	req, err := ParseResponses(raw)
	if err != nil {
		t.Fatalf("ParseResponses failed: %v", err)
	}
	doc := req.Messages[0].Content[0].Document
	if doc == nil || doc.Source == nil || doc.Source.FileID != "file-resp-99" {
		t.Fatalf("input_file.file_id not parsed into IR: %+v", doc)
	}

	out, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatalf("SerializeResponsesRequest failed: %v", err)
	}
	if !strings.Contains(string(out), `"file_id":"file-resp-99"`) {
		t.Fatalf("file_id lost on Responses serialize: %s", out)
	}
}

// ─── (c) file_id images ───

// TestResponsesFileIDImageRoundTrip verifies an input_image with a file_id
// round-trips through the Responses serializer natively (no empty image_url).
func TestResponsesFileIDImageRoundTrip(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [
					{"type": "input_image", "file_id": "file-img-42", "detail": "high"}
				]
			}
		]
	}`)

	req, err := ParseResponses(raw)
	if err != nil {
		t.Fatalf("ParseResponses failed: %v", err)
	}
	img := req.Messages[0].Content[0].Image
	if img == nil || img.FileID != "file-img-42" {
		t.Fatalf("input_image.file_id not parsed into IR: %+v", img)
	}

	out, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatalf("SerializeResponsesRequest failed: %v", err)
	}
	if !strings.Contains(string(out), `"file_id":"file-img-42"`) {
		t.Fatalf("image file_id lost on Responses serialize: %s", out)
	}
	if strings.Contains(string(out), `"image_url":""`) {
		t.Fatalf("empty image_url still emitted for a file_id image: %s", out)
	}

	// Same-protocol re-parse must see the id again.
	req2, err := ParseResponses(out)
	if err != nil {
		t.Fatalf("re-parse serialized request: %v", err)
	}
	img2 := req2.Messages[0].Content[0].Image
	if img2 == nil || img2.FileID != "file-img-42" {
		t.Fatalf("same-protocol Responses round trip lost the image file_id: %+v", img2)
	}
}

// TestOpenAIChatFileIDImageLoss verifies a file_id image routed to an OpenAI
// Chat upstream produces an explicit loss event and never an image_url:""
// block.
func TestOpenAIChatFileIDImageLoss(t *testing.T) {
	cap := resetDedupAndInstall(t)

	req := &InternalRequest{
		Model:          "gpt-4o",
		SourceProtocol: ProtocolAnthropicMessages,
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "image", Image: &ImageSource{Type: "file", FileID: "file_img_abc123"}},
					{Type: "text", Text: "describe the image"},
				},
			},
		},
	}

	out, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI failed: %v", err)
	}

	// The file_id image block must be dropped, not emitted as image_url:"".
	if strings.Contains(string(out), `"image_url":""`) {
		t.Fatalf("empty image_url emitted for a file_id image: %s", out)
	}
	var wire struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("re-unmarshal serialized request: %v", err)
	}
	content := wire.Messages[0].Content
	if len(content) != 1 || content[0]["type"] != "text" {
		t.Fatalf("file_id image block should be dropped, content now: %+v", content)
	}

	if !cap.hasEvent(AnomalyEvent{
		AnomalyType:    AnomalyProtocolLoss,
		FieldPath:      "messages[0].content[0].image.file_id",
		TargetProtocol: ProtocolOpenAIChat,
		Reason:         "loss",
	}) {
		t.Fatalf("expected ir_protocol_loss for the file_id image; events=%+v", cap.snapshot())
	}
}

// TestOpenAIChatURLImageUnaffected pins the no-regression case: URL-bearing
// images keep serializing to image_url with the URL intact.
func TestOpenAIChatURLImageUnaffected(t *testing.T) {
	req := &InternalRequest{
		Model: "gpt-4o",
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "image", Image: &ImageSource{Type: "url", URL: "https://example.com/i.png"}},
				},
			},
		},
	}
	out, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI failed: %v", err)
	}
	if !strings.Contains(string(out), "https://example.com/i.png") {
		t.Fatalf("URL image regressed: %s", out)
	}
}
