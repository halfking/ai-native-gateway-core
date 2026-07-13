package ir

import (
	"strings"
	"testing"
)

// TestDetectProtocol_GeminiBody verifies Gemini generateContent detection.
//
// audit-gemini-detect (2026-07-13): Gemini detection uses exclusive field
// signals (contents, systemInstruction, generationConfig, safetySettings,
// tools[].functionDeclarations, toolConfig, cachedContent) since these
// names are not used by OpenAI or Anthropic at the top level.
func TestDetectProtocol_GeminiBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			// contents is Gemini's authoritative array
			name: "basic contents",
			body: `{"contents": [{"role": "user", "parts": [{"text": "hi"}]}]}`,
		},
		{
			// contents + role=model (Gemini style)
			name: "contents with model role",
			body: `{"contents": [{"role": "user", "parts": [{"text": "hi"}]}, {"role": "model", "parts": [{"text": "Hello"}]}]}`,
		},
		{
			// systemInstruction is Gemini-only at top level
			name: "systemInstruction",
			body: `{"contents": [{"role": "user", "parts": [{"text": "hi"}]}], "systemInstruction": {"parts": [{"text": "Be helpful"}]}}`,
		},
		{
			// generationConfig with multiple Gemini sampling params
			name: "generationConfig",
			body: `{"contents": [{"role": "user", "parts": [{"text": "hi"}]}], "generationConfig": {"temperature": 0.7, "topP": 0.9, "maxOutputTokens": 1024}}`,
		},
		{
			// safetySettings per-category
			name: "safetySettings",
			body: `{"contents": [{"role": "user", "parts": [{"text": "hi"}]}], "safetySettings": [{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"}]}`,
		},
		{
			// tools with functionDeclarations wrapper
			name: "tools with functionDeclarations",
			body: `{"contents": [{"role": "user", "parts": [{"text": "weather?"}]}], "tools": [{"functionDeclarations": [{"name": "get_weather", "parameters": {"type": "object"}}]}]}`,
		},
		{
			// toolConfig with functionCallingConfig
			name: "toolConfig functionCallingConfig",
			body: `{"contents": [{"role": "user", "parts": [{"text": "hi"}]}], "tools": [{"functionDeclarations": [{"name": "x"}]}], "toolConfig": {"functionCallingConfig": {"mode": "AUTO"}}}`,
		},
		{
			// cachedContent reference (Gemini context caching)
			name: "cachedContent",
			body: `{"contents": [{"role": "user", "parts": [{"text": "hi"}]}], "cachedContent": "cachedContents/abc123"}`,
		},
		{
			// Gemini 2.5 thinking config
			name: "thinkingConfig in generationConfig",
			body: `{"contents": [{"role": "user", "parts": [{"text": "hi"}]}], "generationConfig": {"thinkingConfig": {"thinkingBudget": 8192, "includeThoughts": true}}}`,
		},
		{
			// inlineData part (multimodal)
			name: "inlineData part",
			body: `{"contents": [{"role": "user", "parts": [{"inlineData": {"mimeType": "image/png", "data": "iVBORw0KGgo..."}}]}]}`,
		},
		{
			// fileData part (Gemini Files API)
			name: "fileData part",
			body: `{"contents": [{"role": "user", "parts": [{"fileData": {"mimeType": "video/mp4", "fileUri": "https://example.com/v.mp4"}}]}]}`,
		},
		{
			// functionCall part
			name: "functionCall part",
			body: `{"contents": [{"role": "model", "parts": [{"functionCall": {"name": "get_weather", "args": {"city": "Beijing"}}}]}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, conf, err := DetectProtocol([]byte(tt.body))
			if err != nil {
				t.Fatalf("DetectProtocol error: %v", err)
			}
			if proto != ProtocolGeminiGenerate {
				t.Errorf("protocol = %q, want %q", proto, ProtocolGeminiGenerate)
			}
			if conf < 0.5 {
				t.Errorf("confidence = %v, want >= 0.5", conf)
			}
		})
	}
}

// TestDetectProtocol_GeminiMultipleSignals verifies that Gemini detection
// becomes high-confidence when multiple Gemini-exclusive fields are present.
func TestDetectProtocol_GeminiMultipleSignals(t *testing.T) {
	// Both contents + generationConfig + safetySettings + functionDeclarations
	body := `{
		"contents": [{"role": "user", "parts": [{"text": "hi"}]}],
		"generationConfig": {"temperature": 0.7},
		"safetySettings": [{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"}],
		"tools": [{"functionDeclarations": [{"name": "x"}]}]
	}`
	proto, conf, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if proto != ProtocolGeminiGenerate {
		t.Errorf("protocol = %q, want %q", proto, ProtocolGeminiGenerate)
	}
	if conf < 0.85 {
		t.Errorf("confidence = %v, want >= 0.85 (multiple signals)", conf)
	}
}

// TestDetectProtocol_GeminiModelNameHint verifies that a Gemini model name
// triggers detection on otherwise empty bodies.
func TestDetectProtocol_GeminiModelNameHint(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "empty body with gemini model",
			body: `{"model": "gemini-2.5-pro"}`,
		},
		{
			name: "empty body with gemini flash",
			body: `{"model": "gemini-2.0-flash"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, conf, err := DetectProtocol([]byte(tt.body))
			if err != nil {
				t.Fatalf("DetectProtocol: %v", err)
			}
			if proto != ProtocolGeminiGenerate {
				t.Errorf("protocol = %q, want %q", proto, ProtocolGeminiGenerate)
			}
			if conf < 0.5 {
				t.Errorf("confidence = %v, want >= 0.5", conf)
			}
		})
	}
}

