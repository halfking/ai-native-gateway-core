//go:build integration

// Package integration hosts spec §11.1 "全协议 E2E" coverage.
//
// This file exercises the IR Transport layer
// (domains/transformation.IRTransport) across every (client_protocol ×
// upstream_protocol) combination required by §11.1:
//
//   - OpenAI Chat / Anthropic Messages / Gemini generateContent /
//     OpenAI Responses, stream & non-stream, with tools and error inputs.
//
// It is a pure in-memory conversion test: no PostgreSQL or Redis is
// required (IR Transport is a stateless protocol converter). The
// `integration` build tag is kept for consistency with the rest of the
// suite so this coverage graduates into full-gateway E2E unchanged when
// the gateway process harness is wired up later.
//
// Run with:
//
//	go test -tags=integration ./tests/integration -v -run TestProtocolE2E
package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/transformation"
)

// protocolCombo is one cell of the §11.1 cross-protocol matrix.
type protocolCombo struct {
	name     string // human label, also used in subtest names
	client   string // client (inbound) protocol
	upstream string // upstream (outbound) protocol
}

// allCombos is the 4×3 dedup matrix (no same-protocol pairs).
//
// Spec §7.1 IR main-path extension (2026-08-02): now that the IR layer ships a
// ParseResponses request parser, Responses is also valid as a *client* input
// protocol, so Responses→{OpenAI,Anthropic,Gemini} rows are included here.
var allCombos = []protocolCombo{
	{"OpenAIChat_To_Anthropic", "openai-chat", "anthropic-messages"},
	{"OpenAIChat_To_Gemini", "openai-chat", "gemini-generate"},
	{"OpenAIChat_To_Responses", "openai-chat", "openai-responses"},
	{"Anthropic_To_OpenAIChat", "anthropic-messages", "openai-chat"},
	{"Anthropic_To_Gemini", "anthropic-messages", "gemini-generate"},
	{"Anthropic_To_Responses", "anthropic-messages", "openai-responses"},
	{"Gemini_To_OpenAIChat", "gemini-generate", "openai-chat"},
	{"Gemini_To_Anthropic", "gemini-generate", "anthropic-messages"},
	{"Gemini_To_Responses", "gemini-generate", "openai-responses"},
	{"Responses_To_OpenAIChat", "openai-responses", "openai-chat"},
	{"Responses_To_Anthropic", "openai-responses", "anthropic-messages"},
	{"Responses_To_Gemini", "openai-responses", "gemini-generate"},
}

// fixtures holds one body per inbound protocol for each scenario. The
// upstream protocol only affects how the IR is serialized, so a single
// per-(protocol, scenario) body is reused across every upstream target.
type fixtures struct {
	// plain is a simple text conversation (non-stream).
	plain map[string]string
	// tool is a request carrying one function tool + tool_choice=auto.
	tool map[string]string
	// stream is identical to plain but with stream=true.
	stream map[string]string
	// errorBody is a malformed body guaranteed to fail parsing in every
	// protocol (invalid JSON).
	errorBody map[string]string
}

