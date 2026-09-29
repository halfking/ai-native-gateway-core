package attachments

import (
	"encoding/base64"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

func TestExtractFromOpenAIBody(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStorage(dir)
	e := NewExtractor(s)

	body := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "这是什么？"},
					{"type": "image_url", "image_url": {"url": "` + testPNGDataURI() + `"}}
				]
			}
		]
	}`)

	result := e.ExtractFromOpenAIBody("req-test-1", body)
	if result.TotalFound != 1 {
		t.Errorf("TotalFound = %d, want 1", result.TotalFound)
	}
	if result.Saved != 1 {
		t.Errorf("Saved = %d, want 1", result.Saved)
	}
	if len(result.Attachments) != 1 {
		t.Fatalf("len(Attachments) = %d, want 1", len(result.Attachments))
	}
	att := result.Attachments[0]
	if att.ContentType != "image/png" {
		t.Errorf("ContentType = %q", att.ContentType)
	}
	if att.MessageIndex != 0 || att.BlockIndex != 1 {
		t.Errorf("index = (%d,%d), want (0,1)", att.MessageIndex, att.BlockIndex)
	}
	if att.Status != AttachmentStatusManifestReady {
		t.Errorf("Status = %q, want %q", att.Status, AttachmentStatusManifestReady)
	}
}

func TestExtractFromOpenAIBody_HTTPURLNotExtracted(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStorage(dir)
	e := NewExtractor(s)

	body := []byte(`{
		"messages": [{
			"role": "user",
			"content": [
				{"type": "image_url", "image_url": {"url": "https://example.com/x.png"}}
			]
		}]
	}`)

	result := e.ExtractFromOpenAIBody("req", body)
	if result.TotalFound != 0 {
		t.Errorf("HTTP URL should not be extracted, TotalFound = %d", result.TotalFound)
	}
}

func TestExtractFromOpenAIBody_NoImage(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStorage(dir)
	e := NewExtractor(s)

	body := []byte(`{
		"messages": [{"role": "user", "content": "hello"}]
	}`)
	result := e.ExtractFromOpenAIBody("req", body)
	if result.TotalFound != 0 {
		t.Errorf("text-only message should have 0 attachments")
	}
}

func TestExtractFromAnthropicBody(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStorage(dir)
	e := NewExtractor(s)

	// Anthropic base64 图片格式
	pngB64 := testPNGDataURI()
	pngB64 = pngB64[len("data:image/png;base64,"):]
	body := []byte(`{
		"model": "claude-3-5-sonnet",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "` + pngB64 + `"}},
				{"type": "text", "text": "describe this"}
			]
		}]
	}`)

	result := e.ExtractFromAnthropicBody("req-anthropic-1", body)
	if result.TotalFound != 1 {
		t.Errorf("TotalFound = %d, want 1", result.TotalFound)
	}
	if result.Saved != 1 {
		t.Errorf("Saved = %d, want 1", result.Saved)
	}
	if result.Attachments[0].ContentType != "image/png" {
		t.Errorf("ContentType = %q", result.Attachments[0].ContentType)
	}
}

func TestExtractFromAnthropicBody_URLSourceSkipped(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStorage(dir)
	e := NewExtractor(s)

	body := []byte(`{
		"messages": [{
			"role": "user",
			"content": [
				{"type": "image", "source": {"type": "url", "url": "https://x.com/y.png"}}
			]
		}]
	}`)
	result := e.ExtractFromAnthropicBody("req", body)
	if result.TotalFound != 0 {
		t.Errorf("Anthropic url source should be skipped, got %d", result.TotalFound)
	}
}

func TestExtract_StorageFailureDoesNotPanic(t *testing.T) {
	// nil storage 不应 panic
	e := NewExtractor(nil)
	body := []byte(`{"messages":[{"role":"user","content":[
		{"type":"image_url","image_url":{"url":"` + testPNGDataURI() + `"}}]}]}`)
	result := e.ExtractFromOpenAIBody("req", body)
	if result.TotalFound != 1 {
		t.Errorf("TotalFound = %d, want 1 (found even if not saved)", result.TotalFound)
	}
	if result.Saved != 0 {
		t.Errorf("Saved = %d, want 0 (nil storage)", result.Saved)
	}
	if len(result.Attachments) != 1 || result.Attachments[0].Status != AttachmentStatusStoreFailed {
		t.Errorf("failed attachment status = %+v, want store_failed", result.Attachments)
	}
}

func TestCountOnly(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[
		{"type":"image_url","image_url":{"url":"` + testPNGDataURI() + `"}},
		{"type":"image_url","image_url":{"url":"` + testPNGDataURI() + `"}}
	]}]}`)
	if n := CountOnly(body, "openai-chat"); n != 2 {
		t.Errorf("CountOnly = %d, want 2", n)
	}
}

// ── N21-3（2026-09-29）：input_audio / video_url / file / anthropic document
// 此前不入附件列（session_bodies/attachments 对含音频轮次恒空）。

