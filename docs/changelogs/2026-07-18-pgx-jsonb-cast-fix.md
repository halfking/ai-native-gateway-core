# 2026-07-18: Fix pgx `[]byte` → `::jsonb` cast crash + settings_kv unique constraint

## Summary

Fixed HTTP 500 on `PUT /api/admin/modules/compression/toggle` and `PUT /api/admin/settings/compression.enabled` caused by pgx v5 sending `[]byte` parameters as `bytea` OID, which PostgreSQL cannot cast to `jsonb`. Also cleaned up duplicate rows and added UNIQUE constraint on `settings_kv.key`.

## Root Cause

`StoreDB.Set()` used `json.Marshal(value)` → `[]byte` → passed directly to `$2::jsonb` parameter. pgx v5 encodes `[]byte` with `bytea` OID (17). PostgreSQL has **no implicit cast** from `bytea` to `jsonb`, causing:
```
ERROR: invalid input syntax for type json (SQLSTATE 22P02)
```

## Fix

- `settings/store_db.go`: convert `[]byte` → `string` before passing to pgx parameters. pgx sends `string` as `text` OID (25), and `text::jsonb` is well-defined in PostgreSQL.
- Applies to both `StoreDB.Set()` and `StoreDB.SetTenant()`.

## Additional

- `settings_kv` table had no `UNIQUE (key)` constraint, resulting in 22 duplicate rows across 8 keys. Cleaned up and added constraint.
- `tenant_settings_kv` already had no duplicates but similarly lacks explicit UNIQUE constraint — `ON CONFLICT (tenant_id, key)` in `SetTenant()` relies on it.

## Files Changed

| File | Change |
|------|--------|
| `settings/store_db.go` | 8 lines: `[]byte` → `string` for `$N::jsonb` params in `Set()` and `SetTenant()` |
| `deploy/sql/objects/tables/settings_kv.sql` | Added `UNIQUE (key)` constraint (executed on production DB) |

## Verification

- L1: `/healthz` 200 + `status:ok` ✅
- L2: JWT auth / DB connectivity OK ✅
- L3: Both endpoints return 200 with correct response bodies ✅
- L4: Repeated toggle false→true works, no new duplicates created ✅

## Deployment Timeline

| Time (CST) | Server | Action | Result |
|------------|--------|--------|--------|
| 2026-07-18 00:18 | 154 | Local build + manual scp | Fix verified (HTTP 200) |
| 2026-07-18 00:50 | 245 | `scripts/deploy-245.sh` (ran at HEAD 88e1b4e0, before fix commit) | 500 still present — fix NOT in binary |
| 2026-07-18 01:10 | 245 | Manual fix-deploy: build at HEAD 113de87dc + atomic symlink swap | Fix verified (HTTP 200) |

## 252 PG Final State

```sql
SELECT key, count(*) FROM settings_kv GROUP BY key HAVING count(*) > 1;
-- 0 rows (no duplicates)
SELECT count(*) FROM settings_kv;       -- 24 rows
SELECT value FROM settings_kv WHERE key = 'compression.enabled';  -- true
\d settings_kv
-- settings_kv_key_unique UNIQUE CONSTRAINT, btree (key) ✅
```
