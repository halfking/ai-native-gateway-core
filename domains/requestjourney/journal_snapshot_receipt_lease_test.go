package requestjourney

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pashagolub/pgxmock/v4"
)

func TestJournalSnapshotReceiptLiveLeaseRejectsSameOwner(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Unix(1700000000, 0).UTC()
	store := NewPostgresJournalSnapshotReceiptStore(mock, "shared-hostname")
	store.SetClockForTest(func() time.Time { return now })

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO journal_snapshot_receipts").WithArgs("tenant-a", "request-a", int64(2), "hash-a", int64(99), "shared-hostname", now.Add(journalSnapshotReceiptLease), now).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectQuery("SELECT payload_hash, projection_base_seq, status, claim_owner, claim_until").WithArgs("tenant-a", "request-a", int64(2)).WillReturnRows(
		pgxmock.NewRows([]string{"payload_hash", "projection_base_seq", "status", "claim_owner", "claim_until"}).AddRow("hash-a", int64(10), "processing", "shared-hostname", pgtype.Timestamptz{Time: now.Add(time.Second), Valid: true}),
	)
	mock.ExpectRollback()

	claim, err := store.ClaimWithProjectionBase(context.Background(), "tenant-a", "request-a", 2, "hash-a", 99)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Claimed || claim.AlreadyCompleted {
		t.Fatalf("live lease must not be reclaimed by same owner: %+v", claim)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestJournalSnapshotReceiptNilStoreDoesNotPanic(t *testing.T) {
	var store *JournalSnapshotReceiptStore
	if _, err := store.ClaimWithProjectionBase(context.Background(), "tenant-a", "request-a", 1, "hash-a", 0); err != ErrSnapshotReceiptInvalid {
		t.Fatalf("nil store claim error = %v, want %v", err, ErrSnapshotReceiptInvalid)
	}
}

// TestJournalSnapshotReceiptReclaimRejectsBaseMismatch locks in the P1-4
// fix: when a stale retry carries a different base than the one already
// stored (and the stored base is non-zero), the reclaim path must surface
// ErrSnapshotReceiptLeaseLost rather than silently overwriting the lease.
// The earlier base=0 everywhere default would let two concurrent retries
// race past the UNIQUE constraint without detection; this guard is what
// makes the concurrency contract enforceable.
func TestJournalSnapshotReceiptReclaimRejectsBaseMismatch(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Unix(1700000000, 0).UTC()
	store := NewPostgresJournalSnapshotReceiptStore(mock, "shared-hostname")
	store.SetClockForTest(func() time.Time { return now })

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	// INSERT path: caller supplies base=0 but stored base is 5 → second ON CONFLICT path.
	mock.ExpectExec("INSERT INTO journal_snapshot_receipts").WithArgs("tenant-a", "request-a", int64(2), "hash-a", int64(0), "shared-hostname", now.Add(journalSnapshotReceiptLease), now).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	// SELECT sees existing base=5 (different from caller's 0) with an expired lease.
	mock.ExpectQuery("SELECT payload_hash, projection_base_seq, status, claim_owner, claim_until").WithArgs("tenant-a", "request-a", int64(2)).WillReturnRows(
		pgxmock.NewRows([]string{"payload_hash", "projection_base_seq", "status", "claim_owner", "claim_until"}).AddRow("hash-a", int64(5), "processing", "prior-owner", pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true}),
	)
	mock.ExpectRollback()

	_, err = store.ClaimWithProjectionBase(context.Background(), "tenant-a", "request-a", 2, "hash-a", 0)
	if err != ErrSnapshotReceiptLeaseLost {
		t.Fatalf("base-mismatch reclaim must return ErrSnapshotReceiptLeaseLost, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestJournalSnapshotReceiptBothZeroReclaims is the P1-4 contract pin for
// the documented both-zero first-claim sentinel: when the caller supplies
// projection_base_seq=0 and the stored base is also 0, the in-store guard
// allows the reclaim to proceed under the same hash. The hash equality
// check still wins — a different-hash reclaim under both-zero must
// surface ErrSnapshotReceiptConflict rather than silently overwriting.
//
// This test exists to (a) lock the documented behaviour so future refactors
// cannot regress to a "no/zero" only guard, and (b) document that the
// both-zero path is NOT cross-process safe — call sites must serialise
// per-owner delivery (cmd/gateway/main_dispatch_observation.go).
func TestJournalSnapshotReceiptBothZeroReclaims(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Unix(1700000000, 0).UTC()
	store := NewPostgresJournalSnapshotReceiptStore(mock, "shared-hostname")
	store.SetClockForTest(func() time.Time { return now })

	// Both-zero, same hash, expired lease → reclaim succeeds. This mirrors
	// what the production dispatcher does when MaxSeq returns 0.
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO journal_snapshot_receipts").WithArgs("tenant-a", "request-a", int64(2), "hash-a", int64(0), "shared-hostname", now.Add(journalSnapshotReceiptLease), now).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectQuery("SELECT payload_hash, projection_base_seq, status, claim_owner, claim_until").WithArgs("tenant-a", "request-a", int64(2)).WillReturnRows(
		pgxmock.NewRows([]string{"payload_hash", "projection_base_seq", "status", "claim_owner", "claim_until"}).AddRow("hash-a", int64(0), "processing", "prior-owner", pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true}),
	)
	mock.ExpectExec("UPDATE journal_snapshot_receipts").WithArgs("tenant-a", "request-a", int64(2), "shared-hostname", now.Add(journalSnapshotReceiptLease), now, int64(0)).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	claim, err := store.ClaimWithProjectionBase(context.Background(), "tenant-a", "request-a", 2, "hash-a", 0)
	if err != nil {
		t.Fatalf("both-zero same-hash reclaim must succeed, got %v", err)
	}
	if !claim.Claimed || claim.AlreadyCompleted {
		t.Fatalf("both-zero same-hash reclaim = %+v, want Claimed", claim)
	}
	if claim.ProjectionBaseSeq != 0 {
		t.Fatalf("both-zero reclaim must retain base=0 in returned claim, got %d", claim.ProjectionBaseSeq)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	// Both-zero, different hash → hash guard must reject even though the
	// both-zero base exception would otherwise allow the reclaim.
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO journal_snapshot_receipts").WithArgs("tenant-a", "request-a", int64(2), "hash-a", int64(0), "shared-hostname", now.Add(journalSnapshotReceiptLease), now).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectQuery("SELECT payload_hash, projection_base_seq, status, claim_owner, claim_until").WithArgs("tenant-a", "request-a", int64(2)).WillReturnRows(
		pgxmock.NewRows([]string{"payload_hash", "projection_base_seq", "status", "claim_owner", "claim_until"}).AddRow("hash-other", int64(0), "processing", "prior-owner", pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true}),
	)
	mock.ExpectRollback()

	_, err = store.ClaimWithProjectionBase(context.Background(), "tenant-a", "request-a", 2, "hash-a", 0)
	if err != ErrSnapshotReceiptConflict {
		t.Fatalf("both-zero different-hash reclaim must return ErrSnapshotReceiptConflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
