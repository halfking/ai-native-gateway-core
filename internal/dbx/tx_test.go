package dbx

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
)

func TestWithTxCommitAndRollback(t *testing.T) {
	mock, _ := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	defer mock.Close()

	// Commit path.
	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectCommit()
	if err := WithTx(context.Background(), mock, func(context.Context, pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	// Rollback path on fn error.
	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectRollback()
	boom := errors.New("boom")
	err := WithTx(context.Background(), mock, func(context.Context, pgx.Tx) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("WithTx err = %v, want boom", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestWithReadOnlyTxOptions(t *testing.T) {
	mock, _ := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	defer mock.Close()
	mock.ExpectBeginTx(pgx.TxOptions{AccessMode: pgx.ReadOnly})
	mock.ExpectCommit()
	if err := WithReadOnlyTx(context.Background(), mock, func(context.Context, pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("WithReadOnlyTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestWithSavepoint(t *testing.T) {
	mock, _ := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	defer mock.Close()

	// Success: SAVEPOINT → RELEASE → Commit.
	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectExec("SAVEPOINT sp1").WillReturnResult(pgxmock.NewResult("SAVEPOINT", 0))
	mock.ExpectExec("RELEASE SAVEPOINT sp1").WillReturnResult(pgxmock.NewResult("RELEASE", 0))
	mock.ExpectCommit()
	err := WithTx(context.Background(), mock, func(ctx context.Context, tx pgx.Tx) error {
		return WithSavepoint(ctx, tx, "sp1", func(context.Context, pgx.Tx) error { return nil })
	})
	if err != nil {
		t.Fatalf("savepoint success path: %v", err)
	}

	// Failure: SAVEPOINT → ROLLBACK TO → error propagates → tx rollback.
	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectExec("SAVEPOINT sp1").WillReturnResult(pgxmock.NewResult("SAVEPOINT", 0))
	mock.ExpectExec("ROLLBACK TO SAVEPOINT sp1").WillReturnResult(pgxmock.NewResult("ROLLBACK", 0))
	mock.ExpectRollback()
	boom := errors.New("partial failure")
	err = WithTx(context.Background(), mock, func(ctx context.Context, tx pgx.Tx) error {
		return WithSavepoint(ctx, tx, "sp1", func(context.Context, pgx.Tx) error { return boom })
	})
	if !errors.Is(err, boom) {
		t.Fatalf("savepoint failure path err = %v, want boom", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}

	// Injection-safe name rejection.
	if err := WithSavepoint(context.Background(), nil, "sp; DROP TABLE x", nil); err == nil {
		t.Error("invalid savepoint name accepted")
	}
}

func TestIsRetryableTxError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: "40001"}, true},
		{&pgconn.PgError{Code: "40P01"}, true},
		{&pgconn.PgError{Code: "23505"}, false},
		{fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "40001"}), true},
		{errors.New("plain"), false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := IsRetryableTxError(tc.err); got != tc.want {
			t.Errorf("IsRetryableTxError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestRetryOnConflict(t *testing.T) {
	ctx := context.Background()
	calls := 0
	err := RetryOnConflict(ctx, 3, time.Millisecond, func() error {
		calls++
		if calls < 3 {
			return &pgconn.PgError{Code: "40001"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RetryOnConflict: %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}

	// Non-retryable fails immediately.
	calls = 0
	boom := &pgconn.PgError{Code: "23505"}
	if err := RetryOnConflict(ctx, 5, time.Millisecond, func() error {
		calls++
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on non-conflict)", calls)
	}

	// Exhausted attempts return the last conflict.
	calls = 0
	conflict := &pgconn.PgError{Code: "40P01"}
	if err := RetryOnConflict(ctx, 2, time.Millisecond, func() error {
		calls++
		return conflict
	}); !errors.Is(err, conflict) {
		t.Fatalf("err = %v, want conflict", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}

	// Invalid attempts rejected.
	if err := RetryOnConflict(ctx, 0, time.Millisecond, func() error { return nil }); err == nil {
		t.Error("attempts=0 accepted")
	}

	// Context cancellation stops the retry loop.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := RetryOnConflict(cctx, 3, time.Hour, func() error {
		return &pgconn.PgError{Code: "40001"}
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled err = %v, want context.Canceled", err)
	}
}

func TestBackoffDelayNoOverflow(t *testing.T) {
	// base << attempt would overflow int64 and go negative for large
	// attempts; the saturated implementation must always stay within
	// [base, 1s].
	for _, attempt := range []int{0, 1, 10, 53, 62, 100} {
		got := backoffDelay(1<<40, attempt)
		if got <= 0 || got > time.Second {
			t.Errorf("backoffDelay(1<<40, %d) = %v, want (0, 1s]", attempt, got)
		}
	}
	if got := backoffDelay(time.Millisecond, 0); got != time.Millisecond {
		t.Errorf("backoffDelay first attempt = %v, want base", got)
	}
	if got := backoffDelay(time.Millisecond, 3); got != 8*time.Millisecond {
		t.Errorf("backoffDelay doubling = %v, want 8ms", got)
	}
	if got := backoffDelay(time.Second, 1); got != time.Second {
		t.Errorf("backoffDelay cap = %v, want 1s", got)
	}
}

func TestWithTxRollbackOnCancelledContext(t *testing.T) {
	// fn fails because the request context was cancelled; the deferred
	// rollback must still be issued (with a detached context) instead of
	// skipping or killing the connection.
	mock, _ := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectRollback()

	ctx, cancel := context.WithCancel(context.Background())
	err := WithTx(ctx, mock, func(context.Context, pgx.Tx) error {
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("rollback expectation: %v", err)
	}
}
