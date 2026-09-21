package requestjourney

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestLifecycleRetryScheduledEmitsAfterBoundTerminal(t *testing.T) {
	memory := NewProjection(DefaultConfig())
	recorder := NewRecorder(memory, nil, nil)
	t.Cleanup(func() { closeRecorder(t, recorder) })
	lifecycle := NewIngressLifecycle(recorder, "gateway-1", "request-retry", IngressProtocolChat, IngressPathChatCompletions, time.Unix(1700000000, 0).UTC())

	// Unbound lifecycle: the retry boundary has no tenant journey to enter.
	lifecycle.RetryScheduled(context.Background(), "upstream_error")
	if _, err := memory.Detail("default", "request-retry"); !errors.Is(err, ErrJourneyNotFound) {
		t.Fatalf("unbound retry leaked into default tenant: %v", err)
	}

	// Bound + attempt-1 terminal, then the wrapper's retry boundary: the
	// event follows the per-attempt terminal (no terminal guard by design).
	lifecycle.BindTenant(context.Background(), "tenant-a", "auto")
	lifecycle.Finish(context.Background(), OutcomeFailure, "upstream_error", 503)
	lifecycle.RetryScheduled(context.Background(), "upstream_error")
	// Empty reason is dropped rather than emitted as a bare retry marker.
	lifecycle.RetryScheduled(context.Background(), "  ")

	journey, err := memory.Detail("tenant-a", "request-retry")
	if err != nil {
		t.Fatal(err)
	}
	var last JourneyEvent
	for _, event := range journey.Events {
		if event.Type == EventRetryScheduled {
			last = event
		}
	}
	if last.Type != EventRetryScheduled {
		t.Fatalf("journey events = %#v, want a retry_scheduled event", journey.Events)
	}
	if last.Stage != StageRetrying || last.RetryReason != "upstream_error" {
		t.Fatalf("retry event = (stage %q, reason %q), want (retrying, upstream_error)", last.Stage, last.RetryReason)
	}
	if last.Seq <= journey.Events[len(journey.Events)-2].Seq {
		t.Fatalf("retry event seq %d must exceed the terminal it follows", last.Seq)
	}
}

func TestLifecycleSequenceSeedingContinuesAcrossAttempts(t *testing.T) {
	memory := NewProjection(DefaultConfig())
	recorder := NewRecorder(memory, nil, nil)
	t.Cleanup(func() { closeRecorder(t, recorder) })

	first := NewIngressLifecycle(recorder, "gateway-1", "request-seq", IngressProtocolChat, IngressPathChatCompletions, time.Unix(1700000000, 0).UTC())
	first.BindTenant(context.Background(), "tenant-a", "auto")
	first.RouteResolved(context.Background(), "tenant-a", "auto", "provider-a/standard")
	if got := first.SequenceHighWater(); got != 2 {
		t.Fatalf("first attempt high-water = %d, want 2", got)
	}

	// Second attempt builds a fresh lifecycle and seeds it from the previous
	// high-water mark, exactly like ensureRequestJourney does on retry.
	second := NewIngressLifecycle(recorder, "gateway-1", "request-seq", IngressProtocolChat, IngressPathChatCompletions, time.Unix(1700000060, 0).UTC())
	second.SeedSequence(first.SequenceHighWater())
	second.BindTenant(context.Background(), "tenant-a", "auto")

	journey, err := memory.Detail("tenant-a", "request-seq")
	if err != nil {
		t.Fatal(err)
	}
	if len(journey.Events) != 3 {
		t.Fatalf("journey events = %d, want 3 (2 from attempt 1 + 1 seeded rebind)", len(journey.Events))
	}
	if err := journey.Validate(); err != nil {
		t.Fatalf("seeded journey is not a valid ordered stream: %v", err)
	}

	// Seeding never lowers the sequence (fresh lifecycle base is 0 anyway,
	// but the guard must hold for any call order).
	second.SeedSequence(1)
	second.RetryScheduled(context.Background(), "upstream_error")
	if got := second.SequenceHighWater(); got != 4 {
		t.Fatalf("high-water after retry = %d, want 4", got)
	}
}

// fakeRetentionDB captures the cleanup SQL and its transaction scoping.
type fakeRetentionDB struct {
	mu             sync.Mutex
	begins         int
	execSQL        []string
	commit         bool
	receiptErr     error
	transitionsErr error
}

type fakeRetentionTx struct {
	db  *fakeRetentionDB
	err error
}

