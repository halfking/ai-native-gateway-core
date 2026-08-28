package requestdetail

import (
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// captureForwarderCapacity bounds the queue between telemetry's
// SetOnRequestLogEmitted hook (running on the request hot-path goroutine)
// and the single consumer goroutine that performs the body marshalling +
// local file write.
//
// 2026-08-28 (audit follow-up): the previous synchronous CaptureFromEntry
// did JSON marshalling + os.WriteFile + os.Rename on the request hot path,
// which can stall request latency when /tmp is slow or filesystem pressure
// is high. The forwarder keeps the emit callback O(1) non-blocking and
// lets a single consumer drain the queue without holding the store's
// process-wide lifecycle mutex longer than necessary.
//
// Capacity tracks live-streamEmittedForwarder (2048) — capture and live
// stream both run off the same emitted hook, so the same queue depth keeps
// drop rates comparable across the two subsystems. Tune via
// SetCaptureForwarderCapacity in tests.
var captureForwarderCapacity = 2048

// captureForwarderDropsLog bounds drop-warning log volume: log every Nth
// drop (mirrors liveStreamEmittedDropsLog in cmd/gateway/main_livestream.go).
const captureForwarderDropsLog = 50

// SetCaptureForwarderCapacity overrides the queue depth for tests. Must be
// called before the forwarder starts to take effect; later changes are
// ignored. Pass <= 0 to restore the production default.
func SetCaptureForwarderCapacity(n int) {
	if n <= 0 {
		captureForwarderCapacity = 2048
		return
	}
	captureForwarderCapacity = n
}

// captureForwarderStats holds lightweight counters for the forwarder.
// Snapshotted via Stats() (no locks on hot path). Exposed for
// diagnostics; not on a critical metric path.
type captureForwarderStats struct {
	Enqueued    atomic.Uint64 // entries successfully enqueued
	Processed   atomic.Uint64 // entries successfully captured
	Dropped     atomic.Uint64 // entries evicted because queue was full
	CaptureFail atomic.Uint64 // entries where store.Put returned an error
}

// captureForwarder bridges telemetry's onEmitted hook to the request-detail
// Store. The emit callback MUST stay non-blocking; marshalling and file I/O
// happen on the consumer goroutine.
//
// 2026-08-28 (audit follow-up, async body capture): rationale and contract:
//   - SetOnRequestLogEmitted runs on the request hot-path goroutine BEFORE
//     the entry enters the telemetry DB queue. The previous synchronous
//     CaptureFromEntry did JSON marshalling + os.WriteFile + os.Rename under
//     the Store lifecycle lock, which is O(body size) and stalls request
//     latency under burst + slow /tmp.
//   - emit() does a shallow copy of the entry's pointer fields and a
//     non-blocking chan send. Telemetry worker may later mutate the entry
//     in place (sanitize / canonicalize); the shallow copy pins the values
//     we captured to "emit-time", avoiding races where the consumer reads
//     partly-overwritten pointers.
//   - On queue overflow, the OLDEST entry is evicted (FIFO drop) rather
//     than dropping the incoming entry. Old in-flight captures are useless
//     once the request has moved on; new captures carry the latest state.
//   - run() is single-consumer, so JSON marshalling + file I/O for the
//     same request_id are serialized — this matches the Store's existing
//     correctness model (per-request_id sequential via the lifecycle lock).
type captureForwarder struct {
	store    *Store
	entries  chan *telemetry.RequestLogEntry
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
	started  atomic.Bool
	stopped  atomic.Bool
	stats    captureForwarderStats
}

// newCaptureForwarder wires a forwarder for store. The consumer goroutine
// must be started via run() in the same call site that wires telemetry.
func newCaptureForwarder(store *Store) *captureForwarder {
	return &captureForwarder{
		store:   store,
		entries: make(chan *telemetry.RequestLogEntry, captureForwarderCapacity),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
}

// emit is the SetOnRequestLogEmitted callback. Runs synchronously on the
// request hot-path goroutine; MUST stay non-blocking.
//
// We shallow-copy the entry so the consumer reads the emit-time values
// even if the telemetry worker later mutates the original entry's pointer
// fields. The bodies (RequestBody / ResponseBody / OutboundBody) are
// *string / []byte — copying the pointers pins the underlying bytes for
// the consumer's lifetime. Sanitize later reassigns the source's pointer
// fields (e.g. `*raw = json.RawMessage(cleaned)`); the shallow copy
// preserves the OLD pointer values, so we still read the pre-sanitize
// bytes. Sanitize does NOT mutate the bytes themselves.
//
// 2026-08-29 (audit follow-up):
//   - Pre-enqueue body-size guard: any entry whose aggregate body size
//     exceeds MaxBodyFileSize is dropped BEFORE enqueue to bound the
//     queue's worst-case memory at capacity × MaxBodyFileSize. A single
//     100MB body would otherwise consume 100MB of queue buffer per
//     enqueue attempt (and 2048 × 100MB = 200GB worst case).
//   - Empty RequestID now logs a slog.Warn instead of silently returning,
//     so misconfigured callers are visible.
//   - runtime.Gosched() between retry iterations keeps the request hot
//     path responsive under sustained queue pressure (bounded CPU usage).
func (f *captureForwarder) emit(entry *telemetry.RequestLogEntry) {
	if f == nil || entry == nil {
		return
	}
	if f.stopped.Load() {
		return
	}
	if entry.RequestID == "" {
		slog.Warn("requestdetail: capture forwarder skipping entry with empty RequestID",
			"tenant_id", entry.TenantID,
			"client_model", derefString(entry.ClientModel))
		return
	}
	if n := oversizeEntryBodyBytes(entry); n > MaxBodyFileSize {
		storeForwarderDroppedOversizeTotal.Inc()
		slog.Warn("requestdetail: capture forwarder dropping oversize entry",
			"request_id", entry.RequestID,
			"tenant_id", entry.TenantID,
			"size_bytes", n,
			"limit_bytes", MaxBodyFileSize)
		return
	}
	cp := *entry
	for {
		select {
		case f.entries <- &cp:
			f.stats.Enqueued.Add(1)
			return
		default:
		}
		// Queue full: evict oldest entry. The incoming entry must always be
		// accepted (it carries the latest state); losing an older in-flight
		// capture is acceptable because subsequent events overwrite it.
		select {
		case evicted := <-f.entries:
			if n := f.stats.Dropped.Add(1); n%captureForwarderDropsLog == 1 {
				slog.Warn("requestdetail: capture forwarder queue full, evicted oldest entry",
					"dropped", n,
					"evicted_request_id", evicted.RequestID,
					"incoming_request_id", entry.RequestID,
				)
			}
		default:
			// Another consumer drained between the failed send and the
			// receive attempt — retry the send. Yield the goroutine so
			// the request hot path doesn't burn CPU under sustained
			// queue pressure; the consumer's pace will catch up within
			// microseconds in any non-pathological case.
			runtime.Gosched()
		}
	}
}

// oversizeEntryBodyBytes reports the aggregate body size of an entry,
// summing RequestBody + ResponseBody + OutboundBody. Used at emit() to
// bound queue memory. Returns 0 when all body fields are nil/empty
// (zero is always safe and never rejected).
func oversizeEntryBodyBytes(entry *telemetry.RequestLogEntry) int {
	if entry == nil {
		return 0
	}
	var n int
	if entry.RequestBody != nil {
		n += len(*entry.RequestBody)
	}
	if entry.ResponseBody != nil {
		n += len(*entry.ResponseBody)
	}
	n += len(entry.OutboundBody)
	return n
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// run is the single consumer goroutine started by main(). Performs the
// body marshalling + Store.Put on a non-hot-path goroutine. Exits when
// stopCh is closed; pending entries are NOT drained (matches live stream
// forwarder's shutdown contract — losing in-flight projections on a clean
// shutdown is acceptable because the DB write is the durable record).
func (f *captureForwarder) run() {
	if f == nil || !f.started.CompareAndSwap(false, true) {
		return
	}
	f.runStarted()
}

func (f *captureForwarder) runStarted() {
	if f == nil {
		return
	}
	defer close(f.doneCh)
	for {
		select {
		case e := <-f.entries:
			f.captureOne(e)
		case <-f.stopCh:
			for {
				select {
				case e := <-f.entries:
					f.captureOne(e)
				default:
					return
				}
			}
		}
	}
}

// captureOne is the per-entry consumer handler. Mirrors the body of the
// previous synchronous CaptureFromEntry, with all I/O now off the request
// hot path.
func (f *captureForwarder) captureOne(entry *telemetry.RequestLogEntry) {
	if f.store == nil {
		return
	}
	meta := Meta{
		RequestID:   entry.RequestID,
		TenantID:    entry.TenantID,
		GwSessionID: entry.GwSessionID,
		GwTaskID:    entry.GwTaskID,
		ClientModel: entry.ClientModel,
		Status:      entry.RequestStatus,
		LatencyMs:   entry.LatencyMs,
	}
	success := entry.Success
	meta.Success = &success

	var bodies *Bodies
	if entry.RequestBody != nil || entry.ResponseBody != nil || len(entry.OutboundBody) > 0 {
		bodies = &Bodies{}
		if entry.RequestBody != nil {
			bodies.RequestBody = DecodeRaw(entry.RequestBody)
		}
		if entry.ResponseBody != nil {
			bodies.ResponseBody = DecodeRaw(entry.ResponseBody)
		}
		if len(entry.OutboundBody) > 0 {
			bodies.OutboundBody = append([]byte(nil), entry.OutboundBody...)
		}
	}
	if err := f.store.Put(meta, bodies); err != nil {
		f.stats.CaptureFail.Add(1)
		slog.Warn("requestdetail: capture failed",
			"request_id", entry.RequestID,
			"tenant_id", entry.TenantID,
			"error", err,
		)
		return
	}
	f.stats.Processed.Add(1)
}

// stop closes stopCh exactly once. Idempotent. 2026-08-29 (audit
// follow-up): the previous implementation blocked on `<-f.doneCh`
// unconditionally, which could hang the gateway shutdown if the
// consumer was stuck in Store.Put on a slow /tmp. The bounded wait
// below gives the consumer stopGraceTimeout to drain before the
// process exits; if it exceeds the window, we log and proceed so the
// rest of the shutdown sequence (telemetry, DB pools, listeners) can
// still complete.
func (f *captureForwarder) stop() {
	if f == nil || f.stopped.Swap(true) {
		return
	}
	f.stopOnce.Do(func() { close(f.stopCh) })
	if !f.started.Load() {
		return
	}
	select {
	case <-f.doneCh:
	case <-time.After(stopGraceTimeout):
		slog.Warn("requestdetail: capture forwarder stop timed out; consumer may be stuck in file I/O",
			"timeout", stopGraceTimeout,
			"queue_len", len(f.entries))
	}
}

// Stats snapshots the forwarder counters. Used by tests and admin
// diagnostics.
func (f *captureForwarder) Stats() (enqueued, processed, dropped, captureFail uint64) {
	if f == nil {
		return 0, 0, 0, 0
	}
	return f.stats.Enqueued.Load(), f.stats.Processed.Load(),
		f.stats.Dropped.Load(), f.stats.CaptureFail.Load()
}

// drainTimeout bounds the wait for a queued entry to land in the store in
// tests; production code never calls this.
const drainTimeout = 2 * time.Second

// stopGraceTimeout caps how long stop() waits for the consumer goroutine
// to drain. If the consumer is stuck in Store.Put (slow /tmp, etc.), the
// gateway shutdown must not hang past this window. Exceeding the timeout
// is logged so operators can correlate slow-FS incidents with shutdown
// delays.
const stopGraceTimeout = 5 * time.Second

// drainForTest blocks until either the queued count reaches 0 (best effort)
// or drainTimeout elapses. Test-only helper.
func (f *captureForwarder) drainForTest(timeout time.Duration) {
	if f == nil {
		return
	}
	if timeout <= 0 {
		timeout = drainTimeout
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(f.entries) == 0 {
			// Give the consumer a moment to finish the in-flight capture.
			time.Sleep(10 * time.Millisecond)
			if len(f.entries) == 0 {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
}
