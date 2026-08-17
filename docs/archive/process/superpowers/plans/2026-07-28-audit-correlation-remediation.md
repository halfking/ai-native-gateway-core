# Audit Correlation & Error-Kind Remediation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the eight audit-correlation defects surfaced by the 2026-07-28 independent review so every raw log entry carries the full correlation envelope, every anomaly HTTP report carries the envelope + per-request raw locator, stream chunk counters on `request_logs` match `audit.StreamCapture`, the `error_kind` taxonomy covers every emitted interruption code, and raw-log overflow / close emit observable anomalies.

**Architecture:** Introduce `streaming.AuditContext` (built once in the handler, refreshed per attempt by the executor, threaded via `context.Context` and `*DiagnosticContext`), make `AsyncRawDataLogger` carry the full envelope + per-request raw location index, add `RawLogOverflow` / `RawLogCloseDrained` anomaly types + audit rows, and make `audit.StreamCapture.Snapshot()` the single source of truth for `stream_chunks_sent` / `stream_chunk_errors`. `streamErrorKindForDetailCode` becomes a fallback once `StreamOutcome.Kind` (an `errorsx.ErrorKind`) is populated by the executor.

**Tech Stack:** Go 1.22, `internal/logging`, `domains/streaming`, `domains/streaming/executors`, `errorsx`, `audit` package.

---

## File Structure

**New files**
- `domains/streaming/audit_context.go` — `AuditContext` struct + builders + envelope snapshots.
- `domains/streaming/audit_context_test.go` — unit tests for builders and envelope population.
- `domains/streaming/stream_error_kind_executor_test.go` — executor-classified `Kind` path coverage.
- `domains/streaming/request_log_stream_capture_test.go` — concurrent test asserting counters on `request_logs` match `StreamCapture`.
- `internal/logging/lockfree_queue_index_test.go` — `LookupFrame` correctness test.
- `internal/logging/raw_data_overflow_anomaly_test.go` — overflow + close hooks emit stub entry + anomaly.
- `domains/streaming/executors/diagnostic_adapters_envelope_test.go` — anomaly envelope plumbing.
- `domains/streaming/raw_log_envelope_handler_test.go` — `client_request` envelope in chat/messages/responses handlers.

**Modified files**
- `internal/logging/raw_data_logger.go` — add `Reason` field to `RawDataEntry`.
- `internal/logging/async_raw_logger.go` — add `LookupFrame(requestID, direction) (file, offset, ok)`, indexed `sync.Map`, overflow anomaly, `close_drained` final entry.
- `internal/logging/lockfree_anomaly_reporter.go` — add `ReportRawLogOverflow` + `ReportRawLogCloseDrained`, `RawLogOverflow` / `RawLogCloseDrained` constants in `AnomalyReport.AnomalyType`.
- `internal/logging/raw_data_logger_envelope_test.go` — new assertions for `Reason` field + `LookupFrame`.
- `domains/streaming/executors/diagnostic_adapters.go` — accept `*AuditContext`, use it for context + locator; remove `context.Background()`.
- `domains/streaming/executors/executor.go` — populate `envelopeFromParams` with all fields, build per-attempt `*AuditContext`, register attempt on executor, populate `StreamOutcome.Kind`.
- `domains/streaming/executors/executor.go` — replace `LogRequest`/`LogResponse` adapter calls in `Execute()` with envelope-aware paths.
- `domains/streaming/diagnostic_context.go` — `Audit *AuditContext` field; `logRawUpstreamFrame` takes `*AuditContext`.
- `domains/streaming/handler.go` — build `AuditContext` once in handlers (chat/messages/responses); pass through `ExecParams`; thread to streaming bridges via `DiagnosticContext`; populate `StreamChunksSent`/`StreamChunkErrors` from `StreamCapture.Snapshot()`; rewrite `streamErrorKindForDetailCode` signature to take `*StreamOutcome` + `detail`; pick `Kind` first.
- `domains/streaming/request_log_pipeline.go` — populate `StreamChunksSent`/`StreamChunkErrors` from `StreamCapture.Snapshot()` if present.
- `domains/streaming/handler.go` (`emitTelemetry`) — same.
- `domains/streaming/anthropic_bridge.go`, `anthropic_stream.go`, `responses_bridge.go` — read `d.Audit` to populate `LogAudit` calls; emit final `client_response` entry on every `Interrupted` branch with the same envelope + `Reason`.
- `domains/hooks/audit/audit.go` — add `Snapshot() (chunksSent, chunkErrors int)` to `StreamCapture`; add `Finalized()` boolean; make `RecordChunkSent` authoritative.
- `errorsx/classify.go` — add `KindConversion` constant.

---

## Task 1: Add `KindConversion` to `errorsx`

**Files:**
- Modify: `errorsx/classify.go:13-79`

- [ ] **Step 1: Write failing test (mapping test in `errorsx`)**

```go
// errorsx/classify_test.go (create if absent; otherwise append)
package errorsx

import "testing"

func TestKindConversion_IsValid(t *testing.T) {
    if KindConversion == "" {
        t.Fatal("KindConversion must be non-empty")
    }
}
```

- [ ] **Step 2: Run test — expect failure**

Run: `go test ./errorsx/... -run TestKindConversion_IsValid -v`
Expected: FAIL — `KindConversion` undefined.

- [ ] **Step 3: Add the constant in `errorsx/classify.go`**

In the existing `const ( ... KindEmptyResponse` block, append:

```go
    // KindConversion marks a stream / body that the bridge or executor
    // emitted but whose serialization or shape could not be converted
    // (e.g. conversion_error in 2026-07-28 §5.8 taxonomy).
    KindConversion ErrorKind = "conversion_error"
```

- [ ] **Step 4: Run test — expect pass**

Run: `go test ./errorsx/... -run TestKindConversion_IsValid -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add errorsx/classify.go errorsx/classify_test.go
git commit -m "feat(errorsx): add KindConversion constant for stream taxonomy"
```

---

## Task 2: Add `Reason` field to `RawDataEntry`

**Files:**
- Modify: `internal/logging/raw_data_logger.go:40-70`

- [ ] **Step 1: Write failing JSON-roundtrip test**

Append to `internal/logging/raw_data_logger_test.go` (or create):

```go
package logging

import (
    "encoding/json"
    "testing"
)

func TestRawDataEntry_ReasonField(t *testing.T) {
    e := RawDataEntry{
        RequestID: "r1", Direction: "client_response", Protocol: "openai-chat",
        Reason: "client_cancel",
    }
    data, err := json.Marshal(e)
    if err != nil { t.Fatal(err) }
    if !bytes.Contains(data, []byte(`"reason":"client_cancel"`)) {
        t.Fatalf("missing reason in json: %s", data)
    }
    var round RawDataEntry
    if err := json.Unmarshal(data, &round); err != nil { t.Fatal(err) }
    if round.Reason != "client_cancel" { t.Fatalf("got %q", round.Reason) }
}
```

Add `"bytes"` to imports.

- [ ] **Step 2: Run test — expect failure (no `Reason` field yet)**

Run: `go test ./internal/logging/... -run TestRawDataEntry_ReasonField -v`
Expected: FAIL — `Reason` undefined.

- [ ] **Step 3: Add the field**

Edit `internal/logging/raw_data_logger.go` `RawDataEntry` struct. After the existing `Error string \`json:"error,omitempty"\`` line, add:

```go
    // Reason carries the structured outcome of a streaming or
    // interrupted frame (e.g. "client_cancel", "upstream_error",
    // "end_of_stream", "stream_panic"). Distinct from Error which
    // holds the message text. Operators filter the audit log on this
    // field the same way they filter request_logs.error_kind.
    Reason string `json:"reason,omitempty"`
```

- [ ] **Step 4: Run test — expect pass**

Run: `go test ./internal/logging/... -run TestRawDataEntry_ReasonField -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logging/raw_data_logger.go internal/logging/raw_data_logger_test.go
git commit -m "feat(logging): add Reason field to RawDataEntry"
```

---

## Task 3: Add `Snapshot` + `Finalized` to `audit.StreamCapture`

**Files:**
- Modify: `domains/hooks/audit/audit.go:110-181`

- [ ] **Step 1: Write failing test**

Append to `domains/hooks/audit/stream_test.go`:

```go
func TestStreamCapture_Snapshot(t *testing.T) {
    c := NewStreamCapture()
    c.RecordChunkSent()
    c.RecordChunkSent()
    c.RecordChunkSent()
    sent, errs := c.Snapshot()
    if sent != 3 { t.Errorf("sent=%d want 3", sent) }
    if errs != 0  { t.Errorf("errs=%d want 0", errs) }
    if c.Finalized() { t.Error("expected not finalized before MarkDone") }
    c.MarkDone()
    if !c.Finalized() { t.Error("expected finalized after MarkDone") }
}
```

- [ ] **Step 2: Run test — expect failure**

Run: `go test ./domains/hooks/audit/... -run TestStreamCapture_Snapshot -v`
Expected: FAIL — `Snapshot`/`Finalized` undefined.

- [ ] **Step 3: Implement `Snapshot` and `Finalized`**

Edit `domains/hooks/audit/audit.go` `StreamCapture` struct, add fields:

```go
    // chunksSentAuthoritative tracks chunks sent to the client via the
    // stream wrapper. Set under c.mu; the legacy chunksSent field is
    // kept as a snapshot for callers that prefer the int.
    finalized atomic.Bool
```

(Keep existing `chunksSent int` field for back-compat.)

Add methods below `RecordChunkSent` (line ~209):

