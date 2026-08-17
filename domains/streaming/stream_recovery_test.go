package streaming

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── UT-SR-01 触发词分类：全部进入 StreamRecovery，不直接终止 ─────────────

func TestClassifyStreamErrorAllTriggersRecoverable(t *testing.T) {
	cases := []struct {
		err     error
		class   StreamInterruptClass
		recover bool
	}{
		{errors.New("read tcp: connection reset by peer"), InterruptConnectionReset, true},
		{errors.New("write: broken pipe"), InterruptConnectionReset, true},
		{errors.New("upstream sent EOF without [DONE] (eof_without_done)"), InterruptEOFWithoutDone, true},
		{errors.New("stream closed before response completed"), InterruptEOFWithoutDone, true},
		{errors.New("unexpected EOF"), InterruptUnexpectedEOF, true},
		{errors.New("stream_timeout after 120s"), InterruptStreamTimeout, true},
		{errors.New("no data received: stream_chunk_timeout"), InterruptChunkTimeout, true},
		{errors.New("chunk_timeout"), InterruptChunkTimeout, true},
		{errors.New("slow_drip stream judged dead"), InterruptSlowDripDead, true},
		{errors.New("upstream_close mid-stream"), InterruptUpstreamClose, true},
		{context.Canceled, "", false},                             // R2.5 client abort
		{errors.New("invalid api key provided (401)"), "", false}, // directive
		{errors.New("model not found: nope-1"), "", false},        // directive
		{nil, "", false},
	}
	for _, tc := range cases {
		class, recoverable := ClassifyStreamError(tc.err)
		assert.Equal(t, tc.recover, recoverable, "err=%v", tc.err)
		if tc.recover {
			assert.Equal(t, tc.class, class, "err=%v", tc.err)
		}
	}
}

func TestNextRecoveryActionNonRecoverableFallsBackToEnvelope(t *testing.T) {
	act := NextRecoveryAction(&StreamRecoveryState{}, DefaultStreamRecoveryConfig(),
		errors.New("model not found"))
	assert.Equal(t, RecoveryActionErrorEnvelope, act.Kind)
	assert.Equal(t, RecoveryModeError, act.Mode)
}

// ── UT-SR-02 L0 同节点重发 ────────────────────────────────────────────────

func TestNextRecoveryActionL0SameNodeResend(t *testing.T) {
	cfg := DefaultStreamRecoveryConfig() // same_node_stream_retries = 2
	err := errors.New("connection reset by peer")

	// First two interruptions → RetrySameNode on the original node.
	for retries := uint8(0); retries < 2; retries++ {
		state := &StreamRecoveryState{SameNodeRetries: retries, CommittedChunks: 3}
		act := NextRecoveryAction(state, cfg, err)
		require.Equal(t, RecoveryActionRetrySameNode, act.Kind, "SameNodeRetries=%d", retries)
		require.Equal(t, RecoveryModeSameNode, act.Mode)
		require.Equal(t, []NodeSelectionPolicy{NodePolicyOriginal}, act.NodeOrder,
			"L0 keeps prompt-cache affinity on the original node")
	}

	// Same-node budget exhausted with nothing committed → L1 discard+replay
	// with the full ladder (原节点 → 同模型其它节点 → 同品质模型).
	state := &StreamRecoveryState{SameNodeRetries: 2, CommittedChunks: 0,
		HoldbackChunks: 5, HoldbackDeadlineMS: time.Now().Add(time.Second).UnixMilli()}
	act := NextRecoveryAction(state, cfg, err)
	require.Equal(t, RecoveryActionDiscardAndReplay, act.Kind)
	assert.Equal(t, []NodeSelectionPolicy{NodePolicyOriginal, NodePolicyOtherSameModel, NodePolicySameQualityModel}, act.NodeOrder)
}

// ── UT-SR-03 / UT-SR-04 L1 holdback 窗口与边界 ───────────────────────────

