package sessionsummary

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// rawDelta marshals a slice of messages to the JSONB shape request_delta has on
// disk, so tests can feed the collapse function realistic payloads.
func rawDelta(t *testing.T, msgs []sessionMessageV2) []byte {
	t.Helper()
	if msgs == nil {
		return nil
	}
	b, err := json.Marshal(msgs)
	if err != nil {
		t.Fatalf("marshal delta: %v", err)
	}
	return b
}

// rawText wraps a plain-string content into the RawMessage shape used by
// sessionMessageV2.
func rawText(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// TestCollapseToLastRequestMessage_SelectsLast is the V1-parity contract: V1's
// request_logs query returns messages[-1] of the turn body. The V2 source must
// do the same with request_delta so both sources feed the summarizer the same
// representative prompt per turn.
func TestCollapseToLastRequestMessage_SelectsLast(t *testing.T) {
	row := v2TurnRow{
		RequestID: "req-1",
		Model:     "glm-4.6",
		Ts:        time.Unix(1000, 0),
		RequestDelta: rawDelta(t, []sessionMessageV2{
			{Role: "system", Content: rawText("ignored")},
			{Role: "user", Content: rawText("the actual prompt")},
		}),
	}
	sm, ok := collapseToLastRequestMessage(row)
	if !ok {
		t.Fatal("expected a message, got none")
	}
	if sm.Content != "the actual prompt" {
		t.Errorf("content = %q, want last message content", sm.Content)
	}
	if sm.Role != "user" {
		t.Errorf("role = %q, want %q", sm.Role, "user")
	}
	if sm.RequestID != "req-1" || sm.Model != "glm-4.6" {
		t.Errorf("metadata not carried: %+v", sm)
	}
	if !sm.Timestamp.Equal(row.Ts) {
		t.Errorf("ts = %v, want %v", sm.Timestamp, row.Ts)
	}
}

// TestCollapseToLastRequestMessage_RoleDefaultsToUser matches V1's
// COALESCE(..., 'user') for messages that lack an explicit role.
func TestCollapseToLastRequestMessage_RoleDefaultsToUser(t *testing.T) {
	row := v2TurnRow{
		RequestID: "req-2",
		RequestDelta: rawDelta(t, []sessionMessageV2{
			{Content: rawText("no role set")},
		}),
	}
	sm, ok := collapseToLastRequestMessage(row)
	if !ok {
		t.Fatal("expected a message, got none")
	}
	if sm.Role != "user" {
		t.Errorf("role = %q, want default %q", sm.Role, "user")
	}
}

// TestCollapseToLastRequestMessage_SkipsUnusable confirms turns with no usable
// request_delta are skipped (ok=false), not emitted as empty messages — the
// summary must not contain zero-content rows.
func TestCollapseToLastRequestMessage_SkipsUnusable(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte(""),
		[]byte("null"),
		[]byte("[]"),                     // empty array → no messages
		[]byte("not json"),               // corrupt
		[]byte(`{"unexpected":"shape"}`), // not an array
	}
	for i, payload := range cases {
		row := v2TurnRow{RequestID: "req", RequestDelta: payload}
		if _, ok := collapseToLastRequestMessage(row); ok {
			t.Errorf("case %d (%q): expected skip, got a message", i, string(payload))
		}
	}
}

// TestCollapseToLastRequestMessage_MultimodalBlockArray is the regression test
// for IR-audit P1-2 (2026-09-16): a multimodal turn's request_delta content is
// a block ARRAY, which used to fail the plain-string unmarshal and silently
// drop the whole turn from summary input. It must now flatten to its text plus
// visible media placeholders.
func TestCollapseToLastRequestMessage_MultimodalBlockArray(t *testing.T) {
	blocks := json.RawMessage(`[
		{"type": "text", "text": "看看这张图"},
		{"type": "image_url", "image_url": {"url": "data:image/png;base64,xxx"}},
		{"type": "document", "source": {"type": "base64"}}
	]`)
	row := v2TurnRow{
		RequestID:    "req-mm",
		Model:        "glm-4.6",
		Ts:           time.Unix(2000, 0),
		RequestDelta: rawDelta(t, []sessionMessageV2{{Role: "user", Content: blocks}}),
	}
	sm, ok := collapseToLastRequestMessage(row)
	if !ok {
		t.Fatal("multimodal turn must not be dropped")
	}
	want := "看看这张图\n[附图×1]\n[附件×1]"
	if sm.Content != want {
		t.Errorf("content = %q, want %q", sm.Content, want)
	}
}

// TestCollapseTurns_PreservesOrderAndSkips verifies the turn→message mapping
// keeps ascending ts order (matching V1's ORDER BY ts ASC) and drops turns that
// have no usable request_delta.
func TestCollapseTurns_PreservesOrderAndSkips(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)
	t2 := time.Unix(3000, 0)
	turns := []v2TurnRow{
		{RequestID: "r0", Ts: t0, RequestDelta: rawDelta(t, []sessionMessageV2{{Role: "user", Content: rawText("first")}})},
		{RequestID: "r1", Ts: t1, RequestDelta: nil}, // skipped
		{RequestID: "r2", Ts: t2, RequestDelta: rawDelta(t, []sessionMessageV2{{Role: "user", Content: rawText("third")}})},
	}
	got := collapseTurns(turns)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages (1 turn skipped), got %d", len(got))
	}
	if got[0].RequestID != "r0" || got[1].RequestID != "r2" {
		t.Errorf("order/ids wrong: %+v", got)
	}
	if !got[0].Timestamp.Before(got[1].Timestamp) {
		t.Error("messages not in ascending ts order")
	}
}

// TestV2Source_NilPoolErrors guards the nil-safety contract shared with the V1
// source: a nil pool must return an error, not panic.
func TestV2SessionBodiesBaseQuery_UsesCurrentMonthView(t *testing.T) {
	if !strings.Contains(v2SessionBodiesBaseQuery, "LEFT JOIN public.session_turns_with_current_month t") {
		t.Fatalf("V2 message source must join the current-month view: %s", v2SessionBodiesBaseQuery)
	}
	if strings.Contains(v2SessionBodiesBaseQuery, "JOIN public.session_turns t") {
		t.Fatalf("V2 message source must not join the base table directly: %s", v2SessionBodiesBaseQuery)
	}
}

func TestV2Source_NilPoolErrors(t *testing.T) {
	src := &v2SessionBodiesSource{pool: nil}
	if _, err := src.GetSessionMessages(nil, "t", "s"); err == nil {
		t.Error("GetSessionMessages with nil pool: expected error, got nil")
	}
	if _, err := src.GetMessagesSince(nil, "t", "s", time.Time{}); err == nil {
		t.Error("GetMessagesSince with nil pool: expected error, got nil")
	}
}

// TestV2Source_SatisfiesMessageSource is a compile-time guarantee that the V2
// source implements the MessageSource interface (the same way A6's tests did
// for the V1 source). If this stops compiling, the A1 wiring target breaks.
func TestV2Source_SatisfiesMessageSource(t *testing.T) {
	var _ MessageSource = (*v2SessionBodiesSource)(nil)
}
