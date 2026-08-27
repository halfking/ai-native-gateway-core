package dbdegradation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/metrics"
)

// RingBufferStats is a snapshot of RingBuffer state for monitoring
// and the HTTP /internal/telemetry/fallback-buffer/stats endpoint.
type RingBufferStats struct {
	Capacity int    `json:"capacity"` // configured max entries (cap)
	Size     int    `json:"size"`     // currently held entries (≤ Capacity)
	Writes   uint64 `json:"writes"`   // total successful WriteRequestLog/WriteRequestWAL calls
	Dropped  uint64 `json:"dropped"`  // entries overwritten because buffer was full (monotonic)
}

// RingBuffer is an in-memory circular buffer of BackupRecord, designed
// to capture telemetry request_log entries that failed to persist to
// the primary DB. It implements the BackupWriter interface so it can
// be wired alongside the disk-based FileWriter in cmd/gateway/main.go.
//
// When DB writes fail, the worker calls BackupWriter.WriteRequestLog
// and warns "telemetry request db persist failed; fallback written".
// With the ring buffer wired, those failures are now also retained in
// RAM for online dump / replay via HTTP endpoints.
//
// Concurrency:
//   - Writes, Reads (Dump/Stats), Clear, and Replay are all serialized
//     by rb.mu. This matches FileWriter's locking style and is sufficient
//     because none of these paths is on the request hot path (telemetry
//     worker batches at 50 entries / 200ms).
//   - Dropped and Writes counters are updated atomically so they can be
//     read by Stats() without taking the lock.
type RingBuffer struct {
	mu      sync.Mutex
	cap     int
	buf     []BackupRecord
	head    int // next write position (0..cap-1)
	size    int // number of valid entries (0..cap)
	writes  uint64
	dropped uint64
}

// NewRingBuffer creates a RingBuffer with the given capacity.
// cap <= 0 is normalized to 1 so the buffer is still usable.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &RingBuffer{
		cap: capacity,
		buf: make([]BackupRecord, capacity),
	}
}

// WriteGeneric writes a generic record. Satisfies BackupWriter (via
// WriteRequestLog / WriteRequestWAL which delegate here).
func (rb *RingBuffer) WriteGeneric(ctx context.Context, recordType, key string, payload any) error {
	if rb == nil {
		return fmt.Errorf("ring buffer not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal fallback payload: %w", err)
	}
	rb.push(BackupRecord{
		Type:      recordType,
		Timestamp: time.Now().UTC(),
		RecordKey: key,
		Payload:   raw,
	})
	return nil
}

// WriteRequestLog is the BackupWriter entry point for request_log entries.
func (rb *RingBuffer) WriteRequestLog(ctx context.Context, key string, payload any) error {
	return rb.WriteGeneric(ctx, "request_log", key, payload)
}

// WriteRequestWAL is the BackupWriter entry point for request_wal entries.
func (rb *RingBuffer) WriteRequestWAL(ctx context.Context, key string, payload any) error {
	return rb.WriteGeneric(ctx, "request_wal", key, payload)
}

// push inserts a record at head, overwriting the oldest when full.
// MUST be called with rb.mu held, OR by code paths that serialize
// internally (WriteGeneric above takes no lock; instead it serializes
// via rb.mu inside this method).
func (rb *RingBuffer) push(rec BackupRecord) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.pushLocked(rec)
}

// pushLocked writes one record assuming rb.mu is already held. Kept
// separate from push() because Replay() requeues failed records in a batch
// while already holding the lock — a self-locking push would deadlock.
func (rb *RingBuffer) pushLocked(rec BackupRecord) {
	if rb.size == rb.cap {
		// Buffer is full — about to overwrite the oldest entry.
		atomic.AddUint64(&rb.dropped, 1)
		// P0-2 (audit §3.6 R-3.4): surface this on Prometheus so the
		// ringbuffer_dropped_total rule in
		// deploy/monitoring/grafana-alerts/shadow-write-failures.yaml
		// can fire. CRITICAL: these rows are LOST (not deferred to disk
		// or replay). Call from outside the mu would also be safe — the
		// counter is a Prometheus Counter.Add, atomic in prometheus client.
		// We deliberately call while mu is held to preserve the invariant
		// that "every +1 in dropped is paired with exactly one +1 here".
		metrics.Global().RecordRingBufferDropped(1)
	}
	rb.buf[rb.head] = rec
	rb.head = (rb.head + 1) % rb.cap
	if rb.size < rb.cap {
		rb.size++
	}
	atomic.AddUint64(&rb.writes, 1)
}

