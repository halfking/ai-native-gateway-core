package migration

import (
	"context"
	"testing"
	"time"
)

// seedCopyItem inserts an Item into the ledger in the same shape the
// preflight pass would, then returns it for mutation by the test.
func seedCopyItem(t *testing.T, ledger *Ledger, source, canonical string, pttlMs int64, generation int64, fields map[string]string) Item {
	t.Helper()
	it := Item{
		SourceKey:      source,
		KeyType:        "hash",
		CanonicalKey:   canonical,
		SchemaSource:   "legacy",
		Classification: ClassificationMigratable,
		Status:         StatusClassified,
		PTTLMs:         pttlMs,
		Generation:     generation,
		FieldChecksum:  fieldChecksum(fields),
		ScanRunID:      "test",
	}
	if err := ledger.Append(it); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	return it
}

// TestCopySkipsMissingTTLMinus2 documents doc 14 §6.1: PTTL=-2 means the key
// is gone; copy must skip and not write to the target.
func TestCopySkipsMissingTTLMinus2(t *testing.T) {
	_, rdb := newFixtureRedis(t)
	ledger := NewLedger(t.TempDir() + "/ledger.ndjson")
	canonical := migPrefix + "node:k2:YQ:7:Yjo4OmM"
	seedCopyItem(t, ledger, migPrefix+"node:absent:7:b:8:c", canonical, -2, 1, legacyHashFieldsAsStrings())

	cp := &Copy{Prefix: migPrefix, Ledger: ledger, RDB: rdb}
	results, err := cp.Copy(context.Background())
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if len(results) != 1 || results[0].Status != CopyStatusSkipped {
		t.Fatalf("result = %+v", results[0])
	}
	if results[0].Reason != "source missing (pttl=-2)" {
		t.Fatalf("reason = %q", results[0].Reason)
	}
	exists, _ := rdb.Exists(context.Background(), canonical).Result()
	if exists != 0 {
		t.Fatalf("target must not be created for missing source")
	}
}

// TestCopyPreservesNoTTLMinus1: source without TTL must produce a target
// without TTL (PTTL=-1).
func TestCopyPreservesNoTTLMinus1(t *testing.T) {
	_, rdb := newFixtureRedis(t)
	ledger := NewLedger(t.TempDir() + "/ledger.ndjson")
	canonical := migPrefix + "node:k2:YQ:7:Yjo4OmM"
	source := migPrefix + "node:a:7:b:8:c"
	fields := legacyHashFieldsAsStrings()
	seedCopyItem(t, ledger, source, canonical, -1, 1, fields)
	if err := rdb.HSet(context.Background(), source, fields).Err(); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	cp := &Copy{Prefix: migPrefix, Ledger: ledger, RDB: rdb}
	results, err := cp.Copy(context.Background())
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if results[0].Status != CopyStatusCopied {
		t.Fatalf("status = %q, want copied (reason=%s)", results[0].Status, results[0].Reason)
	}
	pttl, err := rdb.PTTL(context.Background(), canonical).Result()
	if err != nil {
		t.Fatalf("pttl: %v", err)
	}
	if pttl >= 0 {
		t.Fatalf("target PTTL = %v, want -1 (no expiry)", pttl)
	}
	tgtFields, _ := rdb.HGetAll(context.Background(), canonical).Result()
	if fieldChecksum(tgtFields) != fieldChecksum(fields) {
		t.Fatal("field checksum diverges")
	}
}

