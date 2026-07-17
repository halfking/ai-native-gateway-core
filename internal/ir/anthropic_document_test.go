package ir

import (
	"encoding/json"
	"testing"
)

// P2-2: Anthropic document block 完整往返测试
// 目标: 补充 Anthropic document block 的完整测试覆盖，验证 document 不被降级为 text

// TestAnthropic_DocumentBlock_Base64 verifies document source type=base64
func TestAnthropic_DocumentBlock_Base64(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "Analyze this document"},
				{
					"type": "document",
					"source": {
						"type": "base64",
						"media_type": "application/pdf",
						"data": "JVBERi0xLjQKJeLjz9MK..."
					}
				}
			]
		}]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("ParseAnthropic: %v", err)
	}

	if len(ir.Messages) != 1 {
		t.Fatalf("Messages count = %d, want 1", len(ir.Messages))
	}
	if len(ir.Messages[0].Content) != 2 {
		t.Fatalf("Content blocks = %d, want 2", len(ir.Messages[0].Content))
	}

	docBlock := ir.Messages[0].Content[1]
	if docBlock.Type != "document" {
		t.Errorf("Block type = %q, want document", docBlock.Type)
	}
	if docBlock.Document == nil {
		t.Fatal("Document is nil")
	}
	if docBlock.Document.Source == nil {
		t.Fatal("Document.Source is nil")
	}
	if docBlock.Document.Source.Type != "base64" {
		t.Errorf("Source.Type = %q, want base64", docBlock.Document.Source.Type)
	}
	if docBlock.Document.Source.MediaType != "application/pdf" {
		t.Errorf("Source.MediaType = %q, want application/pdf", docBlock.Document.Source.MediaType)
	}
	if docBlock.Document.Source.Data != "JVBERi0xLjQKJeLjz9MK..." {
		t.Errorf("Source.Data = %q", docBlock.Document.Source.Data)
	}
}

// TestAnthropic_DocumentBlock_URL verifies document source type=url
func TestAnthropic_DocumentBlock_URL(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [{
			"role": "user",
			"content": [
				{
					"type": "document",
					"source": {
						"type": "url",
						"url": "https://example.com/document.pdf"
					},
					"title": "Example Document"
				}
			]
		}]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("ParseAnthropic: %v", err)
	}

	docBlock := ir.Messages[0].Content[0]
	if docBlock.Type != "document" {
		t.Errorf("Block type = %q, want document", docBlock.Type)
	}
	if docBlock.Document == nil {
		t.Fatal("Document is nil")
	}
	if docBlock.Document.Source == nil {
		t.Fatal("Document.Source is nil")
	}
	if docBlock.Document.Source.Type != "url" {
		t.Errorf("Source.Type = %q, want url", docBlock.Document.Source.Type)
	}
	if docBlock.Document.Source.Data != "https://example.com/document.pdf" {
		t.Errorf("Source.Data (URL) = %q", docBlock.Document.Source.Data)
	}
	if docBlock.Document.Title != "Example Document" {
		t.Errorf("Title = %q, want Example Document", docBlock.Document.Title)
	}
}

