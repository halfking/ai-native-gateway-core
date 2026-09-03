package streaming

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// stream_recovery_l2_wiring_test.go — UT-SR-L2-* (design
// resume-blocked-long-stream-recovery §六 P1 acceptance):
//
//	①帧边界不变量（suppress 偏移永不出现在帧中间 / 绝不转发重复字节）
//	②容量上限（1024 语义 + DroppedTotal）
//	③hash-only 路径（超 head 窗口的尾部哈希校验 hit/miss）
//	④L2 关闭 = 行为与现状一致（默认态回归保护）
//	+ Observe 语义接线（只有语义字节进缓存）与协调器端到端（命中续流/未命中信封）

// ── helpers ────────────────────────────────────────────────────────────────

func anthropicDeltaFrame(text string) string {
	return "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":" + jsonString(text) + "}}\n\n"
}

func jsonString(s string) string {
	return `"` + s + `"`
}

const l2TestMessageStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_replay\"}}\n\n"

// l2ProducerGate builds a buffered gate whose semantic writes feed the cache
// (the coordinator's wiring point 1 shape).
func l2ProducerGate(t *testing.T, reqID string, cache *CommittedPrefixCache) (*AttemptCommitGate, *trackingFlusher, *GateWriter) {
	t.Helper()
	f := &trackingFlusher{}
	sw := NewSerializedStreamWriter(f)
	gate := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, sw, GateOptions{
		Mode:          GateModeBuffered,
		RequestID:     reqID,
		PrefixObserve: cache.Observe,
	})
	return gate, f, NewGateWriter(gate)
}

// l2ReplayGate builds a replay gate armed against a committed snapshot.
func l2ReplayGate(t *testing.T, reqID string, prefix CommittedPrefix) (*AttemptCommitGate, *trackingFlusher, *GateWriter) {
	t.Helper()
	f := &trackingFlusher{}
	sw := NewSerializedStreamWriter(f)
	gate := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, sw, GateOptions{
		Mode:            GateModeBuffered,
		RequestID:       reqID,
		ReplayAlignment: &ReplayAlignmentOptions{Committed: prefix},
	})
	return gate, f, NewGateWriter(gate)
}

// splitCompleteFrames asserts every byte chunk is a whole SSE frame: buf must
// decompose exactly into frames each terminated by a blank line (UT-SR-L2-①
// primitive — a suppress offset may never surface mid-frame).
func splitCompleteFrames(t *testing.T, wire string) []string {
	t.Helper()
	var frames []string
	rest := wire
	for len(rest) > 0 {
		n := frameBoundary([]byte(rest))
		if n < 0 {
			t.Fatalf("wire ends with an unterminated frame (suppress offset leaked mid-frame): %q", rest)
		}
		frames = append(frames, rest[:n])
		rest = rest[n:]
	}
	return frames
}

// l2StreamExecutor scripts per-attempt streams: each attempt writes frames
// through the attempt's GateWriter and returns the scripted executor error.
type l2StreamExecutor struct {
	calls    int
	attempts []l2ScriptedAttempt
}

type l2ScriptedAttempt struct {
	frames []string
	err    error
}

func (e *l2StreamExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	i := e.calls
	e.calls++
	if i >= len(e.attempts) {
		return nil, errors.New("l2 scripted executor exhausted")
	}
	a := e.attempts[i]
	gw, ok := params.W.(*GateWriter)
	if !ok {
		return nil, errors.New("l2 scripted executor requires a gate writer")
	}
	for _, f := range a.frames {
		if _, err := gw.Write([]byte(f)); err != nil {
			// The gate refused the frame (e.g. ErrL2AlignmentMiss): surface
			// it as the attempt failure, mirroring a bridge write failure.
			return nil, &executors.ExecuteError{
				LastErr:  err,
				LastKind: errorsx.KindNetwork,
				Attempts: []executors.AttemptRecord{{ProviderID: 1, CredentialID: 1, Kind: errorsx.KindNetwork}},
			}
		}
	}
	if a.err != nil {
		return nil, a.err
	}
	return &executors.ExecuteResult{}, nil
}

// l2NetworkStreamFailure is the population's failure shape: a long-stream
// mid-flight network interruption (recoverable per ClassifyStreamError).
func l2NetworkStreamFailure() *executors.ExecuteError {
	return &executors.ExecuteError{
		LastErr:  errors.New("upstream stream interrupted: connection reset by peer"),
		LastKind: errorsx.KindNetwork,
		Attempts: []executors.AttemptRecord{{ProviderID: 1, CredentialID: 1, Kind: errorsx.KindNetwork}},
	}
}

func l2UnsetHoldbackEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS",
		"LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS",
	} {
		os.Unsetenv(k)
	}
}

// ── UT-SR-L2-① frame-boundary invariant ────────────────────────────────────

