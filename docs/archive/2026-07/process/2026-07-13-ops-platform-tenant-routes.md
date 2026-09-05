---
archived_from: docs/2026-07-13-ops-platform-tenant-routes.md
archived_at: 2026-08-17
archived_by: docs-archive v1.0
backup_ts: 20260817-190606
status: archived
---

> 本文档已归档，原文保持不变。

# Operations Platform Tenant Routes

## Changes

- Loaded the five `/ops/*` views statically to avoid the shared `ops` chunk's circular dynamic-import failure that left `RouterView` as `<!---->`.
- Added read-only `/tenant/license` and `/tenant/autoupdate` views.
- Added `/api/tenant/license/status` and `/api/tenant/autoupdate/check` with authenticated tenant scoping.
- Added tenant ownership columns for licenses and releases, defaulting existing records to `default`.

## Verification

- `npm run build`
- `go test ./...`
- `git diff --check`
- `vue-tsc --noEmit` remains blocked by pre-existing chart/i18n/model type errors; the ops-specific `CodeIssue` error was fixed.
- Browser-use reproduced the production blank state before the fix. New-code browser validation requires deploying the built SPA and backend; this worktree's preview had no authenticated session.

## Risk

Existing license/release creation paths default new records to `default`; tenant-specific provisioning must set `tenant_id` through a future platform provisioning workflow before tenant views can show non-default records.
