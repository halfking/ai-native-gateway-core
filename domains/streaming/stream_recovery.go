package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// stream_recovery.go — 会话优化 v4 FR-12（首字节后流中断透明恢复，T13-lite）
//
// Implements the L0–L4 recovery ladder for post-first-byte stream
// interruptions:
//
//	L0 同节点重发（闪断首选，保 prompt cache 亲和，默认 2 次）
//	L1 可撤销窗口（首个语义内容后 5s / 20 chunks 先到者，窗口内中断丢弃
//	   缓冲按「原节点→同模型其它节点→同品质模型」重执行，客户端零感知）
//	L2 CommittedPrefix 前缀对齐（hash + 有限窗口字节；分数≥阈值抑制重复
//	   前缀只转发 suffix）
//	L3 continuation prompt（纯函数构造，默认关、白名单启用）
//	L4 降级（stream-undo 撤销控制事件 / 通用客户端 restart 注释 + thinking
//	   + 完整重生成；策略禁止重复时回退错误信封）
//
// StreamRecoveryState is request-scoped only (R12.3): it never enters URSM;
// the terminal summary is journaled by the caller. The ADR-Disp-007
// cross-credential gate is CrossCredentialUnlockAllowed (R12.6).

// ── R12.3 request-scoped recovery state ────────────────────────────────────

// RecoveryMode mirrors the R12.3 mode enum.
type RecoveryMode uint8

const (
	// RecoveryModeNone is the zero value (no recovery attempted yet).
	RecoveryModeNone RecoveryMode = iota
	// RecoveryModeSameNode is L0.
	RecoveryModeSameNode
	// RecoveryModeDiscardReplay is L1.
	RecoveryModeDiscardReplay
	// RecoveryModeAligned is L2.
	RecoveryModeAligned
	// RecoveryModeContinuation is L3.
	RecoveryModeContinuation
	// RecoveryModeRestart is L4 (visible restart).
	RecoveryModeRestart
	// RecoveryModeError is the L4 fallback envelope.
	RecoveryModeError
)

// String implements fmt.Stringer (metrics labels, journey summaries).
func (m RecoveryMode) String() string {
	switch m {
	case RecoveryModeSameNode:
		return "same_node"
	case RecoveryModeDiscardReplay:
		return "discard_replay"
	case RecoveryModeAligned:
		return "aligned"
	case RecoveryModeContinuation:
		return "continuation"
	case RecoveryModeRestart:
		return "restart"
	case RecoveryModeError:
		return "error"
	default:
		return "none"
	}
}

// StreamRecoveryState is the per-request FR-12 recovery bookkeeping (R12.3
// struct verbatim). Not persisted to URSM; the caller mirrors the terminal
// summary into request_logs/journey/TurnArtifactBlock attempt metadata.
type StreamRecoveryState struct {
	RecoveryNo         uint16
	SameNodeRetries    uint8
	LastNodeID         int32
	LastModelID        int32
	CommittedChunks    uint32
	CommittedBytes     uint32
	HoldbackChunks     uint16
	HoldbackDeadlineMS int64
	AlignmentScoreBP   uint16 // 0..10000 basis points
	RecoveryMode       RecoveryMode
	VisibleToClient    bool
}

// ── R12.1 trigger classification ───────────────────────────────────────────

// StreamInterruptClass is the closed R12.1 trigger vocabulary.
type StreamInterruptClass string

const (
	InterruptConnectionReset StreamInterruptClass = "connection_reset"
	InterruptUnexpectedEOF   StreamInterruptClass = "unexpected_eof"
	InterruptEOFWithoutDone  StreamInterruptClass = "eof_without_done"
	InterruptStreamTimeout   StreamInterruptClass = "stream_timeout"
	InterruptChunkTimeout    StreamInterruptClass = "chunk_timeout"
	InterruptSlowDripDead    StreamInterruptClass = "slow_drip_dead"
	InterruptUpstreamClose   StreamInterruptClass = "upstream_close"
)