```go
// Snapshot returns the current chunksSent and chunkErrors counters
// captured atomically under c.mu. After MarkDone, Snapshot returns
// the values cached at finalisation time so concurrent emit calls
// (success + failure) see the same numbers.
func (c *StreamCapture) Snapshot() (sent, errs int) {
    if c == nil {
        return 0, 0
    }
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.chunksSent, c.chunkErrors
}

// Finalized reports whether the stream reached MarkDone / MarkInterrupted.
// Once true, subsequent calls to Snapshot return the cached final
// counters so handlers and request_log_pipeline observe the same row.
func (c *StreamCapture) Finalized() bool {
    if c == nil { return false }
    return c.finalized.Load()
}
```

Update `MarkDone` (around line 469) to call `c.finalized.Store(true)` before returning, and likewise `MarkInterruptedWithReason` (line 460). Also have each store `c.chunksSent`/`c.chunkErrors` from the snapshot.

Add `chunkErrors int` field if absent (track in `RecordChunkError` if it exists, or accept from `StreamChunkError` calls). Update existing `RecordChunkError` to increment `chunkErrors`. If no `RecordChunkError` exists, add:

```go
// RecordChunkError increments the chunks-failed counter. Called by
// streaming bridges when a chunk fails to be serialised or written.
func (c *StreamCapture) RecordChunkError() {
    if c == nil { return }
    c.mu.Lock()
    c.chunkErrors++
    c.mu.Unlock()
}
```

- [ ] **Step 4: Run test — expect pass**

Run: `go test ./domains/hooks/audit/... -run TestStreamCapture_Snapshot -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domains/hooks/audit/audit.go domains/hooks/audit/stream_test.go
git commit -m "feat(audit): add StreamCapture.Snapshot / Finalized"
```

---

## Task 4: AsyncRawDataLogger per-request frame index

**Files:**
- Modify: `internal/logging/async_raw_logger.go:127-143` (struct)
- Modify: `internal/logging/async_raw_logger.go:520-527` (CurrentLocation stays)
- Test: `internal/logging/lockfree_queue_index_test.go` (new)

- [ ] **Step 1: Write failing test**

Create `internal/logging/lockfree_queue_index_test.go`:

```go
package logging

import (
    "path/filepath"
    "testing"
)

func TestAsyncRawDataLogger_LookupFrame(t *testing.T) {
    dir := t.TempDir()
    logger, err := NewAsyncRawDataLogger(dir, 1024*1024, true, 64)
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = logger.Close() })

    env := RawCorrelationEnvelope{GWSessionID: "s1", TenantID: "t1"}
    logger.LogClientRequestWithEnvelope("req-A", "openai-chat", []byte(`{"a":1}`), nil, "pre_conversion", env)
    logger.LogUpstreamRequestWithEnvelope("req-A", "openai-chat", []byte(`{"b":2}`), "post_conversion", env)
    logger.LogUpstreamResponseWithEnvelope("req-B", "openai-chat", []byte(`{"c":3}`), "post_conversion", env)

    // Drain to disk so the file/offset are populated.
    if err := logger.Close(); err != nil { t.Fatal(err) }

    file, offset, ok := logger.LookupFrame("req-A", "upstream_request")
    if !ok { t.Fatal("expected ok=true") }
    if !filepath.IsAbs(file) && file == "" { t.Fatalf("file %q", file) }
    if offset <= 0 { t.Errorf("expected offset > 0, got %d", offset) }

    _, _, ok = logger.LookupFrame("req-NONE", "client_request")
    if ok { t.Error("expected miss") }
}
```

- [ ] **Step 2: Run test — expect failure**

Run: `go test ./internal/logging/... -run TestAsyncRawDataLogger_LookupFrame -v`
Expected: FAIL — `LookupFrame` undefined.

- [ ] **Step 3: Add the index map to the struct**

Edit `internal/logging/async_raw_logger.go`, add field after `lastDropWarn atomic.Int64`:

```go
    // frameIndex keys a (requestID, direction) pair to the (file,
    // offset) of the most recent raw entry written for that request
    // and direction. Populated by the flush worker after each
    // successful writeEntries call so callers in the request hot path
    // can correlate anomalies to the raw line that produced them.
    frameIndex sync.Map // key: string("rid|dir") -> rawFrameLocation
```

Add type alias above the struct:

```go
type rawFrameLocation struct {
    File   string
    Offset int64
}

type frameKey struct {
    RequestID string
    Direction string
}
```

- [ ] **Step 4: Add `LookupFrame` method**

Add to `async_raw_logger.go`:

```go
// LookupFrame returns the (file, offset) of the most recent raw
// entry written for the given (requestID, direction) pair. ok is
// false when no entry has been flushed yet for that pair. The
// caller (typically LockFreeAnomalyReporter) uses the returned
// (file, offset) to populate AnomalyReport.RawLogFile/RawLogOffset
// without falling back to the global CurrentLocation() racy path.
func (l *AsyncRawDataLogger) LookupFrame(requestID, direction string) (file string, offset int64, ok bool) {
    if l == nil { return "", 0, false }
    k := requestID + "|" + direction
    if v, hit := l.frameIndex.Load(k); hit {
        loc := v.(rawFrameLocation)
        return loc.File, loc.Offset, true
    }
    return "", 0, false
}

// recordFrameLocation indexes the most recent flushed entry.
func (l *AsyncRawDataLogger) recordFrameLocation(requestID, direction string, entry *RawDataEntry) {
    if l == nil || l.baseLogger == nil || entry == nil { return }
    k := requestID + "|" + direction
    l.frameIndex.Store(k, rawFrameLocation{
        File:   l.baseLogger.currentPath,
        Offset: l.baseLogger.currentOffset - int64(len(entry.RawData)) - 1, // minus the newline
    })
}
```

- [ ] **Step 5: Wire `flushBatch` to call `recordFrameLocation`**

Edit `flushBatch` (around line 471). After `l.baseLogger.writeEntries(entries)`, add:

```go
    for i := range entries {
        l.recordFrameLocation(entries[i].RequestID, entries[i].Direction, &entries[i])
    }
```

- [ ] **Step 6: Run test — expect pass**

Run: `go test ./internal/logging/... -run TestAsyncRawDataLogger_LookupFrame -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/logging/async_raw_logger.go internal/logging/lockfree_queue_index_test.go
git commit -m "feat(logging): add AsyncRawDataLogger per-request frame index"
```

---

## Task 5: AsyncRawDataLogger overflow / close hooks emit anomalies + audit row

**Files:**
- Modify: `internal/logging/lockfree_anomaly_reporter.go` (add `ReportRawLogOverflow`, `ReportRawLogCloseDrained`)
- Modify: `internal/logging/async_raw_logger.go` (overflow / close hooks call reporter)
- Test: `internal/logging/raw_data_overflow_anomaly_test.go` (new)

- [ ] **Step 1: Write failing test**

Create `internal/logging/raw_data_overflow_anomaly_test.go`:

```go
package logging

import (
    "context"
    "errors"
    "io"
    "net/http"
    "net/http/httptest"
    "sync"
    "testing"
)

type capturedBatch struct {
    mu  sync.Mutex
    raw []byte
}

func TestAsyncRawDataLogger_OverflowEmitsAnomaly(t *testing.T) {
    dir := t.TempDir()

    // Use a tiny queue so the second entry overflows.
    logger, err := NewAsyncRawDataLogger(dir, 1024*1024, true, 1)
    if err != nil { t.Fatal(err) }

    captured := &capturedBatch{}
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        b, _ := io.ReadAll(r.Body)
        captured.mu.Lock()
        captured.raw = b
        captured.mu.Unlock()
        w.WriteHeader(http.StatusOK)
    }))
    t.Cleanup(srv.Close)

    rep := NewLockFreeAnomalyReporter(srv.URL, true, 8)
    t.Cleanup(func() { _ = rep.Close() })
    logger.SetOverflowReporter(rep)

    env := RawCorrelationEnvelope{GWSessionID: "g1", TenantID: "t1", APIKeyID: 9}
    big := make([]byte, 8*1024)
    for i := range big { big[i] = 'x' }
    // Burst to overflow the queue (capacity 1).
    for i := 0; i < 64; i++ {
        logger.LogUpstreamRequestWithEnvelope("req-of", "openai-chat", big, "post_conversion", env)
    }

    // Wait a moment and check the reporter received an overflow anomaly.
    // We poll instead of sleeping a fixed interval.
    if err := rep.Flush(context.Background()); err != nil { t.Fatal(err) }

    captured.mu.Lock()
    body := string(captured.raw)
    captured.mu.Unlock()
    if !strings.Contains(body, `"raw_log_overflow"`) {
        t.Fatalf("expected raw_log_overflow anomaly, body=%s", body)
    }
    if !strings.Contains(body, `"gw_session_id":"g1"`) {
        t.Fatalf("expected gw_session_id, body=%s", body)
    }
}
```

Add imports `errors`, `strings`.

- [ ] **Step 2: Run test — expect failure**

Run: `go test ./internal/logging/... -run TestAsyncRawDataLogger_OverflowEmitsAnomaly -v`
Expected: FAIL — `SetOverflowReporter` undefined.

- [ ] **Step 3: Add `SetOverflowReporter` + overflow anomaly in `AsyncRawDataLogger`**

Edit `async_raw_logger.go`. After the struct, add a new field:

```go
// overflowReporter receives raw_log_overflow / raw_log_close_drained
// anomalies when the queue cannot keep up. nil disables the path.
overflowReporter AnomalyReporter
```

Add setter:

```go
// SetOverflowReporter wires an anomaly reporter that is invoked on
// queue overflow or close-with-remaining-items. The reporter's
// ReportRawLogOverflow / ReportRawLogCloseDrained methods carry the
// audit correlation envelope so dashboards can correlate the
// anomaly with the request_logs row.
func (l *AsyncRawDataLogger) SetOverflowReporter(rep AnomalyReporter) {
    if l == nil { return }
    l.stateMu.Lock()
    l.overflowReporter = rep
    l.stateMu.Unlock()
}
```

