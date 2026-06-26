# Phase 21 — Schema Reconciliation

> **Generated**: 2026-06-26
> **Purpose**: Sync local dev DB schema to match 184 production (via 71 replica)
> **Phase**: 21 (Schema alignment audit)

## Summary

Compared schemas across three environments (184 production, 71 replica, local dev).
Found **212 tables** and **414 indexes** missing in local.

| DB | Tables Missing | Indexes Missing |
|----|---------------|-----------------|
| llm_gateway | 12 | 62 |
| kaixuan | 4 | 0 |
| trendaradar | 43 | 89 |
| crm | 24 | 47 |
| brandmind | 32 | 91 |
| brandmind_test | 28 | 91 |
| geo_flow | 48 | 0 |
| smart_bidding | 8 | 12 |
| port_email | 7 | 11 |
| memos | 1 | 5 |
| aicms_db | 5 | 6 |
| casdoor | 0 | 0 |
| doc_tools | 0 | 0 |
| stock_trading | 0 | 0 |
| **Total** | **212** | **414** |

## What Was Compared

Three environments with different roles:

| Environment | Role | Why Used |
|-------------|------|----------|
| **184 (k3s)** | Production master | Authoritative source |
| **71 (docker)** | Stream replica of 184 | Used as backup reference (184 had PG hang incident) |
| **Local (docker)** | Developer workstation | Target for sync |

## Methodology

1. `pg_dump --schema-only` from each env, per database (14 DBs)
2. Extract structural statements (CREATE TABLE, CREATE INDEX, ALTER TABLE)
3. Sort + diff using `comm` to find missing in local
4. Generate per-DB sync SQL by extracting CREATE blocks from 184 dump

## Files Generated

```
phase-21-schema-sync/
├── 00-RUN-ALL.sql                       — Orchestrator with documentation
├── llm_gateway-sync-from-184.sql         — 12 missing tables + indexes/policies
├── llm_gateway-indexes-sync-from-184.sql — 62 missing indexes only
├── kaixuan-sync-from-184.sql             — 4 missing tables
├── trendaradar-sync-from-184.sql         — 43 missing tables
├── trendaradar-indexes-sync-from-184.sql — 89 missing indexes
├── ... (per-DB files)
└── README.md                              — This file
```

## How to Apply

### Option 1: Master orchestrator (recommended)

```bash
cd services/llm-gateway-go/deploy/sql/phase-21-schema-sync/
PGPASSWORD='CGpGfdG9De502/bdQYXD0Cr4akCVXaJ3' psql -h localhost -p 5434 -U kxuser -d postgres -f 00-RUN-ALL.sql
```

But 00-RUN-ALL.sql is documentation only — the actual work is in the per-DB files.

### Option 2: Per-DB (most control)

```bash
cd services/llm-gateway-go/deploy/sql/phase-21-schema-sync/
PG='PGPASSWORD=CGpGfdG9De502/bdQYXD0Cr4akCVXaJ3 psql -h localhost -p 5434 -U kxuser'

# Tables first (in dependency order: leaf DBs first)
eval $PG -d llm_gateway -f llm_gateway-sync-from-184.sql
eval $PG -d kaixuan -f kaixuan-sync-from-184.sql
eval $PG -d crm -f crm-sync-from-184.sql
eval $PG -d brandmind -f brandmind-sync-from-184.sql
eval $PG -d brandmind_test -f brandmind-test-sync-from-184.sql
eval $PG -d trendaradar -f trendaradar-sync-from-184.sql
eval $PG -d geo_flow -f geo_flow-sync-from-184.sql
eval $PG -d smart_bidding -f smart_bidding-sync-from-184.sql
eval $PG -d port_email -f port_email-sync-from-184.sql
eval $PG -d memos -f memos-sync-from-184.sql
eval $PG -d aicms_db -f aicms_db-sync-from-184.sql

# Indexes second
for f in *-indexes-sync-from-184.sql; do
    db=$(echo "$f" | sed -E 's|-indexes-sync-from-184.sql||')
    echo "Applying indexes for $db"
    eval $PG -d "$db" -f "$f"
done
```

## Idempotency Notes

- `CREATE TABLE` is **NOT** idempotent. Scripts will fail if tables already exist.
- `CREATE INDEX` is **NOT** idempotent (PostgreSQL doesn't have CREATE INDEX IF NOT EXISTS in all versions, but PG15+ does).
- All scripts include `\connect <db>` at the top so they can be run from `psql -d postgres`.

## Verification

After applying, verify with:

```bash
PG='PGPASSWORD=CGpGfdG9De502/bdQYXD0Cr4akCVXaJ3 psql -h localhost -p 5434 -U kxuser'

for db in llm_gateway casdoor kaixuan trendaradar crm brandmind brandmind_test doc_tools geo_flow smart_bidding stock_trading port_email memos aicms_db; do
    count=$(eval $PG -d "$db" -t -c 'SELECT COUNT(*) FROM pg_tables WHERE schemaname='\''public'\'';')
    echo "$db: $count tables"
done
```

Expected: 184 count per DB (matching 184).

## Rollback

If something goes wrong, drop only the tables/indexes added by these scripts:

```sql
-- Per-DB rollback (example for llm_gateway)
DROP TABLE IF EXISTS public.credit_ledger_old CASCADE;
DROP TABLE IF EXISTS public.tool_usage_stats_old CASCADE;
-- ... (or use the missing table list per DB)
```

## Notes

- These scripts were generated mechanically from `pg_dump` output. Review before applying.
- Foreign keys, constraints, RLS policies are included where present in 184 dump.
- Triggers are NOT extracted (need manual review if any).
- **Some scripts may reference functions from other DBs** (cross-DB dependencies). If CREATE fails due to missing function, create the dependent DB first.

## Known Limitations

1. **Schema differences only** — data is NOT synced (use `pg_dump --data-only` for that)
2. **Generated from 184 state on 2026-06-26** — re-run after major schema changes
3. **DDL extraction may miss** complex DDL like `ALTER FUNCTION`, `ALTER TYPE`
4. **Cross-DB dependencies** may require manual ordering