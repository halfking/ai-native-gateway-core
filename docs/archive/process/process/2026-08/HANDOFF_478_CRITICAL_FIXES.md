# Handoff: Migration 478 auto-route affinity — CRITICAL fixes

Source audit: SQL/data-integrity review of migration 478 and its three workers, 2026-08-11.
Scope of this doc: **CRITICAL-1 and CRITICAL-2 only.** The HIGH/MEDIUM findings
(window re-counting, missing partition automation, LATERAL cost, PK/GROUP BY
mismatch, down-migration orphan, inert `explore`/`tenant_id`) are deliberately
out of scope here — do not bundle them.

Line numbers below are from the audit and are **approximate**. Re-read each file
end to end before editing.

## Current state

The feature is a complete no-op in this deployment. Two independent defects each
break the loop, and they mask each other:

- CRITICAL-1 makes every settle query fail at plan time, so nothing is ever settled.
- CRITICAL-2 means `canonical_id` is always NULL, so even with settling fixed the
  learner aborts on the first row.

The only symptom is a `slog.Warn` per sweep. `llmgw_autoroute_settled_total` and
`llmgw_autoroute_affinity_upserts_total` sit flat at zero while
`auto_route_selections` grows with 100% unsettled rows. An alert on
"selections written but settled_total == 0" would have caught both.

## Step 0 — verify the one precondition the CRITICAL-1 fix rests on

The fix drops `request_logs` (the partitioned parent) from the settle path and
reads `request_logs_hot` only. That is correct **iff** hot retention exceeds the
worker's lookback windows: `settleAbandonAfter` = 24h and `baselineWindow` = 24h.
The in-code comment claims ~7 days (citing migration 406). Confirm before relying
on it:

```sql
-- read-only on llm_gateway
SELECT min(ts) AS oldest_hot_row,
       now() - min(ts) AS hot_retention_span,
       count(*)        AS hot_rows
FROM request_logs_hot;
```

If `hot_retention_span` is comfortably above 24h, proceed with the primary fix.
If it is below 24h, use the fallback in CRITICAL-1 §Fallback instead.

---

# CRITICAL-1 — `requestLogSource` fails at plan time

**Location:** `bg/auto_route_settle_worker.go`, `requestLogSource` (~:179-186),
consumed by `loadTaskBaselines` (~:196) and `settleBatch` (~:265, twice).

## Symptom

Every query built on `requestLogSource` fails before execution:

```
ERROR:  invalid perminfoindex 0 in RTE with relid 0
```

`settleBatch` returns that error, `sweep` logs `auto-route settle sweep failed`
and returns. No row is ever stamped `settled_at`, so the aggregate's
`WHERE reward IS NOT NULL` matches nothing. The `settleAbandonAfter` backstop
never runs either, because it lives after the query that fails.

## Root cause

Environment: PostgreSQL 17.10, `citus 13.3-1`, `citus_columnar 13.3-1`.
The `request_logs` partitions use the **columnar** table access method:

```
 request_logs           | (partitioned parent)
 request_logs_2026_07   | columnar
 request_logs_2026_08   | columnar
 request_logs_hot       | heap
```

Empirically verified trigger conditions — all four together are required:

1. a set operation (`UNION ALL`), containing
2. the partitioned **parent** relation (not a leaf partition), whose
3. children use the columnar access method.
4. Fails at **plan** time: bare `EXPLAIN` fails, and it is independent of RLS
   (`SET row_security=off` changes nothing; the session was superuser, so the
   `tenant_isolation_request_logs` policy was bypassed anyway).

Minimal repro, isolating the trigger:

```sql
CREATE EXTENSION IF NOT EXISTS citus_columnar;
CREATE TABLE c_hot (request_id text, ts timestamptz) USING heap;
CREATE TABLE c_parent (request_id text, ts timestamptz) PARTITION BY RANGE (ts);
CREATE TABLE c_parent_2026_08 PARTITION OF c_parent
  FOR VALUES FROM ('2026-08-01') TO ('2026-09-01') USING columnar;
INSERT INTO c_hot VALUES ('a', now());
INSERT INTO c_parent VALUES ('b', '2026-08-05');

-- OK: plain scan of the columnar parent
SELECT count(*) FROM c_parent;                                  -- 1
-- OK: leaf columnar partition inside UNION ALL
SELECT count(*) FROM (SELECT request_id FROM c_hot
       UNION ALL SELECT request_id FROM c_parent_2026_08) x;    -- 2
-- FAILS: partitioned parent inside UNION ALL
SELECT count(*) FROM (SELECT request_id FROM c_hot
       UNION ALL SELECT request_id FROM c_parent) x;
-- ERROR: invalid perminfoindex 0 in RTE with relid 0
```

