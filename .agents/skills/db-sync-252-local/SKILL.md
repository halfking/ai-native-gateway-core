---
name: db-sync-252-local
description: Sync PostgreSQL schema + data from 252 (test) to local Docker container (llm-gateway-pg). POLICY: users/passwords are CREATE-ONLY — never auto-modify an existing role or password (no ALTER ROLE/USER PASSWORD, no drop-and-recreate, no env-based reset); password changes are manual + envs SSOT update. Load only COMMON_PG_SUPERUSER_PASS through envs/loader.sh, authenticate SSH through the 252 SSH config/certificate, resolve the Podman PG IP at tunnel invocation, and require both structure and data audits after every sync. Diagnose "no rows" / "table not found" issues that may be caused by hot-table filtering or password drift. Use when the user asks to "sync from 252", "refresh local DB", "recreate llm-gateway-pg", "password doesn't work after container restart", or asks about `_` prefixed tables.
---

# db-sync-252-local

**Triggers**: User asks to sync / refresh local DB from 252, recreate the `llm-gateway-pg` container, mentions "password mismatch" after restart, queries a `_` prefixed table.

**SSOT**: `docs/06-deployment/02-database/local-pg-sync-from-252.md` (always read first if present)

---

## 1. Quick Reference

### Sync 252 → local

```bash
# Use only the loader-provided credential. env-252.sh creates its compatibility
# alias in memory; do not persist a second password or use an SSH-password fallback.
source ~/workspace/ai-native-tools/envs/loader.sh --project llm-gateway-go
source configs/env-252.sh
source scripts/lib/252-db-tunnel.sh

# Resolve pg-252-pg17's runtime CNI address only within the helper. It never
# stores that address, reuses only a healthy existing listener, and never kills
# an unknown process occupying 15432.
trap db252_tunnel_teardown EXIT HUP INT TERM
db252_tunnel_ensure
export PGPASSWORD="$PG_PASS"

# Full sync (schema + data; exact replacement requires explicit --replace-data)
export PGOPTIONS='-c statement_timeout=0'  # avoid 252's 30s server-side timeout
scripts/pg-table-copy.sh --source configs/env-252.sh --target configs/env-local.sh --replace-data

# Safe default: no DROP and append-only data import; use for inspection only.
# --clean-schema is destructive and must be explicitly requested.

# Data-only exact replacement (does not copy hot/partition data)
scripts/pg-table-copy.sh --data-only --replace-data \
  --source configs/env-252.sh --target configs/env-local.sh

# Schema-only
scripts/pg-table-copy.sh --schema-only --source configs/env-252.sh --target configs/env-local.sh

# Completion gate: both audits are mandatory. They must use the same dynamic target
# resolution and owned tunnel lifecycle when they establish their own connection.
bash scripts/local-dev/verify-db-consistency.sh --verify
bash scripts/local-dev/verify-db-data-consistency.sh
# trap db252_tunnel_teardown EXIT closes only a listener created by this shell.
# PG_PSQL_BIN and PG_DUMP_BIN must resolve to native libpq clients, not the
# host psql shim that executes inside llm-gateway-pg.

> **PHASE 8.5 (2026-09-01)** — `pg-table-copy.sh` 自动在末尾调用
> `scripts/local-dev/apply-routing-mv-fixup.sh`（仅当 target 是本地 docker
> 容器，且未传 `--dry-run` / `--data-only` 时）。该 fixup 补齐 252-only 的
> `routing_analytics_7d` / `routing_audit_summary_7d` 物化视图与
> `columnar_insert_only_parents()` 辅助函数；幂等、重复运行无副作用。
> 不再需要手工调用 fixup 脚本；如需手动跑仍可：
> `bash scripts/local-dev/apply-routing-mv-fixup.sh`。
```

### Recreate container

**POLICY (2026-08-31): create-only.** Never auto-modify existing users/passwords —
no `ALTER ROLE/USER ... PASSWORD`, no drop-and-recreate, no resetting an existing
cluster via `POSTGRES_PASSWORD` env. Automation may only CREATE a role when it
does not exist. Password changes are manual (`ALTER ROLE` by hand) + update the
envs SSOT.

```bash
bash scripts/local-dev/recreate-llm-gateway-pg.sh
# Recreates the container preserving the data dir. POSTGRES_PASSWORD only takes
# effect on FIRST-TIME initdb (empty data dir); an initialized cluster's users
# and passwords are never touched by the entrypoint or this script.
# Do NOT use this as a password-reset tool — see pitfall 4.2 for the real
# mechanism behind "password reverted after restart".
```

### Verify consistency (scripts)

`scripts/local-dev/verify-db-consistency.sh` (v1.2) compares SEVEN dimensions — table inventory, column signatures (ALL tables), views (name+md5(definition)), index logical shape, constraints (normalized), sequences, and functions (name + arg-identity + md5(prosrc)) — and classifies every difference as either **expected** (hot/partition tables) or **real drift**. The functions dimension closes the blind spot that the original 6-object audit never compared function DDL, so migrations like 628 (function bodies) were invisible to the audit.

```bash
bash scripts/local-dev/verify-db-consistency.sh --verify   # read-only; sets up + tears down tunnel
bash scripts/local-dev/verify-db-data-consistency.sh       # read-only ordinary-table count + content digest audit
# structure exit 0 = CONSISTENT (only expected hot/partition tables differ)
# data exit 0 = complete ordinary-table set, row counts, and content signatures match
```

