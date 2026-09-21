package streaming

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// attempt_commit_gate_holdback_test.go — FR-12 L1 (会话优化 v4 T13-lite):
// GateOptions.HoldbackWindow > 0 enables the revocable window on top of the
// existing buffered mode; 0 (default) keeps the legacy behavior untouched.

// openAIFrame renders one minimal openai_chat content frame.
func openAIContentFrame(delta string) string {
	return "data: {\"choices\":[{\"delta\":{\"content\":" + quoteJSON(delta) + "}}]}\n\n"
}

func quoteJSON(s string) string {
	return "\"" + s + "\""
}

func TestAttemptCommitGateHoldbackHoldsSemanticFramesAndDiscards(t *testing.T) {
	var buf bytes.Buffer
	sw := NewSerializedStreamWriter(&buf)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	gate := NewAttemptCommitGate(context.Background(), ProtocolOpenAIChat, sw, GateOptions{
		Mode:              GateModeBuffered,
		HoldbackWindow:    5 * time.Second,
		HoldbackMaxChunks: 20,
		Now:               func() time.Time { return now },
	})

	// First semantic frames stay inside the window: buffered, NOT committed,
	// state must NOT advance (Discard stays legal).
	for i := 0; i < 5; i++ {
		require.NoError(t, gate.WriteFrame(openAIContentFrame("chunk")))
	}
	assert.False(t, gate.Committed())
	assert.Equal(t, CommitStateNone, gate.State(), "held frames must not advance commit state")
	assert.Equal(t, 5, gate.HoldbackHeldChunks())
	assert.True(t, gate.HoldbackWindowOpen())
	assert.Empty(t, buf.String(), "nothing reached the client yet")

	// In-window interruption: discard the buffer invisibly (L1 replay).
	require.NoError(t, gate.Discard())
	assert.Empty(t, buf.String())
	require.ErrorIs(t, gate.WriteFrame(openAIContentFrame("late")), ErrAttemptDiscarded)
}

func TestAttemptCommitGateHoldbackChunkBoundaryFlushes(t *testing.T) {
	var buf bytes.Buffer
	sw := NewSerializedStreamWriter(&buf)
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	current := base
	gate := NewAttemptCommitGate(context.Background(), ProtocolOpenAIChat, sw, GateOptions{
		Mode:              GateModeBuffered,
		HoldbackWindow:    5 * time.Second,
		HoldbackMaxChunks: 3,
		Now:               func() time.Time { return current },
	})

	// Chunks 1..3 held (window limits), chunk 4 flushes everything.
	for i := 0; i < 3; i++ {
		require.NoError(t, gate.WriteFrame(openAIContentFrame("c"+string(rune('0'+i)))))
	}
	assert.False(t, gate.Committed())
	current = base.Add(10 * time.Millisecond)
	require.NoError(t, gate.WriteFrame(openAIContentFrame("c3")))

	assert.True(t, gate.Committed(), "the first frame after the window closes commits the held buffer")
	assert.Equal(t, CommitStateContent, gate.State())
	assert.Contains(t, buf.String(), "c0")
	assert.Contains(t, buf.String(), "c3")
	// Discard now refused (content reached the client → L2 territory).
	require.ErrorIs(t, gate.Discard(), ErrAttemptAlreadyCommitted)
}

func TestAttemptCommitGateHoldbackTimeBoundaryFlushes(t *testing.T) {
	var buf bytes.Buffer
	sw := NewSerializedStreamWriter(&buf)
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	current := base
	gate := NewAttemptCommitGate(context.Background(), ProtocolOpenAIChat, sw, GateOptions{
		Mode:              GateModeBuffered,
		HoldbackWindow:    5 * time.Second,
		HoldbackMaxChunks: 20,
		Now:               func() time.Time { return current },
	})
	require.NoError(t, gate.WriteFrame(openAIContentFrame("early")))
	assert.True(t, gate.HoldbackWindowOpen())

	// 4.9s: still in window. 5.0s: closed — the next semantic frame commits.
	current = base.Add(4900 * time.Millisecond)
	assert.True(t, gate.HoldbackWindowOpen())
	current = base.Add(5000 * time.Millisecond)
	assert.False(t, gate.HoldbackWindowOpen())
	require.NoError(t, gate.WriteFrame(openAIContentFrame("late")))
	assert.True(t, gate.Committed())
	assert.Contains(t, buf.String(), "early")
	assert.Contains(t, buf.String(), "late")
}

func TestAttemptCommitGateHoldbackFlushAtStreamEnd(t *testing.T) {
	// A successful stream that ends while the window is still open must not
	// swallow the held content: FinishAttempt flushes it.
	var buf bytes.Buffer
	sw := NewSerializedStreamWriter(&buf)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	gate := NewAttemptCommitGate(context.Background(), ProtocolOpenAIChat, sw, GateOptions{
		Mode:              GateModeBuffered,
		HoldbackWindow:    5 * time.Second,
		HoldbackMaxChunks: 20,
		Now:               func() time.Time { return now },
	})
	require.NoError(t, gate.WriteFrame(openAIContentFrame("final")))
	require.NoError(t, gate.FinishAttempt("data: [DONE]\n\n"))

	assert.True(t, gate.Committed())
	assert.Contains(t, buf.String(), "final")
	assert.Contains(t, buf.String(), "[DONE]")

	// Explicit FlushHoldback is a no-op after the commit.
	require.NoError(t, gate.FlushHoldback())
}

func TestAttemptCommitGateHoldbackBeforeSemanticCommitHook(t *testing.T) {
	// Held frames reach the network only on flush: the write-ahead hook must
	// fire exactly at that point, not while frames are merely buffered.
	var buf bytes.Buffer
	sw := NewSerializedStreamWriter(&buf)
	hookStates := make([]CommitState, 0, 2)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	gate := NewAttemptCommitGate(context.Background(), ProtocolOpenAIChat, sw, GateOptions{
		Mode:              GateModeBuffered,
		HoldbackWindow:    time.Second,
		HoldbackMaxChunks: 2,
		BeforeSemanticCommit: func(_ context.Context, s CommitState) error {
			hookStates = append(hookStates, s)
			return nil
		},
		Now: func() time.Time { return now },
	})
	require.NoError(t, gate.WriteFrame(openAIContentFrame("a")))
	require.NoError(t, gate.WriteFrame(openAIContentFrame("b")))
	assert.Empty(t, hookStates, "buffering alone never fires the checkpoint hook")
	require.NoError(t, gate.FlushHoldback())
	require.NotEmpty(t, hookStates)
	assert.Contains(t, hookStates, CommitStateContent)
	assert.Contains(t, buf.String(), "a")
}

func TestAttemptCommitGateZeroHoldbackKeepsLegacyBehavior(t *testing.T) {
	// Default (HoldbackWindow == 0): the first semantic frame commits
	// immediately — byte-identical to the pre-T13 gate.
	var buf bytes.Buffer
	sw := NewSerializedStreamWriter(&buf)
	gate := NewAttemptCommitGate(context.Background(), ProtocolOpenAIChat, sw, GateOptions{Mode: GateModeBuffered})
	require.NoError(t, gate.WriteFrame(openAIContentFrame("first")))
	assert.True(t, gate.Committed(), "no holdback → first semantic frame commits")
	assert.Equal(t, 0, gate.HoldbackHeldChunks())
	assert.Contains(t, buf.String(), "first")
}
