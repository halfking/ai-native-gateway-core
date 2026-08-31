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
