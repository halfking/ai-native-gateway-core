# Stats Reconciliation Production Rollout Runbook

> Audience: on-call SRE + data platform owner. Use this runbook when
> enabling, observing, or rolling back any component of the
> `/api/admin/stats/reconciliation` pipeline (legacy-to-canonical
> reconciliation worker, async stats inbox consumer, admin approval /
> adjustment writes, and shadow read comparator for the legacy
> `/api/usage/summary` + dashboard board endpoints).

## 1. Pre-flight

| Check | Command | Expected |
|---|---|---|
| Migrations 536 / 537 / 539 / 540 applied on 252 | `psql -c "SELECT version FROM schema_migrations WHERE version LIKE '5%' ORDER BY version;"` | all four listed |
| `stats_event_inbox` column set complete | `psql -c "\d stats_event_inbox"` | `processing_status`, `next_attempt_at`, `fencing_token`, `dead_lettered_at`, `dead_letter_reason`, `processing_owner`, `lease_until`, `retryable` |
| Indexes ready | `psql -c "SELECT indexname FROM pg_indexes WHERE tablename='stats_event_inbox';"` | `idx_stats_event_inbox_claimable`, `idx_stats_event_inbox_dead_letter` |
| `usage_facts` table present | `psql -c "\dt usage_facts"` | exists |
| `stats_reconciliation_runs/diffs` present | `psql -c "\dt stats_reconciliation_*"` | exists |
| `stats_adjustments` schema parity | `psql -c "\d stats_adjustments"` | **must** contain `adjustment_id` (unique), `metric` columns — see §6 |

> Migration **540** sets `lock_timeout='5s'` and runs `ALTER TABLE` on
> `stats_event_inbox`. Apply during the lowest write window (typically
> 03:00–04:00 local) and verify the Citus event trigger
> `enforce_columnar_trigger` is not active on the target database —
> `pg_dump`-style replay with empty `search_path` will fail. See
> `pg-schema-sync-252` skill for the `DISABLE`/`ENABLE` ritual.

If you are deploying to a fresh environment, run the
`pg-schema-sync-252` skill first to confirm `stats_event_inbox`,
`usage_facts`, `stats_reconciliation_*`, and `stats_adjustments` exist
in both 252 and the target cluster before any code rollout.

## 2. Schema readiness check

```sql
-- 2.1 Inbox consumer tables and constraints
SELECT to_regclass('stats_event_inbox')        AS inbox,
       to_regclass('usage_facts')             AS facts,
       to_regclass('stats_reconciliation_runs') AS runs,
       to_regclass('stats_reconciliation_diffs') AS diffs,
       to_regclass('stats_adjustments')       AS adjustments;

-- 2.2 Adjustment table parity (CURRENT MIGRATION 536 LACKS metric_name/adjustment_type)
SELECT column_name
FROM information_schema.columns
WHERE table_name = 'stats_adjustments'
ORDER BY ordinal_position;

-- 2.3 Optional: confirm there is no leftover event trigger on columnar tables
SELECT evtname FROM pg_event_trigger WHERE evtname = 'enforce_columnar_trigger';
```

If `adjustments` row exists but is missing `adjustment_id`, every
approval will currently fail at the INSERT step — **disable the
`/api/admin/stats/reconciliation/approve` route until §6 ships a
follow-up migration.**

## 3. Shadow read ladder

The legacy `/api/usage/summary` and `/api/admin/dashboard/board`
endpoints are kept on the legacy response contract; canonical stats
are sampled asynchronously and compared via shadow read.

| Sample % | What to watch | Promotion gate |
|---|---|---|
| 0 | Pipeline disabled; counters stay flat | n/a |
| 1 | `llm_gateway_stats_shadow_comparisons_total{result="material_drift"}` rate | < 5/hour for 24h |
| 5 | `..._dropped_total{endpoint="..."}` and `..._duration_seconds` p99 | dropped=0, p99 < 250ms |
| 25 | Same as above plus ops dashboard | No Page |
| 100 | Full traffic | Compare against canonical for 7d before any cutover |

`result="partial_window"` is the **expected** legacy rolling-window
classification. Do **not** treat it as drift.

PromQL cookbook:

```promql
# sustained material drift
sum by (endpoint) (rate(llm_gateway_stats_shadow_comparisons_total{result="material_drift"}[1h])) > 5

# queue health (shadow queue = 32; any dropped indicates back-pressure)
rate(llm_gateway_stats_shadow_dropped_total[5m]) > 0

# compare endpoint latency vs SLO (250ms p99)
histogram_quantile(0.99, sum by (le, endpoint) (rate(llm_gateway_stats_shadow_duration_seconds_bucket[5m]))) > 0.25
```

## 4. Inbox consumer enable / drain / rollback

The async stats inbox consumer is gated by
`LLM_GATEWAY_STATS_INBOX_CONSUMER=1`. It also requires the schema
readiness check above (otherwise `InboxConsumerSchemaReady` returns
false and the synchronous path remains in place).

### 4.1 Enable ladder

1. Pre-stage: in 245 only, set the env var and restart a single
   replica. Wait 1h and verify
   `llm_gateway_stats_shadow_*` (synchronous fallback) plus the
   inbox-specific counters (added by future migration) are stable.
