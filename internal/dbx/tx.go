package dbx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// WithTx opens a read-write transaction, runs fn with a deferred rollback
// (idempotent; a no-op after commit), then commits. Panics inside fn still
// roll back via the deferred call.
func WithTx(ctx context.Context, beginner TxBeginner, fn func(context.Context, pgx.Tx) error) error {
	return runTx(ctx, beginner, pgx.TxOptions{}, fn)
}

// WithReadOnlyTx opens a read-only transaction (RLS reads, dashboards).
func WithReadOnlyTx(ctx context.Context, beginner TxBeginner, fn func(context.Context, pgx.Tx) error) error {
	return runTx(ctx, beginner, pgx.TxOptions{AccessMode: pgx.ReadOnly}, fn)
}

func runTx(ctx context.Context, beginner TxBeginner, opts pgx.TxOptions, fn func(context.Context, pgx.Tx) error) error {
	if beginner == nil {
		return fmt.Errorf("dbx: runTx: nil TxBeginner: %w", ErrInvalidInput)
	}
	tx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("dbx: begin tx: %w", err)
	}
	defer func() {
		// Rollback after Commit returns ErrTxClosed and is ignored. The
		// rollback deliberately detaches from the request context: when fn
		// failed *because* ctx was cancelled, rolling back with that same
		// ctx would make pgx kill the pooled connection instead of just
		// aborting the transaction.
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("dbx: commit: %w", err)
	}
	return nil
}

// WithSavepoint runs fn inside a savepoint on an existing transaction. On
// error the savepoint is rolled back (partial rollback) and the error is
// returned; on success the savepoint is released. name must pass
// ValidateIdentifier.
func WithSavepoint(ctx context.Context, tx pgx.Tx, name string, fn func(context.Context, pgx.Tx) error) error {
	if err := ValidateIdentifier(name); err != nil {
		return fmt.Errorf("dbx: savepoint name: %w", err)
	}
	if _, err := tx.Exec(ctx, "SAVEPOINT "+name); err != nil {
		return fmt.Errorf("dbx: savepoint %s: %w", name, err)
	}
	if err := fn(ctx, tx); err != nil {
		// Same cancellation-safety rule as runTx: roll the savepoint back
		// even when the request context is already dead.
		if _, rbErr := tx.Exec(context.WithoutCancel(ctx), "ROLLBACK TO SAVEPOINT "+name); rbErr != nil {
			return fmt.Errorf("dbx: rollback to savepoint %s: %v (original error: %w)", name, rbErr, err)
		}
		return err
	}
	if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT "+name); err != nil {
		return fmt.Errorf("dbx: release savepoint %s: %w", name, err)
	}
	return nil
}

// Retryable SQLSTATE classes: serialization failure and deadlock detected.
const (
	sqlStateSerialization = "40001"
	sqlStateDeadlock      = "40P01"
)

// IsRetryableTxError reports whether err is a transient transaction conflict
// (serialization failure / deadlock) that a retry can resolve.
func IsRetryableTxError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == sqlStateSerialization || pgErr.Code == sqlStateDeadlock
	}
	return false
}

// RetryOnConflict retries fn while it fails with a retryable transaction
// conflict, up to attempts total, with exponential backoff (base doubled per
// attempt, capped at 1s). ctx cancellation aborts between attempts. The
// last error is returned as-is.
func RetryOnConflict(ctx context.Context, attempts int, base time.Duration, fn func() error) error {
	if attempts <= 0 {
		return fmt.Errorf("dbx: RetryOnConflict: attempts must be >= 1: %w", ErrInvalidInput)
	}
	if base <= 0 {
		base = 25 * time.Millisecond
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if !IsRetryableTxError(err) {
			return err
		}
		timer := time.NewTimer(backoffDelay(base, attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

// backoffDelay doubles base per attempt with saturation. It deliberately
// avoids base << attempt, which overflows to a negative duration for large
// attempt counts and would collapse the backoff into a busy loop.
func backoffDelay(base time.Duration, attempt int) time.Duration {
	wait := base
	for i := 0; i < attempt && wait < time.Second; i++ {
		wait *= 2
	}
	if wait > time.Second {
		wait = time.Second
	}
	return wait
}