// ClassifyStreamError decides whether one stream failure belongs to FR-12
// recovery (shouldRecover=true — never terminate directly) or to the
// legacy outcome path (directive errors, client aborts: R2.2/R2.5).
//
// Trigger vocabulary (UT-SR-01): connection_reset / unexpected_eof /
// eof_without_done / stream_timeout / chunk_timeout / slow-drip 判死 (+
// upstream_close). Message matching first (bridge outcomes carry reason
// strings like "stream_chunk_timeout"), then errorsx kinds.
func ClassifyStreamError(err error) (class StreamInterruptClass, shouldRecover bool) {
	if err == nil {
		return "", false
	}
	// R2.5: 客户端主动断开立即停止调度，不进恢复。
	if errors.Is(err, context.Canceled) {
		return "", false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "slow_drip"):
		return InterruptSlowDripDead, true
	case strings.Contains(msg, "eof_without_done") ||
		strings.Contains(msg, "eof without") ||
		strings.Contains(msg, "stream closed before"):
		return InterruptEOFWithoutDone, true
	case errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(msg, "unexpected eof"):
		return InterruptUnexpectedEOF, true
	case strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection aborted"):
		return InterruptConnectionReset, true
	case strings.Contains(msg, "stream_chunk_timeout") || strings.Contains(msg, "chunk_timeout"):
		return InterruptChunkTimeout, true
	case strings.Contains(msg, "stream_timeout"):
		return InterruptStreamTimeout, true
	case strings.Contains(msg, "upstream_close") || strings.Contains(msg, "upstream closed"):
		return InterruptUpstreamClose, true
	}
	switch errorsx.ClassifyError(err, nil) {
	case errorsx.KindCanceled, errorsx.KindClientBug:
		// Client abort / client-side protocol bug: not recoverable.
		return "", false
	case errorsx.KindStreamTimeout, errorsx.KindTimeout:
		return InterruptStreamTimeout, true
	case errorsx.KindNetwork:
		return InterruptConnectionReset, true
	case errorsx.KindUpstreamDown:
		return InterruptUpstreamClose, true
	default:
		// Directive errors (auth/model_not_found/context_length/…) and
		// everything else: legacy path, direct termination allowed.
		return "", false
	}
}

// ── recovery policy machine ────────────────────────────────────────────────

// NodeSelectionPolicy orders the L1/L2 re-execution ladder (R12.2 L1:
// 原节点 → 同模型其它节点 → 同品质模型).
type NodeSelectionPolicy uint8

const (
	// NodePolicyOriginal keeps the prompt-cache/connection affinity (L0/L2
	// first try). Same credential — legal under ADR-Disp-003 as-is.
	NodePolicyOriginal NodeSelectionPolicy = iota
	// NodePolicyOtherSameModel moves to another node of the same model.
	NodePolicyOtherSameModel
	// NodePolicySameQualityModel switches to a same-quality-tier model.
	NodePolicySameQualityModel
)

func (p NodeSelectionPolicy) String() string {
	switch p {
	case NodePolicyOriginal:
		return "original_node"
	case NodePolicyOtherSameModel:
		return "other_node_same_model"
	case NodePolicySameQualityModel:
		return "same_quality_model"
	default:
		return "unknown"
	}
}

// RecoveryActionKind is the ladder decision.
type RecoveryActionKind uint8

const (
	// RecoveryActionRetrySameNode is L0.
	RecoveryActionRetrySameNode RecoveryActionKind = iota
	// RecoveryActionDiscardAndReplay is L1.
	RecoveryActionDiscardAndReplay
	// RecoveryActionAlignedContinuation is L2.
	RecoveryActionAlignedContinuation
	// RecoveryActionContinuationPrompt is L3.
	RecoveryActionContinuationPrompt
	// RecoveryActionVisibleRestart is L4 (restart/undo signal + full regen).
	RecoveryActionVisibleRestart
	// RecoveryActionErrorEnvelope is the L4 fallback (旧行为).
	RecoveryActionErrorEnvelope
)

func (k RecoveryActionKind) String() string {
	switch k {
	case RecoveryActionRetrySameNode:
		return "RetrySameNode"
	case RecoveryActionDiscardAndReplay:
		return "DiscardAndReplay"
	case RecoveryActionAlignedContinuation:
		return "AlignedContinuation"
	case RecoveryActionContinuationPrompt:
		return "ContinuationPrompt"
	case RecoveryActionVisibleRestart:
		return "VisibleRestart"
	case RecoveryActionErrorEnvelope:
		return "ErrorEnvelope"
	default:
		return "Unknown"
	}
}

var fullReplayLadder = []NodeSelectionPolicy{
	NodePolicyOriginal, NodePolicyOtherSameModel, NodePolicySameQualityModel,
}

// RecoveryAction is the policy verdict for one interruption.
type RecoveryAction struct {
	Kind           RecoveryActionKind
	Mode           RecoveryMode // stamp onto StreamRecoveryState.RecoveryMode
	NodeOrder      []NodeSelectionPolicy
	Reason         string
	Interrupt      StreamInterruptClass
	VisibleRestart bool // L4: marker frames will reach the client
}

