package requestarchive

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRegistryRegisterGetList(t *testing.T) {
	t.Parallel()
	registry := NewActiveRequestRegistry()

	entries := []ActiveRequest{
		{RequestID: "req-b", TenantID: "tenant-2", SessionID: "session-2", Endpoint: "/v1/chat/completions"},
		{RequestID: "req-a", TenantID: "tenant-1", SessionID: "session-1", Endpoint: "/v1/messages"},
	}
	for _, entry := range entries {
		if err := registry.Register(entry); err != nil {
			t.Fatalf("Register(%s) error = %v", entry.RequestID, err)
		}
	}

	got, ok := registry.Get("req-a")
	if !ok || got.TenantID != "tenant-1" || got.Endpoint != "/v1/messages" {
		t.Fatalf("Get(req-a) = (%+v, %v)", got, ok)
	}
	if _, ok := registry.Get("req-missing"); ok {
		t.Fatal("Get(req-missing) reported present")
	}

	// List must be sorted by request id so callers observe stable snapshots.
	list := registry.List()
	if len(list) != 2 || list[0].RequestID != "req-a" || list[1].RequestID != "req-b" {
		t.Fatalf("List() = %+v, want [req-a req-b]", list)
	}

	if err := registry.UpdateState("req-a", StateTerminal); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	if err := registry.MarkPersistOutcome("req-a", PersistOutcomeSuccess); err != nil {
		t.Fatalf("MarkPersistOutcome() error = %v", err)
	}
	got, _ = registry.Get("req-a")
	if got.State != StateTerminal || got.PersistOutcome != PersistOutcomeSuccess {
		t.Fatalf("updated entry = %+v, want terminal/success", got)
	}

	// Mutating a retrieved snapshot must not leak into registry state — the
	// registry owns copies, not aliases.
	got.TenantID = "mutated"
	fresh, _ := registry.Get("req-a")
	if fresh.TenantID != "tenant-1" {
		t.Fatalf("registry state leaked through Get: %+v", fresh)
	}

	if !registry.Remove("req-a") {
		t.Fatal("Remove(req-a) reported absent")
	}
	if registry.Remove("req-a") {
		t.Fatal("double Remove(req-a) reported present")
	}
	if list := registry.List(); len(list) != 1 || list[0].RequestID != "req-b" {
		t.Fatalf("List() after remove = %+v, want only req-b", list)
	}
}

func TestRegistryRejectsDuplicateAndUnknown(t *testing.T) {
	t.Parallel()
	registry := NewActiveRequestRegistry()

	if err := registry.Register(ActiveRequest{RequestID: "req-1", TenantID: "tenant-1"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	// A live id must have exactly one owner; a second registration would let
	// Remove-based cleanup delete a successor's entry.
	err := registry.Register(ActiveRequest{RequestID: "req-1", TenantID: "tenant-2"})
	if !errors.Is(err, ErrRequestRegistered) {
		t.Fatalf("duplicate Register() error = %v, want ErrRequestRegistered", err)
	}
	if err := registry.Register(ActiveRequest{RequestID: "", TenantID: "tenant-1"}); !errors.Is(err, ErrMissingRequestID) {
		t.Fatalf("Register(empty id) error = %v, want ErrMissingRequestID", err)
	}

	// Transitions for unregistered ids are loud failures, not silent inserts.
	if err := registry.UpdateState("req-unknown", StateTerminal); !errors.Is(err, ErrRequestUnknown) {
		t.Fatalf("UpdateState(unknown) error = %v, want ErrRequestUnknown", err)
	}
	if err := registry.MarkPersistOutcome("req-unknown", PersistOutcomeFailure); !errors.Is(err, ErrRequestUnknown) {
		t.Fatalf("MarkPersistOutcome(unknown) error = %v, want ErrRequestUnknown", err)
	}
	if list := registry.List(); len(list) != 1 {
		t.Fatalf("List() = %+v, want only the registered entry", list)
	}
}

func TestRegistryDefaultsAndTimestamps(t *testing.T) {
	t.Parallel()
	registry := NewActiveRequestRegistry()

	before := time.Now()
	if err := registry.Register(ActiveRequest{RequestID: "req-1", TenantID: "tenant-1"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	entry, _ := registry.Get("req-1")
	// Defaults land the entry at the start of the lifecycle: a caller cannot
	// register a request as already terminal or already persisted.
	if entry.State != StateInFlight {
		t.Fatalf("default state = %q, want in_flight", entry.State)
	}
	if entry.PersistOutcome != PersistOutcomePending {
		t.Fatalf("default persist outcome = %q, want pending", entry.PersistOutcome)
	}
	if entry.RegisteredAt.Before(before) || entry.UpdatedAt.Before(entry.RegisteredAt) {
		t.Fatalf("timestamps not stamped: registered=%v updated=%v now>=%v", entry.RegisteredAt, entry.UpdatedAt, before)
	}
}

func TestRegistryConcurrentAccess(t *testing.T) {
	t.Parallel()
	registry := NewActiveRequestRegistry()

	const workers = 16
	const iterations = 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				requestID := "req-" + string(rune('a'+offset))
				switch i % 4 {
				case 0:
					_ = registry.Register(ActiveRequest{RequestID: requestID, TenantID: "tenant-concurrent"})
				case 1:
					_ = registry.UpdateState(requestID, StateTerminal)
					_ = registry.MarkPersistOutcome(requestID, PersistOutcomeSuccess)
				case 2:
					_, _ = registry.Get(requestID)
					_ = registry.List()
				case 3:
					registry.Remove(requestID)
				}
			}
		}(w)
	}
	wg.Wait()

	if list := registry.List(); len(list) != 0 {
		t.Fatalf("List() after concurrent churn = %+v, want empty after final removes", list)
	}
}