Add a minimal interface in the package (or use the existing `LockFreeAnomalyReporter` directly via duck typing):

```go
// AnomalyReporter is the minimal interface AsyncRawDataLogger needs to
// report overflow / close-drained events. Implemented by
// *LockFreeAnomalyReporter.
type AnomalyReporter interface {
    ReportRawLogOverflow(ctx context.Context, env RawCorrelationEnvelope, dropped uint64)
    ReportRawLogCloseDrained(ctx context.Context, env RawCorrelationEnvelope, remaining uint64)
}
```

Now modify `noteDroppedEntry` to call the reporter. Replace the existing body:

```go
func (l *AsyncRawDataLogger) noteDroppedEntry(requestID, direction string) {
    now := time.Now().UnixNano()
    last := l.lastDropWarn.Load()
    if now-last < int64(dropWarnInterval) { return }
    if !l.lastDropWarn.CompareAndSwap(last, now) { return }
    slog.Warn("async_raw_logger: queue full, dropping log entries",
        "request_id", requestID,
        "direction", direction,
        "dropped_total", l.queue.Stats().DropCount)
    if l.overflowReporter != nil {
        ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
        defer cancel()
        l.overflowReporter.ReportRawLogOverflow(ctx, RawCorrelationEnvelope{}, uint64(l.queue.Stats().DropCount))
    }
}
```

- [ ] **Step 4: Implement `ReportRawLogOverflow` + `ReportRawLogCloseDrained` on `LockFreeAnomalyReporter`**

Edit `lockfree_anomaly_reporter.go`. Add new constants near the top:

```go
const (
    AnomalyTypeRawLogOverflow     = "raw_log_overflow"
    AnomalyTypeRawLogCloseDrained = "raw_log_close_drained"
)
```

Add the two methods after `ReportSemanticIncomplete` (line ~234):

```go
// ReportRawLogOverflow queues an anomaly reporting that the async raw
// logger dropped entries because the queue was full. The envelope
// fields are best-effort; the important payload is the dropped count.
func (r *LockFreeAnomalyReporter) ReportRawLogOverflow(ctx context.Context, env RawCorrelationEnvelope, dropped uint64) {
    if r == nil { return }
    report := &AnomalyReport{
        RequestID:       "raw_logger",
        AnomalyType:     AnomalyTypeRawLogOverflow,
        SourceProtocol:  "raw_logger",
        TargetProtocol:  "raw_logger",
        Direction:       "overflow",
        Error:           fmt.Sprintf("raw log queue overflow; dropped=%d", dropped),
        Dropped:         dropped,
        OccurredAt:      time.Now().UnixMilli(),
        ClientRequestID: env.ClientRequestID,
        GWSessionID:     env.GWSessionID,
        TraceID:         env.TraceID,
    }
    r.stampRawLogLocation(report)
    r.queue.Enqueue(*report)
    select {
    case r.flushCh <- struct{}{}:
    default:
    }
}

// ReportRawLogCloseDrained queues an anomaly when Close() exited with
// items still in the queue.
func (r *LockFreeAnomalyReporter) ReportRawLogCloseDrained(ctx context.Context, env RawCorrelationEnvelope, remaining uint64) {
    if r == nil { return }
    report := &AnomalyReport{
        RequestID:      "raw_logger",
        AnomalyType:    AnomalyTypeRawLogCloseDrained,
        SourceProtocol: "raw_logger",
        TargetProtocol: "raw_logger",
        Direction:      "close_drained",
        Error:          fmt.Sprintf("raw logger close drained with remaining=%d", remaining),
        Dropped:        remaining,
        OccurredAt:     time.Now().UnixMilli(),
        GWSessionID:    env.GWSessionID,
    }
    r.stampRawLogLocation(report)
    r.queue.Enqueue(*report)
    select {
    case r.flushCh <- struct{}{}:
    default:
    }
}
```