// TestDetectProtocol_GeminiDoesNotFalsePositiveOnAnthropicFields verifies
// that Anthropic-only bodies don't accidentally trigger Gemini detection.
func TestDetectProtocol_GeminiDoesNotFalsePositiveOnAnthropicFields(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"system": "You are helpful",
		"thinking": {"type": "enabled", "budget_tokens": 4096},
		"messages": [{"role": "user", "content": "hi"}]
	}`
	proto, _, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if proto == ProtocolGeminiGenerate {
		t.Error("Anthropic body incorrectly detected as Gemini")
	}
	if proto != ProtocolAnthropicMessages {
		t.Errorf("protocol = %q, want %q", proto, ProtocolAnthropicMessages)
	}
}

// TestDetectProtocol_GeminiDoesNotFalsePositiveOnOpenAIFields verifies
// that OpenAI-only bodies don't accidentally trigger Gemini detection.
func TestDetectProtocol_GeminiDoesNotFalsePositiveOnOpenAIFields(t *testing.T) {
	body := `{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hi"}],
		"frequency_penalty": 0.5,
		"logprobs": true,
		"tools": [{"type": "function", "function": {"name": "x", "parameters": {}}}]
	}`
	proto, _, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if proto == ProtocolGeminiGenerate {
		t.Error("OpenAI body incorrectly detected as Gemini")
	}
	if proto != ProtocolOpenAIChat {
		t.Errorf("protocol = %q, want %q", proto, ProtocolOpenAIChat)
	}
}

// TestDetectProtocol_GeminiNotConfusedWithOpenAIMultimodal verifies that
// OpenAI multimodal fields (modalities, audio, image_url) don't trigger
// Gemini detection (those exist in OpenAI now).
func TestDetectProtocol_GeminiNotConfusedWithOpenAIMultimodal(t *testing.T) {
	body := `{
		"model": "gpt-4o-audio-preview",
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "Listen"},
			{"type": "input_audio", "input_audio": {"data": "abc", "format": "wav"}}
		]}],
		"modalities": ["text", "audio"],
		"audio": {"voice": "alloy", "format": "wav"}
	}`
	proto, _, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if proto != ProtocolOpenAIChat {
		t.Errorf("OpenAI audio body incorrectly detected as %q", proto)
	}
}

// TestDetectProtocol_GeminiPriorityOverAmbiguousURL verifies that when a
// Gemini body is sent with the right body shape, body-based detection
// overrides URL-based hints.
func TestDetectProtocol_GeminiPriorityOverAmbiguousURL(t *testing.T) {
	body := `{
		"contents": [{"role": "user", "parts": [{"text": "hi"}]}],
		"generationConfig": {"temperature": 0.5}
	}`
	// Even with an OpenAI URL hint, the Gemini body wins.
	proto, _, err := DetectProtocolByURL([]byte(body), "/v1/chat/completions")
	if err != nil {
		t.Fatalf("DetectProtocolByURL: %v", err)
	}
	if proto != ProtocolGeminiGenerate {
		t.Errorf("Gemini body lost to URL hint: got %q", proto)
	}
}

// TestDetectProtocolByURL_GeminiEndpoints verifies URL-based detection for
// Gemini streaming and non-streaming endpoints.
func TestDetectProtocolByURL_GeminiEndpoints(t *testing.T) {
	// Empty body — URL must drive the detection
	emptyBody := []byte(`{}`)

	tests := []struct {
		name      string
		url       string
		wantProto string
	}{
		{
			name:      "v1beta generateContent",
			url:       "/v1beta/models/gemini-2.5-pro:generateContent",
			wantProto: ProtocolGeminiGenerate,
		},
		{
			name:      "v1 generateContent",
			url:       "/v1/models/gemini-2.0-flash:generateContent",
			wantProto: ProtocolGeminiGenerate,
		},
		{
			name:      "streamGenerateContent",
			url:       "/v1beta/models/gemini-2.5-pro:streamGenerateContent",
			wantProto: ProtocolGeminiGenerate,
		},
		{
			name:      "v1beta models base path",
			url:       "/v1beta/models/gemini-2.5-pro",
			wantProto: ProtocolGeminiGenerate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, _, err := DetectProtocolByURL(emptyBody, tt.url)
			if err != nil {
				t.Fatalf("DetectProtocolByURL: %v", err)
			}
			if proto != tt.wantProto {
				t.Errorf("protocol = %q, want %q", proto, tt.wantProto)
			}
		})
	}
}

// TestDetectProtocolByURL_BodyOverridesURLHint verifies that high-confidence
// body detection wins over URL hints.
func TestDetectProtocolByURL_BodyOverridesURLHint(t *testing.T) {
	// Strong Gemini body (high confidence) — should win over OpenAI URL.
	// contents alone produces conf=0.85, which is >= 0.5 threshold.
	body := `{"contents": [{"role": "user", "parts": [{"text": "hi"}]}], "generationConfig": {"temperature": 0.5}}`
	proto, _, err := DetectProtocolByURL([]byte(body), "/v1/chat/completions")
	if err != nil {
		t.Fatalf("DetectProtocolByURL: %v", err)
	}
	if proto != ProtocolGeminiGenerate {
		t.Errorf("Strong Gemini body lost to URL hint: got %q", proto)
	}
}

// TestDetectProtocolByURL_LowConfidenceBodyUsesURLHint verifies that
// low-confidence body detection (just messages[]) is overridden by URL.
func TestDetectProtocolByURL_LowConfidenceBodyUsesURLHint(t *testing.T) {
	// Weak body — just messages[], conf=0.1875 (< 0.5). URL wins.
	body := `{"messages": [{"role": "user", "content": "hi"}]}`
	proto, _, err := DetectProtocolByURL([]byte(body), "/v1/messages")
	if err != nil {
		t.Fatalf("DetectProtocolByURL: %v", err)
	}
	if proto != ProtocolAnthropicMessages {
		t.Errorf("Weak body lost to URL hint: got %q", proto)
	}
}

// TestDetectProtocolByURL_AmbiguousBodyUsesURLHint verifies that when the
// body produces an "unknown" verdict, the URL hint is the tiebreaker.
func TestDetectProtocolByURL_AmbiguousBodyUsesURLHint(t *testing.T) {
	// Truly ambiguous body — only model name, no other signals.
	// With Anthropic URL, expect Anthropic.
	body := `{"model": "claude-sonnet-4"}`
	proto, _, err := DetectProtocolByURL([]byte(body), "/v1/messages")
	if err != nil {
		t.Fatalf("DetectProtocolByURL: %v", err)
	}
	if proto != ProtocolAnthropicMessages {
		t.Errorf("Ambiguous body on Anthropic URL: got %q", proto)
	}
}

// TestDetectProtocol_GeminiNestedInOpenAIObject verifies that nested Gemini
// structures inside an OpenAI object don't confuse detection (e.g.,
// multimodal images in OpenAI content blocks).
func TestDetectProtocol_GeminiNestedInOpenAIObject(t *testing.T) {
	// OpenAI body with a Gemini-style nested object in metadata
	body := `{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hi"}],
		"metadata": {"contents": "should-not-trigger-gemini"}
	}`
	proto, _, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if proto != ProtocolOpenAIChat {
		t.Errorf("nested 'contents' should not trigger Gemini: got %q", proto)
	}
}

// TestDetectProtocol_GeminiHighConfidence verifies that the contents field
// alone produces 0.85+ confidence (decisive signal).
func TestDetectProtocol_GeminiHighConfidence(t *testing.T) {
	body := `{"contents": [{"role": "user", "parts": [{"text": "hi"}]}]}`
	proto, conf, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if proto != ProtocolGeminiGenerate {
		t.Errorf("protocol = %q, want %q", proto, ProtocolGeminiGenerate)
	}
	if conf < 0.85 {
		t.Errorf("confidence = %v, want >= 0.85 (contents alone is decisive)", conf)
	}
	if !strings.Contains(proto, "gemini") {
		t.Errorf("protocol string should contain 'gemini': %q", proto)
	}
}

// TestDetectProtocol_GeminiSingleExclusiveField verifies that a single
// Gemini-exclusive field (e.g., generationConfig without contents) still
// gets detected as Gemini with 0.7 confidence.
func TestDetectProtocol_GeminiSingleExclusiveField(t *testing.T) {
	// generationConfig alone is a strong Gemini hint (OpenAI/Anthropic
	// don't use this name).
	body := `{"generationConfig": {"temperature": 0.7, "maxOutputTokens": 1024}}`
	proto, conf, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if proto != ProtocolGeminiGenerate {
		t.Errorf("protocol = %q, want %q", proto, ProtocolGeminiGenerate)
	}
	if conf < 0.6 {
		t.Errorf("confidence = %v, want >= 0.6", conf)
	}
}
