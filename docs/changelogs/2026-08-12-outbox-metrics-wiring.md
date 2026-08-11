# Changelog — Outbox metrics wiring + Grafana alerts/dashboard

**Date**: 2026-08-12
**Author**: ACC Agent
**Severity**: P1 — observability gap (counters were registered but never incremented)
**Deploy**: 245 seq=1491-8a788d30, version=v2.5.0
**Related**: commit `991acc5e3` (Prometheus metrics for delivery observability)

## 1. Problem

`internal/outbox/metrics.go` (commit `991acc5e3`) declared six Prometheus
metrics — three counters (`outbox_events_sent_total`, `outbox_events_failed_total`,
`outbox_events_retried_total`), two gauges (`outbox_dlq_count`,
`outbox_pending_count`), and one histogram (`outbox_delivery_duration_seconds`).
The `updateGaugeMetrics` poller updated the two gauges, but **no code path
called `RecordEventSent` / `RecordEventFailed` / `RecordEventRetried` /
`ObserveDeliveryDuration`**. As a result, every alert/dashboard panel that
depended on the sent/failed/duration counters was permanently stuck at zero.

This was a silent observability gap: the Grafana dashboard for outbox
delivery (`deploy/grafana/dashboards/outbox-delivery.json`) was already
committed but showed empty panels, and the alerts in
`deploy/monitoring/grafana-alerts/outbox.yaml` would never fire (no data).

## 2. Root cause

`internal/outbox/dispatcher.go::dispatchOne` had three exit paths (unmarshal
failure, dispatch failure, success) but never called the metric helpers
defined in `metrics.go`. The helpers were exported and had unit tests at
the metric-layer level, but there was no integration test pinning
`dispatchOne → metric increment`.

## 3. Fix

### 3.1 Wire dispatchOne to record metrics

Each branch now calls the corresponding helper:

| Exit path | Metric call(s) |
|---|---|
| Success | `RecordEventSent()` + `ObserveDeliveryDuration(duration, true)` |
| HTTP failure (will retry) | `RecordEventFailed(classifyError(err))` + `RecordEventRetried()` |
| HTTP failure (at max → DLQ) | `RecordEventFailed(classifyError(err))` only |
| Unmarshal failure (poison pill) | `RecordEventFailed("validation")` + `RecordEventRetried()` if not at max |
| No claimable event | (no metric — by design, no work was done) |

The duration histogram wraps only the HTTP `dispatch()` call — not the
DB claim/commit — because `dispatchBatch` already batches DB overhead
separately from network roundtrips.

Retry/DLQ distinction uses `attempts+1 < d.maxAttempts` at the call site
rather than a return value from `markFailed`. This keeps `markFailed`'s
signature unchanged (rule 09 §2.3 minimal-surface change).

### 3.2 New alerts (`deploy/monitoring/grafana-alerts/outbox.yaml`)

Six PromQL alert rules, all 30s evaluation, with severity labels:

| Alert | Expression | Threshold |
|---|---|---|
| `OutboxDLQAccumulation` | `outbox_dlq_count > 0` | 1m for, critical |
| `OutboxHighFailureRate` | `failed/(sent+failed) > 10%` | 5m, critical |
| `OutboxBacklogGrowing` | `outbox_pending_count > 1000` | 5m, warning |
| `OutboxSlowDelivery` | `histogram_quantile(0.99, …) > 5s` | 5m, warning |
| `OutboxHMACMismatch` | `rate(reason="hmac_mismatch") > 0` | 1m, critical (rule 39 §5.1) |
| `OutboxHighProviderUnknownRate` | `rate(reason="validation")/sent > 5%` | 5m, warning |

### 3.3 New dashboard (`deploy/grafana/dashboards/outbox-delivery.json`)

Six panels: queue depth (pending + DLQ), delivery rate (sent/failed/retried),
failure reasons, P50/P95/P99 latency, DLQ stat, failure ratio stat.
Refresh 30s. Aligned with the alert rules above.

