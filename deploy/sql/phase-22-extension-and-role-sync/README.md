# Phase 22 — Extension, Role & Columnar Sync

> **Generated**: 2026-06-26
> **Purpose**: Sync local dev DB extensions, roles, and columnar tables to match 184 production
> **Phase**: 22 (Complete environment alignment)

## Summary

After Phase 21 table sync, this phase ensures local has:
1. Same **PostgreSQL extensions** as 184 (per-database)
2. Same **roles / users** as 184
3. Same **columnar table storage** as 184

## Files

```
phase-22-extension-and-role-sync/
├── 00-extensions.sql     — Per-DB extension installation
├── 01-roles.sql          — Roles + DB owners (matches 184)
├── 02-columnar-tables.sql — Convert append-only tables to columnar
└── README.md              — This file
```

## 1. Extensions (00-extensions.sql)

184 has these extensions per database:

| Database | Extensions |
|----------|-----------|
| postgres (cluster) | pgcrypto, pg_trgm, btree_gist, uuid-ossp |
| llm_gateway | + citus_columnar |
| kaixuan | pgcrypto |
| trendaradar | pg_trgm |
| crm | pgcrypto, uuid-ossp |
| brandmind | pgcrypto, uuid-ossp |
| brandmind_test | pgcrypto, uuid-ossp |
| smart_bidding | pgcrypto |
| (others) | plpgsql default only |

**Note**: `citus` extension is loaded via `shared_preload_libraries` from the citusdata/citus image, not via CREATE EXTENSION.

## 2. Roles (01-roles.sql)

184 has these roles:

| Role | Attributes |
|------|-----------|
| `kxuser` | Superuser, Member of llm_gateway |
| `llm_gateway` | SUPERUSER, CREATEROLE, CREATEDB, REPLICATION, BYPASSRLS |
| `casdoor_user` | NOLOGIN |
| `crm_user` | NOLOGIN |
| `doc_tools_user` | NOLOGIN |
| `kaixuan_user` | NOLOGIN |

The script:
1. Creates missing roles (idempotent via DO block)
2. Grants `llm_gateway` to `kxuser`
3. Sets DB owners to `llm_gateway` (matches 184 pattern)

## 3. Columnar Tables (02-columnar-tables.sql)

184 has **9 columnar tables** in `llm_gateway`:

```
candidate_failure_logs
credential_probe_model_log
model_offer_events
model_probe_runs
price_change_events
provider_events
test_columnar_new
tool_call_events
usage_ledger
```

The script converts these from heap → columnar using `ALTER TABLE ... SET ACCESS METHOD columnar`.

**Caveats**:
- Columnar conversion is **online** (no downtime) but may lock briefly
- Empty tables: instant
- Tables with data: rewrite (can be slow on large tables)
- 184 has these as columnar because they're append-only time-series with high compression (15-40x)

## Usage

### Apply extensions first
```bash
PGPASSWORD='CGpGfdG9De502/bdQYXD0Cr4akCVXaJ3' psql -U kxuser -d postgres \
  -h localhost -p 5434 \
  -f 00-extensions.sql
```

### Apply roles
```bash
PGPASSWORD='CGpGfdG9De502/bdQYXD0Cr4akCVXaJ3' psql -U kxuser -d postgres \
  -h localhost -p 5434 \
  -f 01-roles.sql
```

### Apply columnar conversion
```bash
PGPASSWORD='CGpGfdG9De502/bdQYXD0Cr4akCVXaJ3' psql -U kxuser -d llm_gateway \
  -h localhost -p 5434 \
  -f 02-columnar-tables.sql
```

## Verification

```sql
-- Check extensions
SELECT extname, extversion FROM pg_extension ORDER BY extname;

-- Check roles
\du

-- Check columnar tables
SELECT c.relname,
       CASE WHEN c.relam = (SELECT oid FROM pg_am WHERE amname='columnar')
            THEN 'columnar' ELSE 'heap' END AS storage
FROM pg_class c
JOIN pg_namespace n ON c.relnamespace = n.oid
WHERE n.nspname = 'public'
  AND c.relam = (SELECT oid FROM pg_am WHERE amname='columnar')
ORDER BY c.relname;
```

## Phase 21 + Phase 22 Combined Result

After applying both phases, local matches 184 production on:
- ✅ 14/14 databases (table count match)
- ✅ All required extensions (per DB)
- ✅ All roles (kxuser, llm_gateway, _user NOLOGIN roles)
- ✅ All columnar tables (9 in llm_gateway)

**Index count diff**: Local has more indexes than 184 in some DBs (because Phase 1 added extra indexes that 184 has since dropped). This is acceptable — extra indexes don't harm functionality.

## Source

Generated from 184 reference schema on 2026-06-26.

To regenerate after major schema changes on 184:
1. Run: `pg_dump -U llm_gateway -d llm_gateway --schema-only > /tmp/184-ref.sql` (and other DBs)
2. Re-run diff tool
3. Update Phase 21 / Phase 22 sync files
4. Test on local before committing