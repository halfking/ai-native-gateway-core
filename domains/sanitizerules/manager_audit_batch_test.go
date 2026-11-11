package sanitizerules

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// 2026-10-31 211: production hit rates outran the per-event audit write path
// (advisory lock + upsert + retention DELETE per event), the bounded queue
// stayed full and 44250 audit events were dropped in 3h. The batch writer
// restores the drain rate; these tests pin the batch semantics.

// recordingBatchStore implements BatchEventWriter on top of the embedded Store
// and records how many events each store call received.
type recordingBatchStore struct {
	Store
	mu        sync.Mutex
	batchLens []int
}

func (s *recordingBatchStore) RecordEvents(ctx context.Context, events []Event, limit int) error {
	s.mu.Lock()
	s.batchLens = append(s.batchLens, len(events))
	s.mu.Unlock()
	return s.Store.(BatchEventWriter).RecordEvents(ctx, events, limit)
}

// perEventCountingStore implements Store only — no RecordEvents — so the type
// assertion in writeBatch must fall back to the per-event path. Each
// RecordEvent call is recorded.
type perEventCountingStore struct {
	Store
	mu    sync.Mutex
	calls int
}

func (s *perEventCountingStore) RecordEvent(ctx context.Context, event Event, limit int) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.Store.RecordEvent(ctx, event, limit)
}

