package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// Gemini 原生多模态 → OpenAI 线格式契约（2026-10-26，Gemini 多模态转发修复轮）。
//
// GeminiHandler 的真实出站路径是 ParseGemini → SerializeOpenAI（合成的
// /v1/chat/completions），而不是 SerializeGemini 的同协议往返
// （gemini_file_uri_test.go 已覆盖后者）。本轮修复前，fileData（Files API
// 引用形态，URI 载在 IR 的 FileURI 字段）在这条路径上产出**空载荷块**：
//
//	audio → {"type":"input_audio"}                       （无 input_audio 体，上游必 400）
//	video → {"type":"video_url","video_url":{"url":""}}  （空 url）
//
// 本文件把「Gemini 多模态 → OpenAI 线格式」钉成契约，防止回退。

// geminiToOpenAIContent is the shared harness: parse a Gemini body, serialize
// to the OpenAI Chat wire, and return the first message's content array.
func geminiToOpenAIContent(t *testing.T, body string) []map[string]any {
	t.Helper()
	irReq, err := ParseGemini([]byte(body))
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	out, err := SerializeOpenAI(irReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var m struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal OpenAI body: %v", err)
	}
	if len(m.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(m.Messages))
	}
	return m.Messages[0].Content
}

// blockFields decodes one wire block's nested payload object.
func blockFields(t *testing.T, block map[string]any, key string) map[string]any {
	t.Helper()
	inner, ok := block[key].(map[string]any)
	if !ok {
		bj, _ := json.Marshal(block)
		t.Fatalf("block %s payload missing/not object: %s", key, bj)
	}
	return inner
}

// TestGemini_FileData_ToOpenAI_NoEmptyPayloadBlocks pins the修复: every
// fileData part must arrive on the OpenAI wire carrying its URI reference —
// never as a bare/empty block an upstream would reject.
func TestGemini_FileData_ToOpenAI_NoEmptyPayloadBlocks(t *testing.T) {
	const fileBase = "https://generativelanguage.googleapis.com/v1beta/files/"
	content := geminiToOpenAIContent(t, `{
		"contents": [{
			"role": "user",
			"parts": [
				{"text": "describe these"},
				{"fileData": {"mimeType": "image/png", "fileUri": "`+fileBase+`img1"}},
				{"fileData": {"mimeType": "audio/wav", "fileUri": "`+fileBase+`aud1"}},
				{"fileData": {"mimeType": "video/mp4", "fileUri": "`+fileBase+`vid1"}},
				{"fileData": {"mimeType": "application/pdf", "fileUri": "`+fileBase+`doc1"}}
			]
		}]
	}`)

	if len(content) != 5 {
		bj, _ := json.Marshal(content)
		t.Fatalf("content blocks = %d, want 5: %s", len(content), bj)
	}

	// image: image_url carries the URI.
	img := blockFields(t, content[1], "image_url")
	if img["url"] != fileBase+"img1" {
		t.Errorf("image url = %v, want %s", img["url"], fileBase+"img1")
	}

	// audio: input_audio must exist AND carry the URI (was: bare block).
	aud := blockFields(t, content[2], "input_audio")
	if aud["url"] != fileBase+"aud1" {
		t.Errorf("audio url = %v, want %s", aud["url"], fileBase+"aud1")
	}

	// video: video_url.url must be the URI (was: "").
	vid := blockFields(t, content[3], "video_url")
	if vid["url"] != fileBase+"vid1" {
		t.Errorf("video url = %v, want %s", vid["url"], fileBase+"vid1")
	}

	// document: file.file_data carries the URI.
	doc := blockFields(t, content[4], "file")
	if doc["file_data"] != fileBase+"doc1" {
		t.Errorf("document file_data = %v, want %s", doc["file_data"], fileBase+"doc1")
	}
}

// TestGemini_InlineData_ToOpenAI_DataURIs pins the inline (base64) family:
// every inlineData part becomes a data-URI-carrying OpenAI block.
func TestGemini_InlineData_ToOpenAI_DataURIs(t *testing.T) {
	content := geminiToOpenAIContent(t, `{
		"contents": [{
			"role": "user",
			"parts": [
				{"inlineData": {"mimeType": "image/png", "data": "aW1n"}},
				{"inlineData": {"mimeType": "audio/mpeg", "data": "bXAz"}},
				{"inlineData": {"mimeType": "video/mp4", "data": "dmlk"}},
				{"inlineData": {"mimeType": "application/pdf", "data": "cGRm"}}
			]
		}]
	}`)

	if len(content) != 4 {
		bj, _ := json.Marshal(content)
		t.Fatalf("content blocks = %d, want 4: %s", len(content), bj)
	}

	img := blockFields(t, content[0], "image_url")
	if img["url"] != "data:image/png;base64,aW1n" {
		t.Errorf("image url = %v", img["url"])
	}

	// audio: input_audio.data carries the raw base64; the Gemini MIME
	// audio/mpeg must render in OpenAI's format enum word "mp3".
	aud := blockFields(t, content[1], "input_audio")
	if aud["data"] != "bXAz" {
		t.Errorf("audio data = %v", aud["data"])
	}
	if aud["format"] != "mp3" {
		t.Errorf("audio format = %v, want mp3 (OpenAI enum; Gemini emits audio/mpeg)", aud["format"])
	}

	vid := blockFields(t, content[2], "video_url")
	if vid["url"] != "data:video/mp4;base64,dmlk" {
		t.Errorf("video url = %v", vid["url"])
	}

	doc := blockFields(t, content[3], "file")
	if doc["file_data"] != "data:application/pdf;base64,cGRm" {
		t.Errorf("document file_data = %v", doc["file_data"])
	}
}

