package bg

import (
	"testing"
	"time"
)

func TestRecoveryProbeDelayForActiveCredential(t *testing.T) {
	want := []time.Duration{
		10 * time.Second,
		30 * time.Second,
		60 * time.Second,
		5 * time.Minute,
		1000 * time.Second,
		time.Hour,
		time.Hour,
	}
	for attempt, expected := range want {
		if got := RecoveryProbeDelay(true, attempt); got != expected {
			t.Errorf("RecoveryProbeDelay(active, %d) = %s, want %s", attempt, got, expected)
		}
	}
}

func TestRecoveryProbeDelayForInactiveRecoverableCredential(t *testing.T) {
	for _, attempt := range []int{0, 1, 10} {
		if got := RecoveryProbeDelay(false, attempt); got != 6*time.Hour {
			t.Errorf("RecoveryProbeDelay(inactive, %d) = %s, want 6h", attempt, got)
		}
	}
}
