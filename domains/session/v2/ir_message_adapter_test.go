package v2

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// TestMessageToIR_StringContent verifies the v2→IR conversion collapses a
// non-empty string content into a single text content block (preserving the
// historical wire shape) and that ToolCallID/Name carry through unchanged.
func TestMessageToIR_StringContent(t *testing.T) {
	in := Message{
		Role:       "user",
		Content:    "hello",
		ToolCallID: "t1",
		Name:       "fn",
	}
	got := in.ToIR()
	if got.Role != "user" {
		t.Fatalf("role = %q, want user", got.Role)
	}
	if got.ToolCallID != "t1" || got.Name != "fn" {
		t.Fatalf("tool_call_id/name lost: %+v", got)
	}
	if len(got.Content) != 1 || got.Content[0].Type != "text" || got.Content[0].Text != "hello" {
		t.Fatalf("content blocks = %+v, want single text block 'hello'", got.Content)
	}
}

// TestMessageToIR_EmptyContent checks the empty-string branch produces no
// content blocks (so downstream sanitisation can drop empty messages).
func TestMessageToIR_EmptyContent(t *testing.T) {
	got := (Message{Role: "assistant"}).ToIR()
	if len(got.Content) != 0 {
		t.Fatalf("expected no content blocks for empty input, got %+v", got.Content)
	}
}

// TestMessageFromIR_TextOnly — the symmetric path: an IR Message whose
// content is a single text block must round-trip back to a v2.Message with
// the same string in `content` (so existing JSON rows stay byte-identical).
func TestMessageFromIR_TextOnly(t *testing.T) {
	in := ir.Message{
		Role:    "user",
		Content: []ir.ContentBlock{{Type: "text", Text: "hi"}},
	}
	got := MessageFromIR(in)
	if got.Content != "hi" {
		t.Fatalf("content = %q, want %q (must round-trip to legacy string form)", got.Content, "hi")
	}
	if got.Role != "user" {
		t.Fatalf("role lost: %q", got.Role)
	}
	if got.RawContent != nil {
		t.Fatalf("RawContent should be nil for text-only IR → v2, got %v", got.RawContent)
	}
}

// TestMessageFromIR_Multimodal — when the IR Message carries a non-text
// block the adapter stashes the payload via RawContent (json:"-") so the
// on-disk wire shape still produces valid v2 JSON but the IR payload is
// recoverable through the adapter.
func TestMessageFromIR_Multimodal(t *testing.T) {
	in := ir.Message{
		Role: "user",
		Content: []ir.ContentBlock{
			{Type: "text", Text: "what is in this image?"},
			{Type: "image", Image: &ir.ImageSource{
				Type:      "url",
				URL:       "https://example.com/cat.png",
				MediaType: "image/png",
			}},
		},
	}
	got := MessageFromIR(in)
	// Multimodal must NOT be flattened to a single string content.
	if got.Content != "" {
		t.Fatalf("content = %q, want empty for multimodal IR (use RawContent path)", got.Content)
	}
	if got.RawContent == nil {
		t.Fatalf("RawContent should carry the IR envelope, got nil")
	}
	raw, ok := got.RawContent.(json.RawMessage)
	if !ok {
		t.Fatalf("RawContent type = %T, want json.RawMessage", got.RawContent)
	}
	if !strings.Contains(string(raw), `"image"`) {
		t.Fatalf("envelope missing image block: %s", raw)
	}
}

// TestMessage_RoundTripIR — for multimodal/tool IR Messages the round-trip
// must be lossless; for text-only IR Messages the v2 Message carries the
// text in its `content` string (no RawContent), so the inverse check is
// `ToIR()`. The test asserts the symmetric invariant for every shape.
func TestMessage_RoundTripIR(t *testing.T) {
	cases := []ir.Message{
		// Pure text: v2.Message stores in `content` string; round-trip via
		// (Message).ToIR ↔ MessageFromIR must preserve the text block.
		{
			Role:    "user",
			Content: []ir.ContentBlock{{Type: "text", Text: "hi"}},
		},
		// Multimodal: v2.Message carries RawContent envelope; round-trip
		// via recoverIRRaw must preserve every block + image payload.
		{
			Role: "user",
			Content: []ir.ContentBlock{
				{Type: "text", Text: "look"},
				{Type: "image", Image: &ir.ImageSource{Type: "url", URL: "https://example.com/x.png"}},
			},
		},
		// Assistant with tool calls.
		{
			Role:    "assistant",
			Content: []ir.ContentBlock{{Type: "text", Text: ""}},
			ToolCalls: []ir.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "get_weather", Arguments: `{"city":"SF"}`},
			}},
		},
	}
	for i, in := range cases {
		v2Msg := MessageFromIR(in)

		var got ir.Message
		if raw, ok := v2Msg.RawContent.(json.RawMessage); ok && len(raw) > 0 {
			// Multimodal / tool-calls path uses the RawContent envelope.
			var ok2 bool
			got, ok2 = recoverIRRaw(v2Msg)
			if !ok2 {
				t.Fatalf("case %d: recoverIRRaw returned false; v2 message: %+v", i, v2Msg)
			}
		} else {
			// Text-only path: re-derive via ToIR (string content → text block).
			got = v2Msg.ToIR()
		}

		if got.Role != in.Role {
			t.Fatalf("case %d: role lost (got %q want %q)", i, got.Role, in.Role)
		}
		if len(got.Content) != len(in.Content) {
			t.Fatalf("case %d: content blocks lost (got %d want %d)", i, len(got.Content), len(in.Content))
		}
		if len(got.ToolCalls) != len(in.ToolCalls) {
			t.Fatalf("case %d: tool_calls lost (got %d want %d)", i, len(got.ToolCalls), len(in.ToolCalls))
		}
		if in.Content[0].Type == "text" && len(in.Content) > 1 && in.Content[1].Image != nil {
			if got.Content[1].Image == nil || got.Content[1].Image.URL != in.Content[1].Image.URL {
				t.Fatalf("case %d: image URL lost (got %+v want %+v)",
					i, got.Content[1].Image, in.Content[1].Image)
			}
		}
	}
}

