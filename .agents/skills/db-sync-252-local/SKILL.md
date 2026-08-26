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

# Full sync (schema + data, ~14GB, takes 10-30min)
export PGOPTIONS='-c statement_timeout=0'  # avoid 252's 30s server-side timeout
scripts/pg-table-copy.sh --source configs/env-252.sh --target configs/env-local.sh

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

---

## 5. Verification Checklist

After a sync:

- [ ] `docker ps | grep llm-gateway-pg` shows healthy
- [ ] `docker exec llm-gateway-pg psql -U llm_gateway -c 'SELECT 1'` returns 1
- [ ] 252 vs local `comm -23 /tmp/252.txt /tmp/local.txt` is empty (no tables missing in local)
- [ ] `_` prefixed tables exist locally (if previously identified for cleanup)
- [ ] Container `POSTGRES_PASSWORD` env matches envs SSOT

---

## 6. Related Skills & Docs

- `pg-schema-sync-252` (global skill): schema-only bidirectional sync — use for code-level diffs without data
- `pg-table-copy` (global skill): schema + data copy — this skill extends it with 252-specific know-how
- `docs/06-deployment/02-database/local-pg-sync-from-252.md`: full reference
- `scripts/local-dev/recreate-llm-gateway-pg.sh`: container recreate script
