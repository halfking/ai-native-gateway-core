package ir

import "testing"

func TestDetectRequestCapabilitiesResponsesInputAudio(t *testing.T) {
	caps := DetectRequestCapabilities([]byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAA","format":"wav"}}]}]}`))
	if !caps.HasAudio || caps.PrimaryModality() != "audio" {
		t.Fatalf("capabilities=%+v, want audio", caps)
	}
}

func TestDetectRequestCapabilitiesMixedResponsesMedia(t *testing.T) {
	caps := DetectRequestCapabilities([]byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.test/a.png"},{"type":"input_file","file":{"filename":"a.pdf","mime_type":"application/pdf"}}]}]}`))
	if !caps.HasImage || !caps.HasDocument || caps.PrimaryModality() != "vision" {
		t.Fatalf("capabilities=%+v, want image+document with vision primary", caps)
	}
}

func TestDetectRequestCapabilitiesGeminiInlineVideo(t *testing.T) {
	caps := DetectRequestCapabilities([]byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"video/mp4","data":"AAA"}}]}]}`))
	if !caps.HasVideo || caps.PrimaryModality() != "video" {
		t.Fatalf("capabilities=%+v, want video", caps)
	}
}

func TestDetectRequestCapabilitiesNestedToolResult(t *testing.T) {
	caps := DetectRequestCapabilities([]byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","content":[{"type":"image","source":{"type":"url","url":"https://example.test/a.png"}}]}]}]}`))
	if !caps.HasImage || caps.PrimaryModality() != "vision" {
		t.Fatalf("capabilities=%+v, want nested image", caps)
	}
}

func TestDetectRequestCapabilitiesPlainText(t *testing.T) {
	caps := DetectRequestCapabilities([]byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	if caps.PrimaryModality() != "text" || caps.HasImage || caps.HasAudio || caps.HasVideo {
		t.Fatalf("capabilities=%+v, want text-only", caps)
	}
}