func TestHoldbackTrackerWindowSemantics(t *testing.T) {
	cfg := DefaultStreamRecoveryConfig() // 5s / 20 chunks, whichever first
	start := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	tr := &HoldbackTracker{}

	// Before the first semantic content the window is not open.
	assert.False(t, tr.ShouldHold(start, cfg))
	tr.Open(start)
	assert.True(t, tr.Opened())

	// Chunk boundary: chunks 1..20 are held; chunk 21 flushes (边界确定).
	for i := 1; i <= 20; i++ {
		require.True(t, tr.ShouldHold(start.Add(10*time.Millisecond), cfg),
			"chunk %d must stay inside the window", i)
		tr.RecordChunk()
	}
	assert.False(t, tr.ShouldHold(start.Add(20*time.Millisecond), cfg),
		"chunk 21 exceeds HoldbackMaxChunks=20 → window closed")

	// Time boundary: 4.9s still held, 5.0s closes the window.
	tr2 := &HoldbackTracker{}
	tr2.Open(start)
	assert.True(t, tr2.ShouldHold(start.Add(4900*time.Millisecond), cfg), "4.9s in window")
	assert.False(t, tr2.ShouldHold(start.Add(5000*time.Millisecond), cfg), "5.0s boundary closes")
	assert.False(t, tr2.ShouldHold(start.Add(5100*time.Millisecond), cfg), "5.1s out of window")

	// Stamp mirrors the tracker onto the R12.3 state fields.
	state := &StreamRecoveryState{}
	tr2.RecordChunk()
	tr2.Stamp(state, cfg)
	assert.EqualValues(t, 1, state.HoldbackChunks)
	assert.Equal(t, start.Add(5*time.Second).UnixMilli(), state.HoldbackDeadlineMS)
}

// ── UT-SR-05 L2 CommittedPrefix 内存上限与 hash-only 降级 ────────────────

func TestCommittedPrefixCacheBoundedWindowAndCapacity(t *testing.T) {
	cache := NewCommittedPrefixCache(2, 16)         // 2 requests, 16-byte window
	cache.Observe("r1", []byte("0123456789abcdef")) // full window
	cache.Observe("r1", []byte("XYZ"))              // tail beyond window

	p, ok := cache.Snapshot("r1")
	require.True(t, ok)
	assert.Equal(t, 19, p.TotalBytes)
	assert.Equal(t, 16, len(p.Head), "head window must stay bounded")
	assert.NotZero(t, p.Hash)

	// Snapshot deep-copies: mutating the clone must not alias cache state.
	p.Head[0] = '!'
	p2, _ := cache.Snapshot("r1")
	assert.Equal(t, byte('0'), p2.Head[0])

	// Capacity: with r1 and r2 live, a third request is dropped (bounded,
	// never blocks).
	cache.Observe("r2", []byte("second"))
	cache.Observe("r3", []byte("zz"))
	_, ok = cache.Snapshot("r3")
	assert.False(t, ok)
	assert.Equal(t, 2, cache.Len())
	assert.EqualValues(t, 1, cache.DroppedTotal())

	// Remove frees the slot.
	cache.Remove("r1")
	_, ok = cache.Snapshot("r1")
	assert.False(t, ok)
}

func TestPrefixAlignerDegradesToHashOnlyBeyondWindow(t *testing.T) {
	aligner := NewPrefixAligner(9000)
	head := []byte("0123456789abcdef")
	tail := []byte("TAIL-BEYOND-WINDOW")
	full := append(append([]byte(nil), head...), tail...)
	committed := CommittedPrefix{Hash: fnv64a(full), TotalBytes: len(full), Head: head}

	// Exact replay: head matches + full hash verifies → aligned, suffix =
	// bytes after the whole committed prefix (hash-only mode).
	fresh := append(append([]byte(nil), full...), []byte("+suffix")...)
	res := aligner.Align(committed, fresh)
	require.True(t, res.Aligned)
	assert.True(t, res.HashOnly, "committed bytes exceeded the window → hash-only align")
	assert.Equal(t, 10000, int(res.ScoreBP))
	assert.Equal(t, len(full), res.SuffixOffset)
	assert.Equal(t, "+suffix", string(res.Suffix))

	// One byte changed in the hash-covered tail → all-or-nothing miss.
	bad := append([]byte(nil), fresh...)
	bad[len(head)+2] ^= 1
	res = aligner.Align(committed, bad)
	assert.False(t, res.Aligned, "hash mismatch must refuse suppression (不得静默拼接)")
	assert.Empty(t, res.Suffix)
}

