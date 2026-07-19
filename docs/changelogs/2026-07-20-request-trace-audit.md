# Request Trace Audit Fix

## Findings

- Stage-event transaction commit failures were logged and discarded.
- A failed stage-event insert did not stop the transaction, allowing partial writes.
- `FlushToPG` deleted the Redis trace after stage-event persistence failed, preventing retry.
- Hot/partition fallback queries did not deterministically select the newest duplicate row.
- The repository did not contain the migration for `request_stage_events.tenant_id`, although the writer required it.
- `RegisterRoutes` exposed trace endpoints without authorization when the caller passed a nil wrapper.

## Fixes

- Fail fast on marshal or insert errors and return commit errors with request context.
- Keep the Redis trace when normalized stage-event persistence fails.
- Order fallback queries by `ts DESC NULLS LAST`.
- Add idempotent migration `450_request_stage_events_tenant.sql` and rollback script.
- Return `503` instead of registering an unauthenticated trace route when authorization is missing.

## Verification

- `gofmt -w internal/trace/stage_events.go internal/trace/trace.go admin/request_trace.go`
- `go build ./...`
- `go vet ./internal/trace/... ./admin/...`
- `go test ./internal/trace/... ./admin/...`
