package requestjourney

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func TestObservationOutboxEnqueueIsIdempotentAndConflictsOnDifferentPayload(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	event := testJourneyEvent("tenant-a", "request-1", 1)
	now := time.Unix(1700000000, 123456000).UTC()
	outbox := NewObservationOutbox(mock, NewPostgresRepository(mock), nil, "worker-a")
	outbox.clock = func() time.Time { return now }

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO request_journey_observation_outbox").WithArgs(event.TenantID, event.RequestID, event.Seq, pgxmock.AnyArg(), pgxmock.AnyArg(), now).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	if err := outbox.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO request_journey_observation_outbox").WithArgs(event.TenantID, event.RequestID, event.Seq, pgxmock.AnyArg(), pgxmock.AnyArg(), now).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectQuery("SELECT payload_hash").WithArgs(event.TenantID, event.RequestID, event.Seq).WillReturnRows(pgxmock.NewRows([]string{"payload_hash"}).AddRow("different"))
	mock.ExpectRollback()
	if err := outbox.Enqueue(context.Background(), event); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("conflicting enqueue error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestObservationOutboxClaimAndDeliverLeavesLeaseSafeAck(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	event := testJourneyEvent("tenant-a", "request-1", 1)
	payload, err := marshalJourneyEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	outbox := NewObservationOutbox(mock, NewPostgresRepository(mock), nil, "worker-a")
	outbox.clock = func() time.Time { return now }
	outbox.batch = 1

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT id, payload, payload_hash, attempts, claim_fencing_token").WithArgs(now, false).
		WillReturnRows(pgxmock.NewRows([]string{"id", "payload", "payload_hash", "attempts", "claim_fencing_token"}).AddRow(7, payload, "hash", 0, 0))
	mock.ExpectQuery("UPDATE request_journey_observation_outbox").WithArgs(int64(7), "worker-a", now.Add(defaultObservationOutboxLease), now, int64(0)).
		WillReturnRows(pgxmock.NewRows([]string{"claim_fencing_token", "attempts"}).AddRow(1, 1))
	mock.ExpectCommit()
	claims, err := outbox.claim(context.Background(), "", "", 0, false)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO request_state_transitions").WithArgs(
		event.TenantID, event.GatewayInstanceID, event.RequestID, event.Seq,
		event.Type, event.Stage, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		event.ObservationStatus, pgxmock.AnyArg(), event.OccurredAt,
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("DELETE FROM request_journey_observation_outbox").WithArgs(int64(7), "worker-a", int64(1)).WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()
	if err := outbox.deliver(context.Background(), claims[0]); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestObservationOutboxCloseBeforeStartIsTerminalAndIdempotent(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	outbox := NewObservationOutbox(mock, NewPostgresRepository(mock), nil, "worker-lifecycle")
	if outbox == nil {
		t.Fatal("expected outbox")
	}
	if err := outbox.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	outbox.Start()
	if err := outbox.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestObservationOutboxReleaseRequiresActiveFencedLease(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Unix(1700000000, 0).UTC()
	outbox := NewObservationOutbox(mock, NewPostgresRepository(mock), nil, "worker-a")
	outbox.clock = func() time.Time { return now }
	claim := claimedObservation{ID: 7, Owner: "worker-a", ClaimFencingToken: 2, Attempts: 2}
	cause := errors.New("redis unavailable")

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("UPDATE request_journey_observation_outbox").
		WithArgs(int64(7), "worker-a", int64(2), cause.Error(), now.Add(500*time.Millisecond), now).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()
	if err := outbox.release(context.Background(), claim, cause); err == nil {
		t.Fatal("release should reject a stale claim")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func marshalJourneyEvent(event JourneyEvent) ([]byte, error) {
	return json.Marshal(event)
}
