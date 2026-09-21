package main

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

func testJournalSnapshot(version int64, count int) dispatch.JournalSnapshot {
	entries := make([]dispatch.JournalEntry, count)
	for i := range entries {
		entries[i] = dispatch.JournalEntry{Seq: i + 1, At: time.Unix(int64(i+1), 0).UTC(), Action: dispatch.NextActionRetrySameCred, Model: "model-a"}
	}
	return dispatch.JournalSnapshot{
		TenantID: "tenant-a", RequestID: "request-a", Entries: entries,
		SnapshotVersion: version, CallerTenantID: "tenant-a", CallerAuthorized: true,
	}
}

func TestDispatchJourneyJournalAdapterUsesSnapshotVersionReceipt(t *testing.T) {
	projection := requestjourney.NewProjection(requestjourney.DefaultConfig())
	recorder := requestjourney.NewRecorder(projection, nil, nil)
	adapter := newDispatchJourneyJournalAdapter(recorder, "gateway-a").(*dispatchJourneyJournalAdapter)
	snapshot := testJournalSnapshot(101, 2)
	adapter.ApplyJournalSnapshot(context.Background(), snapshot)
	adapter.ApplyJournalSnapshot(context.Background(), snapshot)

	journey, err := projection.Detail("tenant-a", "request-a")
	if err != nil || journey == nil {
		t.Fatalf("journey detail: %v %+v", err, journey)
	}
	if got := len(journey.Events); got != 2 {
		t.Fatalf("duplicate truncated snapshot appended events: got %d want 2", got)
	}
}

func TestDispatchJourneyJournalAdapterAllowsNewVersionAndRejectsZero(t *testing.T) {
	projection := requestjourney.NewProjection(requestjourney.DefaultConfig())
	recorder := requestjourney.NewRecorder(projection, nil, nil)
	adapter := newDispatchJourneyJournalAdapter(recorder, "gateway-a")
	adapter.ApplyJournalSnapshot(context.Background(), testJournalSnapshot(1, 1))
	adapter.ApplyJournalSnapshot(context.Background(), testJournalSnapshot(2, 1))
	adapter.ApplyJournalSnapshot(context.Background(), testJournalSnapshot(0, 1))

	journey, err := projection.Detail("tenant-a", "request-a")
	if err != nil || journey == nil {
		t.Fatalf("journey detail: %v %+v", err, journey)
	}
	if got := len(journey.Events); got != 2 {
		t.Fatalf("version handling produced %d events, want 2", got)
	}
}

