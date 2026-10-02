package credentialfpslot

// node_state_lua_shape_test.go — regression gate for the "empty Lua table"
// shape that made every node-state read fail in production.
//
// The payload below is COPIED VERBATIM from the live Redis key
// llmgw:cred_fp_node:126:gpt-5.6-terra (2026-10-02, api.vapeur.ai /
// credential 126 hxt-local). It is not a hand-written shape: `"slide_window":{}`
// is what Redis' cjson produces for the Lua `state.slide_window = {}` that the
// node scripts assign, and it is what made GetSupportsResponses return an
// error for every node — silently disabling both directions of the durable
// Responses capability gate.
//
// A test built on a freshly seeded miniredis key (no slide_window written by a
// Lua script) passes either way, which is exactly why this gate exists.

import (
	"encoding/json"
	"testing"
)

// verbatim live payload, slide_window included as the empty object cjson emits
const liveVapeurNodeStatePayload = `{"slide_window":{},"disabled":false,"credential_id":126,"success_count":0,"failure_count":0,"capability_updated_at":1790915295,"capability_expires_at":1790918895,"capabilities":{"supports_responses":true},"model":"gpt-5.6-terra"}`

func TestNodeStateUnmarshal_ToleratesEmptyLuaTableSlideWindow(t *testing.T) {
	var s NodeState
	if err := json.Unmarshal([]byte(liveVapeurNodeStatePayload), &s); err != nil {
		t.Fatalf("live production payload failed to decode: %v", err)
	}
	if len(s.SlideWindow) != 0 {
		t.Fatalf("SlideWindow = %v, want empty", s.SlideWindow)
	}
	// The rest of the payload must survive the normalisation.
	if s.CredentialID != 126 || s.Model != "gpt-5.6-terra" {
		t.Fatalf("identity lost: %+v", s)
	}
	if !s.Capabilities.SupportsResponsesKnown() {
		t.Fatal("capabilities verdict lost by the slide_window normalisation")
	}
	if got := *s.Capabilities.SupportsResponses; !got {
		t.Fatal("supports_responses = false, want the true verdict that was stored")
	}
	if s.CapabilityExpiresAt != 1790918895 {
		t.Fatalf("CapabilityExpiresAt = %d, want 1790918895", s.CapabilityExpiresAt)
	}
}

// The verdict is useless if the node key cannot be read at all: this is the
// exact call both capability gates make.
func TestNodeStateUnmarshal_CapabilityRoundTripsThroughVerbatimPayload(t *testing.T) {
	var s NodeState
	if err := json.Unmarshal([]byte(liveVapeurNodeStatePayload), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !s.Capabilities.SupportsResponsesKnown() || !*s.Capabilities.SupportsResponses {
		t.Fatalf("verdict not readable through the gate's entry point: %+v", s.Capabilities)
	}
}

// A real (non-empty) array must still decode normally — the fix must not turn
// into "always drop slide_window".
func TestNodeStateUnmarshal_ArraySlideWindowStillDecodes(t *testing.T) {
	payload := `{"slide_window":[{"success":false,"timestamp":123,"error_kind":"timeout"}],"credential_id":7,"model":"m"}`
	var s NodeState
	if err := json.Unmarshal([]byte(payload), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(s.SlideWindow) != 1 {
		t.Fatalf("SlideWindow = %v, want 1 record", s.SlideWindow)
	}
	if s.SlideWindow[0].ErrorKind != "timeout" {
		t.Fatalf("record = %+v", s.SlideWindow[0])
	}
}

// The absent case (omitempty) must stay absent rather than becoming a nil-vs-empty
// behavioural change for callers doing len()/range.
func TestNodeStateUnmarshal_AbsentSlideWindow(t *testing.T) {
	var s NodeState
	if err := json.Unmarshal([]byte(`{"credential_id":7,"model":"m"}`), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(s.SlideWindow) != 0 {
		t.Fatalf("SlideWindow = %v, want empty", s.SlideWindow)
	}
}

// The hot path decodes one NodeState per routing candidate, so a payload that
// cannot be affected must not pay the fix-up cost. Both shapes below must
// decode correctly through the fast path.
func TestNodeStateUnmarshal_FastPathUnaffected(t *testing.T) {
	// No slide_window key at all → single-parse fast path.
	var a NodeState
	if err := json.Unmarshal([]byte(`{"credential_id":9,"model":"m","disabled":false}`), &a); err != nil {
		t.Fatalf("payload without slide_window failed: %v", err)
	}
	if a.CredentialID != 9 {
		t.Fatalf("CredentialID = %d", a.CredentialID)
	}
	// slide_window present but already an ARRAY → must pass through untouched.
	var b NodeState
	if err := json.Unmarshal([]byte(`{"slide_window":[],"credential_id":9,"model":"m"}`), &b); err != nil {
		t.Fatalf("array slide_window failed: %v", err)
	}
	if len(b.SlideWindow) != 0 {
		t.Fatalf("SlideWindow = %v", b.SlideWindow)
	}
}
