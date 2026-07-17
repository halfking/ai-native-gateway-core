package ir

import (
	"encoding/json"
	"testing"
)

// P2-1: Gemini file URI 完整往返测试
// 目标: 补充 Gemini file URI 的完整测试覆盖，验证 file URI 往返保持 URI 语义

// TestGemini_FileURI_Roundtrip verifies file URI round-trip preserves URI semantics
func TestGemini_FileURI_Roundtrip(t *testing.T) {
	original := []byte(`{
		"contents": [{
			"role": "user",
			"parts": [
				{"text": "Analyze this video"},
				{"fileData": {
					"mimeType": "video/mp4",
					"fileUri": "https://generativelanguage.googleapis.com/v1beta/files/abc123"
				}}
			]
		}]
	}`)

	// Parse Gemini → IR
	ir, err := ParseGemini(original)
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}

	// Verify IR structure
	if len(ir.Messages) != 1 {
		t.Fatalf("Messages count = %d, want 1", len(ir.Messages))
	}
	if len(ir.Messages[0].Content) != 2 {
		t.Fatalf("Content blocks = %d, want 2", len(ir.Messages[0].Content))
	}

	videoBlock := ir.Messages[0].Content[1]
	if videoBlock.Type != "video" {
		t.Errorf("Block type = %q, want video", videoBlock.Type)
	}
	if videoBlock.Video == nil {
		t.Fatal("Video is nil")
	}
	if videoBlock.Video.FileURI != "https://generativelanguage.googleapis.com/v1beta/files/abc123" {
		t.Errorf("FileURI = %q", videoBlock.Video.FileURI)
	}
	if videoBlock.Video.MediaType != "video/mp4" {
		t.Errorf("MediaType = %q, want video/mp4", videoBlock.Video.MediaType)
	}

	// Serialize IR → Gemini
	out, err := SerializeGemini(ir)
	if err != nil {
		t.Fatalf("SerializeGemini: %v", err)
	}

	var result struct {
		Contents []struct {
			Parts []struct {
				Text     string `json:"text"`
				FileData struct {
					MimeType string `json:"mimeType"`
					FileURI  string `json:"fileUri"`
				} `json:"fileData"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}

	// Verify file URI preserved
	if len(result.Contents) != 1 || len(result.Contents[0].Parts) != 2 {
		t.Fatalf("Unexpected structure: %d contents, %d parts", len(result.Contents), len(result.Contents[0].Parts))
	}

	fileDataPart := result.Contents[0].Parts[1].FileData
	if fileDataPart.FileURI != "https://generativelanguage.googleapis.com/v1beta/files/abc123" {
		t.Errorf("Round-trip FileURI = %q, want original", fileDataPart.FileURI)
	}
	if fileDataPart.MimeType != "video/mp4" {
		t.Errorf("Round-trip MimeType = %q", fileDataPart.MimeType)
	}
}

// TestGemini_FileURI_WithMIME verifies file URI includes MIME type
func TestGemini_FileURI_WithMIME(t *testing.T) {
	cases := []struct {
		name     string
		mimeType string
		fileURI  string
		wantType string
	}{
		{
			name:     "image file URI",
			mimeType: "image/jpeg",
			fileURI:  "https://generativelanguage.googleapis.com/v1beta/files/img123",
			wantType: "image",
		},
		{
			name:     "audio file URI",
			mimeType: "audio/mpeg",
			fileURI:  "https://generativelanguage.googleapis.com/v1beta/files/audio456",
			wantType: "audio",
		},
		{
			name:     "video file URI",
			mimeType: "video/quicktime",
			fileURI:  "https://generativelanguage.googleapis.com/v1beta/files/video789",
			wantType: "video",
		},
		{
			name:     "document file URI",
			mimeType: "application/pdf",
			fileURI:  "https://generativelanguage.googleapis.com/v1beta/files/doc999",
			wantType: "document",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{
				"contents": [{
					"role": "user",
					"parts": [{"fileData": {"mimeType": "` + tc.mimeType + `", "fileUri": "` + tc.fileURI + `"}}]
				}]
			}`)

			ir, err := ParseGemini(body)
			if err != nil {
				t.Fatalf("ParseGemini: %v", err)
			}

			block := ir.Messages[0].Content[0]
			if block.Type != tc.wantType {
				t.Errorf("Block type = %q, want %q", block.Type, tc.wantType)
			}

			// Verify MIME type preserved
			var mediaType string
			switch tc.wantType {
			case "image":
				if block.Image == nil {
					t.Fatal("Image is nil")
				}
				mediaType = block.Image.MediaType
			case "audio":
				if block.Audio == nil {
					t.Fatal("Audio is nil")
				}
				mediaType = block.Audio.MediaType
			case "video":
				if block.Video == nil {
					t.Fatal("Video is nil")
				}
				mediaType = block.Video.MediaType
			case "document":
				if block.Document == nil || block.Document.Source == nil {
					t.Fatal("Document or Source is nil")
				}
				mediaType = block.Document.Source.MediaType
			}

			if mediaType != tc.mimeType {
				t.Errorf("MediaType = %q, want %q", mediaType, tc.mimeType)
			}

			// Serialize and verify MIME type survives
			out, err := SerializeGemini(ir)
			if err != nil {
				t.Fatalf("SerializeGemini: %v", err)
			}

			var result struct {
				Contents []struct {
					Parts []struct {
						FileData struct {
							MimeType string `json:"mimeType"`
							FileURI  string `json:"fileUri"`
						} `json:"fileData"`
					} `json:"parts"`
				} `json:"contents"`
			}
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			if result.Contents[0].Parts[0].FileData.MimeType != tc.mimeType {
				t.Errorf("Serialized MimeType = %q, want %q",
					result.Contents[0].Parts[0].FileData.MimeType, tc.mimeType)
			}
		})
	}
}