// TestCopyDecrementsPositiveTTL runs copy after miniredis FastForward 200ms
// and asserts the target's PTTL is between 7000ms and 8000ms (snapshot
// recorded 8000ms; copy must not extend beyond it).
func TestCopyDecrementsPositiveTTL(t *testing.T) {
	mr, rdb := newFixtureRedis(t)
	ledger := NewLedger(t.TempDir() + "/ledger.ndjson")
	canonical := migPrefix + "node:k2:YQ:7:Yjo4OmM"
	source := migPrefix + "node:a:7:b:8:c"
	fields := legacyHashFieldsAsStrings()
	seedCopyItem(t, ledger, source, canonical, 8000, 1, fields)
	if err := rdb.HSet(context.Background(), source, fields).Err(); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if err := rdb.PExpire(context.Background(), source, 8000*time.Millisecond).Err(); err != nil {
		t.Fatalf("pexpire: %v", err)
	}

	// miniredis FastForward: deterministic clock. We advance 200ms.
	mr.FastForward(200 * time.Millisecond)

	cp := &Copy{Prefix: migPrefix, Ledger: ledger, RDB: rdb}
	results, err := cp.Copy(context.Background())
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if results[0].Status != CopyStatusCopied {
		t.Fatalf("status = %q, reason=%s", results[0].Status, results[0].Reason)
	}
	pttl, err := rdb.PTTL(context.Background(), canonical).Result()
	if err != nil {
		t.Fatalf("pttl: %v", err)
	}
	got := pttl.Milliseconds()
	if got < 7000 || got > 8000 {
		t.Fatalf("target PTTL = %dms, want in [7000,8000]", got)
	}
	if got > int64(8000) {
		t.Fatalf("copy extended source TTL (%d > 8000)", got)
	}
}

// TestCopyFencedByGenerationMismatch: the live source hash's generation
// differs from the ledger snapshot. copy must refuse, not write the target.
func TestCopyFencedByGenerationMismatch(t *testing.T) {
	_, rdb := newFixtureRedis(t)
	ledger := NewLedger(t.TempDir() + "/ledger.ndjson")
	canonical := migPrefix + "node:k2:YQ:7:Yjo4OmM"
	source := migPrefix + "node:a:7:b:8:c"
	fields := legacyHashFieldsAsStrings()
	seedCopyItem(t, ledger, source, canonical, -1, 1, fields)
	live := copyStringMap(fields)
	live["generation"] = "5"
	if err := rdb.HSet(context.Background(), source, live).Err(); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	cp := &Copy{Prefix: migPrefix, Ledger: ledger, RDB: rdb}
	results, err := cp.Copy(context.Background())
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if results[0].Status != CopyStatusRefused {
		t.Fatalf("status = %q, want refused (reason=%s)", results[0].Status, results[0].Reason)
	}
	exists, _ := rdb.Exists(context.Background(), canonical).Result()
	if exists != 0 {
		t.Fatal("refused copy must not write target")
	}
}

// TestCopyIdempotentResume: copy twice on the same ledger; second pass must
// be CopyStatusUnchanged for each item.
func TestCopyIdempotentResume(t *testing.T) {
	_, rdb := newFixtureRedis(t)
	ledger := NewLedger(t.TempDir() + "/ledger.ndjson")
	canonical := migPrefix + "node:k2:YQ:7:Yjo4OmM"
	source := migPrefix + "node:a:7:b:8:c"
	fields := legacyHashFieldsAsStrings()
	seedCopyItem(t, ledger, source, canonical, -1, 1, fields)
	if err := rdb.HSet(context.Background(), source, fields).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cp := &Copy{Prefix: migPrefix, Ledger: ledger, RDB: rdb}
	if _, err := cp.Copy(context.Background()); err != nil {
		t.Fatalf("first copy: %v", err)
	}
	results, err := cp.Copy(context.Background())
	if err != nil {
		t.Fatalf("second copy: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 ledger rows (initial seed + first copy), got %d", len(results))
	}
	if results[1].Status != CopyStatusUnchanged {
		t.Fatalf("second pass status = %q, want unchanged", results[1].Status)
	}
}

// legacyHashFieldsAsStrings returns the same shape as legacyHashFields() but
// with all values coerced to strings (the form HGetAll returns).
func legacyHashFieldsAsStrings() map[string]string {
	return map[string]string{
		"generation":    "1",
		"available":     "1",
		"fail_streak":   "0",
		"success_count": "5",
		"failure_count": "0",
		"updated_at_ms": "1700000000000",
	}
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