// TestUTSRL2FrameBoundaryInvariantExactMatch verifies that a replay which
// reproduces the committed prefix byte-for-byte suppresses the whole prefix
// and forwards only complete suffix frames: the wire never contains any
// duplicated byte nor a partial frame.
func TestUTSRL2FrameBoundaryInvariantExactMatch(t *testing.T) {
	reqID := "l2-boundary-exact"
	cache := NewCommittedPrefixCache(4, 64*1024)
	gate, _, gw := l2ProducerGate(t, reqID, cache)

	f1 := anthropicDeltaFrame("Hello ")
	f2 := anthropicDeltaFrame("world")
	f3 := anthropicDeltaFrame("!")
	f4 := "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"

	for _, fr := range []string{f1, f2} {
		if _, err := gw.Write([]byte(fr)); err != nil {
			t.Fatalf("producer write: %v", err)
		}
	}
	if !gate.Committed() {
		t.Fatal("producer gate must have committed on the first semantic frame")
	}
	prefix, ok := cache.Snapshot(reqID)
	if !ok {
		t.Fatal("committed prefix must be tracked")
	}

	rgate, rf, rgw := l2ReplayGate(t, reqID, prefix)
	for _, fr := range []string{l2TestMessageStart, f1, f2, f3, f4} {
		if _, err := rgw.Write([]byte(fr)); err != nil {
			t.Fatalf("replay write %q: %v", fr, err)
		}
	}
	if err := rgate.FinishAttempt(""); err != nil {
		t.Fatalf("replay finish: %v", err)
	}
	decided, res := rgate.AlignmentOutcome()
	if !decided || !res.Aligned {
		t.Fatalf("expected aligned verdict, got decided=%v res=%+v", decided, res)
	}

	wire := rf.buf.String()
	frames := splitCompleteFrames(t, wire)
	if len(frames) != 2 || frames[0] != f3 || frames[1] != f4 {
		t.Fatalf("wire must be exactly the suffix frames %q + %q, got %q", f3, f4, wire)
	}
	if strings.Contains(wire, "Hello ") || strings.Contains(wire, "world") || strings.Contains(wire, "message_start") {
		t.Fatalf("suppressed prefix leaked to the wire: %q", wire)
	}
	if !rgate.Committed() {
		t.Fatal("suffix forwarding must mark the replay gate committed")
	}
}

// TestUTSRL2FrameBoundaryInvariantMidFrameDivergence verifies the red-line
// behavior when the suffix offset lands INSIDE a frame (the replay diverges
// mid-frame near the end of the committed prefix): the straddling frame is
// suppressed whole (forwarding it would duplicate client-visible bytes) and
// forwarding resumes only at the next complete frame boundary.
func TestUTSRL2FrameBoundaryInvariantMidFrameDivergence(t *testing.T) {
	reqID := "l2-boundary-midframe"
	cache := NewCommittedPrefixCache(4, 64*1024)
	_, _, pgw := l2ProducerGate(t, reqID, cache)

	// committed: long frame (90 payload bytes) + short frame (10 bytes).
	f1 := anthropicDeltaFrame(strings.Repeat("a", 85)) // payload ≈ 90B
	f2 := anthropicDeltaFrame("0123456789")
	for _, fr := range []string{f1, f2} {
		if _, err := pgw.Write([]byte(fr)); err != nil {
			t.Fatalf("producer write: %v", err)
		}
	}
	prefix, ok := cache.Snapshot(reqID)
	if !ok {
		t.Fatal("committed prefix must be tracked")
	}

	// replay: f1 identical; f2' shares the first 9 payload bytes then
	// diverges (score ≥ 9000bp, offset lands inside f2'); f3 is new.
	f2Div := anthropicDeltaFrame("012345678X")
	f3 := anthropicDeltaFrame("suffix")
	rgate, rf, rgw := l2ReplayGate(t, reqID, prefix)
	for _, fr := range []string{l2TestMessageStart, f1, f2Div, f3} {
		if _, err := rgw.Write([]byte(fr)); err != nil {
			t.Fatalf("replay write: %v", err)
		}
	}
	decided, res := rgate.AlignmentOutcome()
	if !decided || !res.Aligned {
		t.Fatalf("expected aligned verdict for a ≥9000bp match, got res=%+v", res)
	}
	if res.SuffixOffset <= prefixAtFrame(prefix, f1) || res.SuffixOffset >= committedNormLen(prefix) {
		t.Fatalf("test setup: offset %d must land inside the last committed frame (head=%dB total=%dB)",
			res.SuffixOffset, len(prefix.Head), prefix.TotalBytes)
	}

	wire := rf.buf.String()
	frames := splitCompleteFrames(t, wire)
	if len(frames) != 1 || frames[0] != f3 {
		t.Fatalf("only the post-boundary frame may be forwarded, got %q", wire)
	}
	// The straddling frame must be absent ENTIRELY — a single byte of it on
	// the wire means the suppress offset surfaced mid-frame (duplication).
	if strings.Contains(wire, "01234567") {
		t.Fatalf("straddling frame leaked (partial or whole): %q", wire)
	}
}

// committedNormLen returns prefix.TotalBytes (all normalized semantic bytes).
func committedNormLen(prefix CommittedPrefix) int { return prefix.TotalBytes }

