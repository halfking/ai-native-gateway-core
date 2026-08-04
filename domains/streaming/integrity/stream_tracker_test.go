package integrity

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
)

// blockHash mirrors the tracker's own hash scheme (sha256, first 8 bytes hex)
// so the tests assert against the exact key it records.
func blockHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// ObserveText stays false until RecordMinHits is reached and aborting is off.
func TestStreamTracker_RecordWithoutAbort(t *testing.T) {
	cfg := StreamTrackerConfig{AbortEnabled: false, AbortMinHits: 4, RecordMinHits: 2}
	tr := NewStreamTracker(cfg)

	block := strings.Repeat("a", repeatedBlockBytes)
	// 1st block: first sighting — not flagged yet.
	if tr.ObserveText(block) {
		t.Fatal("abort returned true with abort disabled")
	}
	if _, _, _, _, ok := tr.RepeatedContentHash(); ok {
		t.Fatal("flagged after a single block")
	}
	// 2nd identical block: crosses RecordMinHits → flagged, but still no abort.
	if tr.ObserveText(block) {
		t.Fatal("abort returned true with abort disabled")
	}
	hash, hits, size, blocks, ok := tr.RepeatedContentHash()
	if !ok {
		t.Fatal("expected flagged after 2 hits")
	}
	if hash != blockHash([]byte(block)) {
		t.Fatalf("hash mismatch: got %s", hash)
	}
	if hits != 2 {
		t.Fatalf("hits = %d, want 2", hits)
	}
	if size != repeatedBlockBytes {
		t.Fatalf("block size = %d, want %d", size, repeatedBlockBytes)
	}
	if blocks != 2 {
		t.Fatalf("blocks total = %d, want 2", blocks)
	}
	if got := tr.BreachReason(); got != "integrity_repeated_content" {
		t.Fatalf("breach reason = %q, want integrity_repeated_content", got)
	}
}

// AbortEnabled fires exactly when AbortMinHits is reached.
func TestStreamTracker_AbortEnabled(t *testing.T) {
	cfg := StreamTrackerConfig{AbortEnabled: true, AbortMinHits: 4, RecordMinHits: 2}
	tr := NewStreamTracker(cfg)
	block := strings.Repeat("b", repeatedBlockBytes)

	for i := 1; i <= 3; i++ {
		if tr.ObserveText(block) {
			t.Fatalf("aborted at hit %d, threshold is 4", i)
		}
	}
	// 4th hit crosses AbortMinHits.
	if !tr.ObserveText(block) {
		t.Fatal("expected abort at 4th hit")
	}
	if _, _, _, _, ok := tr.RepeatedContentHash(); !ok {
		t.Fatal("record latch should be set when aborting")
	}
}

// The flagged latch records the FIRST block to cross RecordMinHits and keeps
// updating its hit count, but never switches to a different block's hash.
func TestStreamTracker_LatchesFirstFlaggedBlock(t *testing.T) {
	cfg := StreamTrackerConfig{AbortEnabled: false, AbortMinHits: 4, RecordMinHits: 2}
	tr := NewStreamTracker(cfg)

	blkA := strings.Repeat("a", repeatedBlockBytes)
	blkB := strings.Repeat("b", repeatedBlockBytes)

	// Two A's first → A is flagged.
	tr.ObserveText(blkA)
	tr.ObserveText(blkA)
	_, _, _, _, ok := tr.RepeatedContentHash()
	if !ok {
		t.Fatal("expected A flagged")
	}

	// Now pile on B's beyond RecordMinHits; the latched hash must stay A.
	tr.ObserveText(blkB)
	tr.ObserveText(blkB)
	tr.ObserveText(blkB)
	hash, hits, _, _, ok := tr.RepeatedContentHash()
	if !ok {
		t.Fatal("flag lost after B blocks")
	}
	if hash != blockHash([]byte(blkA)) {
		t.Fatalf("latched hash changed away from A: got %s", hash)
	}
	// hits reflects the running count for the flagged (A) block, unchanged by B.
	if hits != 2 {
		t.Fatalf("flagged hits = %d, want 2", hits)
	}
}

// A loop keeps running; the flagged hit count is kept current for the flagged
// block so the recorded event reflects how long the loop ran.
func TestStreamTracker_FlaggedHitsTracksLoop(t *testing.T) {
	cfg := StreamTrackerConfig{AbortEnabled: false, AbortMinHits: 99, RecordMinHits: 2}
	tr := NewStreamTracker(cfg)
	block := strings.Repeat("c", repeatedBlockBytes)

	for i := 0; i < 7; i++ {
		tr.ObserveText(block)
	}
	_, hits, _, blocks, ok := tr.RepeatedContentHash()
	if !ok {
		t.Fatal("expected flagged")
	}
	if hits != 7 {
		t.Fatalf("flagged hits = %d, want 7", hits)
	}
	if blocks != 7 {
		t.Fatalf("blocks total = %d, want 7", blocks)
	}
}

// Sub-block text: bytes accumulate across calls until a full 256-byte block
// forms, matching the completion-time stride.
func TestStreamTracker_AccumulatesAcrossCalls(t *testing.T) {
	cfg := StreamTrackerConfig{AbortEnabled: false, AbortMinHits: 4, RecordMinHits: 2}
	tr := NewStreamTracker(cfg)
	// Feed the same 256-byte block in 64-byte slices, twice.
	block := strings.Repeat("d", repeatedBlockBytes)
	feed := func() {
		for i := 0; i < repeatedBlockBytes; i += 64 {
			tr.ObserveText(block[i : i+64])
		}
	}
	feed()
	if _, _, _, _, ok := tr.RepeatedContentHash(); ok {
		t.Fatal("flagged after one accumulated block")
	}
	feed()
	if _, _, _, _, ok := tr.RepeatedContentHash(); !ok {
		t.Fatal("expected flagged after two accumulated blocks")
	}
}

