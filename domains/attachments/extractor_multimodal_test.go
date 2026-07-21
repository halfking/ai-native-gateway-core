package attachments

import (
	"encoding/base64"
	"testing"
)

func TestExtractFromOpenAIAndAnthropicImages(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := NewExtractor(s)
	image := base64.StdEncoding.EncodeToString([]byte("image"))
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + image + `"}}]}]}`)
	result := e.ExtractFromOpenAIBody("image-request", body)
	if result.TotalFound != 1 || result.Saved != 1 || result.Attachments[0].Type != "image" {
		t.Fatalf("openai result=%+v", result)
	}

	anthropic := []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + image + `"}}]}]}`)
	result = e.ExtractFromAnthropicBody("anthropic-image", anthropic)
	if result.TotalFound != 1 || result.Saved != 1 || result.Attachments[0].Type != "image" {
		t.Fatalf("anthropic result=%+v", result)
	}
}
