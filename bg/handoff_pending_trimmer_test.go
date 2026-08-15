package bg

import (
	"context"
	"testing"
	"time"
)

func TestHandoffPendingTrimmerNilPoolAndLifecycle(t *testing.T) {
	trimmer := NewHandoffPendingTrimmer(nil)
	if trimmer.tick != time.Minute {
		t.Fatalf("tick = %v, want one minute", trimmer.tick)
	}
	count, err := trimmer.TrimOnce(context.Background())
	if err != nil || count != 0 {
		t.Fatalf("TrimOnce(nil) = (%d, %v), want (0, nil)", count, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	trimmer.Start(ctx)
	cancel()
	select {
	case <-trimmer.done:
	case <-time.After(time.Second):
		t.Fatal("trimmer did not stop after context cancellation")
	}
	trimmer.Stop()
	trimmer.Stop()
}

// TestHandoffPendingTrimmer_RetentionFloor guards the safety floor: a TTL
// below 1 day (or an unset key) must never produce a zero/negative retention
// that would wipe handoff_pending_confirmations in a single batch.
func TestHandoffPendingTrimmer_RetentionFloor(t *testing.T) {
	got := pendingConfirmationRetention()
	if got < 24*time.Hour {
		t.Fatalf("pendingConfirmationRetention = %v, want >= 24h (must never wipe the table)", got)
	}
	if got != 14*24*time.Hour {
		t.Fatalf("pendingConfirmationRetention = %v, want 14d default", got)
	}
}