// prefixAtFrame returns the normalized offset after the first frame's
// payload, i.e. the byte offset where frame f1's contribution ends in the
// committed stream (for the divergence-offset sanity check above).
func prefixAtFrame(prefix CommittedPrefix, frame string) int {
	cls := ClassifyClientFrame(ProtocolAnthropic, frame)
	return len(l2SemanticObservation(cls, frame))
}

// TestUTSRL2FrameBoundaryInvariantKeepaliveDuringAlignment verifies that
// transport keepalives keep flowing to the client while semantic frames are
// suppressed (the alignment window must not stall the connection) and never
// feed the committed prefix.
func TestUTSRL2FrameBoundaryInvariantKeepaliveDuringAlignment(t *testing.T) {
	reqID := "l2-boundary-keepalive"
	cache := NewCommittedPrefixCache(4, 64*1024)
	_, _, pgw := l2ProducerGate(t, reqID, cache)
	f1 := anthropicDeltaFrame("Hello ")
	f2 := anthropicDeltaFrame("world")
	for _, fr := range []string{f1, f2} {
		if _, err := pgw.Write([]byte(fr)); err != nil {
			t.Fatalf("producer write: %v", err)
		}
	}
	prefix, _ := cache.Snapshot(reqID)

	rgate, rf, rgw := l2ReplayGate(t, reqID, prefix)
	// While the alignment window is open (undecided), a keepalive comment
	// must pass straight through and the semantic frames must not.
	if _, err := rgw.Write([]byte(f1)); err != nil {
		t.Fatalf("replay write f1: %v", err)
	}
	if _, err := rgw.Write([]byte(": gw-survival-keepalive\n\n")); err != nil {
		t.Fatalf("replay keepalive: %v", err)
	}
	if got := rf.buf.String(); got != ": gw-survival-keepalive\n\n" {
		t.Fatalf("keepalive must be the only wire bytes during the alignment window, got %q", got)
	}
	if _, err := rgw.Write([]byte(f2)); err != nil {
		t.Fatalf("replay write f2: %v", err)
	}
	if _, err := rgw.Write([]byte(anthropicDeltaFrame("!"))); err != nil {
		t.Fatalf("replay write suffix: %v", err)
	}
	decided, res := rgate.AlignmentOutcome()
	if !decided || !res.Aligned {
		t.Fatalf("expected aligned verdict, got %+v", res)
	}
	if got := rf.buf.String(); got != ": gw-survival-keepalive\n\n"+anthropicDeltaFrame("!") {
		t.Fatalf("unexpected wire %q", got)
	}
}

// ── UT-SR-L2-② capacity bound ──────────────────────────────────────────────

// TestUTSRL2CacheCapacityBoundedDrop verifies the 1024-default semantics at a
// small capacity: the cache never grows past capacity, new tracking past the
// bound is refused (DroppedTotal counts it) and LIVE entries are never evicted.
func TestUTSRL2CacheCapacityBoundedDrop(t *testing.T) {
	cache := NewCommittedPrefixCache(2, 64*1024)
	cache.Observe("req-1", []byte("alpha"))
	cache.Observe("req-2", []byte("beta"))
	if cache.Len() != 2 {
		t.Fatalf("len = %d, want 2", cache.Len())
	}
	cache.Observe("req-3", []byte("gamma")) // capacity hit → dropped
	if cache.Len() != 2 {
		t.Fatalf("len after overflow = %d, want 2 (never exceeds capacity)", cache.Len())
	}
	if got := cache.DroppedTotal(); got != 1 {
		t.Fatalf("DroppedTotal = %d, want 1", got)
	}
	if _, ok := cache.Snapshot("req-3"); ok {
		t.Fatal("refused entry must not be tracked")
	}
	for _, id := range []string{"req-1", "req-2"} {
		p, ok := cache.Snapshot(id)
		if !ok || p.TotalBytes == 0 {
			t.Fatalf("live entry %s must survive the overflow (never evict live tracking)", id)
		}
	}
	// Terminal cleanup drops the entry and frees a slot.
	cache.Remove("req-1")
	cache.Observe("req-4", []byte("delta"))
	if cache.Len() != 2 || cache.DroppedTotal() != 1 {
		t.Fatalf("after Remove the slot must be reusable: len=%d dropped=%d", cache.Len(), cache.DroppedTotal())
	}
}

// TestUTSRL2CacheDefaultCapacityIs1024 pins the spec default (设计 §3.3
// point 5: 容量沿用既有默认 1024).
func TestUTSRL2CacheDefaultCapacityIs1024(t *testing.T) {
	t.Setenv("LLM_GATEWAY_RECOVERY_L2_CACHE_CAPACITY", "")
	t.Setenv("LLM_GATEWAY_RECOVERY_L2_WINDOW_BYTES", "")
	resetRecoveryL2CacheForTest()
	cache := CommittedPrefixCacheShared()
	for i := 0; i < DefaultCommittedPrefixCacheCapacity+3; i++ {
		cache.Observe(string(rune('a'+i%26))+string(rune('0'+i/26)), []byte("x"))
	}
	if got := cache.DroppedTotal(); got != 3 {
		t.Fatalf("dropped = %d, want 3 (capacity %d held)", got, DefaultCommittedPrefixCacheCapacity)
	}
	resetRecoveryL2CacheForTest()
}