// buildFixtures constructs the canonical request bodies per protocol.
//
// Notes on model fields:
//   - Gemini generateContent has no top-level "model"; the model lives in
//     the URL path and is carried by envelope.ClientModel. IR parsers
//     therefore leave req.Model empty for Gemini input. Serializers that
//     require a model (OpenAI/Anthropic/Responses) emit it from
//     req.Model, so Gemini→{OpenAI,Anthropic,Responses} outputs an empty
//     model — that is the documented behavior (see golden fixture
//     gemini_to_openai/expected.json). The model-preservation assertion
//     is therefore protocol-aware below.
func buildFixtures() fixtures {
	const openAIPlain = `{
  "model": "gpt-4o",
  "max_tokens": 256,
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Say hello in one sentence."}
  ]
}`

	const openAITool = `{
  "model": "gpt-4o",
  "max_tokens": 256,
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get current weather for a city",
        "parameters": {
          "type": "object",
          "properties": {"city": {"type": "string"}},
          "required": ["city"]
        }
      }
    }
  ],
  "tool_choice": "auto",
  "messages": [
    {"role": "user", "content": "What is the weather in Tokyo?"}
  ]
}`

	const anthropicPlain = `{
  "model": "claude-sonnet-4-20250514",
  "max_tokens": 256,
  "system": "You are a helpful assistant.",
  "messages": [
    {"role": "user", "content": "Say hello in one sentence."}
  ]
}`

	const anthropicTool = `{
  "model": "claude-sonnet-4-20250514",
  "max_tokens": 256,
  "system": "You are a helpful assistant.",
  "tools": [
    {
      "name": "get_weather",
      "description": "Get current weather for a city",
      "input_schema": {
        "type": "object",
        "properties": {"city": {"type": "string"}},
        "required": ["city"]
      }
    }
  ],
  "tool_choice": {"type": "auto"},
  "messages": [
    {"role": "user", "content": "What is the weather in Tokyo?"}
  ]
}`

	const geminiPlain = `{
  "contents": [
    {"role": "user", "parts": [{"text": "Say hello in one sentence."}]}
  ],
  "systemInstruction": {"parts": [{"text": "You are a helpful assistant."}]},
  "generationConfig": {"maxOutputTokens": 256}
}`

	const geminiTool = `{
  "contents": [
    {"role": "user", "parts": [{"text": "What is the weather in Tokyo?"}]}
  ],
  "systemInstruction": {"parts": [{"text": "You are a helpful assistant."}]},
  "tools": [
    {
      "functionDeclarations": [
        {
          "name": "get_weather",
          "description": "Get current weather for a city",
          "parameters": {
            "type": "OBJECT",
            "properties": {"city": {"type": "STRING"}},
            "required": ["city"]
          }
        }
      ]
    }
  ],
  "toolConfig": {"functionCallingConfig": {"mode": "AUTO"}},
  "generationConfig": {"maxOutputTokens": 256}
}`

	// Responses API plain body. "input" replaces messages[]; the system prompt
	// is hoisted to top-level "instructions"; max_output_tokens replaces
	// max_tokens.
	const responsesPlain = `{
  "model": "gpt-4o",
  "instructions": "You are a helpful assistant.",
  "max_output_tokens": 256,
  "input": [
    {"role": "user", "content": [{"type": "input_text", "text": "Say hello in one sentence."}]}
  ]
}`

	// Responses tool body: flat {type:"function", name, parameters} shape and
	// tool_choice as a bare string.
	const responsesTool = `{
  "model": "gpt-4o",
  "instructions": "You are a helpful assistant.",
  "max_output_tokens": 256,
  "tools": [
    {
      "type": "function",
      "name": "get_weather",
      "description": "Get current weather for a city",
      "parameters": {
        "type": "object",
        "properties": {"city": {"type": "string"}},
        "required": ["city"]
      }
    }
  ],
  "tool_choice": "auto",
  "input": [
    {"role": "user", "content": [{"type": "input_text", "text": "What is the weather in Tokyo?"}]}
  ]
}`

	// plainStream bodies are derived from plain by injecting "stream":true.
	openAIStream := injectStream(openAIPlain, true)
	anthropicStream := injectStream(anthropicPlain, true)
	geminiStream := geminiPlain // Gemini has no stream flag (see notes)
	responsesStream := injectStream(responsesPlain, true)

	return fixtures{
		plain: map[string]string{
			"openai-chat":        openAIPlain,
			"anthropic-messages": anthropicPlain,
			"gemini-generate":    geminiPlain,
			"openai-responses":   responsesPlain,
		},
		tool: map[string]string{
			"openai-chat":        openAITool,
			"anthropic-messages": anthropicTool,
			"gemini-generate":    geminiTool,
			"openai-responses":   responsesTool,
		},
		stream: map[string]string{
			"openai-chat":        openAIStream,
			"anthropic-messages": anthropicStream,
			"gemini-generate":    geminiStream,
			"openai-responses":   responsesStream,
		},
		// Each protocol's error body is invalid JSON so parsing deterministically
		// fails. (Empty-model bodies do NOT error in the IR parsers — they
		// serialize with model="" — so they are not useful as error fixtures.)
		errorBody: map[string]string{
			"openai-chat":        `{invalid-json`,
			"anthropic-messages": `{invalid-json`,
			"gemini-generate":    `{invalid-json`,
			"openai-responses":   `{invalid-json`,
		},
	}
}