func (tx *fakeRetentionTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if tx.err != nil {
		return pgconn.CommandTag{}, tx.err
	}
	tx.db.mu.Lock()
	tx.db.execSQL = append(tx.db.execSQL, sql)
	var injected error
	switch {
	case strings.Contains(sql, "request_state_transitions"):
		injected = tx.db.transitionsErr
	case strings.Contains(sql, "journal_snapshot_receipts"):
		injected = tx.db.receiptErr
	}
	tx.db.mu.Unlock()
	if injected != nil {
		return pgconn.CommandTag{}, injected
	}
	return pgconn.NewCommandTag("DELETE 3"), nil
}

func (tx *fakeRetentionTx) Commit(ctx context.Context) error {
	tx.db.mu.Lock()
	tx.db.commit = true
	tx.db.mu.Unlock()
	return nil
}

func (tx *fakeRetentionTx) Rollback(ctx context.Context) error { return nil }

func (db *fakeRetentionDB) Begin(ctx context.Context) (RetentionTx, error) {
	db.mu.Lock()
	db.begins++
	db.mu.Unlock()
	return &fakeRetentionTx{db: db}, nil
}

func (db *fakeRetentionDB) statements() []string {
	db.mu.Lock()
	defer db.mu.Unlock()
	return append([]string(nil), db.execSQL...)
}

func TestRetentionCleanupScopesBypassAndDeletesAllRowTypes(t *testing.T) {
	db := &fakeRetentionDB{}
	worker := NewRetentionWorker(nil)
	worker.db = db

	deleted, err := worker.CleanupExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 6 {
		t.Fatalf("deleted = %d, want 6 (fake tag: 3 transitions + 3 receipts)", deleted)
	}
	stmts := db.statements()
	if len(stmts) != 3 {
		t.Fatalf("executed %d statements, want bypass + 2 deletes: %v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[0], "app.bypass_rls") || !strings.Contains(stmts[0], "', true)") {
		t.Fatalf("bypass GUC must be transaction-scoped, got:\n%s", stmts[0])
	}
	if !strings.Contains(stmts[1], "DELETE FROM request_state_transitions") || !strings.Contains(stmts[1], "created_at") {
		t.Fatalf("cleanup SQL must delete expired transitions by created_at, got:\n%s", stmts[1])
	}
	if !strings.Contains(stmts[2], "DELETE FROM journal_snapshot_receipts") || !strings.Contains(stmts[2], "updated_at") {
		t.Fatalf("cleanup SQL must delete expired receipts by updated_at, got:\n%s", stmts[2])
	}
	if !db.commit {
		t.Fatal("cleanup transaction was not committed")
	}
}

func TestRetentionWorkerNilDBIsNoOp(t *testing.T) {
	worker := NewRetentionWorker(nil)
	worker.Start() // must not panic or spawn goroutines
	if _, err := worker.CleanupExpired(context.Background()); err != nil {
		t.Fatalf("CleanupExpired() error = %v, want nil no-op", err)
	}
	worker.Stop()
}

func TestRetentionCleanupReceiptErrorsAreReturned(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "undefined table", err: &pgconn.PgError{Code: "42P01", Message: "relation journal_snapshot_receipts does not exist"}},
		{name: "permission denied", err: &pgconn.PgError{Code: "42501", Message: "permission denied for table journal_snapshot_receipts"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakeRetentionDB{receiptErr: tt.err}
			worker := NewRetentionWorker(nil)
			worker.db = db
			if _, err := worker.CleanupExpired(context.Background()); err == nil {
				t.Fatal("CleanupExpired() error = nil, want receipt error")
			}
			if db.commit {
				t.Fatal("cleanup committed after receipt delete error")
			}
		})
	}
}

func TestRetentionWorkerStartedStopIsIdempotentAndConcurrentSafe(t *testing.T) {
	db := &fakeRetentionDB{}
	worker := NewRetentionWorker(nil)
	worker.db = db
	worker.Start()
	worker.Start()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker.Stop()
		}()
	}
	wg.Wait()
	worker.Stop()
	if db.begins == 0 {
		t.Fatal("started worker did not perform initial cleanup")
	}
}

func TestRetentionWorkerStopWithoutStartReturnsImmediately(t *testing.T) {
	db := &fakeRetentionDB{}
	worker := NewRetentionWorker(nil)
	worker.db = db
	// A worker that was never started must not hang waiting for a goroutine
	// that does not exist (e.g. deferred Stop after failed wiring).
	returned := make(chan struct{})
	go func() {
		worker.Stop()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("Stop() hung on a never-started worker")
	}
	if db.begins != 0 {
		t.Fatalf("begins = %d, want 0 (no cleanup without Start)", db.begins)
	}
}