// ── UT-SR-06 L2 对齐续传 ──────────────────────────────────────────────────

func TestPrefixAlignerWindowModeAlignment(t *testing.T) {
	aligner := NewPrefixAligner(9000)
	committedBytes := []byte("The quick brown fox jumps over the lazy dog")
	committed := CommittedPrefix{
		Hash:       fnv64a(committedBytes),
		TotalBytes: len(committedBytes),
		Head:       append([]byte(nil), committedBytes...), // window covers all
	}

	// Fresh stream reproduces the committed prefix and continues: aligned →
	// only the suffix is forwarded; client concatenation equals the full
	// answer.
	fresh := append(append([]byte(nil), committedBytes...), []byte(" — and keeps running.")...)
	res := aligner.Align(committed, fresh)
	require.True(t, res.Aligned)
	assert.Equal(t, 10000, int(res.ScoreBP))
	assert.Equal(t, len(committedBytes), res.CommonBytes)
	assert.Equal(t, " — and keeps running.", string(res.Suffix))
	assert.Equal(t, string(committedBytes)+string(res.Suffix), string(fresh),
		"client-side concatenation must equal the full answer (byte-seamless)")

	// Fresh stream diverges early → score below threshold, not aligned.
	diverged := []byte("A completely different model answer.")
	res = aligner.Align(committed, diverged)
	assert.False(t, res.Aligned)
	assert.Less(t, int(res.ScoreBP), 9000)
	assert.Empty(t, res.Suffix, "unaligned replay must not be spliced")

	// Partial replay ≥ threshold suppresses the matched prefix and forwards
	// the rest (40 of 43 committed bytes reproduced → 9302bp).
	partial := []byte("The quick brown fox jumps over the lazy caX-tra")
	res = aligner.Align(committed, partial)
	require.True(t, res.Aligned)
	assert.Equal(t, len("The quick brown fox jumps over the lazy "), res.CommonBytes)
	assert.Equal(t, "caX-tra", string(res.Suffix))
}

// ── UT-SR-08 L4 降级 ──────────────────────────────────────────────────────

func TestRenderStreamRestartFrames(t *testing.T) {
	// stream-undo 客户端：版本化撤销控制事件。
	undo := RenderStreamRestartFrames(RestartRenderOptions{
		Protocol: ProtocolOpenAIChat, ClientStreamUndo: true, RequestID: "req-1",
	})
	require.Len(t, undo.Frames, 1)
	assert.Contains(t, undo.Frames[0], "event: gw-stream-undo\n")
	assert.Contains(t, undo.Frames[0], `"type":"gw_stream_undo"`)
	assert.Contains(t, undo.Frames[0], `"version":1`)
	assert.True(t, undo.UndoSignal)
	assert.False(t, undo.VisibleRestart)

	// 通用客户端：`: gw-stream-restarted` 注释 + thinking 注释，无 data: 行
	// （Zod 安全），visible_restart 记账。
	generic := RenderStreamRestartFrames(RestartRenderOptions{
		Protocol: ProtocolOpenAIChat, RequestID: "req-1", Reason: "alignment miss",
	})
	require.Len(t, generic.Frames, 2)
	assert.Equal(t, ": gw-stream-restarted\n\n", generic.Frames[0])
	assert.True(t, strings.HasPrefix(generic.Frames[1], ": thinking: "))
	for _, f := range generic.Frames {
		assert.Equal(t, FrameClassKeepalive, ClassifyClientFrame(ProtocolOpenAIChat, f),
			"L4 generic frames must stay comment-only: %q", f)
	}
	assert.True(t, generic.VisibleRestart)
}

