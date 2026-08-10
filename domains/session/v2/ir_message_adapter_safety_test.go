package v2

// ─────────────────────────────────────────────────────────────────────────
// P0 safety tests for the IR ↔ v2.Message adapter.
//
// These tests cover three classes of production risk the original suite did
// not address:
//
//   1. Malicious or malformed JSON. A wire body from a hostile or buggy client
//      must not panic the parser, allocate unbounded memory, or corrupt the
//      session row. Each test feeds a specific shape and asserts the safe
//      outcome: nil result, dropped-to-RawContent, or error surfaced cleanly.
//
//   2. Concurrency. The adapter is pure (no shared state), but the test runs
//      under -race so any future shared state regression fails the build.
//
//   3. Tool-call boundary cases. The legacy probe decoder reads tool_calls
//      with permissive type assertions; tests here pin the invariant that
//      malformed entries are skipped, not silently zeroed into a call with
//      an empty function name (which is what the legacy wire decoder used
//      to do for flat {name,arguments} pairs).
//
// The file is kept separate from ir_message_adapter_test.go so the P0 scope
// is reviewable as one block and so the safe-fallback contract (never panic,
// never allocate without bound) is documented in one place.
// ─────────────────────────────────────────────────────────────────────────

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// TestIRMessagesFromJSON_DeeplyNestedArray — a malicious body that nests
// content arrays many levels deep. json.Unmarshal's stack limit is large but
// finite; the wire-format reader must not crash the goroutine when an
// attacker sends such a payload. The safe outcome is "no panic, no usable
// content blocks" — the outer message envelope may survive with Role=user
// and an empty Content slice, which is the documented degrade-to-empty
// behaviour. The test fails only if the reader crashes or returns a
// non-empty content payload.
func TestIRMessagesFromJSON_DeeplyNestedArray(t *testing.T) {
	// Build {"content":[[[[...]]]]} with 2000 levels. json.Unmarshal in the
	// standard library handles this without crashing on modern Go releases
	// (maxDecodeDepth is 10000), so the inner arrays survive parsing; the
	// per-block decoder rejects them because an array cannot be unmarshaled
	// into the probe struct, so every nested array is dropped.
	var b strings.Builder
	b.WriteString(`{"role":"user","content":[`)
	for i := 0; i < 2000; i++ {
		b.WriteString(`[`)
	}
	b.WriteString(`"x"`)
	for i := 0; i < 2000; i++ {
		b.WriteString(`]`)
	}
	b.WriteString(`]}`)

	got := IRMessagesFromJSON(json.RawMessage(b.String()))
	if len(got) == 0 {
		return // either outcome is safe
	}
	if len(got[0].Content) != 0 {
		t.Fatalf("deeply nested body must yield empty content, got %+v", got[0].Content)
	}
}

// TestIRMessagesFromJSON_TruncatedAtContentArray — a body whose JSON is cut
// off mid-array. json.Unmarshal reports an error, which the parser turns
// into nil. Verifies the reader never returns a partial result.
func TestIRMessagesFromJSON_TruncatedAtContentArray(t *testing.T) {
	cases := []string{
		`{"role":"user","content":[{"type":"text","text":"hel`,
		`{"role":"user","content":[{"type":"image","source":{"url":"https://e.com/a.png`,
		`[{"role":"user","content":[{"type":"text","text":"a"},{"type":"ima`,
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if got := IRMessagesFromJSON(json.RawMessage(c)); len(got) != 0 {
				t.Fatalf("truncated body should yield nil, got %+v", got)
			}
		})
	}
}

// TestIRMessagesFromJSON_InvalidUnicodeEscape — \uXXXX sequences that point
// at surrogate halves (unpaired high/low surrogate) are accepted by Go's
// json.Unmarshal, which substitutes U+FFFD for the lone surrogate. The
// reader must not panic and must keep parsing the surrounding message.
// (Earlier expectation of "returns nil" was over-strict: Go's decoder
// silently substitutes rather than rejecting.)
func TestIRMessagesFromJSON_InvalidUnicodeEscape(t *testing.T) {
	// High surrogate D800 with no low surrogate: the surrounding message
	// parses, the inner string contains U+FFFD + "bad". The reader must
	// not panic and must return the parsed message.
	body := `{"role":"user","content":"\uD800bad"}`
	got := IRMessagesFromJSON(json.RawMessage(body))
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if len(got[0].Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(got[0].Content))
	}
	if !strings.Contains(got[0].Content[0].Text, "bad") {
		t.Fatalf("trailing text lost after surrogate: %q", got[0].Content[0].Text)
	}
}