Mechanism, stated with the uncertainty it deserves: PG16 moved per-relation
permission bookkeeping into a separate `RTEPermissionInfo` list indexed by
`RangeTblEntry.perminfoindex`; subquery/appendrel RTEs legitimately carry
`perminfoindex = 0` and `relid = 0`. The error is a planner-side assertion that
some RTE *must* have permission info when it does not. The trigger conditions
above are verified; **which** citus planner hook mishandles the expansion was not
traced. That does not matter for the fix — the trigger conditions do.

**This is not a 478-only bug.** Any query in the repo that unions `request_logs`
has the same defect. Run this and triage separately:

```
git grep -n -i -E "union all" -- '*.go' '*.sql' | grep -i request_logs
```

## The fix

Reading `request_logs_hot` alone is sufficient for this worker, given Step 0.
Every row the settle path can touch is 2 minutes to 24 hours old
(`settleDelay` → `settleAbandonAfter`), and baselines look back 24h. Rows older
than hot retention are already abandoned and never re-examined. The union was
unnecessary, not merely unlucky.

Delete the `requestLogSource` constant. Replace the three call sites:

### Site 1 — `loadTaskBaselines`

```sql
SELECT task_type,
       COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)::int AS p95_latency_ms,
       COALESCE(percentile_cont(0.75) WITHIN GROUP (ORDER BY cost_usd), 0)        AS p75_cost_usd
FROM request_logs_hot rl
WHERE rl.ts >= NOW() - $1::interval
  AND rl.is_auto_request = TRUE
  AND rl.latency_ms IS NOT NULL
GROUP BY task_type
```

Note for anyone tempted to "fix" this by splitting into two queries and merging
in Go: **percentiles do not merge.** `p95(A ∪ B)` cannot be derived from `p95(A)`
and `p95(B)`. Hot-only is correct; a Go-side merge of two percentile results
would be silently wrong.

### Site 2 + 3 — `settleBatch`

Also carries the CRITICAL-2 backfill columns (`rl.canonical_id`, `rl.tenant_id`).
`request_logs_hot.tenant_id` is `text NOT NULL` while
`auto_route_selections.tenant_id` is `VARCHAR(64)`, hence the `LEFT(...,64)`.

```sql
SELECT s.id, s.partition_date, s.request_id, s.task_type, s.canonical_id, s.ts,
       rl.success, rl.latency_ms, rl.cost_usd,
       rl.canonical_id        AS rl_canonical_id,
       LEFT(rl.tenant_id, 64) AS rl_tenant_id,
       ss.health_score, ss.error_count, ss.request_count,
       mr.model_reqs, mr.retry_count
FROM auto_route_selections s
LEFT JOIN request_logs_hot rl
       ON rl.request_id = s.request_id
LEFT JOIN session_summaries ss
       ON ss.session_key = s.session_id
LEFT JOIN LATERAL (
       SELECT COUNT(*)::int AS model_reqs,
              SUM(GREATEST(COALESCE(jsonb_array_length(r2.routing_attempts), 1) - 1, 0))::int AS retry_count
       FROM request_logs_hot r2
       WHERE s.session_id IS NOT NULL
         AND r2.gw_session_id = s.session_id
         AND r2.canonical_id IS NOT DISTINCT FROM s.canonical_id
) mr ON TRUE
WHERE s.settled_at IS NULL
  AND s.ts < NOW() - $1::interval
ORDER BY s.ts
LIMIT $2
```

The LATERAL body is otherwise unchanged — its `IS NOT DISTINCT FROM` defect is
MEDIUM-1 and stays out of scope. See §Interactions: CRITICAL-2 changes its
behaviour, so read that before shipping.

## DO NOT UNDO — leave this comment in the code

`FROM request_logs_hot` with no union looks like a missing-history bug. It is
deliberate. Put this above the queries so the next reader does not "restore" it:

```go
// DO NOT add `UNION ALL request_logs` here. The request_logs partitions use the
// citus columnar access method, and a UNION ALL containing the partitioned
// PARENT fails at plan time with:
//     ERROR: invalid perminfoindex 0 in RTE with relid 0
// (PG 17.10 / citus 13.3, verified 2026-08-11; leaf partitions are fine, the
// parent is not). Hot-only is also sufficient: this worker never looks further
// back than settleAbandonAfter (24h) and baselineWindow (24h), both well inside
// request_logs_hot retention. See docs/HANDOFF_478_CRITICAL_FIXES.md.
```

