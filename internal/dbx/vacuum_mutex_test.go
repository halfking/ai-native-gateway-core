// Package dbx — vacuum_mutex_test.go
//
// 2026-09-01: tests for the cluster-wide VACUUM FULL mutex.
//
// These tests pin three contracts:
//   1. ErrVacuumFullMutexBusy is returned (with proper wrapping) when
//      the lock acquisition times out — callers can surface a clear
//      "another replica is running" message to operators.
//   2. The lock releases when fn returns successfully — a second
//      call immediately after the first must NOT time out.
//   3. The lock releases when fn errors — a second call after a
//      failed fn must NOT time out (no stale-lock leak).

package dbx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// withTestPool opens a test pool against the same isolated PG the
// audit fixtures use. Skips the test if the pool can't be opened —
// callers run these tests only when an integration DB is available.
func withTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := testDSN(t)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("skipping: cannot connect to test PG: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

// testDSN resolves the integration PG DSN from the same env var the
// audit scripts use.
func testDSN(t *testing.T) string {
	t.Helper()
	// Fall back to TEST_PG_DSN, TEST_DATABASE_URL, or default to a
	// local socket DSN for ad-hoc runs.
	for _, env := range []string{"TEST_AUDIT_ISOLATED_DB_URL", "TEST_PG_DSN", "TEST_DATABASE_URL"} {
		if v := getenv(env); v != "" {
			return v
		}
	}
	return "postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable"
}

func getenv(key string) string {
	// Tiny indirection so we can override in future tests without
	// importing os everywhere.
	return getenvImpl(key)
}

func getenvImpl(key string) string { return os.Getenv(key) }

func TestIsLockNotAvailableRecognizesSQLState(t *testing.T) {
	// Pure-string check; pins the heuristic so a pgx upgrade doesn't
	// silently break our busy-detection.
	if !isLockNotAvailable(errors.New("ERROR:  (SQLSTATE 55P03)")) {
		t.Fatalf("55P03 substring must match")
	}
	if isLockNotAvailable(errors.New("ERROR:  (SQLSTATE 40P01)")) {
		t.Fatalf("40P01 (deadlock_detected) must NOT match — only 55P03 is the lock-busy signal")
	}
	if isLockNotAvailable(errors.New("totally unrelated")) {
		t.Fatalf("unrelated error must NOT match")
	}
	if isLockNotAvailable(nil) {
		t.Fatalf("nil must NOT match")
	}
}

// TestIsLockWaitCanceledRecognizesQueryCanceled pins the classification that
// makes ErrVacuumFullMutexBusy reachable at all.
//
// This is a pure-string test on purpose, and it is the guard that was missing.
// The behavioural guard (TestVacuumFullMutexStatementTimeoutInsideTxn) needs a
// live database, has no build tag, and t.Skipf's when it cannot connect — so it
// reported "ok" in every default `go test ./...` and every CI run while the
// production path it protects was wrong. A predicate that only ever gets
// exercised against a real server is a predicate that can be wrong for months
// without anybody finding out.
//
// The fact it pins: pg_advisory_xact_lock is the BLOCKING variant, so a
// contended lock makes the caller wait; the wait is cut short by SET LOCAL
// statement_timeout; PostgreSQL then reports 57014 (query_canceled), NOT 55P03
// (lock_not_available, which only the pg_try_ variant returns). Classifying
// only 55P03 left ErrVacuumFullMutexBusy unreachable in the real contention
// path, so callers checking errors.Is(err, ErrVacuumFullMutexBusy) to report
// "another replica is running" never saw it.
func TestIsLockWaitCanceledRecognizesQueryCanceled(t *testing.T) {
	if !isLockWaitCanceled(errors.New(
		"ERROR:  canceling statement due to statement timeout (SQLSTATE 57014)")) {
		t.Fatal("57014 must be recognised as a canceled lock wait — that is the " +
			"only signal the blocking pg_advisory_xact_lock produces under contention")
	}
	// It must also see through wrapping, same as isLockNotAvailable.
	if !isLockWaitCanceled(fmt.Errorf("acquire advisory lock: %w",
		errors.New("canceling statement due to statement timeout (SQLSTATE 57014)"))) {
		t.Fatal("57014 must be found through a wrapped error chain")
	}
	// Non-cancellation errors must not be misread as busy.
	for _, msg := range []string{
		"ERROR:  (SQLSTATE 40P01)", // deadlock_detected
		"ERROR:  (SQLSTATE 55P03)", // lock_not_available: the other predicate's job
		"relation \"public.x\" does not exist",
		"totally unrelated",
	} {
		if isLockWaitCanceled(errors.New(msg)) {
			t.Errorf("%q must NOT be classified as a canceled lock wait", msg)
		}
	}
	if isLockWaitCanceled(nil) {
		t.Fatal("nil must NOT match")
	}
}

func TestErrContainsCodeWalksWrappedErrors(t *testing.T) {
	base := errors.New("inner: (SQLSTATE 55P03)")
	wrapped := errors.Join(errors.New("outer context"), base)
	if !errContainsCode(wrapped, "55P03") {
		t.Fatalf("errContainsCode must walk wrapped errors and find SQLSTATE in inner message")
	}
	if errContainsCode(wrapped, "40P01") {
		t.Fatalf("errContainsCode must NOT false-positive on unrelated SQLSTATE")
	}
}

func TestVacuumFullMutexNilGuards(t *testing.T) {
	// Both nil-pool and nil-conn must fail closed rather than
	// nil-deref panic.
	if err := VacuumFullMutex(context.Background(), nil, nil, time.Second,
		func(context.Context, *pgxpool.Conn) error { return nil },
	); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("nil pool/conn must return nil-guard error, got %v", err)
	}
}

