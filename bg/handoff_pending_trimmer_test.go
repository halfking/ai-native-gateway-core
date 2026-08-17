package bg

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestHandoffPendingTrimmerNilPoolAndLifecycle(t *testing.T) {
	trimmer := NewHandoffPendingTrimmer(nil)
	if trimmer.tick != time.Minute {
		t.Fatalf("tick = %v, want one minute", trimmer.tick)
	}
	count, err := trimmer.TrimOnce(context.Background())
	if err != nil || count != 0 {
		t.Fatalf("TrimOnce(nil) = (%d, %v), want (0, nil)", count, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	trimmer.Start(ctx)
	cancel()
	select {
	case <-trimmer.done:
	case <-time.After(time.Second):
		t.Fatal("trimmer did not stop after context cancellation")
	}
	trimmer.Stop()
	trimmer.Stop()
}

// TestHandoffPendingTrimmer_RetentionFloor guards the safety floor: a TTL
// below 1 day (or an unset key) must never produce a zero/negative retention
// that would wipe handoff_pending_confirmations in a single batch.
func TestHandoffPendingTrimmer_RetentionFloor(t *testing.T) {
	got := pendingConfirmationRetention()
	if got < minHandoffPendingRetentionDays*24*time.Hour {
		t.Fatalf("pendingConfirmationRetention = %v, want >= %dd (must never wipe the table)",
			got, minHandoffPendingRetentionDays)
	}
}

// TestTrimOnceDB_ExpiresAndDeletes exercises the database/sql adapter path
// against a sqlmock. The expire step must fire before the delete step (each
// holds an UPDATE or DELETE bounded by LIMIT 5000), and the final returned
// count must equal the sum of rows affected by both statements.
func TestTrimOnceDB_ExpiresAndDeletes(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta(`UPDATE handoff_pending_confirmations SET status = 'expired'`)).
		WillReturnResult(sqlmock.NewResult(0, 7))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM handoff_pending_confirmations`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 12))

	count, err := TrimOnceDB(context.Background(), db)
	if err != nil {
		t.Fatalf("TrimOnceDB: %v", err)
	}
	if count != 19 {
		t.Fatalf("count = %d, want 19 (7 expired + 12 deleted)", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// Expire error must short-circuit the trim and surface to the caller. The
// delete step must NOT fire when the expire step fails.
func TestTrimOnceDB_PropagatesExpireError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	sentinel := errors.New("expire batch failed")
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE handoff_pending_confirmations SET status = 'expired'`)).
		WillReturnError(sentinel)

	if _, err := TrimOnceDB(context.Background(), db); !errors.Is(err, sentinel) {
		t.Fatalf("TrimOnceDB error = %v, want %v", err, sentinel)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("delete step must NOT fire on expire error; unmet: %v", err)
	}
}

func TestTrimOnceDB_NilDB_Noop(t *testing.T) {
	if _, err := TrimOnceDB(context.Background(), nil); err != nil {
		t.Fatalf("nil db must be a no-op, got %v", err)
	}
}