// NextRecoveryAction drives the FR-12 ladder for one post-first-byte stream
// interruption (R12.2), bounded by the recovery budget (R12.7, UT-SR-09) and
// the ADR-Disp-007 cross-credential gate (R12.6, UT-SR-12).
//
// The caller owns the execution: feed observed state back (CommittedChunks,
// SameNodeRetries, AlignmentScoreBP, RecoveryMode, RecoveryNo) after every
// attempt and re-invoke on the next interruption.
//
// 2026-08-19 observability: this signature is preserved (called by
// stream_recovery_test.go across many test fixtures). The ctx-aware variant
// NextRecoveryActionCtx emits the structured slog alongside the same verdict
// so the L1 DiscardAndReplay / L4 fallback decisions are greppable by
// request_id in production logs.
func NextRecoveryAction(state *StreamRecoveryState, cfg StreamRecoveryConfig, err error) RecoveryAction {
	return NextRecoveryActionCtx(context.Background(), state, cfg, err)
}

// NextRecoveryActionCtx is the ctx-aware variant. Production callers pass
// the request context so the structured log line carries the
// request_id/parent_request_id/session_id/tenant_id correlation attrs
// already attached by handler.go / survival_wiring.go.
func NextRecoveryActionCtx(ctx context.Context, state *StreamRecoveryState, cfg StreamRecoveryConfig, err error) RecoveryAction {
	c := cfg.withDefaults()
	class, recoverable := ClassifyStreamError(err)
	if !recoverable {
		act := RecoveryAction{
			Kind:   RecoveryActionErrorEnvelope,
			Mode:   RecoveryModeError,
			Reason: "non_recoverable: " + errDesc(err),
		}
		logRecoveryAction(ctx, state, act, class)
		return act
	}
	if state == nil {
		act := RecoveryAction{Kind: RecoveryActionErrorEnvelope, Mode: RecoveryModeError,
			Reason: "missing recovery state", Interrupt: class}
		logRecoveryAction(ctx, state, act, class)
		return act
	}
	recordStreamInterrupt(class)

	// 恢复预算：stream_recovery_max_attempts 达限转 L4，不无限重生成 (UT-SR-09)。
	if int(state.RecoveryNo) >= c.MaxRecoveryAttempts {
		act := l4Decision(c, class, "recovery budget exhausted")
		logRecoveryAction(ctx, state, act, class)
		return act
	}

	// L0 同节点重发：SameNodeRetries < same_node_stream_retries (UT-SR-02)。
	if int(state.SameNodeRetries) < c.SameNodeStreamRetries {
		act := RecoveryAction{
			Kind:      RecoveryActionRetrySameNode,
			Mode:      RecoveryModeSameNode,
			NodeOrder: []NodeSelectionPolicy{NodePolicyOriginal},
			Reason:    "L0 same-node resend",
			Interrupt: class,
		}
		logRecoveryAction(ctx, state, act, class)
		return act
	}

	unlocked := CrossCredentialUnlockAllowed(state, c)
	// 已提交内容时的跨节点/模型重执行需要 ADR-Disp-007 解锁；未解锁回退
	// 旧行为（仅原节点/同凭据，或错误信封），不得静默拼接重复内容 (UT-SR-12)。
	ladder := fullReplayLadder
	if state.CommittedChunks > 0 && !unlocked {
		ladder = []NodeSelectionPolicy{NodePolicyOriginal}
	}

	// L1 可撤销窗口：语义内容尚未写客户端 → 丢弃缓冲重执行（客户端零感知）。
	if state.CommittedChunks == 0 {
		act := RecoveryAction{
			Kind:      RecoveryActionDiscardAndReplay,
			Mode:      RecoveryModeDiscardReplay,
			NodeOrder: ladder,
			Reason:    "L1 holdback window: discard buffered attempt and replay",
			Interrupt: class,
		}
		logRecoveryAction(ctx, state, act, class)
		return act
	}

	// L2: committed content. A previous aligned attempt that failed to align
	// (alignment miss) escalates to L3/L4.
	alignmentMiss := state.RecoveryMode == RecoveryModeAligned &&
		state.AlignmentScoreBP < c.AlignmentThresholdBP
	if alignmentMiss {
		recordAlignmentMiss()
		if c.ContinuationEnabled {
			// L3 白名单（任务类型/模型）由调用方经 ContinuationAllowed 复核。
			act := RecoveryAction{
				Kind:      RecoveryActionContinuationPrompt,
				Mode:      RecoveryModeContinuation,
				NodeOrder: ladder,
				Reason:    "L2 alignment miss: L3 continuation prompt",
				Interrupt: class,
			}
			logRecoveryAction(ctx, state, act, class)
			return act
		}
		act := l4Decision(c, class, "alignment miss and continuation disabled")
		logRecoveryAction(ctx, state, act, class)
		return act
	}

	act := RecoveryAction{
		Kind:      RecoveryActionAlignedContinuation,
		Mode:      RecoveryModeAligned,
		NodeOrder: ladder,
		Reason:    "L2 committed-prefix aligned continuation",
		Interrupt: class,
	}
	logRecoveryAction(ctx, state, act, class)
	return act
}

