# Stage C.3 — `model` label removed from `dispatch_*` metrics

**Date**: 2026-08-22
**Scope**: Stage C of ADR-0003 (Dispatch governor backend abstraction)
**Author**: dispatch governance round

## Summary

The two Tier-1 model queue metrics lose their `model` label and become
aggregated series:

| Metric | Before | After |
|---|---|---|
| `dispatch_model_queue_depth` | `{model}` | `{}` |
| `dispatch_model_queue_wait_seconds` | `{model}` | `{}` |

Per-model visibility is moved to a `slog.Debug` emission at the same
emission site (model enqueue, model dequeue, model dequeue wait).
Operators investigating a hot model can flip the gateway to
`-log-level=debug` to see individual records.

## Why

The Stage A cardinality guard scaffold (test scaffold only, never
enforced) explicitly listed `model` on the forbidden-exact list:
"model name space is large; use 'provider' instead". Stage C.1 flipped
that guard to enforcement; leaving the existing `model`-labelled series
in place would have made `TestNoHighCardinalityLabels` red on any
`go test ./...` run that pulls dispatch into the metric registry.

## Operational impact

| Surface | Impact |
|---|---|
| Prometheus / Grafana | Any panel / alert using `by (model)` on `dispatch_model_queue_*` series will silently miss data. Update to `sum without (model)` (or omit the dimension). |
| Alerting | Per-model queue-saturation alerts that key on `dispatch_model_queue_depth > N` by model name need to be re-authored at the aggregated level OR replaced with a debug-log-based watcher. |
| Tracing | Per-model drill-down is still available in `slog.Debug` records (`dispatch: model enqueue`, `dispatch: model dequeue`, `dispatch: model dequeue wait`); these are NOT emitted at INFO level to keep production log volume stable. |
| Capacity planning | The model dimension was already imprecise (vendor + alias explosion risk); the aggregated series is a closer approximation of "Tier-1 backpressure". |

## Migration checklist (operator runbook)

1. **Before merging** this change to a deployed environment, audit
   dashboards / alert rules for any reference to
   `dispatch_model_queue_depth` or `dispatch_model_queue_wait_seconds`.
   Run:

   ```promql
   # Find panels / alerts keying on the dropped label:
   {__name__=~"dispatch_model_queue_(depth|wait_seconds).*"}
   ```

2. **Update** each panel:
   - Per-model breakdown: switch to `sum by (le) (rate(dispatch_model_queue_wait_seconds_bucket[5m]))`
     for the aggregated histogram, and `dispatch_model_queue_depth` (now a single
     series) for instantaneous depth.
   - Per-model saturation alerts: either drop the per-model qualifier
     (alert on aggregate depth), or replace with a debug-log watcher
     that tails Loki for `model=<name>` matches.

3. **Verify** in staging by running the production traffic replay for
   ~30 minutes. The aggregated series should NOT show any data points
   with `model` label still attached:

   ```promql
   count(dispatch_model_queue_depth{model!=""})
   # expected: 0
   ```

4. **Communication**: this is a coordinate break. Any operator who
   queried the `model` label previously will see no data. Coordinate
   the rollout with the on-call SRE rotation.

## Backout

This change is a single revert. The previous `model`-labelled metrics
are restored by `git revert <commit>`; no migration of dashboard data
is required because Prometheus stores the labelled series indefinitely.

## Validation

- `go test ./domains/dispatch/... -race` — all green.
- `go test ./metrics/... -race` — `TestNoHighCardinalityLabels` green
  (the prior violation is now resolved).
- New `domains/dispatch/metrics_model_label_test.go` regression test
  enforces no `model` label survives on the dispatched series.
- README in `domains/dispatch/README.md` updated to describe the
  aggregated semantics.