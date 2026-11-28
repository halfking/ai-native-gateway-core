// 会话无进展循环守卫（session loop guard）。
// 背景（2026-11-28，211 生产取证）：agent 宿主在等待用户裁决/长工具期间，
// 会以固定节奏（6~30 秒）把整段上下文重发上游，模型每次只回一条
// 「仍在等待用户输入」的短消息；下一轮把这条回复原样并入历史继续重发。
// 单会话实测 550 轮烧掉 1.14 亿 prompt tokens（gw_872726fd，2026-11-05），
// 另一会话 93% 的轮次为空转、累计 6.0 亿（gw_264193cb，2026-11-27/28）。
// 每一轮除了模型自己的回复（约 200~500 tokens）外没有任何新信息进入
// 上下文——这正是网关侧可判的信号。
//
// 判据（真库校准）：
//
//	ownOnly = (本轮 prompt_tokens − 上轮 prompt_tokens) − 上轮 completion_tokens
//	空转轮: ownOnly ∈ [-128, +512]   （平均 +381；负值容忍 usage 统计噪声）
//	进度轮: 工具结果/用户新输入使 ownOnly 远超该值
//
// 单独一轮 ownOnly 偏小不代表空转（小工具结果常见），所以必须
// 「连续 N 轮」才 trip；而真库对照显示正常高频会话的最长连续段
// 也只有 10~86 轮且伴随进度轮穿插——trip 后误伤的自愈成本只有
// 一个回退窗口，宁可保守也不漏。
//
// 处置（按「自动回退 + 超次数关闭」原则）：
//   - 连续 flat ≥ trip_after_turns ⇒ 回退态：429 + Retry-After 指数退避
//     （30s 起翻倍，封顶 max_retry_after_seconds），不再转发上游——
//     重试不再烧上游 token；每个回退窗口最多放行一个探针轮；
//   - 连续 flat ≥ terminal_turns ⇒ 终态：错误码升级 loop_guard_terminated，
//     探针窗口放宽到 terminal_probe_interval_seconds（超次数关闭）；
//   - 重开路径（保证误判永远可自愈）：
//     ① 入口侧请求体 token 估计较上次成功轮显著增长（≥
//     entry_progress_tokens，默认 2048 ≈ 空转增量上界的 2 倍）
//     ⇒ 视为真实新输入，直接重开放行（人类带着新输入回来不会撞死门）；
//     ② 探针轮若出现真进度（ownOnly 超判据）⇒ 整体重置；
//     ③ 相邻成功轮间隔 > max_gap_seconds ⇒ 连续计数清零（循环自然中断）。
//
// 边界（诚实登记）：本守卫只针对「上下文零增长」型循环。如果宿主每轮
// 注入大块 reminder（增量超过判据），守卫有意不拦——那是计费/限额问题，
// 不是空转问题。网关不伪造模型输出，只拒绝转发。
//
// 状态为进程内存（蓝绿换槽/重启后从零开始重新累计，最多损失 trip 前
// 的 trip_after_turns 轮空转窗口，可接受）；会话态上限受
// loopGuardMaxStates 保护，超限时先逐出陈旧条目、仍超限则整体清空。
package streaming