Both audits are mandatory after every sync, including schema-only and data-only runs. Completion requires both exit statuses to be zero. The data audit deliberately excludes hot/partition relations; the structure audit must still verify their relation kind and partition/index metadata. Table inventories, sampled counts, and migration-tracking rows are diagnostic only and never replace either audit.

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

### 4.2 "Password reverted after restart" — mechanism corrected (2026-08-31)

**POLICY first**: automation must never modify an existing user or password
(create-only). Password changes are manual `ALTER ROLE` + envs SSOT update.

**Corrected mechanism** (verified against this image's entrypoint): the password
is written **only at first initdb** (empty data dir) via `--pwfile`. On an
already-initialized data dir the entrypoint does NOT touch any role — an
in-cluster `ALTER ROLE ... PASSWORD` persists across restarts. The old claim
"entrypoint re-ALTER USERs from POSTGRES_PASSWORD on every startup" is wrong.

What actually happened historically: the container was recreated against a
**different (or wiped) data directory**, so a different cluster (or a fresh
initdb) answered — e.g. the current container mounts `~/.agents-cache/llm-gateway-pg-data`
while `recreate-llm-gateway-pg.sh` points at `~/data/docker/llm-gateway-pg17/data`.

**Diagnosis** before touching anything:

```bash
docker inspect llm-gateway-pg --format '{{json .Mounts}}'   # which data dir is real?
```

Then fix the password **manually** on the correct cluster if needed.

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
# Run only while the dynamically resolved, control-socket-owned tunnel is still active.
psql -h 127.0.0.1 -p "$TUNNEL_LOCAL_PORT" -U llm_gateway -d llm_gateway -v ON_ERROR_STOP=1 -f dump.fixed.sql
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

### 4.8 Port layout — 5432 = container, 15432 = 252 tunnel (settled 2026-08-31)

Stable layout after the Homebrew PostgreSQL removal:

- `127.0.0.1:5432` → **llm-gateway-pg container** (direct mapping, host port =
  container port). The former occupant (Homebrew postgresql@17, plus an orphaned
  postgresql@15 data dir) was removed 2026-08-31 with full backups in
  `~/backups/` (`homebrew-pg17-5432-final-20260831.sql`,
  `homebrew-pg15-datadir-orphan-20260831.tar.gz`).
- `127.0.0.1:15432` → **252 SSH tunnel only** (`configs/env-252.sh`
  `TUNNEL_LOCAL_PORT`). The container does NOT map 15432 anymore — never map it
  there again: while it did, the tunnel fell back to IPv6 `[::1]:15432` and
  `-h localhost` could silently land on the REMOTE 252 cluster.
- Prefer `-h 127.0.0.1` explicitly. If `FATAL: role "llm_gateway" does not
  exist` ever appears on 5432 again, something re-installed a local PG — check
  `lsof -nP -iTCP:5432 -sTCP:LISTEN` and which cluster answers.
- Note: uninstalling Homebrew postgresql also removed the host `psql` client;
  use `docker exec llm-gateway-pg psql ...` or `brew install libpq` if a host
  CLI is needed.

---

## 5. Verification Checklist

After a sync:

- [ ] `docker ps | grep llm-gateway-pg` shows healthy
- [ ] `docker exec llm-gateway-pg psql -U llm_gateway -c 'SELECT 1'` returns 1
- [ ] `verify-db-consistency.sh --verify` exits 0
- [ ] `verify-db-data-consistency.sh` exits 0
- [ ] `_` prefixed tables exist locally (if previously identified for cleanup)
- [ ] Connection credentials came only from loader-provided `COMMON_PG_SUPERUSER_PASS`

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

# Manual equivalent (if you need to inspect the DDL first):
# Use the same shared helper; never hand-resolve/store a Podman CNI address.
source ~/workspace/ai-native-tools/envs/loader.sh --project llm-gateway-go
source configs/env-252.sh
source scripts/lib/252-db-tunnel.sh
trap db252_tunnel_teardown EXIT HUP INT TERM
db252_tunnel_ensure

# dump per table (loop — see pitfall 4.5), order FK parents before children
> /tmp/feature.sql
for t in proxy_subscriptions proxy_nodes provider_domains ...; do
  docker exec -e PGPASSWORD="$PG_PASS" llm-gateway-pg \
    pg_dump -U llm_gateway -d llm_gateway --schema-only --clean --if-exists --no-owner --no-privileges -t "$t" \
    >> /tmp/feature.sql
done
grep -vE "set_config\('search_path', '', false\)" /tmp/feature.sql > /tmp/feature.fixed.sql   # pitfall 4.4
PGPASSWORD="$PG_PASS" "$PG_PSQL_BIN" -h 127.0.0.1 -p "$TUNNEL_LOCAL_PORT" -U llm_gateway -d llm_gateway \
  -v ON_ERROR_STOP=1 -f /tmp/feature.fixed.sql
# trap db252_tunnel_teardown EXIT closes only a listener created by this shell.
# PG_PSQL_BIN must resolve to a native libpq client, not the host psql shim.
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
- `scripts/sync-from-252.sh` and `scripts/sync-schema-to-252.sh`: **retired**; do not invoke or reference them for new work. Use `scripts/pg-table-copy.sh` instead.
