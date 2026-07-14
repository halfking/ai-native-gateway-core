package attachments

import (
	"errors"
	"testing"
	"time"
)

func TestToRow_FillsDefaults(t *testing.T) {
	now := time.Now()
	meta := AttachmentMetadata{
		Type:        "",
		ContentType: "image/png",
		Size:        1234,
		Path:        "2026/07/a1/b2/abcdef.png",
		Hash:        "abcdef0123456789",
		OriginalURL: "data:image/png;base64,AAA",
		CreatedAt:   now,
		Status:      "",
	}
	row := ToRow("req-1", meta)
	if row.AttachmentType != "image" {
		t.Fatalf("empty Type should default to 'image', got %q", row.AttachmentType)
	}
	if row.Status != string(AttachmentStatusDetected) {
		t.Fatalf("empty Status should default to 'detected', got %q", row.Status)
	}
	if row.RequestID != "req-1" {
		t.Fatalf("RequestID not propagated: got %q", row.RequestID)
	}
	if row.SizeBytes != 1234 {
		t.Fatalf("SizeBytes not propagated: got %d", row.SizeBytes)
	}
	if row.Hash != "abcdef0123456789" {
		t.Fatalf("Hash not propagated: got %q", row.Hash)
	}
	if !row.CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt not propagated: got %v want %v", row.CreatedAt, now)
	}
}

func TestToRow_ZeroCreatedAtFallback(t *testing.T) {
	before := time.Now()
	row := ToRow("req-2", AttachmentMetadata{
		Type:      "image",
		CreatedAt: time.Time{},
	})
	if row.CreatedAt.Before(before) {
		t.Fatalf("zero CreatedAt should fall back to time.Now(), got %v before %v", row.CreatedAt, before)
	}
}

func TestToRow_PreservesFailureFields(t *testing.T) {
	meta := AttachmentMetadata{
		Type:      "image",
		Status:    AttachmentStatusStoreFailed,
		ErrorCode: "storage_unavailable",
		Hash:      "deadbeef",
	}
	row := ToRow("req-3", meta)
	if row.Status != string(AttachmentStatusStoreFailed) {
		t.Fatalf("StoreFailed status lost: got %q", row.Status)
	}
	if row.ErrorCode != "storage_unavailable" {
		t.Fatalf("ErrorCode lost: got %q", row.ErrorCode)
	}
	if row.Hash != "deadbeef" {
		t.Fatalf("Hash lost: got %q", row.Hash)
	}
}

func TestNullableString(t *testing.T) {
	if nullableString("") != nil {
		t.Fatal("empty string should map to nil")
	}
	if nullableString("x") != "x" {
		t.Fatal("non-empty string should pass through")
	}
}

func TestRepository_NoDBReturnsErrNoDB(t *testing.T) {
	var nilRepo *Repository
	if _, err := nilRepo.InsertOne(nil, RequestAttachmentRow{}); !errors.Is(err, errNoDB) {
		t.Fatalf("nil repo InsertOne should return errNoDB, got %v", err)
	}
	// InsertBatch with empty input is a no-op (no DB needed).
	if _, err := nilRepo.InsertBatch(nil, nil); !errors.Is(err, nil) {
		t.Fatalf("nil repo InsertBatch(nil) should be no-op, got %v", err)
	}
	// InsertBatch with non-empty input still requires DB.
	if _, err := nilRepo.InsertBatch(nil, []RequestAttachmentRow{{RequestID: "x"}}); !errors.Is(err, errNoDB) {
		t.Fatalf("nil repo InsertBatch(non-empty) should return errNoDB, got %v", err)
	}
	if _, err := nilRepo.ListByRequestID(nil, "x"); !errors.Is(err, errNoDB) {
		t.Fatalf("nil repo ListByRequestID should return errNoDB, got %v", err)
	}
	if _, err := nilRepo.ListByHash(nil, "x"); !errors.Is(err, errNoDB) {
		t.Fatalf("nil repo ListByHash should return errNoDB, got %v", err)
	}
	if _, err := nilRepo.CountByStatus(nil, "stored", time.Now()); !errors.Is(err, errNoDB) {
		t.Fatalf("nil repo CountByStatus should return errNoDB, got %v", err)
	}
	if _, err := nilRepo.DeleteOlderThan(nil, time.Now()); !errors.Is(err, errNoDB) {
		t.Fatalf("nil repo DeleteOlderThan should return errNoDB, got %v", err)
	}

	r := &Repository{}
	if _, err := r.InsertOne(nil, RequestAttachmentRow{}); !errors.Is(err, errNoDB) {
		t.Fatalf("db=nil repo InsertOne should return errNoDB, got %v", err)
	}
}

func TestInsertBatch_EmptyIsNoOp(t *testing.T) {
	r := &Repository{}
	n, err := r.InsertBatch(nil, nil)
	if err != nil || n != 0 {
		t.Fatalf("InsertBatch(nil) should be no-op, got n=%d err=%v", n, err)
	}
	n, err = r.InsertBatch(nil, []RequestAttachmentRow{})
	if err != nil || n != 0 {
		t.Fatalf("InsertBatch([]) should be no-op, got n=%d err=%v", n, err)
	}
}
