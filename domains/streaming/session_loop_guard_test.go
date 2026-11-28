package streaming

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// ── 判据语义（真库校准的形状，逐条钉住）────────────────────────────────
//
// 判据背景（211 生产取证，2026-11-28）：空转轮 ownOnly = (Δprompt −
// 上轮 completion) ∈ [-128, +512]，均值 +381；正常工作轮远超。
// trip=12 / 退避梯 30s 翻倍封顶 600s / 终态 120 轮 / 终态探针 1h /
// 入口进度阈值 2048（空转增量实测上界 ≈1100 的 2 倍）。
//
// 每条测试的 Observe 形状与真实请求流一致：一轮恰好一次 Observe
// （成功完成时），prompt 增量 = 上轮 completion + slop。

// loopGuardFlatTurn feeds ONE flat (no-progress) turn: prompt grows by
// completion+slop only. The reply is the canned waiting message (each turn
// the model "replies the same thing again"). Returns the new prompt size.
func loopGuardFlatTurn(g *SessionLoopGuard, sess string, prompt, completion int, ts time.Time) int {
	return loopGuardFlatTurnReply(g, sess, prompt, completion, "仍在等待你的裁决，无新情况，不会代为决定。", ts)
}

func loopGuardFlatTurnReply(g *SessionLoopGuard, sess string, prompt, completion int, reply string, ts time.Time) int {
	next := prompt + completion + 80 // 80 tokens of reminder slop ⇒ ownOnly=+80
	g.Observe(sess, next, completion, next, reply, ts)
	return next
}

func newTestLoopGuard() *SessionLoopGuard {
	return NewSessionLoopGuard()
}

func (g *SessionLoopGuard) at(t time.Time) { g.now = func() time.Time { return t } }

// withLoopGuardSettings pins settings.Global with loop_guard overrides
// (absent key = spec default). Restored via t.Cleanup. Same shape as
// withSurvivalDeadlineSettings (survival_deadline_p02_test.go).
func withLoopGuardSettings(t *testing.T, overrides map[string]int) {
	t.Helper()
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	store := map[string][]byte{}
	for k, v := range overrides {
		store[k] = []byte(jsonIntForLoopGuard(v))
	}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, fakeSettingsBackend{store: store})
	for _, spec := range settings.LoopGuardSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
}

func jsonIntForLoopGuard(v int) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestSessionLoopGuardLadder pins the exponential backoff ladder shape:
// 30s at trip, doubling per extra flat turn, capped at 600s. This is the
// 「自动回退」 half of the requirement.
func TestSessionLoopGuardLadder(t *testing.T) {
	base := 30 * time.Second
	capAt := 600 * time.Second
	want := map[int]time.Duration{
		11: base, // below trip: window value is just base
		12: 30 * time.Second,
		13: 60 * time.Second,
		14: 120 * time.Second,
		15: 240 * time.Second,
		16: 480 * time.Second,
		17: 600 * time.Second, // cap reached
		30: 600 * time.Second, // stays at cap
	}
	for flat, w := range want {
		if got := loopGuardLadder(flat, 12, base, capAt); got != w {
			t.Fatalf("ladder(flat=%d) = %v, want %v", flat, got, w)
		}
	}
}

