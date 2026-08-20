package requestjourney

import (
	"context"
	"encoding/json"
	"errors"
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
	outbox := newObservationOutbox(mock, &fakeJourneyWriter{}, nil, "worker-a")
	outbox.clock = func() time.Time { return now }

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO request_journey_observation_outbox").WithArgs(event.TenantID, event.RequestID, event.Seq, pgxmock.AnyArg(), pgxmock.AnyArg(), now).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	if err := outbox.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
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
	outbox := newObservationOutbox(mock, &fakeJourneyWriter{}, nil, "worker-a")
	outbox.clock = func() time.Time { return now }
	outbox.batch = 1

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, payload, payload_hash, attempts").WithArgs(now).
		WillReturnRows(pgxmock.NewRows([]string{"id", "payload", "payload_hash", "attempts"}).AddRow(7, payload, "hash", 0))
	mock.ExpectExec("UPDATE request_journey_observation_outbox").WithArgs(int64(7), "worker-a", now.Add(defaultObservationOutboxLease), now).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	claims, err := outbox.claim(context.Background(), "", "", 0)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM request_journey_observation_outbox").WithArgs(int64(7), "worker-a").WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()
	if err := outbox.deliver(context.Background(), claims[0]); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func marshalJourneyEvent(event JourneyEvent) ([]byte, error) {
	return json.Marshal(event)
}