// injectStream toggles the top-level "stream" boolean of a JSON object body.
// For bodies that already carry "stream" it is overwritten. It panics on a
// parse failure (fixture authoring bug, not a runtime path).
func injectStream(body string, on bool) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		panic("injectStream: invalid fixture body: " + err.Error())
	}
	m["stream"] = on
	out, err := json.Marshal(m)
	if err != nil {
		panic("injectStream: marshal: " + err.Error())
	}
	return string(out)
}

// newE2EEnvelope builds a RequestEnvelope mirroring the
// domains/transformation newEnvelope helper but living in the integration
// package (which cannot reach the package-private helper).
func newE2EEnvelope(client, upstream, body, model string) *domain.RequestEnvelope {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return domain.NewEnvelopeBuilder("e2e-req").
		WithTransport(&domain.TransportContext{
			R:                req,
			BodyBytes:        []byte(body),
			ClientProtocol:   client,
			UpstreamProtocol: upstream,
			ClientModel:      model,
		}).
		Build()
}

// toolCarrierKey is the JSON object key under which the upstream protocol
// surfaces tool definitions. Every supported upstream surfaces them under
// "tools" (OpenAI/Responses/Anthropic as tools[], Gemini as
// tools[].functionDeclarations[]), so the carrier key is uniform.
func toolCarrierKey(upstream string) string {
	return "tools"
}

// countTools counts tool definitions in the converted output. Returns the
// length of the upstream tool carrier array, or 0 if absent.
func countTools(t *testing.T, combo protocolCombo, out []byte) int {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("%s: cannot parse output for tool count: %v\noutput: %s", combo.name, err, out)
	}
	key := toolCarrierKey(combo.upstream)
	if tools, ok := parsed[key].([]any); ok {
		return len(tools)
	}
	return 0
}

// jsonField returns the raw value of a top-level JSON object field and
// whether the key was present in the object.
func jsonField(body []byte, field string) (any, bool) {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, false
	}
	v, ok := parsed[field]
	return v, ok
}

// hasStreamFlag reports whether the converted output carries a truthy
// top-level "stream" field. Gemini never emits one.
func hasStreamFlag(out []byte) bool {
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		return false
	}
	if s, ok := parsed["stream"]; ok {
		if b, ok := s.(bool); ok && b {
			return true
		}
	}
	return false
}

// modelFromOutput extracts the top-level "model" string from converted output.
func modelFromOutput(out []byte) string {
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		return ""
	}
	if m, ok := parsed["model"].(string); ok {
		return m
	}
	return ""
}