// logRecoveryAction emits one structured log line per recovery decision so
// the L1/L4 ladder is reconstructible from the application log without a
// Prometheus round-trip. Level: info for routine decisions, warn for L4
// (visible restart / error envelope) because the client sees an envelope.
func logRecoveryAction(ctx context.Context, state *StreamRecoveryState, act RecoveryAction, class StreamInterruptClass) {
	if state == nil {
		// Defensive: callers may invoke the ladder before wiring state; do
		// not panic the recovery path.
		state = &StreamRecoveryState{}
	}
	level := slog.LevelInfo
	if act.Kind == RecoveryActionErrorEnvelope || act.Kind == RecoveryActionVisibleRestart {
		level = slog.LevelWarn
	}
	slog.LogAttrs(ctx, level, "survival_recovery_action",
		slog.String("action", act.Kind.String()),
		slog.String("mode", act.Mode.String()),
		slog.String("reason", act.Reason),
		slog.String("interrupt_class", string(class)),
		slog.Bool("visible_restart", act.VisibleRestart),
		slog.Uint64("committed_chunks", uint64(state.CommittedChunks)),
		slog.Uint64("committed_bytes", uint64(state.CommittedBytes)),
		slog.Uint64("holdback_chunks", uint64(state.HoldbackChunks)),
		slog.Uint64("same_node_retries", uint64(state.SameNodeRetries)),
		slog.Uint64("recovery_no", uint64(state.RecoveryNo)),
		slog.Bool("visible_to_client", state.VisibleToClient),
	)
}

// l4Decision picks the L4 degradation form (R12.2 L4, R12.8): stream-undo
// capable clients get the undo control event; ops-allowed visible restart
// gets the generic restart marker + full regeneration; otherwise fall back
// to the protocol error envelope (旧行为).
func l4Decision(cfg StreamRecoveryConfig, class StreamInterruptClass, why string) RecoveryAction {
	if cfg.ClientStreamUndo || cfg.AllowVisibleRestart {
		return RecoveryAction{
			Kind:           RecoveryActionVisibleRestart,
			Mode:           RecoveryModeRestart,
			NodeOrder:      []NodeSelectionPolicy{NodePolicyOriginal, NodePolicyOtherSameModel, NodePolicySameQualityModel},
			Reason:         "L4 visible restart: " + why,
			Interrupt:      class,
			VisibleRestart: true,
		}
	}
	return RecoveryAction{
		Kind:      RecoveryActionErrorEnvelope,
		Mode:      RecoveryModeError,
		Reason:    "L4 fallback error envelope (policy forbids repeat): " + why,
		Interrupt: class,
	}
}

// CrossCredentialUnlockAllowed is the ADR-Disp-007 four-condition gate
// (R12.6). ANY of the following unlocks cross-node/cross-model re-execution
// after the first byte:
//
//	① 语义内容仍在 L1 可撤销窗口、尚未写客户端；
//	② L2 前缀对齐成功（分数 ≥ 阈值）；
//	③ 客户端声明 X-Gw-Capabilities: stream-undo；
//	④ 运营策略明确允许 L4 visible restart。
//
// All four failing → keep the ADR-Disp-003 legacy rule (same-credential
// resume or error envelope); never splice duplicated content silently
// (UT-SR-12).
func CrossCredentialUnlockAllowed(state *StreamRecoveryState, cfg StreamRecoveryConfig) bool {
	c := cfg.withDefaults()
	if state == nil {
		return false
	}
	// ① L1 uncommitted: holdback window still owns the semantic content.
	if state.CommittedChunks == 0 && (state.HoldbackChunks > 0 || state.HoldbackDeadlineMS > 0) {
		return true
	}
	// ② L2 aligned.
	if state.AlignmentScoreBP >= c.AlignmentThresholdBP {
		return true
	}
	// ③ client stream-undo capability declared.
	if cfg.ClientStreamUndo {
		return true
	}
	// ④ ops allow visible restart.
	if cfg.AllowVisibleRestart {
		return true
	}
	return false
}

func errDesc(err error) string {
	if err == nil {
		return "<nil>"
	}
	msg := err.Error()
	if len(msg) > 128 {
		msg = msg[:128]
	}
	return msg
}

// ── L1 holdback window ─────────────────────────────────────────────────────

// HoldbackTracker implements the L1 revocable window (R12.2 L1): the first
// semantic content OPENS the window; frames are held while
//
//	chunks held < HoldbackMaxChunks  AND  now - openAt < HoldbackWindow
//
// whichever limit hits first closes it (chunk #20 is still held, #21
// flushes; elapsed == window closes the window — deterministic boundaries,
// UT-SR-03/04). Purely time/chunk bookkeeping: the buffering itself is the
// AttemptCommitGate holdback mode (GateOptions.HoldbackWindow).
type HoldbackTracker struct {
	opened bool
	openAt time.Time
	chunks uint16
}

