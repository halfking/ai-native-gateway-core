package requestjourney

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func TestSnapshotPayloadHashExcludesCallerAuthorizationMetadata(t *testing.T) {
	entries := []struct {
		Seq int `json:"seq"`
	}{{Seq: 1}}
	first, err := SnapshotPayloadHash("tenant-a", "request-a", entries, false, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SnapshotPayloadHash("tenant-a", "request-a", entries, false, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("canonical snapshot hash changed without payload change: %q != %q", first, second)
	}
	changed, err := SnapshotPayloadHash("tenant-a", "request-a", entries, true, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first == changed {
		t.Fatal("truncation metadata must participate in the payload hash")
	}
}

func TestJournalSnapshotReceiptClaimIsIdempotentAndHashBound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	now := time.Unix(1700000000, 0).UTC()
	store := NewPostgresJournalSnapshotReceiptStore(mock, "gateway-a")
	store.clock = func() time.Time { return now }

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO journal_snapshot_receipts").WithArgs("tenant-a", "request-a", int64(4), "hash-a", "gateway-a", now.Add(journalSnapshotReceiptLease), now).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	claim, err := store.Claim(context.Background(), "tenant-a", "request-a", 4, "hash-a")
	if err != nil || !claim.Claimed || claim.AlreadyCompleted {
		t.Fatalf("first claim = %+v, err=%v", claim, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO journal_snapshot_receipts").WithArgs("tenant-a", "request-a", int64(4), "hash-a", "gateway-a", now.Add(journalSnapshotReceiptLease), now).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectQuery("SELECT payload_hash, status, claim_owner, claim_until").WithArgs("tenant-a", "request-a", int64(4)).WillReturnRows(
		pgxmock.NewRows([]string{"payload_hash", "status", "claim_owner", "claim_until"}).AddRow("hash-a", "completed", "gateway-a", nil),
	)
	mock.ExpectRollback()
	claim, err = store.Claim(context.Background(), "tenant-a", "request-a", 4, "hash-a")
	if err != nil || !claim.AlreadyCompleted || claim.Claimed {
		t.Fatalf("duplicate claim = %+v, err=%v", claim, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("INSERT INTO journal_snapshot_receipts").WithArgs("tenant-a", "request-a", int64(4), "hash-a", "gateway-a", now.Add(journalSnapshotReceiptLease), now).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectQuery("SELECT payload_hash, status, claim_owner, claim_until").WithArgs("tenant-a", "request-a", int64(4)).WillReturnRows(
		pgxmock.NewRows([]string{"payload_hash", "status", "claim_owner", "claim_until"}).AddRow("hash-other", "completed", "gateway-a", nil),
	)
	mock.ExpectRollback()
	_, err = store.Claim(context.Background(), "tenant-a", "request-a", 4, "hash-a")
	if !errors.Is(err, ErrSnapshotReceiptConflict) {
		t.Fatalf("hash conflict = %v, want ErrSnapshotReceiptConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestJournalSnapshotReceiptCompleteRequiresClaimOwner(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	store := NewPostgresJournalSnapshotReceiptStore(mock, "gateway-a")
	claim := JournalSnapshotReceiptClaim{TenantID: "tenant-a", RequestID: "request-a", SnapshotVersion: 2, Owner: "gateway-a"}
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.bypass_rls', 'true', true)")).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("UPDATE journal_snapshot_receipts").WithArgs("tenant-a", "request-a", int64(2), "gateway-a").WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()
	if err := store.Complete(context.Background(), claim); err == nil {
		t.Fatal("expected completion to reject lost claim")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
