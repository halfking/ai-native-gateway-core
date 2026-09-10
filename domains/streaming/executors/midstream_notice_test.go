package executors

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestMaybeNotifyMidStreamFailure (audit R9 candidate 17): a stream that
// dies after chunks reached the client must surface an in-band `: thinking:`
// side-band message before the connection closes — ADR-Disp-003 forbids a
// node switch at that point, so the notice is the only explanation channel.
func TestMaybeNotifyMidStreamFailure(t *testing.T) {
	t.Run("fires_on_terminal_post_first_byte_interruption", func(t *testing.T) {
		capture := audit.NewStreamCapture()
		capture.RecordChunkSent()
		capture.RecordChunkSent()

		var got string
		params := &ExecParams{
			RequestID:          "req-c17",
			Capture:            capture,
			OnMidStreamFailure: func(msg string) { got = msg },
		}
		sie := &streamInterruptedError{reason: "stream_timeout", kind: errorsx.KindStreamTimeout}
		maybeNotifyMidStreamFailure(params, sie, true)

		if got == "" {
			t.Fatal("expected mid-stream failure notice, got none")
		}
		if !strings.Contains(got, "stream_timeout") || !strings.Contains(got, "2 chunk(s)") {
			t.Fatalf("notice missing reason/chunk count: %q", got)
		}
	})

	t.Run("silent_without_callback_or_bytes", func(t *testing.T) {
		capture := audit.NewStreamCapture()
		capture.RecordChunkSent()
		sie := &streamInterruptedError{reason: "stream_timeout", kind: errorsx.KindStreamTimeout}

		called := false
		notify := func(string) { called = true }

		// nil callback: must not panic.
		maybeNotifyMidStreamFailure(&ExecParams{Capture: capture}, sie, true)
		// pre-first-byte (bytesSent=false): failover is still possible, no notice.
		maybeNotifyMidStreamFailure(&ExecParams{Capture: capture, OnMidStreamFailure: notify}, sie, false)
		// nil interruption: not a stream death.
		maybeNotifyMidStreamFailure(&ExecParams{OnMidStreamFailure: notify}, nil, true)
		if called {
			t.Fatal("notice must stay silent without callback / bytesSent / interrupted error")
		}
	})

	t.Run("silent_for_client_side_interruption", func(t *testing.T) {
		capture := audit.NewStreamCapture()
		capture.RecordChunkSent()

		called := false
		params := &ExecParams{
			Capture:            capture,
			OnMidStreamFailure: func(string) { called = true },
		}
		sie := &streamInterruptedError{reason: "client_cancel", kind: errorsx.KindCanceled}
		maybeNotifyMidStreamFailure(params, sie, true)
		if called {
			t.Fatal("client-side cancellation must not emit a notice (recipient is gone)")
		}
	})

	t.Run("rollback_switch_disables_notice", func(t *testing.T) {
		t.Setenv("LLM_GATEWAY_MIDSTREAM_FAILURE_NOTICE", "false")
		capture := audit.NewStreamCapture()
		capture.RecordChunkSent()

		called := false
		params := &ExecParams{
			Capture:            capture,
			OnMidStreamFailure: func(string) { called = true },
		}
		sie := &streamInterruptedError{reason: "stream_timeout", kind: errorsx.KindStreamTimeout}
		maybeNotifyMidStreamFailure(params, sie, true)
		if called {
			t.Fatal("LLM_GATEWAY_MIDSTREAM_FAILURE_NOTICE=false must suppress the notice")
		}
	})
}
