# Request Body Storage Phase 1

## Summary

Moved new `outbound_body` persistence to `request_logs_bodies_hot` so the wide
`request_logs_hot` row no longer receives a second TOAST/WAL copy. The legacy
`request_logs_hot.outbound_body` column remains during the compatibility window.

## Scope

- Telemetry insert/update paths write `outbound_body` only to the dedicated body table.
- Body rows receive `tenant_id` for scoped lifecycle cleanup.
- Admin body lookup, log detail, compression statistics, compression sessions,
  session comparison, and blob cleanup read the dedicated body side table.
- Installer embed and dbinit startup lists include migration 600.

## Compatibility And Rollback

- Existing `request_logs_hot.outbound_body` data is not deleted.
- Existing readers can continue to use the legacy column during rollback.
- Rollback is application-first: deploy the previous release to restore legacy
  writes. The migration down file is intentionally non-destructive because the
  added `tenant_id` is compatible metadata.

## Verification

- `go test ./admin/... ./domains/hooks/observability/telemetry/... -count=1`
- `go vet ./...`
- `go build ./...`
- `cd installer && go test ./... -count=1`
- `cd installer && go build ./...`
- Canonical migration and installer embed checksum/content comparison passed.
- Full root `go test ./...` was attempted; unrelated pre-existing failures were
  observed in `domains/hooks/TestConfigManagerWatch` and
  `plugin-runtime/TestExecCommand_StartsRealProcess`.