// ── UT-SR-L2-③ hash-only tail verification ────────────────────────────────

// TestUTSRL2HashOnlyTailVerification drives a committed prefix larger than
// the cache's head window: the head is byte-compared, the tail only
// hash-verified (all-or-nothing). Hit → aligned with HashOnly and the suffix
// forwarded from the verified boundary; miss → attempt voided, nothing on
// the wire.
func TestUTSRL2HashOnlyTailVerification(t *testing.T) {
	// Window smaller than the payloads → the committed record degrades to
	// head-bytes + full-stream hash.
	window := 48
	cache := NewCommittedPrefixCache(4, window)
	reqID := "l2-hash-only"

	gate, _, gw := l2ProducerGate(t, reqID, cache)
	f1 := anthropicDeltaFrame(strings.Repeat("h", 60))
	f2 := anthropicDeltaFrame(strings.Repeat("t", 55))
	for _, fr := range []string{f1, f2} {
		if _, err := gw.Write([]byte(fr)); err != nil {
			t.Fatalf("producer write: %v", err)
		}
	}
	if !gate.Committed() {
		t.Fatal("producer gate must have committed")
	}
	prefix, ok := cache.Snapshot(reqID)
	if !ok {
		t.Fatal("prefix missing")
	}
	if prefix.TotalBytes <= len(prefix.Head) {
		t.Fatalf("test setup: want hash-only record, got total=%d head=%d", prefix.TotalBytes, len(prefix.Head))
	}

	t.Run("hit", func(t *testing.T) {
		suffix := anthropicDeltaFrame("tail-suffix")
		rgate, rf, rgw := l2ReplayGate(t, reqID, prefix)
		for _, fr := range []string{l2TestMessageStart, f1, f2, suffix} {
			if _, err := rgw.Write([]byte(fr)); err != nil {
				t.Fatalf("replay write: %v", err)
			}
		}
		decided, res := rgate.AlignmentOutcome()
		if !decided || !res.Aligned || !res.HashOnly {
			t.Fatalf("want hash-only aligned verdict, got decided=%v res=%+v", decided, res)
		}
		if res.SuffixOffset != prefix.TotalBytes {
			t.Fatalf("suffix offset = %d, want %d (tail hash boundary)", res.SuffixOffset, prefix.TotalBytes)
		}
		frames := splitCompleteFrames(t, rf.buf.String())
		if len(frames) != 1 || frames[0] != suffix {
			t.Fatalf("wire must be exactly the verified suffix, got %q", rf.buf.String())
		}
	})

	t.Run("miss", func(t *testing.T) {
		f2Div := anthropicDeltaFrame(strings.Repeat("X", 55)) // head window still matches, tail hash must not
		rgate, rf, rgw := l2ReplayGate(t, reqID, prefix)
		// f1 alone does not fill the decision window (needBytes == TotalBytes):
		// the write is swallowed while buffering.
		if _, err := rgw.Write([]byte(f1)); err != nil {
			t.Fatalf("replay write f1 (still buffering): %v", err)
		}
		// f2Div fills the window: head bytes match but the tail hash cannot —
		// the write fails with the miss sentinel and every later frame too.
		for _, fr := range []string{f2Div, anthropicDeltaFrame("never-seen")} {
			if _, err := rgw.Write([]byte(fr)); err == nil {
				t.Fatalf("replay write %q must fail after the verdict", fr)
			}
		}
		decided, res := rgate.AlignmentOutcome()
		if !decided || res.Aligned {
			t.Fatalf("want a miss verdict, got decided=%v res=%+v", decided, res)
		}
		if !res.HashOnly {
			t.Fatal("miss on a beyond-window prefix must report hash_only")
		}
		if rf.buf.Len() != 0 {
			t.Fatalf("a miss must leave the wire untouched, got %q", rf.buf.String())
		}
	})
}

// ── UT-SR-L2 observation semantics (wiring point 1) ───────────────────────

