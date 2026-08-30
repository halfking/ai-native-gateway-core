---
name: db-sync-252-local
description: Sync PostgreSQL schema + data from 252 (test) to local Docker container (llm-gateway-pg). Recreate container with 252-aligned credentials. Diagnose "no rows" / "table not found" issues that may be caused by hot-table filtering or password drift. Use when the user asks to "sync from 252", "refresh local DB", "recreate llm-gateway-pg", "password doesn't work after container restart", or asks about `_` prefixed tables.
---

# db-sync-252-local

**Triggers**: User asks to sync / refresh local DB from 252, recreate the `llm-gateway-pg` container, mentions "password mismatch" after restart, queries a `_` prefixed table.

**SSOT**: `docs/06-deployment/02-database/local-pg-sync-from-252.md` (always read first if present)

---

## 1. Quick Reference

### Sync 252 → local

```bash
# Tunnel target is defined by configs/env-252.sh; do not hardcode it in docs.
source ~/workspace/ai-native-tools/envs/loader.sh --project llm-gateway-go
export PG_PASS_252="$COMMON_PG_SUPERUSER_PASS"
export SSH_PASS_252="${SSH_PASS_252:-ssh-config-auth}"
source configs/env-252.sh
ssh -f -N -L "$TUNNEL_LOCAL_PORT:$TUNNEL_REMOTE_TARGET" 252 && sleep 2
export PGPASSWORD="$COMMON_PG_SUPERUSER_PASS"

# Full sync (schema + data; exact replacement requires explicit --replace-data)
export PGOPTIONS='-c statement_timeout=0'  # avoid 252's 30s server-side timeout
export PG_PASS_LOCAL="$COMMON_PG_SUPERUSER_PASS"
scripts/pg-table-copy.sh --source configs/env-252.sh --target configs/env-local.sh --replace-data

# Safe default: no DROP and append-only data import; use for inspection only.
# --clean-schema is destructive and must be explicitly requested.

# Data-only exact replacement (does not copy hot/partition data)
scripts/pg-table-copy.sh --data-only --replace-data \
  --source configs/env-252.sh --target configs/env-local.sh

# Schema-only (fast, ~2min)
scripts/pg-table-copy.sh --schema-only --source configs/env-252.sh --target configs/env-local.sh

kill $(lsof -tiTCP:15432 -sTCP:LISTEN)  # cleanup tunnel
```

### Recreate container (password persistence)

```bash
bash scripts/local-dev/recreate-llm-gateway-pg.sh
# Loads COMMON_PG_SUPERUSER_PASS from envs, recreates container with same data dir.
# Use when:
#   - Password mismatch after container restart
#   - POSTGRES_PASSWORD env still has old value
#   - ALTER USER doesn't persist across restarts
```

### Verify sync result

```bash
source ~/workspace/ai-native-tools/envs/loader.sh --project llm-gateway-go
export PG_PASS_252="$COMMON_PG_SUPERUSER_PASS"
export SSH_PASS_252="${SSH_PASS_252:-ssh-config-auth}"
source configs/env-252.sh
ssh -f -N -L "$TUNNEL_LOCAL_PORT:$TUNNEL_REMOTE_TARGET" 252 && sleep 2
export PGPASSWORD="$COMMON_PG_SUPERUSER_PASS"

# 252 vs local table inventory
psql -h localhost -p 15432 -U llm_gateway -d llm_gateway -tAc \
  "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename" \
  > /tmp/252.txt
docker exec -e PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" llm-gateway-pg \
  psql -U llm_gateway -d llm_gateway -tAc \
  "SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename NOT LIKE '\_%' ESCAPE '\\' ORDER BY tablename" \
  > /tmp/local.txt
comm -23 /tmp/252.txt /tmp/local.txt   # tables in 252 but missing in local (should be empty)
comm -13 /tmp/252.txt /tmp/local.txt   # local extras (should be empty after rename)

kill $(lsof -tiTCP:15432 -sTCP:LISTEN)
```

### Verify consistency (scripts)

`scripts/local-dev/verify-db-consistency.sh` (v1.1) compares SIX dimensions — table inventory, column signatures (ALL tables), views (name+md5(definition)), index logical shape, constraints (normalized), sequences — and classifies every difference as either **expected** (hot/partition tables) or **real drift**.

```bash
bash scripts/local-dev/verify-db-consistency.sh --verify   # read-only; sets up + tears down tunnel
bash scripts/local-dev/verify-db-data-consistency.sh       # read-only ordinary-table count + content digest audit
# structure exit 0 = CONSISTENT (only expected hot/partition tables differ)
# data exit 0 = complete ordinary-table set, row counts, and content signatures match
```

Both audits are required after every sync. The data audit deliberately excludes hot/partition relations; the structure audit must still verify their relation kind and partition/index metadata.

---

## 2. Hot Table Filter (default behavior)

`pg-table-copy.sh` **excludes** these from data export (schema only):