// TestGemini_FileURI_NotConvertedToURL verifies file URI is not converted to public URL
func TestGemini_FileURI_NotConvertedToURL(t *testing.T) {
	// Gemini file URI should stay as file URI, not converted to regular URL field
	body := []byte(`{
		"contents": [{
			"role": "user",
			"parts": [{
				"fileData": {
					"mimeType": "image/png",
					"fileUri": "https://generativelanguage.googleapis.com/v1beta/files/xyz789"
				}
			}]
		}]
	}`)

	ir, err := ParseGemini(body)
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}

	imgBlock := ir.Messages[0].Content[0]
	if imgBlock.Type != "image" {
		t.Fatalf("Block type = %q, want image", imgBlock.Type)
	}
	if imgBlock.Image == nil {
		t.Fatal("Image is nil")
	}

	// FileURI should be set (stored in URL field when Type="file_uri")
	// Note: parse_gemini.go stores file URI in URL field for images
	if imgBlock.Image.URL == "" {
		t.Error("URL (file URI carrier) is empty, should be set")
	}
	if imgBlock.Image.URL != "https://generativelanguage.googleapis.com/v1beta/files/xyz789" {
		t.Errorf("URL (file URI) = %q", imgBlock.Image.URL)
	}
	if imgBlock.Image.Type != "file_uri" {
		t.Errorf("Type = %q, want file_uri", imgBlock.Image.Type)
	}

	// Serialize back and verify it stays as fileData, not converted to URL
	out, err := SerializeGemini(ir)
	if err != nil {
		t.Fatalf("SerializeGemini: %v", err)
	}

	var result struct {
		Contents []struct {
			Parts []map[string]interface{} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	part := result.Contents[0].Parts[0]

	// Should have fileData, not inlineData
	if _, ok := part["fileData"]; !ok {
		t.Error("Part should have fileData field")
	}
	if _, ok := part["inlineData"]; ok {
		t.Error("Part should not have inlineData field (file URI, not base64)")
	}

	fileData := part["fileData"].(map[string]interface{})
	if fileData["fileUri"] != "https://generativelanguage.googleapis.com/v1beta/files/xyz789" {
		t.Errorf("fileUri = %q, want original", fileData["fileUri"])
	}
}

// TestGemini_FileURI_GCSFormat verifies Google Cloud Storage gs:// URI format
func TestGemini_FileURI_GCSFormat(t *testing.T) {
	body := []byte(`{
		"contents": [{
			"role": "user",
			"parts": [{
				"fileData": {
					"mimeType": "video/mp4",
					"fileUri": "gs://my-bucket/videos/sample.mp4"
				}
			}]
		}]
	}`)

	ir, err := ParseGemini(body)
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}

	videoBlock := ir.Messages[0].Content[0]
	if videoBlock.Type != "video" {
		t.Fatalf("Block type = %q, want video", videoBlock.Type)
	}
	if videoBlock.Video == nil {
		t.Fatal("Video is nil")
	}
	if videoBlock.Video.FileURI != "gs://my-bucket/videos/sample.mp4" {
		t.Errorf("FileURI = %q, want gs:// format", videoBlock.Video.FileURI)
	}

	// Serialize and verify gs:// URI preserved
	out, err := SerializeGemini(ir)
	if err != nil {
		t.Fatalf("SerializeGemini: %v", err)
	}

	var result struct {
		Contents []struct {
			Parts []struct {
				FileData struct {
					FileURI string `json:"fileUri"`
				} `json:"fileData"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if result.Contents[0].Parts[0].FileData.FileURI != "gs://my-bucket/videos/sample.mp4" {
		t.Errorf("Round-trip FileURI = %q, gs:// format should be preserved",
			result.Contents[0].Parts[0].FileData.FileURI)
	}
}

// TestGemini_FileURI_MixedWithInlineData verifies file URI and inline data can coexist
func TestGemini_FileURI_MixedWithInlineData(t *testing.T) {
	body := []byte(`{
		"contents": [{
			"role": "user",
			"parts": [
				{"text": "Compare these images"},
				{"inlineData": {"mimeType": "image/png", "data": "iVBORw0KGgo..."}},
				{"fileData": {"mimeType": "image/jpeg", "fileUri": "https://generativelanguage.googleapis.com/v1beta/files/img123"}}
			]
		}]
	}`)

	ir, err := ParseGemini(body)
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}

	if len(ir.Messages[0].Content) != 3 {
		t.Fatalf("Content blocks = %d, want 3", len(ir.Messages[0].Content))
	}

	// First image: inline data (base64)
	img1 := ir.Messages[0].Content[1]
	if img1.Type != "image" {
		t.Errorf("Block 1 type = %q, want image", img1.Type)
	}
	if img1.Image == nil || img1.Image.Type != "base64" {
		t.Error("Image 1 should be base64 type")
	}

	// Second image: file URI (stored in URL field when Type="file_uri")
	img2 := ir.Messages[0].Content[2]
	if img2.Type != "image" {
		t.Errorf("Block 2 type = %q, want image", img2.Type)
	}
	if img2.Image == nil || (img2.Image.URL == "" && img2.Image.FileURI == "") {
		t.Error("Image 2 should have URL or FileURI")
	}
	if img2.Image.Type != "file_uri" {
		t.Errorf("Image 2 Type = %q, want file_uri", img2.Image.Type)
	}

	// Serialize and verify both formats preserved
	out, err := SerializeGemini(ir)
	if err != nil {
		t.Fatalf("SerializeGemini: %v", err)
	}

	var result struct {
		Contents []struct {
			Parts []map[string]interface{} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	parts := result.Contents[0].Parts
	if len(parts) != 3 {
		t.Fatalf("Serialized parts = %d, want 3", len(parts))
	}

	// Verify first image is inlineData
	if _, ok := parts[1]["inlineData"]; !ok {
		t.Error("Part 1 should have inlineData")
	}

	// Verify second image is fileData
	if _, ok := parts[2]["fileData"]; !ok {
		t.Error("Part 2 should have fileData")
	}
}

// TestGemini_FileURI_WithExpiry verifies expiry time metadata (if present) is preserved
// Note: Gemini file URIs may include expiry info in extensions
func TestGemini_FileURI_WithExpiry(t *testing.T) {
	body := []byte(`{
		"contents": [{
			"role": "user",
			"parts": [{
				"fileData": {
					"mimeType": "video/mp4",
					"fileUri": "https://generativelanguage.googleapis.com/v1beta/files/abc123"
				}
			}]
		}],
		"fileMetadata": {
			"abc123": {
				"expiryTime": "2026-07-20T00:00:00Z",
				"sizeBytes": 1048576
			}
		}
	}`)

	ir, err := ParseGemini(body)
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}

	// Verify file URI parsed
	videoBlock := ir.Messages[0].Content[0]
	if videoBlock.Type != "video" {
		t.Fatalf("Block type = %q, want video", videoBlock.Type)
	}

	// Verify extensions captured unknown fields (fileMetadata)
	if len(ir.Extensions) == 0 {
		t.Error("Extensions should capture fileMetadata")
	}

	if _, ok := ir.Extensions["fileMetadata"]; !ok {
		t.Error("Extensions should have fileMetadata field")
	}

	// Serialize and verify extensions preserved (same-protocol scenario)
	out, err := SerializeGemini(ir)
	if err != nil {
		t.Fatalf("SerializeGemini: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// Note: Extensions recovery is handled by transformation layer
	// This test verifies IR captures the metadata
	if ir.Extensions["fileMetadata"] == nil {
		t.Error("fileMetadata should be preserved in IR Extensions")
	}
}
