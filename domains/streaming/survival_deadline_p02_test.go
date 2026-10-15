package streaming

// survival_deadline_p02_test.go — P0-2（2026-10-14，
// docs/research/2026-10-14-magpie对比与优化方案.md §P0-2）：
// 交互等待预算分级下调（5h 挂起消除）的三层契约。
//
//  1. 零值回落：SurvivalOptions{}.Deadline 零值 → 240s（不是 5h）；
//  2. settings_kv 热加载键 survival.interactive_deadline_seconds > 0 时
//     覆盖 wiring 传入的 Deadline（每次 Run 热读取）；键缺席时 opts 值
//     继续生效（允许侧配「前提在场」配对断言）；
//  3. deadline 到期的终端决策带可读摘要（attempts / kinds 直方图 /
//     earliest_recovery_sec），renderSurvivalTerminal 把摘要渲染进三种
//     客户端协议的 message 字段，code/type/reason 字节不变。
//
// 背景（154 生产事故）：候选全灭后 wait_recovery 轮询 + 5h 交互 deadline，
// 客户端拿到 200+keepalive 后被无限挂着（p99=889s、max 85min）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// ── 1. 零值回落 ────────────────────────────────────────────────────────────

// TestSurvivalOptionsZeroDeadlineDefaultsToBounded240s pins the P0-2 default:
// a zero-value Deadline falls back to the 240s bounded budget, NOT the
// historical 5h that hung clients behind keepalives.
func TestSurvivalOptionsZeroDeadlineDefaultsToBounded240s(t *testing.T) {
	got := SurvivalOptions{}.withDefaults().Deadline
	if got != 4*time.Minute {
		t.Fatalf("zero-value Deadline = %v, want 4m (240s, P0-2 bounded budget)", got)
	}
	if got == 5*time.Hour {
		t.Fatal("zero-value Deadline must not fall back to the historical 5h")
	}
	// Explicit deadlines pass through untouched (upper bound is enforced at
	// the config layer; the coordinator honors whatever it is given).
	if d := (SurvivalOptions{Deadline: 90 * time.Second}).withDefaults().Deadline; d != 90*time.Second {
		t.Fatalf("explicit Deadline = %v, want unchanged 90s", d)
	}
}

// ── 2. settings_kv 热加载覆盖 ──────────────────────────────────────────────

// withSurvivalDeadlineSettings pins settings.Global to a registry whose
// platform backend serves the survival deadline key with the given seconds
// value (0 = key absent). Restored via t.Cleanup.
func withSurvivalDeadlineSettings(t *testing.T, seconds int) {
	t.Helper()
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	store := map[string][]byte{}
	if seconds > 0 {
		store[settings.KeySurvivalInteractiveDeadlineSeconds] = []byte(jsonInt(seconds))
	}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, fakeSettingsBackend{store: store})
	for _, spec := range settings.SurvivalSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
}

func jsonInt(v int) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestSurvivalRunDeadlineSettingsOverride pins the hot-reload override and
// its paired premise:
//
//   - override: key=10s beats a 30-minute opts.Deadline — the loop stops at
//     the 10s budget even though the wiring supplied 30 minutes;
//   - premise: with the key ABSENT, the same 30-minute opts.Deadline lets the
//     scripted recovery complete (Run does not stop early — proving the stop
//     in the first case came from the settings key, not from anything else).
//
// Mutation target: survivalInteractiveDeadlineOverride / the override branch
// in Run. Removing it makes the override case run to the scripted recovery
// (or the 30min budget) instead of stopping at 10s.
func TestSurvivalRunDeadlineSettingsOverride(t *testing.T) {
	t.Run("settings key overrides the wiring deadline", func(t *testing.T) {
		withSurvivalDeadlineSettings(t, 10)

		// Endless recoverable failures: the loop may only stop because of
		// the settings-driven deadline.
		h := newCoordHarness(&scriptedExecutor{
			errs: []error{rateLimitFailure(), rateLimitFailure(), rateLimitFailure(), rateLimitFailure()},
		})
		c := h.coordinator()
		c.Options.Deadline = 30 * time.Minute

		res := c.Run(context.Background(), h.sw, &executors.ExecParams{})

		if res.Succeed {
			t.Fatal("deadline override must stop the endless recovery loop")
		}
		if res.Decision.Reason != "deadline_exceeded" {
			t.Fatalf("decision reason = %q, want deadline_exceeded", res.Decision.Reason)
		}
		// The fake clock advanced only ~10s (5s + 5s capped waits) — far
		// below the 30-minute opts.Deadline. Elapsed is observed through
		// the sleeps; each sleep is bounded by the remaining budget.
		total := time.Duration(0)
		for _, s := range h.sleeps {
			total += s
		}
		if total > 15*time.Second {
			t.Fatalf("loop waited %v in total, want ≤15s (the 10s settings budget governs, not the 30m opts deadline)", total)
		}
	})

	t.Run("premise: key absent keeps the wiring deadline in charge", func(t *testing.T) {
		withSurvivalDeadlineSettings(t, 0)

		h := newCoordHarness(&scriptedExecutor{
			errs:    []error{rateLimitFailure(), nil},
			results: []*executors.ExecuteResult{nil, {}},
		})
		c := h.coordinator()
		c.Options.Deadline = 30 * time.Minute

		res := c.Run(context.Background(), h.sw, &executors.ExecParams{})

		if !res.Succeed {
			t.Fatalf("with no override key the 30m opts.Deadline must govern and the scripted recovery must complete, decision=%+v", res.Decision)
		}
		if h.exec.calls != 2 {
			t.Fatalf("executor calls = %d, want 2", h.exec.calls)
		}
	})
}

