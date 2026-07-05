# Phase 23 — Columnar Storage Invariant

> **Date**: 2026-07-02
> **Status**: Production-grade. Idempotent. Self-healing.
> **Replaces**: ad-hoc `ALTER TABLE ... SET ACCESS METHOD columnar` one-offs.

## Why this phase exists

The 2026-07-02 audit showed that **only ~5 MB of columnar data was actually
compressed** on 184; the rest (≈ 3.8 GB across `request_logs_default`,
`request_logs_archive_2026_06`, `credential_model_index_2026_06`,
`routing_decision_log_*`) was still heap. The root causes were:

1. The four `ensure_<table>_partition()` SQL functions called by
   `bg.PartitionManager` forgot to append `USING columnar` for
   INSERT-only parents.
2. There was no runtime safety net to catch new heap partitions.
3. There was no startup-time diagnostic to detect drift.

Phase 23 fixes all three with **durable SQL machinery**, not a one-off
script.

## Compatibility policy (read this before adding tables)

The invariant is enforced by a whitelist. Each partition parent falls into
exactly one of three buckets:

| Bucket | Examples | Access method | Why |
|---|---|---|---|
| INSERT-only | `routing_decision_log`, `credential_model_index` | **columnar** | Go source has only INSERT, no UPDATE/DELETE on rows |
| UPDATE-heavy | `request_logs`, `request_wal`, `usage_ledger` | **heap** | Code path enriches rows after stream completes |
| Large JSONB bodies | `request_logs` (multi-MB bodies) | **heap** | Citus columnar 1 GB serialization buffer overflow |

The whitelist lives in `columnar_insert_only_parents()` (one function).
Anything outside that list stays heap and is not converted, ever.

## Files

```
phase-23-columnar-invariant/
├── 00-prereqs.sql                — extension check
├── 01-rewrite-ensure-functions.sql
├── 02-event-trigger.sql          — runtime safety net
├── 03-healthcheck-and-heal.sql   — diagnostic + repair SQL functions
├── 99-verify.sql                 — final report
└── README.md                     — this file
```

## What this phase installs

- **`columnar_insert_only_parents()`** — single source of truth for the
  whitelist. Returns `text[]`.
- **Patched `ensure_*_partition()` functions** — INSERT-only parents
  emit `USING columnar`; UPDATE-heavy parents stay heap.
- **`enforce_columnar_partition(part_name, parent_name)`** — converts
  a single partition to columnar. Idempotent, exception-safe.
- **`fn_enforce_columnar_event_trigger()`** — `ddl_command_end` event
  trigger that converts any new heap partition of an INSERT-only parent.
- **`columnar_healthcheck()`** — per-partition compliance report.
- **`columnar_drift_report()`** — per-parent compact summary (one row
  per parent). Suitable for alert messages.
- **`columnar_heal()`** — runtime converter; safe to call from cron.

## How to apply

```bash
# Run all phase-23 files in order on the PG primary.
bash scripts/phase-23-apply.sh 184
# Or manually:
for f in deploy/sql/phase-23-columnar-invariant/[0-9]*.sql; do
  kubectl exec -n pms-test llm-gateway-pg-<pod> -c citus -- \
    env PGPASSWORD='...' psql -U llm_gateway -d llm_gateway -f "$f"
done
```

## How to verify

```sql
-- Compact per-parent report (suitable for an alert rule)
SELECT * FROM columnar_drift_report();

-- Full per-partition compliance report
SELECT parent_name, partition_name, storage, expected, compliant
FROM columnar_healthcheck()
WHERE NOT compliant;

-- On-demand repair (idempotent)
SELECT * FROM columnar_heal();
```

## Operational notes

- **Daily cron** (`scripts/columnar-daily-cron.sh`) runs after the
  existing monthly archive cron. It calls `columnar_drift_report()`,
  emits a diff message to the gateway log, and invokes `columnar_heal()`
  on any non-compliant INSERT-only partitions.
- **Gateway boot** (`cmd/gateway/main.go`) calls `columnar_healthcheck()`
  once during startup; non-compliance is logged at WARN level but does
  not block startup (the event trigger will repair on the next DDL,
  and the daily cron heals as a safety net).
- **Idempotency**: every SQL operation checks `pg_am` / `pg_class` /
  `pg_proc` / `pg_event_trigger` for current state before acting.
  Re-running the phase produces no diffs.

## Adding a new table to the invariant

If you onboard a new INSERT-only time-series table:

1. Add a row to `bg/partition_manager.go::ensureSpecs()`.
2. Add a migration that creates `ensure_<new_table>_partition()` and
   emits `USING columnar`.
3. Add the table name to `columnar_insert_only_parents()` in
   phase-23 / 02-event-trigger.sql.
4. Re-run phase 23 / 99-verify.sql to confirm compliance.

## Relationship with phase 22

- Phase 22 (2026-06-26) was a one-shot conversion of the small tables
  to match 184 production. It is now obsolete but kept for git history.
- Phase 23 (this phase) supersedes it with a durable invariant.
- Running phase 22 after phase 23 is a no-op because the access methods
  are already correct.

## Future: body-table split (migration 328)

The `request_logs` family is still UPDATE-heavy because:

- rows are enriched by `telemetry/client.go` after the response stream
  completes (cost, tokens, latency);
- multi-MB JSONB `request_body` / `outbound_body` / `response_body`
  columns overflow the columnar 1 GB buffer.

A future migration 328 is planned to split the body columns into a
sibling table `request_logs_bodies (id PK + body fields, columnar)` so
the metadata-only `request_logs_*` partitions can finally join the
columnar invariant.