// TestUTSRL2ObserveFoldsOnlySemanticWireBytes verifies the Observe contract:
// exactly the client-bound semantic bytes (SSE data payloads of content /
// tool-call / terminal / unknown frames) feed the cache — never transport
// keepalives, comments, attempt metadata or error frames — and Remove drops
// the request entry at terminal.
func TestUTSRL2ObserveFoldsOnlySemanticWireBytes(t *testing.T) {
	reqID := "l2-observe-semantics"
	cache := NewCommittedPrefixCache(4, 64*1024)
	gate, f, gw := l2ProducerGate(t, reqID, cache)

	keepalive := ": gw-survival-keepalive\n\n"
	meta := l2TestMessageStart
	content := anthropicDeltaFrame("Hello ")
	terminal := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	errFrame := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"x\"}}\n\n"

	for _, fr := range []string{keepalive, meta, content} {
		if _, err := gw.Write([]byte(fr)); err != nil {
			t.Fatalf("write %q: %v", fr, err)
		}
	}
	// Error frames never feed the committed prefix (error path, not content).
	if _, err := gw.Write([]byte(errFrame)); err != nil {
		t.Fatalf("write error frame: %v", err)
	}
	if err := gw.Finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := gate.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// Trailing terminal partial through FinishAttempt's committed path.
	if err := gate.WriteFrame(terminal); err != nil {
		t.Fatalf("write terminal: %v", err)
	}
	if !strings.Contains(f.buf.String(), "Hello ") {
		t.Fatal("sanity: content reached the wire")
	}

	prefix, ok := cache.Snapshot(reqID)
	if !ok {
		t.Fatal("prefix missing")
	}
	wantHead := string(l2SemanticObservation(FrameClassContent, content)) +
		string(l2SemanticObservation(FrameClassTerminal, terminal))
	if string(prefix.Head) != wantHead {
		t.Fatalf("observed head = %q, want %q (semantic payloads only)", prefix.Head, wantHead)
	}
	if prefix.Hash != fnv64a([]byte(wantHead)) {
		t.Fatal("observed hash must cover exactly the semantic payloads")
	}
	// Keepalive / metadata / error frames contributed nothing.
	if strings.Contains(string(prefix.Head), "message_start") || strings.Contains(string(prefix.Head), "keepalive") {
		t.Fatalf("non-semantic frames leaked into the prefix: %q", prefix.Head)
	}

	cache.Remove(reqID)
	if _, ok := cache.Snapshot(reqID); ok {
		t.Fatal("Remove must drop the entry at request terminal")
	}
}

// TestUTSRL2ObserveHoldbackFlushCoversHeldChunks verifies the L1-interaction
// shape: frames released by the holdback window (flushed as a raw buffer at
// commit) are observed in wire order — the observation must survive the
// buffered flush path, not just direct writes.
func TestUTSRL2ObserveHoldbackFlushCoversHeldChunks(t *testing.T) {
	l2UnsetHoldbackEnv(t)
	reqID := "l2-observe-holdback"
	cache := NewCommittedPrefixCache(4, 64*1024)
	f := &trackingFlusher{}
	sw := NewSerializedStreamWriter(f)
	gate := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, sw, GateOptions{
		Mode:              GateModeBuffered,
		RequestID:         reqID,
		PrefixObserve:     cache.Observe,
		HoldbackWindow:    time.Minute,
		HoldbackMaxChunks: 10,
	})
	gw := NewGateWriter(gate)

	held1 := anthropicDeltaFrame("held-one ")
	held2 := anthropicDeltaFrame("held-two")
	for _, fr := range []string{l2TestMessageStart, held1, held2} {
		if _, err := gw.Write([]byte(fr)); err != nil {
			t.Fatalf("write %q: %v", fr, err)
		}
	}
	if gate.Committed() {
		t.Fatal("holdback window must still hold the frames")
	}
	if err := gate.FlushHoldback(); err != nil {
		t.Fatalf("flush holdback: %v", err)
	}
	prefix, ok := cache.Snapshot(reqID)
	if !ok {
		t.Fatal("held chunks must be observed once wire-proven")
	}
	want := string(l2SemanticObservation(FrameClassContent, held1)) +
		string(l2SemanticObservation(FrameClassContent, held2))
	if string(prefix.Head) != want {
		t.Fatalf("observed head = %q, want %q", prefix.Head, want)
	}
}

// ── UT-SR-L2-④ default-off regression protection ──────────────────────────

// TestUTSRL2DisabledByDefaultKeepsLegacyBehavior verifies the hard invariant:
// with LLM_GATEWAY_RECOVERY_L2_ENABLED unset (the default) a committed_output
// interruption behaves exactly as before — one attempt, resume_blocked, a
// single committed terminal render — and the committed-prefix cache is never
// written (Observe never runs on the hot path).
func TestUTSRL2DisabledByDefaultKeepsLegacyBehavior(t *testing.T) {
	l2UnsetHoldbackEnv(t)
	os.Unsetenv("LLM_GATEWAY_RECOVERY_L2_ENABLED")

	if RecoveryL2EnabledFromEnv() {
		t.Fatal("L2 must default to disabled")
	}

	reqID := "l2-default-off-req"
	exec := &l2StreamExecutor{attempts: []l2ScriptedAttempt{
		{frames: []string{anthropicDeltaFrame("committed bytes")}, err: l2NetworkStreamFailure()},
	}}
	h := newCoordHarness(nil)
	c := h.coordinator()
	c.Exec = exec

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{RequestID: reqID, IsStream: true})

	if res.Succeed {
		t.Fatal("resume-blocked must not succeed")
	}
	if res.Decision.Action != TaskActionResumeBlocked {
		t.Fatalf("decision = %v (%s), want resume_blocked", res.Decision.Action, res.Decision.Reason)
	}
	if res.Decision.Reason == "l2_alignment_miss" {
		t.Fatal("default-off path must never produce L2 verdicts")
	}
	if exec.calls != 1 {
		t.Fatalf("executor calls = %d, want 1 (no L2 replay may be armed)", exec.calls)
	}
	if len(h.terminals) != 1 || h.terminals[0].Action != TaskActionResumeBlocked || !h.committeds[0] {
		t.Fatalf("legacy terminal render expected, got %+v committed=%v", h.terminals, h.committeds)
	}
	// The wire carries exactly the committed attempt bytes — nothing else.
	if got, want := h.flusher.buf.String(), anthropicDeltaFrame("committed bytes"); got != want {
		t.Fatalf("wire = %q, want %q (byte-identical legacy behavior)", got, want)
	}
	// No prefix observation ever ran.
	if _, ok := CommittedPrefixCacheShared().Snapshot(reqID); ok {
		t.Fatal("with L2 disabled the committed-prefix cache must stay untouched")
	}
}

