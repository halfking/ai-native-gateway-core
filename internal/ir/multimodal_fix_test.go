package ir

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// Regression tests for the multimodal serialization fixes (2026-08-10).
// These pin the behaviors that were previously broken:
//   - parseOpenAIFileBlock no longer drops data: URIs without base64,
//   - buildGeminiContents emits input_audio (instead of dropping it),
//   - serializeOpenAIAudioBlock emits type "input_audio" (not "audio"),
//   - irDocumentToGeminiPart emits inlineData for text docs (not fileUri),
//   - Gemini media converters skip empty/unresolvable file refs (no empty fileUri).

// ─── parseOpenAIFileBlock ───

// TestParseOpenAIFileBlock_DataURINoBase64 verifies that a data: URI without the
// base64 token (e.g. data:text/plain,hello) is parsed as inline text instead of
// being silently dropped. Previously idx==-1 left src.Type=="file" and the block
// returned nil.
func TestParseOpenAIFileBlock_DataURINoBase64(t *testing.T) {
	db := parseOpenAIFileBlock(map[string]any{
		"file_data": "data:text/plain,hello world",
		"mime_type": "text/plain",
	})
	if db == nil {
		t.Fatal("parseOpenAIFileBlock returned nil for non-base64 data: URI; expected inline text")
	}
	if db.Source == nil || db.Source.Type != "text" {
		t.Fatalf("source type = %q, want \"text\"", nilStr(db.Source))
	}
	if db.Source.Data != "hello world" {
		t.Errorf("payload = %q, want %q", db.Source.Data, "hello world")
	}
	if db.Source.MediaType != "text/plain" {
		t.Errorf("media type = %q, want %q", db.Source.MediaType, "text/plain")
	}
}

// TestParseOpenAIFileBlock_DataURIBase64StillWorks verifies the original base64
// path is not regressed.
func TestParseOpenAIFileBlock_DataURIBase64StillWorks(t *testing.T) {
	db := parseOpenAIFileBlock(map[string]any{
		"file_data": "data:application/pdf;base64,JVBERi0=",
	})
	if db == nil {
		t.Fatal("returned nil for base64 data: URI")
	}
	if db.Source.Type != "base64" {
		t.Errorf("source type = %q, want base64", db.Source.Type)
	}
	if db.Source.MediaType != "application/pdf" {
		t.Errorf("media type = %q, want application/pdf", db.Source.MediaType)
	}
	if db.Source.Data != "JVBERi0=" {
		t.Errorf("data = %q, want JVBERi0=", db.Source.Data)
	}
}

// TestParseOpenAIFileBlock_DataURIJSONPayload verifies a non-text media type with
// no base64 token is parsed correctly.
func TestParseOpenAIFileBlock_DataURIJSONPayload(t *testing.T) {
	db := parseOpenAIFileBlock(map[string]any{
		"file_data": "data:application/json,{\"k\":1}",
	})
	if db == nil {
		t.Fatal("returned nil for json data: URI")
	}
	if db.Source.Type != "text" {
		t.Errorf("source type = %q, want text", db.Source.Type)
	}
	if db.Source.MediaType != "application/json" {
		t.Errorf("media type = %q, want application/json", db.Source.MediaType)
	}
	if db.Source.Data != `{"k":1}` {
		t.Errorf("data = %q, want {\"k\":1}", db.Source.Data)
	}
}

func nilStr(s *DocumentSource) string {
	if s == nil {
		return "<nil>"
	}
	return s.Type
}

// ─── buildGeminiContents: input_audio ───

// TestSerializeGemini_InputAudioNotDropped verifies that an OpenAI input_audio
// block routed to a Gemini upstream is emitted as inlineData, not silently
// dropped. Previously buildGeminiContents had no case for "input_audio".
func TestSerializeGemini_InputAudioNotDropped(t *testing.T) {
	req := &InternalRequest{
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "input_audio", InputAudio: &InputAudioBlock{Data: "UklGRiQ=", Format: "wav"}},
					{Type: "text", Text: "transcribe"},
				},
			},
		},
	}
	out, err := SerializeGemini(req)
	if err != nil {
		t.Fatalf("SerializeGemini: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	contents := parsed["contents"].([]any)
	parts := contents[0].(map[string]any)["parts"].([]any)

	// Expect exactly two parts: inlineData (audio) + text. If input_audio were
	// dropped, only the text part would remain.
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts (audio + text), got %d", len(parts))
	}

	inline, ok := parts[0].(map[string]any)["inlineData"].(map[string]any)
	if !ok {
		t.Fatalf("first part is not inlineData: %v", parts[0])
	}
	if mime := inline["mimeType"].(string); mime != "audio/wav" {
		t.Errorf("mimeType = %q, want audio/wav", mime)
	}
	if data := inline["data"].(string); data != "UklGRiQ=" {
		t.Errorf("data = %q, want UklGRiQ=", data)
	}
}

// ─── serializeOpenAIAudioBlock: type input_audio ───

