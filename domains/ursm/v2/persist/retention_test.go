package persist

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeSnapshotRetentionDB captures the cleanup SQL / args and lets tests
// script per-batch rows-deleted + returned cursor (batch loop) and injected errors.
type fakeSnapshotRetentionDB struct {
	mu           sync.Mutex
	begins       int
	execSQL      []string
	execArgs     [][]any
	commit       bool
	batchResults []int64   // consumed per batch; last value repeats once exhausted
	batchFloors  []time.Time // next cursor per batch; zero value = NULL
	execErr      error
}

type fakeSnapshotRetentionTx struct {
	db *fakeSnapshotRetentionDB
}

// fakeRow is a minimal pgx.Row: Scan fills (deleted int64, nextFloor *time.Time).
type fakeRow struct {
	deleted   int64
	nextFloor *time.Time
	err       error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for _, d := range dest {
		switch p := d.(type) {
		case *int64:
			*p = r.deleted
		case **time.Time:
			*p = r.nextFloor
		}
	}
	return nil
}

func (tx *fakeSnapshotRetentionTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.db.mu.Lock()
	defer tx.db.mu.Unlock()
	tx.db.execSQL = append(tx.db.execSQL, sql)
	tx.db.execArgs = append(tx.db.execArgs, args)
	if tx.db.execErr != nil {
		return fakeRow{err: tx.db.execErr}
	}
	idx := len(tx.db.execSQL) - 1
	pick := func(n int) int {
		if idx >= n {
			return n - 1
		}
		return idx
	}
	var rows int64
	if len(tx.db.batchResults) > 0 {
		rows = tx.db.batchResults[pick(len(tx.db.batchResults))]
	}
	var floor *time.Time
	if len(tx.db.batchFloors) > 0 {
		f := tx.db.batchFloors[pick(len(tx.db.batchFloors))]
		if !f.IsZero() {
			floor = &f
		}
	}
	return fakeRow{deleted: rows, nextFloor: floor}
}

func (tx *fakeSnapshotRetentionTx) Commit(ctx context.Context) error {
	tx.db.mu.Lock()
	tx.db.commit = true
	tx.db.mu.Unlock()
	return nil
}

func (tx *fakeSnapshotRetentionTx) Rollback(ctx context.Context) error { return nil }

func (db *fakeSnapshotRetentionDB) Begin(ctx context.Context) (SnapshotRetentionTx, error) {
	db.mu.Lock()
	db.begins++
	db.mu.Unlock()
	return &fakeSnapshotRetentionTx{db: db}, nil
}

func (db *fakeSnapshotRetentionDB) statements() []string {
	db.mu.Lock()
	defer db.mu.Unlock()
	return append([]string(nil), db.execSQL...)
}

func (db *fakeSnapshotRetentionDB) began() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.begins
}

func (db *fakeSnapshotRetentionDB) committed() bool {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.commit
}

func TestSnapshotRetentionBatchLoopStopsOnPartialBatch(t *testing.T) {
	db := &fakeSnapshotRetentionDB{batchResults: []int64{5000, 1234}}
	worker := NewSnapshotRetentionWorker(nil, DefaultSnapshotRetentionConfig())
	worker.db = db

	deleted, err := worker.CleanupExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 6234 {
		t.Fatalf("deleted = %d, want 6234 (full batch + partial batch)", deleted)
	}
	if got := db.began(); got != 2 {
		t.Fatalf("began = %d, want 2 (one transaction per batch)", got)
	}
	stmts := db.statements()
	for i, stmt := range stmts {
		if !strings.Contains(stmt, "DELETE FROM ursm_node_snapshot_min") ||
			!strings.Contains(stmt, "snapshot_ts") {
			t.Fatalf("batch %d SQL must delete expired snapshots by snapshot_ts, got:\n%s", i, stmt)
		}
	}
	// Args are (retention interval, batch size, scan floor).
	if len(db.execArgs[0]) != 3 {
		t.Fatalf("exec args = %v, want (interval, batch size, floor)", db.execArgs[0])
	}
	if interval, ok := db.execArgs[0][0].(string); !ok || !strings.HasPrefix(interval, "720h") {
		t.Fatalf("retention arg = %v, want 30d duration string (720h...)", db.execArgs[0][0])
	}
	if size, ok := db.execArgs[0][1].(int); !ok || size != 5000 {
		t.Fatalf("batch size arg = %v, want 5000", db.execArgs[0][1])
	}
	if !db.committed() {
		t.Fatal("cleanup transactions were not committed")
	}
}

