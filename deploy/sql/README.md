# deploy/sql/ — Database Bootstrap

> Reverse-engineered initialization SQL + seed data for the `<DB_NAME>` PostgreSQL
> database that backs this service.

## Quick start

### Bootstrap a fresh database (production path)

```bash
# On the target server, with admin DB credentials:
DATABASE_URL='postgresql://kxuser:***@14.103.112.184:5432/<DB_NAME>?ssl=false' \
  ./init.sh
```

This will:
1. Verify connectivity
2. Apply `00-prereqs.sql` (extensions, auto-detected)
3. Apply `01-schema.sql` (full public schema)
4. Apply `02-seed.sql` (system-level config tables)
5. Print row counts and policy counts

Add `--reset` to DROP the public schema first (DESTRUCTIVE — wipes the DB):

```bash
DATABASE_URL='postgresql://...' ./init.sh --reset
```

### Dry-run verification (no TimescaleDB required)

For CI / local dev where TimescaleDB is not installed:

```bash
./verify.sh --no-timescale        # spins up a temp DB and asserts row counts
./verify.sh --no-timescale --keep # keep the test DB for manual inspection
```

The expected row counts are auto-derived from the current production DB.
To override, edit `verify.sh`'s `EXPECTED_OVERRIDE` array.

### Regenerate from current production DB

After schema changes are deployed to production, refresh the SQL files:

```bash
DATABASE_URL='postgresql://kxuser:***@14.103.112.184:5432/<DB_NAME>?ssl=false' \
  ./dump-schema.sh
DATABASE_URL='postgresql://kxuser:***@14.103.112.184:5432/<DB_NAME>?ssl=false' \
  ./dump-seed.sh
```

Both scripts share logic with the SSOT library:
`scripts/_lib/db-init-lib.sh` — **edit there**, not here.

## What's in this directory

| File | Source | What it contains |
|------|--------|------------------|
| `00-prereqs.sql` | `db_init::dump_prereqs` | `CREATE EXTENSION` for the extensions installed in prod (auto-detected) |
| `01-schema.sql` | `db_init::dump_schema` | Full public schema: tables, views, indexes, RLS, triggers, functions |
| `02-seed.sql` | `db_init::dump_seed` | System-level config / lookup tables (auto-selected by heuristic) |
| `init.sh` | thin wrapper | Apply prereqs → schema → seed in order |
| `verify.sh` | thin wrapper | Local dry-run with auto-derived row-count assertions |
| `dump-schema.sh` | thin wrapper | Re-dump prereqs + schema from current prod DB |
| `dump-seed.sh` | thin wrapper | Re-dump seed from current prod DB |

## Seed table selection

`db_init::select_seed_tables` (in the lib) picks tables to seed based on:

1. **Always-include** (white-list): application/role/permission/maas_settings/
   provider_*/model_*/work_type_*/tool_*/settings_kv etc.
2. **Always-exclude** (black-list): user/tenant/api_key/credential/audit/
   event/log/transaction/wallet/ledger/... — never seeded.
3. **Heuristic**: name matches `*config*|*settings*|*catalog*|*family*|*type*`
   AND row count ≤ 200.

The selector also detects single-column vs composite PKs and uses
`ON CONFLICT (pk) DO NOTHING` for single-col or `ON CONFLICT DO NOTHING`
(composite) — both safe to re-run.

## What is deliberately excluded

| Table kind | Why excluded |
|------------|--------------|
| users / tenants | populated by OIDC / onboarding flow |
| api_keys / credentials | issued on demand; contains ciphertext |
| request_logs / request_wal | runtime data |
| *_audit / *_event | generated at runtime |
| *_history / *_index | derived from operational data |
| Hypertable time-series | runtime, not config |

If you need to recover business data, do so via the application's
onboarding flow, NOT by re-seeding this file.

## Post-processing: function reordering

`pg_dump --schema-only` orders by `pg_class.oid` (creation order), not by
dependency. `db_init::dump_schema` does two re-orderings to make the dump
work on a fresh DB:

1. Functions like `recent_success_rate` (LANGUAGE sql, refs tables directly)
   are moved to a `DEFERRED FUNCTIONS` block.
2. The block is placed BEFORE the first `CREATE TRIGGER` statement, so
   trigger functions exist before their triggers reference them.

## Production DB reference (snapshot <DATE>)

- **Host**: `14.103.112.184:5432` (single citus cluster, all 14 DBs)
- **Database**: `<DB_NAME>`
- **User**: `kxuser` (formerly `stockuser` / `postgres` — merged 2026-06-24)
- **PG version**: PostgreSQL 15.2 (Ubuntu 15.2-1.pgdg22.04+1)
- **k8s service**: `llm-gateway-pg-svc.pms-test.svc.cluster.local:5432`