// TestSessionLoopGuardTripsOnlyAfterConsecutiveFlatTurns pins: isolated
// small-delta turns (small tool results are normal work) never trip; the
// trip needs 12 CONSECUTIVE no-progress turns.
func TestSessionLoopGuardTripsOnlyAfterConsecutiveFlatTurns(t *testing.T) {
	g := newTestLoopGuard()
	withLoopGuardSettings(t, nil) // defaults: trip=12
	base := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)

	sess := "gw_loop_test"
	prompt := 400000
	// The first Observe only establishes the baseline (no previous turn to
	// compare) ⇒ flatRun = turns − 1. 12 turns keep flatRun at 11 < trip.
	for i := 1; i <= 12; i++ {
		prompt = loopGuardFlatTurn(g, sess, prompt, 300, base.Add(time.Duration(i)*10*time.Second))
		if lr := g.Check(sess, prompt); lr.Verdict != LoopAllow {
			t.Fatalf("turn %d: verdict=%v, want allow (trip needs 12 consecutive transitions)", i, lr.Verdict)
		}
	}
	// 13th turn completes the 12th consecutive flat transition ⇒ trips
	// (30s window armed from that turn's ts = base+130s).
	prompt = loopGuardFlatTurn(g, sess, prompt, 300, base.Add(130*time.Second))
	// Immediately after trip: inside the 30s window ⇒ reject.
	g.at(base.Add(131 * time.Second))
	lr := g.Check(sess, prompt)
	if lr.Verdict != LoopBackoff {
		t.Fatalf("after 12 flat turns: verdict=%v, want LoopBackoff", lr.Verdict)
	}
	if lr.RetryAfter <= 0 || lr.RetryAfter > 30*time.Second {
		t.Fatalf("RetryAfter=%v, want within the 30s first window", lr.RetryAfter)
	}
}

// TestSessionLoopGuardProgressResetsImmediately pins: a real tool result /
// user message (ownOnly beyond the判据) resets the streak and reopens the
// gate at once — a misfired trip costs at most one backoff window.
func TestSessionLoopGuardProgressResetsImmediately(t *testing.T) {
	g := newTestLoopGuard()
	withLoopGuardSettings(t, nil)
	base := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	g.at(base)

	sess := "gw_progress_test"
	prompt := 100000
	for i := 1; i <= 13; i++ { // trip it (13 turns ⇒ flatRun=12)
		prompt = loopGuardFlatTurn(g, sess, prompt, 300, base.Add(time.Duration(i)*10*time.Second))
	}
	g.at(base.Add(131 * time.Second))
	if lr := g.Check(sess, prompt); lr.Verdict != LoopBackoff {
		t.Fatalf("precondition: want tripped, got %v", lr.Verdict)
	}
	// Real progress arrives: an ~8KB tool result (≈2100 tokens) enters the
	// context next to the 300-token reply ⇒ ownOnly = 2100 > 512.
	prompt += 2100 + 300
	g.Observe(sess, prompt, 500, prompt, "tool result appended", base.Add(3*time.Minute))
	g.at(base.Add(3*time.Minute + 1*time.Second))
	if lr := g.Check(sess, prompt); lr.Verdict != LoopAllow || lr.ResetReason != "" {
		t.Fatalf("after progress: verdict=%v reset=%q, want clean allow", lr.Verdict, lr.ResetReason)
	}
}

// TestSessionLoopGuardProbeOnePerWindowAndExtension pins the hammer
// behavior: a client retrying every 6s gets rejected for the whole window,
// exactly ONE probe per window, and each flat probe extends the window
// along the ladder until the cap. This is what actually stops the token
// bleed on 211 (one probe per 10min instead of ~100 requests).
func TestSessionLoopGuardProbeOnePerWindowAndExtension(t *testing.T) {
	g := newTestLoopGuard()
	withLoopGuardSettings(t, nil)
	base := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)

	sess := "gw_hammer_test"
	prompt := 400000
	// 13 turns ⇒ flatRun=12 at ts=base+130s ⇒ 30s window until base+160s.
	for i := 1; i <= 13; i++ {
		prompt = loopGuardFlatTurn(g, sess, prompt, 300, base.Add(time.Duration(i)*10*time.Second))
	}

	// Hammer every 6s inside the first window (30s): all rejected.
	for i := 1; i <= 4; i++ {
		g.at(base.Add(132*time.Second + time.Duration(i)*6*time.Second))
		if lr := g.Check(sess, prompt); lr.Verdict != LoopBackoff {
			t.Fatalf("hammer req %d inside window: verdict=%v, want LoopBackoff", i, lr.Verdict)
		}
	}
	// Window elapsed (30s after trip): exactly one probe passes…
	g.at(base.Add(161 * time.Second))
	if lr := g.Check(sess, prompt); lr.Verdict != LoopAllow || !lr.Probe {
		t.Fatalf("window elapsed: verdict=%v probe=%v, want allow+probe", lr.Verdict, lr.Probe)
	}
	// …and the immediate next hammer is rejected again (window re-armed).
	if lr := g.Check(sess, prompt); lr.Verdict != LoopBackoff {
		t.Fatalf("second hammer right after probe: verdict=%v, want LoopBackoff", lr.Verdict)
	}
	// The probe turned out flat ⇒ ladder extends: flatRun=13 ⇒ 60s window
	// armed from the probe turn's ts (base+162s) ⇒ until base+222s.
	g.Observe(sess, prompt+380, 300, prompt+380, "still waiting", base.Add(162*time.Second))
	g.at(base.Add(170 * time.Second))
	if lr := g.Check(sess, prompt); lr.Verdict != LoopBackoff || lr.RetryAfter <= 30*time.Second || lr.RetryAfter > 60*time.Second {
		t.Fatalf("after flat probe: verdict=%v retry=%v, want backoff in (30s,60s]", lr.Verdict, lr.RetryAfter)
	}
}

