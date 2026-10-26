package bg

import (
	"context"
	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/redis/go-redis/v9"
	"regexp"
	"testing"
	"time"
)

func TestAvailabilityBackfillCoversParkedNodesAndAdvances(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	db, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cache := NewModelAvailabilityCache(client, time.Hour)
	w := newTestBackfill(cache, NewModelAvailabilityReader(client))
	w.db, w.batchSize = db, 1
	ctx := context.Background()
	observed := time.Now().UTC().Add(-time.Minute)
	parked := observed.Add(30 * 24 * time.Hour)
	fields := modelAvailabilityFields(21, "MiniMax-M3", "suspicious", true, "timeout", 0, 1, nil, "backfill")
	fields["updated_at"] = observed.Add(-time.Minute).Format(time.RFC3339Nano)
	if err := cache.Set(ctx, 21, "MiniMax-M3", fields); err != nil {
		t.Fatal(err)
	}
	query := regexp.QuoteMeta(`FROM v_node_probe_state_compat
 WHERE (credential_id, raw_model_name) > ($1, $2)
 ORDER BY credential_id, raw_model_name
 LIMIT $3`) + `\s*$`
	columns := []string{"credential_id", "raw_model_name", "state", "successes", "failures", "total", "updated_at", "next_retry_at", "last_status", "paused"}
	// Timestamp columns are handed to the mock as *time.Time: the production
	// scan targets **time.Time (a nullable timestamp), and pgxmock's
	// assignable-value branch is the only one that can populate that shape.
	// Real pgx returns a plain time.Time and the same assignment happens.
	db.ExpectQuery(query).WithArgs(0, "", 1).WillReturnRows(pgxmock.NewRows(columns).AddRow(21, "MiniMax-M3", "healthy_confirmed", 2, 0, 2, &observed, &parked, nil, false))
	db.ExpectQuery(query).WithArgs(21, "MiniMax-M3", 1).WillReturnRows(pgxmock.NewRows(columns).AddRow(42, "MiniMax-M3", "healthy_confirmed", 1, 0, 1, &observed, &parked, nil, true))
	db.ExpectQuery(query).WithArgs(42, "MiniMax-M3", 1).WillReturnRows(pgxmock.NewRows(columns))
	db.ExpectQuery(query).WithArgs(0, "", 1).WillReturnRows(pgxmock.NewRows(columns))
	for i := 0; i < 4; i++ {
		if _, err := w.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	a, err := w.reader.Read(ctx, 21, "MiniMax-M3")
	if err != nil || a == nil || a.State != "healthy_confirmed" || a.ConsecutiveFailures != 0 || !a.Available || !a.UpdatedAt.Equal(observed) {
		t.Fatalf("parked recovery not projected with observation time: %+v err=%v", a, err)
	}
	b, err := w.reader.Read(ctx, 42, "MiniMax-M3")
	if err != nil || b == nil || b.Available {
		t.Fatalf("paused node projected available: %+v err=%v", b, err)
	}
}

// TestAvailabilityBackfillNeverBindsZeroLimit pins the batch-size
// normalisation the package ratchet demands: a worker whose batch size was
// never set must not bind LIMIT 0. LIMIT 0 scans zero rows, so the worker
// would be a silent no-op that still logs "cycle done" — the backfill would
// look healthy while projecting nothing.
func TestAvailabilityBackfillNeverBindsZeroLimit(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	db, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cache := NewModelAvailabilityCache(client, time.Hour)
	w := newTestBackfill(cache, NewModelAvailabilityReader(client))
	w.db, w.batchSize = db, 0
	query := regexp.QuoteMeta(`FROM v_node_probe_state_compat
    WHERE (credential_id, raw_model_name) > ($1, $2)
    ORDER BY credential_id, raw_model_name
    LIMIT $3`) + `\s*$`
	columns := []string{"credential_id", "raw_model_name", "state", "successes", "failures", "total", "updated_at", "next_retry_at", "last_status", "paused"}
	db.ExpectQuery(query).WithArgs(0, "", defaultAvailabilityBackfillBatchSize).
		WillReturnRows(pgxmock.NewRows(columns))
	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
