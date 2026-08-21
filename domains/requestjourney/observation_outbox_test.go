package requestjourney

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func expectTenantContext(mock pgxmock.PgxPoolIface, tenantID string) {
	mock.ExpectExec(`SELECT set_config\('app.current_tenant', \$1, true\)`).WithArgs(tenantID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
}

func expectWorkerContext(mock pgxmock.PgxPoolIface) {
	mock.ExpectExec(`SELECT set_config\('app.current_role', 'super_admin', true\)`).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`SELECT set_config\('app.bypass_rls', 'true', true\)`).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
}

func TestObservationOutboxEnqueueIsTenantScopedAndConflictsOnDifferentPayload(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	event := testJourneyEvent("tenant-a", "request-1", 1)
	now := time.Unix(1700000000, 123456000).UTC()
	outbox := newObservationOutbox(mock, NewPostgresRepository(mock), nil, "worker-a")
	outbox.clock = func() time.Time { return now }

	mock.ExpectBegin()
	expectTenantContext(mock, event.TenantID)
	mock.ExpectExec("INSERT INTO request_journey_observation_outbox").WithArgs(event.TenantID, event.RequestID, event.Seq, pgxmock.AnyArg(), pgxmock.AnyArg(), now).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	if err := outbox.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	expectTenantContext(mock, event.TenantID)
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

func TestObservationOutboxClaimProjectsAndAcknowledgesWithFence(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	event := testJourneyEvent("tenant-a", "request-1", 1)
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	outbox := newObservationOutbox(mock, NewPostgresRepository(mock), nil, "worker-a")
	outbox.clock = func() time.Time { return now }
	outbox.batch = 1

	mock.ExpectBegin()
	expectWorkerContext(mock)
	mock.ExpectQuery("SELECT id, tenant_id, request_id, seq, payload, payload_hash, attempts, claim_token, status, next_retry_at, claim_until").WithArgs(now).
		WillReturnRows(pgxmock.NewRows([]string{"id", "tenant_id", "request_id", "seq", "payload", "payload_hash", "attempts", "claim_token", "status", "next_retry_at", "claim_until"}).
			AddRow(7, event.TenantID, event.RequestID, event.Seq, payload, "hash", 0, 0, "pending", now, nil))
	mock.ExpectQuery("UPDATE request_journey_observation_outbox").WithArgs(int64(7), "worker-a", now.Add(defaultObservationOutboxLease), now).
		WillReturnRows(pgxmock.NewRows([]string{"claim_token"}).AddRow(1))
	mock.ExpectCommit()
	claims, err := outbox.claim(context.Background(), "", "", 0, false)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}

	mock.ExpectBegin()
	expectTenantContext(mock, event.TenantID)
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
	expectTenantContext(mock, event.TenantID)
	mock.ExpectExec("DELETE FROM request_journey_observation_outbox").WithArgs(int64(7), event.TenantID, "worker-a", int64(1)).WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()
	if err := outbox.deliver(context.Background(), claims[0]); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestObservationOutboxReleaseRejectsStaleFence(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Unix(1700000000, 0).UTC()
	outbox := newObservationOutbox(mock, NewPostgresRepository(mock), nil, "worker-a")
	outbox.clock = func() time.Time { return now }
	claim := claimedObservation{ID: 7, Owner: "worker-a", ClaimToken: 3, Attempts: 2, Event: testJourneyEvent("tenant-a", "request-1", 1)}

	mock.ExpectBegin()
	expectTenantContext(mock, claim.Event.TenantID)
	mock.ExpectExec("UPDATE request_journey_observation_outbox").WithArgs(int64(7), claim.Event.TenantID, "worker-a", "redis unavailable", now.Add(500*time.Millisecond), now, int64(3)).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()
	if err := outbox.release(context.Background(), claim, errors.New("redis unavailable")); err == nil {
		t.Fatal("stale fence release unexpectedly succeeded")
	}
}
