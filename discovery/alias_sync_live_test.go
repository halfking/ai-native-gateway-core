package discovery

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// alias_sync_live_test.go — live-DB regression for the rebuildAliasIndex
// arbiter fix (2026-09-12).
//
// Before the fix the statement used ON CONFLICT (raw_name), but
// model_aliases has no unique index on raw_name — only
// uq_model_aliases_canonical_raw on (canonical_id, raw_name) from
// migration 357 — so the statement failed with 42P10 on every run and
// the alias index was never rebuilt.
//
// The whole check runs inside one transaction that is rolled back at the
// end (the service's db field accepts any Exec/Query/QueryRow handle, a
// pgx.Tx included), so it leaves zero footprint on the database.
//
// Run with:
//
//	TEST_DATABASE_URL=postgres://... go test -count=1 -run TestRebuildAliasIndex_LiveArbiter ./discovery
// txDB adapts a pgx.Tx to the service's narrower db interface (whose Exec
// drops the CommandTag).
type txDB struct{ tx pgx.Tx }

func (t txDB) Exec(ctx context.Context, q string, args ...any) error {
	_, err := t.tx.Exec(ctx, q, args...)
	return err
}

// errCloseRows adapts pgx.Rows (Close has no error) to the service's row
// interface (Close returns error).
type errCloseRows struct{ pgx.Rows }

func (r errCloseRows) Close() error {
	r.Rows.Close()
	return nil
}

func (t txDB) Query(ctx context.Context, q string, args ...any) (interface {
	Next() bool
	Scan(...any) error
	Close() error
}, error) {
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return errCloseRows{Rows: rows}, nil
}

func (t txDB) QueryRow(ctx context.Context, q string, args ...any) interface{ Scan(...any) error } {
	return t.tx.QueryRow(ctx, q, args...)
}

func TestRebuildAliasIndex_LiveArbiter(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live-DB regression")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// RepeatableRead pins one snapshot for the rebuild AND the verification:
	// on a live deployment the gateway keeps mutating model_offers /
	// model_aliases between the two statements (probes flip availability,
	// discovery adds offers), which would make the invariant count racy.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() {
		//nolint:errcheck // rollback is the point: zero footprint
		tx.Rollback(ctx)
	}()

	s := &AliasSyncService{db: txDB{tx: tx}}
	if err := s.rebuildAliasIndex(ctx, &AliasSyncResult{}); err != nil {
		t.Fatalf("rebuildAliasIndex: %v — 42P10 here means the arbiter regressed to (raw_name)", err)
	}

	// INSERT IGNORE contract: after the rebuild no available offer's
	// canonical_raw_name may be without any active alias row. (Offers whose
	// raw_name already maps to a *different* canonical are deliberately left
	// alone — that is the DO-NOTHING half of the semantics. Offers with a
	// NULL canonical_id are out of scope: model_aliases.canonical_id is NOT
	// NULL, they cannot be mirrored at all.)
	var absent int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(DISTINCT mo.canonical_raw_name)
		FROM model_offers mo
		WHERE mo.available = TRUE
		  AND mo.canonical_raw_name IS NOT NULL
		  AND mo.canonical_id IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1 FROM model_aliases ma
			WHERE ma.raw_name = mo.canonical_raw_name
			  AND ma.status = 'active'
		  )
	`).Scan(&absent); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if absent != 0 {
		t.Fatalf("%d available offer raw_name(s) still without an active alias after rebuild", absent)
	}
}
