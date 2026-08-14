package streaming

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SR-W1 Phase 0B (doc 18 §9.3): with the gate in immediate mode the wire
// bytes must be identical to the legacy path — only commit-state tracking is
// added. These tests pin that guarantee at the component level and end-to-end
// through the real bridges.

func newImmediateGateWriter(protocol ClientProtocol) (*GateWriter, *AttemptCommitGate, *trackingFlusher) {
	f := &trackingFlusher{}
	gate := NewAttemptCommitGate(protocol, NewSerializedStreamWriter(f), GateOptions{Mode: GateModeImmediate})
	return NewGateWriter(gate), gate, f
}

func TestGateWriterImmediateModeByteIdentityWithSplitWrites(t *testing.T) {
	frames := []string{
		"event: message_start\ndata: {\"message\":{}}\n\n",
		"event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n",
		"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
		"event: message_stop\ndata: {}\n\n",
	}
	var legacy strings.Builder
	for _, fr := range frames {
		legacy.WriteString(fr)
	}

	gw, gate, f := newImmediateGateWriter(ProtocolAnthropic)
	// Feed one byte at a time to prove frames split across Write calls are
	// reassembled and emitted in order.
	raw := legacy.String()
	for i := 0; i < len(raw); i++ {
		n, err := gw.Write([]byte(raw[i : i+1]))
		require.NoError(t, err)
		require.Equal(t, 1, n)
	}
	gw.Finish()

	assert.Equal(t, legacy.String(), f.buf.String(), "wire bytes must be byte-identical in immediate mode")
	assert.Equal(t, CommitStateTerminal, gate.State(), "gate should track terminal state")
}

func TestGateWriterPendingPartialFrameHeldUntilFinish(t *testing.T) {
	gw, _, f := newImmediateGateWriter(ProtocolOpenAIChat)
	_, err := gw.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
	require.NoError(t, err)
	// A partial frame with no terminator yet.
	_, err = gw.Write([]byte("data: {\"choices\""))
	require.NoError(t, err)
	require.Equal(t, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n", f.buf.String(),
		"partial frame must not reach the wire before it completes")
	// Bridge Flush calls must not dump the unclassified partial frame.
	gw.Flush()
	require.Equal(t, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n", f.buf.String(),
		"Flush must not bypass the gate with pending partial frames")
	// Attempt end: Finish passes the trailing partial frame through unchanged.
	gw.Finish()
	assert.Equal(t,
		"data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: {\"choices\"",
		f.buf.String(), "Finish must pass the trailing partial frame through unchanged")
}

func upstreamSSEServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestGateWriterEndToEndAnthropicPassthroughByteIdentity runs the real
// Anthropic passthrough bridge twice — once into a plain writer (legacy) and
// once through a GateWriter in immediate mode — and requires identical
// bytes plus correct commit-state tracking.
func TestGateWriterEndToEndAnthropicPassthroughByteIdentity(t *testing.T) {
	body := strings.Join([]string{
		"event: message_start\n",
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"output_tokens\":1}}}\n",
		"\n",
		"event: content_block_delta\n",
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n",
		"\n",
		"event: message_stop\n",
		"data: {\"type\":\"message_stop\"}\n",
		"\n",
	}, "")
	srv := upstreamSSEServer(t, body)

	// Legacy path.
	resp1, err := http.Get(srv.URL)
	require.NoError(t, err)
	legacy := newBridgeWriter()
	pc1 := newBridgePendingCapturer(1024)
	out1 := StreamAnthropicPassthrough(legacy, resp1, "claude-x", "claude-x", "req-1", nil, pc1)
	require.False(t, out1.Interrupted)

	// Gated path (Phase 0B immediate mode).
	resp2, err := http.Get(srv.URL)
	require.NoError(t, err)
	gw, gate, f := newImmediateGateWriter(ProtocolAnthropic)
	pc2 := newBridgePendingCapturer(1024)
	out2 := StreamAnthropicPassthrough(gw, resp2, "claude-x", "claude-x", "req-1", nil, pc2)
	require.False(t, out2.Interrupted)

	assert.Equal(t, legacy.buf.String(), f.buf.String(),
		"Anthropic passthrough bytes must be identical with the Phase 0B gate")
	assert.Equal(t, CommitStateTerminal, gate.State())
}

// TestGateWriterEndToEndAnthropicToOpenAIByteIdentity runs the
// Anthropic→OpenAI conversion bridge through a GateWriter in immediate mode
// and requires byte-identical output vs the legacy writer.
func TestGateWriterEndToEndAnthropicToOpenAIByteIdentity(t *testing.T) {
	body := strings.Join([]string{
		"event: message_start\n",
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"claude-x\",\"role\":\"assistant\",\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n",
		"\n",
		"event: content_block_delta\n",
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hey\"}}\n",
		"\n",
		"event: message_delta\n",
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n",
		"\n",
		"event: message_stop\n",
		"data: {\"type\":\"message_stop\"}\n",
		"\n",
	}, "")
	srv := upstreamSSEServer(t, body)

	// Legacy path.
	resp1, err := http.Get(srv.URL)
	require.NoError(t, err)
	legacy := httptest.NewRecorder()
	out1 := StreamAnthropicSSEToOpenAI(legacy, resp1, "claude-x", "claude-x", "req-1", nil, nil)
	require.False(t, out1.Interrupted)

	// Gated path.
	resp2, err := http.Get(srv.URL)
	require.NoError(t, err)
	gw, gate, f := newImmediateGateWriter(ProtocolOpenAIChat)
	out2 := StreamAnthropicSSEToOpenAI(gw, resp2, "claude-x", "claude-x", "req-1", nil, nil)
	require.False(t, out2.Interrupted)

	assert.Equal(t, legacy.Body.String(), f.buf.String(),
		"OpenAI-chat bridge bytes must be identical with the Phase 0B gate")
	assert.Equal(t, CommitStateTerminal, gate.State())
}
