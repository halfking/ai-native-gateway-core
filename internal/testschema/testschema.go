// Package testschema gives one integration test its own PostgreSQL schema
// inside an already-populated database.
//
// # Why this exists
//
// Round 44 §7 measured that the integration-tagged tests fall into two
// families that cannot share one database:
//
//   - tests that assume an already-migrated database and read production-shaped
//     data (the majority), and
//   - tests that build their own schema and therefore collide with a populated
//     one — on a full database they fail with SQLSTATE 42P07
//     `relation "X" already exists`.
//
// A gate harness can only build one kind of database, so one shape could only
// ever serve half the suite. Round 44's closure round added GATE_DB_SHAPE to
// the harness and then measured all three shapes against the same packages.
// The result refuted the "two clean families" premise: the families are
// interleaved INSIDE packages, and no package was green on any shape — one of
// them (./db) hung outright on the near-empty shape. See
// docs/audit/2026-10-01-round44-closure-fixtures.md.
//
// Switching the whole database does not separate them. A per-test schema does.
//
// # How it works
//
// CREATE SCHEMA + SET search_path. Every unqualified name the test and the
// code under test use resolves inside the private schema first, so the test's
// own CREATE TABLE statements land there instead of colliding with public.
//
// This only works because the product code under test uses UNQUALIFIED table
// names. A statement that hardcodes `public.foo` ignores search_path entirely
// and will still collide — that is the hard boundary of this approach, and it
// is the first thing to check when migrating a file onto it.
//
// # Usage
//
//	pool, cleanup := testschema.New(t, os.Getenv("TEST_PG_URL"))
//	defer cleanup()
//	pool.Exec(ctx, `CREATE TABLE foo (...)`)   // lands in the private schema
//
// When dsn is empty the test skips with a clear message rather than silently
// exercising a real database.
package testschema

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// New connects to dsn and returns a pool bound to a fresh, private schema.
//
// The schema is created, registered for cleanup, and pushed onto the
// connection's search_path via RuntimeParams so that EVERY pooled connection
// sees it — setting it on one acquired connection would leak into other
// tests' queries the moment the pool hands that connection out again.
//
// Returns a no-op cleanup on skip so `defer cleanup()` is always safe.
func New(t *testing.T, dsn string) (*pgxpool.Pool, func()) {
	t.Helper()
	if dsn == "" {
		t.Skip("no database URL: set TEST_PG_URL (scripts/audit/run-integration-gate.sh injects it)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	name := "t_" + randomSuffix(t)
	if err := execOnAdmin(ctx, dsn, "CREATE SCHEMA "+quoteIdent(name)); err != nil {
		t.Fatalf("testschema: CREATE SCHEMA %s: %v", name, err)
	}
	t.Cleanup(func() {
		// Best-effort, and deliberately not fatal: cleanup runs after the
		// test has already reported. Silently leaving a schema behind would
		// accumulate litter across gate runs, so try to drop it — but a
		// failure here must not mask the test's own verdict.
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		_ = execOnAdmin(dropCtx, dsn, "DROP SCHEMA IF EXISTS "+quoteIdent(name)+" CASCADE")
	})

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("testschema: parse dsn: %v", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	// pg_catalog stays first so system functions resolve identically to a
	// normal session; the test schema comes before public so the test's own
	// objects shadow production ones, which is the entire point.
	cfg.ConnConfig.RuntimeParams["search_path"] = name + ",public,pg_catalog"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("testschema: connect: %v", err)
	}
	cleanup := func() { pool.Close() }
	t.Cleanup(cleanup)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("testschema: ping: %v", err)
	}
	return pool, cleanup
}

// NewWithSchema is New plus one setup statement run inside the private schema,
// which is where a test's own CREATE TABLE belongs.
func NewWithSchema(t *testing.T, dsn, schema string) (*pgxpool.Pool, func()) {
	t.Helper()
	pool, cleanup := New(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if schema != "" {
		if _, err := pool.Exec(ctx, schema); err != nil {
			t.Fatalf("testschema: apply schema: %v", err)
		}
	}
	return pool, cleanup
}

// execOnAdmin runs one statement over a throwaway connection to `dsn`.
// CREATE/DROP SCHEMA are database-wide, so they must not go through the
// test's own pool (whose search_path is already redirected).
func execOnAdmin(ctx context.Context, dsn, stmt string) error {
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer admin.Close()
	_, err = admin.Exec(ctx, stmt)
	return err
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("testschema: entropy: %v", err)
	}
	return hex.EncodeToString(b[:])
}

func quoteIdent(s string) string {
	return fmt.Sprintf("%q", s)
}