func TestExtractFromOpenAIBody_InputAudio(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStorage(dir)
	e := NewExtractor(s)

	audioB64 := base64.StdEncoding.EncodeToString([]byte("RIFFfake-wav-payload"))
	body := []byte(`{
		"messages": [{
			"role": "user",
			"content": [
				{"type": "input_audio", "input_audio": {"data": "` + audioB64 + `", "format": "wav"}},
				{"type": "input_audio", "input_audio": {"data": "` + audioB64 + `", "format": "mp3"}},
				{"type": "input_audio", "input_audio": {"data": "` + audioB64 + `"}}
			]
		}]
	}`)

	result := e.ExtractFromOpenAIBody("req-audio", body)
	if result.TotalFound != 3 || result.Saved != 3 {
		t.Fatalf("TotalFound/Saved = %d/%d, want 3/3", result.TotalFound, result.Saved)
	}
	wantCT := []string{"audio/wav", "audio/mpeg", "application/octet-stream"}
	for i, ct := range wantCT {
		if got := result.Attachments[i].ContentType; got != ct {
			t.Errorf("Attachments[%d].ContentType = %q, want %q", i, got, ct)
		}
		if got := result.Attachments[i].Type; got != "audio" {
			t.Errorf("Attachments[%d].Type = %q, want audio", i, got)
		}
	}
}

func TestExtractFromOpenAIBody_VideoAndFileBlocks(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStorage(dir)
	e := NewExtractor(s)

	pdfB64 := base64.StdEncoding.EncodeToString([]byte("%PDF-fake"))
	body := []byte(`{
		"messages": [{
			"role": "user",
			"content": [
				{"type": "video_url", "video_url": {"url": "data:video/mp4;base64,` + pdfB64 + `"}},
				{"type": "file", "file": {"file_data": "data:application/pdf;base64,` + pdfB64 + `", "filename": "doc.pdf"}},
				{"type": "file", "file": {"file_data": "https://example.com/remote.pdf"}},
				{"type": "video_url", "video_url": {"url": "https://example.com/remote.mp4"}}
			]
		}]
	}`)

	result := e.ExtractFromOpenAIBody("req-media", body)
	if result.TotalFound != 2 || result.Saved != 2 {
		t.Fatalf("TotalFound/Saved = %d/%d, want 2/2 (HTTP URLs must not count)", result.TotalFound, result.Saved)
	}
	if got := result.Attachments[0].Type; got != "video" {
		t.Errorf("Attachments[0].Type = %q, want video", got)
	}
	if got := result.Attachments[0].ContentType; got != "video/mp4" {
		t.Errorf("Attachments[0].ContentType = %q, want video/mp4", got)
	}
	if got := result.Attachments[1].Type; got != "file" {
		t.Errorf("Attachments[1].Type = %q, want file", got)
	}
	if got := result.Attachments[1].ContentType; got != "application/pdf" {
		t.Errorf("Attachments[1].ContentType = %q, want application/pdf", got)
	}
}

func TestExtractFromAnthropicBody_Document(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStorage(dir)
	e := NewExtractor(s)

	pdfB64 := base64.StdEncoding.EncodeToString([]byte("%PDF-fake"))
	body := []byte(`{
		"messages": [{
			"role": "user",
			"content": [
				{"type": "document", "source": {"type": "base64", "media_type": "application/pdf", "data": "` + pdfB64 + `"}}
			]
		}]
	}`)

	result := e.ExtractFromAnthropicBody("req-doc", body)
	if result.TotalFound != 1 || result.Saved != 1 {
		t.Fatalf("TotalFound/Saved = %d/%d, want 1/1", result.TotalFound, result.Saved)
	}
	if got := result.Attachments[0].Type; got != "file" {
		t.Errorf("Type = %q, want file", got)
	}
	if got := result.Attachments[0].ContentType; got != "application/pdf" {
		t.Errorf("ContentType = %q, want application/pdf", got)
	}
}

// TestGeminiInlineMediaExtractionViaConversion 实证 gemini 入站腿：inlineData
// → ParseGemini → SerializeOpenAI 合成体（image_url 重建 data URI / 音频落
// input_audio）→ ExtractFromOpenAIBody 全部入列。二十一轮 N21-3 的
// 「gemini 轮次附件列恒空」对图片不成立（既有提取在合成体上生效），音频/
// 视频腿由本次 input_audio/video_url 扩展闭合。
func TestGeminiInlineMediaExtractionViaConversion(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStorage(dir)
	e := NewExtractor(s)

	pngB64 := testPNGDataURI()
	pngB64 = pngB64[len("data:image/png;base64,"):]
	wavB64 := base64.StdEncoding.EncodeToString([]byte("RIFFfake-wav-payload"))
	geminiBody := []byte(`{
		"contents": [{
			"role": "user",
			"parts": [
				{"text": "看图听音频"},
				{"inlineData": {"mimeType": "image/png", "data": "` + pngB64 + `"}},
				{"inlineData": {"mimeType": "audio/wav", "data": "` + wavB64 + `"}}
			]
		}]
	}`)

	irReq, err := ir.ParseGemini(geminiBody)
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	openaiBody, err := ir.SerializeOpenAI(irReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}

	result := e.ExtractFromOpenAIBody("req-gemini", openaiBody)
	if result.TotalFound != 2 || result.Saved != 2 {
		t.Fatalf("TotalFound/Saved = %d/%d, want 2/2", result.TotalFound, result.Saved)
	}
	if got := result.Attachments[0].Type; got != "image" {
		t.Errorf("image leg Type = %q, want image", got)
	}
	if got := result.Attachments[1].Type; got != "audio" {
		t.Errorf("audio leg Type = %q, want audio", got)
	}
	if got := result.Attachments[1].ContentType; got != "audio/wav" {
		t.Errorf("audio leg ContentType = %q, want audio/wav", got)
	}
}