## Fallback — only if Step 0 shows hot retention < 24h

Do **not** reintroduce the union. Instead:

- Join paths (sites 2, 3): run two queries — one against `request_logs_hot`, one
  against `request_logs` — and merge in Go keyed on `request_id`, hot winning.
  A lookup is a merge, not an aggregate, so this is safe.
- Baselines (site 1): keep hot-only and shorten `baselineWindow` to fit
  retention. Do not merge percentiles across the two tables.

Alternative if the parent is genuinely required in one statement: reference it
without a set operation (correlated subquery / `LATERAL` / `COALESCE` over two
scalar subqueries). Untested against the columnar bug — verify with `EXPLAIN`
against `llm_gateway` before adopting.

## Pre-existing caveat, not introduced here

`request_logs_hot` PK is `(request_id, ts)`, so `LEFT JOIN ... ON request_id`
can multiply a pending row if a `request_id` ever appears twice with different
`ts`. The old union code had the same exposure and its comment asserts
`request_id` uniqueness. If you want to close it, `DISTINCT ON (request_id)
... ORDER BY request_id, ts DESC` in a subquery. Low priority.

---

# CRITICAL-2 — `canonical_id` is never populated

**Locations:** `domains/streaming/auto_route.go` (~:388-421),
`domains/hooks/observability/telemetry/selection_writer.go` (~:254-259),
`bg/auto_route_settle_worker.go` (~:414-426),
`bg/auto_route_affinity_worker.go` (~:177-213).

## The chain

1. `recordAutoSelection` never sets `CanonicalID` (nor `TenantID`, nor `Explore`).
2. `nullableInt64(0)` maps the zero value to SQL NULL.
3. The comment at `auto_route.go` ~:381-387 asserts *"The settle worker joins on
   request_id and backfills both."* **It does not.** `writeReward` sets only
   `success, latency_ms, cost_usd, reward, reward_source, settled_at`; `abandon`
   sets only `settled_at, reward_source`. That false comment is why this survived
   review — delete it as part of the fix.
4. `task_model_affinity.canonical_id` is `BIGINT NOT NULL`, so the learner cannot
   store the bucket.
5. It never even reaches the INSERT: pgx fails scanning NULL into `int64` first.

Verified in a scratch DB with 478 applied:

```
 auto_route_selections | canonical_id | YES   <- nullable
 task_model_affinity   | canonical_id | NO    <- NOT NULL

-- after simulating writeReward:
 request_id | canonical_id | reward | settled
 req-1      |              |  0.850 | t          <- still NULL

-- feeding that into the affinity upsert:
ERROR:  null value in column "canonical_id" of relation "task_model_affinity"
        violates not-null constraint
```

## Fix A (primary) — backfill at settle time

`request_logs.canonical_id` / `.tenant_id` are populated by the request pipeline,
so the settle join is the reliable source and needs no name→id resolution on the
hot path. Site 2 above already selects `rl_canonical_id` / `rl_tenant_id`.

Add to `pendingSelection`:

```go
rlCanonicalID *int64
rlTenantID    *string
```

scan them in the same order as the SELECT, then `writeReward`:

```sql
UPDATE auto_route_selections
SET success       = $1,
    latency_ms    = $2,
    cost_usd      = $3,
    reward        = $4,
    reward_source = $5,
    canonical_id  = COALESCE(canonical_id, $6),
    tenant_id     = COALESCE(tenant_id, $7),
    settled_at    = NOW()
WHERE id = $8 AND partition_date = $9
  AND settled_at IS NULL
```

`COALESCE(canonical_id, …)` so a decision-time value, once Fix D lands, always
wins over the log. Args become
`(success, latencyMs, costUSD, reward, source, rlCanonicalID, rlTenantID, id, partitionDate)`.

Abandoned rows keep `canonical_id = NULL`, which is fine: they have
`reward IS NULL` and the aggregate already excludes them.

## Fix B — make the NOT NULL target unreachable

`bg/auto_route_affinity_worker.go`, `aggregate()`. Add the guard so a backfill
miss degrades to "one bucket skipped" instead of "sweep dies":

