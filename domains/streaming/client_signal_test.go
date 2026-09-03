package streaming

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRenderClientSignalFrame verifies the gw-* SSE frame format produced by
// RenderClientSignalFrame. The format must be:
//
//	event: <EventName>\n
//	data: <JSON>\n
//	\n
//
// so safeWriteSSE can write it directly to the wire. The empty EventName
// guard prevents accidental writes of "event: \ndata: ..." which standard
// SSE parsers would interpret as an event with no type.
func TestRenderClientSignalFrame(t *testing.T) {
	t.Run("continue frame", func(t *testing.T) {
		got := RenderClientSignalFrame(ClientSignalRenderOptions{
			EventName: "gw-continue",
			Payload: map[string]interface{}{
				"version":      1,
				"type":         "gw_continue",
				"reason":       "goal_incomplete",
				"attempt":      1,
				"max_attempts": 3,
				"hint":         "请继续",
			},
		})
		assert.True(t, strings.HasPrefix(got, "event: gw-continue\ndata: "),
			"expected event: prefix, got: %q", got)
		assert.True(t, strings.HasSuffix(got, "\n\n"),
			"expected double-newline terminator, got: %q", got)
		assert.Contains(t, got, `"hint":"请继续"`,
			"payload should contain hint JSON-escaped")
	})

	t.Run("handoff frame", func(t *testing.T) {
		got := RenderClientSignalFrame(ClientSignalRenderOptions{
			EventName: "gw-handoff",
			Payload: map[string]interface{}{
				"version":     1,
				"type":        "gw_handoff",
				"reason":      "context_near_limit",
				"tokens_used": 215000,
			},
		})
		assert.Contains(t, got, "event: gw-handoff\n")
		assert.Contains(t, got, `"tokens_used":215000`)
	})

	t.Run("empty event name returns empty string", func(t *testing.T) {
		got := RenderClientSignalFrame(ClientSignalRenderOptions{
			EventName: "",
			Payload:   map[string]interface{}{"x": 1},
		})
		assert.Empty(t, got, "empty EventName must NOT emit a malformed frame")
	})
}

// TestParseSubAgentsHeader covers the four documented cases:
//   - missing header → empty snapshot
//   - malformed JSON → empty snapshot (fail-open)
//   - all completed → Pending=0
//   - mixed → counts split correctly
func TestParseSubAgentsHeader(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		snap := parseSubAgentsHeader("")
		assert.Equal(t, SubAgentSnapshot{}, snap)
	})

	t.Run("malformed JSON is silent drop", func(t *testing.T) {
		snap := parseSubAgentsHeader("{not-json")
		assert.Equal(t, SubAgentSnapshot{}, snap)
	})

	t.Run("all completed", func(t *testing.T) {
		snap := parseSubAgentsHeader(`[{"id":"a","status":"completed"},{"id":"b","status":"completed"}]`)
		assert.Equal(t, 2, snap.Total)
		assert.Equal(t, 2, snap.Completed)
		assert.Equal(t, 0, snap.Pending)
	})

	t.Run("mixed", func(t *testing.T) {
		snap := parseSubAgentsHeader(`[{"id":"a","status":"completed"},{"id":"b","status":"running"},{"id":"c","status":"queued"}]`)
		assert.Equal(t, 3, snap.Total)
		assert.Equal(t, 1, snap.Completed)
		assert.Equal(t, 2, snap.Pending)
	})

	t.Run("unknown status counted as pending", func(t *testing.T) {
		snap := parseSubAgentsHeader(`[{"id":"a","status":"frobnicate"}]`)
		assert.Equal(t, 1, snap.Pending,
			"unknown statuses must NOT be silently treated as completed")
	})
}

// TestParseClientCapabilitiesContinueHandoff (2026-09-03) verifies the
// capability whitelist accepts the new "continue" and "handoff" tokens,
// alongside the historical "durable-recovery" and "status-events".
func TestParseClientCapabilitiesContinueHandoff(t *testing.T) {
	t.Run("single new token", func(t *testing.T) {
		caps := ParseClientCapabilities("continue")
		assert.True(t, caps.Has(CapabilityClientSignal))
		assert.False(t, caps.Has(CapabilityHandoffSignal))
		assert.False(t, caps.Has(CapabilityDurableRecovery))
	})

	t.Run("both new tokens", func(t *testing.T) {
		caps := ParseClientCapabilities("continue, handoff")
		assert.True(t, caps.Has(CapabilityClientSignal))
		assert.True(t, caps.Has(CapabilityHandoffSignal))
	})

	t.Run("unknown tokens ignored", func(t *testing.T) {
		caps := ParseClientCapabilities("continue,bogus-token")
		assert.True(t, caps.Has(CapabilityClientSignal))
		// unknown tokens are silently dropped, not surfaced
		assert.Equal(t, 1, len(caps.Tokens()))
	})

	t.Run("back-compat with historical tokens", func(t *testing.T) {
		caps := ParseClientCapabilities("durable-recovery,status-events")
		assert.True(t, caps.Has(CapabilityDurableRecovery))
		assert.True(t, caps.Has(CapabilityStatusEvents))
	})
}

// TestClientSignalRequested (2026-09-03) verifies the per-request helper
// reads X-Gw-Capabilities correctly when invoked through a real *http.Request.
func TestClientSignalRequested(t *testing.T) {
	t.Run("no header → false", func(t *testing.T) {
		req := newReq(t, "")
		assert.False(t, ClientSignalRequested(req))
		assert.False(t, HandoffSignalRequested(req))
	})

	t.Run("only continue → continue true, handoff false", func(t *testing.T) {
		req := newReq(t, "continue")
		assert.True(t, ClientSignalRequested(req))
		assert.False(t, HandoffSignalRequested(req))
	})
}

// newReq is a tiny helper to build a *http.Request for capability-header
// tests without pulling in the full handler plumbing. Kept private to the
// test file (lowercase) so it does not become exported API surface.
func newReq(t *testing.T, caps string) *http.Request {
	t.Helper()
	r, err := http.NewRequest("POST", "/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if caps != "" {
		r.Header.Set(GatewayCapabilitiesHeader, caps)
	}
	return r
}
