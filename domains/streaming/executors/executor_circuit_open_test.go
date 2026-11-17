package executors

// executor_circuit_open_test.go —— W1-A（2026-11-18）：钉住「熔断把候选打空」
// 这条链路上的两个判据：
//
//	① dispatchExhaustedByCircuitOpen —— 什么算「熔断打空」；
//	② dispatchErrToExecuteError —— 真实 kind 不能塌成 transient。
//
// ── 为什么需要这些钉测 ───────────────────────────────────────
//
// 缺口本体（源码核验）：熔断准入返回 ForwardOutcome{ErrorKind:"circuit_open"}，
// planner 换完兄弟凭据后终态错误是 dispatch.ExhaustedError；而
// dispatchErrToExecuteError 的 default 分支把 forward 路径的哨兵一律映射成
// KindTransient ⇒ 真实 kind 在执行器层丢失 ⇒ 客户端与运维都分不清
// 「我们主动熔断」与「上游挂了」。
//
// 判据只看最后一个错误是不够的：ExhaustedError.Unwrap 会把最后一个具体
// 失败透出来 ⇒ 混合场景（凭据 A 被熔断、凭据 B 上游 500）里
// errors.Is(exhausted, errDispatchCircuitOpen) 也可能为真 ⇒ 必须看尝试摘要。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// circuitOpenAttempts 造一份「每条尝试都是熔断拒的」摘要。
func circuitOpenAttempts(reason string, n int) []dispatch.ExhaustionAttempt {
	out := make([]dispatch.ExhaustionAttempt, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, dispatch.ExhaustionAttempt{
			Model:        "gpt-4o",
			ProviderID:   int64(10 + i),
			CredentialID: int64(20 + i),
			Reason:       reason,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// 1. 判据：什么算「熔断打空」
// ---------------------------------------------------------------------------

func TestDispatchExhaustedByCircuitOpen_Judge(t *testing.T) {
	circuitReason := string(errorsx.KindCircuitOpen)
	cases := []struct {
		name string
		in   error
		want bool
	}{
		{
			name: "全部尝试都是熔断",
			in: &dispatch.ExhaustedError{
				Cause:    errDispatchCircuitOpen,
				Attempts: circuitOpenAttempts(circuitReason, 2),
			},
			want: true,
		},
		{
			name: "单条尝试是熔断",
			in: &dispatch.ExhaustedError{
				Cause:    errDispatchCircuitOpen,
				Attempts: circuitOpenAttempts(circuitReason, 1),
			},
			want: true,
		},
		{
			name: "混合：熔断 + 上游 500（主导因是上游，不得判成熔断）",
			in: &dispatch.ExhaustedError{
				Cause: &upstreampkg.Error{Kind: errorsx.KindTransient, StatusCode: 502},
				Attempts: append(
					circuitOpenAttempts(circuitReason, 1),
					dispatch.ExhaustionAttempt{Model: "gpt-4o", ProviderID: 11, CredentialID: 21, Reason: string(errorsx.KindTransient)},
				),
			},
			want: false,
		},
		{
			name: "混合：上游 500 + 熔断（熔断在最后一条也不许判成熔断）",
			in: &dispatch.ExhaustedError{
				Cause:    errDispatchCircuitOpen,
				Attempts: circuitOpenAttempts(circuitReason, 1),
			},
			want: true, // 只有一条尝试 ⇒ 它就是全部
		},
		{
			name: "无摘要 + 裸熔断哨兵",
			in:   errDispatchCircuitOpen,
			want: true,
		},
		{
			name: "无摘要 + 裸上游错误",
			in:   &upstreampkg.Error{Kind: errorsx.KindTransient, StatusCode: 502},
			want: false,
		},
		{
			name: "nil",
			in:   nil,
			want: false,
		},
		{
			name: "普通错误",
			in:   errors.New("boom"),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dispatchExhaustedByCircuitOpen(tc.in); got != tc.want {
				t.Fatalf("dispatchExhaustedByCircuitOpen = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDispatchExhaustedByCircuitOpen_MixedWithUpstreamFirst pins the case that
// actually matters in production: a candidate set where one credential was
// circuit-open and another failed upstream. The dominant cause is the upstream
// failure, so the client must NOT be told "everything is circuit-open".
func TestDispatchExhaustedByCircuitOpen_MixedWithUpstreamFirst(t *testing.T) {
	circuitReason := string(errorsx.KindCircuitOpen)
	mixed := &dispatch.ExhaustedError{
		Cause: &upstreampkg.Error{Kind: errorsx.KindTransient, StatusCode: 502},
		Attempts: []dispatch.ExhaustionAttempt{
			{Model: "gpt-4o", ProviderID: 10, CredentialID: 20, Reason: circuitReason},
			{Model: "gpt-4o", ProviderID: 11, CredentialID: 21, Reason: string(errorsx.KindTransient)},
		},
	}
	if dispatchExhaustedByCircuitOpen(mixed) {
		t.Fatal("a mixed exhaustion must not be claimed as circuit-open")
	}
	ee := dispatchErrToExecuteError(mixed)
	if ee.LastKind == errorsx.KindCircuitOpen {
		t.Fatal("LastKind must not be circuit_open for a mixed exhaustion")
	}
}

// ---------------------------------------------------------------------------
// 2. 真实 kind 不能塌成 transient
// ---------------------------------------------------------------------------

func TestDispatchErrToExecuteError_CircuitOpenKindPreserved(t *testing.T) {
	circuitReason := string(errorsx.KindCircuitOpen)
	exhausted := &dispatch.ExhaustedError{
		Cause:    errDispatchCircuitOpen,
		Attempts: circuitOpenAttempts(circuitReason, 2),
	}
	ee := dispatchErrToExecuteError(exhausted)
	if ee == nil {
		t.Fatal("dispatchErrToExecuteError returned nil")
	}
	if ee.LastKind != errorsx.KindCircuitOpen {
		t.Fatalf("LastKind = %q, want %q (the circuit-open cause must not collapse to transient)",
			ee.LastKind, errorsx.KindCircuitOpen)
	}
	if !ee.Exhausted {
		t.Fatal("Exhausted must be true")
	}
	// LastErr 仍须包住原错误（既有契约：调用方按具体错误做 errors.Is 匹配）。
	if !errors.Is(ee.LastErr, errDispatchCircuitOpen) {
		t.Fatalf("LastErr does not wrap the original cause: %v", ee.LastErr)
	}
}

// TestDispatchErrToExecuteError_CircuitOpenIsNotGatewayRetryable pins the
// deliberate distinction: circuit-open is terminal **for the gateway's own
// internal retry loop** (retrying would hit the same open breaker), but it is
// NOT a client-side permanent failure. The client-side verdict lives in the
// handler's dedicated branch (provider_circuit_open + retryable=true).
func TestDispatchErrToExecuteError_CircuitOpenIsNotGatewayRetryable(t *testing.T) {
	circuitReason := string(errorsx.KindCircuitOpen)
	exhausted := &dispatch.ExhaustedError{
		Cause:    errDispatchCircuitOpen,
		Attempts: circuitOpenAttempts(circuitReason, 1),
	}
	ee := dispatchErrToExecuteError(exhausted)
	if errorsx.EffectiveRetryable(ee.LastKind) {
		t.Fatalf("EffectiveRetryable(%q) = true — the gateway must not loop on an open breaker", ee.LastKind)
	}
}

// TestDispatchErrToExecuteError_NonCircuitUnchanged is the阴性对照: every other
// exhaustion keeps its pre-existing mapping (zero regression surface).
func TestDispatchErrToExecuteError_NonCircuitUnchanged(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want errorsx.ErrorKind
	}{
		{"no-route", dispatch.ErrNoRoute, errorsx.KindConcurrent},
		{"overflow", dispatch.ErrOverflow, errorsx.KindConcurrent},
		{"deadline", context.DeadlineExceeded, errorsx.KindTimeout},
		{"generic", errors.New("boom"), errorsx.KindTransient},
		{"upstream-502", &upstreampkg.Error{Kind: errorsx.KindTransient, StatusCode: 502}, errorsx.KindTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ee := dispatchErrToExecuteError(tc.in)
			if ee == nil {
				t.Fatal("dispatchErrToExecuteError returned nil")
			}
			if ee.LastKind != tc.want {
				t.Fatalf("LastKind = %q, want %q (non-circuit exhaustion must not change)", ee.LastKind, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3. Retry-After：取自熔断器自己的冷却截止
// ---------------------------------------------------------------------------

func TestCircuitOpenRetryAfterSeconds_FromBreakerCooling(t *testing.T) {
	mgr := credential.NewManager()
	// 冷却窗口对齐到一个明确的未来时刻（openAlignedToQuotaReset 会把
	// CoolingUntil 设为该 resetAt）。
	resetAt := time.Now().Add(90 * time.Second)
	mgr.RecordFailureWithReset(7, 7, credential.KindQuota, resetAt, true)
	if got := mgr.Get(7, 7).State(); got != credential.StateOpen {
		t.Fatalf("breaker state = %v, want open", got)
	}

	got := circuitOpenRetryAfterSeconds(mgr, []provider.Candidate{{ProviderID: 7, CredentialID: 7}})
	if got <= 0 {
		t.Fatalf("retry-after = %d, want a positive number derived from the breaker cooling window", got)
	}
	if got > 95 {
		t.Fatalf("retry-after = %d, want it close to the ~90s cooling window", got)
	}
}

func TestCircuitOpenRetryAfterSeconds_EarliestWins(t *testing.T) {
	mgr := credential.NewManager()
	// 两个熔断器的冷却窗口不同。★ 注意配额对齐路径有 30s 下限
	// （breaker.go 的 quotaResetMinCooling）⇒ 这里的两个窗口都取 >30s，
	// 否则会被夹到同一个值，"取最早"就测不出差别。
	soon := time.Now().Add(45 * time.Second)
	late := time.Now().Add(600 * time.Second)
	mgr.RecordFailureWithReset(1, 1, credential.KindQuota, late, true)
	mgr.RecordFailureWithReset(2, 2, credential.KindQuota, soon, true)

	got := circuitOpenRetryAfterSeconds(mgr, []provider.Candidate{
		{ProviderID: 1, CredentialID: 1},
		{ProviderID: 2, CredentialID: 2},
	})
	if got < 40 || got > 50 {
		t.Fatalf("retry-after = %d, want the earliest cooling window (~45s), not the latest (~600s)", got)
	}
}

func TestCircuitOpenRetryAfterSeconds_EdgeCases(t *testing.T) {
	mgr := credential.NewManager()
	// 熔断器里没有这个 key ⇒ 算不出 ⇒ 0。
	if got := circuitOpenRetryAfterSeconds(mgr, []provider.Candidate{{ProviderID: 99, CredentialID: 99}}); got != 0 {
		t.Fatalf("retry-after = %d, want 0 for an unknown breaker", got)
	}
	// 没有候选 ⇒ 0。
	if got := circuitOpenRetryAfterSeconds(mgr, nil); got != 0 {
		t.Fatalf("retry-after = %d, want 0 for no candidates", got)
	}
	// 熔断器为 nil ⇒ 0（不得 panic）。
	if got := circuitOpenRetryAfterSeconds(nil, []provider.Candidate{{ProviderID: 1, CredentialID: 1}}); got != 0 {
		t.Fatalf("retry-after = %d, want 0 for a nil manager", got)
	}
	// 闭合态的节点不参与「还要等多久」的计算。
	mgr.RecordFailure(3, 3, credential.KindTransient)
	mgr.RecordFailure(3, 3, credential.KindTransient)
	mgr.RecordSuccess(3, 3)
	if got := mgr.Get(3, 3).State(); got != credential.StateClosed {
		t.Fatalf("breaker state = %v, want closed", got)
	}
	if got := circuitOpenRetryAfterSeconds(mgr, []provider.Candidate{{ProviderID: 3, CredentialID: 3}}); got != 0 {
		t.Fatalf("retry-after = %d, want 0 for a closed breaker", got)
	}
}

// TestCircuitOpenRetrySeconds_Pure pins the seconds conversion on its own.
//
// ★ 为什么单独测：冷却「刚好到期」这条分支在集成测试里造不出来（熔断器
// 只在真实等待或状态迁移时推进冷却窗）⇒ 内联在采集逻辑里就永远没有直测。
func TestCircuitOpenRetrySeconds_Pure(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name       string
		coolingTil time.Time
		want       int
	}{
		{"零值（无冷却窗）", time.Time{}, 0},
		{"已到期", now.Add(-5 * time.Second), 1},
		{"恰好到期", now, 1},
		{"未来 45s", now.Add(45 * time.Second), 45},
		{"未来 90s", now.Add(90 * time.Second), 90},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := circuitOpenRetrySeconds(tc.coolingTil, now); got != tc.want {
				t.Fatalf("circuitOpenRetrySeconds = %d, want %d", got, tc.want)
			}
		})
	}
}