```sql
WHERE reward IS NOT NULL
  AND canonical_id IS NOT NULL
  AND settled_at >= NOW() - $1::interval
```

## Fix C — stop the `continue`-on-scan-error pattern (audit item CRITICAL-2b)

`bg/auto_route_affinity_worker.go` ~:202-208, `settleBatch` ~:300,
`applyStalenessDecay` ~:329.

In pgx v5 a Scan error **terminates iteration** and sets `rows.Err()`; it does
not skip one row. `continue` therefore silently truncates the result set.
Verified: a 2-row result where row 1 had a NULL `canonical_id` yielded

```
SCAN FAILED: can't scan into dest[2] (col: canonical_id): cannot scan NULL into *int64
rows.Err:   can't scan into dest[2] (col: canonical_id): cannot scan NULL into *int64
RESULT: scanned=0 skipped=1     <- row 2 never seen
```

`aggregate` then returns that error and `sweep` bails at ~:150-152, so
`applyStalenessDecay` and `recomputeRanks` never run either.

Fix in all three: scan nullable columns into pointer types, decide explicitly
(skip + increment a counter + log at Debug/Warn), and treat a genuine scan error
as fatal to the sweep rather than swallowing it. Do this in the same pass as A
and B — this pattern is what turned CRITICAL-2 from a loud constraint violation
into an invisible no-op, and it will hide the next bug just as well.

## Fix D (optional, follow-up) — populate at decision time

Set `CanonicalID`/`TenantID` in `recordAutoSelection` so rows are complete before
settling. `Decision` carries the canonical *name*, not the id, so this needs a
name→id lookup; check whether `decision.CandidatesTopN[i].Candidate` already
exposes an id before adding a catalog query to the request path. Not required for
correctness once A–C are in. `Explore` is also unset — that is MEDIUM-6, separate.

---

# Verification

Run in this order. Step 3 is the one that actually proves CRITICAL-1, because the
failure only reproduces against the columnar schema.

1. `go build ./...` && `go vet ./...`
2. `go test ./bg/... ./autoroute/... ./domains/hooks/observability/telemetry/...`
3. **Read-only against `llm_gateway`:** execute the rewritten `loadTaskBaselines`
   and `settleBatch` SQL verbatim. Pass = rows returned (0 rows is fine).
   Fail = `invalid perminfoindex 0 in RTE with relid 0`. Before the fix both
   statements error; that is the before/after signal.
4. **Throwaway scratch DB** (`CREATE DATABASE audit_scratch_478`, drop when done;
   never write to `llm_gateway`): apply 478, insert a selection whose matching
   `request_logs_hot` row has a non-NULL `canonical_id`, run the settle UPDATE,
   confirm `canonical_id` is now populated, run the aggregate, confirm a
   `task_model_affinity` row is written rather than skipped.
5. Post-deploy: `llmgw_autoroute_settled_total` and
   `llmgw_autoroute_affinity_upserts_total` must leave zero. If they do not, the
   loop is still broken regardless of what the logs say.

# Interactions and what stays broken

- **CRITICAL-2 activates the session-attribution path.** Once `canonical_id` is
  non-NULL, the LATERAL's `r2.canonical_id IS NOT DISTINCT FROM s.canonical_id`
  starts matching real values instead of only NULL-canonical rows, so
  `model_reqs`, `RetryRatio` and `reward_source='session'` all begin to move.
  Expect reward values to shift after deploy — that is the fix working, not a
  regression. The NULL-tolerant operator remains wrong for rows where resolution
  failed (MEDIUM-1): prefer `=` plus an explicit `s.canonical_id IS NOT NULL`
  guard when that item is picked up.
- **Still broken after this work** (unchanged, by design): HIGH-1 window
  re-counting inflating `sample_count`/`confidence`; HIGH-2 no partition
  automation past 2026-09 (**deadline: fix before 2026-10-01, after which the
  DEFAULT partition cannot be split without DETACH**); MEDIUM-2 LATERAL cost and
  the missing `request_logs_hot (gw_session_id)` index; MEDIUM-3 no `settled_at`
  index; MEDIUM-4 `GROUP BY`/PK mismatch clobbering buckets; MEDIUM-5
  down-migration orphan; MEDIUM-6 inert `explore`/`tenant_id`.
- Because HIGH-1 is still live, treat `sample_count` and `confidence` as
  unreliable when eyeballing whether the fix worked. Use
  `llmgw_autoroute_settled_total` and raw
  `auto_route_selections.reward IS NOT NULL` counts instead.