2. Roll 252 one shard at a time. Use the load balancer's blue/green
   to drain each shard before flipping.
3. Verify the synchronous projection is no longer taking writes:
   ```sql
   SELECT processing_status, count(*)
   FROM stats_event_inbox
   GROUP BY 1;
   ```
   Expected `processing='processing'` count near 0 with steady
   `processed` growth.

### 4.2 Drain before rollback

```sql
-- Wait until no rows are leased
SELECT count(*) FROM stats_event_inbox
WHERE processing_status = 'processing';

-- Mark any stragglers retryable so a compatible binary can pick them up
UPDATE stats_event_inbox
SET processing_status = 'retryable',
    processing_owner = NULL,
    lease_until = NULL,
    retryable = true,
    next_attempt_at = now()
WHERE processing_status = 'processing';
```

Only after the drain returns 0 can you safely unset
`LLM_GATEWAY_STATS_INBOX_CONSUMER` and roll back to an older image.

> **Critical:** an old image cannot claim from `stats_event_inbox`.
> If you must roll back to a pre-540 binary, drain first or accept
> backlog until the binary is restored. The pre-540 binary's
> synchronous projection still works because it does not depend on
> `stats_event_inbox`.

## 5. Reconciliation worker

The worker (`statsReconciliationWorker` in `cmd/gateway/main.go`) ticks
every 6h and runs `ReconcilePeriod` for the last 7 days.

| Metric | Threshold | Action |
|---|---|---|
| `llm_gateway_stats_reconciliation_runs_total{status="failed"}` rate | > 5% of total runs in last 24h | Page on-call |
| `llm_gateway_stats_reconciliation_diffs_total{resolution="open"}` rate | > 2× historical p95 over 6h | Page on-call |
| `runs_total{status="completed"}` rate | 4 / day baseline (one per 6h tick) | none |

Recovery:

```sql
-- Investigate the most recent failed run
SELECT run_id, started_at, finished_at, error
FROM stats_reconciliation_runs
WHERE status = 'failed'
ORDER BY started_at DESC
LIMIT 5;
```

If failures are caused by `maxReconciliationRows` (100000), either
shrink the window (change `defaultReconciliationInterval` callers)
or split the period manually:

```go
worker.ReconcilePeriod(ctx, start, mid, "manual_split")
worker.ReconcilePeriod(ctx, mid, end, "manual_split")
```

> The worker has a one-shot lifecycle: `Stop()` flips `started=true`
> permanently. Re-running requires a process restart, not
> `Start()` again. Plan shutdowns accordingly.

## 6. Approval / Adjustment

`POST /api/admin/stats/reconciliation/approve` runs in a single
transaction. Currently **migration 536's `stats_adjustments` table
defines `adjustment_id`/`metric`** while the handler writes
`adjustment_type`/`metric_name` and omits `adjustment_id`. This
mismatch causes the INSERT to fail and rolls back the whole batch
unless every diff was already resolved.

What this looks like in metrics:

```
llm_gateway_stats_adjustments_total{action="approve",result="failed"} > 0
llm_gateway_stats_adjustments_total{action="approve",result="committed"} == 0
```

Until a follow-up migration aligns the schema, treat the approval
endpoint as read-only: either reject at the router layer or keep
`super_admin` callers informed via the runbook.

The follow-up migration must add (or rename) at minimum:

```sql
ALTER TABLE stats_adjustments
    ADD COLUMN IF NOT EXISTS adjustment_id text NOT NULL DEFAULT gen_random_uuid()::text UNIQUE,
    ADD COLUMN IF NOT EXISTS adjustment_type text NOT NULL DEFAULT 'reconciliation',
    ADD COLUMN IF NOT EXISTS metric_name text;

-- Backfill existing rows so legacy `metric` rows are still valid
UPDATE stats_adjustments
SET metric_name = metric
WHERE metric_name IS NULL AND metric IS NOT NULL;
```

After the migration ships:

- Restart gateway. Existing scheduled diffs remain valid.
- Replay by issuing approvals; expect
  `llm_gateway_stats_adjustments_total{action="approve",result="committed"}` > 0 within minutes.
- If you see `result="failed"` post-migration, check the
  `stats_adjustments` constraint catalog (the unique constraint on
  `adjustment_id` will fail on duplicate UUIDs).

## 7. Rollback matrix

| Trigger | Action | Verification |
|---|---|---|
| `llm_gateway_stats_shadow_comparisons_total{result="material_drift"}` rate spike | Set `stats.shadow_read.sample_percent=0` | dropped / duration metrics flat for 10m |
| `runs_total{status="failed"}` rate > 5% over 24h | Stop the worker via process restart, inspect `stats_reconciliation_runs.error` | failure rate < 1% |
| Inbox consumer OOM / stalled `processing` count | Unset env var, drain per §4.2 | `processing` count = 0 |
| Approval schema mismatch | Disable `/api/admin/stats/reconciliation/approve`, ship §6 migration, re-enable | `committed` > 0 |

## 8. Change control

- Any threshold change in this runbook requires a PR reviewed under
  the `comprehensive-code-audit` and `llm-gateway-test` skills.
- CI must keep `go test -race ./metrics ./domains/stats ./admin` green
  on the default branch.
- Schema migrations affecting this surface must reference this runbook
  in the PR description and follow the
  `pg-release-schema-manager` skill.