package ir

import (
	"encoding/json"
	"testing"
)

// Regression for 2026-09-05 audit A-#3: presencePenalty / frequencyPenalty were
// declared on GenerationConfig and InternalRequest but never parsed or
// serialized, so they vanished on the Gemini round trip.
func TestGeminiGenerationConfigPenaltiesRoundTrip(t *testing.T) {
	raw := []byte(`{
		"contents": [{"role": "user", "parts": [{"text": "hi"}]}],
		"generationConfig": {
			"temperature": 0.5,
			"presencePenalty": 0.25,
			"frequencyPenalty": -0.5
		}
	}`)

	req, err := ParseGemini(raw)
	if err != nil {
		t.Fatalf("ParseGemini failed: %v", err)
	}
	if req.PresencePenalty == nil || *req.PresencePenalty != 0.25 {
		t.Fatalf("presencePenalty not parsed into IR: %+v", req.PresencePenalty)
	}
	if req.FrequencyPenalty == nil || *req.FrequencyPenalty != -0.5 {
		t.Fatalf("frequencyPenalty not parsed into IR: %+v", req.FrequencyPenalty)
	}

	out, err := SerializeGemini(req)
	if err != nil {
		t.Fatalf("SerializeGemini failed: %v", err)
	}
	var wire struct {
		GenerationConfig struct {
			PresencePenalty  *float64 `json:"presencePenalty"`
			FrequencyPenalty *float64 `json:"frequencyPenalty"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("re-unmarshal serialized request: %v", err)
	}
	if wire.GenerationConfig.PresencePenalty == nil || *wire.GenerationConfig.PresencePenalty != 0.25 {
		t.Fatalf("presencePenalty lost on serialize: %+v", wire.GenerationConfig)
	}
	if wire.GenerationConfig.FrequencyPenalty == nil || *wire.GenerationConfig.FrequencyPenalty != -0.5 {
		t.Fatalf("frequencyPenalty lost on serialize: %+v", wire.GenerationConfig)
	}
}
