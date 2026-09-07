# Experience: local Docker ↔ 252 PostgreSQL instance sync

Date: 2026-09-08

## What this is for

Align `llm-gateway-pg` with 252 `pg-252-pg17` without copying `llm_gateway`
data, without silent updates/deletes, and without treating live inventory as
the SQL SSOT.

## Hard rules that survived the run

1. Plan first, then wait for explicit confirmation. Inventory and impact are
   read-only; writers need `--yes`, a matching manifest hash, and freshness.
2. `llm_gateway` is schema-only forever. Full bootstrap and data merge are
   blocked in both directions.
3. Data merge is insert-only: `INSERT ... ON CONFLICT DO NOTHING`. Conflicting
   primary keys keep 252. No UPDATE/DELETE/TRUNCATE.
4. Additive schema never applies `CONFLICT_LOCAL_WINS`. Type changes such as
   `risk_level` varchar vs integer stay gated.
5. Owner/GRANT noise is comparison-only under `semantic_only`.
6. Rollback creates a **new** database from a checksummed dump. It never
   overwrites an existing database.

## Reusable pipeline

```bash
bash scripts/pg-instance-sync.sh inventory --output-dir "$OUT"
bash scripts/pg-instance-sync.sh plan \
  --local-inventory "$OUT/local-inventory.tsv" \
  --remote-inventory "$OUT/remote-inventory.tsv" \
  --policy configs/pg-sync-policy.conf --manifest "$OUT/manifest.tsv"
HASH=$(awk '/^manifest_hash:/{print $2}' <<<"$(
  bash scripts/pg-instance-sync.sh plan \
    --local-inventory "$OUT/local-inventory.tsv" \
    --remote-inventory "$OUT/remote-inventory.tsv" \
    --policy configs/pg-sync-policy.conf --manifest "$OUT/manifest.tsv")")
bash scripts/pg-instance-impact.sh --manifest "$OUT/manifest.tsv" \
  --policy configs/pg-sync-policy.conf --output-dir "$OUT/impact"
# review impact, then:
bash scripts/pg-instance-sync.sh backup --yes --freshness-check \
  --manifest "$OUT/manifest.tsv" --manifest-hash "$HASH" \
  --policy configs/pg-sync-policy.conf --output-dir "$OUT/backup"
bash scripts/pg-instance-bootstrap.sh --yes --freshness-check \
  --manifest "$OUT/manifest.tsv" --manifest-hash "$HASH" \
  --policy configs/pg-sync-policy.conf \
  --backup-index "$OUT/backup/backup-index.tsv" --output-dir "$OUT/bootstrap"
bash scripts/pg-instance-sync.sh apply-schema --yes --freshness-check \
  --manifest "$OUT/manifest.tsv" --manifest-hash "$HASH" \
  --policy configs/pg-sync-policy.conf \
  --impact-matrix "$OUT/impact/impact-matrix.tsv" --work-dir "$OUT/schema"
bash scripts/pg-instance-sync.sh apply-data --yes --freshness-check \
  --manifest "$OUT/manifest.tsv" --manifest-hash "$HASH" \
  --policy configs/pg-sync-policy.conf \
  --impact-matrix "$OUT/impact/impact-matrix.tsv" --work-dir "$OUT/data"
bash scripts/pg-instance-sync.sh verify \
  --manifest "$OUT/manifest.tsv" --policy configs/pg-sync-policy.conf \
  --impact-matrix "$OUT/impact/impact-matrix.tsv" --output-dir "$OUT/verify"
```

Rollback a single dump into a new name:

```bash
bash scripts/pg-instance-sync.sh restore --yes --freshness-check \
  --manifest "$OUT/manifest.tsv" --manifest-hash "$HASH" \
  --policy configs/pg-sync-policy.conf \
  --backup-index "$OUT/backup/backup-index.tsv" \
  --source-side remote --source-database acc_db \
  --target-side remote --target-database acc_db_rollback
```

## Traps found in this task

- Remote Docker can publish multiple IPs; pick the network with
  `REMOTE_PG_NETWORK`.
- `pg_dump --disable-triggers` emits `session_replication_role`; forbid it.
- Manifest freshness must bind inventory/policy SHA, not file mtime.
- Forbidden-statement grep must match whole SQL statements, or JSON text such
  as `DELETE /api/notes/:id` false-positives.
- Bash `[[ "$(cmd)" ==` plus a newline silently bypasses hash comparison.
- `llm_gateway.maintain` is owned by `ai-native-maintain`, not this repo.
- Baseline `sql/schema/01-schema.sql` is a snapshot, not additive DDL.
- Unified `all` must stay fail-closed; the steps have different risk gates.

## Skill

Reuse `docs/skills/pg-instance-sync-252/SKILL.md` (also installed locally
as `~/.cursor/skills/pg-instance-sync-252/SKILL.md`).