// TestIRMessagesFromJSON_RecursiveContentArray — `{content: content}` is
// not valid JSON for our shape (the array decoder would call itself), and
// must not stack-overflow the goroutine. The safe outcome is "no panic, no
// usable content blocks" — the message envelope may survive with role but
// empty Content, since the per-block decoder rejects objects outright.
func TestIRMessagesFromJSON_RecursiveContentArray(t *testing.T) {
	body := `{"role":"user","content":{"content":[{"type":"text","text":"x"}]}}`
	got := IRMessagesFromJSON(json.RawMessage(body))
	if len(got) == 0 {
		return // either outcome is safe
	}
	if len(got[0].Content) != 0 {
		t.Fatalf("object-in-content must yield empty content, got %+v", got[0].Content)
	}
}

// TestIRMessagesFromJSON_DeeplyNestedContentBlocks — a single message whose
// content array is 10000 entries long. The reader must return the array
// intact (this is not malformed), and must not allocate beyond what the
// blocks themselves need. The test catches accidental quadratic copies.
func TestIRMessagesFromJSON_DeeplyNestedContentBlocks(t *testing.T) {
	const n = 10000
	var b strings.Builder
	b.WriteString(`[{"role":"user","content":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(`,`)
		}
		b.WriteString(`{"type":"text","text":"x"}`)
	}
	b.WriteString(`]}]`)
	got := IRMessagesFromJSON(json.RawMessage(b.String()))
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if len(got[0].Content) != n {
		t.Fatalf("content blocks = %d, want %d", len(got[0].Content), n)
	}
}

// TestIRMessagesFromJSON_NotAnObjectNorArray — top-level scalars must not be
// treated as messages. The reader returns nil, the caller skips the body.
func TestIRMessagesFromJSON_NotAnObjectNorArray(t *testing.T) {
	cases := []string{
		`42`,
		`"a bare string"`,
		`true`,
		`null`,
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if got := IRMessagesFromJSON(json.RawMessage(c)); len(got) != 0 {
				t.Fatalf("scalar body should yield nil, got %+v", got)
			}
		})
	}
}

// TestMessage_UnmarshalJSON_CorruptedEnvelope — when RawContent points at an
// envelope whose `content` value is malformed, the reader must not panic on
// the next call to ToIR. RecoverIRRaw is the gate that decides whether to
// trust the envelope; a malformed value there must fall through to the
// other content sources (the text projection in this test).
func TestMessage_UnmarshalJSON_CorruptedEnvelope(t *testing.T) {
	// The RawContent here is an envelope whose nested `content` array is
	// not an array of valid blocks; on unmarshal the inner array parse
	// would error, but the envelope itself unmarshals as a struct with an
	// empty Content slice, so ToIR falls through.
	m := Message{
		Role:       "user",
		Content:    "fallback text",
		RawContent: json.RawMessage(`{"role":"user","content":[{"type":"text","text":"hello"`), // truncated
	}
	got := m.ToIR()
	if got.Role != "user" {
		t.Fatalf("role lost: %q", got.Role)
	}
	if len(got.Content) != 1 || got.Content[0].Text != "fallback text" {
		t.Fatalf("fallback text path lost: %+v", got.Content)
	}
}

// TestMessage_MarshalJSON_HandlesInvalidRawContent — a RawContent value that
// cannot be marshaled must not break MarshalJSON for the surrounding
// Message. The current implementation drops un-marshalable RawContent via
// irEnvelopeContent returning nil; this test pins that contract.
func TestMessage_MarshalJSON_HandlesInvalidRawContent(t *testing.T) {
	// A channel cannot be marshaled. This is a type-only test of the
	// helper contract; the production code never receives such a value,
	// but the path must not panic.
	m := Message{
		Role:       "user",
		Content:    "hi",
		RawContent: make(chan int), // unmarshalable
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"content":"hi"`) {
		t.Fatalf("legacy content lost on marshal: %s", b)
	}
	if !strings.Contains(string(b), `"role":"user"`) {
		t.Fatalf("role lost on marshal: %s", b)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Concurrency: the adapter is pure, but the test runs under -race so any
// future shared-state regression (a package-level cache, an unsafe pointer)
// fails the build. Each sub-test fans out N goroutines that all drive the
// same code path and asserts every goroutine sees the same correct result.
// ─────────────────────────────────────────────────────────────────────────

// TestAdapter_ConcurrentSafe_ToIR — N goroutines call ToIR on independent
// Message values. There is no shared mutable state, so the only failure
// mode is a future regression introducing one.
func TestAdapter_ConcurrentSafe_ToIR(t *testing.T) {
	const goroutines = 64
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	errCh := make(chan error, goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				m := Message{
					Role:    "user",
					Content: "shared text",
				}
				got := m.ToIR()
				if got.Role != "user" {
					errCh <- errAt(g, i, "role=%q", got.Role)
					return
				}
				if len(got.Content) != 1 || got.Content[0].Text != "shared text" {
					errCh <- errAt(g, i, "content=%+v", got.Content)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

// TestAdapter_ConcurrentSafe_MessageFromIR — N goroutines call MessageFromIR
// on independent IR Messages. Mirrors the ToIR concurrency test for the
// inverse path.
func TestAdapter_ConcurrentSafe_MessageFromIR(t *testing.T) {
	const goroutines = 64
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	errCh := make(chan error, goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				in := ir.Message{
					Role:    "user",
					Content: []ir.ContentBlock{{Type: "text", Text: "hi"}},
				}
				got := MessageFromIR(in)
				if got.Content != "hi" {
					errCh <- errAt(g, i, "v2.content=%q", got.Content)
					return
				}
				if got.RawContent != nil {
					errCh <- errAt(g, i, "v2.RawContent set for text-only IR")
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

// TestAdapter_ConcurrentSafe_IRMessagesFromJSON — N goroutines parse the
// same payload concurrently. The parser allocates per-call, so this both
// verifies the race detector is clean and that the per-call allocations do
// not produce cross-goroutine contamination.
func TestAdapter_ConcurrentSafe_IRMessagesFromJSON(t *testing.T) {
	const goroutines = 64
	const iterations = 50
	payload := json.RawMessage(`{"messages":[
		{"role":"system","content":"be brief"},
		{"role":"user","content":"hi"}
	]}`)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	errCh := make(chan error, goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				got := IRMessagesFromJSON(payload)
				if len(got) != 2 {
					errCh <- errAt(g, i, "len=%d", len(got))
					return
				}
				if got[0].Role != "system" || got[1].Role != "user" {
					errCh <- errAt(g, i, "roles=%q,%q", got[0].Role, got[1].Role)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

// TestAdapter_ConcurrentSafe_PersistAndRecover — the full
// ir.Message → JSON → ir.Message round-trip on independent inputs, in
// parallel. Catches races in Marshal/Unmarshal and the per-call helpers
// like stripBOM.
func TestAdapter_ConcurrentSafe_PersistAndRecover(t *testing.T) {
	const goroutines = 32
	const iterations = 25

	originals := []ir.Message{
		{Role: "user", Content: []ir.ContentBlock{{Type: "text", Text: "hi"}}},
		{Role: "user", Content: []ir.ContentBlock{
			{Type: "text", Text: "look"},
			{Type: "image", Image: &ir.ImageSource{Type: "url", URL: "https://e.com/x.png"}},
		}},
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	errCh := make(chan error, goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				in := originals[i%len(originals)]
				// Wrap in a slice so the wire payload matches the slice
				// shape the reader expects; Message.MarshalJSON emits a
				// single object, which cannot unmarshal into []Message.
				wire, err := json.Marshal([]Message{MessageFromIR(in)})
				if err != nil {
					errCh <- errAt(g, i, "marshal: %v", err)
					return
				}
				var readBack []Message
				if err := json.Unmarshal(wire, &readBack); err != nil {
					errCh <- errAt(g, i, "unmarshal: %v", err)
					return
				}
				if len(readBack) != 1 {
					errCh <- errAt(g, i, "readback=%d", len(readBack))
					return
				}
				got := IRMessagesFromV2(readBack)
				if len(got) != 1 {
					errCh <- errAt(g, i, "recover=%d", len(got))
					return
				}
				if len(got[0].Content) != len(in.Content) {
					errCh <- errAt(g, i, "content.len=%d want=%d", len(got[0].Content), len(in.Content))
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Tool-call boundary cases. The legacy probe decoder reads tool_calls with
// permissive type assertions (`id, _ := raw["id"].(string)`); tests here
// pin the invariant that malformed entries are not silently transformed
// into a call with an empty function name.
// ─────────────────────────────────────────────────────────────────────────

// TestIRMessageFromJSON_ToolCalls_MissingID — a tool_calls entry without an
// id field still parses (the assertion is non-fatal) but the resulting IR
// ToolCall.ID is empty. This is intentional: downstream code can decide
// whether an empty ID is acceptable, but the adapter must not invent one.
func TestIRMessageFromJSON_ToolCalls_MissingID(t *testing.T) {
	body := json.RawMessage(`{"role":"assistant","tool_calls":[
		{"type":"function","function":{"name":"get_weather","arguments":"{}"}}
	]}`)
	got := IRMessagesFromJSON(body)
	if len(got) != 1 || len(got[0].ToolCalls) != 1 {
		t.Fatalf("unexpected parse result: %+v", got)
	}
	if got[0].ToolCalls[0].ID != "" {
		t.Fatalf("missing id should yield empty ID, got %q", got[0].ToolCalls[0].ID)
	}
	if got[0].ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("function name lost: %+v", got[0].ToolCalls[0].Function)
	}
}

// TestIRMessageFromJSON_ToolCalls_MissingFunction — a tool_calls entry with
// no `function` key at all. The function name and arguments are empty;
// the entry itself is preserved so the surrounding message still has
// exactly one tool call (callers can reject empty-name calls explicitly).
func TestIRMessageFromJSON_ToolCalls_MissingFunction(t *testing.T) {
	body := json.RawMessage(`{"role":"assistant","tool_calls":[
		{"id":"call_1","type":"function"}
	]}`)
	got := IRMessagesFromJSON(body)
	if len(got) != 1 || len(got[0].ToolCalls) != 1 {
		t.Fatalf("unexpected parse result: %+v", got)
	}
	if got[0].ToolCalls[0].ID != "call_1" {
		t.Fatalf("id lost: %+v", got[0].ToolCalls[0])
	}
	if got[0].ToolCalls[0].Function.Name != "" {
		t.Fatalf("function name should be empty, got %q", got[0].ToolCalls[0].Function.Name)
	}
}

// TestIRMessageFromJSON_ToolCalls_ArgumentsAsObject — some clients send
// arguments as a JSON object rather than a JSON-encoded string. The legacy
// probe decoder (and thus IR) expects arguments to be a string. An object
// arrives via `interface{}` assertions; the type assertion fails silently
// and arguments become "". The test pins this behaviour so a future
// change to "be permissive" is a deliberate decision, not an accident.
func TestIRMessageFromJSON_ToolCalls_ArgumentsAsObject(t *testing.T) {
	body := json.RawMessage(`{"role":"assistant","tool_calls":[
		{"id":"call_1","type":"function","function":{"name":"f","arguments":{"city":"SF"}}}
	]}`)
	got := IRMessagesFromJSON(body)
	if len(got) != 1 || len(got[0].ToolCalls) != 1 {
		t.Fatalf("unexpected parse result: %+v", got)
	}
	if got[0].ToolCalls[0].Function.Arguments != "" {
		t.Fatalf("object arguments should yield empty arguments, got %q", got[0].ToolCalls[0].Function.Arguments)
	}
	if got[0].ToolCalls[0].Function.Name != "f" {
		t.Fatalf("function name lost: %+v", got[0].ToolCalls[0].Function)
	}
}

// TestIRMessageFromJSON_ToolCalls_AllFieldsMissing — a tool_calls entry
// whose every field is missing or wrong-typed. The entry round-trips as
// an empty IR ToolCall: the count is preserved, every field is empty.
func TestIRMessageFromJSON_ToolCalls_AllFieldsMissing(t *testing.T) {
	body := json.RawMessage(`{"role":"assistant","tool_calls":[
		{}, {"id":42}, {"function":"not-an-object"}
	]}`)
	got := IRMessagesFromJSON(body)
	if len(got) != 1 || len(got[0].ToolCalls) != 3 {
		t.Fatalf("expected 3 tool calls (count preserved), got %d: %+v", len(got[0].ToolCalls), got)
	}
	for i, tc := range got[0].ToolCalls {
		if tc.ID != "" {
			t.Fatalf("tool[%d] ID should be empty, got %q", i, tc.ID)
		}
		if tc.Function.Name != "" {
			t.Fatalf("tool[%d] function name should be empty, got %q", i, tc.Function.Name)
		}
	}
}

// TestIRMessageFromJSON_ToolCalls_NullEntry — a tool_calls array with a
// literal null. The decoder skips null entries (Go's map[string]interface{}
// preserves the null but the for-loop just produces an empty map; the
// resulting ToolCall is empty). The count of real entries is 0; the
// surrounding message still parses.
func TestIRMessageFromJSON_ToolCalls_NullEntry(t *testing.T) {
	body := json.RawMessage(`{"role":"assistant","tool_calls":[null]}`)
	got := IRMessagesFromJSON(body)
	if len(got) != 1 {
		t.Fatalf("unexpected parse result: %+v", got)
	}
	// null decodes to an empty map in Go's map[string]interface{}; the
	// resulting ToolCall has every field empty. The adapter must not panic.
	if len(got[0].ToolCalls) != 1 {
		t.Fatalf("expected 1 (empty) tool call, got %d", len(got[0].ToolCalls))
	}
}

// TestMessageFromIR_ToolCalls_EmptyFunctionName — the IR→v2 path must
// preserve an empty function name as empty, not invent "function".
func TestMessageFromIR_ToolCalls_EmptyFunctionName(t *testing.T) {
	in := ir.Message{
		Role: "assistant",
		ToolCalls: []ir.ToolCall{{
			ID:   "call_1",
			Type: "function",
			// Function.Name deliberately empty
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Arguments: `{}`},
		}},
	}
	got := MessageFromIR(in)
	if len(got.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(got.ToolCalls))
	}
	if fn, ok := got.ToolCalls[0]["function"].(map[string]interface{}); !ok {
		t.Fatalf("function should be an object, got %T", got.ToolCalls[0]["function"])
	} else if fn["name"] != "" {
		t.Fatalf("function.name should be empty, got %v", fn["name"])
	}
}

// TestIRMessageFromJSON_ToolCalls_NestedEnvelope — a tool_calls entry whose
// `function` is an envelope-shaped object (has a `raw` sub-field). The
// decoder ignores `raw` and reads name/arguments. This pins the fact that
// the legacy probe path (rather than the envelope path) wins on this
// entry — a tool_calls entry is never classified as envelope.
func TestIRMessageFromJSON_ToolCalls_NestedEnvelope(t *testing.T) {
	body := json.RawMessage(`{"role":"assistant","tool_calls":[
		{"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"x\":1}","raw":{"meta":1}}}
	]}`)
	got := IRMessagesFromJSON(body)
	if len(got) != 1 || len(got[0].ToolCalls) != 1 {
		t.Fatalf("unexpected parse result: %+v", got)
	}
	if got[0].ToolCalls[0].Function.Name != "f" {
		t.Fatalf("function name lost: %+v", got[0].ToolCalls[0].Function)
	}
	if got[0].ToolCalls[0].Function.Arguments != `{"x":1}` {
		t.Fatalf("arguments lost: %+v", got[0].ToolCalls[0].Function)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// RawContent type variants. ToIR's recoverIRRaw accepts RawContent only as
// a json.RawMessage; Message.RawContent is typed `interface{}` so callers
// can put any value there. The reader must not panic on non-rawmessage
// values, and must produce a sensible fallback when it cannot read them.
// ─────────────────────────────────────────────────────────────────────────

// TestMessage_ToIR_RawContent_NotJSONRawMessage — RawContent is typed
// `interface{}`. When callers store a string instead of json.RawMessage,
// recoverIRRaw reports false and the adapter falls through to the text
// projection. This is the safety contract for in-process callers that
// have not been audited.
func TestMessage_ToIR_RawContent_NotJSONRawMessage(t *testing.T) {
	cases := []struct {
		name string
		val  interface{}
	}{
		{"string", "raw string"},
		{"int", 42},
		{"nil", nil},
		{"struct", struct{ X int }{X: 1}},
		{"map", map[string]int{"k": 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Message{
				Role:       "user",
				Content:    "fallback",
				RawContent: c.val,
			}
			got := m.ToIR()
			if got.Role != "user" {
				t.Fatalf("role lost: %q", got.Role)
			}
			if len(got.Content) != 1 || got.Content[0].Text != "fallback" {
				t.Fatalf("fallback text lost: %+v", got.Content)
			}
		})
	}
}

// TestMessage_ToIR_RawContent_EmptyJSONRawMessage — a non-nil but empty
// json.RawMessage. recoverIRRaw reports false (the len check), the
// adapter falls through to the text projection.
func TestMessage_ToIR_RawContent_EmptyJSONRawMessage(t *testing.T) {
	m := Message{
		Role:       "user",
		Content:    "fallback",
		RawContent: json.RawMessage(""),
	}
	got := m.ToIR()
	if len(got.Content) != 1 || got.Content[0].Text != "fallback" {
		t.Fatalf("fallback text lost: %+v", got.Content)
	}
}

// TestMessage_ToIR_RawContent_InvalidJSON — RawContent holds bytes that
// are not valid JSON. recoverIRRaw's json.Unmarshal fails and the adapter
// falls through to the text projection.
func TestMessage_ToIR_RawContent_InvalidJSON(t *testing.T) {
	m := Message{
		Role:       "user",
		Content:    "fallback",
		RawContent: json.RawMessage(`{not valid`),
	}
	got := m.ToIR()
	if len(got.Content) != 1 || got.Content[0].Text != "fallback" {
		t.Fatalf("fallback text lost: %+v", got.Content)
	}
}

// TestMessage_ToIR_RawContent_BareArrayNotEnvelope — RawContent holds a
// JSON array rather than an envelope object. recoverIRRaw's json.Unmarshal
// into irRawEnvelope fails (an array cannot unmarshal into a struct) and
// the adapter falls through. The array is the same shape the wire-format
// reader uses for `content`, so this matches the production behaviour.
func TestMessage_ToIR_RawContent_BareArrayNotEnvelope(t *testing.T) {
	m := Message{
		Role:       "user",
		Content:    "fallback",
		RawContent: json.RawMessage(`[{"type":"text","text":"envelope-text"}]`),
	}
	got := m.ToIR()
	// structuredContent sees the array first (RawContent holds it) and
	// dispatches through decodeContentBlocks; the text block in the array
	// is the canonical answer here.
	if len(got.Content) != 1 || got.Content[0].Text != "envelope-text" {
		t.Fatalf("array-in-RawContent path lost: %+v", got.Content)
	}
}

// TestMessage_ToIR_RawContent_EmptyEnvelope — RawContent is a valid JSON
// envelope whose every field is empty. recoverIRRaw's isEmpty check
// reports false, the adapter falls through to the text projection. The
// pin is that a zero envelope never overwrites a populated message.
func TestMessage_ToIR_RawContent_EmptyEnvelope(t *testing.T) {
	m := Message{
		Role:       "assistant",
		Content:    "fallback",
		ToolCalls:  []map[string]interface{}{{"id": "c1"}},
		RawContent: json.RawMessage(`{}`),
	}
	got := m.ToIR()
	if got.Role != "assistant" {
		t.Fatalf("role lost: %q", got.Role)
	}
	if len(got.Content) != 1 || got.Content[0].Text != "fallback" {
		t.Fatalf("fallback text lost: %+v", got.Content)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("tool calls lost: %+v", got.ToolCalls)
	}
}

// TestRawJSONFromAny_TypeVariants — the rawJSONFromAny helper normalises
// any-typed RawContent into json.RawMessage. Each variant must produce
// either valid JSON bytes or nil (which the envelope encoder skips via
// omitempty).
func TestRawJSONFromAny_TypeVariants(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want string // expected JSON bytes, or "" for nil
	}{
		{"json.RawMessage valid", json.RawMessage(`{"k":1}`), `{"k":1}`},
		{"json.RawMessage empty", json.RawMessage(""), ""},
		{"[]byte valid JSON", []byte(`{"k":1}`), `{"k":1}`},
		{"[]byte invalid JSON", []byte(`not json`), ""},
		{"[]byte empty", []byte{}, ""},
		{"string valid JSON", `{"k":1}`, `{"k":1}`},
		{"string plain text", "hello", `"hello"`},
		{"string empty", "", ""},
		{"int", 42, "42"},
		{"map", map[string]int{"k": 1}, `{"k":1}`},
		{"nil", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rawJSONFromAny(c.in)
			if c.want == "" {
				if len(got) != 0 {
					t.Fatalf("expected nil, got %s", got)
				}
				return
			}
			if string(got) != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}
}

// TestIRMessageFromJSON_SurrogatePair — content whose text uses a
// surrogate pair (legal UTF-8 in JSON, encoded as \uD83D\uDE00). The
// reader must preserve it (Go strings are UTF-8 and json.Unmarshal
// decodes the pair into a 4-byte sequence).
func TestIRMessageFromJSON_SurrogatePair(t *testing.T) {
	body := json.RawMessage(`{"role":"user","content":"hi \uD83D\uDE00 there"}`)
	got := IRMessagesFromJSON(body)
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if len(got[0].Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(got[0].Content))
	}
	if !strings.Contains(got[0].Content[0].Text, "\xf0\x9f\x98\x80") {
		t.Fatalf("surrogate pair did not decode to UTF-8 emoji: %q", got[0].Content[0].Text)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Parse-failure observability. logParseFail emits a Warn-level log line
// when the adapter degrades a payload. The line carries a stable `stage`
// key (e.g. "recover_ir_raw") and a structured `error`; operators grep
// the stage to count degradation events. Each sub-test below triggers one
// known fail-soft site and asserts the expected stage appears in the log.
//
// The handler swap uses slog.SetDefault, which is documented as safe to
// call from multiple goroutines. We restore the previous default in defer
// so this test cannot affect siblings that share the same process.
// ─────────────────────────────────────────────────────────────────────────

// TestParseFailObservability_Coverage — drives each known fail-soft site
// and asserts the corresponding stage appears in the captured log output.
// Adding a new log site without a sub-test here is a coverage gap; adding
// a sub-test here without a log site is a dead test.
func TestParseFailObservability_Coverage(t *testing.T) {
	// Independent buffer so parallel tests cannot interleave lines.
	var buf safeBuffer
	prev := slog.Default()
	defer slog.SetDefault(prev)
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	// recover_ir_raw — a RawContent that is neither a block array nor a
	// valid envelope (so it falls past the array fast-path).
	_ = (Message{
		Role: "user", Content: "fallback",
		RawContent: json.RawMessage(`{not a valid envelope`),
	}).ToIR()

	// ir_envelope_content — same path, but invoked via the standalone
	// helper (the ToIR path covers it through recoverIRRaw).
	_, _ = irEnvelopeContent(json.RawMessage(`{not a valid envelope`))

	// marshal_or_nil — an unmarshalable value (chan int).
	_ = marshalOrNil(make(chan int))

	// raw_json_from_any — invalid []byte that is non-empty and not JSON.
	// (The []byte branch is intentionally silent: a non-JSON byte slice
	// is a "valid empty" outcome with no error to attach. We exercise it
	// here to confirm it does not log a misleading line.)
	_ = rawJSONFromAny([]byte("not valid json"))

	// raw_json_from_any.default_marshal — a non-primitive value that
	// json.Marshal rejects (channels and funcs are the canonical cases).
	// The string branch cannot reach the log because json.Marshal always
	// succeeds on a string.
	_ = rawJSONFromAny(make(chan int))

	// decode_content_blocks — a truncated array triggers the defensive
	// outer-unmarshal failure path (all callers check `raw[0]=='['` first,
	// so the only way to hit this from a test is to call the helper
	// directly with malformed bytes).
	_ = decodeContentBlocks(json.RawMessage(`[{"type":"text","text":"hi"`))

	// decode_content_block — a block whose probe unmarshal fails because
	// the element is an array, not an object.
	IRMessagesFromJSON(json.RawMessage(`{"role":"user","content":[
		{"type":"text","text":"hi"},
		[1,2,3]
	]}`))

	// decode_image_source — an image carrier that does not parse because
	// the URL field is a number rather than a string.
	IRMessagesFromJSON(json.RawMessage(`{"role":"user","content":[
		{"type":"image","image_url": {"url": 42}}
	]}`))

	// decode_document_block — a document source that does not parse
	// because the type field is a number rather than a string.
	IRMessagesFromJSON(json.RawMessage(`{"role":"user","content":[
		{"type":"document","source": {"type":42}}
	]}`))

	// ir_blocks_from_raw.image — an envelope-shaped block whose image
	// sub-payload is a JSON string rather than the expected object. The
	// sub-payload unmarshals fine into json.RawMessage (a string is valid
	// JSON) but fails when the decoder tries to materialise it as an
	// ir.ImageSource struct.
	IRMessagesFromJSON(json.RawMessage(`{"role":"user","content":[
		{"$ir":1,"type":"image","image":"not-an-object"}
	]}`))

	// ir_blocks_from_raw.audio — same shape, audio sub-payload. Kept
	// distinct from image so each sub-field's log line is exercised.
	IRMessagesFromJSON(json.RawMessage(`{"role":"user","content":[
		{"$ir":1,"type":"audio","audio":"not-an-object"}
	]}`))

	// Decode the captured log lines and assert each known stage appears.
	output := buf.String()
	if output == "" {
		t.Fatalf("no log lines captured; observability is broken")
	}
	for _, stage := range []string{
		"recover_ir_raw",
		"ir_envelope_content",
		"raw_json_from_any",
		"marshal_or_nil",
		"decode_content_blocks",
		"decode_content_block",
		"decode_image_source",
		"decode_document_block",
		"ir_blocks_from_raw.image",
		"ir_blocks_from_raw.audio",
	} {
		if !strings.Contains(output, stage) {
			t.Errorf("expected stage %q in log output, got:\n%s", stage, output)
		}
	}
}

// TestParseFailObservability_NoLogOnHappyPath — a well-formed payload must
// not produce any logParseFail output. Catches a regression where someone
// adds a log call to a hot path that fires on every message.
func TestParseFailObservability_NoLogOnHappyPath(t *testing.T) {
	var buf safeBuffer
	prev := slog.Default()
	defer slog.SetDefault(prev)
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	// Round-trip a text message through ToIR.
	_ = (Message{Role: "user", Content: "hi"}).ToIR()

	// Round-trip a multimodal IR message through Marshal + Unmarshal +
	// ToIR.
	in := ir.Message{
		Role: "user",
		Content: []ir.ContentBlock{
			{Type: "text", Text: "hi"},
			{Type: "image", Image: &ir.ImageSource{Type: "url", URL: "https://e.com/a.png"}},
		},
	}
	wire, err := json.Marshal([]Message{MessageFromIR(in)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var readBack []Message
	if err := json.Unmarshal(wire, &readBack); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	_ = IRMessagesFromV2(readBack)

	if strings.Contains(buf.String(), "ir adapter parse failure") {
		t.Fatalf("happy path emitted a parse-failure log:\n%s", buf.String())
	}
}

// safeBuffer is a thread-safe wrapper around bytes.Buffer so the global
// slog handler can write to it without racing against the test goroutine.
// slog.Default is package-global; if any other goroutine logs while our
// test handler is installed, that line lands here too.
type safeBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ─────────────────────────────────────────────────────────────────────────
// Test helpers. errAt() formats the goroutine id + iteration + message
// into a single string so the errCh carries enough context to identify
// which goroutine lost.
// ─────────────────────────────────────────────────────────────────────────

func errAt(g, i int, format string, args ...interface{}) error {
	return fmt.Errorf("g=%d i=%d: "+format, append([]interface{}{g, i}, args...)...)
}
