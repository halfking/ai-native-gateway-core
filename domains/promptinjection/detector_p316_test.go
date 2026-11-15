package promptinjection

import (
	"testing"
)

// TestP316_ParseLLMResponseMultipleJSON tests parsing with multiple JSON objects (P3-16).
func TestP316_ParseLLMResponseMultipleJSON(t *testing.T) {
	engine := &LLMDetectionEngine{}

	// Scenario 1: LLM returns preamble + valid JSON
	response1 := `Here's my analysis:
{
  "is_injection": true,
  "confidence": 0.95,
  "categories": ["jailbreak", "override"],
  "reason": "Attempts to override system instructions",
  "evidence": "Ignore previous instructions"
}
Some trailing text.`

	result, err := engine.parseLLMResponse(response1, 0.5)
	if err != nil {
		t.Errorf("P3-16: Failed to parse response with preamble: %v", err)
	}
	if !result.IsInjection {
		t.Error("P3-16: Expected is_injection=true")
	}
	if result.Confidence != 0.95 {
		t.Errorf("P3-16: Expected confidence=0.95, got %f", result.Confidence)
	}
}

// TestP316_ParseLLMResponseNestedJSON tests parsing with nested JSON structures (P3-16).
func TestP316_ParseLLMResponseNestedJSON(t *testing.T) {
	engine := &LLMDetectionEngine{}

	// Scenario 2: JSON with nested objects
	response2 := `{
  "is_injection": false,
  "confidence": 0.30,
  "categories": [],
  "reason": "Safe user input",
  "evidence": null,
  "metadata": {
    "nested_field": "value",
    "score": 0.3
  }
}`

	result, err := engine.parseLLMResponse(response2, 0.5)
	if err != nil {
		t.Errorf("P3-16: Failed to parse nested JSON: %v", err)
	}
	if result.IsInjection {
		t.Error("P3-16: Expected is_injection=false for safe input")
	}
}

// TestP316_ParseLLMResponseThresholdBelowConfidence tests threshold application (P3-16).
func TestP316_ParseLLMResponseThresholdBelowConfidence(t *testing.T) {
	engine := &LLMDetectionEngine{}

	response := `{
  "is_injection": true,
  "confidence": 0.45,
  "categories": ["possible_jailbreak"],
  "reason": "Might be injection",
  "evidence": "unclear"
}`

	// Threshold is 0.5, confidence is 0.45, so should be forced to false
	result, err := engine.parseLLMResponse(response, 0.5)
	if err != nil {
		t.Errorf("P3-16: Failed to parse with threshold: %v", err)
	}
	if result.IsInjection {
		t.Error("P3-16: Expected is_injection=false when confidence < threshold")
	}
}

// TestP316_ParseLLMResponseMissingJSON tests error handling for missing JSON (P3-16).
func TestP316_ParseLLMResponseMissingJSON(t *testing.T) {
	engine := &LLMDetectionEngine{}

	// Response with no JSON
	response := "This response contains no JSON at all."

	_, err := engine.parseLLMResponse(response, 0.5)
	if err == nil {
		t.Error("P3-16: Expected error for response without JSON")
	}
}

// TestP316_ParseLLMResponseEmptyJSON tests handling of minimal JSON (P3-16).
func TestP316_ParseLLMResponseEmptyJSON(t *testing.T) {
	engine := &LLMDetectionEngine{}

	// Minimal JSON object (may have missing fields)
	response := `{"is_injection": false}`

	result, err := engine.parseLLMResponse(response, 0.5)
	if err != nil {
		t.Errorf("P3-16: Failed to parse minimal JSON: %v", err)
	}
	if result.IsInjection {
		t.Error("P3-16: Expected is_injection=false for minimal JSON")
	}
}

// TestP316_ParseLLMResponseMalformedJSON tests robustness with malformed JSON (P3-16).
func TestP316_ParseLLMResponseMalformedJSON(t *testing.T) {
	engine := &LLMDetectionEngine{}

	// Preamble + partially malformed JSON + valid suffix
	response := `The analysis shows:
{
  "is_injection": true,
  "confidence": 0.88,
  "categories": ["jailbreak"],
  "reason": "Clear injection attempt",
  "evidence": "Ignore all previous"
}

This is my final answer.`

	result, err := engine.parseLLMResponse(response, 0.5)
	if err != nil {
		t.Errorf("P3-16: Should handle malformed with valid JSON: %v", err)
	}
	if !result.IsInjection || result.Confidence != 0.88 {
		t.Error("P3-16: Should extract valid JSON despite surrounding text")
	}
}

// TestP316_ParseLLMResponseComplexNesting tests deep JSON nesting (P3-16).
func TestP316_ParseLLMResponseComplexNesting(t *testing.T) {
	engine := &LLMDetectionEngine{}

	response := `{
  "is_injection": true,
  "confidence": 0.92,
  "categories": ["jailbreak", "override", "exfiltration"],
  "reason": "Sophisticated attack",
  "evidence": "System prompt request",
  "analysis": {
    "layers": [
      {"type": "preamble", "confidence": 0.85},
      {"type": "encoding", "confidence": 0.90},
      {"type": "misdirection", "confidence": 0.95}
    ],
    "risk_score": 0.92
  }
}`

	result, err := engine.parseLLMResponse(response, 0.5)
	if err != nil {
		t.Errorf("P3-16: Failed to parse complex nested JSON: %v", err)
	}
	if !result.IsInjection {
		t.Error("P3-16: Expected is_injection=true for complex nesting test")
	}
	if result.Confidence != 0.92 {
		t.Errorf("P3-16: Expected confidence=0.92, got %f", result.Confidence)
	}
}

// TestP316_ParseLLMResponseEdgeCases tests edge cases (P3-16).
func TestP316_ParseLLMResponseEdgeCases(t *testing.T) {
	engine := &LLMDetectionEngine{}

	tests := []struct {
		name        string
		response    string
		threshold   float64
		expectError bool
		expectInj   bool
	}{
		{
			name:        "JSON at end of text",
			response:    "Some text\n\n{\"is_injection\":true,\"confidence\":0.99,\"categories\":[],\"reason\":\"\",\"evidence\":\"\"}",
			threshold:   0.5,
			expectError: false,
			expectInj:   true,
		},
		{
			name:        "Multiple JSON objects",
			response:    "{\"wrong\":true} {\"is_injection\":false,\"confidence\":0.1,\"categories\":[],\"reason\":\"\",\"evidence\":\"\"}",
			threshold:   0.5,
			expectError: false,
			expectInj:   false,
		},
		{
			name:        "JSON with escaped quotes",
			response:    `{"is_injection":true,"confidence":0.75,"categories":[],"reason":"String with \"quotes\"","evidence":"test"}`,
			threshold:   0.5,
			expectError: false,
			expectInj:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.parseLLMResponse(tt.response, tt.threshold)
			if (err != nil) != tt.expectError {
				t.Errorf("P3-16 %s: expectError=%v, got err=%v", tt.name, tt.expectError, err)
			}
			if err == nil && result.IsInjection != tt.expectInj {
				t.Errorf("P3-16 %s: expected is_injection=%v, got %v", tt.name, tt.expectInj, result.IsInjection)
			}
		})
	}
}
