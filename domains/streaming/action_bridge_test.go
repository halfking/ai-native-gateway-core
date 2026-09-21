package streaming

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// frameRecorder is a synchronous FrameWriter capturing every frame verbatim.
type frameRecorder struct {
	buf bytes.Buffer
}

func (w *frameRecorder) WriteFrame(frame string) error {
	_, err := w.buf.WriteString(frame)
	return err
}

func (w *frameRecorder) frames() []string {
	raw := w.buf.String()
	var out []string
	for _, part := range strings.Split(raw, "\n\n") {
		if strings.TrimSpace(part) != "" {
			out = append(out, part+"\n\n")
		}
	}
	return out
}

func bridgeTestEvent(action liveactions.Action, requestID string) liveactions.ActionEvent {
	return liveactions.ActionEvent{
		RequestID: requestID,
		Action:    action,
		Model:     "gpt-x",
		Detail:    map[string]string{"from": "cred-1", "to": "cred-2"},
	}
}

// ── UT-SK-06 思考帧格式与运营开关 ────────────────────────────────────────

func TestActionBridgeCommentFrameFormatMatchesHandler(t *testing.T) {
	reg := NewConnectionRegistry(0, time.Second, 0)
	rec := &frameRecorder{}
	require.NoError(t, reg.Register("req-1", rec, RegistrationMetadata{
		Protocol: "openai_chat", ClientType: "unknown",
	}, nil))

	b := NewActionBridge(ActionBridgeConfig{Enabled: true, Registry: reg, BufferSize: 8})
	b.Submit(bridgeTestEvent(liveactions.ActionNodeSwitch, "req-1"))
	b.Close()

	frames := rec.frames()
	require.Len(t, frames, 1)
	frame := frames[0]
	// Format contract (handler.go:210-229 alignment): `: thinking: <json>`
	// SSE comment — comment-only, no `data:` lines, payload is a JSON string.
	require.True(t, strings.HasPrefix(frame, ": thinking: "), "frame = %q", frame)
	assert.True(t, strings.HasSuffix(frame, "\n\n"))
	assert.NotContains(t, frame, "data:", "Zod-strict clients must never see data: lines")

	payload := strings.TrimSuffix(strings.TrimPrefix(frame, ": thinking: "), "\n\n")
	var decoded string
	require.NoError(t, json.Unmarshal([]byte(payload), &decoded), "payload must be a JSON string: %q", payload)
	assert.Contains(t, decoded, "node_switch")
	assert.Contains(t, decoded, "gpt-x")
	assert.EqualValues(t, 1, b.SentTotal())
	assert.EqualValues(t, 1, b.CommentFramesTotal())
	assert.EqualValues(t, 0, b.SemanticFramesTotal())
}

func TestActionBridgeDisabledSendsNothing(t *testing.T) {
	reg := NewConnectionRegistry(0, time.Second, 0)
	rec := &frameRecorder{}
	require.NoError(t, reg.Register("req-1", rec, RegistrationMetadata{Protocol: "openai_chat"}, nil))

	// 运营开关整体关闭：不发任何思考帧（UT-SK-06）。
	b := NewActionBridge(ActionBridgeConfig{Enabled: false, Registry: reg})
	b.Submit(bridgeTestEvent(liveactions.ActionNodeSwitch, "req-1"))
	b.Close()
	assert.Empty(t, rec.frames())
	assert.EqualValues(t, 0, b.SentTotal())
}

func TestActionBridgeFiltersNonRequestScopedAndUnregistered(t *testing.T) {
	reg := NewConnectionRegistry(0, time.Second, 0)
	rec := &frameRecorder{}
	require.NoError(t, reg.Register("req-1", rec, RegistrationMetadata{Protocol: "openai_chat"}, nil))

	b := NewActionBridge(ActionBridgeConfig{Enabled: true, Registry: reg})
	b.Submit(liveactions.ActionEvent{RequestID: "", Action: liveactions.ActionArrive})               // no request id
	b.Submit(liveactions.ActionEvent{RequestID: "req-1", Action: liveactions.ActionStateChange})     // node-dimension
	b.Submit(bridgeTestEvent(liveactions.ActionNodeSwitch, "req-other"))                             // not a live client
	b.Submit(liveactions.ActionEvent{RequestID: "req-1", Action: liveactions.ActionUpstreamRequest}) // routed
	b.Close()

	frames := rec.frames()
	require.Len(t, frames, 1)
	assert.Contains(t, frames[0], ": thinking: ")
	assert.EqualValues(t, 1, b.NotRoutedTotal())
}

func TestActionBridgeSourcePumpConsumesTap(t *testing.T) {
	reg := NewConnectionRegistry(0, time.Second, 0)
	rec := &frameRecorder{}
	require.NoError(t, reg.Register("req-1", rec, RegistrationMetadata{Protocol: "openai_chat"}, nil))

	tap := NewChannelActionSource(8)
	b := NewActionBridge(ActionBridgeConfig{Enabled: true, Registry: reg, Source: tap})
	tap.Emit(bridgeTestEvent(liveactions.ActionModelSwitch, "req-1"))
	deadline := time.Now().Add(2 * time.Second)
	for b.SentTotal() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	b.Close()
	tap.Close()

	assert.EqualValues(t, 1, b.SentTotal())
	require.Len(t, rec.frames(), 1)
	assert.Contains(t, rec.frames()[0], "model_switch")
}

// ── UT-SK-07 语义帧白名单（unknown 回退注释） ────────────────────────────