// ── 3. deadline 摘要：协调器决策 ───────────────────────────────────────────

// TestSurvivalRunDeadlineDecisionCarriesReadableSummary drives the loop into
// the interactive deadline with two rate_limit attempts and pins the summary
// on the synthetic decision (same layer as the decision construction).
func TestSurvivalRunDeadlineDecisionCarriesReadableSummary(t *testing.T) {
	h := newCoordHarness(&scriptedExecutor{errs: []error{rateLimitFailure(), rateLimitFailure()}})
	c := h.coordinator()
	// 6s budget: attempt 1 → 5s wait → attempt 2 → 1s capped wait → deadline.
	c.Options.Deadline = 6 * time.Second

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{})

	if res.Decision.Reason != "deadline_exceeded" {
		t.Fatalf("decision reason = %q, want deadline_exceeded", res.Decision.Reason)
	}
	if res.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", res.Attempts)
	}
	want := "attempts=2 kinds=rate_limit:2"
	if res.Decision.Detail != want {
		t.Fatalf("decision.Detail = %q, want %q", res.Decision.Detail, want)
	}
	// The terminal seam receives the same decision object — the summary must
	// travel to the renderer, not only into the returned result.
	if len(h.terminals) != 1 || h.terminals[0].Detail != want {
		t.Fatalf("terminal decisions = %+v, want one carrying Detail %q", h.terminals, want)
	}
}

// TestSurvivalDeadlineDetailHintSources pins the earliest-recovery hint
// sources at the pure-function layer: per-candidate Retry-After on the final
// attempt wins (minimum), the last aggregated decision's NextRetryAfter is
// the fallback, and no hint degrades to attempts+kinds only.
func TestSurvivalDeadlineDetailHintSources(t *testing.T) {
	kinds := map[errorsx.ErrorKind]int{
		errorsx.KindRateLimit:    3,
		errorsx.KindUpstreamDown: 4,
	}

	t.Run("candidate RetryAfter wins with the minimum", func(t *testing.T) {
		res := SurvivalResult{
			Attempts: 7,
			FinalAttempt: &AttemptResult{CandidateOutcomes: []CandidateOutcome{
				{Kind: errorsx.KindRateLimit, RetryAfter: 45 * time.Second},
				{Kind: errorsx.KindUpstreamDown, RetryAfter: 20 * time.Second},
			}},
		}
		want := "attempts=7 kinds=rate_limit:3,upstream_down:4 earliest_recovery_sec=20"
		if got := survivalDeadlineDetail(res, kinds); got != want {
			t.Fatalf("detail = %q, want %q", got, want)
		}
	})

	t.Run("decision NextRetryAfter is the fallback hint", func(t *testing.T) {
		res := SurvivalResult{
			Attempts: 7,
			Decision: TaskDecision{NextRetryAfter: 30 * time.Second},
			FinalAttempt: &AttemptResult{CandidateOutcomes: []CandidateOutcome{
				{Kind: errorsx.KindRateLimit},
			}},
		}
		want := "attempts=7 kinds=rate_limit:3,upstream_down:4 earliest_recovery_sec=30"
		if got := survivalDeadlineDetail(res, kinds); got != want {
			t.Fatalf("detail = %q, want %q", got, want)
		}
	})

	t.Run("no hint degrades to attempts+kinds", func(t *testing.T) {
		res := SurvivalResult{Attempts: 1}
		want := "attempts=1"
		if got := survivalDeadlineDetail(res, nil); got != want {
			t.Fatalf("detail = %q, want %q", got, want)
		}
	})
}

// ── 3b. deadline 摘要：三协议终端帧 ────────────────────────────────────────