// Open marks the first semantic content. Idempotent.
func (t *HoldbackTracker) Open(now time.Time) {
	if t == nil || t.opened {
		return
	}
	t.opened = true
	t.openAt = now
}

// Opened reports whether the window has opened.
func (t *HoldbackTracker) Opened() bool { return t != nil && t.opened }

// Chunks reports held semantic chunks so far.
func (t *HoldbackTracker) Chunks() uint16 {
	if t == nil {
		return 0
	}
	return t.chunks
}

// ShouldHold reports whether the NEXT semantic chunk may stay buffered.
func (t *HoldbackTracker) ShouldHold(now time.Time, cfg StreamRecoveryConfig) bool {
	if t == nil || !t.opened {
		return false
	}
	c := cfg.withDefaults()
	if int(t.chunks) >= c.HoldbackMaxChunks {
		return false
	}
	return now.Sub(t.openAt) < time.Duration(c.HoldbackWindowMS)*time.Millisecond
}

// RecordChunk counts one held chunk.
func (t *HoldbackTracker) RecordChunk() {
	if t == nil {
		return
	}
	t.chunks++
}

// Deadline returns the absolute window close time (zero before Open).
func (t *HoldbackTracker) Deadline(cfg StreamRecoveryConfig) time.Time {
	if t == nil || !t.opened {
		return time.Time{}
	}
	return t.openAt.Add(time.Duration(cfg.withDefaults().HoldbackWindowMS) * time.Millisecond)
}

// Stamp mirrors the tracker onto the R12.3 state fields (HoldbackChunks /
// HoldbackDeadlineMS unix-milliseconds) for CrossCredentialUnlockAllowed ①.
func (t *HoldbackTracker) Stamp(state *StreamRecoveryState, cfg StreamRecoveryConfig) {
	if t == nil || state == nil {
		return
	}
	state.HoldbackChunks = t.chunks
	if d := t.Deadline(cfg); !d.IsZero() {
		state.HoldbackDeadlineMS = d.UnixMilli()
	}
}

// ── L2 committed-prefix cache + aligner ────────────────────────────────────

const (
	fnvOffset64 uint64 = 14695981039346656037
	fnvPrime64  uint64 = 1099511628211
)

func fnv64aUpdate(h uint64, data []byte) uint64 {
	for _, b := range data {
		h ^= uint64(b)
		h *= fnvPrime64
	}
	return h
}

// fnv64a computes the FNV-1a 64 hash of data.
func fnv64a(data []byte) uint64 { return fnv64aUpdate(fnvOffset64, data) }

// CommittedPrefix is the memory-bounded record of what the client already
// received on a stream (R12.2 L2): a hash over ALL committed bytes plus the
// FIRST window bytes (the fresh replay stream reproduces the head first, so
// the head window is what byte alignment needs). When TotalBytes exceeds
// the window the tail is hash-only (UT-SR-05).
type CommittedPrefix struct {
	Hash       uint64
	TotalBytes int
	Head       []byte
}

// Clone deep-copies (callers must not alias the cache internals).
func (p CommittedPrefix) Clone() CommittedPrefix {
	return CommittedPrefix{Hash: p.Hash, TotalBytes: p.TotalBytes, Head: append([]byte(nil), p.Head...)}
}

// CommittedPrefixCache stores one CommittedPrefix per request, capacity
// bounded; new entries past capacity are dropped (never block, never evict
// live tracking for memory pressure).
type CommittedPrefixCache struct {
	mu          sync.Mutex
	capacity    int
	windowBytes int
	entries     map[string]*CommittedPrefix
	dropped     uint64
}

// NewCommittedPrefixCache builds the cache (capacity/windowBytes <= 0 →
// defaults).
func NewCommittedPrefixCache(capacity, windowBytes int) *CommittedPrefixCache {
	c := DefaultStreamRecoveryConfig()
	if capacity <= 0 {
		capacity = c.CommittedPrefixCacheCapacity
	}
	if windowBytes <= 0 {
		windowBytes = c.CommittedPrefixWindowBytes
	}
	return &CommittedPrefixCache{
		capacity:    capacity,
		windowBytes: windowBytes,
		entries:     make(map[string]*CommittedPrefix),
	}
}

