package attachments

import (
	"encoding/base64"
	"testing"
)

func TestExtractFromOpenAIAndGeminiMedia(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := NewExtractor(s)
	audio := base64.StdEncoding.EncodeToString([]byte("audio"))
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"` + audio + `","format":"wav"}}]}]}`)
	result := e.ExtractFromOpenAIBody("audio-request", body)
	if result.TotalFound != 1 || result.Saved != 1 || result.Attachments[0].Type != "audio" {
		t.Fatalf("audio result=%+v", result)
	}

	gemini := []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"video/mp4","data":"` + audio + `"}}]}]}`)
	result = e.extractBody("gemini-request", gemini, "gemini")
	if result.TotalFound != 1 || result.Saved != 1 || result.Attachments[0].Type != "video" {
		t.Fatalf("gemini result=%+v", result)
	}
}

func TestExtractFromResponsesFileAndAnthropicDocument(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := NewExtractor(s)
	data := base64.StdEncoding.EncodeToString([]byte("pdf"))
	responses := []byte(`{"input":[{"type":"input_file","input_file":{"file_data":"data:application/pdf;base64,` + data + `","mime_type":"application/pdf"}}]}`)
	result := e.extractBody("responses-request", responses, "responses")
	if result.TotalFound != 1 || result.Saved != 1 || result.Attachments[0].Type != "file" {
		t.Fatalf("responses result=%+v", result)
	}

	anthropic := []byte(`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + data + `"}}]}]}`)
	result = e.ExtractFromAnthropicBody("anthropic-document", anthropic)
	if result.TotalFound != 1 || result.Saved != 1 || result.Attachments[0].Type != "file" {
		t.Fatalf("anthropic result=%+v", result)
	}
}
