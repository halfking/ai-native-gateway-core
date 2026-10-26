package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestClosePoolWithTimeout_ReturnsWhenNoConnectionIsLeaked is the offline
// leg of the pool-close bound.
//
// A pool with no outstanding resources closes immediately, so the helper
// must report success rather than wait for the timeout.
func TestClosePoolWithTimeout_ReturnsWhenNoConnectionIsLeaked(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://test:test@127.0.0.1:1/test?sslmode=disable")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	cfg.MinConns = 0
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig: %v", err)
	}
	start := time.Now()
	ok := closePoolWithTimeout(pool, 5*time.Second)
	if !ok {
		t.Fatal("closePoolWithTimeout reported a timeout for a pool with no outstanding connections")
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("closePoolWithTimeout took %s for an idle pool — it should return as soon as the pool is closed", el)
	}
}

// TestClosePoolWithTimeout_NilPool pins the nil case: Close() must not
// panic on a DB that was never opened.
func TestClosePoolWithTimeout_NilPool(t *testing.T) {
	if !closePoolWithTimeout(nil, time.Second) {
		t.Fatal("closePoolWithTimeout(nil) must report success")
	}
}

// TestCloseUsesTheBoundedPoolClose is the shape gate.
//
// The bound only protects the process if (*DB).Close() actually routes
// through it. A future edit that calls pool.Close() directly would
// reintroduce the unbounded wait — and that is exactly the path the
// 2026-10-21 incident took (97-minute hang in pgxpool.Close()).
func TestCloseUsesTheBoundedPoolClose(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func (d *DB) Close() {")
	if start < 0 {
		t.Fatal("(*DB).Close() not found in db.go")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not delimit (*DB).Close()")
	}
	closeBody := body[start : start+end]
	if !strings.Contains(closeBody, "closePoolWithTimeout(pool, poolCloseTimeout)") {
		t.Fatal("(*DB).Close() must close the pool through closePoolWithTimeout — a direct pool.Close() " +
			"waits forever for a leaked connection and hangs the process (2026-10-21 incident)")
	}
	if strings.Contains(closeBody, "pool.Close()") {
		t.Fatal("(*DB).Close() still calls pool.Close() directly; the unbounded wait is back")
	}
}

// TestClosePoolWithTimeout_HonoursTheBoundWithALeakedConnection is the
// real-database leg.
//
// A connection that is acquired and never returned is what makes
// pgxpool.Close() wait forever. The bound must make the wait finite.
//
// SKIPPED without TEST_DATABASE_URL / TEST_DB_URL — this leg talks to a
// real PostgreSQL, and destructive legs must never point at a live
// database.
func TestClosePoolWithTimeout_HonoursTheBoundWithALeakedConnection(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL not set — SKIPPING the real-database leg")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("pool.Acquire: %v", err)
	}
	// The connection is deliberately never released: this is the leak that
	// blocks pgxpool.Close() in production.
	defer conn.Release()

	const bound = 300 * time.Millisecond
	start := time.Now()
	ok := closePoolWithTimeout(pool, bound)
	elapsed := time.Since(start)

	if ok {
		t.Fatal("closePoolWithTimeout reported success while a connection was still checked out")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("closePoolWithTimeout took %s with a %s bound — the bound is not being honoured", elapsed, bound)
	}
}
