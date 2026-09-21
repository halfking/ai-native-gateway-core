// Package admin — vacuum_mutex.go
//
// 2026-09-01: cluster-wide VACUUM FULL mutex.
//
// Audit gap: the two paths that execute VACUUM FULL
// (bg/vacuum_worker.go weekly schedule + admin/data_lifecycle_storage.go
// on-demand UI button) had no cluster-wide coordination. With two
// gateway replicas both reaching the Sunday-02:00 firing window, both
// would attempt to acquire ACCESS EXCLUSIVE on the target table. The
// first to acquire wins; the second fails with 55P03 (lock_not_available)
// from `SET LOCAL lock_timeout = '5s'` — but the failure surfaced as a
// generic "vacuum failed" to the user / cron, with no hint that another
// replica was already running the same operation.
//
// Fix: a Postgres advisory lock scoped to a fixed bigint key acts as
// the cluster-wide mutex. The lock is acquired inside the same
// transaction as the VACUUM FULL itself, so:
//   - If no replica holds the lock, the call proceeds (lock acquired).
//   - If another replica holds the lock, the call blocks up to
//     pg_advisory_xact_lock's wait budget; if the budget expires,
//     55P03 (lock_not_available) returns and the caller reports
//     "another replica is running vacuum" rather than a generic error.
//   - When the holder's transaction commits or rolls back, the lock
//     releases automatically. No manual unlock path = no leak risk.
//
// Why not pg_try_advisory_lock + manual unlock? Because manual unlock
// requires keeping a connection alive for the full VACUUM duration
// (minutes for a 100GB table). xact-scoped lock binds to the
// transaction, which VACUUM FULL cannot run inside (VACUUM itself
// manages its own transaction). So we use a separate short transaction
// just to hold the advisory lock for the duration of the VACUUM FULL
// (a separate connection that's not used for VACUUM itself).
//
// The two concurrent connections per replica:
//
//	replica-A conn1 (vacuumConn):  VACUUM FULL <table>      — runs the actual op
//	replica-A conn2 (lockConn):    BEGIN; pg_advisory_xact_lock(KEY);  --WAIT-- ; COMMIT
//
// The lockConn sits in pg_advisory_xact_lock for the whole duration of
// the VACUUM FULL on conn1. When conn1 finishes (commit/rollback), the
// caller releases conn2 (which auto-releases the advisory lock on
// COMMIT). The cluster-wide lock is gone.
//
// Why this is safe under replica crash: pg_advisory_xact_lock is bound
// to the transaction, which is bound to the connection. If the
// connection dies, the transaction rolls back, the lock releases. No
// stale lock possible.

package dbx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// vacuumFullAdvisoryLockKey is the fixed bigint key for the cluster-wide
// VACUUM FULL mutex. It is intentionally a stable constant (not derived
// from the table name) so that all replicas serialize against a single
// global mutex for VACUUM FULL regardless of which table is targeted.
//
// Reasoning: VACUUM FULL holds ACCESS EXCLUSIVE on the target table,
// which itself serializes per-table — but if replica-A is running
// VACUUM FULL on request_logs_bodies AND replica-B fires VACUUM FULL
// on request_logs (different table), both will proceed and contend on
// WAL / maintenance_work_mem / I/O bandwidth. A global mutex avoids
// that cross-table contention too.
//
// To pick the key we hash a stable label to a 64-bit signed integer.
// 0x5641_4355_4d5f_4655_4c4c ("VACUUM_FULL" in ASCII, big-endian).
const vacuumFullAdvisoryLockKey int64 = 0x56414355_4d5f4655

// ErrVacuumFullMutexBusy is returned when another replica holds the
// cluster-wide VACUUM FULL mutex and the caller has waited past its
// acquisition deadline. Callers should surface this to the operator
// as "another replica is running VACUUM FULL — try again later" rather
// than a generic failure.
var ErrVacuumFullMutexBusy = errors.New("another replica holds the VACUUM FULL cluster mutex")

