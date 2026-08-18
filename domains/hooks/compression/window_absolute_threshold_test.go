package compression

import (
	"testing"
	"time"
)

func TestWindow_AbsoluteForceUsesActualOutboundTokens(t *testing.T) {
	body := make([]byte, DefaultTokenThresholdForce*7/2+4)
	state := makeState(8, 80_000, 0, 0)

	res := ShouldTriggerWindow(body, state, 0, false, time.Now())
	if !res.ShouldTrigger {
		t.Fatal("expected absolute token trigger")
	}
	if res.Reason != "sliding_window_token_absolute" {
		t.Fatalf("Reason = %q, want sliding_window_token_absolute", res.Reason)
	}
	if res.TokenBand != OutboundTokenBandForced {
		t.Fatalf("TokenBand = %q, want forced", res.TokenBand)
	}
	if res.PriorLayerTokens != 80_000 {
		t.Fatalf("PriorLayerTokens = %d, want 80000", res.PriorLayerTokens)
	}
	if res.Threshold != DefaultTokenThresholdForce {
		t.Fatalf("Threshold = %d, want %d", res.Threshold, DefaultTokenThresholdForce)
	}
}

func TestWindow_AbsoluteForceIgnoresRecentCompressionGuard(t *testing.T) {
	body := make([]byte, DefaultTokenThresholdForce*7/2+4)
	state := makeState(8, 80_000, 0, time.Now().Unix())
	state.RecentlyCompressedAt = time.Now().Unix()

	res := ShouldTriggerWindow(body, state, 0, false, time.Now())
	if !res.ShouldTrigger || res.Degraded {
		t.Fatalf("forced result = %+v, want non-degraded forced trigger", res)
	}
}

func TestWindow_PreviouslyCompressedSessionBelowAbsoluteThreshold(t *testing.T) {
	// The raw client snapshot may be large, but compression decisions use the
	// actual outbound body. Here the prior compressed layer (80k) plus delta
	// results in only 180k outbound tokens, so neither absolute band fires.
	body := make([]byte, 180_000*7/2)
	state := makeState(8, 80_000, 0, 0)

	res := ShouldTriggerWindow(body, state, 1_000_000, false, time.Now())
	if res.TokenBand != OutboundTokenBandBelow {
		t.Fatalf("TokenBand = %q, want below", res.TokenBand)
	}
	if res.Reason != "" || res.ShouldTrigger {
		t.Fatalf("unexpected trigger: reason=%q should=%v", res.Reason, res.ShouldTrigger)
	}
	if res.PriorLayerTokens != 80_000 {
		t.Fatalf("PriorLayerTokens = %d, want 80000", res.PriorLayerTokens)
	}
}

func TestWindow_PreliminaryBandDoesNotForceCompression(t *testing.T) {
	body := make([]byte, 250_000*7/2)
	res := ShouldTriggerWindow(body, makeState(8, 80_000, 0, 0), 1_000_000, false, time.Now())
	if res.TokenBand != OutboundTokenBandPreliminary {
		t.Fatalf("TokenBand = %q, want preliminary", res.TokenBand)
	}
	if res.Reason != "" || res.ShouldTrigger {
		t.Fatalf("preliminary band unexpectedly triggered: reason=%q should=%v", res.Reason, res.ShouldTrigger)
	}
}
