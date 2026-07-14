package bg

import (
	"testing"
	"time"
)

func TestNextDelay_ZeroFailuresReturnsMax(t *testing.T) {
	cfg := DefaultProbeBackoffConfig()
	d := cfg.NextDelay(0)
	if d != cfg.MaxDelay {
		t.Errorf("NextDelay(0) = %v, want %v", d, cfg.MaxDelay)
	}
}

func TestNextDelay_NegativeFailuresReturnsMax(t *testing.T) {
	cfg := DefaultProbeBackoffConfig()
	d := cfg.NextDelay(-1)
	if d != cfg.MaxDelay {
		t.Errorf("NextDelay(-1) = %v, want %v", d, cfg.MaxDelay)
	}
}

func TestNextDelay_FirstFailureIsBase(t *testing.T) {
	cfg := ProbeBackoffConfig{
		BaseDelay:  1 * time.Minute,
		MaxDelay:   2 * time.Hour,
		Multiplier: 2.0,
		Jitter:     0,
	}
	d := cfg.NextDelay(1)
	if d != 1*time.Minute {
		t.Errorf("NextDelay(1) = %v, want 1m", d)
	}
}

func TestNextDelay_ExponentialRamp(t *testing.T) {
	cfg := ProbeBackoffConfig{
		BaseDelay:  1 * time.Minute,
		MaxDelay:   2 * time.Hour,
		Multiplier: 2.0,
		Jitter:     0,
	}
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{1, 1 * time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
	}
	for _, tt := range tests {
		d := cfg.NextDelay(tt.failures)
		if d != tt.want {
			t.Errorf("NextDelay(%d) = %v, want %v", tt.failures, d, tt.want)
		}
	}
}

func TestNextDelay_CapsAtMax(t *testing.T) {
	cfg := ProbeBackoffConfig{
		BaseDelay:  1 * time.Minute,
		MaxDelay:   10 * time.Minute,
		Multiplier: 2.0,
		Jitter:     0,
	}
	d := cfg.NextDelay(5)
	if d != 10*time.Minute {
		t.Errorf("NextDelay(5) = %v, want 10m (capped)", d)
	}
}

func TestNextDelay_JitterBounds(t *testing.T) {
	cfg := ProbeBackoffConfig{
		BaseDelay:  1 * time.Minute,
		MaxDelay:   1 * time.Hour,
		Multiplier: 1.0,
		Jitter:     30 * time.Second,
	}
	for i := 0; i < 100; i++ {
		d := cfg.NextDelay(1)
		if d < 1*time.Minute {
			t.Errorf("NextDelay(1) = %v, below base 1m", d)
		}
		if d > 1*time.Minute+30*time.Second {
			t.Errorf("NextDelay(1) = %v, above base+jitter 1m30s", d)
		}
	}
}

func TestNextDelay_JitterCappedByMax(t *testing.T) {
	cfg := ProbeBackoffConfig{
		BaseDelay:  55 * time.Minute,
		MaxDelay:   1 * time.Hour,
		Multiplier: 1.0,
		Jitter:     30 * time.Second,
	}
	for i := 0; i < 100; i++ {
		d := cfg.NextDelay(1)
		if d > 1*time.Hour {
			t.Errorf("NextDelay(1) with high base+jitter = %v, exceeded max 1h", d)
		}
		if d < 55*time.Minute {
			t.Errorf("NextDelay(1) with high base = %v, below base 55m", d)
		}
	}
}

func TestDefaultBackoff_NonZero(t *testing.T) {
	if DefaultBackoff.BaseDelay != 5*time.Minute {
		t.Errorf("DefaultBackoff.BaseDelay = %v, want 5m", DefaultBackoff.BaseDelay)
	}
	if DefaultBackoff.MaxDelay != 2*time.Hour {
		t.Errorf("DefaultBackoff.MaxDelay = %v, want 2h", DefaultBackoff.MaxDelay)
	}
}

func TestNextDelay_CustomMultiplier(t *testing.T) {
	cfg := ProbeBackoffConfig{
		BaseDelay:  1 * time.Minute,
		MaxDelay:   1 * time.Hour,
		Multiplier: 3.0,
		Jitter:     0,
	}
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{1, 1 * time.Minute},
		{2, 3 * time.Minute},
		{3, 9 * time.Minute},
		{4, 27 * time.Minute},
	}
	for _, tt := range tests {
		d := cfg.NextDelay(tt.failures)
		if d != tt.want {
			t.Errorf("NextDelay(%d) with mult=3 = %v, want %v", tt.failures, d, tt.want)
		}
	}
}
