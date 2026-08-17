# 2026-07-19: System health worker NULL scan fix

## What

Fixed `system_health_worker: query failed` on 154 production — `cannot scan NULL into float64` for `success_rate` column.

## Root cause

`system_health_status(30)` returns `NULL` for `success_rate` when the 30s window has zero requests (status = `'suspect'`). The Go scan variable was `float64`, which pgx cannot scan NULL into — it requires `*float64`.

## Fix

- `bg/system_health.go`: changed `successRate` scan type from `float64` to `*float64`, added nil → `0.0` fallback before storing into `atomic.Value`
- `domains/streaming/handler.go`: fixed `isRetriableError` to use existing `errorsx` constants (`KindUpstreamDown`, `KindTransient`, `KindModelNotFound`) instead of non-existent ones

## Verification

- `go build ./...` — pass
- `go vet ./...` — pass

## Affected versions

Build 1171+ (154), not yet deployed. 245 (build 1181) unaffected by this specific NULL scan but will receive the fix as part of the next build bump.