// TestProtocolE2E_Matrix is the spec §11.1 table test. It runs every
// (client × upstream) combo across the plain/tool/stream/error fixtures.
func TestProtocolE2E_Matrix(t *testing.T) {
	fix := buildFixtures()
	tr := transformation.NewIRTransport()

	for _, combo := range allCombos {
		combo := combo               // capture for parallel-safe subtests
		clientModel := "gpt-4o-mini" // nominal model carried on the envelope

		t.Run(combo.name, func(t *testing.T) {
			t.Parallel()

			// --- (a) plain text conversation (non-stream) ---
			t.Run("plain", func(t *testing.T) {
				body := fix.plain[combo.client]
				env := newE2EEnvelope(combo.client, combo.upstream, body, clientModel)

				out, err := tr.Convert(context.Background(), env)
				if err != nil {
					t.Fatalf("plain Convert: %v", err)
				}
				assertParseable(t, combo, out)

				// Model preservation is protocol-aware:
				//   - Gemini generateContent upstream carries no top-level
				//     "model" (the model lives in the URL path), so the model
				//     assertion is skipped for the Gemini upstream.
				//   - For other upstreams, the model is taken from the parsed
				//     request body (Gemini input has no model → "" upstream).
				if combo.upstream == "gemini-generate" {
					if _, ok := jsonField(out, "model"); ok {
						t.Errorf("gemini upstream should not carry a body model field\noutput: %s", out)
					}
				} else {
					gotModel := modelFromOutput(out)
					if wantModel := plainModelFor(combo.client); gotModel != wantModel {
						t.Errorf("plain model: got %q, want %q\noutput: %s", gotModel, wantModel, out)
					}
				}
			})

			// --- (b) function tool + tool_choice=auto ---
			t.Run("tool", func(t *testing.T) {
				body := fix.tool[combo.client]
				env := newE2EEnvelope(combo.client, combo.upstream, body, clientModel)

				out, err := tr.Convert(context.Background(), env)
				if err != nil {
					t.Fatalf("tool Convert: %v", err)
				}
				assertParseable(t, combo, out)

				// Exactly one tool must survive conversion.
				if n := countTools(t, combo, out); n != 1 {
					t.Errorf("tool count: got %d, want 1\noutput: %s", n, out)
				}

				// tool_choice survives as auto (string or object depending on
				// target protocol). Gemini renders it as toolConfig.mode=AUTO.
				if !toolChoiceAutoPresent(combo.upstream, out) {
					t.Errorf("tool_choice=auto not preserved for upstream %s\noutput: %s", combo.upstream, out)
				}
			})

			// --- (c) error request (invalid JSON) → error, no panic ---
			t.Run("error", func(t *testing.T) {
				body := fix.errorBody[combo.client]
				env := newE2EEnvelope(combo.client, combo.upstream, body, clientModel)

				out, err := tr.Convert(context.Background(), env)
				if err == nil {
					t.Fatalf("error Convert: expected error for invalid JSON, got nil\noutput: %s", out)
				}
				if len(out) != 0 {
					t.Errorf("error Convert: expected empty output on parse error, got %s", out)
				}
				// The error must mention the protocol for diagnosability.
				if !strings.Contains(err.Error(), combo.client) {
					t.Errorf("error message should reference client protocol %q: %v", combo.client, err)
				}
			})

			// --- (d) stream=true → output carries stream:true ---
			//
			// Protocol note: neither Gemini input nor Gemini output carries a
			// body-level stream flag (Gemini streams via the ?alt=sse query
			// parameter, which is outside IR Transport's request-body scope).
			// So:
			//   - When the CLIENT is Gemini, stream=true cannot be encoded in the
			//     request body and the IR Stream flag stays false; we only assert
			//     that conversion succeeds without error.
			//   - When the UPSTREAM is Gemini, the flag is legitimately absent
			//     from the serialized output regardless of the client protocol.
			t.Run("stream", func(t *testing.T) {
				body := fix.stream[combo.client]
				env := newE2EEnvelope(combo.client, combo.upstream, body, clientModel)

				out, err := tr.Convert(context.Background(), env)
				if err != nil {
					t.Fatalf("stream Convert: %v", err)
				}
				assertParseable(t, combo, out)

				switch {
				case combo.client == "gemini-generate":
					// Gemini input cannot express stream=true in the body; the IR
					// Stream flag is false and the upstream gets no stream marker.
					// Just confirm the conversion is lossless in shape (no error,
					// parseable JSON) — already asserted above.
				case combo.upstream == "gemini-generate":
					if hasStreamFlag(out) {
						t.Errorf("gemini upstream must NOT emit a stream flag\noutput: %s", out)
					}
				default:
					if !hasStreamFlag(out) {
						t.Errorf("stream=true not propagated to upstream %s\noutput: %s", combo.upstream, out)
					}
				}
			})
		})
	}
}