func TestNextRecoveryActionL4Fallbacks(t *testing.T) {
	err := errors.New("connection reset by peer")

	// 策略禁止重复（无 stream-undo、运营不允许 visible restart）→ 错误信封
	// （旧行为，不静默拼接重复内容）。
	state := &StreamRecoveryState{
		RecoveryNo: 3, SameNodeRetries: 2, CommittedChunks: 5,
		RecoveryMode: RecoveryModeAligned, AlignmentScoreBP: 100,
	}
	act := NextRecoveryAction(state, DefaultStreamRecoveryConfig(), err)
	assert.Equal(t, RecoveryActionErrorEnvelope, act.Kind)
	assert.False(t, act.VisibleRestart)

	// 运营允许 visible restart → L4 restart。
	allow := DefaultStreamRecoveryConfig()
	allow.AllowVisibleRestart = true
	act = NextRecoveryAction(state, allow, err)
	assert.Equal(t, RecoveryActionVisibleRestart, act.Kind)
	assert.True(t, act.VisibleRestart)

	// 客户端声明 stream-undo → L4 undo form。
	undo := DefaultStreamRecoveryConfig()
	undo.ClientStreamUndo = true
	act = NextRecoveryAction(state, undo, err)
	assert.Equal(t, RecoveryActionVisibleRestart, act.Kind)
}

// ── UT-SR-09 恢复预算 ─────────────────────────────────────────────────────

func TestNextRecoveryActionRecoveryBudgetExhaustion(t *testing.T) {
	resetStreamRecoveryMetricsForTest()
	defer resetStreamRecoveryMetricsForTest()
	err := errors.New("stream_timeout")
	cfg := DefaultStreamRecoveryConfig() // max attempts 3

	// RecoveryNo == 3 (attempted three recoveries) → L4, never infinite
	// regeneration.
	state := &StreamRecoveryState{RecoveryNo: 3, SameNodeRetries: 5, CommittedChunks: 2}
	act := NextRecoveryAction(state, cfg, err)
	assert.Equal(t, RecoveryActionErrorEnvelope, act.Kind)

	// One below the budget still recovers (L2 for committed content).
	state.RecoveryNo = 2
	act = NextRecoveryAction(state, cfg, err)
	assert.Equal(t, RecoveryActionAlignedContinuation, act.Kind)

	// R12.9 accounting: interrupt counted; success by mode; visible interrupt.
	RecordStreamRecoverySuccess(RecoveryModeSameNode)
	RecordClientVisibleInterrupt()
	snap := SnapshotStreamRecoveryMetrics()
	assert.GreaterOrEqual(t, snap.InterruptTotal, uint64(2))
	assert.EqualValues(t, 1, snap.RecoverySuccessByMode["same_node"])
	assert.EqualValues(t, 1, snap.VisibleInterruptTotal)
	AddStreamRecoveryCostTokens(1500)
	assert.EqualValues(t, 1500, SnapshotStreamRecoveryMetrics().CostTokens)
}

// ── UT-SR-12 ADR-Disp-007 四条件门控 ─────────────────────────────────────

func TestCrossCredentialUnlockAllowedFourConditions(t *testing.T) {
	base := DefaultStreamRecoveryConfig()

	// All four failing → locked (回退旧行为).
	assert.False(t, CrossCredentialUnlockAllowed(&StreamRecoveryState{CommittedChunks: 5}, base))

	// ① L1 未提交（内容仍在可撤销窗口）。
	assert.True(t, CrossCredentialUnlockAllowed(&StreamRecoveryState{
		CommittedChunks: 0, HoldbackChunks: 3, HoldbackDeadlineMS: 1,
	}, base))

	// ② L2 对齐成功。
	assert.True(t, CrossCredentialUnlockAllowed(&StreamRecoveryState{
		CommittedChunks: 9, AlignmentScoreBP: 9500,
	}, base))

	// ③ 客户端声明 stream-undo。
	undo := base
	undo.ClientStreamUndo = true
	assert.True(t, CrossCredentialUnlockAllowed(&StreamRecoveryState{CommittedChunks: 9}, undo))

	// ④ 运营允许 visible restart。
	ops := base
	ops.AllowVisibleRestart = true
	assert.True(t, CrossCredentialUnlockAllowed(&StreamRecoveryState{CommittedChunks: 9}, ops))
}