// TestUTSRL2EnabledEnvParsing pins the switch parsing (default false, the
// design's 灰度 knob).
func TestUTSRL2EnabledEnvParsing(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"", false}, {"0", false}, {"false", false}, {"no", false}, {"off", false}, {"garbage", false},
		{"1", true}, {"true", true}, {"YES", true}, {"On", true},
	} {
		t.Setenv("LLM_GATEWAY_RECOVERY_L2_ENABLED", tc.val)
		if got := RecoveryL2EnabledFromEnv(); got != tc.want {
			t.Fatalf("env %q → %v, want %v", tc.val, got, tc.want)
		}
	}
}

func TestUTSRL2ShadowKeepsCommittedOutputBehaviorByteIdentical(t *testing.T) {
	l2UnsetHoldbackEnv(t)
	os.Unsetenv("LLM_GATEWAY_RECOVERY_L2_ENABLED")

	type runResult struct {
		wire     string
		calls    int
		attempts int
		decision TaskDecision
	}
	run := func(mode RecoveryL2Mode) runResult {
		t.Setenv("LLM_GATEWAY_RECOVERY_L2_MODE", string(mode))
		exec := &l2StreamExecutor{attempts: []l2ScriptedAttempt{{
			frames: []string{anthropicDeltaFrame("committed bytes")}, err: l2NetworkStreamFailure(),
		}}}
		h := newCoordHarness(nil)
		c := h.coordinator()
		c.Exec = exec
		c.PrefixCache = NewCommittedPrefixCache(4, 64*1024)
		res := c.Run(context.Background(), h.sw, &executors.ExecParams{RequestID: "l2-shadow-equivalence-" + string(mode), IsStream: true})
		return runResult{wire: h.flusher.buf.String(), calls: exec.calls, attempts: res.Attempts, decision: res.Decision}
	}

	off := run(RecoveryL2ModeOff)
	shadow := run(RecoveryL2ModeShadow)
	if off.wire != shadow.wire {
		t.Fatalf("shadow changed wire bytes:\noff=%q\nshadow=%q", off.wire, shadow.wire)
	}
	if off.calls != 1 || shadow.calls != 1 {
		t.Fatalf("executor calls off/shadow = %d/%d, want 1/1 (no speculative replay)", off.calls, shadow.calls)
	}
	if off.attempts != shadow.attempts || off.decision != shadow.decision {
		t.Fatalf("shadow changed recovery outcome: off=%+v/%d shadow=%+v/%d", off.decision, off.attempts, shadow.decision, shadow.attempts)
	}
	if shadow.decision.Reason == "l2_alignment_miss" {
		t.Fatal("shadow must not convert a legacy resume_blocked outcome into l2_alignment_miss")
	}
}

func TestUTSRL2ShadowAlignedNeverSuppressesFrames(t *testing.T) {
	reqID := "l2-shadow-aligned"
	cache := NewCommittedPrefixCache(4, 64*1024)
	_, _, producer := l2ProducerGate(t, reqID, cache)
	f1 := anthropicDeltaFrame("already committed ")
	f2 := anthropicDeltaFrame("prefix")
	for _, frame := range []string{f1, f2} {
		if _, err := producer.Write([]byte(frame)); err != nil {
			t.Fatalf("producer write: %v", err)
		}
	}
	prefix, ok := cache.Snapshot(reqID)
	if !ok {
		t.Fatal("expected committed prefix")
	}

	flusher := &trackingFlusher{}
	var results []ShadowAlignmentResult
	gate := NewAttemptCommitGate(context.Background(), ProtocolAnthropic, NewSerializedStreamWriter(flusher), GateOptions{
		Mode:            GateModeBuffered,
		RequestID:       reqID,
		ShadowAlignment: &ShadowAlignmentOptions{Committed: prefix},
		ShadowAlignmentResult: func(result ShadowAlignmentResult) {
			results = append(results, result)
		},
	})
	writer := NewGateWriter(gate)
	for _, frame := range []string{f1, f2} {
		if _, err := writer.Write([]byte(frame)); err != nil {
			t.Fatalf("shadow write: %v", err)
		}
	}
	gate.FinishShadowObservation()
	gate.FinishShadowObservation()

	if got, want := flusher.buf.String(), f1+f2; got != want {
		t.Fatalf("shadow suppressed or changed wire bytes: got %q, want %q", got, want)
	}
	if len(results) != 1 {
		t.Fatalf("shadow result callback count = %d, want 1", len(results))
	}
	if got := results[0]; got.Result != "aligned" || !got.Aligned || got.Applied || !got.ClientBytesUnchanged || !got.RecoveryBehaviorUnchanged {
		t.Fatalf("shadow result = %+v, want aligned and purely observational", got)
	}
}