// TestSessionLoopGuardTerminalAfterOverLimitTurns pins the「超次数关闭」
// half: once the flat run reaches terminal_turns the verdict upgrades to
// LoopTerminal and the probe window widens to the terminal interval.
func TestSessionLoopGuardTerminalAfterOverLimitTurns(t *testing.T) {
	g := newTestLoopGuard()
	withLoopGuardSettings(t, map[string]int{
		"loop_guard.terminal_turns":                  20,
		"loop_guard.terminal_probe_interval_seconds": 3600,
		"loop_guard.trip_after_turns":                3,
		"loop_guard.base_retry_after_seconds":        30,
		"loop_guard.max_retry_after_seconds":         600,
	})
	base := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)

	sess := "gw_terminal_test"
	prompt := 400000
	// 21 turns ⇒ flatRun=20 (first Observe is the baseline).
	for i := 1; i <= 21; i++ {
		prompt = loopGuardFlatTurn(g, sess, prompt, 300, base.Add(time.Duration(i)*10*time.Second))
	}
	g.at(base.Add(215 * time.Second))
	lr := g.Check(sess, prompt)
	if lr.Verdict != LoopTerminal {
		t.Fatalf("flat=20 ≥ terminal=20: verdict=%v, want LoopTerminal", lr.Verdict)
	}
	// Terminal probe interval (1h) replaces the ladder cap (10min).
	if lr.RetryAfter > 3600*time.Second || lr.RetryAfter < 3500*time.Second {
		t.Fatalf("terminal RetryAfter=%v, want ≈1h", lr.RetryAfter)
	}
}

