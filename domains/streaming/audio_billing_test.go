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
}

func (s *stubAudioMaas) Enabled() bool { return s.enabled }

func (s *stubAudioMaas) ChargeRequestMultimodal(ctx context.Context, tenantID, requestID, canonicalName string, usage maas.TokenUsage) (int64, error) {
	s.last = usage
	s.model = canonicalName
	s.tenant = tenantID
	_ = ctx
	_ = requestID
	if usage.AudioSeconds <= 0 {
		return 0, nil
	}
	return int64(usage.AudioSeconds) + 1, nil
}

func TestChargeTranscription_usesDuration(t *testing.T) {
	svc := &AudioService{}
	stub := &stubAudioMaas{enabled: true}
	svc.SetMaas(stub)
	sec := 26.325
	svc.chargeTranscription(context.Background(), "acme", "req-1", "minimax-asr-1.0", &sec)
	if stub.last.AudioSeconds != 26.325 {
		t.Fatalf("seconds=%v", stub.last.AudioSeconds)
	}
	if stub.tenant != "acme" || stub.model != "minimax-asr-1.0" {
		t.Fatalf("tenant/model = %s/%s", stub.tenant, stub.model)
	}
}

func TestChargeTranscription_minOneSecondWhenMissing(t *testing.T) {
	svc := &AudioService{}
	stub := &stubAudioMaas{enabled: true}
	svc.SetMaas(stub)
	svc.chargeTranscription(context.Background(), "acme", "req-2", "glm-asr", nil)
	if stub.last.AudioSeconds != 1 {
		t.Fatalf("want min 1s, got %v", stub.last.AudioSeconds)
	}
}

func TestChargeTranscription_skipsDefaultTenant(t *testing.T) {
	svc := &AudioService{}
	stub := &stubAudioMaas{enabled: true}
	svc.SetMaas(stub)
	sec := 9.0
	svc.chargeTranscription(context.Background(), "default", "req-3", "m", &sec)
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
	svc.chargeTranscription(context.Background(), "default", "req-4", "asr", &sec)
	if log.calls != 1 || log.seconds != 4.2 {
		t.Fatalf("usage log = %+v", log)
	}
}