// TestUTSRL2ModeParsingAndLegacyCompatibility pins the three-state rollout
// contract. MODE is authoritative whenever present; the old boolean only
// maps to enforce when MODE is absent, so a persisted shadow/off value cannot
// accidentally be overridden by a stale legacy setting.
func TestUTSRL2ModeParsingAndLegacyCompatibility(t *testing.T) {
	tests := []struct {
		name    string
		modeSet bool
		mode    string
		legacy  string
		want    RecoveryL2Mode
	}{
		{name: "default off", want: RecoveryL2ModeOff},
		{name: "legacy true maps enforce", legacy: "true", want: RecoveryL2ModeEnforce},
		{name: "legacy false maps off", legacy: "false", want: RecoveryL2ModeOff},
		{name: "explicit shadow wins legacy true", modeSet: true, mode: " shadow ", legacy: "true", want: RecoveryL2ModeShadow},
		{name: "explicit off wins legacy true", modeSet: true, mode: "off", legacy: "true", want: RecoveryL2ModeOff},
		{name: "explicit enforce", modeSet: true, mode: "ENFORCE", legacy: "false", want: RecoveryL2ModeEnforce},
		{name: "explicit empty is safe off", modeSet: true, mode: "", legacy: "true", want: RecoveryL2ModeOff},
		{name: "invalid is safe off", modeSet: true, mode: "replay-everything", legacy: "true", want: RecoveryL2ModeOff},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.modeSet {
				t.Setenv("LLM_GATEWAY_RECOVERY_L2_MODE", tc.mode)
			} else {
				os.Unsetenv("LLM_GATEWAY_RECOVERY_L2_MODE")
			}
			if tc.legacy == "" {
				os.Unsetenv("LLM_GATEWAY_RECOVERY_L2_ENABLED")
			} else {
				t.Setenv("LLM_GATEWAY_RECOVERY_L2_ENABLED", tc.legacy)
			}
			if got := RecoveryL2ModeFromEnv(); got != tc.want {
				t.Fatalf("mode = %q, want %q", got, tc.want)
			}
		})
	}
}

// ── coordinator end-to-end (wiring points 2 + 3) ──────────────────────────

// TestUTSRL2CoordinatorAlignedReplayContinuesStream verifies the full happy
// path: committed interruption → ladder says aligned continuation → the
// replay gate suppresses the reproduced prefix and forwards only the suffix →
// request succeeds with zero duplicated bytes and the aligned success metric
// recorded; the cache entry is removed at terminal.
func TestUTSRL2CoordinatorAlignedReplayContinuesStream(t *testing.T) {
	l2UnsetHoldbackEnv(t)
	t.Setenv("LLM_GATEWAY_RECOVERY_L2_ENABLED", "true")
	resetStreamRecoveryMetricsForTest()

	reqID := "l2-e2e-aligned"
	f1 := anthropicDeltaFrame("Hello ")
	f2 := anthropicDeltaFrame("world")
	f3 := anthropicDeltaFrame("!")
	stop := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	exec := &l2StreamExecutor{attempts: []l2ScriptedAttempt{
		// attempt 1: commits two deltas, then the upstream connection dies.
		{frames: []string{l2TestMessageStart, f1, f2}, err: l2NetworkStreamFailure()},
		// attempt 2 (L2 replay): regenerates everything + a genuine suffix.
		{frames: []string{l2TestMessageStart, f1, f2, f3, stop}},
	}}
	h := newCoordHarness(nil)
	c := h.coordinator()
	c.Exec = exec
	cache := NewCommittedPrefixCache(4, 64*1024)
	c.PrefixCache = cache

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{RequestID: reqID, IsStream: true})

	if !res.Succeed {
		t.Fatalf("aligned replay must complete the request, decision=%v (%s) err=%v",
			res.Decision.Action, res.Decision.Reason, res.FinalAttempt.FinalError)
	}
	if exec.calls != 2 {
		t.Fatalf("executor calls = %d, want 2 (interrupted attempt + one aligned replay)", exec.calls)
	}
	if res.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", res.Attempts)
	}

	// Wire: original attempt frames + ONLY the replay's suffix. The replay's
	// regenerated prefix (message_start, f1, f2) must never appear twice.
	want := l2TestMessageStart + f1 + f2 + f3 + stop
	if got := h.flusher.buf.String(); got != want {
		t.Fatalf("wire mismatch:\n got %q\nwant %q", got, want)
	}
	if got := strings.Count(h.flusher.buf.String(), "Hello "); got != 1 {
		t.Fatalf("committed content must appear exactly once, got %d", got)
	}
	if len(h.terminals) != 0 {
		t.Fatalf("a surviving request must not render a terminal, got %+v", h.terminals)
	}
	snap := SnapshotStreamRecoveryMetrics()
	if snap.RecoverySuccessByMode[RecoveryModeAligned.String()] != 1 {
		t.Fatalf("aligned recovery success metric = %v, want 1", snap.RecoverySuccessByMode)
	}
	if cache.Len() != 0 {
		t.Fatalf("cache entry must be removed at request terminal, len=%d", cache.Len())
	}
}

