package dispatch

import (
	"context"
	"sync"
	"testing"
	"time"
)

func newQueueCollectorForPipeline(p *Pipeline) *QueueMetricsCollector {
	if p == nil {
		return NewQueueMetricsCollector(nil)
	}
	projection := NewQueueProjection()
	p.SetQueueObservationSink(projection)
	return NewQueueMetricsCollector(projection)
}

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
	if !snap.Enabled {
		t.Errorf("expected Enabled=true in degraded mode, got false")
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

	c := newQueueCollectorForPipeline(p)
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
	releaseForward := make(chan struct{})
	p := NewPipeline(Deps{
		RouteFunc: func(ctx context.Context, qr *QueuedRequest) ([]CredentialRef, error) {
			return []CredentialRef{{CredentialID: 42, ConcurrencyMode: "concurrency"}}, nil
		},
		ModelResolveFunc: func(ctx context.Context, req string, tried []string) (string, []string, error) {
			return "gpt-4", nil, nil
		},
		ForwardFunc: func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
			<-releaseForward
			return ForwardOutcome{}
		},
		AllowModelChange: false,
	})
	p.Start()
	defer p.Stop()
	defer close(releaseForward)

	c := newQueueCollectorForPipeline(p)

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

	if snap.Pipeline == nil || snap.Pipeline.InFlight < 1 {
		t.Errorf("expected an in-flight request while forward is blocked, got %+v", snap.Pipeline)
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

	c := newQueueCollectorForPipeline(p)

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

	c := newQueueCollectorForPipeline(p)

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