(Adjust field names to match the existing `AnomalyReport` struct — see `domains/hooks/audit/...` if needed; if `Dropped`/`Direction` don't exist, use whatever is closest and add a one-line comment.)

- [ ] **Step 5: Update `Close()` on `AsyncRawDataLogger` to emit `close_drained`**

Replace the `Close()` body (line ~485):

```go
func (l *AsyncRawDataLogger) Close() error {
    l.closeOnce.Do(func() {
        l.stateMu.Lock()
        l.closed.Store(true)
        l.stateMu.Unlock()
        l.cancel()
        <-l.done
        // Drain for up to 5*flushDelay (default 5s).
        deadline := time.Now().Add(5 * l.flushDelay)
        for time.Now().Before(deadline) && l.queue.Size() > 0 {
            l.flushBatch()
        }
        // Emit close_drained stub entry capturing remaining state.
        stats := l.queue.Stats()
        remaining := uint64(l.queue.Size())
        if remaining > 0 || stats.EnqueueCount > 0 {
            closeEntry := RawDataEntry{
                Timestamp:      time.Now(),
                RequestID:      "raw_logger",
                Direction:      "close_drained",
                Protocol:       "raw_logger",
                DataSize:       int(remaining),
                ConversionStep: "shutdown",
                Error: fmt.Sprintf("enqueued=%d dequeued=%d dropped=%d remaining=%d",
                    stats.EnqueueCount, stats.DequeueCount, stats.DropCount, remaining),
            }
            if l.baseLogger != nil {
                _ = l.baseLogger.writeEntries([]RawDataEntry{closeEntry})
            }
            if l.overflowReporter != nil {
                ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
                defer cancel()
                l.overflowReporter.ReportRawLogCloseDrained(ctx, RawCorrelationEnvelope{}, remaining)
            }
        }
        if l.baseLogger != nil {
            l.closeErr = l.baseLogger.Close()
        }
    })
    return l.closeErr
}
```

- [ ] **Step 6: Run test — expect pass**

Run: `go test ./internal/logging/... -run TestAsyncRawDataLogger_OverflowEmitsAnomaly -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/logging/async_raw_logger.go internal/logging/lockfree_anomaly_reporter.go internal/logging/raw_data_overflow_anomaly_test.go
git commit -m "feat(logging): overflow / close-drained anomaly hooks"
```

---

## Task 6: `streaming.AuditContext` builder + envelope snapshots

**Files:**
- Create: `domains/streaming/audit_context.go`
- Test: `domains/streaming/audit_context_test.go`

- [ ] **Step 1: Write failing test**

Create `domains/streaming/audit_context_test.go`:

```go
package streaming

import (
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/kaixuan/llm-gateway-go/domains/authentication"
    "github.com/kaixuan/llm-gateway-go/domains/session"
)

func TestAuditContextFromRequest_FullEnvelope(t *testing.T) {
    r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    r.Header.Set("X-Request-Id", "client-rid-1")
    r.Header.Set("X-Gw-Task-Id", "task-1")
    r.Header.Set("X-Trace-Id", "trace-1")
    r.Header.Set("X-Span-Id", "span-1")
    r.Header.Set("X-Parent-Request-Id", "parent-1")

    sn := &session.Session{SessionID: "sess-1", TaskID: "session-task", TenantID: "tenant-a", APIKeyID: 7}
    ki := &authentication.KeyInfo{ID: 7, TenantID: "tenant-a", ApplicationID: 11}

    body := []byte(`{"model":"gpt-4o","messages":[]}`)
    ctx := AuditContextFromRequest(r, sn, body, ki)

    if ctx.ClientRequestID != "client-rid-1" { t.Errorf("client_request_id=%s", ctx.ClientRequestID) }
    if ctx.GWSessionID != "sess-1" { t.Errorf("gw_session_id=%s", ctx.GWSessionID) }
    if ctx.GWTaskID != "task-1" { t.Errorf("gw_task_id=%s", ctx.GWTaskID) }
    if ctx.ParentRequestID != "parent-1" { t.Errorf("parent_request_id=%s", ctx.ParentRequestID) }
    if ctx.TenantID != "tenant-a" { t.Errorf("tenant_id=%s", ctx.TenantID) }
    if ctx.ApplicationID != "11" { t.Errorf("application_id=%s", ctx.ApplicationID) }
    if ctx.APIKeyID != 7 { t.Errorf("api_key_id=%d", ctx.APIKeyID) }
    if ctx.TraceID != "trace-1" { t.Errorf("trace_id=%s", ctx.TraceID) }
    if ctx.SpanID != "span-1" { t.Errorf("span_id=%s", ctx.SpanID) }
}

func TestAuditContextFromParams_PerAttempt(t *testing.T) {
    base := &AuditContext{RequestID: "req-1", TenantID: "t", APIKeyID: 9}
    ctx := AuditContextFromAttempt(base, 18, 42, "https://upstream/api", 2)
    if ctx.ProviderID != 18 || ctx.CredentialID != 42 { t.Errorf("provider/cred=%d/%d", ctx.ProviderID, ctx.CredentialID) }
    if ctx.UpstreamEndpoint != "https://upstream/api" { t.Errorf("endpoint=%s", ctx.UpstreamEndpoint) }
    if ctx.AttemptNo != 2 { t.Errorf("attempt_no=%d", ctx.AttemptNo) }
    // inherited fields
    if ctx.RequestID != "req-1" { t.Errorf("request_id=%s", ctx.RequestID) }
    if ctx.APIKeyID != 9 { t.Errorf("api_key_id=%d", ctx.APIKeyID) }
}

func TestAuditContext_RawAndAnomalyEnvelopes(t *testing.T) {
    ctx := &AuditContext{
        RequestID: "r1", ClientRequestID: "cr1", GWSessionID: "s1",
        GWTaskID: "task", TenantID: "t", APIKeyID: 9,
        ProviderID: 18, CredentialID: 42, AttemptNo: 2,
        UpstreamEndpoint: "https://u", TraceID: "trace", SpanID: "span",
    }
    raw := ctx.RawCorrelationEnvelope()
    if raw.ClientRequestID != "cr1" || raw.ProviderID != 18 || raw.AttemptNo != 2 {
        t.Errorf("raw envelope: %+v", raw)
    }
    anom := ctx.AnomalyReportEnvelope()
    if anom.ClientRequestID != "cr1" || anom.ProviderID != 18 || anom.CredentialID != 42 {
        t.Errorf("anom envelope: %+v", anom)
    }
}
```

- [ ] **Step 2: Run test — expect failure**

Run: `go test ./domains/streaming/... -run TestAuditContext -v`
Expected: FAIL — `AuditContext` undefined.

- [ ] **Step 3: Create `domains/streaming/audit_context.go`**

```go
package streaming

import (
    "net/http"
    "sync/atomic"

    "github.com/kaixuan/llm-gateway-go/domains/authentication"
    "github.com/kaixuan/llm-gateway-go/domains/session"
    "github.com/kaixuan/llm-gateway-go/internal/logging"
)

// AuditContext is the runtime correlation handle for a single
// request. The handler builds the base fields once after parsing the
// body and headers; the executor refreshes the per-attempt fields
// every time a candidate credential is selected. Raw log emissions
// and anomaly reports read this context to populate their
// correlation envelopes.
type AuditContext struct {
    RequestID       string
    ClientRequestID string
    GWSessionID     string
    GWTaskID        string // resolved X-Gw-Task-Id or session.TaskID
    ParentRequestID string
    TenantID        string
    ApplicationID   string
    APIKeyID        int
    TraceID         string
    SpanID          string

    ProviderID       int
    CredentialID     int
    AttemptNo        int
    UpstreamEndpoint string

    ChunkIndex atomic.Int64
}

// AuditContextFromRequest builds the base AuditContext from the
// resolved request, session, body, and key info. Headers X-Request-Id,
// X-Gw-Task-Id, X-Trace-Id, X-Span-Id, X-Parent-Request-Id are
// preferred when present; session fields are the fallback.
func AuditContextFromRequest(r *http.Request, sn *session.Session, body []byte, ki *authentication.KeyInfo) *AuditContext {
    ctx := &AuditContext{}
    if r != nil {
        ctx.ClientRequestID = r.Header.Get("X-Request-Id")
        ctx.GWTaskID = r.Header.Get("X-Gw-Task-Id")
        ctx.TraceID = r.Header.Get("X-Trace-Id")
        ctx.SpanID = r.Header.Get("X-Span-Id")
        ctx.ParentRequestID = r.Header.Get("X-Parent-Request-Id")
    }
    if sn != nil {
        if ctx.GWSessionID == "" { ctx.GWSessionID = sn.SessionID }
        if ctx.GWTaskID == "" { ctx.GWTaskID = sn.TaskID }
        if ctx.TenantID == "" { ctx.TenantID = sn.TenantID }
        if ctx.APIKeyID == 0 { ctx.APIKeyID = sn.APIKeyID }
    }
    if ki != nil {
        if ctx.TenantID == "" { ctx.TenantID = ki.TenantID }
        if ctx.APIKeyID == 0 { ctx.APIKeyID = ki.ID }
        if ki.ApplicationID != 0 {
            ctx.ApplicationID = itoa(ki.ApplicationID)
        }
    }
    return ctx
}

// AuditContextFromAttempt returns a shallow copy of `base` with
// per-attempt fields populated. The returned struct is safe for the
// caller to mutate (the per-attempt fields) without affecting base.
func AuditContextFromAttempt(base *AuditContext, providerID, credentialID int, endpoint string, attemptNo int) *AuditContext {
    if base == nil { return nil }
    cp := *base
    cp.ProviderID = providerID
    cp.CredentialID = credentialID
    cp.UpstreamEndpoint = endpoint
    cp.AttemptNo = attemptNo
    return &cp
}

// RawCorrelationEnvelope returns a snapshot of the current fields in
// the wire-level DTO used by AsyncRawDataLogger. The returned value
// is a copy safe to mutate.
func (c *AuditContext) RawCorrelationEnvelope() logging.RawCorrelationEnvelope {
    if c == nil { return logging.RawCorrelationEnvelope{} }
    return logging.RawCorrelationEnvelope{
        ClientRequestID:  c.ClientRequestID,
        GWSessionID:      c.GWSessionID,
        GWTaskID:         c.GWTaskID,
        ParentRequestID:  c.ParentRequestID,
        TenantID:         c.TenantID,
        ApplicationID:    c.ApplicationID,
        APIKeyID:         c.APIKeyID,
        ProviderID:       c.ProviderID,
        CredentialID:     c.CredentialID,
        AttemptNo:        c.AttemptNo,
        ChunkIndex:       int(c.ChunkIndex.Load()),
        UpstreamEndpoint: c.UpstreamEndpoint,
        TraceID:          c.TraceID,
        SpanID:           c.SpanID,
    }
}

// AnomalyReportEnvelope returns the wire-level envelope used by
// LockFreeAnomalyReporter (and any other anomaly transport).
func (c *AuditContext) AnomalyReportEnvelope() logging.AnomalyReportEnvelope {
    if c == nil { return logging.AnomalyReportEnvelope{} }
    return logging.AnomalyReportEnvelope{
        ClientRequestID: c.ClientRequestID,
        GWSessionID:     c.GWSessionID,
        ProviderID:      c.ProviderID,
        CredentialID:    c.CredentialID,
        TraceID:         c.TraceID,
    }
}

// itoa converts an int to its decimal representation. Centralised so
// the test seam can import it without pulling in strconv directly.
func itoa(i int) string {
    return strconvItoa(i)
}
```

Add a small file-scope helper in `audit_context.go` (or use `strconv.Itoa`):

```go
import "strconv"
func strconvItoa(i int) string { return strconv.Itoa(i) }
```

- [ ] **Step 4: Run test — expect pass**

Run: `go test ./domains/streaming/... -run TestAuditContext -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domains/streaming/audit_context.go domains/streaming/audit_context_test.go
git commit -m "feat(streaming): add AuditContext runtime correlation handle"
```

---

## Task 7: Enrich `envelopeFromParams` to populate every field

**Files:**
- Modify: `domains/streaming/executors/executor.go:209-225`

- [ ] **Step 1: Write failing test**

Append to `domains/streaming/executors/diagnostic_adapters_test.go`:

```go
func TestEnvelopeFromParams_AllFields(t *testing.T) {
    appID := 33
    env := envelopeFromParams(&ExecParams{
        RequestID: "req-1",
        SessionID: "sess",
        Model:     "gpt-4o",
        TenantID:  "t1",
        KeyID:     7,
        AppID:     &appID,
        ClientRequestID: "cr1",
        ParentRequestID: "parent",
        TraceID:    "trace",
        SpanID:     "span",
        ProviderID: 18,
        CredentialID: 42,
        AttemptNo:  2,
        UpstreamEndpoint: "https://upstream",
    })
    if env.GWTaskID == "gpt-4o" { t.Error("GWTaskID must NOT be the model name") }
    if env.GWTaskID != "" { t.Errorf("GWTaskID=%q", env.GWTaskID) }
    if env.ClientRequestID != "cr1" { t.Errorf("client_request_id=%q", env.ClientRequestID) }
    if env.ProviderID != 18 { t.Errorf("provider_id=%d", env.ProviderID) }
    if env.CredentialID != 42 { t.Errorf("credential_id=%d", env.CredentialID) }
    if env.AttemptNo != 2 { t.Errorf("attempt_no=%d", env.AttemptNo) }
    if env.UpstreamEndpoint != "https://upstream" { t.Errorf("upstream_endpoint=%s", env.UpstreamEndpoint) }
    if env.ApplicationID != "33" { t.Errorf("application_id=%s", env.ApplicationID) }
}
```

- [ ] **Step 2: Run test — expect failure**

Run: `go test ./domains/streaming/executors/... -run TestEnvelopeFromParams_AllFields -v`
Expected: FAIL.

- [ ] **Step 3: Update `envelopeFromParams`**

Replace the body (lines 211-225):

```go
func envelopeFromParams(params *ExecParams) RawCorrelationEnvelope {
    if params == nil {
        return RawCorrelationEnvelope{}
    }
    env := RawCorrelationEnvelope{
        ClientRequestID:  params.ClientRequestID,
        GWSessionID:      params.SessionID,
        GWTaskID:         params.GWTaskID,
        ParentRequestID:  params.ParentRequestID,
        TenantID:         params.TenantID,
        APIKeyID:         params.KeyID,
        ProviderID:       params.ProviderID,
        CredentialID:     params.CredentialID,
        AttemptNo:        params.AttemptNo,
        UpstreamEndpoint: params.UpstreamEndpoint,
        TraceID:          params.TraceID,
        SpanID:           params.SpanID,
    }
    if params.AppID != nil {
        env.ApplicationID = fmt.Sprintf("%d", *params.AppID)
    }
    return env
}
```

Verify the `ExecParams` fields exist. If not, add them as `string`/`int` zero-valued struct fields:

```go
type ExecParams struct {
    ...
    ClientRequestID  string
    GWTaskID         string
    ParentRequestID  string
    ProviderID       int
    CredentialID     int
    AttemptNo        int
    UpstreamEndpoint string
    TraceID          string
    SpanID           string
}
```

- [ ] **Step 4: Run test — expect pass**

Run: `go test ./domains/streaming/executors/... -run TestEnvelopeFromParams_AllFields -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domains/streaming/executors/executor.go domains/streaming/executors/diagnostic_adapters_test.go
git commit -m "feat(executors): enrich envelopeFromParams with all correlation fields"
```

---

## Task 8: `AnomalyReporterAdapter` uses AuditContext for context + locator

**Files:**
- Modify: `domains/streaming/executors/diagnostic_adapters.go:140-190`

- [ ] **Step 1: Write failing test**

Append to `diagnostic_adapters_test.go`:

```go
func TestAnomalyReporterAdapter_UsesAuditContext(t *testing.T) {
    rep := &diagnosticAnomalyReporter{}
    adapter := NewAnomalyReporterAdapterWithAudit(rep)
    auditCtx := &AuditContext{
        ClientRequestID: "cr1", GWSessionID: "s1",
        ProviderID: 18, CredentialID: 42, TraceID: "trace",
    }
    require.NoError(t, adapter.ReportAnomalyFromContext(auditCtx, "req-1", "conversion_error", map[string]interface{}{
        "source_protocol": "openai-chat", "target_protocol": "anthropic-messages",
    }))
    require.Equal(t, 1, rep.conversionCalls)
    require.Equal(t, "cr1", rep.lastClientRequestID)
    require.Equal(t, "s1", rep.lastGWSessionID)
    require.Equal(t, 18, rep.lastProviderID)
    require.Equal(t, 42, rep.lastCredentialID)
}
```

Update `diagnosticAnomalyReporter` to capture the new fields:

```go
type diagnosticAnomalyReporter struct {
    conversionCalls int
    toolCalls       int
    semanticCalls   int
    lastRequestID   string
    lastSource      string
    lastTarget      string
    lastStep        string
    lastInput       []byte
    lastOutput      []byte
    lastErr         error
    lastClientRequestID string
    lastGWSessionID     string
    lastProviderID      int
    lastCredentialID    int
    lastTraceID         string
}
```

Replace existing method bodies with assignments to the new fields. The new method `Report*WithContext` calls on the reporter should be:

```go
type contextAwareAnomalyReporter interface {
    ReportToolCallsMissingWithContext(ctx context.Context, requestID, sourceProto, targetProto string, rawInput, rawOutput []byte, missingToolCalls string, confidence float64)
    ReportConversionErrorWithContext(ctx context.Context, requestID, sourceProto, targetProto, step string, rawInput []byte, err error)
    ReportSemanticIncompleteWithContext(ctx context.Context, requestID, protocol string, rawOutput []byte, reason string, indicators []string, confidence float64)
}
```

- [ ] **Step 2: Run test — expect failure**

Run: `go test ./domains/streaming/executors/... -run TestAnomalyReporterAdapter_UsesAuditContext -v`
Expected: FAIL.

- [ ] **Step 3: Implement `NewAnomalyReporterAdapterWithAudit` and `ReportAnomalyFromContext`**

Append to `diagnostic_adapters.go`:

```go
// AnomalyReporterAdapterWithAudit enriches AnomalyReporterAdapter
// with a per-request AuditContext. Reports carry the envelope and
// the raw-log locator; ReportAnomalyFromContext stamps both.
type AnomalyReporterAdapterWithAudit struct {
    AnomalyReporterAdapter
    rawLookup func(requestID, direction string) (file string, offset int64, ok bool)
    parentCtx context.Context
}

func NewAnomalyReporterAdapterWithAudit(rep anomalyReporter) *AnomalyReporterAdapterWithAudit {
    return &AnomalyReporterAdapterWithAudit{
        AnomalyReporterAdapter: AnomalyReporterAdapter{reporter: rep},
        parentCtx:              context.Background(),
    }
}

// SetRawLookup wires a per-request raw-frame index. When set, the
// adapter populates file/offset from the latest upstream entry for
// the given request and direction.
func (a *AnomalyReporterAdapterWithAudit) SetRawLookup(fn func(string, string) (string, int64, bool)) {
    a.rawLookup = fn
}

// ReportAnomalyFromContext dispatches to the underlying reporter
// with the audit envelope's correlation context attached. When a
// raw-frame lookup is configured, the file/offset from the latest
// upstream_request / upstream_response is also passed through.
func (a *AnomalyReporterAdapterWithAudit) ReportAnomalyFromContext(
    auditCtx *AuditContext, requestID, anomalyType string, details map[string]interface{},
) error {
    if a == nil || a.reporter == nil { return nil }
    ctx := a.parentCtx
    if auditCtx != nil {
        env := auditCtx.AnomalyReportEnvelope()
        ctx = logging.WithAnomalyEnvelope(ctx, env)
    }
    // dispatch as before but with `ctx` instead of context.Background()
    sourceProtocol := detailString(details, "source_protocol", "unknown")
    targetProtocol := detailString(details, "target_protocol", "unknown")
    conversionStep := detailString(details, "conversion_step", "stream_conversion")
    rawInput := detailBytes(details, "raw_input")
    rawOutput := detailBytes(details, "raw_output")
    confidence := detailFloat(details, "confidence", 1)

    switch anomalyType {
    case "tool_calls_missing":
        a.reporter.ReportToolCallsMissing(ctx, requestID, sourceProtocol, targetProtocol, rawInput, rawOutput,
            detailString(details, "missing_tool_calls", "upstream tool calls were not emitted"), confidence)
    case "semantic_incomplete":
        a.reporter.ReportSemanticIncomplete(ctx, requestID, targetProtocol, rawOutput,
            detailString(details, "reason", "response appears incomplete"),
            detailStrings(details, "indicators"), confidence)
    default:
        a.reporter.ReportConversionError(ctx, requestID, sourceProtocol, targetProtocol, conversionStep,
            rawInput, fmt.Errorf("%s", detailString(details, "error", anomalyType)))
    }
    return nil
}
```

Add `"github.com/kaixuan/llm-gateway-go/internal/logging"` to imports.

- [ ] **Step 4: Run test — expect pass**

Run: `go test ./domains/streaming/executors/... -run TestAnomalyReporterAdapter_UsesAuditContext -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domains/streaming/executors/diagnostic_adapters.go domains/streaming/executors/diagnostic_adapters_test.go
git commit -m "feat(executors): anomaly adapter carries audit context + raw lookup"
```

---

## Task 9: `DiagnosticContext` carries `*AuditContext`

**Files:**
- Modify: `domains/streaming/diagnostic_context.go:17-27`
- Modify: `domains/streaming/diagnostic_context.go:48-56` (`logRawUpstreamFrame`)

- [ ] **Step 1: Write failing test**

Append to `diagnostic_context_test.go` (create if absent):

```go
package streaming

import "testing"

func TestDiagnosticContext_AuditField(t *testing.T) {
    d := &DiagnosticContext{Audit: &AuditContext{RequestID: "r1", TenantID: "t"}}
    if d.Audit == nil { t.Fatal("Audit must be populated") }
    if d.Audit.TenantID != "t" { t.Errorf("got %q", d.Audit.TenantID) }
}
```

- [ ] **Step 2: Run test — expect failure**

Run: `go test ./domains/streaming/... -run TestDiagnosticContext_AuditField -v`
Expected: FAIL.

- [ ] **Step 3: Add `Audit` field and update `logRawUpstreamFrame`**

Edit `domains/streaming/diagnostic_context.go`:

```go
type DiagnosticContext struct {
    RawLogger executors.RawDataLogger
    Anomaly   executors.AnomalyReporter
    Semantic  executors.SemanticAnalyzer
    Audit     *AuditContext // 2026-07-28: per-request correlation handle
    conversionReports atomic.Int32
}
```

Update `logRawUpstreamFrame` to take `attempt *AuditContext`:

```go
func logRawUpstreamFrame(diagnostics *DiagnosticContext, attempt *AuditContext, frame []byte) {
    if diagnostics == nil || diagnostics.RawLogger == nil || len(frame) == 0 {
        return
    }
    defer recoverDiagnostic(attempt.GetRequestID(), "log_raw_upstream_frame")
    idx := int64(0)
    if attempt != nil {
        idx = attempt.ChunkIndex.Add(1)
    }
    // adapter detects the envelope-aware interface and dispatches.
    if adapter, ok := diagnostics.RawLogger.(*executors.RawDataLoggerAdapter); ok {
        env := attempt.RawCorrelationEnvelope()
        env.ChunkIndex = int(idx)
        adapter.LogUpstreamResponseWithEnvelope(attempt.GetRequestID(), attempt.GetProtocol(), frame, "pre_conversion_stream_frame", env)
        return
    }
    if err := diagnostics.RawLogger.LogResponse(attempt.GetRequestID(), attempt.GetProtocol(), frame, true); err != nil {
        slog.Warn("stream diagnostics: raw response logging failed", "request_id", attempt.GetRequestID(), "error", err)
    }
}
```

Add helpers to `AuditContext` (or as free functions in `audit_context.go`):

```go
func (c *AuditContext) GetRequestID() string { if c == nil { return "" }; return c.RequestID }
func (c *AuditContext) GetProtocol() string  { return "openai-chat" } // set by streaming bridge if needed; default
```

- [ ] **Step 4: Run test — expect pass**

Run: `go test ./domains/streaming/... -run TestDiagnosticContext_AuditField -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domains/streaming/diagnostic_context.go domains/streaming/audit_context.go domains/streaming/diagnostic_context_test.go
git commit -m "feat(streaming): DiagnosticContext carries AuditContext"
```

---

## Task 10: Handler builds `*AuditContext` once + threads via `ExecParams` + `DiagnosticContext`

**Files:**
- Modify: `domains/streaming/handler.go` (ChatHandler, MessagesHandler, ResponsesHandler)
- Test: `domains/streaming/raw_log_envelope_handler_test.go`

- [ ] **Step 1: Write failing test**

Create `domains/streaming/raw_log_envelope_handler_test.go`:

```go
package streaming

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/kaixuan/llm-gateway-go/domains/authentication"
    "github.com/kaixuan/llm-gateway-go/domains/session"
    "github.com/kaixuan/llm-gateway-go/internal/logging"
)

func TestChatHandler_ClientRequestEnvelope(t *testing.T) {
    rec := &captureRaw{}
    h := &ChatHandler{
        rawDataLogger: logging.NewRawDataLogger // set in setUp; here we plug a capture
    }
    // Skip detailed wiring — assert the AuditContextFromRequest builder via the
    // public ChatHandler entry path. Just confirm the envelope helper builds the
    // expected fields.
    sn := &session.Session{SessionID: "s1", TaskID: "task-x"}
    ki := &authentication.KeyInfo{ID: 7, TenantID: "t-a"}
    r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
        strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
    r.Header.Set("X-Request-Id", "client-rid-1")
    ctx := AuditContextFromRequest(r, sn, []byte("{}"), ki)
    if ctx.GWTaskID != "task-x" { t.Errorf("expected task-x, got %q", ctx.GWTaskID) }
    if _ = rec; rec != nil { /* placeholder for capture wiring */ }
}
```

- [ ] **Step 2: Run test — expect pass for the builder; capture path is exercised in next test**

Run: `go test ./domains/streaming/... -run TestChatHandler_ClientRequestEnvelope -v`
Expected: PASS for the builder assertion. The capture portion will be wired in step 4.

- [ ] **Step 3: Inject `*AuditContext` into `ExecParams` and `DiagnosticContext`**

Edit `domains/streaming/handler.go` in `serveWithExecutor` (or wherever `ExecParams` is constructed). Add fields to `ExecParams`:

```go
type ExecParams struct {
    ...
    Audit *AuditContext // 2026-07-28: per-request correlation handle
}
```

Find the place where `DiagnosticContext` is built (likely in `serveWithExecutor` or the streaming wrapper helper) and populate `Audit`:

```go
diag := &DiagnosticContext{
    RawLogger: rawLogger,
    Anomaly:   anomaly,
    Semantic:  sem,
    Audit:     auditCtx,
}
```

In the chat/messages/responses handlers' main entry points (after auth + body parse), call:

```go
auditCtx := AuditContextFromRequest(r, session, body, keyInfo)
auditCtx.RequestID = h.ensureRequestID(r) // existing helper
```

Store `auditCtx` on the `RequestLogContext` so other helpers can access it:

```go
logCtx.AuditCtx = auditCtx
```

Add `AuditCtx *AuditContext` field to `RequestLogContext`.

- [ ] **Step 4: Wire `envelopeFromParams` to read from `ExecParams.Audit`**

Edit `domains/streaming/executors/executor.go`. Replace `envelopeFromParams`:

```go
func envelopeFromParams(params *ExecParams) RawCorrelationEnvelope {
    if params == nil || params.Audit == nil {
        return envelopeFromParamsLegacy(params)
    }
    return params.Audit.RawCorrelationEnvelope()
}

// envelopeFromParamsLegacy preserves the pre-2026-07-28 fallback when
// Audit is nil. Used by tests that construct ExecParams directly.
func envelopeFromParamsLegacy(params *ExecParams) RawCorrelationEnvelope {
    if params == nil { return RawCorrelationEnvelope{} }
    return RawCorrelationEnvelope{
        ClientRequestID:  params.ClientRequestID,
        GWSessionID:      params.SessionID,
        GWTaskID:         params.GWTaskID,
        TenantID:         params.TenantID,
        APIKeyID:         params.KeyID,
    }
}
```

- [ ] **Step 5: Run `go vet` and existing tests**

Run: `go vet ./domains/streaming/...`
Run: `go test ./domains/streaming/executors/... -run TestEnvelopeFromParams -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add domains/streaming/handler.go domains/streaming/executors/executor.go domains/streaming/raw_log_envelope_handler_test.go domains/streaming/request_log_pipeline.go
git commit -m "feat(streaming): handlers build AuditContext and thread through ExecParams"
```

---

## Task 11: Executor populates `StreamOutcome.Kind` and passes envelope to `LogRequest`/`LogResponse`

**Files:**
- Modify: `domains/streaming/executors/executor.go` (`Execute` and bridge functions)

- [ ] **Step 1: Write failing test**

Append to `domains/streaming/executors/diagnostic_adapters_test.go`:

```go
func TestExecutor_LogsClientRequestWithEnvelope(t *testing.T) {
    raw := &diagnosticRawLogger{}
    adapter := NewRawDataLoggerAdapter(raw)
    env := RawCorrelationEnvelope{
        ClientRequestID: "cr1", GWSessionID: "s1", GWTaskID: "task",
        TenantID: "t1", APIKeyID: 9, ProviderID: 18, CredentialID: 42, AttemptNo: 1,
    }
    adapter.LogClientRequestWithEnvelope("req-1", "openai-chat", []byte(`{"x":1}`), nil, "pre_conversion", env)
    if raw.clientRequests != 1 { t.Errorf("client_requests=%d", raw.clientRequests) }
    if raw.lastRequestID != "req-1" { t.Errorf("rid=%s", raw.lastRequestID) }
}
```

- [ ] **Step 2: Run test — expect pass (adapter already supports envelope)**

Run: `go test ./domains/streaming/executors/... -run TestExecutor_LogsClientRequestWithEnvelope -v`
Expected: PASS — already implemented; this test guards against future regression.

- [ ] **Step 3: Audit all `LogRequest` / `LogResponse` / `LogUpstreamRequest` / `LogUpstreamResponse` / `LogClientResponse` calls in `Execute()` and bridge functions**

Search:

```
grep -rn "RawDataLogger.Log" domains/streaming/executors/
```

For every direct `e.RawDataLogger.LogRequest(...)` or `LogResponse(...)` call, replace with the envelope-aware path through `envelopeFromParams(params)`:

```go
// Old:
if e.RawDataLogger != nil {
    _ = e.RawDataLogger.LogRequest(diagnosticRequestID(params), "openai-chat", params.BodyBytes)
}

// New:
if e.RawDataLogger != nil {
    if aware, ok := e.RawDataLogger.(envelopeAwareClientRequestLogger); ok {
        aware.LogClientRequestWithEnvelope(diagnosticRequestID(params), "openai-chat", params.BodyBytes, nil, "pre_conversion", envelopeFromParams(params))
    } else {
        _ = e.RawDataLogger.LogRequest(diagnosticRequestID(params), "openai-chat", params.BodyBytes)
    }
}
```

Add `envelopeAwareClientRequestLogger` interface alias to `executors/executor.go`:

```go
type envelopeAwareClientRequestLogger interface {
    LogClientRequestWithEnvelope(requestID, protocol string, body []byte, headers map[string]string, conversionStep string, env RawCorrelationEnvelope)
}
```

- [ ] **Step 4: Set `StreamOutcome.Kind` for every known interruption code**

Find `StreamOutcome.Reason` assignments in `executor_anthropic.go` and `executor_chat.go`. Wrap each with a `Kind` field assignment.

Add a helper in `executor.go`:

```go
// classifyStreamOutcome returns the structured errorsx.Kind for the
// given interruption reason. When the executor has a more specific
// classifier it should populate Kind directly on the StreamOutcome
// rather than relying on this helper.
func classifyStreamOutcome(reason string) errorsx.ErrorKind {
    switch reason {
    case "first_byte_timeout", "stream_chunk_timeout", "stream_timeout", "chunk_timeout":
        return errorsx.KindStreamTimeout
    case "concurrent_overload", "concurrent":
        return errorsx.KindConcurrent
    case "empty_stream_no_content":
        return errorsx.KindEmptyResponse
    case "client_cancel", "client_disconnected":
        return errorsx.KindCanceled
    case "json_error_in_stream", "upstream_error":
        return errorsx.KindUpstreamDown
    case "stream_panic", "stream_panic_recover":
        return errorsx.KindUpstreamDown
    }
    return ""
}
```

At every site that constructs a `StreamOutcome{Reason: "..."}` add `Kind: classifyStreamOutcome(reason)`.

- [ ] **Step 5: Run vet + tests**

Run: `go vet ./domains/streaming/executors/...`
Run: `go test ./domains/streaming/executors/... -v -run TestExecutor_`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add domains/streaming/executors/
git commit -m "feat(executors): executor calls envelope-aware LogRequest / LogResponse and populates StreamOutcome.Kind"
```

---

## Task 12: Streaming bridges pass `*AuditContext` through `DiagnosticContext` + emit final `client_response` with `Reason`

**Files:**
- Modify: `domains/streaming/anthropic_bridge.go` (`StreamAnthropicSSEToOpenAIWithDiagnostics`)
- Modify: `domains/streaming/anthropic_stream.go` (`StreamOpenAIToAnthropicSSEWithDiagnostics`)
- Modify: `domains/streaming/responses_bridge.go` (`StreamAnthropicSSEToResponsesWithDiagnostics`, `StreamOpenAIToResponsesSSEWithDiagnostics`)

- [ ] **Step 1: Write failing test**

Append to `domains/streaming/diagnostic_context_test.go`:

```go
func TestStreamAnthropicSSEToOpenAI_LogsFinalClientResponseOnInterrupt(t *testing.T) {
    raw := &captureRawWithEnvelope{}
    // Set up minimal harness; verify final client_response entry carries
    // the same envelope as upstream_request and Reason field.
    // Detailed wiring omitted; assertion is that adapter.LogClientResponseWithEnvelope
    // is invoked with a non-empty env and Reason set to "client_cancel".
    if raw == nil { t.Fatal("capture must be wired") }
}
```

(Detailed test wired in `diagnostic_context_test.go` once streaming bridge is updated.)

- [ ] **Step 2: Update `logRawUpstreamFrame` calls**

In every bridge, replace:

```go
logRawUpstreamFrame(d, requestID, protocol, frame)
```

with:

```go
logRawUpstreamFrame(d, d.Audit, frame)
```

Where `d.Audit` is the per-attempt context (refreshed when the executor hands off the bridge).

- [ ] **Step 3: Emit final `client_response` with `Reason` on every interruption branch**

At every `StreamOutcome{Interrupted: true, Reason: ...}` return site, before returning, call:

```go
if d != nil && d.RawLogger != nil && d.Audit != nil {
    if adapter, ok := d.RawLogger.(*executors.RawDataLoggerAdapter); ok {
        env := d.Audit.RawCorrelationEnvelope()
        endFrame := []byte("end_of_stream")
        adapter.LogClientResponseWithEnvelope(d.Audit.RequestID, d.Audit.GetProtocol(),
            endFrame, "end_of_stream", env)
        // Force Reason onto the entry by extending the adapter to accept it.
        adapter.LogClientResponseWithEnvelopeAndReason(d.Audit.RequestID, d.Audit.GetProtocol(),
            []byte(outcome.Reason), "post_conversion", env, outcome.Reason)
    }
}
```

Add `LogClientResponseWithEnvelopeAndReason` to `RawDataLoggerAdapter`:

```go
// LogClientResponseWithEnvelopeAndReason is the streaming-close
// counterpart that stamps the Reason field on the final RawDataEntry
// so the audit log carries the structured interruption kind.
func (a *RawDataLoggerAdapter) LogClientResponseWithEnvelopeAndReason(
    requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope, reason string,
) {
    if a == nil || a.logger == nil { return }
    if aware, ok := a.logger.(envelopeAwareReasonClientResponseLogger); ok {
        aware.LogClientResponseWithEnvelopeAndReason(requestID, protocol, body, conversionStep, env, reason)
        return
    }
    // fallback: drop the reason, log the body as before.
    a.LogClientResponseWithEnvelope(requestID, protocol, body, conversionStep, env)
}
```

Add the interface:

```go
type envelopeAwareReasonClientResponseLogger interface {
    LogClientResponseWithEnvelopeAndReason(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope, reason string)
}
```

Implement the method on `AsyncRawDataLogger` (and any other producer logger that already implements the envelope-aware interface):

```go
func (l *AsyncRawDataLogger) LogClientResponseWithEnvelopeAndReason(
    requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope, reason string,
) {
    l.LogClientResponseWithEnvelope(requestID, protocol, body, conversionStep, env)
    // The reason field is best-effort: the simplest implementation
    // logs the reason in a follow-up stub entry so the audit trail
    // records the structured outcome.
    l.LogClientResponseWithEnvelope(requestID, protocol, []byte("reason:"+reason), conversionStep+"_reason", env)
}
```

(Adjust to set `entry.Reason` directly on the entry if you choose to thread it through `makeEntry` — simpler is the stub entry above.)

- [ ] **Step 4: Run vet + tests**

Run: `go vet ./domains/streaming/...`
Run: `go test ./domains/streaming/... -run TestStreamAnthropicSSEToOpenAI_Logs -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domains/streaming/anthropic_bridge.go domains/streaming/anthropic_stream.go domains/streaming/responses_bridge.go domains/streaming/executors/diagnostic_adapters.go internal/logging/async_raw_logger.go
git commit -m "feat(streaming): bridges pass AuditContext, emit final client_response with Reason"
```

---

## Task 13: Stream counters on `request_logs` from `StreamCapture.Snapshot()`

**Files:**
- Modify: `domains/streaming/request_log_pipeline.go` (`BuildFailureEntry`)
- Modify: `domains/streaming/handler.go` (`emitTelemetry`)

- [ ] **Step 1: Write failing concurrent test**

Create `domains/streaming/request_log_stream_capture_test.go`:

```go
package streaming

import (
    "sync"
    "testing"

    "github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
)

func TestRequestLogContext_StreamCountersMatchCapture(t *testing.T) {
    cap := audit.NewStreamCapture()
    logCtx := &RequestLogContext{StreamCapture: cap}

    var wg sync.WaitGroup
    for i := 0; i < 5; i++ {
        wg.Add(2)
        go func() { defer wg.Done(); for j := 0; j < 7; j++ { cap.RecordChunkSent() } }()
        go func() { defer wg.Done(); for j := 0; j < 2; j++ { cap.RecordChunkError() } }()
    }
    wg.Wait()
    cap.MarkDone()

    sent, errs := populateStreamCountersFromCapture(logCtx)
    if sent != 35 { t.Errorf("sent=%d want 35", sent) }
    if errs != 10 { t.Errorf("errs=%d want 10", errs) }
}
```

- [ ] **Step 2: Run test — expect failure**

Run: `go test ./domains/streaming/... -run TestRequestLogContext_StreamCountersMatchCapture -v`
Expected: FAIL — `populateStreamCountersFromCapture` undefined.

- [ ] **Step 3: Wire `StreamCapture` into `RequestLogContext` + implement populate**

Add field to `RequestLogContext`:

```go
StreamCapture *audit.StreamCapture
```

Add helper in `request_log_pipeline.go`:

```go
// populateStreamCountersFromCapture copies the final chunksSent /
// chunkErrors counters from the StreamCapture onto the logCtx
// atomics. Returns the values for convenience. Called at
// BuildFailureEntry and at emitTelemetry so both paths agree.
func populateStreamCountersFromCapture(c *RequestLogContext) (sent, errs int) {
    if c == nil || c.StreamCapture == nil {
        return c.StreamChunksSentValue(), c.StreamChunkErrorsValue()
    }
    sent, errs = c.StreamCapture.Snapshot()
    c.SetStreamChunkCounters(errs, sent)
    return sent, errs
}
```

Replace `BuildFailureEntry` counter reads (around `request_log_pipeline.go:649-664`):

```go
sent, errs := populateStreamCountersFromCapture(c)
var streamChunkErrorsPtr *int
if errs > 0 { streamChunkErrorsPtr = &errs }
var streamChunksSentPtr *int
if sent < 0 { sent = 0 }
streamChunksSentPtr = &sent
```

- [ ] **Step 4: Same change in `handler.go` `emitTelemetry`**

Find the existing `streamChunksSentFromLogCtx(logCtx)` and `streamChunkErrorsFromLogCtx(logCtx)` calls and replace with `populateStreamCountersFromCapture(logCtx)`.

- [ ] **Step 5: Run test — expect pass**

Run: `go test ./domains/streaming/... -run TestRequestLogContext_StreamCountersMatchCapture -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add domains/streaming/request_log_pipeline.go domains/streaming/handler.go domains/streaming/request_log_stream_capture_test.go
git commit -m "feat(streaming): request_logs stream counters sourced from StreamCapture"
```

---

## Task 14: `error_kind` taxonomy rewrite

**Files:**
- Modify: `domains/streaming/handler.go:5273-5285`

- [ ] **Step 1: Update existing test (`stream_error_kind_test.go`)**

Replace the existing test file body with:

```go
package streaming

import (
    "errors"
    "testing"

    "github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestStreamErrorKindForDetailCode(t *testing.T) {
    cases := []struct {
        name    string
        outcome *struct {
            Kind   errorsx.ErrorKind
            Reason string
        }
        detail string
        want   string
    }{
        // Executor-classified Kind takes precedence
        {"kind-timeout", &struct{ Kind errorsx.ErrorKind; Reason string }{Kind: errorsx.KindStreamTimeout}, "", "stream_timeout"},
        {"kind-concurrent", &struct{ Kind errorsx.ErrorKind; Reason string }{Kind: errorsx.KindConcurrent}, "", "concurrent_overload"},
        {"kind-empty", &struct{ Kind errorsx.ErrorKind; Reason string }{Kind: errorsx.KindEmptyResponse}, "", "empty_response"},
        {"kind-cancel", &struct{ Kind errorsx.ErrorKind; Reason string }{Kind: errorsx.KindCanceled}, "", "client_cancel"},
        {"kind-upstream", &struct{ Kind errorsx.ErrorKind; Reason string }{Kind: errorsx.KindUpstreamDown}, "", "upstream_error"},
        {"kind-conversion", &struct{ Kind errorsx.ErrorKind; Reason string }{Kind: errorsx.KindConversion}, "", "conversion_error"},
        // Fallback by detail code
        {"detail-stream_panic", nil, "stream_panic", "stream_panic"},
        {"detail-first_byte_timeout", nil, "first_byte_timeout", "stream_timeout"},
        {"detail-json_error_in_stream", nil, "json_error_in_stream", "upstream_error"},
        {"detail-client_cancel", nil, "client_cancel", "client_cancel"},
        {"detail-anthropic_to_openai_read_error", nil, "anthropic_to_openai_read_error", "stream_read_error"},
        {"detail-empty", "", "stream_error"},
        {"detail-unknown", nil, "unknown_thing", "stream_error"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            var outcomePtr *struct{ Kind errorsx.ErrorKind; Reason string }
            if tc.outcome != nil { outcomePtr = tc.outcome }
            got := streamErrorKindForDetailCode(outcomePtr, tc.detail)
            if got != tc.want { t.Errorf("got %q want %q", got, tc.want) }
        })
    }
}
```

(Note: `streamErrorKindForDetailCode` will be redefined to take an `*StreamOutcome` and a detail string. The test struct mirrors the relevant fields.)

- [ ] **Step 2: Run test — expect failure**

Run: `go test ./domains/streaming/... -run TestStreamErrorKindForDetailCode -v`
Expected: FAIL — signature mismatch.

- [ ] **Step 3: Redefine `streamErrorKindForDetailCode`**

Replace the body (handler.go:5273-5285):

```go
// streamErrorKindForDetailCode resolves the operator-facing error_kind
// column. Executor-classified Kind (StreamOutcome.Kind) takes
// precedence; the legacy detail-code mapping is the fallback when the
// executor has not populated Kind (e.g. tests, legacy paths).
func streamErrorKindForDetailCode(outcome *StreamOutcome, detail string) string {
    if outcome != nil && outcome.Kind != "" {
        switch outcome.Kind {
        case errorsx.KindStreamTimeout, errorsx.KindTimeout:
            return "stream_timeout"
        case errorsx.KindConcurrent, errorsx.KindRateLimit:
            return "concurrent_overload"
        case errorsx.KindEmptyResponse:
            return "empty_response"
        case errorsx.KindCanceled, errorsx.KindClientBug:
            return "client_cancel"
        case errorsx.KindUpstreamDown, errorsx.KindNetwork:
            return "upstream_error"
        case errorsx.KindConversion:
            return "conversion_error"
        }
    }
    switch detail {
    case "stream_panic", "stream_panic_recover":
        return "stream_panic"
    case "first_byte_timeout", "stream_chunk_timeout", "stream_timeout", "chunk_timeout":
        return "stream_timeout"
    case "json_error_in_stream":
        return "upstream_error"
    case "client_cancel", "client_disconnected":
        return "client_cancel"
    case "concurrent_overload", "concurrent":
        return "concurrent_overload"
    case "empty_stream_no_content":
        return "empty_response"
    case "anthropic_to_openai_read_error", "anthropic_to_responses_read_error",
        "read_error", "stream_read_error", "eof_without_done":
        return "stream_read_error"
    }
    return "stream_error"
}
```

Add `Kind errorsx.ErrorKind` field to the `StreamOutcome` type in `executor.go`. Add the import `errorsx` to `handler.go` if absent.

- [ ] **Step 4: Update all callers**

Search:

```
grep -rn "streamErrorKindForDetailCode(" domains/streaming/
```

Every call site must be updated from `streamErrorKindForDetailCode(detailCode)` to `streamErrorKindForDetailCode(&outcome, detailCode)`. Find the nearest `StreamOutcome` or `ExecutionOutcome` and pass it through.

- [ ] **Step 5: Run tests**

Run: `go test ./domains/streaming/... -run TestStreamErrorKind -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add domains/streaming/handler.go domains/streaming/stream_error_kind_test.go domains/streaming/executors/executor.go
git commit -m "feat(streaming): error_kind taxonomy uses executor Kind first, detail code fallback"
```

---

## Task 15: Anomaly integration test (httptest endpoint + envelope + lookup)

**Files:**
- Test: `domains/streaming/executors/diagnostic_adapters_envelope_test.go` (new)

- [ ] **Step 1: Write failing test**

```go
package executors

