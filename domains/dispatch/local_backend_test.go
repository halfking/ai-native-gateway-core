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

// Stage B.2 — LocalBackend.New(spec) wraps the existing newGovernor
// factory by mapping GovernorSpec to CredentialRef. The mapping must
// preserve every field that governs factory routing, and the resulting
// Governor must report Mode() consistent with spec.Mode.
func TestLocalBackendNewReturnsExistingGovernors(t *testing.T) {
	b := NewLocalBackend("process-A")
	ctx := context.Background()

	cases := []struct {
		name string
		spec GovernorSpec
		want string
	}{
		{
			name: "concurrency → concurrencyGovernor",
			spec: GovernorSpec{Mode: ModeConcurrency, Limit: 8},
			want: ModeConcurrency,
		},
		{
			name: "rpm → rpmGovernor",
			spec: GovernorSpec{Mode: ModeRPM, RPMLimit: 60},
			want: ModeRPM,
		},
		{
			name: "tpm → tpmGovernor",
			spec: GovernorSpec{Mode: ModeTPM, TPMLimit: 120000},
			want: ModeTPM,
		},
		{
			name: "disabled → noopGovernor",
			spec: GovernorSpec{Mode: ModeDisabled},
			want: ModeDisabled,
		},
		{
			name: "rpm with zero RPMLimit → noopGovernor (degraded)",
			spec: GovernorSpec{Mode: ModeRPM, RPMLimit: 0},
			want: ModeDisabled,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, err := b.New(ctx, tc.spec)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if m := g.Mode(); m != tc.want {
				t.Fatalf("Mode() = %q, want %q", m, tc.want)
			}
		})
	}
}