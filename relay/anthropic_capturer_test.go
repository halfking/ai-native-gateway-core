// Track C C5 (2026-06-21): capturer integration tests for the
// Anthropic streaming paths. Verifies that when a session-bearing
// client connects, every byte written to w is also appended to the
// pending-store capturer so the body can be replayed on reconnect.
//
// These tests use a real httptest.ResponseRecorder + a fabricated
// upstream Response.Body so we exercise the actual read loop, not
// a mock. The pendingCapturer is the same primitive used by the
// OpenAI path (relay/stream.go:623).

package relay

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/audit"
)

// makeAnthropicSSEBody wraps raw Anthropic SSE event bytes (each event
// is `event: ...\ndata: ...\n\n`) into an io.ReadCloser suitable for
// resp.Body. Returns the body and the underlying string so tests can
// compare.
func makeAnthropicSSEBody(t *testing.T, events []string) (io.ReadCloser, string) {
	t.Helper()
	body := strings.Join(events, "\n") + "\n"
	return io.NopCloser(strings.NewReader(body)), body
}

// TestStreamAnthropicPassthrough_CapturerAccumulatesLines verifies the
// basic Track C C5 contract: when pc is non-nil, every line forwarded
// to w is also captured. The Snapshot returns the same bytes.
func TestStreamAnthropicPassthrough_CapturerAccumulatesLines(t *testing.T) {
	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-3\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" world\"}}\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n",
		"",
	}
	body, raw := makeAnthropicSSEBody(t, events)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       body,
		Header:     http.Header{},
	}
	rec := httptest.NewRecorder()
	pc := NewPendingCapturer(0) // 0 → 1 MiB default

	outcome := StreamAnthropicPassthrough(rec, resp, "claude-3-sonnet", "claude-3-sonnet", "req-c5", &audit.StreamCapture{}, pc)
	if outcome.Interrupted {
		t.Fatalf("expected clean completion, got interrupted: %s", outcome.Reason)
	}

	// The capturer should hold the same bytes the client received.
	// Note: pc.Snapshot returns the finalized buffer; finalize marks
	// it completed after the read loop finishes.
	snapshot, state, ok := pc.Snapshot()
	if !ok {
		t.Fatalf("capturer Snapshot returned ok=false")
	}
	if state.Status != "completed" {
		t.Errorf("capturer status: got %q, want completed", state.Status)
	}
	// Compare byte-for-byte. The recorder received every line including
	// trailing newlines; the capturer should mirror it exactly.
	if string(snapshot) != raw {
		t.Errorf("capturer body mismatch:\n got: %q\nwant: %q", string(snapshot), raw)
	}
}

// TestStreamAnthropicPassthrough_NilCapturerIsNoOp verifies the legacy
// non-session path: pc=nil should not panic and should still forward
// the full body to w.
func TestStreamAnthropicPassthrough_NilCapturerIsNoOp(t *testing.T) {
	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_nil\",\"model\":\"m\"}}\n",
		"",
	}
	body, _ := makeAnthropicSSEBody(t, events)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       body,
		Header:     http.Header{},
	}
	rec := httptest.NewRecorder()
	outcome := StreamAnthropicPassthrough(rec, resp, "m", "m", "req-nil", &audit.StreamCapture{}, nil)
	if outcome.Interrupted {
		t.Fatalf("expected clean completion, got interrupted: %s", outcome.Reason)
	}
	if !strings.Contains(rec.Body.String(), `"id":"msg_nil"`) {
		t.Errorf("expected message_start in body, got: %q", rec.Body.String())
	}
}

// TestStreamAnthropicPassthrough_ClientCancelMarksCompletedWithBody
// verifies the BUG-4-style path: client disconnects mid-stream after at
// least one chunk arrived. The capturer should mark status=completed
// (because we have a replayable body) — not failed.
func TestStreamAnthropicPassthrough_ClientCancelMarksCompletedWithBody(t *testing.T) {
	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_cancel\",\"model\":\"m\"}}\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n",
	}
	body, _ := makeAnthropicSSEBody(t, events)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       body,
		Header:     http.Header{},
	}
	// A ResponseRecorder that returns errors on the second write to
	// simulate the client having disconnected. StreamAnthropicPassthrough
	// will mark outcome.Reason="client_disconnected" and return.
	rec := &flakyRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		failAfter:        1, // succeed once, then error
	}
	pc := NewPendingCapturer(0)
	outcome := StreamAnthropicPassthrough(rec, resp, "m", "m", "req-cancel", &audit.StreamCapture{}, pc)
	if !outcome.Interrupted || outcome.Reason != "client_disconnected" {
		t.Errorf("expected client_disconnected, got interrupted=%v reason=%q", outcome.Interrupted, outcome.Reason)
	}
	snapshot, state, ok := pc.Snapshot()
	if !ok {
		t.Fatalf("expected capturer finalized after client cancel")
	}
	// Per relay/stream.go:702-705 the StreamChatWithPendingCapture
	// pattern treats client_cancel+bytes>0 as "completed" so the body
	// is replayable. We mirror that behaviour here.
	if state.Status != "completed" {
		t.Errorf("client_cancel with body: status got %q, want completed (replayable)", state.Status)
	}
	if !strings.Contains(string(snapshot), "message_start") {
		t.Errorf("capturer missing first captured line: %q", string(snapshot))
	}
}

// flakyRecorder is an http.ResponseWriter that succeeds on the first N
// Write calls then returns an error to simulate a client disconnect.
type flakyRecorder struct {
	*httptest.ResponseRecorder
	failAfter int
	writes    int
}

func (f *flakyRecorder) Write(p []byte) (int, error) {
	f.writes++
	if f.writes > f.failAfter {
		return 0, io.ErrClosedPipe
	}
	return f.ResponseRecorder.Write(p)
}

// TestStreamAnthropicPassthrough_ReadErrorMarksFailed verifies the
// upstream-error path: upstream returns a non-EOF error before any
// complete chunk arrives. The capturer should mark status=failed
// (no replayable body).
func TestStreamAnthropicPassthrough_ReadErrorMarksFailed(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       io.NopCloser(&errReader{err: io.ErrUnexpectedEOF}),
		Header:     http.Header{},
	}
	rec := httptest.NewRecorder()
	pc := NewPendingCapturer(0)
	outcome := StreamAnthropicPassthrough(rec, resp, "m", "m", "req-err", &audit.StreamCapture{}, pc)
	if !outcome.Interrupted {
		t.Errorf("expected interrupted, got: %+v", outcome)
	}
	_, state, ok := pc.Snapshot()
	if !ok {
		t.Fatalf("expected capturer finalized")
	}
	if state.Status != "failed" {
		t.Errorf("upstream error: status got %q, want failed", state.Status)
	}
}

// errReader returns the same error from every Read call.
type errReader struct{ err error }

func (e *errReader) Read(p []byte) (int, error) { return 0, e.err }
func (e *errReader) Close() error               { return nil }

// ensure bufio is referenced; some Go toolchains complain about unused
// imports if the helpers above don't materialise any buffered reads.
var _ = bufio.NewReader