func TestDispatchJourneyJournalAdapterSerializesConcurrentRetry(t *testing.T) {
	projection := requestjourney.NewProjection(requestjourney.DefaultConfig())
	recorder := requestjourney.NewRecorder(projection, nil, nil)
	adapter := newDispatchJourneyJournalAdapter(recorder, "gateway-a")
	snapshot := testJournalSnapshot(11, 3)
	done := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		go func() {
			adapter.ApplyJournalSnapshot(context.Background(), snapshot)
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	journey, err := projection.Detail("tenant-a", "request-a")
	if err != nil || journey == nil || len(journey.Events) != 3 {
		t.Fatalf("concurrent retry produced %+v (err=%v)", journey, err)
	}
}

func TestDispatchJourneyJournalAdapterDurableRetryKeepsProjectionBase(t *testing.T) {
	// 2026-08-31 P1-4 followup: the durable reclaim path now refuses to
	// re-claim when the caller's projection_base_seq disagrees with the
	// stored base (see journal_snapshot_receipt.go ClaimWithProjectionBase).
	// This test pins the contract: a retry issued AFTER the lease expired
	// but with a NEW projection_base_seq (MaxSeq has advanced because the
	// first call already applied its snapshot events) must be rejected
	// with ErrSnapshotReceiptLeaseLost, NOT silently re-apply the snapshot
	// with the stored base. The stored base still survives on disk so a
	// later retry that passes the matching base would replay deterministically.
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	projection := requestjourney.NewProjection(requestjourney.DefaultConfig())
	recorder := requestjourney.NewRecorder(projection, nil, nil)
	if err := recorder.Apply(context.Background(), requestjourney.JourneyEvent{
		TenantID: "tenant-a", GatewayInstanceID: "gateway-a", RequestID: "request-a", Seq: 50,
		Type: requestjourney.EventObservationDegraded, Stage: requestjourney.StageUpstream,
		ObservationStatus: requestjourney.ObservationDegraded, OccurredAt: time.Unix(50, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	receipt := requestjourney.NewPostgresJournalSnapshotReceiptStore(mock, "gateway-a")
	firstNow := time.Unix(1700000000, 0).UTC()
	receiptClock := firstNow
	receipt.SetClockForTest(func() time.Time { return receiptClock })
	adapter := newDispatchJourneyJournalAdapterWithReceipt(recorder, "gateway-a", receipt)
	snapshot := testJournalSnapshot(100, 2)
	snapshotHash, err := requestjourney.SnapshotPayloadHash(snapshot.TenantID, snapshot.RequestID, snapshot.Entries, snapshot.Truncated, snapshot.TruncatedCount, snapshot.SnapshotVersion)
	if err != nil {
		t.Fatal(err)
	}

	// First call: claim fixes base=50, completion fails (UPDATE 0)
	// because the row's status is still 'processing' — this is fine,
	// the projection events have already been applied in-memory.
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO journal_snapshot_receipts").WithArgs("tenant-a", "request-a", int64(100), snapshotHash, int64(50), "gateway-a", firstNow.Add(time.Minute), firstNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("UPDATE journal_snapshot_receipts").WithArgs("tenant-a", "request-a", int64(100), "gateway-a").WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()
	adapter.ApplyJournalSnapshot(context.Background(), snapshot)

	// Second call (retry, lease has expired). Caller-supplied base=52
	// (MaxSeq after first call applied 2 events) does NOT match the
	// stored base=50; the new P1-4 contract returns ErrLeaseLost and the
	// adapter must NOT re-apply the snapshot to the projection.
	receiptClock = firstNow.Add(2 * time.Minute)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO journal_snapshot_receipts").WithArgs("tenant-a", "request-a", int64(100), pgxmock.AnyArg(), int64(52), "gateway-a", receiptClock.Add(time.Minute), receiptClock).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectQuery("SELECT payload_hash, projection_base_seq, status, claim_owner, claim_until").WithArgs("tenant-a", "request-a", int64(100)).WillReturnRows(
		pgxmock.NewRows([]string{"payload_hash", "projection_base_seq", "status", "claim_owner", "claim_until"}).AddRow(snapshotHash, int64(50), "processing", "gateway-a", pgtype.Timestamptz{Time: firstNow.Add(time.Minute), Valid: true}))
	mock.ExpectRollback()
	adapter.ApplyJournalSnapshot(context.Background(), snapshot)

	// Projection still holds the original 3 events from the first call:
	// Seq 50 (preexisting observation) + Seq 51, 52 (snapshot entries).
	// The retry must NOT have appended duplicate 53/54 events.
	journey, err := projection.Detail("tenant-a", "request-a")
	if err != nil || journey == nil {
		t.Fatalf("journey detail: %v %+v", err, journey)
	}
	if got := len(journey.Events); got != 3 {
		t.Fatalf("durable retry should not mutate projection: got %d events, want 3 (1 preexisting + 2 snapshot)", got)
	}
	if journey.Events[0].Seq != 50 || journey.Events[1].Seq != 51 || journey.Events[2].Seq != 52 {
		t.Fatalf("projection seqs drifted after rejected retry: %+v", journey.Events)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchJourneyJournalAdapterRejectsSameVersionPayloadConflict(t *testing.T) {
	projection := requestjourney.NewProjection(requestjourney.DefaultConfig())
	recorder := requestjourney.NewRecorder(projection, nil, nil)
	adapter := newDispatchJourneyJournalAdapter(recorder, "gateway-a")
	first := testJournalSnapshot(7, 1)
	second := testJournalSnapshot(7, 1)
	second.Entries[0].Model = "model-b"
	adapter.ApplyJournalSnapshot(context.Background(), first)
	adapter.ApplyJournalSnapshot(context.Background(), second)

	journey, err := projection.Detail("tenant-a", "request-a")
	if err != nil || journey == nil {
		t.Fatalf("journey detail: %v %+v", err, journey)
	}
	if got := len(journey.Events); got != 1 || journey.Events[0].Model != "model-a" {
		t.Fatalf("conflicting snapshot changed projection: %+v", journey.Events)
	}
}
