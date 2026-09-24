package ir

import (
	"testing"
)

func TestDetectProtocol_OpenAIBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "basic messages",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "with tools",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {}}}]}`,
		},
		{
			name: "with frequency_penalty",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "frequency_penalty": 0.5}`,
		},
		{
			name: "with logprobs",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "logprobs": true, "top_logprobs": 5}`,
		},
		{
			name: "with response_format",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "response_format": {"type": "json_object"}}`,
		},
		{
			name: "with seed",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "seed": 42}`,
		},
		{
			name: "with user field",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "user": "user123"}`,
		},
		{
			name: "with n",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "n": 2}`,
		},
		{
			name: "with max_completion_tokens",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "max_completion_tokens": 500}`,
		},
		{
			name: "with tool_choice required",
			body: `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "tool_choice": "required"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, conf, err := DetectProtocol([]byte(tt.body))
			if err != nil {
				t.Fatalf("DetectProtocol error: %v", err)
			}
			if proto != ProtocolOpenAIChat {
				t.Errorf("protocol = %q, want %q", proto, ProtocolOpenAIChat)
			}
			if conf < 0.15 {
				t.Errorf("confidence = %v, want >= 0.15", conf)
			}
		})
	}
}

func TestDetectProtocol_ResponsesBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "input string",
			body: `{"model":"gpt-5","input":"hello"}`,
		},
		{
			name: "instructions and typed input",
			body: `{"model":"gpt-5","instructions":"be brief","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
		},
		{
			name: "response chaining",
			body: `{"model":"gpt-5","previous_response_id":"resp_123"}`,
		},
		{
			name: "responses sampling field",
			body: `{"model":"gpt-5","max_output_tokens":128}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			protocol, confidence, err := DetectProtocol([]byte(tt.body))
			if err != nil {
				t.Fatalf("DetectProtocol: %v", err)
			}
			if protocol != ProtocolOpenAIResponses {
				t.Errorf("protocol = %q, want %q", protocol, ProtocolOpenAIResponses)
			}
			if confidence < 0.5 {
				t.Errorf("confidence = %v, want >= 0.5", confidence)
			}
		})
	}
}

func TestDetectProtocol_AnthropicBody(t *testing.T) {
	// Phase E (2026-07-01) regression fix: body shape is the canonical
	// protocol indicator. The `messages[]` array contributes +0.3 to
	// openAIScore (normalized 0.1875), which dominates any anthropic
	// extension fields when both are present. So bodies that include
	// `messages[]` along with anthropic-specific fields (system,
	// thinking, etc.) are correctly detected as Chat Completions —
	// the executor's request-body conversion handles the field
	// translation when the upstream is anthropic-messages.
	//
	// True Anthropic Messages bodies — those sent through /v1/messages —
	// don't carry `messages[]` in the format we recognize here. They
	// typically only carry `system` + `messages` (with content as
	// blocks) AND have multiple anthropic signals strong enough to
	// outscore the messages[] contribution.
	tests := []struct {
		name string
		body string
	}{
		{
			// system + thinking + cache_control + messages[]
			// anthropicScore (0.8 raw → 0.38 normalized) wins over
			// messages[] (0.1875 normalized).
			name: "anthropic signals dominate messages",
			body: `{"model": "claude-sonnet-4", "system": "You are helpful", "thinking": {"type": "enabled"}, "cache_control": {"type": "ephemeral"}, "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			// Same idea with different field mix.
			name: "documents + thinking + cache_control + messages",
			body: `{"model": "claude-sonnet-4", "documents": [{"type": "document", "source": {"type": "text", "data": "x"}}], "thinking": {"type": "enabled"}, "cache_control": {"type": "ephemeral"}, "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			// anthropic tools + thinking + system + messages
			name: "anthropic tools + thinking + system + messages",
			body: `{"model": "claude-sonnet-4", "tools": [{"name": "get_weather", "input_schema": {"type": "object"}}], "thinking": {"type": "enabled"}, "system": "Help", "messages": [{"role": "user", "content": "hi"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, conf, err := DetectProtocol([]byte(tt.body))
			if err != nil {
				t.Fatalf("DetectProtocol error: %v", err)
			}
			if proto != ProtocolAnthropicMessages {
				t.Errorf("protocol = %q, want %q", proto, ProtocolAnthropicMessages)
			}
			if conf < 0.15 {
				t.Errorf("confidence = %v, want >= 0.15", conf)
			}
		})
	}
}

// TestDetectProtocol_ChatCompletionWithAnthropicFields covers the
// realistic case where a Chat Completions client sends one or two
// Anthropic-style fields (system, thinking, etc.) to a Claude model.
// The body is still Chat Completions (messages[] array is the canonical
// marker), and the executor's request-body conversion handles the
// format translation when the upstream is anthropic-messages.
//
// Previously these bodies were mis-classified as anthropic-messages
// because the model name "claude" tipped the result in the (broken)
// < 0.2 ambiguity branch — see Phase E fix above.
func TestDetectProtocol_ChatCompletionWithAnthropicFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "system only",
			body: `{"model": "claude-sonnet-5", "system": "You are helpful", "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "thinking only",
			body: `{"model": "claude-sonnet-5", "thinking": {"type": "enabled"}, "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "cache_control only",
			body: `{"model": "claude-sonnet-5", "cache_control": {"type": "ephemeral"}, "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "top_k only",
			body: `{"model": "claude-sonnet-5", "top_k": 100, "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "anthropic tools only (input_schema)",
			body: `{"model": "claude-sonnet-5", "tools": [{"name": "get_weather", "input_schema": {"type": "object"}}], "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "tool_choice type=tool only",
			body: `{"model": "claude-sonnet-5", "tool_choice": {"type": "tool", "name": "get_weather"}, "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "documents only",
			body: `{"model": "claude-sonnet-5", "documents": [{"type": "document", "source": {"type": "text", "data": "x"}}], "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "stop_sequences only",
			body: `{"model": "claude-sonnet-5", "stop_sequences": ["END"], "messages": [{"role": "user", "content": "hi"}]}`,
		},
		{
			name: "metadata.user_id only",
			body: `{"model": "claude-sonnet-5", "metadata": {"user_id": "u123"}, "messages": [{"role": "user", "content": "hi"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, _, err := DetectProtocol([]byte(tt.body))
			if err != nil {
				t.Fatalf("DetectProtocol error: %v", err)
			}
			if proto != ProtocolOpenAIChat {
				t.Errorf("protocol = %q, want %q (body shape is Chat Completions)",
					proto, ProtocolOpenAIChat)
			}
		})
	}
}

func TestDetectProtocol_ModelBasedDetection(t *testing.T) {
	// Phase E (2026-07-01) regression fix: body shape is the canonical
	// protocol indicator. The `messages[]` array is a definitive
	// Chat Completions marker — even with a Claude model name, the
	// body is OpenAI Chat Completions and must be detected as such.
	//
	// Previously the model hint branch kicked in whenever both body
	// scores were below 0.2 normalized, but a minimal Chat Completions
	// body yields openAIScore=0.1875 (just from `messages`) which fell
	// into that ambiguous bucket. The Claude model name then tipped
	// the result to anthropic-messages, which broke Q3 routing for
	// `/v1/chat/completions` → apiclaude traffic.
	//
	// The model hint now only resolves truly empty bodies (both scores
	// < 0.1) where the model name is genuinely the best hint.
	tests := []struct {
		name      string
		body      string
		wantProto string
	}{
		{
			// Body has messages[] → definitive Chat Completions shape.
			// Model name "claude" is irrelevant.
			name:      "claude in model",
			body:      `{"model": "claude-sonnet-4-20250514", "messages": [{"role": "user", "content": "hi"}]}`,
			wantProto: ProtocolOpenAIChat,
		},
		{
			name:      "gpt in model",
			body:      `{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}]}`,
			wantProto: ProtocolOpenAIChat,
		},
		{
			name:      "chatgpt in model",
			body:      `{"model": "chatgpt-4o", "messages": [{"role": "user", "content": "hi"}]}`,
			wantProto: ProtocolOpenAIChat,
		},
		{
			// Truly empty body — model hint decides.
			name:      "empty body with claude model",
			body:      `{"model": "claude-sonnet-4-20250514"}`,
			wantProto: ProtocolAnthropicMessages,
		},
		{
			// Truly empty body with gpt model — model hint decides.
			name:      "empty body with gpt model",
			body:      `{"model": "gpt-4o"}`,
			wantProto: ProtocolOpenAIChat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, _, err := DetectProtocol([]byte(tt.body))
			if err != nil {
				t.Fatalf("DetectProtocol error: %v", err)
			}
			if proto != tt.wantProto {
				t.Errorf("protocol = %q, want %q", proto, tt.wantProto)
			}
		})
	}
}

func TestDetectProtocol_EmptyBody(t *testing.T) {
	_, _, err := DetectProtocol([]byte{})
	if err == nil {
		t.Error("expected error for empty body")
	}
}

func TestDetectProtocol_InvalidJSON(t *testing.T) {
	_, _, err := DetectProtocol([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestDetectProtocol_UnknownBody(t *testing.T) {
	// Body with no distinguishing fields
	body := `{"model": "some-model", "messages": [{"role": "user", "content": "hi"}]}`
	proto, _, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should default to OpenAI when truly ambiguous
	if proto != ProtocolOpenAIChat {
		t.Logf("protocol = %q, may default to OpenAI for ambiguous body", proto)
	}
}

func TestDetectProtocolByURL(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		url       string
		wantProto string
	}{
		{
			name:      "openai chat completions URL",
			body:      `{"messages": [{"role": "user", "content": "hi"}]}`,
			url:       "/v1/chat/completions",
			wantProto: ProtocolOpenAIChat,
		},
		{
			name:      "anthropic messages URL",
			body:      `{"messages": [{"role": "user", "content": "hi"}]}`,
			url:       "/v1/messages",
			wantProto: ProtocolAnthropicMessages,
		},
		{
			name:      "empty body falls back to openai chat URL",
			url:       "/v1/chat/completions",
			wantProto: ProtocolOpenAIChat,
		},
		{
			name:      "empty body falls back to anthropic messages URL",
			url:       "/v1/messages",
			wantProto: ProtocolAnthropicMessages,
		},
		{
			name:      "empty body falls back to responses URL",
			url:       "/v1/responses",
			wantProto: ProtocolOpenAIResponses,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, _, err := DetectProtocolByURL([]byte(tt.body), tt.url)
			if err != nil {
				t.Fatalf("DetectProtocolByURL error: %v", err)
			}
			if proto != tt.wantProto {
				t.Errorf("protocol = %q, want %q", proto, tt.wantProto)
			}
		})
	}
}

func BenchmarkDetectProtocol(b *testing.B) {
	bodies := []string{
		`{"model": "gpt-4o", "messages": [{"role": "user", "content": "hi"}], "tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {}}}]}`,
		`{"model": "claude-sonnet-4-20250514", "max_tokens": 1024, "system": "You are helpful", "messages": [{"role": "user", "content": "hi"}]}`,
		`{"model": "MiniMax-M2.7", "messages": [{"role": "user", "content": "hi"}]}`,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, body := range bodies {
			_, _, err := DetectProtocol([]byte(body))
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}

// ── Ollama-native detection (r0924 supplier-protocol-optimization §3.6 / §2.4) ──

func TestDetectProtocol_OllamaOptionsOnly(t *testing.T) {
	body := `{"model":"llama3.1","messages":[{"role":"user","content":"hi"}],"options":{"temperature":0.7}}`
	proto, conf, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if proto != ProtocolOllamaChat {
		t.Errorf("protocol = %q, want %q", proto, ProtocolOllamaChat)
	}
	if conf < 0.85 {
		t.Errorf("confidence = %v, want >= 0.85", conf)
	}
}

func TestDetectProtocol_OllamaKeepAlive(t *testing.T) {
	body := `{"model":"llama3.1","messages":[{"role":"user","content":"hi"}],"keep_alive":"5m"}`
	proto, conf, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if proto != ProtocolOllamaChat {
		t.Errorf("protocol = %q, want %q", proto, ProtocolOllamaChat)
	}
	if conf < 0.85 {
		t.Errorf("confidence = %v, want >= 0.85", conf)
	}
}

func TestDetectProtocol_OllamaRaw(t *testing.T) {
	body := `{"model":"llama3.1","messages":[{"role":"user","content":"hi"}],"raw":false}`
	proto, _, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if proto != ProtocolOllamaChat {
		t.Errorf("protocol = %q, want %q", proto, ProtocolOllamaChat)
	}
}

func TestDetectProtocol_OllamaFormatString(t *testing.T) {
	// String `format` (Ollama) — NOT object (OpenAI response_format).
	body := `{"model":"llama3.1","messages":[{"role":"user","content":"hi"}],"format":"json"}`
	proto, _, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if proto != ProtocolOllamaChat {
		t.Errorf("protocol = %q, want %q", proto, ProtocolOllamaChat)
	}
}

func TestDetectProtocol_OllamaAllPrivateFieldsBoostsConfidence(t *testing.T) {
	// Every Ollama-private key — confidence must climb above the 0.85
	// baseline. Documents the "more signals → higher confidence" rule.
	body := `{
		"model":"llama3.1",
		"messages":[{"role":"user","content":"hi"}],
		"options":{"temperature":0.7},
		"keep_alive":"5m",
		"raw":false,
		"format":"json"
	}`
	proto, conf, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if proto != ProtocolOllamaChat {
		t.Errorf("protocol = %q, want %q", proto, ProtocolOllamaChat)
	}
	if conf < 0.95 {
		t.Errorf("confidence = %v, want >= 0.95", conf)
	}
}

func TestDetectProtocol_OllamaOptionsNoMessagesFallback(t *testing.T) {
	// Embedding-style request: only `options`, no `messages`. The
	// fallback branch should classify as Ollama at moderate confidence.
	body := `{"model":"llama3.1","options":{"temperature":0.7}}`
	proto, conf, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if proto != ProtocolOllamaChat {
		t.Errorf("protocol = %q, want %q", proto, ProtocolOllamaChat)
	}
	if conf < 0.65 {
		t.Errorf("confidence = %v, want >= 0.65", conf)
	}
}

func TestDetectProtocol_OllamaBeatsOpenAIChatScoring(t *testing.T) {
	// Body looks OpenAI-shaped BUT carries `options` — the Ollama branch
	// must short-circuit before the OpenAI scoring fires. This is the
	// "Ollama native client hitting /v1/chat/completions" case from
	// F6 audit (静默降级): previously the openAIScore from the
	// `messages[]` field gave 0.3 and won the body-shape comparison.
	body := `{"model":"llama3.1","messages":[{"role":"user","content":"hi"}],"options":{"temperature":0.7}}`
	proto, _, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if proto != ProtocolOllamaChat {
		t.Errorf("protocol = %q, want %q (must NOT fall through to OpenAI)", proto, ProtocolOllamaChat)
	}
}

func TestDetectProtocolByURL_OllamaChat(t *testing.T) {
	proto, _, err := DetectProtocolByURL(nil, "/api/chat")
	if err != nil {
		t.Fatalf("DetectProtocolByURL: %v", err)
	}
	if proto != ProtocolOllamaChat {
		t.Errorf("protocol = %q, want %q", proto, ProtocolOllamaChat)
	}
}

func TestDetectProtocolByURL_OllamaGenerate(t *testing.T) {
	proto, _, err := DetectProtocolByURL(nil, "/api/generate")
	if err != nil {
		t.Fatalf("DetectProtocolByURL: %v", err)
	}
	if proto != ProtocolOllamaChat {
		t.Errorf("protocol = %q, want %q", proto, ProtocolOllamaChat)
	}
}

func TestDetectProtocolByURL_OpenAIShapeOverridesOllamaURL(t *testing.T) {
	// High-confidence body detection wins over URL: an OpenAI body sent
	// to /api/chat (e.g. operator misroute) is still OpenAI.
	body := `{"messages":[{"role":"user","content":"hi"}],"frequency_penalty":0.5,"presence_penalty":0.5,"seed":42,"logprobs":true,"response_format":{"type":"json_object"}}`
	proto, _, err := DetectProtocolByURL([]byte(body), "/api/chat")
	if err != nil {
		t.Fatalf("DetectProtocolByURL: %v", err)
	}
	if proto != ProtocolOpenAIChat {
		t.Errorf("protocol = %q, want %q (high-confidence body wins over URL)", proto, ProtocolOpenAIChat)
	}
}

func TestDetectProtocol_RawBoolOnlyDoesNotTriggerOllama(t *testing.T) {
	// An Ollama-style `raw:false` MUST be a boolean to count. If the field
	// is a string (a misuse), the OpenAI scoring should run normally and
	// we should NOT classify as Ollama.
	body := `{"model":"llama3.1","messages":[{"role":"user","content":"hi"}],"raw":"false"}`
	proto, _, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	// Either OpenAI (default) or unknown — but never Ollama, because
	// the `raw` value is not a bool.
	if proto == ProtocolOllamaChat {
		t.Errorf("protocol = %q, must not be Ollama when raw is non-bool", proto)
	}
}

func TestDetectProtocol_FormatObjectIsOpenAIResponseFormat(t *testing.T) {
	// `format` as an object is OpenAI's `response_format`, NOT Ollama.
	// The Ollama branch's type check (string) must reject this shape.
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"format":{"type":"json_object"}}`
	proto, _, err := DetectProtocol([]byte(body))
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if proto == ProtocolOllamaChat {
		t.Errorf("protocol = %q, format-as-object must not be Ollama", proto)
	}
	// Should land at OpenAI (response_format is a strong signal).
	if proto != ProtocolOpenAIChat {
		t.Errorf("protocol = %q, want OpenAI (response_format signal)", proto)
	}
}
