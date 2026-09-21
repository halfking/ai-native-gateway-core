package ir

import (
	"encoding/json"
	"testing"
)

func TestGeminiSafetySettingsAndCachedContentRoundTrip(t *testing.T) {
	input := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"hello"}]}],
		"safetySettings":[
			{"category":"HARM_CATEGORY_HARASSMENT","threshold":"BLOCK_ONLY_HIGH"},
			{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_MEDIUM_AND_ABOVE","method":"SEVERITY"}
		],
		"cachedContent":"cachedContents/example-123"
	}`)

	req, err := ParseGemini(input)
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	if len(req.SafetySettings) != 2 {
		t.Fatalf("SafetySettings length = %d, want 2", len(req.SafetySettings))
	}
	if req.SafetySettings[1].Method != "SEVERITY" {
		t.Fatalf("SafetySettings[1].Method = %q, want SEVERITY", req.SafetySettings[1].Method)
	}
	if req.CachedContent != "cachedContents/example-123" {
		t.Fatalf("CachedContent = %q, want cachedContents/example-123", req.CachedContent)
	}

	encoded, err := SerializeGemini(req)
	if err != nil {
		t.Fatalf("SerializeGemini: %v", err)
	}

	var wire struct {
		SafetySettings []SafetySetting `json:"safetySettings"`
		CachedContent  string          `json:"cachedContent"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("unmarshal serialized Gemini request: %v", err)
	}
	if len(wire.SafetySettings) != len(req.SafetySettings) {
		t.Fatalf("serialized SafetySettings length = %d, want %d", len(wire.SafetySettings), len(req.SafetySettings))
	}
	if wire.SafetySettings[0] != req.SafetySettings[0] || wire.SafetySettings[1] != req.SafetySettings[1] {
		t.Fatalf("serialized SafetySettings = %#v, want %#v", wire.SafetySettings, req.SafetySettings)
	}
	if wire.CachedContent != req.CachedContent {
		t.Fatalf("serialized CachedContent = %q, want %q", wire.CachedContent, req.CachedContent)
	}
}