// Dump returns a copy of all valid entries in FIFO order (oldest first).
// The returned slice is independent of the internal buffer; mutating it
// does not affect the ring buffer. Safe to call concurrently with writes.
func (rb *RingBuffer) Dump() []BackupRecord {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if rb.size == 0 {
		return []BackupRecord{}
	}
	out := make([]BackupRecord, rb.size)
	// FIFO order: oldest is at (head - size + cap) % cap
	start := (rb.head - rb.size + rb.cap) % rb.cap
	for i := 0; i < rb.size; i++ {
		out[i] = rb.buf[(start+i)%rb.cap]
	}
	return out
}

// Stats returns a snapshot of RingBuffer counters.
func (rb *RingBuffer) Stats() RingBufferStats {
	rb.mu.Lock()
	size := rb.size
	cap := rb.cap
	rb.mu.Unlock()
	return RingBufferStats{
		Capacity: cap,
		Size:     size,
		Writes:   atomic.LoadUint64(&rb.writes),
		Dropped:  atomic.LoadUint64(&rb.dropped),
	}
}

// Clear resets the buffer. The backing slice is reused (not reallocated)
// so subsequent writes have stable memory addresses. Dropped is monotonic
// (deliberately NOT reset) so monitoring alerts can still see historical
// overflow.
func (rb *RingBuffer) Clear() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	for i := range rb.buf {
		rb.buf[i] = BackupRecord{}
	}
	rb.head = 0
	rb.size = 0
}

// ReplayFn is the function signature for replaying one BackupRecord.
// Returning an error is treated as a failure for that entry; the caller
// decides what to do (currently: log only, do NOT requeue).
type ReplayFn func(ctx context.Context, record BackupRecord) error

// Replay invokes fn on each buffer entry (oldest first) and clears the
// buffer as it goes. limit caps how many entries are replayed; 0 means
// "all". Failed entries are NOT requeued (avoid loops when DB is still
// down) — they're surfaced via the failed count and slog.Warn only.
//
// Returns (replayed, failed, error). error is non-nil only for
// context-cancellation / unrecoverable plumbing failures; per-entry
// replay errors are reflected in `failed`.
func (rb *RingBuffer) Replay(ctx context.Context, limit int, fn ReplayFn) (int, int, error) {
	if rb == nil || fn == nil {
		return 0, 0, nil
	}

	// Snapshot under lock so we don't hold the lock during fn calls
	// (fn may do I/O like DB writes that could deadlock or starve writers).
	rb.mu.Lock()
	if rb.size == 0 {
		rb.mu.Unlock()
		return 0, 0, nil
	}
	n := rb.size
	if limit > 0 && limit < n {
		n = limit
	}
	snapshot := make([]BackupRecord, n)
	start := (rb.head - rb.size + rb.cap) % rb.cap
	for i := 0; i < n; i++ {
		snapshot[i] = rb.buf[(start+i)%rb.cap]
	}
	// Pop the replayed entries from the buffer so callers don't see them again.
	if limit > 0 && limit < rb.size {
		// Partial replay — drop just the replayed prefix. The remaining
		// entries are already contiguous starting at newHead (elements
		// start+limit .. start+size-1 == newHead .. newHead+newSize-1),
		// so no compaction copy is needed; the previous copy loop read
		// past the valid region and overwrote survivors with garbage.
		newSize := rb.size - limit
		newHead := (rb.head - rb.size + rb.cap + limit) % rb.cap
		rb.size = newSize
		rb.head = (newHead + newSize) % rb.cap
	} else {
		// Full drain.
		for i := range rb.buf {
			rb.buf[i] = BackupRecord{}
		}
		rb.head = 0
		rb.size = 0
	}
	rb.mu.Unlock()

	var replayed, failed int
	var failedRecords []BackupRecord
	for i, rec := range snapshot {
		if err := ctx.Err(); err != nil {
			// Unreplayed remainder goes back so ctx cancellation does not
			// silently drop WAL records either.
			failedRecords = append(failedRecords, snapshot[i:]...)
			break
		}
		if err := fn(ctx, rec); err != nil {
			failed++
			slog.Warn("telemetry fallback buffer replay failed",
				"record_key", rec.RecordKey,
				"record_type", rec.Type,
				"error", err)
			// Requeue on failure: these may be WAL records whose loss is
			// not acceptable. Replay is only ever triggered manually via
			// the /replay endpoint (no automatic loop), so requeueing
			// cannot spin. When the DB recovers the operator replays again.
			failedRecords = append(failedRecords, rec)
			continue
		}
		replayed++
	}
	if len(failedRecords) > 0 {
		rb.mu.Lock()
		for _, rec := range failedRecords {
			rb.pushLocked(rec)
		}
		rb.mu.Unlock()
	}
	return replayed, failed, nil
}
