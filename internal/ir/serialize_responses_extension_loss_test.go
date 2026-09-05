package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSerializeResponsesRequest_ExtensionLoss documents which Responses request
// fields round-trip through SerializeResponsesRequest and which are dropped
// or moved to Extensions.
//
// Pinning matrix (top-level Responses fields as of 2026-08-30):
//
//	┌──────────────────────────────┬──────────────────────────┬───────────────────┐
//	│ Field                        │ Status                   │ Sink              │
//	├──────────────────────────────┼──────────────────────────┼───────────────────┤
//	│ model                        │ preserved                │ out["model"]      │
//	│ instructions                 │ preserved (from System)  │ out["instructions"]│
//	│ input[]                      │ preserved (from Messages)│ out["input"]      │
//	│ max_output_tokens            │ preserved (from MaxTokens)│ out["max_output_tokens"]│
//	│ temperature                  │ preserved (ptr)          │ out["temperature"]│
//	│ top_p                        │ preserved (ptr)          │ out["top_p"]      │
//	│ stream                       │ preserved                │ out["stream"]     │
//	│ stop                         │ preserved                │ out["stop"]       │
//	│ tools[]                      │ preserved                │ out["tools"]      │
//	│ tool_choice                  │ preserved                │ out["tool_choice"]│
//	│ parallel_tool_calls          │ preserved (ptr)          │ out["parallel_tool_calls"]│
//	│ previous_response_id         │ preserved                │ out["previous_response_id"]│
//	│ prompt_cache_key             │ preserved                │ out["prompt_cache_key"]│
//	│ metadata                     │ preserved                │ out["metadata"]   │
//	│ user                         │ preserved                │ out["user"]       │
//	│ store                        │ preserved (ptr)          │ out["store"]      │
//	│ truncation                   │ preserved                │ out["truncation"] │
//	│ reasoning                    │ preserved (structured)   │ out["reasoning"]  │
//	│ text.format                  │ preserved (from RespFmt) │ out["text"]       │
//	│ service_tier                 │ preserved                │ out["service_tier"]│
//	│ safety_identifier            │ preserved                │ out["safety_identifier"]│
//	│ x_test_extension (unknown)   │ routed to Extensions     │ restoreExtensions()│
//	│ unknown_top_level (generic)  │ routed to Extensions     │ restoreExtensions()│
//	└──────────────────────────────┴──────────────────────────┴───────────────────┘
//
// If a future OpenAI Responses field is added above and SerializeResponsesRequest
// does not emit it natively, this test will surface the regression by failing
// on the `want` list — update the matrix in the comment at the same time.
func TestSerializeResponsesRequest_ExtensionLoss(t *testing.T) {
	req := &InternalRequest{
		Model:    "gpt-4o",
		Stream:   true,
		MaxTokens: 1024,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
		// Source protocol must be Responses for restoreExtensions() to honor Extensions.
		// We also seed the explicit typed fields so the test does not depend on
		// round-tripping Extensions for preserved fields.
		SourceProtocol: ProtocolOpenAIResponses,
		PreviousResponseID: "resp_abc",
		PromptCacheKey:     "cache_xyz",
		SafetyIdentifier:   "user_safe_1",
		Store:              ptrBool(true),
		Truncation:         "auto",
		User:               "u_test",
		// Extensions: the unknown / future Responses fields. The transport
		// extractor would normally populate these; here we seed them directly.
		Extensions: map[string]json.RawMessage{
			"x_test_extension": json.RawMessage(`"hello"`),
			"future_field":     json.RawMessage(`{"nested":true,"n":42}`),
		},
		// Tools + tool choice so the typed branches fire.
		Tools: []ToolDefinition{{
			Type:        "function",
			Name:        "search",
			Description: "search the web",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
		ToolChoice:  &ToolChoice{Type: "auto"},
		ServiceTier: "auto",
	}
	// Temperature and TopP must be non-nil to emit.
	t1, tp := 0.7, 0.9
	req.Temperature = &t1
	req.TopP = &tp
	// Metadata so the metadata branch is exercised.
	req.Metadata = &Metadata{UserID: "u_123"}
	// Reasoning effort (o-series).
	req.Reasoning = &ReasoningConfig{Effort: "high"}
	// ResponseFormat so text.format is exercised.
	req.ResponseFormat = &ResponseFormat{Type: "json_object"}

	body, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatalf("SerializeResponsesRequest failed: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	// === preserved (only fields actually set on the request) ===
	preservedKeys := []string{
		"model", "input", "max_output_tokens", "temperature", "top_p",
		"stream", "tools", "tool_choice", "previous_response_id",
		"prompt_cache_key", "metadata", "user", "store", "truncation",
		"reasoning", "text", "service_tier", "safety_identifier",
	}
	for _, k := range preservedKeys {
		if _, ok := out[k]; !ok {
			t.Errorf("preserved key %q missing in output", k)
		}
	}

	// === preserved-but-not-emitted (typed-only) ===
	// parallel_tool_calls is conditional on req.ParallelToolCalls being non-nil.
	// This test exercises the default-zero branch on purpose: when the
	// caller does not set it, the field is NOT in the wire output and that
	// is intentional (avoid noise). We assert absence here to pin behavior.
	if _, ok := out["parallel_tool_calls"]; ok {
		t.Errorf("parallel_tool_calls unexpectedly present (zero-value should be omitted)")
	}

	// === extension restoration ===
	// restoreExtensions() must write the unknown fields back to the top-level map.
	xt, ok := out["x_test_extension"]
	if !ok {
		t.Fatalf("Extensions field x_test_extension was not restored to output")
	}
	if xt != "hello" {
		t.Errorf("x_test_extension = %v, want hello", xt)
	}
	ff, ok := out["future_field"]
	if !ok {
		t.Fatalf("Extensions field future_field was not restored to output")
	}
	ffmap := ff.(map[string]any)
	if ffmap["nested"] != true {
		t.Errorf("future_field.nested = %v, want true", ffmap["nested"])
	}
	if ffmap["n"].(float64) != 42 {
		t.Errorf("future_field.n = %v, want 42", ffmap["n"])
	}

	// === no panic on known extensions that overlap with typed fields ===
	// If a future field name collides with a typed field, the typed emission
	// takes precedence and the Extensions entry is ignored. Pin that here so
	// nobody accidentally flips the precedence.
	reqOverlap := &InternalRequest{
		Model:          "gpt-4o",
		Messages:       []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		SourceProtocol: ProtocolOpenAIResponses,
		Store:          ptrBool(true),
		Extensions: map[string]json.RawMessage{
			"store": json.RawMessage(`false`), // collision with typed field
		},
	}
	bodyOverlap, err := SerializeResponsesRequest(reqOverlap)
	if err != nil {
		t.Fatalf("SerializeResponsesRequest overlap failed: %v", err)
	}
	var outOverlap map[string]any
	_ = json.Unmarshal(bodyOverlap, &outOverlap)
	if outOverlap["store"] != true {
		t.Errorf("typed field precedence broken: store = %v, want true", outOverlap["store"])
	}
}

// TestSerializeResponsesRequest_NativeVsCrossProtocol documents that the
// Extensions restore path is now registry-driven (see extensions_restore.go):
// unknown / non-dialect-scoped fields round-trip regardless of source protocol.
// This was a deliberate 2026-08-11 fix that replaced the old
// "source protocol == target protocol" gate which was too strict for
// cross-protocol forwarding (e.g. Claude Code Anthropic → DeepSeek OpenAI
// where client_extra_body fields must survive).
//
// What IS lost cross-protocol:
//   - Dialect-private fields (e.g. Anthropic context_management) are
//     dropped via paramreg.ActionDrop and emit an ir_protocol_loss anomaly.
//   - Hard-rejected fields (per paramreg spec.RejectedBy) are clipped.
//
// This test pins the unknown-field-restore behavior so a future contributor
// does not silently re-add the strict gate. To assert the dialect-private
// drop, see the paramregistry tests.
func TestSerializeResponsesRequest_NativeVsCrossProtocol(t *testing.T) {
	reqNative := &InternalRequest{
		Model:          "gpt-4o",
		Messages:       []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		SourceProtocol: ProtocolOpenAIResponses,
		Extensions: map[string]json.RawMessage{
			"x_test_extension": json.RawMessage(`"native"`),
		},
	}
	body, err := SerializeResponsesRequest(reqNative)
	if err != nil {
		t.Fatalf("SerializeResponsesRequest native failed: %v", err)
	}
	if !strings.Contains(string(body), `"x_test_extension":"native"`) {
		t.Errorf("native protocol: x_test_extension not restored, body=%s", string(body))
	}

	reqCross := &InternalRequest{
		Model:          "gpt-4o",
		Messages:       []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		SourceProtocol: ProtocolAnthropicMessages, // different protocol
		Extensions: map[string]json.RawMessage{
			"x_test_extension": json.RawMessage(`"cross"`),
		},
	}
	bodyCross, err := SerializeResponsesRequest(reqCross)
	if err != nil {
		t.Fatalf("SerializeResponsesRequest cross failed: %v", err)
	}
	// Unknown / non-dialect fields now round-trip cross-protocol by design.
	if !strings.Contains(string(bodyCross), `"x_test_extension":"cross"`) {
		t.Errorf("cross protocol: unknown field should still round-trip via registry, body=%s", string(bodyCross))
	}
}

func ptrBool(b bool) *bool { return &b }