// Observe folds one committed chunk into the request's prefix record.
func (c *CommittedPrefixCache) Observe(requestID string, data []byte) {
	if requestID == "" || len(data) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.entries[requestID]
	if !ok {
		if len(c.entries) >= c.capacity {
			c.dropped++
			return // bounded: drop new tracking, never block
		}
		p = &CommittedPrefix{Hash: fnvOffset64}
		c.entries[requestID] = p
	}
	p.Hash = fnv64aUpdate(p.Hash, data)
	p.TotalBytes += len(data)
	if len(p.Head) < c.windowBytes {
		take := c.windowBytes - len(p.Head)
		if take > len(data) {
			take = len(data)
		}
		p.Head = append(p.Head, data[:take]...)
	}
}

// Snapshot returns a deep copy of the request's prefix record.
func (c *CommittedPrefixCache) Snapshot(requestID string) (CommittedPrefix, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.entries[requestID]
	if !ok {
		return CommittedPrefix{}, false
	}
	return p.Clone(), true
}

// Remove drops the request's record (terminal cleanup).
func (c *CommittedPrefixCache) Remove(requestID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, requestID)
}

// Len reports the number of tracked requests.
func (c *CommittedPrefixCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// DroppedTotal counts entries refused because the cache was full.
func (c *CommittedPrefixCache) DroppedTotal() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

// PrefixAlignment is the L2 verdict for one fresh replay stream.
type PrefixAlignment struct {
	// ScoreBP grades how much of the committed prefix the fresh stream
	// reproduced (0..10000 basis points).
	ScoreBP uint16
	// CommonBytes is the matched prefix length (head-window scope).
	CommonBytes int
	// SuffixOffset is where the non-duplicated suffix starts in the fresh
	// stream (== CommonBytes in window mode; == TotalBytes when the full
	// hash verified). Valid only when Aligned.
	SuffixOffset int
	// Suffix is fresh[SuffixOffset:] — the bytes to forward. Valid only
	// when Aligned.
	Suffix []byte
	// Aligned: score ≥ threshold AND (window covers everything OR the full
	// hash verified). Only then may the caller suppress the prefix.
	Aligned bool
	// HashOnly: committed bytes exceeded the window, so the tail could only
	// be hash-verified (all-or-nothing).
	HashOnly bool
}

// PrefixAligner grades one fresh replay stream against the committed prefix
// (R12.2 L2). Window mode compares the head byte-wise; once the committed
// bytes exceed the window the tail degrades to hash-only: full-hash match
// scores 10000bp, anything else is an alignment miss (conservative — never
// splice on partial evidence).
type PrefixAligner struct {
	thresholdBP uint16
}

// NewPrefixAligner builds the aligner; thresholdBP <= 0 → default 9000.
func NewPrefixAligner(thresholdBP uint16) *PrefixAligner {
	if thresholdBP == 0 {
		thresholdBP = DefaultAlignmentThresholdBP
	}
	return &PrefixAligner{thresholdBP: thresholdBP}
}

// ThresholdBP reports the configured suppression threshold.
func (a *PrefixAligner) ThresholdBP() uint16 {
	if a == nil {
		return DefaultAlignmentThresholdBP
	}
	return a.thresholdBP
}

func commonPrefixLen(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// Align compares the fresh replay stream against the committed prefix.
func (a *PrefixAligner) Align(committed CommittedPrefix, fresh []byte) PrefixAlignment {
	threshold := a.ThresholdBP()
	if committed.TotalBytes == 0 || len(committed.Head) == 0 || len(fresh) == 0 {
		return PrefixAlignment{}
	}
	common := commonPrefixLen(committed.Head, fresh)
	headScore := uint16(0)
	if common > 0 {
		headScore = uint16(common * 10000 / len(committed.Head))
	}
	windowCoversAll := committed.TotalBytes <= len(committed.Head)
	if windowCoversAll {
		aligned := headScore >= threshold
		res := PrefixAlignment{
			ScoreBP:      headScore,
			CommonBytes:  common,
			SuffixOffset: common,
			Aligned:      aligned,
		}
		if aligned {
			res.Suffix = append([]byte(nil), fresh[common:]...)
		}
		return res
	}
	// Tail beyond the window: hash-only degrade (UT-SR-05).
	if common == len(committed.Head) &&
		len(fresh) >= committed.TotalBytes &&
		fnv64a(fresh[:committed.TotalBytes]) == committed.Hash {
		return PrefixAlignment{
			ScoreBP:      10000,
			CommonBytes:  common,
			SuffixOffset: committed.TotalBytes,
			Suffix:       append([]byte(nil), fresh[committed.TotalBytes:]...),
			Aligned:      true,
			HashOnly:     true,
		}
	}
	// Partial evidence: report the head score but refuse suppression.
	return PrefixAlignment{ScoreBP: headScore, CommonBytes: common, HashOnly: true, Aligned: false}
}

// ── L3 continuation prompt (pure function, default off) ────────────────────

// ContinuationMessage is the minimal chat-shape used by the pure builder.
type ContinuationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// continuationInstruction is appended as the final user turn (R12.2 L3).
const continuationInstruction = "Your previous response was interrupted after the assistant text below. Continue exactly from the interruption point. Do not repeat, restate or re-output any content already sent; continue seamlessly as if nothing happened."

// BuildContinuationPrompt constructs the L3 continuation request from the
// most recent full upstream request plus the already-sent partial answer:
// original messages → assistant(sent partial) → user(continue instruction).
// Pure function; callers render it into the concrete upstream protocol.
func BuildContinuationPrompt(original []ContinuationMessage, sentContent string) ([]ContinuationMessage, error) {
	if len(original) == 0 {
		return nil, errors.New("continuation prompt requires the original upstream request")
	}
	out := make([]ContinuationMessage, 0, len(original)+2)
	out = append(out, original...)
	if strings.TrimSpace(sentContent) != "" {
		out = append(out, ContinuationMessage{Role: "assistant", Content: sentContent})
	}
	out = append(out, ContinuationMessage{Role: "user", Content: continuationInstruction})
	return out, nil
}

// ContinuationAllowed is the L3 gate: master toggle AND task-type/model
// whitelist. Empty whitelist = nobody qualifies (默认关, UT-SR-07).
func ContinuationAllowed(cfg StreamRecoveryConfig, taskType, model string) bool {
	if !cfg.ContinuationEnabled {
		return false
	}
	if taskType != "" {
		for _, t := range cfg.ContinuationTaskTypes {
			if t == taskType {
				return true
			}
		}
	}
	if model != "" {
		for _, m := range cfg.ContinuationModels {
			if m == model {
				return true
			}
		}
	}
	return false
}

// ── L4 restart rendering ───────────────────────────────────────────────────

// RestartRenderOptions parameterizes the L4 degradation frames.
type RestartRenderOptions struct {
	Protocol ClientProtocol
	// ClientStreamUndo: the client declared X-Gw-Capabilities: stream-undo
	// (R12.8) and receives the versioned undo control event instead of the
	// generic restart marker.
	ClientStreamUndo bool
	RequestID        string
	Reason           string
}

// RestartRenderResult is the rendered L4 signal.
type RestartRenderResult struct {
	// Frames to write through the serialized client writer (comment /
	// versioned control events only — never content deltas).
	Frames []string
	// VisibleRestart marks client-visible degradation (visible_restart
	// accounting, R12.9).
	VisibleRestart bool
	// UndoSignal marks the stream-undo control event form.
	UndoSignal bool
}

// RenderStreamRestartFrames renders the L4 degradation signal (UT-SR-08):
//
//   - stream-undo capable clients: `event: gw-stream-undo` with a versioned
//     JSON control payload (R12.8) — the client deletes the unfinished
//     assistant block before the full regeneration arrives;
//   - generic clients: SSE comment `: gw-stream-restarted` + a
//     `: thinking:` comment (Zod-safe: no data: lines, A17);
//   - policy-forbidden repeat is NOT rendered here — the caller keeps the
//     existing protocol error envelope (RecoveryActionErrorEnvelope).
func RenderStreamRestartFrames(opts RestartRenderOptions) RestartRenderResult {
	if opts.ClientStreamUndo {
		payload, err := json.Marshal(map[string]any{
			"type":       "gw_stream_undo",
			"version":    1,
			"request_id": opts.RequestID,
		})
		if err != nil {
			payload = []byte(`{"type":"gw_stream_undo","version":1}`)
		}
		return RestartRenderResult{
			Frames:     []string{"event: gw-stream-undo\ndata: " + string(payload) + "\n\n"},
			UndoSignal: true,
		}
	}
	// Generic clients: comment + thinking only — no data: frames, so strict
	// schema-validating clients (Zod) are never broken (A17/L4).
	thinking := "gateway stream restarted"
	if opts.Reason != "" {
		thinking += ": " + opts.Reason
	}
	escaped, err := json.Marshal(thinking)
	if err != nil {
		escaped = []byte(`"gateway stream restarted"`)
	}
	return RestartRenderResult{
		Frames: []string{
			": gw-stream-restarted\n\n",
			": thinking: " + string(escaped) + "\n\n",
		},
		VisibleRestart: true,
	}
}

// ── R12.9 metrics ──────────────────────────────────────────────────────────

type streamRecoveryMetricsStruct struct {
	interruptTotal         atomic.Uint64
	visibleInterruptTotal  atomic.Uint64
	alignmentMissTotal     atomic.Uint64
	costTokens             atomic.Uint64
	recoveryLatencySumNS   atomic.Int64
	recoveryLatencySamples atomic.Uint64

	mu               sync.Mutex
	successByMode    map[string]uint64
	interruptByClass map[string]uint64
}

var streamRecoveryMetrics = &streamRecoveryMetricsStruct{
	successByMode:    map[string]uint64{},
	interruptByClass: map[string]uint64{},
}

// StreamRecoveryMetricsSnapshot is the read model for admin/ops.
type StreamRecoveryMetricsSnapshot struct {
	InterruptTotal             uint64            `json:"stream_interrupt_total"`
	InterruptByClass           map[string]uint64 `json:"stream_interrupt_by_class,omitempty"`
	VisibleInterruptTotal      uint64            `json:"client_visible_stream_interrupt_total"`
	AlignmentMissTotal         uint64            `json:"stream_alignment_miss_total"`
	CostTokens                 uint64            `json:"stream_recovery_cost_tokens"`
	RecoverySuccessByMode      map[string]uint64 `json:"stream_recovery_success_total"`
	RecoveryLatencyP95ApproxMS float64           `json:"stream_recovery_latency_p95_approx_ms"`
}

func recordStreamInterrupt(class StreamInterruptClass) {
	streamRecoveryMetrics.interruptTotal.Add(1)
	streamRecoveryMetrics.mu.Lock()
	streamRecoveryMetrics.interruptByClass[string(class)]++
	streamRecoveryMetrics.mu.Unlock()
}

// RecordStreamInterruptClass counts one upstream interruption with its class
// (exported for the executor seam; the ladder calls it internally).
func RecordStreamInterruptClass(class StreamInterruptClass) {
	recordStreamInterrupt(class)
}

// RecordStreamRecoverySuccess counts one successful recovery by mode
// (same_node|discard_replay|aligned|continuation).
func RecordStreamRecoverySuccess(mode RecoveryMode) {
	streamRecoveryMetrics.mu.Lock()
	streamRecoveryMetrics.successByMode[mode.String()]++
	streamRecoveryMetrics.mu.Unlock()
}

// RecordClientVisibleInterrupt counts one client-visible interruption
// (reached L4). 主 SLO 指标 (R12.9).
func RecordClientVisibleInterrupt() {
	streamRecoveryMetrics.visibleInterruptTotal.Add(1)
}

// RecordStreamRecoveryLatency observes one recovery's wall time (P95 by
// running mean approximation until a histogram lands in metrics/).
func RecordStreamRecoveryLatency(d time.Duration) {
	streamRecoveryMetrics.recoveryLatencySumNS.Add(int64(d))
	streamRecoveryMetrics.recoveryLatencySamples.Add(1)
}

func recordAlignmentMiss() {
	streamRecoveryMetrics.alignmentMissTotal.Add(1)
}

// AddStreamRecoveryCostTokens adds the token cost of one re-execution
// attempt (每次重执行独立记录, R12.7).
func AddStreamRecoveryCostTokens(n int64) {
	if n <= 0 {
		return
	}
	streamRecoveryMetrics.costTokens.Add(uint64(n))
}

// SnapshotStreamRecoveryMetrics copies the current counters.
func SnapshotStreamRecoveryMetrics() StreamRecoveryMetricsSnapshot {
	m := streamRecoveryMetrics
	m.mu.Lock()
	success := make(map[string]uint64, len(m.successByMode))
	for k, v := range m.successByMode {
		success[k] = v
	}
	byClass := make(map[string]uint64, len(m.interruptByClass))
	for k, v := range m.interruptByClass {
		byClass[k] = v
	}
	m.mu.Unlock()
	var meanMS float64
	if s := m.recoveryLatencySamples.Load(); s > 0 {
		meanMS = float64(m.recoveryLatencySumNS.Load()) / float64(s) / float64(time.Millisecond)
	}
	return StreamRecoveryMetricsSnapshot{
		InterruptTotal:             m.interruptTotal.Load(),
		InterruptByClass:           byClass,
		VisibleInterruptTotal:      m.visibleInterruptTotal.Load(),
		AlignmentMissTotal:         m.alignmentMissTotal.Load(),
		CostTokens:                 m.costTokens.Load(),
		RecoverySuccessByMode:      success,
		RecoveryLatencyP95ApproxMS: meanMS,
	}
}

// resetStreamRecoveryMetricsForTest clears counters (tests only).
func resetStreamRecoveryMetricsForTest() {
	m := streamRecoveryMetrics
	m.interruptTotal.Store(0)
	m.visibleInterruptTotal.Store(0)
	m.alignmentMissTotal.Store(0)
	m.costTokens.Store(0)
	m.recoveryLatencySumNS.Store(0)
	m.recoveryLatencySamples.Store(0)
	m.mu.Lock()
	m.successByMode = map[string]uint64{}
	m.interruptByClass = map[string]uint64{}
	m.mu.Unlock()
}