// TestSessionLoopGuardEntryProgressReopens pins the human-return path: a
// tripped session whose next request carries substantially new content
// (≥ entry_progress_tokens) is reset and allowed IMMEDIATELY at entry —
// a returning user must never hit the dead door.
func TestSessionLoopGuardEntryProgressReopens(t *testing.T) {
	g := newTestLoopGuard()
	withLoopGuardSettings(t, map[string]int{
		"loop_guard.trip_after_turns": 3,
		"loop_guard.terminal_turns":   10,
	})
	base := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)

	sess := "gw_human_test"
	prompt := 400000
	// 11 turns ⇒ flatRun=10 = terminal_turns (trip=3 arms much earlier).
	for i := 1; i <= 11; i++ {
		prompt = loopGuardFlatTurn(g, sess, prompt, 300, base.Add(time.Duration(i)*10*time.Second))
	}
	g.at(base.Add(120 * time.Second))
	if lr := g.Check(sess, prompt); lr.Verdict != LoopTerminal {
		t.Fatalf("precondition: want terminal, got %v", lr.Verdict)
	}
	// The user typed a real message: entry estimate jumps by ~8k tokens
	// (well past the 2048 default; and past the ~1100 loop-turn upper bound,
	// which is exactly why the threshold must sit ABOVE the loop's slop).
	g.at(base.Add(115 * time.Second))
	if lr := g.Check(sess, prompt+8000); lr.Verdict != LoopAllow || lr.ResetReason != "entry_progress" {
		t.Fatalf("entry progress: verdict=%v reset=%q, want allow via entry_progress", lr.Verdict, lr.ResetReason)
	}
	// After the reset the accounting is clean: a slop-band request is
	// allowed too (verdict stays allow), but the streak must REBUILD from
	// zero — it takes another 3 flat turns before backoff arms again, not
	// an instant re-trip on the old state.
	prompt2 := prompt + 1100
	g.Observe(sess, prompt2, 300, prompt2, "resumed work", base.Add(125*time.Second))
	prompt2 = loopGuardFlatTurn(g, sess, prompt2, 300, base.Add(135*time.Second))
	if lr := g.Check(sess, prompt2); lr.Verdict != LoopAllow {
		t.Fatalf("2 flat turns after reset (trip=3): verdict=%v, want allow (streak rebuilding from zero)", lr.Verdict)
	}
	prompt2 = loopGuardFlatTurn(g, sess, prompt2, 300, base.Add(145*time.Second))
	prompt2 = loopGuardFlatTurn(g, sess, prompt2, 300, base.Add(155*time.Second))
	g.at(base.Add(156 * time.Second))
	if lr := g.Check(sess, prompt2); lr.Verdict != LoopBackoff {
		t.Fatalf("loop resumed: verdict=%v, want backoff after re-accumulating 3 flat turns", lr.Verdict)
	}
}

// TestSessionLoopGuardLongGapResetsStreak pins: silence longer than
// max_gap_seconds means the loop episode ended — the counter must not
// accumulate across days (a low-frequency session polling once every
// 30min must never trip).
func TestSessionLoopGuardLongGapResetsStreak(t *testing.T) {
	g := newTestLoopGuard()
	withLoopGuardSettings(t, map[string]int{"loop_guard.trip_after_turns": 3, "loop_guard.max_gap_seconds": 600})
	base := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)

	sess := "gw_sparse_test"
	prompt := 50000
	for i := 1; i <= 5; i++ { // short gaps: trips at the 3rd
		prompt = loopGuardFlatTurn(g, sess, prompt, 300, base.Add(time.Duration(i)*10*time.Second))
	}
	g.at(base.Add(50 * time.Second))
	if lr := g.Check(sess, prompt); lr.Verdict != LoopBackoff {
		t.Fatalf("precondition: want tripped after 5 short-gap flat turns, got %v", lr.Verdict)
	}
	// 12 minutes of silence, then one more flat turn: streak resets.
	prompt = loopGuardFlatTurn(g, sess, prompt, 300, base.Add(12*time.Minute))
	g.at(base.Add(12*time.Minute + 1*time.Second))
	if lr := g.Check(sess, prompt); lr.Verdict != LoopAllow {
		t.Fatalf("after long gap: verdict=%v, want allow (episode ended)", lr.Verdict)
	}
}

// TestSessionLoopGuardIgnoresTinyAndConcurrent pins two noise exclusions:
//   - prompt < min_prompt_tokens (internal probes, sys:anon traffic with 0
//     tokens) never enter the accounting;
//   - sub-500ms gaps (same-session concurrent bursts) don't count as flat
//     turns because "previous turn" is undefined there.
func TestSessionLoopGuardIgnoresTinyAndConcurrent(t *testing.T) {
	g := newTestLoopGuard()
	withLoopGuardSettings(t, map[string]int{"loop_guard.trip_after_turns": 3})
	base := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	g.at(base)

	// Tiny prompts are invisible to the guard: a static 100-token payload
	// re-sent forever (the sys:anon / health-probe shape) never enters the
	// accounting because every Observe is below min_prompt_tokens.
	for i := 1; i <= 10; i++ {
		g.Observe("gw_tiny", 100, 50, 100, "tiny", base.Add(time.Duration(i)*10*time.Second))
	}
	if lr := g.Check("gw_tiny", 100); lr.Verdict != LoopAllow {
		t.Fatalf("tiny prompts: verdict=%v, want allow", lr.Verdict)
	}

	// Concurrent burst: 20 successes 100ms apart count as ONE turn at most.
	for i := 1; i <= 20; i++ {
		g.Observe("gw_burst", 100000, 300, 100000, "burst reply", base.Add(time.Duration(i)*100*time.Millisecond))
	}
	if lr := g.Check("gw_burst", 100000); lr.Verdict != LoopAllow {
		t.Fatalf("concurrent burst: verdict=%v, want allow", lr.Verdict)
	}
}

