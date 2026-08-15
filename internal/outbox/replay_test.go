package outbox

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

type recordingDeliverer struct {
	eventIDs []string
	errFor   map[string]error
}

func (d *recordingDeliverer) Deliver(_ context.Context, env EventEnvelope) error {
	d.eventIDs = append(d.eventIDs, env.EventID)
	return d.errFor[env.EventID]
}

func replayRow(id int64, eventID, tenantID string, occurredAt time.Time) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "event_id", "event_type", "schema_version", "tenant_id",
		"aggregate_id", "aggregate_version", "occurred_at", "payload",
	}).AddRow(
		id, eventID, "request.completed.v1", 1, tenantID,
		"session-1", 1, occurredAt, []byte(`{"request_id":"request-1","correlation_id":"corr-1"}`),
	)
}

func TestReplayerSelectionAndIdempotentRerun(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	from := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	options := ReplayOptions{TenantID: "tenant-1", From: &from, To: &to, Limit: 10}
	deliverer := &recordingDeliverer{errFor: map[string]error{}}
	replayer := NewReplayer(db, deliverer)

	mock.ExpectQuery(replaySelectionQuery).WithArgs("", "tenant-1", from, to, 10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "event_id"}).
			AddRow(int64(7), "evt-replay-1").
			AddRow(int64(8), "evt-replay-2"))
	for _, selected := range []struct {
		id      int64
		eventID string
	}{{7, "evt-replay-1"}, {8, "evt-replay-2"}} {
		mock.ExpectBegin()
		mock.ExpectQuery(replayClaimQuery).WithArgs(selected.id).WillReturnRows(replayRow(selected.id, selected.eventID, "tenant-1", from.Add(time.Hour)))
		mock.ExpectExec(replayMarkSentQuery).WithArgs(selected.id).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
	}

	result, err := replayer.Replay(context.Background(), options)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if result.Selected != 2 || result.Replayed != 2 || result.Failed != 0 || result.Skipped != 0 {
		t.Fatalf("result = %+v", result)
	}
	if !reflect.DeepEqual(deliverer.eventIDs, []string{"evt-replay-1", "evt-replay-2"}) {
		t.Fatalf("delivered event IDs = %v", deliverer.eventIDs)
	}

	// A rerun with the same selector is a no-op after successful rows have left DLQ.
	mock.ExpectQuery(replaySelectionQuery).WithArgs("", "tenant-1", from, to, 10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "event_id"}))
	second, err := replayer.Replay(context.Background(), options)
	if err != nil {
		t.Fatalf("second Replay: %v", err)
	}
	if second.Selected != 0 || second.Replayed != 0 {
		t.Fatalf("second result = %+v, want no-op", second)
	}
	if len(deliverer.eventIDs) != 2 {
		t.Fatalf("rerun delivered events again: %v", deliverer.eventIDs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

func TestReplayerDryRunSelectsWithoutDelivery(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	deliverer := &recordingDeliverer{errFor: map[string]error{}}
	replayer := NewReplayer(db, deliverer)
	mock.ExpectQuery(replaySelectionQuery).WithArgs("evt-one", "", nil, nil, 100).
		WillReturnRows(sqlmock.NewRows([]string{"id", "event_id"}).AddRow(int64(9), "evt-one"))

	result, err := replayer.Replay(context.Background(), ReplayOptions{EventID: "evt-one", DryRun: true})
	if err != nil {
		t.Fatalf("Replay dry-run: %v", err)
	}
	if result.Selected != 1 || result.Replayed != 0 || !reflect.DeepEqual(result.EventIDs, []string{"evt-one"}) {
		t.Fatalf("result = %+v", result)
	}
	if len(deliverer.eventIDs) != 0 {
		t.Fatalf("dry-run delivered events: %v", deliverer.eventIDs)
	}
}

func TestReplayerFailureRemainsInDLQ(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	deliverErr := errors.New("session manager unavailable")
	deliverer := &recordingDeliverer{errFor: map[string]error{"evt-failed": deliverErr}}
	replayer := NewReplayer(db, deliverer)
	mock.ExpectQuery(replaySelectionQuery).WithArgs("evt-failed", "", nil, nil, 100).
		WillReturnRows(sqlmock.NewRows([]string{"id", "event_id"}).AddRow(int64(10), "evt-failed"))
	mock.ExpectBegin()
	mock.ExpectQuery(replayClaimQuery).WithArgs(int64(10)).WillReturnRows(replayRow(10, "evt-failed", "tenant-1", time.Now()))
	mock.ExpectExec(replayMarkFailedQuery).WithArgs(deliverErr.Error(), int64(10)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := replayer.Replay(context.Background(), ReplayOptions{EventID: "evt-failed"})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if result.Failed != 1 || result.Replayed != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestReplayOptionsRequireSelector(t *testing.T) {
	replayer := NewReplayer(&sql.DB{}, &recordingDeliverer{})
	_, err := replayer.Replay(context.Background(), ReplayOptions{})
	if !errors.Is(err, ErrReplaySelectorRequired) {
		t.Fatalf("Replay error = %v, want ErrReplaySelectorRequired", err)
	}
}