func TestSnapshotRetentionHonorsCleanupWindow(t *testing.T) {
	db := &fakeSnapshotRetentionDB{batchResults: []int64{5000}}
	worker := NewSnapshotRetentionWorker(nil, SnapshotRetentionConfig{
		Retention:        30 * 24 * time.Hour,
		BatchSize:        5000,
		MaxCleanupWindow: time.Nanosecond, // deadline already passed after batch 1
	})
	worker.db = db

	if _, err := worker.CleanupExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := db.began(); got != 1 {
		t.Fatalf("began = %d, want 1 (window cap stops the batch loop)", got)
	}
}

func TestSnapshotRetentionBatchErrorStopsLoop(t *testing.T) {
	db := &fakeSnapshotRetentionDB{
		batchResults: []int64{5000},
		execErr:      &pgconn.PgError{Code: "42501", Message: "permission denied for table ursm_node_snapshot_min"},
	}
	worker := NewSnapshotRetentionWorker(nil, DefaultSnapshotRetentionConfig())
	worker.db = db

	deleted, err := worker.CleanupExpired(context.Background())
	if err == nil {
		t.Fatal("CleanupExpired() error = nil, want exec error")
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want 0 (failed batch must not count its rows)", deleted)
	}
	if db.committed() {
		t.Fatal("cleanup committed after batch error")
	}
}

func TestSnapshotRetentionDisabledIsNoOp(t *testing.T) {
	db := &fakeSnapshotRetentionDB{}
	worker := NewSnapshotRetentionWorker(nil, SnapshotRetentionConfig{Retention: 0})
	if !worker.Disabled() {
		t.Fatal("Disabled() = false for zero retention")
	}
	worker.Start() // must not panic or spawn goroutines
	if _, err := worker.CleanupExpired(context.Background()); err != nil {
		t.Fatalf("CleanupExpired() error = %v, want nil no-op", err)
	}
	worker.Stop()
	if got := db.began(); got != 0 {
		t.Fatalf("begins = %d, want 0 (disabled worker must not touch the DB)", got)
	}
}

func TestSnapshotRetentionNilDBIsNoOp(t *testing.T) {
	worker := NewSnapshotRetentionWorker(nil, DefaultSnapshotRetentionConfig())
	worker.Start()
	if _, err := worker.CleanupExpired(context.Background()); err != nil {
		t.Fatalf("CleanupExpired() error = %v, want nil no-op", err)
	}
	worker.Stop()
}

