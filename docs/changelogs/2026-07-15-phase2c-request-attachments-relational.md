# 2026-07-15 - Multimodal Phase 2C (request_attachments relational)

## Scope

Phase 2C phase 1: lift `request_logs.attachments` JSONB into a first-class
relational table for indexable queries, dedup statistics, lifecycle scans,
and bulk cleanup.

JSONB remains the primary write target (zero risk to existing reads).
The new table is a write-time mirror, populated by a telemetry
`onPersisted` hook.

## Commit chain

- `feat(attachments): add request_attachments relational table (migration 401)`
  - `sql/migrations/startup/401_request_attachments_relational.sql`
  - `sql/migrations/startup/401_request_attachments_relational.down.sql`
  - 12 columns mirroring `attachments.AttachmentMetadata`
  - 4 indexes: request_id, hash (partial), (status, created_at DESC),
    created_at DESC for cleanup
  - CHECK constraint on status enum
- `feat(attachments): add repository layer (InsertOne/InsertBatch/List/Count/Delete)`
  - `domains/attachments/repository.go`
  - pgxpool-based writer; nil-safe no-op when no DB is wired
  - Best-effort contract: errors are returned to callers for logging,
    never propagated to block the primary request_logs INSERT
- `feat(attachments): mirror JSONB into relational table on persist`
  - `internal/attachmentmirror/hook.go`
  - 500ms bounded secondary write
  - Lives in a leaf package to break the telemetry ↔ attachments cycle
- `feat(gateway): wire request_attachments mirror into main`
  - `cmd/gateway/main.go`
  - One-line AddOnRequestLogPersisted registration; gated on dbConn.Enabled

## Verification

- `go build ./...` — PASS
- `go vet ./...` — PASS
- `go test ./domains/attachments/... ./internal/attachmentmirror/...` — PASS
- Existing `go test ./...` runs continue to pass
- Pre-commit hooks (4/4 PASS): go vet, no SET+placeholder, unique NNN,
  has down.sql

## Compatibility

- `request_logs.attachments` JSONB continues to be the primary read path.
- All admin endpoints (`/api/attachments/...`,
  `data_lifecycle_attachments.go`) continue to read JSONB unchanged.
- Migration is `CREATE TABLE IF NOT EXISTS` — safe to run on a hot
  partitioned parent.

## References

- `docs/会话优化v2/05-融合实施方案-附件与模型契约.md` (Phase 2C section updated)
- `docs/changelogs/2026-07-14-multimodal-phase2a.md` (Phase 2A context)