// TestSessionLoopGuardDisabledBySettings pins the kill switch: the guard
// reads loop_guard.enabled on every call (HotReload), so flipping the
// setting stops both Check and Observe without a restart.
func TestSessionLoopGuardDisabledBySettings(t *testing.T) {
	base := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	sess := "gw_toggle"
	prompt := 400000

	g := newTestLoopGuard()
	withLoopGuardSettings(t, nil)
	g.at(base)
	for i := 1; i <= 13; i++ {
		prompt = loopGuardFlatTurn(g, sess, prompt, 300, base.Add(time.Duration(i)*10*time.Second))
	}
	g.at(base.Add(131 * time.Second))
	if lr := g.Check(sess, prompt); lr.Verdict != LoopBackoff {
		t.Fatalf("precondition: want tripped while enabled, got %v", lr.Verdict)
	}

	// Flip the kill switch: Check allows, Observe stops accounting.
	withLoopGuardSettings(t, map[string]int{"loop_guard.enabled": 0})
	store := map[string][]byte{"loop_guard.enabled": []byte("false")}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, fakeSettingsBackend{store: store})
	for _, spec := range settings.LoopGuardSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry

	if lr := g.Check(sess, prompt); lr.Verdict != LoopAllow {
		t.Fatalf("disabled: verdict=%v, want allow", lr.Verdict)
	}
	prompt = loopGuardFlatTurnReply(g, sess, prompt, 300, "disabled-period reply", base.Add(2*time.Minute))

	// Re-enable: the streak must NOT have grown while disabled (state was
	// frozen at flat=12, but the disabled-period turn never counted).
	registry = settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, fakeSettingsBackend{store: map[string][]byte{}})
	for _, spec := range settings.LoopGuardSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
	g.at(base.Add(3 * time.Minute))
	lr := g.Check(sess, prompt)
	if lr.Verdict != LoopAllow {
		t.Fatalf("re-enabled: verdict=%v, want allow (streak did not grow while disabled)", lr.Verdict)
	}
	if lr.FlatRun > 12 {
		t.Fatalf("flatRun=%d, want ≤12 (disabled-period turn must not count)", lr.FlatRun)
	}
}

// TestLoopGuardSpecsRegistered pins the settings surface: all 11 loop_guard
// keys are registered, platform-scoped, hot-reloadable, and the enabled
// flag defaults to true (protection on by default, per the 211 evidence).
func TestLoopGuardSpecsRegistered(t *testing.T) {
	specs := settings.PlatformSpecs()
	byKey := map[string]*settings.Spec{}
	for _, s := range specs {
		byKey[s.Key] = s
	}
	want := []string{
		"loop_guard.enabled",
		"loop_guard.flat_delta_tokens",
		"loop_guard.trip_after_turns",
		"loop_guard.base_retry_after_seconds",
		"loop_guard.max_retry_after_seconds",
		"loop_guard.terminal_turns",
		"loop_guard.terminal_probe_interval_seconds",
		"loop_guard.entry_progress_tokens",
		"loop_guard.repeat_identical_limit",
		"loop_guard.max_gap_seconds",
		"loop_guard.min_prompt_tokens",
	}
	for _, k := range want {
		s, ok := byKey[k]
		if !ok {
			t.Fatalf("spec %q not registered in PlatformSpecs()", k)
		}
		if s.Scope != settings.ScopePlatform {
			t.Fatalf("spec %q scope=%v, want platform", k, s.Scope)
		}
		if !s.HotReload {
			t.Fatalf("spec %q must be HotReload (guard reads it per-call)", k)
		}
	}
	if byKey["loop_guard.enabled"].Default != true {
		t.Fatalf("loop_guard.enabled default=%v, want true (protection on by default)", byKey["loop_guard.enabled"].Default)
	}
	if got := byKey["loop_guard.entry_progress_tokens"].Default; got != 2048 {
		t.Fatalf("loop_guard.entry_progress_tokens default=%v, want 2048 (2x the measured loop-turn upper bound ≈1100)", got)
	}
}