// TestAnthropic_DocumentBlock_Roundtrip verifies document block round-trip preserves type
func TestAnthropic_DocumentBlock_Roundtrip(t *testing.T) {
	original := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "Review this PDF"},
				{
					"type": "document",
					"source": {
						"type": "base64",
						"media_type": "application/pdf",
						"data": "JVBERi0xLjQKJeLjz9MK..."
					},
					"title": "Contract.pdf"
				}
			]
		}]
	}`)

	// Parse Anthropic → IR
	ir, err := ParseAnthropic(original)
	if err != nil {
		t.Fatalf("ParseAnthropic: %v", err)
	}

	// Verify IR structure
	docBlock := ir.Messages[0].Content[1]
	if docBlock.Type != "document" {
		t.Fatalf("Block type after parse = %q, want document", docBlock.Type)
	}

	// Serialize IR → Anthropic
	out, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}

	var result struct {
		Messages []struct {
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text,omitempty"`
				Source *struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source,omitempty"`
				Title string `json:"title,omitempty"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}

	// Verify document block preserved
	if len(result.Messages[0].Content) != 2 {
		t.Fatalf("Content blocks after round-trip = %d, want 2", len(result.Messages[0].Content))
	}

	docContent := result.Messages[0].Content[1]
	if docContent.Type != "document" {
		t.Errorf("Round-trip type = %q, want document", docContent.Type)
	}
	if docContent.Source == nil {
		t.Fatal("Source is nil after round-trip")
	}
	if docContent.Source.Type != "base64" {
		t.Errorf("Round-trip source.type = %q, want base64", docContent.Source.Type)
	}
	if docContent.Source.MediaType != "application/pdf" {
		t.Errorf("Round-trip media_type = %q", docContent.Source.MediaType)
	}
	if docContent.Title != "Contract.pdf" {
		t.Errorf("Round-trip title = %q", docContent.Title)
	}
}

// TestAnthropic_DocumentBlock_NotDowngradedToText verifies document is not converted to text
func TestAnthropic_DocumentBlock_NotDowngradedToText(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [{
			"role": "user",
			"content": [
				{
					"type": "document",
					"source": {
						"type": "base64",
						"media_type": "text/plain",
						"data": "VGhpcyBpcyBhIHRleHQgZG9jdW1lbnQu"
					}
				}
			]
		}]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("ParseAnthropic: %v", err)
	}

	docBlock := ir.Messages[0].Content[0]
	// Even though media_type is text/plain, should remain document type
	if docBlock.Type != "document" {
		t.Errorf("Block type = %q, want document (should not downgrade to text)", docBlock.Type)
	}
	if docBlock.Document == nil {
		t.Fatal("Document is nil (was downgraded)")
	}

	// Serialize and verify it stays as document
	out, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}

	var result struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if result.Messages[0].Content[0].Type != "document" {
		t.Errorf("Serialized type = %q, want document (was downgraded to %q)",
			result.Messages[0].Content[0].Type, result.Messages[0].Content[0].Type)
	}
}

// TestAnthropic_DocumentBlock_WithCacheControl verifies document + cache_control
func TestAnthropic_DocumentBlock_WithCacheControl(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [{
			"role": "user",
			"content": [
				{
					"type": "document",
					"source": {
						"type": "base64",
						"media_type": "application/pdf",
						"data": "JVBERi0xLjQK..."
					},
					"cache_control": {"type": "ephemeral"}
				}
			]
		}]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("ParseAnthropic: %v", err)
	}

	docBlock := ir.Messages[0].Content[0]
	if docBlock.Type != "document" {
		t.Errorf("Block type = %q, want document", docBlock.Type)
	}
	if docBlock.CacheControl == nil {
		t.Fatal("CacheControl is nil")
	}
	if docBlock.CacheControl.Type != "ephemeral" {
		t.Errorf("CacheControl.Type = %q, want ephemeral", docBlock.CacheControl.Type)
	}

	// Serialize and verify cache_control preserved
	out, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}

	var result struct {
		Messages []struct {
			Content []struct {
				Type         string `json:"type"`
				CacheControl *struct {
					Type string `json:"type"`
				} `json:"cache_control,omitempty"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	docContent := result.Messages[0].Content[0]
	if docContent.CacheControl == nil {
		t.Fatal("Round-trip cache_control is nil")
	}
	if docContent.CacheControl.Type != "ephemeral" {
		t.Errorf("Round-trip cache_control.type = %q", docContent.CacheControl.Type)
	}
}