// TestUTSRL2CoordinatorMissDegradesToResumeBlockedEnvelope verifies the miss
// path: the replay diverges below the threshold → the attempt is voided
// (nothing client-visible from it) → the existing resume_blocked envelope
// runs — exactly one extra attempt, no duplicated bytes on the wire.
func TestUTSRL2CoordinatorMissDegradesToResumeBlockedEnvelope(t *testing.T) {
	l2UnsetHoldbackEnv(t)
	t.Setenv("LLM_GATEWAY_RECOVERY_L2_ENABLED", "true")

	reqID := "l2-e2e-miss"
	f1 := anthropicDeltaFrame("Hello ")
	f2 := anthropicDeltaFrame("world")
	diverged := anthropicDeltaFrame("totally different replay")

	exec := &l2StreamExecutor{attempts: []l2ScriptedAttempt{
		{frames: []string{l2TestMessageStart, f1, f2}, err: l2NetworkStreamFailure()},
		{frames: []string{l2TestMessageStart, diverged, anthropicDeltaFrame("never forwarded")}},
		// A third attempt must never run.
		{frames: []string{f1}},
	}}
	h := newCoordHarness(nil)
	c := h.coordinator()
	c.Exec = exec
	c.PrefixCache = NewCommittedPrefixCache(4, 64*1024)

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{RequestID: reqID, IsStream: true})

	if res.Succeed {
		t.Fatal("a replay miss must not succeed")
	}
	if res.Decision.Action != TaskActionResumeBlocked {
		t.Fatalf("decision = %v, want resume_blocked", res.Decision.Action)
	}
	if res.Decision.Reason != "l2_alignment_miss" {
		t.Fatalf("reason = %q, want l2_alignment_miss", res.Decision.Reason)
	}
	if exec.calls != 2 {
		t.Fatalf("executor calls = %d, want 2 (no retry after a miss)", exec.calls)
	}
	if len(h.terminals) != 1 || !h.committeds[0] {
		t.Fatalf("miss must degrade to the committed envelope, terminals=%v committeds=%v", h.terminals, h.committeds)
	}
	// Wire: only the original attempt's bytes — the divergent replay wrote
	// nothing to the client.
	want := l2TestMessageStart + f1 + f2
	if got := h.flusher.buf.String(); got != want {
		t.Fatalf("wire mismatch:\n got %q\nwant %q", got, want)
	}
}

// TestUTSRL2CoordinatorBudgetBoundsReplays verifies the ladder's recovery
// budget: after DefaultStreamRecoveryMaxAttempts aligned replays the ask
// degrades to the error envelope instead of looping forever.
func TestUTSRL2CoordinatorBudgetBoundsReplays(t *testing.T) {
	l2UnsetHoldbackEnv(t)
	t.Setenv("LLM_GATEWAY_RECOVERY_L2_ENABLED", "true")

	reqID := "l2-e2e-budget"
	f1 := anthropicDeltaFrame("Hello ")
	f2 := anthropicDeltaFrame("world")

	// Every attempt commits the same prefix and dies mid-stream: each replay
	// aligns, forwards nothing new (stream ends at the same point), fails
	// again → re-ask until the budget is exhausted.
	attempts := make([]l2ScriptedAttempt, 0, DefaultStreamRecoveryMaxAttempts+2)
	fail := l2ScriptedAttempt{frames: []string{l2TestMessageStart, f1, f2}, err: l2NetworkStreamFailure()}
	for i := 0; i < DefaultStreamRecoveryMaxAttempts+2; i++ {
		attempts = append(attempts, fail)
	}
	exec := &l2StreamExecutor{attempts: attempts}
	h := newCoordHarness(nil)
	c := h.coordinator()
	c.Exec = exec
	c.PrefixCache = NewCommittedPrefixCache(4, 64*1024)

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{RequestID: reqID, IsStream: true})

	if res.Succeed {
		t.Fatal("budget test must not succeed")
	}
	// 1 original + DefaultStreamRecoveryMaxAttempts armed replays.
	wantCalls := 1 + DefaultStreamRecoveryMaxAttempts
	if exec.calls != wantCalls {
		t.Fatalf("executor calls = %d, want %d (ladder budget bounds the replays)", exec.calls, wantCalls)
	}
	if res.Decision.Action != TaskActionResumeBlocked {
		t.Fatalf("decision = %v, want resume_blocked after budget exhaustion", res.Decision.Action)
	}
}