// ── 接线判据（源码锚点，防「写了判定体但没接线」）────────────────────────
//
// 防重蹈 team_budget_gate 教训：判定体在、调用点缺失 ⇒ 门静默失效。
// 三个协议面必须有入口 Check；emitTelemetry 必须有完成侧 Observe；
// ChatHandler 必须构造守卫。

func fileLineCount(t *testing.T, path, needle string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Count(string(b), needle)
}

func requireFileContains(t *testing.T, path string, needles ...string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	s := string(b)
	for _, n := range needles {
		if !strings.Contains(s, n) {
			t.Fatalf("%s missing required anchor %q (wiring regressed?)", path, n)
		}
	}
}

func TestSessionLoopGuardHasCallSites(t *testing.T) {
	// Entry Check on all three protocol faces (after session resolution).
	requireFileContains(t, "handler.go",
		"h.loopGuard.Check(sessionID, estimatePromptTokens(bodyBytes))",
	)
	requireFileContains(t, "messages.go",
		"h.chatHandler.loopGuard.Check(sessionID, estimatePromptTokens(bodyBytes))",
	)
	requireFileContains(t, "responses.go",
		"h.chatHandler.loopGuard.Check(sessionID, estimatePromptTokens(bodyBytes))",
	)
	// Completion-side Observe has exactly ONE call site (emitTelemetry —
	// the shared terminal for chat/messages/responses).
	if n := fileLineCount(t, "handler.go", "h.loopGuard.Observe("); n != 1 {
		t.Fatalf("Observe call sites = %d, want exactly 1 (emitTelemetry)", n)
	}
	// Guard is constructed in NewChatHandler (production wiring).
	requireFileContains(t, "handler.go",
		"loopGuard: NewSessionLoopGuard()",
	)
	// Rejection shapes are shared across the faces.
	requireFileContains(t, "session_loop_guard.go", "func loopGuardRejectionShape(")
	for _, f := range []string{"handler.go", "messages.go", "responses.go"} {
		if n := fileLineCount(t, f, "loopGuardRejectionShape(lr)"); n != 1 {
			t.Fatalf("%s loopGuardRejectionShape call sites = %d, want 1", f, n)
		}
	}
}

// TestSessionLoopGuardRejectionsUseRateLimitedPipeline pins: loop-guard
// rejections must go through the rate-limited accounting (EmitRateLimited
// / captureAndEmitRateLimited), NOT the failure pipeline — rejected loop
// requests must not pollute provider error counts, mirroring the RPM/TPM
// rejection convention.
func TestSessionLoopGuardRejectionsUseRateLimitedPipeline(t *testing.T) {
	requireFileContains(t, "handler.go",
		"captureAndEmitRateLimited(lgCode, lgMsg, nil, nil)",
	)
	requireFileContains(t, "messages.go",
		"logCtx.EmitRateLimited(lgCode, lgMsg, nil, nil)",
	)
	requireFileContains(t, "responses.go",
		"logCtx.EmitRateLimited(lgCode, lgMsg, nil, nil)",
	)
}

// TestSessionLoopGuardInternalKeyExempt pins: internal loopback keys are
// exempt on every face (probes/health checks legitimately resend fixed
// payloads at fixed cadence — they must never hit the wall).
func TestSessionLoopGuardInternalKeyExempt(t *testing.T) {
	for _, f := range []string{"handler.go", "messages.go", "responses.go"} {
		requireFileContains(t, f, "!keyInfo.IsInternal")
	}
}