import (
	"hash/fnv"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// settings keys (defined in settings/spec_loop_guard.go).
const (
	loopGuardEnabledKey         = "loop_guard.enabled"
	loopGuardFlatDeltaKey       = "loop_guard.flat_delta_tokens"
	loopGuardTripTurnsKey       = "loop_guard.trip_after_turns"
	loopGuardBaseRetryKey       = "loop_guard.base_retry_after_seconds"
	loopGuardMaxRetryKey        = "loop_guard.max_retry_after_seconds"
	loopGuardTerminalTurnsKey   = "loop_guard.terminal_turns"
	loopGuardTerminalProbeKey   = "loop_guard.terminal_probe_interval_seconds"
	loopGuardMaxGapKey          = "loop_guard.max_gap_seconds"
	loopGuardMinPromptTokensKey = "loop_guard.min_prompt_tokens"
	loopGuardEntryProgressKey   = "loop_guard.entry_progress_tokens"
	loopGuardRepeatLimitKey     = "loop_guard.repeat_identical_limit"
)

// loopGuardConfig is a snapshot of the HotReload-able settings.
type loopGuardConfig struct {
	enabled            bool
	flatDeltaTokens    int
	tripAfterTurns     int
	baseRetryAfter     time.Duration
	maxRetryAfter      time.Duration
	terminalTurns      int
	terminalProbeEvery time.Duration
	maxGap             time.Duration
	minPromptTokens    int
	entryProgress      int
	repeatIdentical    int
}

func loadLoopGuardConfig() loopGuardConfig {
	return loopGuardConfig{
		enabled:            settings.GetPlatformBool(loopGuardEnabledKey, true),
		flatDeltaTokens:    settings.GetPlatformInt(loopGuardFlatDeltaKey, 512),
		tripAfterTurns:     settings.GetPlatformInt(loopGuardTripTurnsKey, 12),
		baseRetryAfter:     time.Duration(settings.GetPlatformInt(loopGuardBaseRetryKey, 30)) * time.Second,
		maxRetryAfter:      time.Duration(settings.GetPlatformInt(loopGuardMaxRetryKey, 600)) * time.Second,
		terminalTurns:      settings.GetPlatformInt(loopGuardTerminalTurnsKey, 120),
		terminalProbeEvery: time.Duration(settings.GetPlatformInt(loopGuardTerminalProbeKey, 3600)) * time.Second,
		maxGap:             time.Duration(settings.GetPlatformInt(loopGuardMaxGapKey, 600)) * time.Second,
		minPromptTokens:    settings.GetPlatformInt(loopGuardMinPromptTokensKey, 256),
		entryProgress:      settings.GetPlatformInt(loopGuardEntryProgressKey, 2048),
		repeatIdentical:    settings.GetPlatformInt(loopGuardRepeatLimitKey, 5),
	}
}

// loopGuardOwnOnlyLower: usage 统计噪声容忍（estimated usage 与 actual 之间
// 的少量偏差不应把空转轮判成进度轮）。
const loopGuardOwnOnlyLower = -128

// 入口侧进度判据：请求体 token 估计相对上次成功轮的增长 ≥
// entry_progress_tokens（默认 2048）才视为真实新输入。
// 上界的取值依据（真库校准）：空转轮的 prompt 增量 = 上轮 completion
// （216~516）＋ 少量 slop，实测最大 ≈1100 tokens ⇒ 2048 留 2 倍余量，
// 保证空转轮永不触发入口重置（否则守卫会被循环自身一拍一拍打穿）。
// 返程用户的真实新输入（新消息/新任务）通常 ≥ 该值；短于该值的输入
// 走探针路径重开（最多等一个回退窗口）。
const loopGuardEntryProgressDefault = 2048

// 并发突发下限：相邻成功轮间隔小于该值不参与连续计数（同一会话的
// 并行请求无法定义「上一轮」）。
const loopGuardMinGap = 500 * time.Millisecond

// loopGuardMaxStates: 状态表条目上限（防失控会话把内存吃穿）。
const loopGuardMaxStates = 20000

// LoopVerdict distinguishes the guard phases for callers that want to
// differentiate the client-facing error code.
type LoopVerdict int

const (
	// LoopAllow: not tripped (or reset by entry-side progress) — forward.
	LoopAllow LoopVerdict = iota
	// LoopBackoff: tripped, inside the current backoff window — reject with
	// 429 + Retry-After, error code loop_guard_backoff.
	LoopBackoff
	// LoopTerminal: flat run exceeded terminal turns — reject with 429 +
	// Retry-After (terminal probe interval), error code loop_guard_terminated.
	LoopTerminal
)

// LoopCheckResult is the entry-side decision.
type LoopCheckResult struct {
	Verdict      LoopVerdict
	RetryAfter   time.Duration // for rejections: suggested wait (≤ current window)
	FlatRun      int
	RepeatStreak int // consecutive identical-reply successes (fingerprint lane)
	// Probe marks a request that passed because the backoff window had
	// elapsed (one probe per window; Observe re-arms or resets afterwards).
	Probe bool
	// ResetReason is set (non-empty) when the guard cleared a previously
	// tripped state at entry time because the incoming request shows real
	// new input. Observability only.
	ResetReason string
}

// loopState is the per-session accounting record.
type loopState struct {
	promptTokens     int       // last success: actual prompt tokens reported by the LLM
	completionTokens int       // last success: completion tokens (ownOnly = delta − this)
	estPrompt        int       // last success: gateway-side token estimate of the request body
	flatRun          int       // consecutive no-progress successes
	lastSuccess      time.Time // last success ts
	dueAt            time.Time // when the next probe may pass (zero while not tripped)
	lastSeen         time.Time // last request seen at Check (allowed or blocked)
	// reply-fingerprint fast lane（字节级重复回复，见 loopGuardNormalizeReply）：
	// 同一归一化指纹连续出现 repeat_identical_limit 次 ⇒ 直接进入回退态，
	// 不等 trip_after_turns 的上下文零增长计数——批量重试型宿主每轮可能
	// 注入 reminder 使上下文增量超判据，但回复是逐字节相同的。
	replyFP     string // normalized fingerprint of the last success reply
	replyStreak int    // consecutive successes sharing the same fingerprint
}

// SessionLoopGuard tracks per-session no-progress loops. All methods are
// safe for concurrent use. The guard never talks to the database; state is
// in-memory and intentionally cheap.
type SessionLoopGuard struct {
	mu    sync.Mutex
	state map[string]*loopState
	now   func() time.Time // test seam
}

// NewSessionLoopGuard builds an enabled-by-default guard (the enabled flag
// is re-read from settings on every call, so HotReload works without a
// restart).
func NewSessionLoopGuard() *SessionLoopGuard {
	return &SessionLoopGuard{
		state: make(map[string]*loopState),
		now:   time.Now,
	}
}

// windowFor returns the probe window for a session whose flat run is n:
// the exponential ladder (base doubling per extra flat turn, capped) while
// below terminal turns, and the (much longer) terminal probe interval once
// the run reaches terminal turns.
func loopGuardWindowFor(flatRun int, cfg loopGuardConfig) time.Duration {
	if flatRun >= cfg.terminalTurns {
		return cfg.terminalProbeEvery
	}
	return loopGuardLadder(flatRun, cfg.tripAfterTurns, cfg.baseRetryAfter, cfg.maxRetryAfter)
}

// Check is called at request entry (after session resolution, before any
// upstream work). estPromptTokens must be estimatePromptTokens(bodyBytes)
// computed at the same compression stage as the Observe-side estimate.
func (g *SessionLoopGuard) Check(sessionID string, estPromptTokens int) LoopCheckResult {
	cfg := loadLoopGuardConfig()
	if !cfg.enabled || sessionID == "" {
		return LoopCheckResult{Verdict: LoopAllow}
	}
	now := g.now()

	g.mu.Lock()
	defer g.mu.Unlock()

	s, ok := g.state[sessionID]
	if !ok || s == nil {
		// First sight of this session: record the entry estimate so the
		// entry-side progress compare has an anchor, and allow.
		g.state[sessionID] = &loopState{estPrompt: estPromptTokens, lastSeen: now}
		return LoopCheckResult{Verdict: LoopAllow}
	}
	s.lastSeen = now

	// Entry-side progress: the incoming request carries significantly more
	// (or structurally different) content than the last success — treat as
	// real new input, reset everything, allow. This is the path a returning
	// human takes; it must never hit the backoff wall.
	if s.estPrompt > 0 && estPromptTokens > 0 {
		threshold := cfg.entryProgress
		if threshold < 1 {
			threshold = loopGuardEntryProgressDefault
		}
		delta := estPromptTokens - s.estPrompt
		if delta >= threshold || -delta >= threshold {
			wasTripped := !s.dueAt.IsZero()
			s.flatRun = 0
			s.dueAt = time.Time{}
			s.promptTokens = 0
			s.completionTokens = 0
			s.estPrompt = estPromptTokens
			if wasTripped {
				return LoopCheckResult{Verdict: LoopAllow, ResetReason: "entry_progress"}
			}
			return LoopCheckResult{Verdict: LoopAllow}
		}
	}

	if s.dueAt.IsZero() {
		return LoopCheckResult{Verdict: LoopAllow, FlatRun: s.flatRun}
	}

	// Tripped. Silence ≥ current window ⇒ exactly one probe passes; the
	// window re-arms immediately so a hammering client gets at most one
	// request per window. Observe will extend the window along the ladder
	// if the probe turns out flat, or reset everything if it shows progress.
	if !now.Before(s.dueAt) {
		s.dueAt = now.Add(loopGuardWindowFor(s.flatRun, cfg))
		return LoopCheckResult{Verdict: LoopAllow, FlatRun: s.flatRun, Probe: true}
	}

	verdict := LoopBackoff
	if s.flatRun >= cfg.terminalTurns {
		verdict = LoopTerminal
	}
	// repeatIdentical=0 disables the fingerprint lane entirely: don't
	// surface the streak (or the repeat-branded rejection shape).
	streak := 0
	if cfg.repeatIdentical > 0 {
		streak = s.replyStreak
	}
	return LoopCheckResult{
		Verdict:      verdict,
		RetryAfter:   s.dueAt.Sub(now),
		FlatRun:      s.flatRun,
		RepeatStreak: streak,
	}
}

// Observe is called at request completion for SUCCESSFUL turns only (the
// failure path has its own pipeline and client retries after failures are
// legitimate). promptTokens/completionTokens come from the finalized
// request log; estPromptTokens is the gateway-side estimate of the same
// request body.
func (g *SessionLoopGuard) Observe(sessionID string, promptTokens, completionTokens, estPromptTokens int, replyText string, ts time.Time) {
	cfg := loadLoopGuardConfig()
	if !cfg.enabled || sessionID == "" || promptTokens < cfg.minPromptTokens {
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	s, ok := g.state[sessionID]
	if !ok || s == nil {
		s = &loopState{}
		g.state[sessionID] = s
	}
	g.evictLocked()

	// Tiny gap = concurrent burst from the same session: the notion of
	// "previous turn" is undefined; skip flat accounting entirely.
	if !s.lastSuccess.IsZero() {
		gap := ts.Sub(s.lastSuccess)
		if gap < loopGuardMinGap {
			return
		}
		// Long gap = the loop (or the session) went quiet: a new episode.
		if gap > cfg.maxGap {
			s.flatRun = 0
			s.dueAt = time.Time{}
			s.replyStreak = 0
		}
	}

	if !s.lastSuccess.IsZero() && s.promptTokens > 0 {
		ownOnly := (promptTokens - s.promptTokens) - s.completionTokens
		if ownOnly >= loopGuardOwnOnlyLower && ownOnly <= cfg.flatDeltaTokens {
			s.flatRun++
		} else {
			// Real progress: tool results / user input entered the context.
			s.flatRun = 0
			s.dueAt = time.Time{}
		}
	}

	// Reply-fingerprint fast lane: identical normalized replies stack a
	// streak independent of the context-delta signal. progress resets it.
	fp := loopGuardReplyFingerprint(replyText)
	if fp != "" && fp == s.replyFP {
		s.replyStreak++
	} else {
		s.replyFP = fp
		s.replyStreak = 1
	}

	s.promptTokens = promptTokens
	s.completionTokens = completionTokens
	s.estPrompt = estPromptTokens
	s.lastSuccess = ts

	if s.flatRun >= cfg.tripAfterTurns || (cfg.repeatIdentical > 0 && s.replyStreak >= cfg.repeatIdentical) {
		// (Re)arm the backoff window deterministically from the ladder.
		// The fingerprint lane uses the trip-tier window (it proves the
		// loop with far fewer turns; escalating per extra repeat still
		// happens naturally on the next flat probes via flatRun).
		s.dueAt = ts.Add(loopGuardWindowFor(s.flatRun, cfg))
	}
}

// loopGuardReplyFingerprint normalizes a completion's text and hashes it:
// whitespace collapse + trailing-punctuation strip (models end waiting
// messages with varying punctuation), truncated to the last 2KB so
// verbose-but-identical tails still match. Empty/whitespace replies
// return "" (no fingerprint — the ownOnly lane already covers them).
// 非密码学用途，fnv 足够且无分配负担。
func loopGuardReplyFingerprint(replyText string) string {
	fields := strings.Fields(replyText)
	if len(fields) == 0 {
		return ""
	}
	joined := strings.Join(fields, " ")
	if len(joined) > 2048 {
		joined = joined[len(joined)-2048:]
	}
	joined = strings.TrimRight(joined, "。.!?！？…")
	h := fnv.New64a()
	h.Write([]byte(joined))
	return strconv.FormatUint(h.Sum64(), 16)
}

// evictLocked keeps the state table bounded. Cheap path: drop entries idle
// for over 10 minutes. If the table is still over budget (an extreme burst
// of active sessions), reset it entirely — accounting restarts from zero,
// which costs at most one trip window of unguarded looping per session.
func (g *SessionLoopGuard) evictLocked() {
	if len(g.state) <= loopGuardMaxStates {
		return
	}
	staleBefore := g.now().Add(-10 * time.Minute)
	for id, s := range g.state {
		if s.lastSeen.Before(staleBefore) {
			delete(g.state, id)
		}
	}
	if len(g.state) > loopGuardMaxStates {
		g.state = make(map[string]*loopState)
	}
}

// loopGuardRejectionShape renders the client-facing error code + message
// for a loop-guard rejection. Shared verbatim by the three protocol faces
// (chat / messages / responses) so a client sees one consistent contract.
func loopGuardRejectionShape(lr LoopCheckResult) (code, msg string) {
	if lr.Verdict == LoopTerminal {
		return "loop_guard_terminated",
			"Session closed by loop guard after " + strconv.Itoa(lr.FlatRun) +
				" consecutive no-progress turns (over-limit shutdown). Genuinely new input " +
				"is required to resume; periodic retries will keep being rejected."
	}
	if lr.RepeatStreak >= 3 && lr.FlatRun+1 <= lr.RepeatStreak {
		// Fingerprint-lane trip: the context-delta counter has not accumulated
		// yet, but the replies are byte-identical — report the repeat count.
		return "loop_guard_repeat",
			"Session paused by loop guard: the model returned the identical reply " +
				strconv.Itoa(lr.RepeatStreak) + " times in a row (no new information is being produced). " +
				"Upstream calls are suspended to avoid wasting tokens. Send real new input " +
				"(user message or task progress) to resume, or stop retrying and wait."
	}
	return "loop_guard_backoff",
		"Session paused by loop guard: " + strconv.Itoa(lr.FlatRun) +
			" consecutive turns added no new context (only the model's own reply was re-fed). " +
			"Upstream calls are suspended to avoid wasting tokens. Send real new input " +
			"(user message or task progress) to resume, or stop retrying and wait."
}

// loopGuardLadder is a pure helper (exposed for tests): the Retry-After
// window for a given flat run — base doubling per extra flat turn beyond
// tripTurns, capped at cap.
func loopGuardLadder(flatRun, tripTurns int, base, cap time.Duration) time.Duration {
	if flatRun <= tripTurns {
		return base
	}
	w := base
	for i := tripTurns; i < flatRun; i++ {
		w *= 2
		if w >= cap {
			return cap
		}
	}
	return w
}