// TestGemini_FileData_ToResponses_NoDataLessAudio pins the Responses dialect
// (reachable when a Gemini-inbound request is routed to an openai-responses
// upstream): a URI-only audio has no Responses representation, so the
// serializer must omit it instead of emitting a data-less input_audio —
// and must never append a nil entry (which would marshal as content: null).
func TestGemini_FileData_ToResponses_NoDataLessAudio(t *testing.T) {
	irReq, err := ParseGemini([]byte(`{
		"contents": [{
			"role": "user",
			"parts": [
				{"text": "listen"},
				{"fileData": {"mimeType": "audio/wav", "fileUri": "https://example.com/a.wav"}}
			]
		}]
	}`))
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	out, err := SerializeResponsesRequest(irReq)
	if err != nil {
		t.Fatalf("SerializeResponses: %v", err)
	}
	var m struct {
		Input []struct {
			Content []json.RawMessage `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(m.Input) == 0 {
		t.Fatalf("no input items")
	}
	// Exactly one content block (the text) — the URI-only audio is omitted,
	// and no null element may appear.
	if len(m.Input[0].Content) != 1 {
		t.Fatalf("content blocks = %d, want 1 (text only): %s",
			len(m.Input[0].Content), string(m.Input[0].Content[0]))
	}
	var b map[string]any
	if err := json.Unmarshal(m.Input[0].Content[0], &b); err != nil {
		t.Fatalf("unmarshal block: %v", err)
	}
	if b["type"] != "input_text" {
		t.Errorf("block type = %v, want input_text", b["type"])
	}
}

// TestGemini_InlineDataAudio_ToResponses_KeepsData pins that the Responses
// path still emits base64 audio (the skip must only affect URI-only refs).
func TestGemini_InlineDataAudio_ToResponses_KeepsData(t *testing.T) {
	irReq, err := ParseGemini([]byte(`{
		"contents": [{
			"role": "user",
			"parts": [{"inlineData": {"mimeType": "audio/mpeg", "data": "bXAz"}}]
		}]
	}`))
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	out, err := SerializeResponsesRequest(irReq)
	if err != nil {
		t.Fatalf("SerializeResponses: %v", err)
	}
	if !strings.Contains(string(out), `"data":"bXAz"`) {
		t.Errorf("responses body lost inline audio data: %s", out)
	}
}

// TestGemini_FileDataAudio_SurvivesOpenAIRoundTrip pins the executor's
// mandatory Parse→Validate→Serialize cycle (finalizeOpenAIUpstreamBody): the
// URI-reference input_audio produced by SerializeOpenAI must still be on the
// wire after the body is parsed back to IR and serialized again. Before the
// parse_openai fix the url form parsed to a nil InputAudio and the audio
// reference vanished — the upstream received a request with no audio at all
// (reproduced live, 2026-10-26).
func TestGemini_FileDataAudio_SurvivesOpenAIRoundTrip(t *testing.T) {
	irReq, err := ParseGemini([]byte(`{
		"contents": [{
			"role": "user",
			"parts": [
				{"text": "transcribe"},
				{"fileData": {"mimeType": "audio/wav", "fileUri": "https://example.com/a.wav"}}
			]
		}]
	}`))
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	first, err := SerializeOpenAI(irReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI #1: %v", err)
	}
	if !strings.Contains(string(first), `"url":"https://example.com/a.wav"`) {
		t.Fatalf("first-hop wire lost the audio URI: %s", first)
	}

	// The executor cycle: ParseOpenAI → SerializeOpenAI again.
	second, err := ParseOpenAI(first)
	if err != nil {
		t.Fatalf("ParseOpenAI (re-parse): %v", err)
	}
	secondWire, err := SerializeOpenAI(second)
	if err != nil {
		t.Fatalf("SerializeOpenAI #2: %v", err)
	}
	if !strings.Contains(string(secondWire), `"url":"https://example.com/a.wav"`) {
		t.Fatalf("audio URI lost on the mandatory re-parse round trip: %s", secondWire)
	}
}

// TestGemini_FileDataAudio_SurvivesValidateAndFix pins the FULL executor
// path shape: the legacy IR lane runs Parse → ValidateAndFixRequest →
// Serialize (finalizeOpenAIUpstreamBody), not just Parse → Serialize. The
// Validate step must not strip the raw-preserved URI-reference audio block.
func TestGemini_FileDataAudio_SurvivesValidateAndFix(t *testing.T) {
	irReq, err := ParseGemini([]byte(`{
		"contents": [{
			"role": "user",
			"parts": [{"fileData": {"mimeType": "audio/wav", "fileUri": "https://example.com/a.wav"}}]
		}]
	}`))
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	first, err := SerializeOpenAI(irReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	second, err := ParseOpenAI(first)
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	fixed := ValidateAndFixRequest(second, "gemini-multimodal-audit-test")
	out, err := SerializeOpenAI(fixed)
	if err != nil {
		t.Fatalf("SerializeOpenAI after fix: %v", err)
	}
	if !strings.Contains(string(out), "https://example.com/a.wav") {
		t.Fatalf("audio URI lost through ValidateAndFixRequest (full executor path):\n%s", out)
	}
}

// TestSerializeOpenAI_NoEmptyPayloadOrNullBlocks pins the 2026-10-27 audit
// round: a block the wire cannot express must be OMITTED (with a loss
// report), never emitted as an empty-payload block or as a literal null
// element in content[]. Three degenerate carriers are covered: file_id-only
// video (video_url has no file_id slot on Chat Completions), a document
// whose Source is nil (previously serialized content:[null]), and an
// all-empty audio source.
func TestSerializeOpenAI_NoEmptyPayloadOrNullBlocks(t *testing.T) {
	req := &InternalRequest{
		Model:          "m",
		SourceProtocol: ProtocolGeminiGenerate,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{
				{Type: "text", Text: "hello"},
				{Type: "video", Video: &MediaSource{Kind: "video", Type: "file_id", FileID: "file-xyz", MediaType: "video/mp4"}},
				{Type: "document", Document: &DocumentBlock{MIMEType: "application/pdf", Title: "t.pdf"}},
				{Type: "audio", Audio: &MediaSource{Kind: "audio", Type: "url", MediaType: "audio/wav"}},
			}},
		},
	}
	out, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var m struct {
		Messages []struct {
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(m.Messages) != 1 || len(m.Messages[0].Content) == 0 {
		t.Fatalf("unexpected shape: %s", out)
	}
	for _, b := range m.Messages[0].Content {
		s := string(b)
		if s == "null" {
			t.Errorf("content[] contains a literal null element: %s", out)
		}
		if strings.Contains(s, `"url":""`) {
			t.Errorf("content[] contains an empty-url block: %s", s)
		}
		if strings.Contains(s, `"input_audio":{}`) {
			t.Errorf("content[] contains a data-less input_audio: %s", s)
		}
		if strings.Contains(s, `"video_url"`) {
			t.Errorf("file_id-only video must be dropped, not serialized: %s", s)
		}
	}
	// The surviving block is the text block only.
	if len(m.Messages[0].Content) != 1 {
		t.Errorf("expected only the text block to survive, got %d blocks: %s", len(m.Messages[0].Content), out)
	}
	if !strings.Contains(string(m.Messages[0].Content[0]), `"text"`) {
		t.Errorf("expected the text block to survive, got: %s", m.Messages[0].Content[0])
	}
}

// TestSerializeResponses_NoEmptyFileData pins the same empty-payload rule on
// the Responses dialect: a url-type document source whose URL and Data are
// both empty must not emit file_data:"" (nor a null content element).
func TestSerializeResponses_NoEmptyFileData(t *testing.T) {
	req := &InternalRequest{
		Model:          "m",
		SourceProtocol: ProtocolGeminiGenerate,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{
				{Type: "text", Text: "hello"},
				{Type: "document", Document: &DocumentBlock{
					MIMEType: "application/pdf",
					Source:   &DocumentSource{Type: "url", MediaType: "application/pdf"},
				}},
			}},
		},
	}
	out, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatalf("SerializeResponsesRequest: %v", err)
	}
	if strings.Contains(string(out), `"file_data":""`) {
		t.Errorf("responses body contains an empty file_data: %s", out)
	}
	if strings.Contains(string(out), "null,") || strings.HasSuffix(strings.TrimSpace(string(out)), "null") {
		t.Errorf("responses body contains a null content element: %s", out)
	}
	// The text block survives; the empty document is dropped.
	if !strings.Contains(string(out), `"hello"`) {
		t.Errorf("text block lost: %s", out)
	}
}
