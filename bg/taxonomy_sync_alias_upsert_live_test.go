package bg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// taxonomy_sync_alias_upsert_live_test.go — live-DB regression for the
// upsertAlias arbiter fix (2026-09-12).
//
// Before the fix the statement used ON CONFLICT (raw_name), but
// model_aliases has no unique index on raw_name — only
// uq_model_aliases_canonical_raw on (canonical_id, raw_name) from
// migration 357 — so every alias upsert failed with 42P10 ("there is no
// unique or exclusion constraint matching the ON CONFLICT specification")
// and sync() swallowed the error, logging "aliases 0" for a populated
// YAML. Only real PostgreSQL catches this; a fake pool happily accepts
// any SQL string.
//
// Run with:
//
//	TEST_DATABASE_URL=postgres://... go test -count=1 -run TestTaxonomyUpsertAlias_Live ./bg
//
// The test writes only zz-prefixed rows and deletes them again; a
// pre-test pg_dump of model_aliases/models_canonical is still the
// operator's safety net on shared databases.
func TestTaxonomyUpsertAlias_Live(t *testing.T) {
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
	// t.Cleanup runs LIFO, so registering the pool close first keeps the
	// pool open while the row cleanup below it runs (a deferred Close would
	// run before t.Cleanup and silently kill the DELETEs).
	t.Cleanup(pool.Close)

	cleanup := func() {
		// Own context per call: the shared test ctx is cancel()ed by defer
		// before t.Cleanup runs, which would silently kill these DELETEs.
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		//nolint:errcheck // best-effort test cleanup
		pool.Exec(cctx, `DELETE FROM model_aliases WHERE raw_name LIKE 'zz-alias-%'`)
		//nolint:errcheck // best-effort test cleanup
		pool.Exec(cctx, `DELETE FROM models_canonical WHERE canonical_name IN ('zz-test-canonical-a', 'zz-test-canonical-b')`)
	}
	cleanup()
	t.Cleanup(cleanup)

	// Seed the two canonical rows and a deliberate ambiguity: two active
	// rows for zz-alias-dup pointing at different canonicals.
	if _, err := pool.Exec(ctx, `
		INSERT INTO models_canonical (canonical_name, family, display_name, modality, context_window, source, status)
		VALUES
			('zz-test-canonical-a', 'zz-test', 'ZZ Test A', 'text', 8192, 'taxonomy-yaml', 'active'),
			('zz-test-canonical-b', 'zz-test', 'ZZ Test B', 'text', 8192, 'taxonomy-yaml', 'active')
		ON CONFLICT (canonical_name) DO UPDATE SET status = 'active'
	`); err != nil {
		t.Fatalf("seed canonicals: %v", err)
	}
	var idA, idB int
	if err := pool.QueryRow(ctx, `SELECT id FROM models_canonical WHERE canonical_name = 'zz-test-canonical-a'`).Scan(&idA); err != nil {
		t.Fatalf("id a: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM models_canonical WHERE canonical_name = 'zz-test-canonical-b'`).Scan(&idB); err != nil {
		t.Fatalf("id b: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO model_aliases (raw_name, canonical_id, status)
		VALUES ('zz-alias-dup', $1, 'active'), ('zz-alias-dup', $2, 'active')
	`, idA, idB); err != nil {
		t.Fatalf("seed ambiguous alias: %v", err)
	}

	yamlPath := filepath.Join(t.TempDir(), "model_taxonomy.yaml")
	const doc = `families:
  - id: zz-test
    display_name: ZZ Test
    vendor: zz
    versions:
      - canonical_name: zz-test-canonical-a
        display_name: ZZ Test A
        aliases:
          - zz-alias-shared
          - zz-alias-dup
          - zz-alias-new
      - canonical_name: zz-test-canonical-b
        display_name: ZZ Test B
        aliases:
          - zz-alias-shared
`
	if err := os.WriteFile(yamlPath, []byte(doc), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	// Before the fix, every upsertAlias here failed with 42P10 and the
	// pre-seeded divergence survived untouched.
	NewTaxonomySync(pool, yamlPath).RunOnce(ctx)

	assertAliases := func(label string, wantCanonicalID int, wantRows int) {
		t.Helper()
		rows, err := pool.Query(ctx, `
			SELECT canonical_id FROM model_aliases
			WHERE raw_name = $1 AND status = 'active'
		`, label)
		if err != nil {
			t.Fatalf("query %s: %v", label, err)
		}
		defer rows.Close()
		var got []int
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan %s: %v", label, err)
			}
			got = append(got, id)
		}
		if len(got) != wantRows {
			t.Fatalf("%s: got %d active rows (%v), want %d", label, len(got), got, wantRows)
		}
		for _, id := range got {
			if id != wantCanonicalID {
				t.Fatalf("%s: row points at canonical %d, want %d (repoint/converge failed)", label, id, wantCanonicalID)
			}
		}
	}

	// Processed under canonical-a first, then demoted/reactivated by
	// canonical-b — last taxonomy writer wins, exactly one active row.
	assertAliases("zz-alias-shared", idB, 1)
	// Pre-seeded divergence (a+b) converges: the taxonomy mapping stays
	// active and the competing mapping is demoted to 'disabled' (repointing
	// both rows onto one canonical would violate uq_model_aliases_canonical_raw).
	assertAliases("zz-alias-dup", idA, 1)
	var disabledID int
	if err := pool.QueryRow(ctx, `
		SELECT canonical_id FROM model_aliases
		WHERE raw_name = 'zz-alias-dup' AND status = 'disabled'
	`).Scan(&disabledID); err != nil {
		t.Fatalf("competing row not demoted to 'disabled': %v", err)
	}
	if disabledID != idB {
		t.Fatalf("demoted row points at canonical %d, want %d", disabledID, idB)
	}
	// Fresh alias inserts exactly one row (arbiter is (canonical_id, raw_name)).
	assertAliases("zz-alias-new", idA, 1)
}