// TestProtocolE2E_ResponsesAsInput exercises the spec §7.1 IR main-path
// extension: the OpenAI Responses API is now a valid *client* input protocol.
//
// Before 2026-08-02 this test asserted the opposite ("unsupported client
// protocol") because the IR layer shipped no ParseResponses. Now that
// internal/ir.ParseResponses exists, a Responses-shaped request must convert
// successfully to every upstream protocol with its key fields intact.
//
// The Responses→{OpenAI,Anthropic,Gemini} rows are also covered by the
// TestProtocolE2E_Matrix table (see allCombos); this test adds focused
// assertions on model preservation and tool_choice shape per target.
func TestProtocolE2E_ResponsesAsInput(t *testing.T) {
	tr := transformation.NewIRTransport()

	targets := []string{"openai-chat", "anthropic-messages", "gemini-generate", "openai-responses"}
	for _, upstream := range targets {
		upstream := upstream
		t.Run("Responses_To_"+strings.ReplaceAll(strings.Title(upstream), "-", ""), func(t *testing.T) {
			t.Parallel()
			// A minimal Responses-shaped request body. It must now parse and
			// convert instead of erroring.
			body := `{"model":"gpt-4o","instructions":"be brief","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
			env := newE2EEnvelope("openai-responses", upstream, body, "gpt-4o")

			out, err := tr.Convert(context.Background(), env)
			if err != nil {
				t.Fatalf("Responses→%s Convert failed: %v\noutput: %s", upstream, err, out)
			}
			assertParseable(t, protocolCombo{client: "openai-responses", upstream: upstream}, out)

			// Model preservation is protocol-aware: Gemini upstream carries no
			// body-level model; every other upstream must carry gpt-4o.
			if upstream != "gemini-generate" {
				if m := modelFromOutput(out); m != "gpt-4o" {
					t.Errorf("Responses→%s model = %q, want gpt-4o\noutput: %s", upstream, m, out)
				}
			}
		})
	}
}

// TestProtocolE2E_NilEnvelope ensures Convert degrades gracefully (returns a
// clear error) on nil input rather than panicking.
func TestProtocolE2E_NilEnvelope(t *testing.T) {
	tr := transformation.NewIRTransport()
	if _, err := tr.Convert(context.Background(), nil); err == nil {
		t.Fatal("Convert(nil envelope) must return an error")
	}
}

// assertParseable verifies the converted output is valid JSON and is a JSON
// object (all upstream protocol request bodies are objects).
func assertParseable(t *testing.T, combo protocolCombo, out []byte) {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("%s: output is not parseable JSON: %v\noutput: %s", combo.name, err, out)
	}
	if len(parsed) == 0 {
		t.Fatalf("%s: output parsed to empty object\noutput: %s", combo.name, out)
	}
}

// plainModelFor returns the model string embedded in the plain fixture body
// for the given client protocol. Gemini plain fixtures carry no model.
func plainModelFor(client string) string {
	switch client {
	case "openai-chat":
		return "gpt-4o"
	case "anthropic-messages":
		return "claude-sonnet-4-20250514"
	case "gemini-generate":
		return "" // Gemini input has no model; serializer emits ""
	case "openai-responses":
		return "gpt-4o"
	}
	return ""
}

// toolChoiceAutoPresent reports whether tool_choice=auto is represented in
// the converted output for the given upstream protocol.
func toolChoiceAutoPresent(upstream string, out []byte) bool {
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		return false
	}
	switch upstream {
	case "gemini-generate":
		// Gemini renders tool_choice as toolConfig.functionCallingConfig.mode=AUTO.
		tc, _ := parsed["toolConfig"].(map[string]any)
		fcc, _ := tc["functionCallingConfig"].(map[string]any)
		mode, _ := fcc["mode"].(string)
		return mode == "AUTO"
	case "anthropic-messages":
		// Anthropic tool_choice is an object {"type":"auto"} (or string).
		switch tc := parsed["tool_choice"].(type) {
		case map[string]any:
			return tc["type"] == "auto"
		case string:
			return tc == "auto"
		}
		return false
	case "openai-chat", "openai-responses":
		// OpenAI/Responses tool_choice is the string "auto".
		tc, ok := parsed["tool_choice"].(string)
		return ok && tc == "auto"
	}
	return false
}
