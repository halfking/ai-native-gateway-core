package credential

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// P0-C 同层 wiring：Breaker.RecordFailure 经 PenaltyOf 短路。
// json-word 400（r1027）→ KindClientBug → PenaltyNone → 节点不进冷却。

func TestP0CBreakerRecordFailureSkipsPenaltyNone(t *testing.T) {
	// r1027 raw-logs 原文：json mode 契约 → Classify → KindClientBug。
	body := []byte(`{"error":{"message":"'messages' must contain the word 'json' in some form, to use 'response_format' of type 'json_object'.","type":"invalid_request_error","param":"messages","code":null}}`)
	kind := errorsx.ClassifyErrorWithBody(400, body)
	if kind != errorsx.KindClientBug {
		t.Fatalf("classify json-word 400 = %q, want KindClientBug (前提在场)", kind)
	}
	if errorsx.PenaltyOf(kind).Target != errorsx.PenaltyNone {
		t.Fatalf("PenaltyOf(%s) target = %v, want none", kind, errorsx.PenaltyOf(kind))
	}

	b := NewManager().GetOrCreate(1, 42)
	for i := 0; i < 5; i++ {
		b.RecordFailure(ErrorKind(kind))
	}
	if b.State() != StateClosed {
		t.Fatalf("after 5× json-word client_bug RecordFailure state=%s, want closed (no cool)", b.State())
	}
	if b.ConsecutiveFailures() != 0 {
		t.Fatalf("consecutive_failures=%d, want 0", b.ConsecutiveFailures())
	}
	// 规划「同凭据其它模型仍可选」：PenaltyNone 后 Allow 仍 true（兄弟模型不连坐）。
	if !b.Allow() {
		t.Fatal("breaker.Allow() false after client_bug; same-cred sibling models would be blocked")
	}
}

func TestP0CBreakerRecordFailureStillCoolsTransient(t *testing.T) {
	// 前提在场配对：同入口对真实故障仍冷却——证明短路不是「恒跳过」。
	b := NewManager().GetOrCreate(1, 43)
	b.RecordFailure(KindNetwork)
	b.RecordFailure(KindNetwork)
	if b.State() == StateClosed && b.ConsecutiveFailures() == 0 {
		t.Fatal("transient RecordFailure must accumulate (前提在场); got closed+0")
	}
}

func TestP0CBreakerRecordFailureSkipsContextLength(t *testing.T) {
	// PenaltyNone 扩集：context_length 也不冷却（原 IsClientBug 未覆盖）。
	b := NewManager().GetOrCreate(1, 44)
	for i := 0; i < 5; i++ {
		b.RecordFailure(ErrorKind(errorsx.KindContextLength))
	}
	if b.State() != StateClosed {
		t.Fatalf("context_length must not cool breaker, state=%s", b.State())
	}
}

func TestP0CBreakerQuotaResetStillCools(t *testing.T) {
	// rest 分级同层：PenaltyOf(quota)≠none → RecordFailureWithReset 仍对齐冷却。
	if errorsx.PenaltyOf(errorsx.KindQuotaPeriodic).Target == errorsx.PenaltyNone {
		t.Fatal("quota_periodic must not be PenaltyNone")
	}
	b := NewManager().GetOrCreate(1, 45)
	resetAt := time.Now().Add(19 * time.Minute)
	b.RecordFailureWithReset(errorsx.KindQuotaPeriodic, resetAt, true)
	until := b.CoolingUntil()
	if until.IsZero() {
		t.Fatal("quota RecordFailureWithReset must set CoolingUntil")
	}
	skew := until.Sub(resetAt)
	if skew < -2*time.Second || skew > 2*time.Second {
		t.Fatalf("CoolingUntil=%v resetAt=%v skew=%v, want ≈aligned", until, resetAt, skew)
	}
}
