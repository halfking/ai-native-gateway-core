package dispatch

import (
	"context"
	"sync"
	"testing"
)

// Compile-time guarantee that LocalBackend satisfies the contract.
var _ GovernorBackend = (*LocalBackend)(nil)

func TestLocalBackendDefaultsToBackendLocal(t *testing.T) {
	b := NewLocalBackend("")
	if b.Kind() != BackendLocal {
		t.Fatalf("Kind: got %q want %q", b.Kind(), BackendLocal)
	}
	if b.Name() != "local" {
		t.Fatalf("Name default: got %q want %q", b.Name(), "local")
	}
}

func TestLocalBackendLifecycleRoundTrip(t *testing.T) {
	b := NewLocalBackend("process-A")
	ctx := context.Background()
	if err := b.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := b.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestLocalBackendOpenIsIdempotent(t *testing.T) {
	b := NewLocalBackend("process-A")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := b.Open(ctx); err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
	}
	if err := b.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Reopen after close must succeed without leaking prior counter state.
	if err := b.Open(ctx); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := b.Close(ctx); err != nil {
		t.Fatalf("re-close: %v", err)
	}
}

func TestLocalBackendCloseOnUnopenedIsSafe(t *testing.T) {
	b := NewLocalBackend("process-A")
	if err := b.Close(context.Background()); err != nil {
		t.Fatalf("Close on unopened: %v", err)
	}
}

func TestLocalBackendNotifyRevisionsIsNoop(t *testing.T) {
	b := NewLocalBackend("process-A")
	if err := b.NotifyRevisions(context.Background(), 1); err != nil {
		t.Fatalf("NotifyRevisions(1): %v", err)
	}
	if err := b.NotifyRevisions(context.Background(), 99); err != nil {
		t.Fatalf("NotifyRevisions(99): %v", err)
	}
}

// Lifecycle must be safe under concurrent Open/Close from management
// goroutines. Stage E will register/unregister backends on policy
// transitions; we pin the locking here so a future refactor that
// drops the mutex cannot silently reintroduce a race.
func TestLocalBackendConcurrentLifecycle(t *testing.T) {
	b := NewLocalBackend("process-A")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = b.Open(context.Background())
		}()
		go func() {
			defer wg.Done()
			_ = b.Close(context.Background())
		}()
	}
	wg.Wait()
	// Final state must be deterministic: either open or closed, never
	// panicked / deadlocked. We just verify the methods still respond.
	_ = b.Open(context.Background())
	_ = b.Close(context.Background())
}