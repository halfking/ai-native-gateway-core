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