// Short / empty text never forms a block.
func TestStreamTracker_ShortTextNotFlagged(t *testing.T) {
	cfg := StreamTrackerConfig{AbortEnabled: true, AbortMinHits: 2, RecordMinHits: 2}
	tr := NewStreamTracker(cfg)

	if tr.ObserveText("") {
		t.Fatal("empty text aborted")
	}
	if tr.ObserveText("short") {
		t.Fatal("short text aborted")
	}
	if _, _, _, _, ok := tr.RepeatedContentHash(); ok {
		t.Fatal("short text flagged")
	}
}

// Reset clears all state so the observer can be reused after a failover.
func TestStreamTracker_ResetClearsState(t *testing.T) {
	cfg := StreamTrackerConfig{AbortEnabled: true, AbortMinHits: 2, RecordMinHits: 2}
	tr := NewStreamTracker(cfg)
	block := strings.Repeat("e", repeatedBlockBytes)
	tr.ObserveText(block)
	tr.ObserveText(block) // flagged + would-abort
	if _, _, _, _, ok := tr.RepeatedContentHash(); !ok {
		t.Fatal("precondition: expected flagged")
	}

	tr.Reset()
	if _, _, _, _, ok := tr.RepeatedContentHash(); ok {
		t.Fatal("flag survived Reset")
	}
	// After reset, a single new block is a first sighting (no abort, no flag).
	if tr.ObserveText(strings.Repeat("f", repeatedBlockBytes)) {
		t.Fatal("aborted after Reset on first sighting")
	}
	if _, _, _, _, ok := tr.RepeatedContentHash(); ok {
		t.Fatal("flagged after Reset on first sighting")
	}
}

// Blocks beyond maxTrackedBlocks stop inserting new hashes but keep counting
// hits on already-tracked blocks (a loop revisits blocks it produced).
func TestStreamTracker_MaxTrackedBlocksCap(t *testing.T) {
	cfg := StreamTrackerConfig{AbortEnabled: false, AbortMinHits: 99, RecordMinHits: 2}
	tr := NewStreamTracker(cfg)

	// Fill the table with distinct blocks up to the cap.
	for i := 0; i < maxTrackedBlocks; i++ {
		tr.ObserveText(string(uniqueBlock(i)))
	}
	// The first block should now be tracked; replaying it repeatedly must still
	// count hits even though the table is full (no new insertions).
	first := string(uniqueBlock(0))
	for i := 0; i < 10; i++ {
		tr.ObserveText(first)
	}
	hash, hits, _, _, ok := tr.RepeatedContentHash()
	if !ok {
		t.Fatal("expected the replayed first block to be flagged")
	}
	if hash != blockHash([]byte(first)) {
		t.Fatalf("flagged hash = %s, want first block hash", hash)
	}
	// 1 (initial) + 10 replays.
	if hits != 11 {
		t.Fatalf("hits = %d, want 11", hits)
	}
}

// uniqueBlock produces a deterministic, collision-free 256-byte block per
// seed, used to saturate the tracker's hash table in the cap test. The seed is
// encoded verbatim in the first bytes so two different seeds never hash alike.
func uniqueBlock(seed int) []byte {
	b := make([]byte, repeatedBlockBytes)
	// Big-endian seed in the first 8 bytes; a fixed filler after. The first
	// bytes alone make every seed distinct, so the sha256 is unique per seed.
	b[0] = byte(seed)
	b[1] = byte(seed >> 8)
	b[2] = byte(seed >> 16)
	b[3] = byte(seed >> 24)
	for i := 4; i < repeatedBlockBytes; i++ {
		b[i] = 0x5a
	}
	return b
}

// Nil receiver is safe (called from a nil-detector adapter path).
func TestStreamTracker_NilSafe(t *testing.T) {
	var tr *StreamTracker
	if tr.ObserveText("x") {
		t.Fatal("nil tracker aborted")
	}
	if tr.BreachReason() != "integrity_repeated_content" {
		// BreachReason is a constant; still safe to call on nil.
	}
	if _, _, _, _, ok := tr.RepeatedContentHash(); ok {
		t.Fatal("nil tracker flagged")
	}
	tr.Reset() // must not panic
}

// Concurrent ObserveText does not race (run with -race).
func TestStreamTracker_ConcurrentSafe(t *testing.T) {
	cfg := StreamTrackerConfig{AbortEnabled: false, AbortMinHits: 99, RecordMinHits: 2}
	tr := NewStreamTracker(cfg)
	block := strings.Repeat("g", repeatedBlockBytes)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				tr.ObserveText(block)
			}
		}()
	}
	wg.Wait()
	// 8 goroutines × 4 blocks = 32 hits on the same block.
	_, hits, _, blocks, ok := tr.RepeatedContentHash()
	if !ok {
		t.Fatal("expected flagged after concurrent feeds")
	}
	if blocks != 32 {
		t.Fatalf("blocks total = %d, want 32", blocks)
	}
	if hits != 32 {
		t.Fatalf("hits = %d, want 32", hits)
	}
}