func waitForBatch[T any](t *testing.T, probe func() (T, bool), desc string) T {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := probe(); ok {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
	var zero T
	return zero
}

func TestAuditBatchAggregatesDuplicateFingerprints(t *testing.T) {
	store := NewMemoryStore()
	m, err := NewManager(store, testConfig(), Options{InstanceID: "batch-agg"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()

	base := Event{TenantID: "tenant-1", RuleID: "rule-1", Version: 1, Phase: "output", Action: "mask"}
	other := Event{TenantID: "tenant-1", RuleID: "rule-2", Version: 1, Phase: "input", Action: "block"}
	for i := 0; i < 5; i++ {
		if !m.Record(ctx, base) {
			t.Fatal("valid event was not accepted into queue")
		}
	}
	for i := 0; i < 3; i++ {
		if !m.Record(ctx, other) {
			t.Fatal("valid event was not accepted into queue")
		}
	}
	events := waitForBatch(t, func() ([]Event, bool) {
		got, err := store.ListEvents(ctx, EventFilter{})
		if err != nil {
			return nil, false
		}
		total := 0
		for _, e := range got {
			total += int(e.Count)
		}
		return got, len(got) == 2 && total == 8
	}, "2 aggregated rows with total count 8")
	for _, e := range events {
		switch e.RuleID {
		case "rule-1":
			if e.Count != 5 {
				t.Fatalf("rule-1 count = %d, want 5 (5 queued events aggregated into one row)", e.Count)
			}
		case "rule-2":
			if e.Count != 3 {
				t.Fatalf("rule-2 count = %d, want 3", e.Count)
			}
		default:
			t.Fatalf("unexpected rule %q", e.RuleID)
		}
	}
}

func TestAuditBatchDrainsFloodInBatches(t *testing.T) {
	store := NewMemoryStore()
	rec := &recordingBatchStore{Store: store}
	m, err := NewManager(rec, testConfig(), Options{InstanceID: "batch-flood", QueueSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()

	for i := 0; i < 300; i++ {
		if !m.Record(ctx, Event{TenantID: "tenant-1", RuleID: "rule-1", Version: 1, Phase: "output", Action: "mask"}) {
			t.Fatalf("event %d dropped before the queue was even full", i)
		}
	}
	maxBatch := waitForBatch(t, func() (int, bool) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		best := 0
		for _, n := range rec.batchLens {
			if n > best {
				best = n
			}
		}
		return best, best > 1
	}, "a store call that received more than one event")
	if maxBatch > auditWriteBatchSize {
		t.Fatalf("batch size %d exceeds auditWriteBatchSize %d", maxBatch, auditWriteBatchSize)
	}
	if m.DroppedEvents() != 0 {
		t.Fatalf("dropped = %d, want 0 (flood of 300 with queue 512 must fully drain)", m.DroppedEvents())
	}
}

func TestAuditQueueFullDropsAreCountedAndReported(t *testing.T) {
	m, err := NewManager(NewMemoryStore(), testConfig(), Options{
		InstanceID: "batch-queue-full",
		QueueSize:  2,
		OnError:    func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var accepted int
	for i := 0; i < 100; i++ {
		if m.Record(ctx, Event{TenantID: "tenant-1", RuleID: "rule-1", Version: 1, Phase: "output", Action: "mask"}) {
			accepted++
		}
	}
	if accepted > 2 {
		t.Fatalf("accepted = %d with queue size 2 and no consumer running", accepted)
	}
	if m.DroppedEvents() == 0 {
		t.Fatal("full queue did not count dropped events")
	}
	if got := m.Status().DroppedEvents; got != m.DroppedEvents() {
		t.Fatalf("Status().DroppedEvents = %d, want %d (dropped count must surface on the status API)", got, m.DroppedEvents())
	}
}

func TestAuditBatchHonorsDisabledSwitch(t *testing.T) {
	store := NewMemoryStore()
	m, err := NewManager(store, testConfig(), Options{InstanceID: "batch-disabled", AuditEnabled: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()

	for i := 0; i < 10; i++ {
		if !m.Record(ctx, Event{TenantID: "tenant-1", RuleID: "rule-1", Version: 1, Phase: "output", Action: "mask"}) {
			t.Fatal("valid event was not accepted into queue")
		}
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(m.queue) == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	events, err := store.ListEvents(context.Background(), EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("disabled audit switch persisted %d events, want 0 (queued events are discarded at write time)", len(events))
	}
}

func TestAuditBatchFallsBackToPerEventStore(t *testing.T) {
	store := NewMemoryStore()
	rec := &perEventCountingStore{Store: store}
	m, err := NewManager(rec, testConfig(), Options{InstanceID: "batch-fallback"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()

	for i := 0; i < 6; i++ {
		if !m.Record(ctx, Event{TenantID: "tenant-1", RuleID: "rule-1", Version: 1, Phase: "output", Action: "mask"}) {
			t.Fatal("valid event was not accepted into queue")
		}
	}
	waitForBatch(t, func() (bool, bool) {
		events, err := store.ListEvents(ctx, EventFilter{})
		if err != nil {
			return false, false
		}
		for _, e := range events {
			if e.RuleID == "rule-1" && e.Count == 6 {
				return true, true
			}
		}
		return false, false
	}, "6 events persisted through the per-event fallback path")
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.calls != 6 {
		t.Fatalf("fallback path issued %d per-event calls, want 6", rec.calls)
	}
}

func TestPostgresRecordEventsBatchSQLContract(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewPostgresStore(db)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(741968280105)")).WillReturnResult(sqlmock.NewResult(0, 1))
	// 5 identical + 1 distinct → 2 aggregated rows in one multi-row upsert; the
	// INSERT must carry the aggregated count per row (the column defaults to 1,
	// so omitting it loses k-1 hits on the fresh-INSERT branch) and the ON
	// CONFLICT clause must accumulate EXCLUDED.count, never a constant 1.
	insertRe := regexp.MustCompile(`^INSERT INTO sanitize_rule_events\(fingerprint,tenant_id,rule_id,version,phase,action,sample_masked,first_seen,last_seen,sensitive_type,field_kind,count\) VALUES \(\$1,\$2,\$3,\$4,\$5,\$6,\$7,\$8,\$9,\$10,\$11,\$12\),\(\$13,\$14,\$15,\$16,\$17,\$18,\$19,\$20,\$21,\$22,\$23,\$24\) ON CONFLICT\(fingerprint\) DO UPDATE SET count=sanitize_rule_events\.count\+EXCLUDED\.count,last_seen=EXCLUDED\.last_seen$`)
	mock.ExpectExec(insertRe.String()).
		WillReturnResult(sqlmock.NewResult(0, 2)).
		WithArgs(sqlmock.AnyArg(), "tenant-1", "rule-1", int64(1), "output", "mask", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), int64(5),
			sqlmock.AnyArg(), "tenant-1", "rule-2", int64(1), "input", "block", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), int64(1))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM sanitize_rule_events WHERE id IN (SELECT id FROM sanitize_rule_events ORDER BY last_seen DESC,sanitize_rule_events.count DESC,id DESC OFFSET $1)")).
		WithArgs(10000).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	now := time.Now().UTC()
	events := []Event{
		{TenantID: "tenant-1", RuleID: "rule-1", Version: 1, Phase: "output", Action: "mask", FirstSeen: now, LastSeen: now},
		{TenantID: "tenant-1", RuleID: "rule-1", Version: 1, Phase: "output", Action: "mask", FirstSeen: now, LastSeen: now},
		{TenantID: "tenant-1", RuleID: "rule-1", Version: 1, Phase: "output", Action: "mask", FirstSeen: now, LastSeen: now},
		{TenantID: "tenant-1", RuleID: "rule-1", Version: 1, Phase: "output", Action: "mask", FirstSeen: now, LastSeen: now},
		{TenantID: "tenant-1", RuleID: "rule-1", Version: 1, Phase: "output", Action: "mask", FirstSeen: now, LastSeen: now},
		{TenantID: "tenant-1", RuleID: "rule-2", Version: 1, Phase: "input", Action: "block", FirstSeen: now, LastSeen: now},
	}
	if err := s.RecordEvents(context.Background(), events, 10000); err != nil {
		t.Fatalf("RecordEvents: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("batch SQL contract violated: %v", err)
	}
}

func TestPostgresRecordEventsRejectsInvalidEvent(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewPostgresStore(db)
	// storeError wraps into sanitizeStoreError (ErrInvalid identity is not
	// preserved — same convention as the per-event RecordEvent path).
	err = s.RecordEvents(context.Background(), []Event{{TenantID: "tenant-1", RuleID: "bad id!", Version: 1, Phase: "output", Action: "mask"}}, 10000)
	if err == nil || !strings.Contains(err.Error(), "event write failed") {
		t.Fatalf("invalid event error = %v, want store error for \"event write\"", err)
	}
}

func TestPostgresRecordEventsEmptyBatchIsNoop(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewPostgresStore(db)
	if err := s.RecordEvents(context.Background(), nil, 10000); err != nil {
		t.Fatalf("empty batch: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("empty batch must not touch the database: %v", err)
	}
}

func TestMemoryRecordEventsAggregatesAcrossCalls(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	e := Event{TenantID: "tenant-1", RuleID: "rule-1", Version: 1, Phase: "output", Action: "mask"}
	// Batch 1: 3 identical NEW fingerprints in one call → count must land as 3.
	if err := s.RecordEvents(ctx, []Event{e, e, e}, 10000); err != nil {
		t.Fatal(err)
	}
	// Batch 2: 2 more of the same fingerprint → existing row must gain 2.
	if err := s.RecordEvents(ctx, []Event{e, e}, 10000); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (same fingerprint across batches stays one row)", len(rows))
	}
	if rows[0].Count != 5 {
		t.Fatalf("count = %d, want 5 (3 fresh + 2 on conflict); a per-event ++ mutation lands as 4", rows[0].Count)
	}
}

func TestMemoryRecordEventsSingleBatchNewFingerprintLandsAggregatedCount(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	e := Event{TenantID: "tenant-1", RuleID: "rule-1", Version: 1, Phase: "output", Action: "mask"}
	if err := s.RecordEvents(ctx, []Event{e, e, e, e}, 10000); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Count != 4 {
		t.Fatalf("rows=%d count=%d, want 1 row with count=4 (fresh INSERT must carry the aggregated hit total)", len(rows), rows[0].Count)
	}
}

func TestAuditWriteBatchSizeBound(t *testing.T) {
	if auditWriteBatchSize < 2 || auditWriteBatchSize > 1000 {
		t.Fatalf("auditWriteBatchSize = %d outside sane bounds [2,1000]", auditWriteBatchSize)
	}
	if !strings.Contains(fmt.Sprint(auditWriteBatchSize), "128") {
		t.Fatalf("auditWriteBatchSize drift: %d (contract test expects 128 — update deliberately, not silently)", auditWriteBatchSize)
	}
}
