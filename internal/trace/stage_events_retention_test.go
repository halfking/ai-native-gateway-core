package trace

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// fakeStageEventsRetentionDB captures cleanup SQL / args and scripts
// per-batch RowsAffected for the batch loop.
type fakeStageEventsRetentionDB struct {
	mu           sync.Mutex
	begins       int
	execSQL      []string
	execArgs     [][]any
	commit       bool
	batchResults []int64
	execErr      error
}

type fakeStageEventsRetentionTx struct {
	db *fakeStageEventsRetentionDB
}

func (tx *fakeStageEventsRetentionTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.db.mu.Lock()
	defer tx.db.mu.Unlock()
	tx.db.execSQL = append(tx.db.execSQL, sql)
	tx.db.execArgs = append(tx.db.execArgs, args)
	if tx.db.execErr != nil {
		return pgconn.CommandTag{}, tx.db.execErr
	}
	var rows int64
	if len(tx.db.batchResults) > 0 {
		idx := len(tx.db.execSQL) - 1
		if idx >= len(tx.db.batchResults) {
			idx = len(tx.db.batchResults) - 1
		}
		rows = tx.db.batchResults[idx]
	}
	return pgconn.NewCommandTag("DELETE " + strconv.FormatInt(rows, 10)), nil
}

func (tx *fakeStageEventsRetentionTx) Commit(ctx context.Context) error {
	tx.db.mu.Lock()
	tx.db.commit = true
	tx.db.mu.Unlock()
	return nil
}

func (tx *fakeStageEventsRetentionTx) Rollback(ctx context.Context) error { return nil }

func (db *fakeStageEventsRetentionDB) Begin(ctx context.Context) (StageEventsRetentionTx, error) {
	db.mu.Lock()
	db.begins++
	db.mu.Unlock()
	return &fakeStageEventsRetentionTx{db: db}, nil
}

func (db *fakeStageEventsRetentionDB) began() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.begins
}

func (db *fakeStageEventsRetentionDB) committed() bool {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.commit
}

func TestStageEventsRetentionBatchLoopStopsOnPartialBatch(t *testing.T) {
	db := &fakeStageEventsRetentionDB{batchResults: []int64{5000, 42}}
	worker := NewStageEventsRetentionWorker(nil, DefaultStageEventsRetentionConfig())
	worker.db = db

	deleted, err := worker.CleanupExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 5042 {
		t.Fatalf("deleted = %d, want 5042", deleted)
	}
	if got := db.began(); got != 2 {
		t.Fatalf("began = %d, want 2 (one transaction per batch)", got)
	}
	stmts := db.execSQL
	for i, stmt := range stmts {
		if !strings.Contains(stmt, "DELETE FROM request_stage_events") ||
			!strings.Contains(stmt, "created_at") {
			t.Fatalf("batch %d SQL must delete expired stage events by created_at, got:\n%s", i, stmt)
		}
	}
	if interval, ok := db.execArgs[0][0].(string); !ok || !strings.HasPrefix(interval, "168h") {
		t.Fatalf("retention arg = %v, want 7d duration string (168h...)", db.execArgs[0][0])
	}
	if size, ok := db.execArgs[0][1].(int); !ok || size != 5000 {
		t.Fatalf("batch size arg = %v, want 5000", db.execArgs[0][1])
	}
	if !db.committed() {
		t.Fatal("cleanup transactions were not committed")
	}
}

func TestStageEventsRetentionBatchErrorStopsLoop(t *testing.T) {
	db := &fakeStageEventsRetentionDB{
		execErr: &pgconn.PgError{Code: "42P01", Message: "relation request_stage_events does not exist"},
	}
	worker := NewStageEventsRetentionWorker(nil, DefaultStageEventsRetentionConfig())
	worker.db = db

	if _, err := worker.CleanupExpired(context.Background()); err == nil {
		t.Fatal("CleanupExpired() error = nil, want exec error")
	}
	if db.committed() {
		t.Fatal("cleanup committed after batch error")
	}
}

func TestStageEventsRetentionDisabledAndNilDBAreNoOp(t *testing.T) {
	disabled := NewStageEventsRetentionWorker(nil, StageEventsRetentionConfig{Retention: 0})
	if !disabled.Disabled() {
		t.Fatal("Disabled() = false for zero retention")
	}
	disabled.Start()
	if _, err := disabled.CleanupExpired(context.Background()); err != nil {
		t.Fatalf("CleanupExpired() error = %v, want nil no-op", err)
	}
	disabled.Stop()

	nilDB := NewStageEventsRetentionWorker(nil, DefaultStageEventsRetentionConfig())
	nilDB.Start()
	nilDB.Stop() // must not hang waiting for a goroutine that never started
}

func TestStageEventsRetentionStartStopIsIdempotentAndConcurrentSafe(t *testing.T) {
	db := &fakeStageEventsRetentionDB{batchResults: []int64{0}}
	worker := NewStageEventsRetentionWorker(nil, DefaultStageEventsRetentionConfig())
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
	if got := db.began(); got == 0 {
		t.Fatal("started worker did not perform initial cleanup")
	}
}

func TestStageEventsRetentionConfigFromEnv(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		if cfg := StageEventsRetentionConfigFromEnv(); cfg.Retention != 7*24*time.Hour {
			t.Fatalf("retention = %s, want default 7d", cfg.Retention)
		}
	})
	t.Run("explicit override", func(t *testing.T) {
		t.Setenv("STAGE_EVENTS_RETENTION_DAYS", "14")
		if cfg := StageEventsRetentionConfigFromEnv(); cfg.Retention != 14*24*time.Hour {
			t.Fatalf("retention = %s, want 14d", cfg.Retention)
		}
	})
	t.Run("zero disables", func(t *testing.T) {
		t.Setenv("STAGE_EVENTS_RETENTION_DAYS", "0")
		if cfg := StageEventsRetentionConfigFromEnv(); cfg.Retention != 0 {
			t.Fatalf("retention = %s, want 0 (disabled)", cfg.Retention)
		}
	})
}