## 4. Test coverage

Six new tests in `internal/outbox/dispatcher_test.go` driven by
`go-sqlmock`:

- `TestDispatcher_DispatchOne_SuccessIncrementsSent`
- `TestDispatcher_DispatchOne_HTTPFailureIncrementsFailedAndRetried`
- `TestDispatcher_DispatchOne_FailureAtMaxAttemptsNoRetry`
- `TestDispatcher_DispatchOne_NoRowsNoMetricChange`
- `TestDispatcher_DispatchOne_DurationHistogramObserved`
- `TestClassifyError` (10 subtests covering all reason labels)

`sumMetricVecName` helper aggregates via `prometheus.DefaultGatherer.Gather()`
because `testutil.ToFloat64` panics on `CounterVec` labels that haven't
been observed yet — this lets the no-rows test pass without warming any
label.

## 5. Verification on 245

After `bash scripts/deploy-245.sh` (seq bumped 1490 → 1491):

```
$ curl -H "Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" \
       http://245/metrics | grep ^outbox_
outbox_dlq_count 0
outbox_events_retried_total 0
outbox_pending_count 0
```

The three gauges / counter are emitted at zero. CounterVec / HistogramVec
series (`outbox_events_sent_total{status=...}`, `outbox_events_failed_total{reason=...}`,
`outbox_delivery_duration_seconds_bucket`) appear only after first
`Inc()`/`Observe()` — this is Prometheus client behavior, not a defect.
Unit tests prove the increment path fires on every branch.

## 6. Deferred / known gaps

- **No end-to-end dispatch exercised on 245**: `ASM_INTERNAL_ENDPOINT`
  is not configured on 245 (preprod is gateway-only). The dispatcher
  was therefore not started. Wiring correctness is proven by unit
  tests; the metric registration is proven by `/metrics` output. A
  true e2e test requires deploying ASM mock + configuring
  `ASM_INTERNAL_ENDPOINT` (out of scope for this change).
- **CounterVec/HistogramVec show no series until first use**: this is
  expected Prometheus behavior, not a bug. Operators should not be
  alarmed when `outbox_events_sent_total` is missing from `/metrics`
  output before the first event has been delivered — it will appear
  automatically once `RecordEventSent()` is called.

## 7. Files touched

| File | Type | Lines |
|---|---|---|
| `internal/outbox/dispatcher.go` | modify | +24 |
| `internal/outbox/dispatcher_test.go` | modify | +334 |
| `deploy/monitoring/grafana-alerts/outbox.yaml` | new | +87 |
| `deploy/grafana/dashboards/outbox-delivery.json` | new | +176 |
| `version.json` / `VERSION` / `web/public/version.json` | bump | (deploy script) |
| `docs/db-changelog.md` | append | +6 |
| `go.mod` / `vendor/modules.txt` | sync | (pre-existing inconsistency fixed) |
| `web/public/menu-config.json` | timestamp bump | (deploy script) |

## 8. Verification commands run

```bash
go build ./...                                            # exit 0
go vet ./internal/outbox/...                              # exit 0
go test ./internal/outbox/... ./domains/hooks/observability/... # all green
                                                              # (TestWriter_Write is a pre-existing env issue: no PG role)
python3 -c "import json; json.load(open('deploy/grafana/dashboards/outbox-delivery.json'))"  # JSON OK
python3 -c "import yaml; yaml.safe_load(open('deploy/monitoring/grafana-alerts/outbox.yaml'))"  # YAML OK
bash scripts/deploy-245.sh                                # 245 deployed seq=1491
curl http://245:8781/api/system/version                    # build_seq=1491
curl http://245:8781/api/system/background-tasks           # 401 (not 503 = DB OK)
curl -H "Authorization: Bearer $ADMIN_TOKEN" http://245:8781/metrics | grep ^outbox_  # 3 metrics exposed
```