func TestNextRecoveryActionLockedLadderRestrictedToOriginalNode(t *testing.T) {
	// Committed content + none of the four unlock conditions: cross-node /
	// cross-model re-execution is NOT allowed — the ladder falls back to the
	// original node only (ADR-Disp-003 旧行为), never silently splicing
	// duplicated content.
	err := errors.New("connection reset by peer")
	state := &StreamRecoveryState{CommittedChunks: 10, SameNodeRetries: 2}
	cfg := DefaultStreamRecoveryConfig()

	act := NextRecoveryAction(state, cfg, err)
	require.Equal(t, RecoveryActionAlignedContinuation, act.Kind)
	assert.Equal(t, []NodeSelectionPolicy{NodePolicyOriginal}, act.NodeOrder)

	// Unlocked (alignment succeeded) → full ladder.
	state.AlignmentScoreBP = 9500
	act = NextRecoveryAction(state, cfg, err)
	require.Equal(t, RecoveryActionAlignedContinuation, act.Kind)
	assert.Equal(t, fullReplayLadder, act.NodeOrder)
}

// ── UT-SR-07 L3 continuation（默认关、白名单） ───────────────────────────

func TestContinuationPromptPureFunction(t *testing.T) {
	original := []ContinuationMessage{
		{Role: "user", Content: "write a poem"},
	}
	out, err := BuildContinuationPrompt(original, "Roses are red")
	require.NoError(t, err)
	require.Len(t, out, 3)
	assert.Equal(t, "user", out[0].Role)
	assert.Equal(t, "assistant", out[1].Role)
	assert.Equal(t, "Roses are red", out[1].Content, "已发送部分进入 assistant turn")
	assert.Equal(t, "user", out[2].Role)
	assert.Contains(t, strings.ToLower(out[2].Content), "do not repeat")
	// Pure: input slice untouched.
	assert.Len(t, original, 1)

	// No sent content yet → assistant partial omitted.
	out, err = BuildContinuationPrompt(original, "  ")
	require.NoError(t, err)
	assert.Len(t, out, 2)

	// No original request → error.
	_, err = BuildContinuationPrompt(nil, "x")
	assert.Error(t, err)
}

func TestContinuationAllowedWhitelist(t *testing.T) {
	cfg := DefaultStreamRecoveryConfig()
	assert.False(t, ContinuationAllowed(cfg, "coding", "model-a"), "L3 默认关")

	cfg.ContinuationEnabled = true
	assert.False(t, ContinuationAllowed(cfg, "coding", "model-a"),
		"空 whitelist = nobody qualifies (显式白名单才开启)")
	cfg.ContinuationTaskTypes = []string{"coding"}
	cfg.ContinuationModels = []string{"model-b"}
	assert.True(t, ContinuationAllowed(cfg, "coding", "whatever"))
	assert.True(t, ContinuationAllowed(cfg, "whatever", "model-b"))
	assert.False(t, ContinuationAllowed(cfg, "other", "model-a"))
}

// ── L2→L3 升级路径 ────────────────────────────────────────────────────────

func TestNextRecoveryActionAlignmentMissEscalates(t *testing.T) {
	resetStreamRecoveryMetricsForTest()
	defer resetStreamRecoveryMetricsForTest()
	err := errors.New("connection reset by peer")

	// Previous L2 attempt failed to align, L3 disabled → L4 fallback.
	state := &StreamRecoveryState{
		CommittedChunks: 4, SameNodeRetries: 2,
		RecoveryMode: RecoveryModeAligned, AlignmentScoreBP: 1200,
	}
	act := NextRecoveryAction(state, DefaultStreamRecoveryConfig(), err)
	assert.Equal(t, RecoveryActionErrorEnvelope, act.Kind)
	assert.EqualValues(t, 1, SnapshotStreamRecoveryMetrics().AlignmentMissTotal)

	// L3 enabled (whitelist validated by the caller) → continuation prompt.
	cfg := DefaultStreamRecoveryConfig()
	cfg.ContinuationEnabled = true
	act = NextRecoveryAction(state, cfg, err)
	assert.Equal(t, RecoveryActionContinuationPrompt, act.Kind)
	assert.Equal(t, RecoveryModeContinuation, act.Mode)
}
