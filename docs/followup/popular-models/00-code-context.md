# Popular Models Follow-up Code Context

## Entry Points
- `admin/routing.go:handleRoutingAvailableModels` returns the admin picker payload.
- `admin/telemetry.go:persistRequestLog` records successful request models.
- `admin/live_stream_sse.go:overlaySnapshotTerminalStatuses` corrects stale SSE tile states.

## Current Flow
1. Routing reads featured policy models, Redis live lanes, one global recent-model ZSET, then `request_logs_hot`.
2. Telemetry writes a successful request log and increments the global ZSET.
3. SSE queries terminal tile states and updates in-memory snapshots.

## Relevant Contracts
- `request_logs_hot.tenant_id` is `NOT NULL`; baseline has `(tenant_id, ts DESC)` index.
- Tenant admin scope comes from `EffectiveTenantID`; super-admin uses an empty all-tenant scope.
- Policy entries precede popularity entries and must retain that order.
- SQL uses a Go-bound cutoff argument, not `NOW() - INTERVAL`.

## Files
| Change | File |
|---|---|
| Tenant-specific recent/usage models | `admin/routing.go`, `admin/telemetry.go` |
| Lookup-window configuration | `admin/popular_models_config.go` |
| Overlay outcome metric | `metrics/live_stream_overlay_metrics.go`, `admin/live_stream_sse.go` |
| Regression coverage | `admin/routing_popular_models_test.go`, `admin/live_stream_sse_test.go` |