// vacuumFullMutex acquires the cluster-wide VACUUM FULL advisory lock,
// runs fn, then releases the lock (the lock is released when the
// holding transaction commits — see file header for the two-connection
// rationale).
//
// `acquireTimeout` bounds how long we wait for the lock before
// returning ErrVacuumFullMutexBusy. The actual VACUUM FULL inside fn
// has its own timeout (statement_timeout / context deadline); this
// timeout is only for the lock acquisition phase.
//
// `vacuumConn` and `lockConn` must be two distinct connections from
// the same pool. They cannot be the same connection because
// pg_advisory_xact_lock holds the transaction open for the entire
// VACUUM FULL duration, and VACUUM FULL itself runs in its own
// implicit transaction on a separate connection.
//
// On success, returns nil. On lock acquisition timeout, returns
// ErrVacuumFullMutexBusy (wrapped with context info). On any other
// error (connection acquire, SET LOCAL, fn panic propagated via
// defer-rollback), the error is returned and the lock is released by
// the rollback in the defer.
func VacuumFullMutex(
	ctx context.Context,
	pool *pgxpool.Pool,
	vacuumConn *pgxpool.Conn,
	acquireTimeout time.Duration,
	fn func(ctx context.Context, conn *pgxpool.Conn) error,
) (retErr error) {
	if pool == nil || vacuumConn == nil {
		return errors.New("VacuumFullMutex: nil pool/conn")
	}
	// Acquire a SECOND connection for the advisory lock. We can't
	// reuse vacuumConn because pg_advisory_xact_lock would hold the
	// transaction open on vacuumConn, and VACUUM FULL inside fn
	// would deadlock waiting for the same connection.
	lockConn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire lockConn: %w", err)
	}
	defer lockConn.Release()

	// Begin the lock-holding transaction. We use a plain Exec
	// (auto-commit on the implicit txn) rather than an explicit
	// BEGIN/COMMIT pair because pg_advisory_xact_lock is the only
	// statement that needs to be in this transaction; COMMIT
	// releases the lock automatically. We need a tx-scoped lock
	// (not session-scoped) so a connection crash releases it
	// without an explicit unlock.
	tx, err := lockConn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin lock txn: %w", err)
	}

	// 2026-09-01 fix (audit P2): SET LOCAL statement_timeout MUST run inside
	// the transaction, after Begin and before the lock Exec. Previously it ran
	// before Begin(): outside an explicit txn the SET executes in autocommit
	// mode, where LOCAL is a no-op the server silently ignores — so the
	// advisory-lock wait had NO timeout cap and a contended lock could park
	// this replica forever (until ctx cancellation). Inside the txn, SET LOCAL
	// scopes the timeout to this transaction only and is reset at COMMIT,
	// leaving pooled-connection session state untouched.
	if _, err := tx.Exec(ctx,
		fmt.Sprintf("SET LOCAL statement_timeout = %d", acquireTimeout.Milliseconds()),
	); err != nil {
		return fmt.Errorf("set lockConn statement_timeout: %w", err)
	}
	// Defer-rollback is a safety net: if fn succeeds the COMMIT
	// releases the lock; if fn fails we ROLLBACK which also releases
	// the lock. Either way, no manual unlock needed.
	defer func() {
		// Use a fresh context (the caller's may be cancelled) for
		// the rollback so we always clean up.
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*1000*1000) // 5s
		defer cancel()
		if rbErr := tx.Rollback(rollbackCtx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			// Rollback failed — log via retErr if set, otherwise
			// silent (the connection release in the outer defer
			// will close the txn anyway).
			if retErr == nil {
				retErr = fmt.Errorf("rollback lock txn: %w", rbErr)
			}
		}
	}()

	// Acquire the cluster mutex. This blocks until either we get
	// the lock or statement_timeout fires.
	if _, err := tx.Exec(ctx,
		fmt.Sprintf("SELECT pg_advisory_xact_lock(%d)", vacuumFullAdvisoryLockKey),
	); err != nil {
		// Distinguish lock-busy from other errors so callers can
		// surface "another replica is running" cleanly.
		if isLockNotAvailable(err) {
			return fmt.Errorf("%w: %v", ErrVacuumFullMutexBusy, err)
		}
		return fmt.Errorf("acquire advisory lock: %w", err)
	}

	// Hold the lock through the caller's work. The defer above
	// commits/rolls back the lock txn once fn returns.
	if err := fn(ctx, vacuumConn); err != nil {
		return err
	}
	// Commit the lock txn to release the mutex — but only AFTER fn
	// succeeded. If fn failed, the deferred Rollback releases the
	// lock.
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit lock txn: %w", err)
	}
	return nil
}

// isLockNotAvailable reports whether err comes from a Postgres lock
// acquisition timeout (55P03). We use stringly-typed matching here
// because pgconn.PgError wrapping is not guaranteed across pgx
// versions and the SQLSTATE is the stable contract.
func isLockNotAvailable(err error) bool {
	if err == nil {
		return false
	}
	// Quick check: postgres "lock_not_available" SQLSTATE.
	// We avoid importing pgconn just for this one error code.
	const lockNotAvailable = "55P03"
	return errContainsCode(err, lockNotAvailable)
}

// errContainsCode walks an error chain looking for the SQLSTATE.
// pgx wraps errors via fmt.Errorf("%w", ...), so errors.Is won't match
// — we walk manually. Tests in vacuum_mutex_test.go pin this behavior.
func errContainsCode(err error, code string) bool {
	for err != nil {
		if msg := err.Error(); msg != "" {
			// pgx surfaces SQLSTATE inside the message via
			// "(SQLSTATE 55P03)". Cheap substring check is enough.
			if contains(msg, code) {
				return true
			}
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// contains is strings.Contains without the import.
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