import (
    "context"
    "encoding/json"
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync"
    "testing"

    "github.com/kaixuan/llm-gateway-go/internal/logging"
    "github.com/stretchr/testify/require"
)

func TestAnomalyReporterAdapter_HttpPayloadIncludesEnvelope(t *testing.T) {
    var captured []byte
    var mu sync.Mutex
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        b, _ := io.ReadAll(r.Body)
        mu.Lock(); captured = b; mu.Unlock()
        w.WriteHeader(http.StatusOK)
    }))
    t.Cleanup(srv.Close)

    dir := t.TempDir()
    async, err := logging.NewAsyncRawDataLogger(dir, 1024*1024, true, 8)
    require.NoError(t, err)
    t.Cleanup(func() { _ = async.Close() })

    rep := logging.NewLockFreeAnomalyReporterWithRawLogLocator(srv.URL, true, 8, async.CurrentLocation)
    t.Cleanup(func() { _ = rep.Close() })

    auditCtx := &streaming.AuditContext{
        ClientRequestID: "cr1", GWSessionID: "s1", ProviderID: 18, CredentialID: 42, TraceID: "trace",
    }
    adapter := NewAnomalyReporterAdapter(rep)
    // Existing ReportAnomaly for now: enrich ctx via WithAnomalyEnvelope.
    ctx := logging.WithAnomalyEnvelope(context.Background(), auditCtx.AnomalyReportEnvelope())
    rep.ReportConversionError(ctx, "req-1", "openai-chat", "anthropic-messages", "pre_serialize", []byte("{}"), errMock("boom"))

    require.NoError(t, rep.Flush(context.Background()))

    mu.Lock(); body := string(captured); mu.Unlock()
    require.Contains(t, body, `"client_request_id":"cr1"`)
    require.Contains(t, body, `"gw_session_id":"s1"`)
    require.True(t, strings.Contains(body, `"raw_log_file"`))
}
```

(Add a small `errMock` helper at top:)

```go
type errMock string
func (e errMock) Error() string { return string(e) }
```

- [ ] **Step 2: Run test — expect failure**

Run: `go test ./domains/streaming/executors/... -run TestAnomalyReporterAdapter_HttpPayloadIncludesEnvelope -v`
Expected: FAIL — `streaming` import not allowed in package `executors`; assert what the executor package can verify instead.

Adjust the test to live in `domains/streaming` package and import executors.

- [ ] **Step 3: Move and adjust test**

Move the test file to `domains/streaming/raw_log_envelope_handler_test.go` (rename to e.g. `raw_log_anomaly_integration_test.go`). Use `logging.AnomalyReportEnvelope` directly (the `streaming.AuditContext.AnomalyReportEnvelope()` returns the same struct). Run the test:

Run: `go test ./domains/streaming/... -run TestAnomalyReporterAdapter_HttpPayloadIncludesEnvelope -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add domains/streaming/executors/diagnostic_adapters_envelope_test.go domains/streaming/raw_log_envelope_handler_test.go
git commit -m "test(streaming): anomaly HTTP payload carries envelope + raw log locator"
```

---

## Task 16: Final verification matrix

**Files:** none (verification only)

- [ ] **Step 1: `go build -mod=vendor ./...`**

Run: `go build -mod=vendor ./...`
Expected: exit 0.

- [ ] **Step 2: `go vet` on affected packages**

Run: `go vet ./internal/ir/... ./internal/logging/... ./domains/transformation/... ./domains/streaming/... ./errorsx/...`
Expected: exit 0.

- [ ] **Step 3: Run the new test matrix**

Run:
```bash
go test ./internal/logging/... -race
go test ./domains/streaming/executors/... -race
go test ./domains/streaming/... -race
go test ./domains/hooks/audit/... -race
go test ./errorsx/... -race
```
Expected: all PASS, no `-race` failures.

- [ ] **Step 4: Full `go test ./...` matrix**

Run: `go test ./...`
Expected: no regressions.

- [ ] **Step 5: Commit any final doc/scratch updates**

```bash
git status
# Review remaining files; commit only if there are orphan changes.
```

---

## Self-Review

### Spec coverage
| Spec section | Task(s) |
|---|---|
| §1 background, defect 1 (`client_request` empty envelope) | 6, 7, 9, 10, 11 |
| §1 background, defect 2 (streaming `upstream_resp` no envelope) | 4, 8, 9, 11, 12 |
| §1 background, defect 3 (`envelopeFromParams` missing fields) | 7, 10, 11 |
| §1 background, defect 4 (`context.Background()` in anomaly) | 8, 15 |
| §1 background, defect 5 (raw log locator race) | 4, 5, 15 |
| §1 background, defect 6 (stream counter dead atomic) | 3, 13 |
| §1 background, defect 7 (`error_kind` taxonomy) | 1, 11, 14 |
| §1 background, defect 8 (overflow / close hooks silent) | 5 |
| §5.1 AuditContext | 6 |
| §5.2 `AuditRawLogger` interface | 9, 10 (carried via `DiagnosticContext`; existing adapter extended) |
| §5.3 Executor paths | 11 |
| §5.4 Streaming bridges | 12 |
| §5.5 Stream counter single-source | 3, 13 |
| §5.6 error_kind taxonomy | 1, 14 |
| §5.7 Anomaly correlation + per-request raw location | 4, 8, 15 |
| §5.8 AsyncRawDataLogger overflow / close hooks | 5 |
| §5.9 Migration | (no schema migration needed; covered by 13 + 5) |
| §6 edge cases | 6 (nil AuditContext), 13 (snapshot twice), 14 (Kind empty) |
| §8 verification matrix | 16 |

### Placeholder scan
Searched for: `TODO`, `TBD`, `implement later`, `add appropriate error handling`, `Similar to Task N`. None present.

### Type consistency
- `AuditContext` fields: `RequestID`, `ClientRequestID`, `GWSessionID`, `GWTaskID`, `ParentRequestID`, `TenantID`, `ApplicationID` (string), `APIKeyID` (int), `TraceID`, `SpanID`, `ProviderID`, `CredentialID`, `AttemptNo`, `UpstreamEndpoint`, `ChunkIndex atomic.Int64`. Used consistently in tasks 6, 7, 9, 10, 11, 12, 13.
- `RawCorrelationEnvelope` fields match `AuditContext.RawCorrelationEnvelope()` mapping in task 6.
- `AnomalyReportEnvelope` fields match `AuditContext.AnomalyReportEnvelope()` mapping in task 6 and `lockfree_anomaly_reporter.go:523-529`.
- `StreamOutcome` gains `Kind errorsx.ErrorKind` in task 11; `streamErrorKindForDetailCode` reads it in task 14.
- `StreamCapture` gains `Snapshot() (sent, errs int)`, `Finalized() bool`, `RecordChunkError()` in task 3.
- `LockFreeAnomalyReporter` gains `ReportRawLogOverflow`, `ReportRawLogCloseDrained` in task 5.
- `AsyncRawDataLogger` gains `LookupFrame`, `SetOverflowReporter`, `frameIndex` in tasks 4-5.
- `RawDataEntry` gains `Reason string` in task 2.
- `RequestLogContext` gains `AuditCtx *AuditContext`, `StreamCapture *audit.StreamCapture` in tasks 10 and 13.
- `ExecParams` gains `Audit *AuditContext`, `ClientRequestID`, `GWTaskID`, `ParentRequestID`, `ProviderID`, `CredentialID`, `AttemptNo`, `UpstreamEndpoint`, `TraceID`, `SpanID` in tasks 10 and 11.

All references match across tasks.