| Pattern | Example | Reason |
|---------|---------|--------|
| `*_hot` | `request_logs_hot` | recent/hot window, regenerated locally |
| `*_YYYY_MM` | `request_wal_2026_07` | monthly partition |
| `*_archive*` | `usage_ledger_2026_07_archived` | archived partition |
| parent partitions | `request_logs` (parent) | Citus auto-created subtable |

If a user reports "table is empty in local but has data in 252":
- Check if the table matches the hot patterns above → **expected empty locally**
- Explain that schema-only mode cannot add data. For a specific hot table, prepare a separate approved table-level export/import procedure.

---

## 3. `_` Prefix Table Convention

Local-only tables (sync leftovers, not in 252) are renamed with `_` prefix:

```
_identity_migration_ownership
_shadow_users
_shadow_user_providers   # FK to _shadow_users (preserved by rename)
_archived_or_test_tables
```

**Contract**: `_` prefix = pending-deletion. Business code / views / tests should NOT reference.

If user queries a `_` table:
- Confirm intent (data recovery vs legitimate need)
- If they want to permanently delete → walk them through rule 19 §11 (3-stage: impact analysis → 2nd audit → human confirm)
- If they want to keep → rename back to original

---

## 4. Common Pitfalls

### 4.1 Statement timeout on 252

`statement_timeout = 30s` is set server-side on 252. Large table `count(*)` or full scans will fail.

**Fix**: `export PGOPTIONS='-c statement_timeout=0'` before running `pg-table-copy.sh`. The script supports `${PGOPTIONS:-}` passthrough.

### 4.2 Password drift after restart

PG `docker-entrypoint.sh` reads `POSTGRES_PASSWORD` env on every startup and re-`ALTER USER` if mismatch. In-container `ALTER USER` doesn't persist.

**Fix**: `recreate-llm-gateway-pg.sh` rebuilds the container with `POSTGRES_PASSWORD` env pre-loaded from envs. Data dir is preserved.

### 4.3 zsh multi-line SQL bug

```bash
# ❌ BAD — zsh treats unquoted multi-line variables as one big string with newlines
LOCAL_TABLES="
table_a
table_b
"
for t in $LOCAL_TABLES; do ... done  # expands as ONE quoted string
```

**Fix**: use `while IFS= read -r` + printf for SQL generation:

```bash
> /tmp/rename.sql
while IFS= read -r tbl; do
  printf 'ALTER TABLE IF EXISTS public."%s" RENAME TO "_%s";\n' "$tbl" "$tbl" >> /tmp/rename.sql
done < /tmp/local-only-tables.txt

docker exec -i llm-gateway-pg psql -U llm_gateway -d llm_gateway < /tmp/rename.sql
```

### 4.4 `pg_dump` emits `SET search_path=''` — breaks 252's columnar event trigger

When you `pg_dump --schema-only` and pipe the SQL into 252, the dump sets
`SELECT pg_catalog.set_config('search_path', '', false)` for security. 252 has an
event trigger `enforce_columnar_trigger` that fires on every table DDL and calls the
unqualified function `columnar_insert_only_parents()`. With `search_path=''` the trigger
cannot resolve that function and **every CREATE/DROP TABLE fails**:

```
ERROR:  function columnar_insert_only_parents() does not exist
CONTEXT:  PL/pgSQL function public.fn_enforce_columnar_event_trigger() line 12
```

**Fix**: strip the guard line before applying (everything in the dump is already
schema-qualified as `public.*`, so dropping it is safe):

```bash
grep -vE "set_config\('search_path', '', false\)" dump.sql > dump.fixed.sql
psql -h localhost -p 15432 -U llm_gateway -d llm_gateway -v ON_ERROR_STOP=1 -f dump.fixed.sql
```

### 4.5 `pg_dump -t` needs a repeated `-t` per table (not a space-separated list)

`pg_dump -t "a b c"` treats the whole string as ONE table name → `too many command-line
arguments`. Build a repeated `-t` (or loop per table). Note: passing a bash array
`"${args[@]}"` into `docker exec ... pg_dump ...` also collapses incorrectly under zsh —
loop and append to a file instead:

```bash
> /tmp/feature.sql
for t in agent_discovery proxy_nodes proxy_subscriptions; do
  docker exec -e PGPASSWORD="$PASS" llm-gateway-pg \
    pg_dump -U llm_gateway -d llm_gateway --schema-only --clean --if-exists --no-owner --no-privileges -t "$t" \
    >> /tmp/feature.sql 2>/tmp/dump.err
done
```

### 4.6 A 252-only monthly partition is EXPECTED, not drift

252 creates runtime monthly partitions (e.g. `request_logs_bodies_2026_10`) that match the
hot-table filter `*_2026_*` and are excluded from the sync. They will show up in
`comm -23 /tmp/252.txt /tmp/local.txt` and that is **correct** — do not try to copy them
to local (local regenerates its own partitions). The verify script classifies these as
"EXPECTED hot/partition" automatically.

### 4.7 Migration-tracking tables can LIE after a sync — verify structure, not table counts

