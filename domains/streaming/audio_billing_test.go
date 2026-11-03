package streaming

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/maas"
)

type stubAudioMaas struct {
	enabled bool
	last    maas.TokenUsage
	model   string
	tenant  string
	owner   string
	stamped float64
}

func (s *stubAudioMaas) Enabled() bool { return s.enabled }

func (s *stubAudioMaas) ChargeRequestMultimodalWithMultiplier(ctx context.Context, tenantID, ownerUser, requestID, canonicalName string, usage maas.TokenUsage, rateMultiplier float64) (int64, float64, error) {
	s.last = usage
	s.model = canonicalName
	s.tenant = tenantID
	s.owner = ownerUser
	s.stamped = rateMultiplier
	_ = ctx
	_ = requestID
	if usage.AudioSeconds <= 0 {
		return 0, rateMultiplier, nil
	}
	return int64(usage.AudioSeconds) + 1, rateMultiplier, nil
}

func TestChargeTranscription_usesDuration(t *testing.T) {
	svc := &AudioService{}
	stub := &stubAudioMaas{enabled: true}
	svc.SetMaas(stub)
	sec := 26.325
	svc.chargeTranscription(context.Background(), "acme", "", "req-1", "minimax-asr-1.0", &sec)
	if stub.last.AudioSeconds != 26.325 {
		t.Fatalf("seconds=%v", stub.last.AudioSeconds)
	}
	if stub.tenant != "acme" || stub.model != "minimax-asr-1.0" {
		t.Fatalf("tenant/model = %s/%s", stub.tenant, stub.model)
	}
}

func TestChargeTranscription_passesOwnerUser(t *testing.T) {
	// F-4（2026-11-04 审计）：音频车道扣费必须把 ownerUser 透传给
	// ChargeRequestMultimodalWithMultiplier（credit_ledger.owner_user 的
	// 写入源），否则音频 consume 流水在 /api/maas/my/* 用户视角不可见。
	svc := &AudioService{}
	stub := &stubAudioMaas{enabled: true}
	svc.SetMaas(stub)
	sec := 3.5
	svc.chargeTranscription(context.Background(), "acme", "alice@example.com", "req-owner-1", "minimax-asr-1.0", &sec)
	if stub.owner != "alice@example.com" {
		t.Fatalf("charger owner_user = %q, want alice@example.com", stub.owner)
	}
	if stub.tenant != "acme" || stub.model != "minimax-asr-1.0" {
		t.Fatalf("tenant/model = %s/%s", stub.tenant, stub.model)
	}
	if stub.stamped != 1.0 {
		t.Fatalf("audio lane must keep explicit 1.0 multiplier, got %v", stub.stamped)
	}
	// 空串 = 归属未知（历史形状），仍必须照常透传而不是吞掉。
	stub2 := &stubAudioMaas{enabled: true}
	svc2 := &AudioService{}
	svc2.SetMaas(stub2)
	svc2.chargeTranscription(context.Background(), "acme", "", "req-owner-2", "glm-asr", &sec)
	if stub2.owner != "" {
		t.Fatalf("empty owner must pass through as empty, got %q", stub2.owner)
	}
}

func TestChargeTranscription_minOneSecondWhenMissing(t *testing.T) {
	svc := &AudioService{}
	stub := &stubAudioMaas{enabled: true}
	svc.SetMaas(stub)
	svc.chargeTranscription(context.Background(), "acme", "", "req-2", "glm-asr", nil)
	if stub.last.AudioSeconds != 1 {
		t.Fatalf("want min 1s, got %v", stub.last.AudioSeconds)
	}
}

func TestChargeTranscription_skipsDefaultTenant(t *testing.T) {
	svc := &AudioService{}
	stub := &stubAudioMaas{enabled: true}
	svc.SetMaas(stub)
	sec := 9.0
	svc.chargeTranscription(context.Background(), "default", "", "req-3", "m", &sec)
	if stub.last.AudioSeconds != 0 {
		t.Fatalf("default tenant must not charge, got %#v", stub.last)
	}
}

type stubUsageLog struct {
	seconds float64
	credits int64
	calls   int
}

func (s *stubUsageLog) LogAudioUsage(ctx context.Context, tenantID, requestID, model string, seconds float64, credits int64) {
	s.seconds = seconds
	s.credits = credits
	s.calls++
}

func TestChargeTranscription_writesUsageLog(t *testing.T) {
	svc := &AudioService{}
	log := &stubUsageLog{}
	svc.SetUsageLogger(log)
	sec := 4.2
	svc.chargeTranscription(context.Background(), "default", "", "req-4", "asr", &sec)
	if log.calls != 1 || log.seconds != 4.2 {
		t.Fatalf("usage log = %+v", log)
	}
}
