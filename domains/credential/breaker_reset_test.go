package credential

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// readCooling returns the breaker's remaining cooling window under its mutex.
func readCooling(b *Breaker) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Until(b.coolingExpires)
}

// TestQuotaFailureWithResetAlignsCooling: quota 族失败带 10min 后的重置时刻
// → 单次记录即 OPEN，冷却 ≈10min（而非无提示时的阈值未满/60s 瞬时策略，
// 也非固定指数窗）。前提在场配对：同一 kind 不带 reset 的单次记录不会开路。
func TestQuotaFailureWithResetAlignsCooling(t *testing.T) {
	b := New(1, 1)
	b.RecordFailureWithReset(errorsx.KindQuotaPeriodic, time.Now().Add(10*time.Minute), true)
	if b.State() != StateOpen {
		t.Fatalf("expected open after single reset-hinted quota failure, got %s", b.State())
	}
	cooling := readCooling(b)
	if cooling < 9*time.Minute || cooling > 11*time.Minute {
		t.Fatalf("expected ~10min cooling aligned to reset, got %s", cooling)
	}
	if b.Allow() {
		t.Fatal("open breaker should not allow requests")
	}

	// 前提在场：同 kind、无 reset hint → 单次记录不满足阈值，不开路。
	b2 := New(2, 1)
	b2.RecordFailure(errorsx.KindQuotaPeriodic)
	if b2.State() != StateClosed {
		t.Fatalf("premise broken: single unhinted quota_periodic failure opened the circuit (%s)", b2.State())
	}
}

// TestQuotaFailureWithResetClampBounds: resetAt 超上限 2h → 钳到 2h；
// resetAt 近于下界 30s → 钳到 30s。
func TestQuotaFailureWithResetClampBounds(t *testing.T) {
	b := New(1, 1)
	b.RecordFailureWithReset(errorsx.KindQuotaPeriodic, time.Now().Add(3*time.Hour), true)
	if got := readCooling(b); got < 110*time.Minute || got > 2*time.Hour+time.Minute {
		t.Fatalf("3h reset should clamp to 2h, got %s", got)
	}

	b2 := New(2, 2)
	b2.RecordFailureWithReset(errorsx.KindQuotaPeriodic, time.Now().Add(5*time.Second), true)
	if got := readCooling(b2); got < 25*time.Second || got > 40*time.Second {
		t.Fatalf("5s reset should clamp to 30s floor, got %s", got)
	}
}

// TestQuotaFailureWithoutResetKeepsLegacyQuarantine: ok=false 回落旧语义——
// KindQuota 两次确认仍 QUARANTINED（RecoveryPermanent 策略不变）。
func TestQuotaFailureWithoutResetKeepsLegacyQuarantine(t *testing.T) {
	b := New(1, 1)
	b.RecordFailureWithReset(errorsx.KindQuota, time.Time{}, false)
	b.RecordFailureWithReset(errorsx.KindQuota, time.Time{}, false)
	if b.State() != StateQuarantined {
		t.Fatalf("expected legacy quarantine without reset hint, got %s", b.State())
	}
}

// TestQuotaResetDowngradesQuarantine: 先按旧语义 QUARANTINED，随后带 reset
// 证据的 quota 失败把状态降级为 OPEN + 对齐冷却（P0-4：QUARANTINED 保留给
// auth 类/真永久；上游显式给出重置时间即证明是时间有界耗尽）。
func TestQuotaResetDowngradesQuarantine(t *testing.T) {
	b := New(1, 1)
	b.RecordFailure(errorsx.KindQuota)
	b.RecordFailure(errorsx.KindQuota)
	if b.State() != StateQuarantined {
		t.Fatalf("premise broken: expected quarantined before reset hint, got %s", b.State())
	}
	b.RecordFailureWithReset(errorsx.KindQuota, time.Now().Add(45*time.Minute), true)
	if b.State() != StateOpen {
		t.Fatalf("expected downgrade to open with reset evidence, got %s", b.State())
	}
	if got := readCooling(b); got < 40*time.Minute || got > 50*time.Minute {
		t.Fatalf("expected ~45min aligned cooling after downgrade, got %s", got)
	}
}