// ── 重复回复快道（reply-fingerprint）────────────────────────────────────
//
// 211 取证（2026-11-28）：gw_872726fd 空转段 108 轮回复逐字节相同
// （md5 d9e2becb…，turn 426-544）；gw_b1be9881 同一请求体重复 344 次、
// 节拍 30.9s。这类会话的上下文增量可能被宿主 reminder 抬过 ownOnly
// 判据，但回复指纹纹丝不动 ⇒ 用归一化指纹计数早拦。

// TestSessionLoopGuardIdenticalRepliesTripFast pins: N consecutive
// byte-identical replies (default limit 5) trip the guard WITHOUT
// waiting for trip_after_turns=12 — the fast lane. The error code is
// loop_guard_repeat and reports the repeat count.
func TestSessionLoopGuardIdenticalRepliesTripFast(t *testing.T) {
	g := newTestLoopGuard()
	withLoopGuardSettings(t, map[string]int{"loop_guard.trip_after_turns": 12})
	base := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)

	sess := "gw_fastlane_test"
	prompt := 400000
	// 4 identical replies: below the limit of 5 — still allowed.
	for i := 1; i <= 4; i++ {
		prompt = loopGuardFlatTurnReply(g, sess, prompt, 300, "仍在等待你的裁决，无新情况，不会代为决定。", base.Add(time.Duration(i)*6*time.Second))
	}
	g.at(base.Add(30 * time.Second))
	if lr := g.Check(sess, prompt); lr.Verdict != LoopAllow {
		t.Fatalf("4 identical replies < limit 5: verdict=%v, want allow", lr.Verdict)
	}
	// 5th identical reply trips immediately.
	prompt = loopGuardFlatTurnReply(g, sess, prompt, 300, "仍在等待你的裁决，无新情况，不会代为决定。", base.Add(36*time.Second))
	g.at(base.Add(37 * time.Second))
	lr := g.Check(sess, prompt)
	if lr.Verdict != LoopBackoff {
		t.Fatalf("5 identical replies: verdict=%v, want LoopBackoff via fast lane", lr.Verdict)
	}
	if lr.RepeatStreak != 5 {
		t.Fatalf("RepeatStreak=%d, want 5", lr.RepeatStreak)
	}
	// Rejection shape names the repeat lane.
	code, msg := loopGuardRejectionShape(lr)
	if code != "loop_guard_repeat" || !strings.Contains(msg, "5 times") {
		t.Fatalf("shape code=%q msg=%q, want loop_guard_repeat mentioning 5", code, msg)
	}
}

// TestSessionLoopGuardReplyVariationResetsStreak pins: a changed reply
// (even slightly different wording, or extra trailing punctuation /
// whitespace) resets the fingerprint streak — paraphrasing models and
// punctuation drift must not accumulate.
func TestSessionLoopGuardReplyVariationResetsStreak(t *testing.T) {
	g := newTestLoopGuard()
	withLoopGuardSettings(t, nil)
	base := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)

	sess := "gw_paraphrase_test"
	prompt := 400000
	// Whitespace + trailing punctuation differences normalize to ONE fp:
	// 4 normalized-equal replies stay under the limit of 5.
	for i := 1; i <= 4; i++ {
		r := []string{"仍在等待你的裁决。", "仍在等待你的裁决   ", "仍在等待你的裁决!", "仍在等待你的裁决……"}[i%4]
		prompt = loopGuardFlatTurnReply(g, sess, prompt, 300, r, base.Add(time.Duration(i)*10*time.Second))
	}
	g.at(base.Add(50 * time.Second))
	if lr := g.Check(sess, prompt); lr.Verdict != LoopAllow {
		t.Fatalf("normalized-equal replies must not trip: verdict=%v", lr.Verdict)
	}
	// A genuinely different reply resets the streak: even 4 more identical
	// turns afterward (streak restarts at 1..4) stay allowed.
	prompt = loopGuardFlatTurnReply(g, sess, prompt, 300, "现在换了一个说法，情况有变化。", base.Add(60*time.Second))
	if loopGuardReplyFingerprint("现在换了一个说法，情况有变化。") == loopGuardReplyFingerprint("仍在等待你的裁决。") {
		t.Fatalf("distinct texts must hash distinct fps")
	}
	for i := 1; i <= 4; i++ {
		prompt = loopGuardFlatTurnReply(g, sess, prompt, 300, "仍在等待你的裁决。", base.Add(time.Duration(60+i*10)*time.Second))
	}
	g.at(base.Add(110 * time.Second))
	if lr := g.Check(sess, prompt); lr.Verdict != LoopAllow {
		t.Fatalf("streak restarted after variation: verdict=%v, want allow", lr.Verdict)
	}
}

