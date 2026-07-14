package attachments

import (
	"testing"
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

func TestExtractFromGeminiBody_MixedInlineData(t *testing.T) {
	tmp := t.TempDir()
	storage, err := NewStorage(tmp)
	if err != nil {
		t.Fatal(err)
	}
	extractor := NewExtractor(storage)
	body := []byte(`{"contents":[{"role":"user","parts":[
		{"inlineData":{"mimeType":"image/png","data":"aW1n"}},
		{"inlineData":{"mimeType":"audio/mpeg","data":"YXVkaW8="}},
		{"inlineData":{"mimeType":"video/mp4","data":"dmlkZW8="}},
		{"inlineData":{"mimeType":"application/pdf","data":"cGRm"}},
		{"fileData":{"mimeType":"text/plain","fileUri":"files/text-1"}}
	]}]}`)

	result := extractor.ExtractFromGeminiBody("req-gemini", body)
	if result.TotalFound != 4 || result.Saved != 4 || result.Failed != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	wantTypes := []string{"image", "audio", "video", "file"}
	for i, want := range wantTypes {
		if result.Attachments[i].Type != want {
			t.Errorf("attachment %d type = %q, want %q", i, result.Attachments[i].Type, want)
		}
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
