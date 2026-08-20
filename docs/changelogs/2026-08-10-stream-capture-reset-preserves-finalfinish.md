# Changelog — StreamCapture.Reset preserves finalFinish (probe_timeout cross-retry)

**Date**: 2026-08-10
**Author**: ACC Agent
**Severity**: P1 — 154 production observable misclassification
**Related**: commit `0f55458a0` (probe-side classification), commit `d63331789` (wire StreamCapture to logCtx)

## 1. Problem

Production logs on 154 (deployed version `2.5.0-93bbd43a`, includes both
`0f55458a0` and `d63331789`) continue to emit
`probe-client_cancel-cred<N>-...` rows with empty `upstream_finish_reason`,
even for requests where the upstream first byte takes >30s (client gives
up). Symptom example:

```
probe-client_cancel-cred21-1786356586451122082 | client_cancel | (null) | (null) | 2026-08-10 18:09:46
```

The user-visible conclusion ("this is supplier first-byte timeout, should
be probe_timeout") does not match the database row.

## 2. Root cause

`domains/streaming/executors/executor.go:2375` calls
`params.Capture.Reset()` **between credential failover attempts**:

```go
if params.IsStream && params.Capture != nil && tried > 1 {
    params.Capture.Reset()
}
```

`Reset()` previously cleared `finalFinish` along with the chunk counters,
checksums, textContent, etc. End-to-end flow:

1. Attempt 1 (credential A): first byte times out.
   - `stream.go:509` → `capture.MarkInterruptedWithReason("first_byte_timeout")`
     → `finalFinish = "first_byte_timeout"`.
   - `outcome.Kind = KindStreamTimeout`, `Resumable = true` (zero chunks).
2. Executor outer loop catches `streamInterruptedError`, continues to
   the next candidate.
3. Attempt 1's `Reset()` wipes `finalFinish` to `""`.
4. Attempt 2 (credential B): succeeds quickly, stream completes normally.
   - Successful `ObservePayload(..., "stop", ...)` overwrites
     `finalFinish`, so preserving that field alone still loses the timeout.
5. But the client already disconnected during step 1's wait → the
   deferred `emitClientDisconnectProbe` in `serveHTTPInner` runs with
   `r.Context().Err() == context.Canceled`.
6. `buildClientDisconnectProbeEntry` reads
   `logCtx.StreamCapture.SummaryAsMap()["upstream_finish_reason"]` →
   finds it empty (wiped in step 3).
7. Falls back to `errors.Is(ctxErr, context.DeadlineExceeded)` →
   `Canceled` does not match → `errorKind = "client_cancel"`.

Result: `probe-client_cancel-cred<N>-...` is written to
`request_logs_hot`, even though the original failure was an upstream
first-byte timeout.

## 3. Fix

The completed fix separates the current finish reason from cross-attempt
failure evidence:

### 3.1 `StreamCapture.Reset()` — preserve `finalFinish`

Removed `sc.finalFinish = ""` from the Reset body and added a dedicated
`supplierTimeoutReason` latch. `MarkInterruptedWithReason` writes the current
finish field and latches the first supplier timeout;
normal finish events may update `finalFinish`, but do not erase the latched
failure. All per-attempt counters remain reset.

The reason is documented inline with a `// KEEP:` marker per the team's
dead-code/value-retention rules.

### 3.2 `isInterruptionCode()` — include `first_byte_timeout` etc.

Added `first_byte_timeout`, `stream_chunk_timeout`, `chunk_timeout` to
the interruption-code whitelist. Previously the whitelist omitted
these, so even when `finalFinish = "first_byte_timeout"` was correctly
written to `upstream_finish_reason`, `failure_detail_code` was left
NULL. Now they map to each other consistently.

### 3.3 Probe classification reads failure evidence first

`buildClientDisconnectProbeEntry` now checks `failure_detail_code` before
falling back to `upstream_finish_reason`. A successful retry can therefore
record `stop` without hiding the earlier supplier timeout.

### 3.4 Regression tests

Pins the cross-retry contract: `MarkInterruptedWithReason(...)` +
`Reset()` still leaves `upstream_finish_reason` (and `failure_detail_code`)
populated. It also verifies that counters are cleared and a subsequent
successful `ObservePayload` records `upstream_finish_reason=stop` while
`failure_detail_code=first_byte_timeout` remains latched. The handler-level
`TestBuildClientDisconnectProbeEntry_FirstByteTimeoutAfterSuccessfulRetry`
reproduces the full classification path.

## 4. Behavior after fix

For the original failure pattern (first byte timeout → retry success →
client cancel):

- `probe-client_cancel-cred<N>-...` row is now written with
  `error_kind = "probe_timeout"` (was: `client_cancel`).
- `upstream_finish_reason = "stop"` when the retry completed normally.
- `failure_detail_code = "first_byte_timeout"` (was: `null`).

Sibling credentials get the proper failure signal via the executor's
failure bookkeeping (already wires `KindStreamTimeout` since attempt 1),
so circuit breaker feedback is unaffected. The probe row is the only
public surface that was wrong; the credential health records were
already correct.

## 5. Verification

- Unit: `go test ./domains/hooks/audit/...` PASS (incl. new test)
- Unit: `go test ./domains/streaming/...` PASS (all probe classifier
  tests, incl. `TestBuildClientDisconnectProbeEntry_FirstByteTimeout`
  and `TestBuildClientDisconnectProbeEntry_NonTimeoutReason`).
- `go build ./...` clean
- `go vet ./domains/hooks/audit/... ./domains/streaming/...` clean
- Post-deploy verification on 154 (TBD after deploy): DB query
  `SELECT request_id, error_kind, upstream_finish_reason FROM
  request_logs_hot WHERE request_id LIKE 'probe-%' ORDER BY ts DESC
  LIMIT 20` should show `probe-probe_timeout-cred<N>-...` rows with
  `upstream_finish_reason = 'first_byte_timeout'`.

## 6. Risks / out of scope

- The supplier-timeout latch is request-scoped and intentionally survives credential
  retries; a new request receives a new `StreamCapture`.
- No change to the executor's retry policy or to credential
  health bookkeeping.
- The pre-existing `fix(streaming): improve error classification` and
  `fix(streaming): wire executor StreamCapture into probe context`
  commits remain the foundation; this commit completes the third
  piece (cross-retry preservation) that the previous two relied on
  implicitly.