func TestActionBridgeSemanticWhitelistAndUnknownFallback(t *testing.T) {
	reg := NewConnectionRegistry(0, time.Second, 0)
	whitelisted := &frameRecorder{}
	unknown := &frameRecorder{}
	require.NoError(t, reg.Register("req-wl", whitelisted, RegistrationMetadata{
		Protocol: "openai_chat", ClientType: "zcode",
	}, nil))
	require.NoError(t, reg.Register("req-unk", unknown, RegistrationMetadata{
		Protocol: "openai_chat", ClientType: "unknown",
	}, nil))

	b := NewActionBridge(ActionBridgeConfig{
		Enabled:                  true,
		Registry:                 reg,
		SemanticFrameClientTypes: []string{"zcode", "cursor"},
	})
	b.Submit(bridgeTestEvent(liveactions.ActionNodeSwitch, "req-wl"))
	b.Submit(bridgeTestEvent(liveactions.ActionNodeSwitch, "req-unk"))
	b.Close()

	// Whitelisted client (openai_chat) → reasoning_content semantic frame.
	wl := whitelisted.frames()
	require.Len(t, wl, 1)
	assert.True(t, strings.HasPrefix(wl[0], "data: "), "whitelisted frame = %q", wl[0])
	assert.Contains(t, wl[0], "reasoning_content")
	assert.EqualValues(t, 1, b.SemanticFramesTotal())

	// unknown client type → comment fallback (注释兜底, R3.2).
	unk := unknown.frames()
	require.Len(t, unk, 1)
	assert.True(t, strings.HasPrefix(unk[0], ": thinking: "))
	assert.EqualValues(t, 1, b.CommentFramesTotal())
}

func TestActionBridgeAnthropicSemanticFrame(t *testing.T) {
	reg := NewConnectionRegistry(0, time.Second, 0)
	rec := &frameRecorder{}
	require.NoError(t, reg.Register("req-a", rec, RegistrationMetadata{
		Protocol: "anthropic", ClientType: "claude-code",
	}, nil))

	b := NewActionBridge(ActionBridgeConfig{
		Enabled:                  true,
		Registry:                 reg,
		SemanticFrameClientTypes: []string{"claude-code"},
	})
	b.Submit(bridgeTestEvent(liveactions.ActionNodeSwitch, "req-a"))
	b.Close()

	frames := rec.frames()
	require.Len(t, frames, 1)
	assert.Contains(t, frames[0], "event: content_block_delta")
	assert.Contains(t, frames[0], "thinking_delta")
}

// ── UT-SK-08 不污染最终 answer ────────────────────────────────────────────

func TestActionBridgeNeverPollutesAnswerStream(t *testing.T) {
	// The answer is streamed by the main data path; the bridge may only add
	// frames. Frame classification proves it: every bridge frame is either a
	// keepalive comment (all client types) or a reasoning/thinking semantic
	// frame on whitelisted types — never a content delta.
	answer := "data: {\"choices\":[{\"delta\":{\"content\":\"final answer\"}}]}\n\ndata: [DONE]\n\n"

	reg := NewConnectionRegistry(0, time.Second, 0)
	rec := &frameRecorder{}
	require.NoError(t, reg.Register("req-1", rec, RegistrationMetadata{
		Protocol: "openai_chat", ClientType: "unknown",
	}, nil))
	b := NewActionBridge(ActionBridgeConfig{Enabled: true, Registry: reg})
	for _, action := range []liveactions.Action{
		liveactions.ActionArrive, liveactions.ActionRouteResolved,
		liveactions.ActionModelEnqueued, liveactions.ActionCredentialSelected,
		liveactions.ActionNodeSelected, liveactions.ActionUpstreamRequest,
		liveactions.ActionFirstByte, liveactions.ActionNodeSwitch,
		liveactions.ActionModelSwitch, liveactions.ActionNodeSwitch,
	} {
		b.Submit(bridgeTestEvent(action, "req-1"))
	}
	b.Close()

	bridgeFrames := rec.frames()
	require.NotEmpty(t, bridgeFrames)
	for _, frame := range bridgeFrames {
		class := ClassifyClientFrame(ProtocolOpenAIChat, frame)
		assert.Equal(t, FrameClassKeepalive, class, "bridge emitted a non-comment frame for a comment-mode client: %q", frame)
		assert.NotContains(t, frame, "\"content\"", "bridge must never write content deltas")
	}

	// Stripping the bridge frames leaves the answer byte-identical: the
	// data path is untouched (只增注释帧).
	var merged strings.Builder
	merged.WriteString(answer)
	for _, frame := range bridgeFrames {
		merged.WriteString(frame)
	}
	assert.Contains(t, merged.String(), answer)
}

func TestActionBridgeDropsWhenQueueFull(t *testing.T) {
	// 旁路异步：有界 channel 满即丢弃并计数，不阻塞调用方。
	reg := NewConnectionRegistry(0, 80*time.Millisecond, 0)
	bw := &blockedWriter{release: make(chan struct{})}
	require.NoError(t, reg.Register("req-1", bw, RegistrationMetadata{Protocol: "openai_chat"}, nil))

	b := NewActionBridge(ActionBridgeConfig{Enabled: true, Registry: reg, BufferSize: 1})
	for i := 0; i < 8; i++ {
		b.Submit(bridgeTestEvent(liveactions.ActionArrive, "req-1")) // must never block
	}
	// The pump dispatches at most one event (blocked on the stalled client
	// until the write deadline detaches it); the 1-slot queue overflows.
	deadline := time.Now().Add(2 * time.Second)
	for b.DroppedTotal() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
		b.Submit(bridgeTestEvent(liveactions.ActionArrive, "req-1"))
	}
	close(bw.release)
	b.Close()

	assert.GreaterOrEqual(t, b.DroppedTotal(), uint64(1), "full queue must drop + count")
}