// Integration test: requires a real PG. Skip if unavailable.
func TestVacuumFullMutexHappyPath(t *testing.T) {
	pool := withTestPool(t)
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Skipf("acquire conn: %v", err)
	}
	defer conn.Release()

	// Acquire, run a no-op fn, release. A second call right after
	// must succeed too (lock released cleanly).
	for i := 0; i < 2; i++ {
		err := VacuumFullMutex(ctx, pool, conn, 2*time.Second,
			func(ctx context.Context, c *pgxpool.Conn) error {
				// No-op: simulate the work fn that the real VACUUM
				// FULL call would do. We do NOT actually run
				// VACUUM FULL in tests because that would require
				// a non-trivial fixture and could lock test tables.
				_, err := c.Exec(ctx, "SELECT 1")
				return err
			})
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
}

func TestVacuumFullMutexReleasesOnError(t *testing.T) {
	// Verifies the defer-rollback path: if fn errors, the advisory
	// lock must still release so the next call can proceed.
	pool := withTestPool(t)
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Skipf("acquire conn: %v", err)
	}
	defer conn.Release()

	want := errors.New("simulated work failure")
	got := VacuumFullMutex(ctx, pool, conn, 2*time.Second,
		func(context.Context, *pgxpool.Conn) error { return want })
	if !errors.Is(got, want) {
		t.Fatalf("expected wrapped %v, got %v", want, got)
	}

	// Second call must succeed (lock released despite the failure).
	got = VacuumFullMutex(ctx, pool, conn, 2*time.Second,
		func(ctx context.Context, c *pgxpool.Conn) error {
			_, err := c.Exec(ctx, "SELECT 1")
			return err
		})
	if got != nil {
		t.Fatalf("second call must succeed after error release, got %v", got)
	}
}

// TestVacuumFullMutexStatementTimeoutInsideTxn (2026-09-01 audit P2 regression
// guard): SET LOCAL statement_timeout must run INSIDE the lock transaction
// (after Begin, before the advisory-lock Exec). Before the fix it ran in
// autocommit mode where LOCAL is silently ignored, leaving the lock wait
// unbounded. Against a real PG we assert the observable contract: a second
// caller contending with a holder that never commits gets
// ErrVacuumFullMutexBusy within roughly acquireTimeout (not forever, not
// instantly).
func TestVacuumFullMutexStatementTimeoutInsideTxn(t *testing.T) {
	pool := withTestPool(t)
	ctx := context.Background()

	// Holder: acquires the mutex and blocks until released.
	releaseHolder := make(chan struct{})
	holderDone := make(chan error, 1)
	vacuumConn, err := pool.Acquire(ctx)
	if err != nil {
		t.Skipf("acquire vacuum conn: %v", err)
	}
	defer vacuumConn.Release()
	go func() {
		holderDone <- VacuumFullMutex(ctx, pool, vacuumConn, 2*time.Second,
			func(context.Context, *pgxpool.Conn) error {
				<-releaseHolder
				return nil
			})
	}()

	// Give the holder a moment to take the advisory lock, then contend.
	time.Sleep(300 * time.Millisecond)
	acquireTimeout := 1 * time.Second
	start := time.Now()
	contenderVacuum, err := pool.Acquire(ctx)
	if err != nil {
		t.Skipf("acquire contender conn: %v", err)
	}
	defer contenderVacuum.Release()
	err = VacuumFullMutex(ctx, pool, contenderVacuum, acquireTimeout,
		func(context.Context, *pgxpool.Conn) error { return nil })
	elapsed := time.Since(start)

	close(releaseHolder)
	if errHolder := <-holderDone; errHolder != nil {
		t.Fatalf("holder call failed: %v", errHolder)
	}

	if err == nil {
		t.Fatalf("contender unexpectedly acquired the held lock")
	}
	if !errors.Is(err, ErrVacuumFullMutexBusy) {
		t.Fatalf("contender error must be ErrVacuumFullMutexBusy, got %v", err)
	}
	// The timeout must actually bound the wait: not instant (lock round-trip
	// takes some time) and not far past the budget (pre-fix it waited
	// unboundedly until the holder released).
	if elapsed < 200*time.Millisecond {
		t.Fatalf("contender gave up suspiciously fast (%v) — lock may not have been held", elapsed)
	}
	if elapsed > acquireTimeout+2*time.Second {
		t.Fatalf("contender waited %v, exceeding budget %v by >2s — SET LOCAL likely ignored (autocommit regression)", elapsed, acquireTimeout)
	}
}
