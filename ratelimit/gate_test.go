package ratelimit

import "testing"

func TestRateLimitGate(t *testing.T) {
	SetRateLimitEnabled(true)
	t.Cleanup(func() { SetRateLimitEnabled(true) })

	if !IsRateLimitEnabled() {
		t.Fatal("gate should be enabled initially")
	}

	DisableRateLimit()
	if IsRateLimitEnabled() {
		t.Fatal("gate should be disabled")
	}

	EnableRateLimit()
	if !IsRateLimitEnabled() {
		t.Fatal("gate should be enabled after restore")
	}
}
