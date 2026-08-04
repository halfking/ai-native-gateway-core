// Package integrity — stream_tracker.go
//
// 2026-08-02: incremental (mid-stream) repeated-content detection.
//
// The completion-time Detector.Observe pass only sees the finished
// textContent, so a model stuck in a loop burns the full max_tokens
// budget before anything is recorded. StreamTracker applies the *same*
// rule as Detector.checkRepeatedContent — sha256 over 256-byte aligned
// blocks, a block seen minRepeatedHits times is a repeat — to the text
// as it streams, so the event is recorded early and (optionally) the
// stream is cut.
//
// Two thresholds, deliberately different:
//
//	record  (default 2 hits) — parity with the final pass, so an
//	                           incrementally-detected loop produces the
//	                           same event the final pass would have.
//	abort   (default 8 hits) — stricter, because cutting a paying
//	                          request on a false positive is worse than
//	                          letting it finish. 256-byte aligned blocks
//	                          repeat legitimately in code indentation,
//	                          Markdown table rules and base64, so the
//	                          abort is also off by default.
//
// Configuration (env, read once at construction):
//
//	LLM_GATEWAY_INTEGRITY_STREAM_ABORT=false        // cut the stream on breach
//	LLM_GATEWAY_INTEGRITY_STREAM_ABORT_MIN_HITS=4   // hits required to cut
//	LLM_GATEWAY_INTEGRITY_STREAM_RECORD_MIN_HITS=2  // hits required to flag
package integrity

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"sync"
)

// maxTrackedBlocks caps the hash table so a multi-megabyte stream cannot
// grow it without bound. 2 MiB of text at 256 bytes per block is 8192
// blocks; we stop inserting new hashes past this point but keep counting
// hits on the blocks already tracked, which is what the repeat rule
// needs (a loop revisits blocks it has already produced).
const maxTrackedBlocks = 8192

// StreamTrackerConfig holds the resolved incremental thresholds.
type StreamTrackerConfig struct {
	// AbortEnabled cuts the stream when AbortMinHits is reached.
	AbortEnabled bool
	// AbortMinHits is the block hit count that triggers the abort.
	AbortMinHits int
	// RecordMinHits is the block hit count that flags the event for the
	// completion-time recorder. Should match the final pass (2).
	RecordMinHits int
}

// DefaultStreamTrackerConfig reads the env overrides once. Invalid or
// absent values fall back to the safe defaults described above.
//
// 2026-08-04: the abort default was raised 4 → 8. The 256-byte aligned block
// rule matches blocks *anywhere* in the stream (not just consecutively), so
// legitimate agent output — repeated JSON keys in tool-call arguments, code
// indentation, Markdown table rules, base64 payloads — routinely produces
// 4–7 incidental matches of an identical block. Cutting a paying request on
// those false positives caused the "agent task interrupted through the
// gateway" symptom. A real model loop revisits the same block dozens of
// times, so 8 still catches genuine loops with margin. Abort remains OFF by
// default; this only affects deployments that opt in.
func DefaultStreamTrackerConfig() StreamTrackerConfig {
	cfg := StreamTrackerConfig{
		AbortEnabled:  os.Getenv("LLM_GATEWAY_INTEGRITY_STREAM_ABORT") == "true",
		AbortMinHits:  envIntAtLeast("LLM_GATEWAY_INTEGRITY_STREAM_ABORT_MIN_HITS", 8, 2),
		RecordMinHits: envIntAtLeast("LLM_GATEWAY_INTEGRITY_STREAM_RECORD_MIN_HITS", 2, 2),
	}
	if cfg.AbortMinHits < cfg.RecordMinHits {
		// An abort threshold below the record threshold would cut the
		// stream before the event is flagged; clamp so the recorded
		// event always exists when we abort.
		cfg.AbortMinHits = cfg.RecordMinHits
	}
	return cfg
}

func envIntAtLeast(key string, def, min int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min {
		return def
	}
	return n
}

// StreamTracker implements audit.StreamTextObserver. One instance per
// request; safe for concurrent use even though the capture serialises
// ObserveText under its own mutex (the completion path reads the result
// from another goroutine).
type StreamTracker struct {
	cfg StreamTrackerConfig

	mu sync.Mutex
	// pending holds bytes that did not fill a whole block yet. Blocks are
	// cut at a fixed 256-byte stride across the whole stream so the
	// alignment matches the final pass, which slices textContent from
	// offset 0.
	pending []byte
	counts  map[string]int
	blocks  int
	// flagged latches the first block that crossed RecordMinHits, so the
	// event is recorded once per request no matter how long the loop runs.
	flagged     bool
	flaggedHash string
	flaggedHits int
}

// NewStreamTracker builds a tracker with the supplied config.
func NewStreamTracker(cfg StreamTrackerConfig) *StreamTracker {
	return &StreamTracker{cfg: cfg, counts: make(map[string]int, 16)}
}

// ObserveText consumes appended assistant text and reports whether the
// stream should be aborted. Returns false when aborting is disabled, so
// detection still records the event without changing stream behavior.
func (t *StreamTracker) ObserveText(s string) bool {
	if t == nil || s == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	t.pending = append(t.pending, s...)
	abort := false
	for len(t.pending) >= repeatedBlockBytes {
		block := t.pending[:repeatedBlockBytes]
		t.blocks++
		h := sha256.Sum256(block)
		key := hex.EncodeToString(h[:8])
		if _, seen := t.counts[key]; seen || len(t.counts) < maxTrackedBlocks {
			t.counts[key]++
			n := t.counts[key]
			if n >= t.cfg.RecordMinHits && !t.flagged {
				t.flagged = true
				t.flaggedHash = key
				t.flaggedHits = n
			}
			if t.flagged && key == t.flaggedHash {
				// Keep the recorded hit count current while the loop runs
				// so the event reflects how long it went on.
				t.flaggedHits = n
			}
			if t.cfg.AbortEnabled && n >= t.cfg.AbortMinHits {
				abort = true
			}
		}
		// Advance by a full block; copy down rather than reslice so the
		// backing array does not grow across a long stream.
		t.pending = append(t.pending[:0], t.pending[repeatedBlockBytes:]...)
	}
	return abort
}

// BreachReason implements audit.StreamTextObserver.
func (t *StreamTracker) BreachReason() string {
	return "integrity_repeated_content"
}

// RepeatedContentHash returns the latched finding for the completion
// path. ok is false when nothing crossed RecordMinHits.
func (t *StreamTracker) RepeatedContentHash() (string, int, int, int, bool) {
	if t == nil {
		return "", 0, 0, 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.flagged {
		return "", 0, 0, 0, false
	}
	return t.flaggedHash, t.flaggedHits, repeatedBlockBytes, t.blocks, true
}

// Reset clears all state for a fresh failover attempt.
func (t *StreamTracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = t.pending[:0]
	t.counts = make(map[string]int, 16)
	t.blocks = 0
	t.flagged = false
	t.flaggedHash = ""
	t.flaggedHits = 0
}