// TestSessionLoopGuardRepeatLimitZeroDisablesFastLane pins: setting the
// limit to 0 disables the fingerprint lane entirely (operator escape
// hatch), leaving only the context-delta lane.
func TestSessionLoopGuardRepeatLimitZeroDisablesFastLane(t *testing.T) {
	g := newTestLoopGuard()
	withLoopGuardSettings(t, map[string]int{"loop_guard.repeat_identical_limit": 0, "loop_guard.trip_after_turns": 12})
	base := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)

	sess := "gw_nolane_test"
	prompt := 400000
	for i := 1; i <= 20; i++ {
		prompt = loopGuardFlatTurnReply(g, sess, prompt, 300, "identical", base.Add(time.Duration(i)*10*time.Second))
	}
	g.at(base.Add(210 * time.Second))
	lr := g.Check(sess, prompt)
	// flatRun = 19 ≥ trip 12 ⇒ the SLOW lane trips; the fast lane must not
	// have fired early (i.e. trip happened via flat counter semantics, and
	// shape reports backoff by flat-run, not repeat).
	if lr.Verdict == LoopAllow {
		t.Fatalf("slow lane should still trip at flatRun=19")
	}
	// With the lane disabled the verdict must be the SLOW lane's shape
	// (flat-run backoff), not the repeat lane's — even though streaks
	// still accumulate internally, they must never produce an earlier
	// trip or a repeat-branded rejection.
	code, _ := loopGuardRejectionShape(lr)
	if code == "loop_guard_repeat" {
		t.Fatalf("disabled fast lane must not produce loop_guard_repeat, got %q", code)
	}
	if lr.Verdict != LoopBackoff {
		t.Fatalf("verdict=%v, want LoopBackoff from the slow lane", lr.Verdict)
	}
}

// TestSessionLoopGuardFingerprintNormalization pins the normalizer edges:
// empty replies carry no fingerprint (ownOnly lane owns them), long
// replies fingerprint on their 2KB tail, and the hash is stable.
func TestSessionLoopGuardFingerprintNormalization(t *testing.T) {
	if fp := loopGuardReplyFingerprint("   \n\t "); fp != "" {
		t.Fatalf("whitespace-only reply must produce empty fp, got %q", fp)
	}
	a := loopGuardReplyFingerprint("同一句话，还在等待。")
	b := loopGuardReplyFingerprint("同一句话，还在等待!")
	c := loopGuardReplyFingerprint("同一句话，还在等待。  \n\t ")
	d := loopGuardReplyFingerprint("同一句话，还在等待……")
	if a != b || a != c || a != d {
		t.Fatalf("normalization must fold trailing punctuation/whitespace: %q %q %q %q", a, b, c, d)
	}
	e := loopGuardReplyFingerprint("同一句话，已经完成。")
	if a == e {
		t.Fatalf("genuinely different texts must hash differently")
	}
	long := strings.Repeat("前缀内容保持不变。", 400) + "结尾标记X"
	long2 := strings.Repeat("前缀内容保持不变。", 400) + "结尾标记X"
	if loopGuardReplyFingerprint(long) != loopGuardReplyFingerprint(long2) {
		t.Fatalf("identical long replies must hash equal")
	}
	diffTail := strings.Repeat("前缀内容保持不变。", 400) + "结尾标记Y"
	if loopGuardReplyFingerprint(long) == loopGuardReplyFingerprint(diffTail) {
		t.Fatalf("different tails must hash differently")
	}
}