// TestAnthropic_DocumentBlock_MultipleSourceTypes verifies all source types
func TestAnthropic_DocumentBlock_MultipleSourceTypes(t *testing.T) {
	cases := []struct {
		name       string
		sourceType string
		mediaType  string
		payload    string
	}{
		{
			name:       "PDF base64",
			sourceType: "base64",
			mediaType:  "application/pdf",
			payload:    "JVBERi0xLjQK...",
		},
		{
			name:       "CSV base64",
			sourceType: "base64",
			mediaType:  "text/csv",
			payload:    "bmFtZSxhZ2UKam9obiwyNQ==",
		},
		{
			name:       "Text base64",
			sourceType: "base64",
			mediaType:  "text/plain",
			payload:    "VGhpcyBpcyB0ZXh0",
		},
		{
			name:       "URL source",
			sourceType: "url",
			mediaType:  "application/pdf",
			payload:    "https://example.com/doc.pdf",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			if tc.sourceType == "base64" {
				body = []byte(`{
					"model": "claude-sonnet-4-20250514",
					"max_tokens": 1024,
					"messages": [{
						"role": "user",
						"content": [{
							"type": "document",
							"source": {
								"type": "` + tc.sourceType + `",
								"media_type": "` + tc.mediaType + `",
								"data": "` + tc.payload + `"
							}
						}]
					}]
				}`)
			} else {
				body = []byte(`{
					"model": "claude-sonnet-4-20250514",
					"max_tokens": 1024,
					"messages": [{
						"role": "user",
						"content": [{
							"type": "document",
							"source": {
								"type": "` + tc.sourceType + `",
								"url": "` + tc.payload + `"
							}
						}]
					}]
				}`)
			}

			ir, err := ParseAnthropic(body)
			if err != nil {
				t.Fatalf("ParseAnthropic: %v", err)
			}

			docBlock := ir.Messages[0].Content[0]
			if docBlock.Type != "document" {
				t.Errorf("Block type = %q, want document", docBlock.Type)
			}
			if docBlock.Document == nil || docBlock.Document.Source == nil {
				t.Fatal("Document or Source is nil")
			}
			if docBlock.Document.Source.Type != tc.sourceType {
				t.Errorf("Source.Type = %q, want %q", docBlock.Document.Source.Type, tc.sourceType)
			}

			// Serialize and verify round-trip
			out, err := SerializeAnthropic(ir)
			if err != nil {
				t.Fatalf("SerializeAnthropic: %v", err)
			}

			var result struct {
				Messages []struct {
					Content []struct {
						Type   string `json:"type"`
						Source *struct {
							Type      string `json:"type"`
							MediaType string `json:"media_type,omitempty"`
							Data      string `json:"data,omitempty"`
							URL       string `json:"url,omitempty"`
						} `json:"source"`
					} `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			source := result.Messages[0].Content[0].Source
			if source == nil {
				t.Fatal("Round-trip source is nil")
			}
			if source.Type != tc.sourceType {
				t.Errorf("Round-trip source.type = %q, want %q", source.Type, tc.sourceType)
			}
		})
	}
}

// TestAnthropic_DocumentBlock_NotConfusedWithImage verifies document vs image distinction
func TestAnthropic_DocumentBlock_NotConfusedWithImage(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [{
			"role": "user",
			"content": [
				{
					"type": "image",
					"source": {
						"type": "base64",
						"media_type": "image/png",
						"data": "iVBORw0KGgo..."
					}
				},
				{
					"type": "document",
					"source": {
						"type": "base64",
						"media_type": "application/pdf",
						"data": "JVBERi0xLjQK..."
					}
				}
			]
		}]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("ParseAnthropic: %v", err)
	}

	if len(ir.Messages[0].Content) != 2 {
		t.Fatalf("Content blocks = %d, want 2", len(ir.Messages[0].Content))
	}

	// First should be image
	imgBlock := ir.Messages[0].Content[0]
	if imgBlock.Type != "image" {
		t.Errorf("Block 0 type = %q, want image", imgBlock.Type)
	}
	if imgBlock.Image == nil {
		t.Error("Block 0 Image is nil")
	}
	if imgBlock.Document != nil {
		t.Error("Block 0 should not have Document field")
	}

	// Second should be document
	docBlock := ir.Messages[0].Content[1]
	if docBlock.Type != "document" {
		t.Errorf("Block 1 type = %q, want document", docBlock.Type)
	}
	if docBlock.Document == nil {
		t.Error("Block 1 Document is nil")
	}
	if docBlock.Image != nil {
		t.Error("Block 1 should not have Image field")
	}
}