// TestRecordFailureWithResetIgnoresHintForNonQuotaKind: 非 quota 族 kind 的
// reset hint 必须被忽略——行为与 RecordFailure 完全一致（阈值/60s 瞬时冷却）。
func TestRecordFailureWithResetIgnoresHintForNonQuotaKind(t *testing.T) {
	b := New(1, 1)
	b.RecordFailureWithReset(errorsx.KindTimeout, time.Now().Add(10*time.Minute), true)
	if b.State() != StateClosed {
		t.Fatalf("single timeout failure should not open (threshold 2), got %s", b.State())
	}
	b.RecordFailureWithReset(errorsx.KindTimeout, time.Now().Add(10*time.Minute), true)
	b.RecordFailureWithReset(errorsx.KindTimeout, time.Now().Add(10*time.Minute), true)
	if b.State() != StateOpen {
		t.Fatalf("confirmed transient failures should open, got %s", b.State())
	}
	if got := readCooling(b); got > 90*time.Second {
		t.Fatalf("timeout cooling must stay at the transient policy (~60s), not align to the hint; got %s", got)
	}
}

// TestCreditExhaustionViaGradeQuotaRestRoutesFlatCooling: 402 money 文案经
// errorsx.GradeQuotaRest 二分为 credit 类 → RecordFailureWithReset 得到
// 30min 量级冷却（CreditExhaustedCooling），取代永久隔离。
func TestCreditExhaustionViaGradeQuotaRestRoutesFlatCooling(t *testing.T) {
	now := time.Now()
	body := []byte(`{"error":{"message":"Insufficient account balance","code":"INSUFFICIENT_BALANCE"}}`)
	// 前提在场：该体确属 quota 语境分类（402 状态 → quota 族）。
	if kind := errorsx.ClassifyErrorWithBody(402, body); !errorsx.IsQuotaExhaustion(kind) {
		t.Fatalf("premise broken: 402 money body classified %q, want quota family", kind)
	}
	hint := errorsx.GradeQuotaRest(body, now)
	if hint.Class != errorsx.QuotaRestCredit {
		t.Fatalf("GradeQuotaRest(money body) = %q, want credit", hint.Class)
	}

	b := New(1, 1)
	b.RecordFailureWithReset(errorsx.KindQuotaPermanent, hint.ResetAt, hint.HasReset)
	if b.State() != StateOpen {
		t.Fatalf("credit exhaustion should open with flat cooling, got %s", b.State())
	}
	if got := readCooling(b); got < 29*time.Minute || got > 31*time.Minute {
		t.Fatalf("credit exhaustion should cool ~%s, got %s", errorsx.CreditExhaustedCooling, got)
	}
}

// TestRecordModelScopedFailureSkipsCredentialState: 模型级拒收不得累计
// 凭据熔断——连续多次记录后状态仍 CLOSED、consecutive 不涨、放行不变。
func TestRecordModelScopedFailureSkipsCredentialState(t *testing.T) {
	b := New(1, 1)
	for i := 0; i < 5; i++ {
		b.RecordModelScopedFailure(errorsx.KindModelNotFound, "gpt-5-fake")
	}
	if b.State() != StateClosed {
		t.Fatalf("model-scoped failures must not trip the credential breaker, got %s", b.State())
	}
	if got := b.ConsecutiveFailures(); got != 0 {
		t.Fatalf("model-scoped failures must not accumulate consecutive count, got %d", got)
	}
	if !b.Allow() {
		t.Fatal("credential must stay usable for sibling models")
	}
}

// TestRecordModelScopedFailureReleasesHalfOpenProbe: HALF_OPEN 中撞上模型级
// 拒收（探针请求打在已死模型上）→ 归还探针槽，凭据可被下一次请求再探，
// 而不是卡在探针被占的半开态（对齐 KindRateLimit 早退路径的语义）。
func TestRecordModelScopedFailureReleasesHalfOpenProbe(t *testing.T) {
	b := New(1, 1)
	b.mu.Lock()
	b.state.Store(int32(StateHalfOpen))
	b.halfOpenProbes.Store(1)
	b.nextProbeAt = time.Now().Add(halfOpenProbeTimeout)
	b.mu.Unlock()

	b.RecordModelScopedFailure(errorsx.KindModelDeprecated, "glm-5.2")

	if !b.PeekAllowed() {
		t.Fatal("probe slot must be released so the next request can re-probe the credential")
	}
}