// TestRenderSurvivalTerminalDeadlineSummaryThreeProtocols pins that the
// deadline summary reaches the client-visible message on every protocol arm
// while the machine fields (code/type/reason/retryable) stay byte-identical
// to the pre-P0-2 contract.
func TestRenderSurvivalTerminalDeadlineSummaryThreeProtocols(t *testing.T) {
	detail := "attempts=7 kinds=rate_limit:3,upstream_down:4 earliest_recovery_sec=30"
	decision := TaskDecision{
		Action: TaskActionFailClosed,
		Reason: "deadline_exceeded",
		Detail: detail,
	}

	t.Run("anthropic error frame carries the summary", func(t *testing.T) {
		f := &trackingFlusher{}
		renderSurvivalTerminal(NewSerializedStreamWriter(f), ProtocolAnthropic, decision, false)
		raw := f.buf.String()
		if !strings.HasPrefix(raw, "event: error\ndata: ") {
			t.Fatalf("frame = %q, want an anthropic error event", raw)
		}
		var payload struct {
			Type  string `json:"type"`
			Error struct {
				Type      string `json:"type"`
				Message   string `json:"message"`
				Reason    string `json:"reason"`
				Retryable bool   `json:"retryable"`
				Code      string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(raw, "event: error\ndata: ")), &payload); err != nil {
			t.Fatalf("decode: %v (raw=%q)", err, raw)
		}
		assertDeadlineFrame(t, payload.Error.Message, payload.Error.Reason, payload.Error.Code, payload.Error.Retryable, payload.Error.Type, "api_error", detail)
	})

	t.Run("openai responses failed frame carries the summary", func(t *testing.T) {
		f := &trackingFlusher{}
		renderSurvivalTerminal(NewSerializedStreamWriter(f), ProtocolOpenAIResponses, decision, false)
		raw := f.buf.String()
		if !strings.HasPrefix(raw, "event: response.failed\ndata: ") {
			t.Fatalf("frame = %q, want a response.failed event", raw)
		}
		var payload struct {
			Type     string `json:"type"`
			Response struct {
				Error struct {
					Code      string `json:"code"`
					Message   string `json:"message"`
					Reason    string `json:"reason"`
					Retryable bool   `json:"retryable"`
				} `json:"error"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(raw, "event: response.failed\ndata: ")), &payload); err != nil {
			t.Fatalf("decode: %v (raw=%q)", err, raw)
		}
		assertDeadlineFrame(t, payload.Response.Error.Message, payload.Response.Error.Reason, payload.Response.Error.Code, payload.Response.Error.Retryable, payload.Type, "response.failed", detail)
	})

	t.Run("openai chat error frame carries the summary", func(t *testing.T) {
		f := &trackingFlusher{}
		renderSurvivalTerminal(NewSerializedStreamWriter(f), ProtocolOpenAIChat, decision, false)
		raw := f.buf.String()
		if !strings.HasSuffix(raw, "data: [DONE]\n\n") {
			t.Fatalf("frame = %q, want the chat [DONE] terminator", raw)
		}
		var payload struct {
			Error struct {
				Message   string `json:"message"`
				Type      string `json:"type"`
				Code      string `json:"code"`
				Reason    string `json:"reason"`
				Retryable bool   `json:"retryable"`
			} `json:"error"`
		}
		dataFrame := strings.TrimSuffix(raw, "data: [DONE]\n\n")
		if err := json.Unmarshal([]byte(strings.TrimPrefix(dataFrame, "data: ")), &payload); err != nil {
			t.Fatalf("decode: %v (raw=%q)", err, raw)
		}
		assertDeadlineFrame(t, payload.Error.Message, payload.Error.Reason, payload.Error.Code, payload.Error.Retryable, payload.Error.Type, "server_error", detail)
	})

	t.Run("no detail keeps the historical message byte-identical", func(t *testing.T) {
		f := &trackingFlusher{}
		renderSurvivalTerminal(NewSerializedStreamWriter(f), ProtocolAnthropic, TaskDecision{
			Action: TaskActionFailClosed,
			Reason: "deadline_exceeded",
		}, false)
		var payload struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(f.buf.String(), "event: error\ndata: ")), &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Error.Message != "gateway request survival ended: deadline_exceeded" {
			t.Fatalf("empty Detail changed the message: %q", payload.Error.Message)
		}
	})
}

func assertDeadlineFrame(t *testing.T, message, reason, code string, retryable bool, typ, wantType, detail string) {
	t.Helper()
	if message != "gateway request survival ended: deadline_exceeded; "+detail {
		t.Fatalf("message = %q, want the survival prefix plus the summary %q", message, detail)
	}
	if reason != "deadline_exceeded" {
		t.Fatalf("reason = %q, want the unchanged machine reason deadline_exceeded", reason)
	}
	if code != "gateway_survival_fail_closed" {
		t.Fatalf("code = %q, want the unchanged gateway_survival_fail_closed", code)
	}
	if retryable {
		t.Fatal("fail_closed deadline verdict must stay non-retryable (pre-P0-2 semantics)")
	}
	if typ != wantType {
		t.Fatalf("type = %q, want %q (unchanged)", typ, wantType)
	}
}
