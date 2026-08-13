package dispatch

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestQueueMetricsCollector_DegradedMode verifies that a nil pipeline
// results in a permanently degraded collector (Wired=false).
func TestQueueMetricsCollector_DegradedMode(t *testing.T) {
	c := NewQueueMetricsCollector(nil)
	if c == nil {
		t.Fatal("NewQueueMetricsCollector(nil) returned nil")
	}

	snap := c.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot() returned nil in degraded mode")
	}
	if snap.Wired {
		t.Errorf("expected Wired=false in degraded mode, got true")
	}
	if snap.Enabled {
		t.Errorf("expected Enabled=false in degraded mode, got true")
	}
}

// TestQueueMetricsCollector_WiredMode verifies that a valid pipeline
// results in a wired collector (Wired=true) and gate state is tracked.
func TestQueueMetricsCollector_WiredMode(t *testing.T) {
	// Create a real pipeline
	p := NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) { return nil, nil },
		ModelResolveFunc: func(ctx context.Context, req string, tried []string) (string, []string, error) {
			return "model-a", nil, nil
		},
		ForwardFunc: func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
		AllowModelChange: false,
	})
	p.Start()
	defer p.Stop()

	c := NewQueueMetricsCollector(p)
	if c == nil {
		t.Fatal("NewQueueMetricsCollector(pipeline) returned nil")
	}

	snap := c.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot() returned nil in wired mode")
	}
	if !snap.Wired {
		t.Errorf("expected Wired=true in wired mode, got false")
	}

	// Gate should be enabled by default (gate.go init)
	if !snap.Enabled {
		t.Errorf("expected Enabled=true (default gate state), got false")
	}
}

// TestQueueMetricsCollector_SnapshotReflectsPipelineState verifies that
// Snapshot() reads live depths from Pipeline.Snapshot().
func TestQueueMetricsCollector_SnapshotReflectsPipelineState(t *testing.T) {
	p := NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) {
			return []CredentialRef{{CredentialID: 42, ConcurrencyMode: "concurrency"}}, nil
		},
		ModelResolveFunc: func(ctx context.Context, req string, tried []string) (string, []string, error) {
			return "gpt-4", nil, nil
		},
		ForwardFunc: func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
			// Simulate slow forward to keep request in queue
			time.Sleep(100 * time.Millisecond)
			return ForwardOutcome{}
		},
		AllowModelChange: false,
	})
	p.Start()
	defer p.Stop()

	c := NewQueueMetricsCollector(p)

	// Submit a request to populate model queue (non-blocking via goroutine)
	go func() {
		qr := &QueuedRequest{
			ID:             "test-1",
			RequestedModel: "gpt-4",
			ResultCh:       make(chan ForwardOutcome, 1),
			Ctx:            context.Background(),
		}
		p.Submit(context.Background(), qr)
	}()

	// Give pipeline time to enqueue
	time.Sleep(50 * time.Millisecond)

	snap := c.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot() returned nil")
	}

	// Should have at least one model lane (gpt-4) or credential lane (42)
	if len(snap.Models) == 0 && len(snap.Credentials) == 0 {
		t.Errorf("expected non-empty model or credential lanes, got empty snapshot")
	}
}

// TestQueueMetricsCollector_ConcurrentSnapshots verifies thread-safety
// under concurrent Snapshot() calls (race detector must pass).
func TestQueueMetricsCollector_ConcurrentSnapshots(t *testing.T) {
	p := NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) { return nil, nil },
		ModelResolveFunc: func(ctx context.Context, req string, tried []string) (string, []string, error) {
			return "model-b", nil, nil
		},
		ForwardFunc: func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
		AllowModelChange: false,
	})
	p.Start()
	defer p.Stop()

	c := NewQueueMetricsCollector(p)

	// Simulate 10 concurrent SSE ticks reading snapshots
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				snap := c.Snapshot()
				if snap == nil {
					t.Errorf("Snapshot() returned nil during concurrent read")
					return
				}
				if !snap.Wired {
					t.Errorf("expected Wired=true, got false during concurrent read")
					return
				}
			}
		}()
	}

	wg.Wait()
}

// TestQueueMetricsCollector_GateTransition verifies that the collector
// tracks dispatch gate transitions (enabled ↔ disabled).
func TestQueueMetricsCollector_GateTransition(t *testing.T) {
	// Save original gate state and restore after test
	originalState := IsDispatchEnabled()
	defer SetDispatchEnabled(originalState)

	p := NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) { return nil, nil },
		ModelResolveFunc: func(ctx context.Context, req string, tried []string) (string, []string, error) {
			return "model-c", nil, nil
		},
		ForwardFunc: func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
		AllowModelChange: false,
	})
	p.Start()
	defer p.Stop()

	c := NewQueueMetricsCollector(p)

	// Initial state (should be enabled by default)
	snap1 := c.Snapshot()
	if !snap1.Enabled {
		t.Errorf("expected initial Enabled=true, got false")
	}

	// Disable gate
	SetDispatchEnabled(false)
	time.Sleep(10 * time.Millisecond) // Allow transition handler to fire

	snap2 := c.Snapshot()
	if snap2.Enabled {
		t.Errorf("expected Enabled=false after SetDispatchEnabled(false), got true")
	}

	// Re-enable gate
	SetDispatchEnabled(true)
	time.Sleep(10 * time.Millisecond)

	snap3 := c.Snapshot()
	if !snap3.Enabled {
		t.Errorf("expected Enabled=true after SetDispatchEnabled(true), got false")
	}
}

// TestQueueMetricsCollector_RecordHooksNonBlocking verifies that
// RecordEnqueue/RecordDequeue hooks do not panic and are safe no-ops
// (current implementation does not use push hooks, only pull via Snapshot).
func TestQueueMetricsCollector_RecordHooksNonBlocking(t *testing.T) {
	p := NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) { return nil, nil },
		ModelResolveFunc: func(ctx context.Context, req string, tried []string) (string, []string, error) {
			return "model-d", nil, nil
		},
		ForwardFunc: func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
		AllowModelChange: false,
	})
	p.Start()
	defer p.Stop()

	c := NewQueueMetricsCollector(p)

	// Should not panic
	c.RecordEnqueue("gpt-4", 0, "")
	c.RecordEnqueue("", 123, "rpm")
	c.RecordDequeue("gpt-4", 0)
	c.RecordDequeue("", 123)

	// Degraded mode should also be safe
	cDegraded := NewQueueMetricsCollector(nil)
	cDegraded.RecordEnqueue("model-x", 0, "")
	cDegraded.RecordDequeue("", 456)
}