**Symptom (2026-08-31 audit)**: `schema_migrations` identical on both sides (573/610
recorded as applied), yet local's `request_logs` still had the body columns 573 was
supposed to drop and lacked 610's CHECK constraint.

**Root cause**: `pg-table-copy.sh` used to import schema with `-v ON_ERROR_STOP=off`
and only inspected the log for ERROR lines when psql exited non-zero — which it never
does with that setting. Combined with pitfall 4.4 (search_path='' breaking the columnar
event trigger on EVERY table DDL), the 2026-08-26 schema import failed wholesale while
reporting "Schema imported". Local kept its pre-sync schema; the tracking tables were
copied as DATA and still claimed 573/610 were applied. Table-name-level comparison
passed because the stale tables had the same names.

**Fixed** (both in `pg-table-copy.sh`): the search_path guard is now stripped before
import, and the import log is ALWAYS scanned for ERROR/FATAL lines regardless of exit
code; the run exits 1 on schema-import errors.

**Rule**: after any sync, run `scripts/local-dev/verify-db-consistency.sh` — column /
view / constraint fingerprints, never table counts or tracking tables alone. Known
benign variances the script already normalizes: physical column order (PG cannot
reorder in place) and the two `ANY(ARRAY[...])` text renderings in CHECK constraints
and partial-index defs.

---

## 5. Verification Checklist

After a sync:

- [ ] `docker ps | grep llm-gateway-pg` shows healthy
- [ ] `docker exec llm-gateway-pg psql -U llm_gateway -c 'SELECT 1'` returns 1
- [ ] 252 vs local `comm -23 /tmp/252.txt /tmp/local.txt` is empty (no tables missing in local)
- [ ] `_` prefixed tables exist locally (if previously identified for cleanup)
- [ ] Container `POSTGRES_PASSWORD` env matches envs SSOT

---

## 7. Reconcile 252 ← local (feature tables created by the app)

**Symptom**: `comm -13 /tmp/252.txt /tmp/local.txt` lists tables that exist locally but
not on 252 (e.g. `agent_discovery`, `agent_gateways`, `orchestration_sessions`,
`provider_domains`, `proxy_nodes`, `proxy_subscriptions`). These are usually created by
the app's runtime AutoMigrate / newer code, so they are **not** in the tracked
`schema_migrations` baseline on either side. Local dev is simply ahead of 252's deployed
app version.

**Goal**: bring 252's schema up to match local so both sides are consistent, **without
dropping anything** (the local app needs those tables).

**Procedure** (gated — modifies 252; confirm before running):

```bash
# 1. The verify script's --reconcile mode does this safely (sets up tunnel,
#    dumps local DDL with the search_path fix, applies to 252, re-verifies):
bash scripts/local-dev/verify-db-consistency.sh --reconcile \
  agent_discovery agent_gateways agent_migration_log gateway_run_bindings \
  industry_registry orchestration_sessions provider_domains proxy_nodes proxy_subscriptions --yes

# Manual equivalent (if you need to tweak the DDL first):
source ~/workspace/ai-native-tools/envs/loader.sh --project llm-gateway-go
export PG_PASS_252="$COMMON_PG_SUPERUSER_PASS"
export SSH_PASS_252="${SSH_PASS_252:-ssh-config-auth}"
source configs/env-252.sh
ssh -f -N -L "$TUNNEL_LOCAL_PORT:$TUNNEL_REMOTE_TARGET" 252 && sleep 3

# dump per table (loop — see pitfall 4.5), order FK parents before children
> /tmp/feature.sql
for t in proxy_subscriptions proxy_nodes provider_domains ...; do
  docker exec -e PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" llm-gateway-pg \
    pg_dump -U llm_gateway -d llm_gateway --schema-only --clean --if-exists --no-owner --no-privileges -t "$t" \
    >> /tmp/feature.sql
done
grep -vE "set_config\('search_path', '', false\)" /tmp/feature.sql > /tmp/feature.fixed.sql   # pitfall 4.4
PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" psql -h localhost -p 15432 -U llm_gateway -d llm_gateway \
  -v ON_ERROR_STOP=1 -f /tmp/feature.fixed.sql
kill $(lsof -tiTCP:15432 -sTCP:LISTEN)
```

**Notes**:
- Order tables so FK parents precede children (`proxy_subscriptions` before `proxy_nodes`).
- The dump uses `--clean --if-exists` → re-running is idempotent (DROP IF EXISTS + CREATE).
- Data is NOT copied (these are empty feature tables); only structure. Copy rows separately
  only if both sides need identical seed data.

---

## 6. Related Skills & Docs

- `pg-schema-sync-252` (global skill): schema-only bidirectional sync — use for code-level diffs without data
- `pg-table-copy` (global skill): schema + data copy — this skill extends it with 252-specific know-how
- `docs/06-deployment/02-database/local-pg-sync-from-252.md`: full reference
- `scripts/local-dev/recreate-llm-gateway-pg.sh`: container recreate script
