# ADR: `success && response_body missing` observability

- **Status:** Accepted
- **Date:** 2026-08-29
- **Scope:** telemetry `request_logs` settlement path; PromQL alert surface
- **Origin:** `.handoff/2026-08-28-followup-audit-closeout.md` §5.5

## Context

The gateway classifies each request as `success=true` when the upstream
returned a usable response and the streaming / non-streaming pipeline
reached settlement. The audit row in `request_logs` carries the response
body in a separate hot table
(`request_logs_bodies_hot`, since 2026-07-22) and exposes a
`has_response_body` slog label at `client.go:1481` — but that label only
fires on the **bodies-hot write failure** path, not on the
`success=true && ResponseBody == nil` case itself.

The pre-existing gap: a request can be classified as successful even when
its audit row is bodyless. The most common causes are

1. the streaming reassembly path returning empty
   (`reassembleStreamBody(streamCapture)` at `handler.go:4934`)
2. a transformer side-effect that nulls the body in `request_bodies_hot`
3. a redaction path that over-empties the field

None of these alert today; the only signal is `has_response_body=false`
in a `slog.Error` that fires only when the hot-table write itself fails.

## Decision

Adopt a single Prometheus counter
`telemetry_success_response_body_missing_total{protocol,stream}` plus a
`WARN`-level slog emission, both fired by the
`persistRequestLog → onPersisted` hook:

```
domains/hooks/observability/telemetry/empty_response_metrics.go
  recordEmptyResponseBody(entry *RequestLogEntry) → Counter.Inc() + slog.Warn
  RegisterEmptyResponseGate(*Client) wires the hook once per process
  cmd/gateway/main.go:1947 calls RegisterEmptyResponseGate right after
    telemetry.NewClient().
```

### Classification contract

The gate increments the counter when ALL of the following hold:

- `entry.Success == true`
- `entry.ResponseBody == nil || all-whitespace`
- `entry.StreamChunkCount == nil || *StreamChunkCount == 0` (i.e. the
  request was non-streaming)
- The gate is enabled (production wiring flips it on at startup;
  tests can opt in or out per-case)

The streaming bucket is recorded separately (the `stream` label) for
ratio analysis but never alerted on — a stream with `chunk_count > 0`
already proves success at the byte level; an empty body is the expected
shape (the bytes live in `stream_capture` / `outbound_body`, not in
`response_body`).

### Label cardinality

```
protocol ∈ {chat, responses, anthropic_messages, unknown}    (bounded)
stream   ∈ {non_stream, stream}                               (derived)
```

All six `non-unknown` cells are pre-initialised at boot so dashboards
observe a stable surface from process start (matches the audit
`P2-3` convention used by `sanitizeEventsTotal`).

### Settlement invariant

The hook fires AFTER `insertRequestLog` / `updateRequestLog` commits and
BEFORE `releaseBodies()` zeroes the bodies. A panic inside
`recordEmptyResponseBody` is recovered locally AND by the existing
`persistRequestLog` defer — the counter increment failure must not
affect settlement or hot-body writes.

The gate runs in the telemetry worker goroutine and is bounded
(`Success && has_response_body_missed`); per-request cost is one
`strings.IndexFunc`-style classifier + one counter increment + one
`slog.Warn` line. Well below the 100 µs ceiling applied to other
onPersisted hooks.

## Alternatives considered

- **Augment the existing `has_response_body` slog label with a counter
  pair.** Rejected: the existing label fires on the wrong path (write
  failure, not the row state). Mixing two semantics on one label
  surfaces the wrong question in dashboards.
- **Add a derived Prometheus gauge from `request_logs`.** Rejected: the
  cardinality of `tenant_id × request_id` and the 5-minute scrape
  cadence cannot surface low-rate regressions. An event-driven counter
  on the settlement path is the correct surface.
- **Block the row INSERT when the body is empty.** Rejected: this would
  regress the streaming path (where empty `response_body` is the
  correct, expected shape) and re-introduce the hot-body-only SSOT
  violation called out in 2026-07-22.

## Consequences

The alert surface is `(rate(non_stream bucket) > 0 over 5m)`. A
healthy gateway with N streaming + M non-streaming successes reports
**zero** in the `non_stream` bucket. Any non-zero rate is a body-loss
regression worth a page.

Test coverage in
`domains/hooks/observability/telemetry/empty_response_metrics_test.go`
pins:

- `Success && empty body → counter++` (the alertable surface)
- `Success && non-empty body → no increment`
- `Success && empty body && StreamChunkCount > 0 → stream bucket only,
  non_stream bucket untouched`
- `!Success → no increment regardless of body`
- gate-disabled path is a no-op (production wiring boundary)

The pre-init label list and the whitespace-only classifier are
tested directly so a future contributor cannot accidentally break the
contract by removing either.

## References

- `.handoff/2026-08-29-section5-recheck.md` §3.5 / §4.4
- `.handoff/2026-08-28-followup-audit-closeout.md` §5.5
- `domains/hooks/observability/telemetry/empty_response_metrics.go` (this ADR)
- `domains/hooks/observability/telemetry/empty_response_metrics_test.go`
- `domains/hooks/observability/telemetry/client.go:1481` (existing
  `has_response_body` label — kept for the write-failure path)
- `cmd/gateway/main.go:1947` (gate wiring at startup)