// TestIRMessagesFromJSON_ObjectForm — parses the canonical
// {"messages":[{...}]} shape produced by request_logs RequestBody.
func TestIRMessagesFromJSON_ObjectForm(t *testing.T) {
	raw := json.RawMessage(`{"model":"gpt-4o","messages":[
		{"role":"system","content":"be brief"},
		{"role":"user","content":"hi"}
	]}`)
	got := IRMessagesFromJSON(raw)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Role != "system" {
		t.Fatalf("messages[0].role = %q", got[0].Role)
	}
	if len(got[0].Content) != 1 || got[0].Content[0].Text != "be brief" {
		t.Fatalf("messages[0].content = %+v", got[0].Content)
	}
	if got[1].Role != "user" || got[1].Content[0].Text != "hi" {
		t.Fatalf("messages[1] = %+v", got[1])
	}
}

// TestIRMessagesFromJSON_BareArray — the legacy bare-array shape emitted by
// some clients/tests directly as [{role,content}, ...].
func TestIRMessagesFromJSON_BareArray(t *testing.T) {
	raw := json.RawMessage(`[{"role":"assistant","content":"hi"}]`)
	got := IRMessagesFromJSON(raw)
	if len(got) != 1 || got[0].Role != "assistant" {
		t.Fatalf("got = %+v", got)
	}
}

// TestIRMessagesFromJSON_MultimodalArray — when the wire carries an array
// of content blocks (OpenAI Chat multimodal), the reader must materialise
// each block into its IR ContentBlock representation.
func TestIRMessagesFromJSON_MultimodalArray(t *testing.T) {
	raw := json.RawMessage(`{"messages":[
		{"role":"user","content":[
			{"type":"text","text":"describe"},
			{"type":"image_url","image_url":{"url":"https://example.com/a.png","detail":"high"}}
		]}
	]}`)
	got := IRMessagesFromJSON(raw)
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if len(got[0].Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(got[0].Content))
	}
	if got[0].Content[0].Type != "text" {
		t.Fatalf("first block type = %q", got[0].Content[0].Type)
	}
	if got[0].Content[1].Image == nil {
		t.Fatalf("second block missing Image: %+v", got[0].Content[1])
	}
}

// TestIRMessagesFromJSON_IREnvelope verifies that messages already in IR
// shape (the dual-shape envelope) are recognised and reconstructed
// losslessly.
func TestIRMessagesFromJSON_IREnvelope(t *testing.T) {
	// Start with a multimodal IR message.
	original := ir.Message{
		Role: "user",
		Content: []ir.ContentBlock{
			{Type: "text", Text: "look"},
			{Type: "image", Image: &ir.ImageSource{Type: "url", URL: "https://example.com/x.png"}},
		},
	}
	v2Msg := MessageFromIR(original)
	raw, ok := v2Msg.RawContent.(json.RawMessage)
	if !ok {
		t.Fatalf("RawContent type = %T", v2Msg.RawContent)
	}
	got := IRMessagesFromJSON(raw)
	if len(got) != 1 {
		t.Fatalf("expected 1 IR message back, got %d", len(got))
	}
	if got[0].Role != "user" {
		t.Fatalf("role lost: %q", got[0].Role)
	}
	if len(got[0].Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(got[0].Content))
	}
	if got[0].Content[1].Image == nil {
		t.Fatalf("image payload lost in envelope round-trip")
	}
}

// TestIRMessagesFromJSON_EmptyAndMalformed — nil / empty / invalid input
// must yield nil messages without panicking. The wire-format reader relies
// on this to skip malformed bodies cleanly.
func TestIRMessagesFromJSON_EmptyAndMalformed(t *testing.T) {
	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"nil", nil},
		{"empty", json.RawMessage("")},
		{"whitespace", json.RawMessage("   ")},
		{"non-json", json.RawMessage("not a json body")},
		{"empty object", json.RawMessage("{}")},
		{"empty array", json.RawMessage("[]")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IRMessagesFromJSON(c.raw); got != nil {
				t.Fatalf("expected nil for %s, got %+v", c.name, got)
			}
		})
	}
}

// TestIRMessagesFromJSON_BOMStripped — wire bodies from clients that prepend
// a UTF-8 BOM must parse without error (json.Unmarshal rejects BOM).
func TestIRMessagesFromJSON_BOMStripped(t *testing.T) {
	raw := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"messages":[{"role":"user","content":"hi"}]}`)...)
	got := IRMessagesFromJSON(raw)
	if len(got) != 1 || got[0].Content[0].Text != "hi" {
		t.Fatalf("got = %+v", got)
	}
}

// TestSafeJSONMarshal_BackwardCompat — adding RawContent (json:"-") to the
// Message struct must NOT change the wire JSON for the legacy fields. This
// is the regression guard for "byte-identical" on-disk rows.
func TestSafeJSONMarshal_BackwardCompat(t *testing.T) {
	m := Message{
		Role:       "user",
		Content:    "hi",
		ToolCallID: "",
		Name:       "",
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"role":"user","content":"hi"}`
	if string(b) != want {
		t.Fatalf("got %s, want %s (RawContent must not leak into JSON)", b, want)
	}
}