// TestSerializeOpenAI_AudioBlockType verifies that a Gemini-origin audio block
// (Type=="audio") routed to an OpenAI Chat upstream emits the valid
// {"type":"input_audio",...} structure, not the invalid {"type":"audio"}.
func TestSerializeOpenAI_AudioBlockType(t *testing.T) {
	req := &InternalRequest{
		Model: "gpt-4o",
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "audio", Audio: &MediaSource{Type: "base64", Data: "UklGRiQ=", Format: "wav"}},
					{Type: "text", Text: "describe"},
				},
			},
		},
	}
	out, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}

	// The emitted block must use "type":"input_audio", never "type":"audio".
	if strings.Contains(string(out), `"type":"audio"`) {
		t.Errorf("output contains invalid \"type\":\"audio\" block; want input_audio.\nout: %s", out)
	}
	if !strings.Contains(string(out), `"type":"input_audio"`) {
		t.Errorf("output missing \"type\":\"input_audio\" block.\nout: %s", out)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs := parsed["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	audioBlock, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("first content entry not an object: %v", content[0])
	}
	if audioBlock["type"] != "input_audio" {
		t.Errorf("audio block type = %v, want input_audio", audioBlock["type"])
	}
}

// ─── irDocumentToGeminiPart: text document ───

// TestSerializeGemini_TextDocumentInline verifies a text-type document is
// emitted as inlineData (base64-encoded, per Gemini's inlineData contract),
// not stuffed into fileData.fileUri (which produced a malformed request) and
// not emitted as raw text (which the Gemini parser would misread as base64).
func TestSerializeGemini_TextDocumentInline(t *testing.T) {
	const body = "the quick brown fox"
	part := irDocumentToGeminiPart(&DocumentBlock{
		MIMEType: "text/plain",
		Source:   &DocumentSource{Type: "text", Data: body},
	})
	if part == nil {
		t.Fatal("returned nil for text document")
	}
	inline, ok := part["inlineData"].(map[string]any)
	if !ok {
		t.Fatalf("expected inlineData, got %v", part)
	}
	if inline["mimeType"] != "text/plain" {
		t.Errorf("mimeType = %v, want text/plain", inline["mimeType"])
	}
	// inlineData.data must be base64 of the UTF-8 body, not raw text.
	enc, _ := inline["data"].(string)
	if enc == body {
		t.Errorf("inline data is raw text; must be base64-encoded")
	}
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("inline data is not valid base64 (%v): %q", err, enc)
	}
	if string(dec) != body {
		t.Errorf("decoded inline data = %q, want %q", dec, body)
	}
	if _, hasFileData := part["fileData"]; hasFileData {
		t.Errorf("text document must not produce fileData; got %v", part["fileData"])
	}
}

// TestSerializeGemini_CSVDocumentInline verifies csv documents resolve mimeType
// and that the payload is base64-encoded.
func TestSerializeGemini_CSVDocumentInline(t *testing.T) {
	const body = "a,b,c"
	part := irDocumentToGeminiPart(&DocumentBlock{
		Source: &DocumentSource{Type: "csv", Data: body},
	})
	if part == nil {
		t.Fatal("returned nil for csv document")
	}
	inline := part["inlineData"].(map[string]any)
	if inline["mimeType"] != "text/csv" {
		t.Errorf("mimeType = %v, want text/csv", inline["mimeType"])
	}
	enc, _ := inline["data"].(string)
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("csv inline data not valid base64 (%v): %q", err, enc)
	}
	if string(dec) != body {
		t.Errorf("decoded csv inline data = %q, want %q", dec, body)
	}
}

// ─── Gemini media converters: empty file ref guard ───

// TestGeminiMediaConverters_EmptyFileRefSkipped verifies that file_id-only media
// (no resolvable FileURI/URL) returns nil instead of emitting an empty fileUri,
// which would produce a malformed request.
func TestGeminiMediaConverters_EmptyFileRefSkipped(t *testing.T) {
	img := irImageToGeminiPart(&ImageSource{Type: "file_id", FileID: "file-abc"})
	if img != nil {
		t.Errorf("irImageToGeminiPart(file_id-only) = %v, want nil", img)
	}
	audio := irAudioToGeminiPart(&MediaSource{Kind: "audio", Type: "file_id", FileID: "file-abc"})
	if audio != nil {
		t.Errorf("irAudioToGeminiPart(file_id-only) = %v, want nil", audio)
	}
	video := irVideoToGeminiPart(&MediaSource{Kind: "video", Type: "file_id", FileID: "file-abc"})
	if video != nil {
		t.Errorf("irVideoToGeminiPart(file_id-only) = %v, want nil", video)
	}
}

// TestGeminiMediaConverters_ResolvableURIStillWorks verifies a file_uri image
// still emits a fileData part (no regression from the empty-ref guard).
func TestGeminiMediaConverters_ResolvableURIStillWorks(t *testing.T) {
	img := irImageToGeminiPart(&ImageSource{
		Type: "file_uri", MediaType: "image/png", FileURI: "gs://bucket/f.png",
	})
	if img == nil {
		t.Fatal("irImageToGeminiPart(file_uri) returned nil; expected fileData")
	}
	fd, ok := img["fileData"].(map[string]any)
	if !ok {
		t.Fatalf("expected fileData, got %v", img)
	}
	if fd["fileUri"] != "gs://bucket/f.png" {
		t.Errorf("fileUri = %v, want gs://bucket/f.png", fd["fileUri"])
	}
}
