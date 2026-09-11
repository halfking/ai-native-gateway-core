package admin

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// models_alias_sql_live_test.go — live-DB regression for the admin alias
// create / bulk-import SQL (2026-09-12).
//
// Both statements used to arbitrate ON CONFLICT on the expression
// (raw_name, COALESCE(quantization,''), COALESCE(surface,'')), but
// model_aliases has no such unique index — only
// uq_model_aliases_canonical_raw (canonical_id, raw_name) — so every admin
// alias create failed with 42P10. The rewrite arbitrates on the real pair
// constraint and demotes cross-canonical competitors; this test pins the
// statement shapes against real PostgreSQL (42P10/23505-class regressions
// cannot be caught by sqlmock).
//
// Everything runs inside one REPEATABLE READ transaction that is rolled
// back, so the test leaves zero footprint.
//
// Run with:
//
//	TEST_DATABASE_URL=postgres://... go test -count=1 -run TestAdminAliasSQL_Live ./admin
func TestAdminAliasSQL_Live(t *testing.T) {
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

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() {
		//nolint:errcheck // rollback is the point: zero footprint
		tx.Rollback(ctx)
	}()

	// Seed two canonical rows to play the roles of the edited model and a
	// cross-canonical competitor.
	var idA, idB int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO models_canonical (canonical_name, family, display_name, modality, context_window, source, status)
		VALUES ('zz-admin-canonical-a', 'zz-admin', 'ZZ Admin A', 'text', 8192, 'seed', 'active')
		ON CONFLICT (canonical_name) DO UPDATE SET status = 'active'
		RETURNING id
	`).Scan(&idA); err != nil {
		t.Fatalf("seed canonical a: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO models_canonical (canonical_name, family, display_name, modality, context_window, source, status)
		VALUES ('zz-admin-canonical-b', 'zz-admin', 'ZZ Admin B', 'text', 8192, 'seed', 'active')
		ON CONFLICT (canonical_name) DO UPDATE SET status = 'active'
		RETURNING id
	`).Scan(&idB); err != nil {
		t.Fatalf("seed canonical b: %v", err)
	}

	// 1) Fresh create: inserts one active row with the given payload.
	var aliasID int64
	if err := tx.QueryRow(ctx, aliasUpsertSQL, idA, "zz-admin-alias", "q8", "srv", "note-1", nil).Scan(&aliasID); err != nil {
		t.Fatalf("fresh create: %v — 42P10 here means the arbiter regressed to the expression form", err)
	}
	var notes string
	var status string
	if err := tx.QueryRow(ctx, `SELECT notes, status FROM model_aliases WHERE id = $1`, aliasID).Scan(&notes, &status); err != nil {
		t.Fatalf("fetch created: %v", err)
	}
	if notes != "note-1" || status != "active" {
		t.Fatalf("fresh create payload: notes=%q status=%q", notes, status)
	}

	// 2) Same-pair conflict: updates the payload in place, same row id.
	if err := tx.QueryRow(ctx, aliasUpsertSQL, idA, "zz-admin-alias", "", "", "note-2", nil).Scan(&aliasID); err != nil {
		t.Fatalf("same-pair upsert: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT notes, status FROM model_aliases WHERE id = $1`, aliasID).Scan(&notes, &status); err != nil {
		t.Fatalf("fetch updated: %v", err)
	}
	if notes != "note-2" || status != "active" {
		t.Fatalf("same-pair upsert payload: notes=%q status=%q", notes, status)
	}

	// 3) Cross-canonical competitor: an active row for the same raw_name
	// under another canonical gets demoted to 'disabled' by the companion
	// statement; the operator's mapping is the only active one left.
	if _, err := tx.Exec(ctx, `
		INSERT INTO model_aliases (raw_name, canonical_id, status)
		VALUES ('zz-admin-alias', $1, 'active')
	`, idB); err != nil {
		t.Fatalf("seed competitor: %v", err)
	}
	if _, err := tx.Exec(ctx, aliasDemoteCompetitorsSQL, "zz-admin-alias", idA); err != nil {
		t.Fatalf("demote: %v", err)
	}
	var activeCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM model_aliases
		WHERE raw_name = 'zz-admin-alias' AND status = 'active'
	`).Scan(&activeCount); err != nil {
		t.Fatalf("count active: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("active rows for raw_name after demote: %d, want 1", activeCount)
	}
	var compStatus string
	if err := tx.QueryRow(ctx, `
		SELECT status FROM model_aliases
		WHERE raw_name = 'zz-admin-alias' AND canonical_id = $1
	`, idB).Scan(&compStatus); err != nil {
		t.Fatalf("fetch competitor: %v", err)
	}
	if compStatus != "disabled" {
		t.Fatalf("competitor status %q, want disabled", compStatus)
	}
}
