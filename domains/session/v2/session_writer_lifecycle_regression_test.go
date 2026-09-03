package v2

import (
	"context"
	"testing"
)

func TestSessionWriter_EnsureLifecycleRepairsMissingCancel(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()

	writer := &SessionWriterV2{lifecycleCtx: parent}
	writer.ensureLifecycle()
	if writer.lifecycleCancel == nil {
		t.Fatal("ensureLifecycle left lifecycleCancel nil")
	}
	if writer.lifecycleCtx == parent {
		t.Fatal("ensureLifecycle did not derive a cancellable child context")
	}

	if err := writer.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-writer.lifecycleCtx.Done():
	default:
		t.Fatal("Stop did not cancel the repaired lifecycle context")
	}
}