func TestSnapshotRetentionStartStopIsIdempotentAndConcurrentSafe(t *testing.T) {
	db := &fakeSnapshotRetentionDB{batchResults: []int64{0}}
	worker := NewSnapshotRetentionWorker(nil, DefaultSnapshotRetentionConfig())
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

func TestSnapshotRetentionStopWithoutStartReturnsImmediately(t *testing.T) {
	db := &fakeSnapshotRetentionDB{}
	worker := NewSnapshotRetentionWorker(nil, DefaultSnapshotRetentionConfig())
	worker.db = db
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
	if got := db.began(); got != 0 {
		t.Fatalf("begins = %d, want 0 (no cleanup without Start)", got)
	}
}

func TestSnapshotRetentionConfigFromEnv(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		cfg := SnapshotRetentionConfigFromEnv()
		if cfg.Retention != 30*24*time.Hour {
			t.Fatalf("retention = %s, want default 30d", cfg.Retention)
		}
	})
	t.Run("explicit override", func(t *testing.T) {
		t.Setenv("URSM_SNAPSHOT_RETENTION_DAYS", "7")
		if cfg := SnapshotRetentionConfigFromEnv(); cfg.Retention != 7*24*time.Hour {
			t.Fatalf("retention = %s, want 7d", cfg.Retention)
		}
	})
	t.Run("zero disables", func(t *testing.T) {
		t.Setenv("URSM_SNAPSHOT_RETENTION_DAYS", "0")
		if cfg := SnapshotRetentionConfigFromEnv(); cfg.Retention != 0 {
			t.Fatalf("retention = %s, want 0 (disabled)", cfg.Retention)
		}
	})
	t.Run("invalid ignored", func(t *testing.T) {
		t.Setenv("URSM_SNAPSHOT_RETENTION_DAYS", "not-a-number")
		if cfg := SnapshotRetentionConfigFromEnv(); cfg.Retention != 30*24*time.Hour {
			t.Fatalf("retention = %s, want default 30d on invalid input", cfg.Retention)
		}
	})
}

// TestSnapshotRetentionAdvancesScanFloorBetweenBatches pins the 2026-10-02
// production defect: without a per-batch scan floor, every batch restarts its
// candidate scan at the head of the pkey index and must walk past all index
// entries deleted by earlier batches before it can fill BatchSize. On 252
// (45.6M rows / 26.1M expired) the measured rate decayed 8,658 → 15,024 →
// 4,208 rows/s, i.e. O(n^2) across a round.
//
// The floor must therefore be carried forward. It is INCLUSIVE (>=) on
// purpose: one flush writes ~1,008 rows sharing a single snapshot_ts, so a
// strict (>) cursor risks silently skipping live rows that share the
// boundary timestamp. Re-scanning one timestamp is cheap; skipping rows is
// not.
func TestSnapshotRetentionAdvancesScanFloorBetweenBatches(t *testing.T) {
	first := time.Date(2026, 9, 6, 23, 56, 50, 0, time.UTC)
	second := time.Date(2026, 9, 6, 23, 58, 10, 0, time.UTC)
	db := &fakeSnapshotRetentionDB{
		batchResults: []int64{5000, 5000, 17},
		batchFloors:  []time.Time{first, second, second},
	}
	worker := NewSnapshotRetentionWorker(nil, DefaultSnapshotRetentionConfig())
	worker.db = db

	deleted, err := worker.CleanupExpired(context.Background())
	if err != nil {
		t.Fatalf("CleanupExpired() error = %v", err)
	}
	if want := int64(10017); deleted != want {
		t.Fatalf("deleted = %d, want %d", deleted, want)
	}

	args := db.execArgs
	if len(args) != 3 {
		t.Fatalf("batches = %d, want 3", len(args))
	}
	// Batch 1 starts from the zero floor: the "no lower bound" seed.
	if got, ok := args[0][2].(time.Time); !ok || !got.IsZero() {
		t.Fatalf("batch 1 floor = %v, want zero time", args[0][2])
	}
	// Batches 2 and 3 must resume from the previous batch's max snapshot_ts.
	for i, want := range []time.Time{first, second} {
		got, ok := args[i+1][2].(time.Time)
		if !ok || !got.Equal(want) {
			t.Fatalf("batch %d floor = %v, want %v", i+2, args[i+1][2], want)
		}
	}

	// The floor predicate must be inclusive. A strict ">" would let the
	// scanner step past live rows sharing the boundary timestamp.
	for i, stmt := range db.statements() {
		if !strings.Contains(stmt, "snapshot_ts >= $3::timestamptz") {
			t.Fatalf("batch %d must use an INCLUSIVE floor (>=), got:\n%s", i, stmt)
		}
		if strings.Contains(stmt, "snapshot_ts > $3") {
			t.Fatalf("batch %d uses a strict floor (>) which can skip live rows:\n%s", i, stmt)
		}
	}
}
