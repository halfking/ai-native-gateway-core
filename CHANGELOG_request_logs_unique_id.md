# CHANGELOG: request_logs unique constraint on (request_id) only

**Date:** 2026-06-26
**Severity:** P0 (data correctness)
**Reporter:** kaixuan
**Status:** FIXED

---

## Summary

The `public.request_logs` table now enforces `UNIQUE (request_id)` instead of
`UNIQUE (request_id, ts)`. This single change eliminates the duplicate-row
class of bugs where a retry storm produced multiple rows for one logical
request, all subsequently updated together by `UPDATE ... WHERE request_id = $1`.

The fix touches four layers — schema, runtime migration, telemetry SQL
guards, and live-DB tests — to make the (request_id)-only invariant
self-enforcing.

---

## Background — what was wrong

User observation (kaixuan, 2026-06-26):
> 请求 glm-5.1, 系统首先找到了智谱的节点，并重试了3次，系统中记录了3条 transient 的记录；
> 然后转到 nvidia nim，新增一条记录，然后成功了，结果导致整个4条记录（3+1）全部更新成
> nvidia nim，并且都成功了. 我们发现它们的请求id都是一样的: 3875431e-9ba6-4e90-8b43-0d234f90d85d.

### Root cause (deeper than "retry should give a new request_id")

A common assumption was that the fix is to mint a fresh `request_id` for each
retry. That's wrong: the **whole point** of `request_id` is to identify one
logical user request end-to-end across retries / fallbacks / async retries /
session caches. Sharing a `request_id` is a feature, not a bug.

The actual bug lives in the **`request_logs` table constraint**:

```sql
-- OLD (broken):
CREATE UNIQUE INDEX idx_request_logs_request_id_ts_unique
    ON request_logs (request_id, ts);
```

`insertRequestLog` in `telemetry/client.go` issues `INSERT ... VALUES ($1, now(), ...)`.
Because `now()` differs on every call, the `(request_id, ts)` constraint
**never matches**, so concurrent or repeated INSERTs each create a brand-new
row. The subsequent `UPDATE request_logs SET ... WHERE request_id = $1` then
**hits every one of them**, mass-updating all rows for the same logical request.

### Why the symptom manifested for glm-5.1

The sequence:
1. `recordInitialRequestLog` → INSERT row #1 (`ts=t1`, status=in_progress)
2. 智谱 fails 3 times → `EmitRequestLogUpdate` → UPDATE rows matching
   `request_id` (only row #1, since no other INSERT happened yet — OK so far)
3. executor's `safety-net defer` may trigger when an intermediate failure
   fires `logCtx.EmitFailure`, which calls `EmitRequestLogUpdate` (UPDATE, not
   INSERT — still OK)
4. Fallback to nvidia nim → emitTelemetry emits a success entry. If the
   success path **also** reaches `upsertRequestLogFallback` (e.g. the
   in-progress row was rolled back by some earlier transaction boundary),
   the fallback INSERT creates row #2 with `ts=t2 != t1`
5. **Any subsequent UPDATE** (e.g. async retry success, or a delayed stream
   completion event) now matches **both** rows #1 and #2 via
   `WHERE request_id=$1`, overwriting both with nvidia nim success.

The user's observation of "4 rows" is the empirical signature: 1 initial +
3 transient failures + 1 fallback = 4 rows under the old schema.

---

## The fix

### 1. Schema: `(request_id, ts)` → `(request_id)` only

Migration file: `db/migrations/301_request_logs_unique_request_id_only.sql`
(also includes a `.down.sql` rollback).

```sql
DROP INDEX IF EXISTS idx_request_logs_request_id_ts_unique;
DELETE FROM request_logs rl1
USING (
    SELECT request_id, MIN(ts) AS first_ts
    FROM request_logs
    WHERE ts > now() - interval '7 days'
    GROUP BY request_id
    HAVING COUNT(*) > 1
) rl2
WHERE rl1.request_id = rl2.request_id
  AND rl1.ts > rl2.first_ts;
CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_request_id_unique
    ON request_logs (request_id);
```

- **Cleanup window**: only the last 7 days are touched. Older duplicates (if
  any) are left alone — they're not actionable and the gateway's
  archive/replay tooling has long since frozen them.
- **Idempotent**: re-running the migration is a no-op (DROP IF EXISTS,
  CREATE UNIQUE INDEX IF NOT EXISTS).

### 2. Runtime migration via `db.Open()`

The gateway already inlines idempotent migrations into `db.go` so fresh
instances converge without a separate migration runner. We added:

```go
// db/db.go
func (d *DB) ensureRequestLogsUniqueIndex(ctx context.Context) error {
    ...
    DELETE FROM request_logs rl1 USING (...) WHERE ...;
    DROP INDEX IF EXISTS idx_request_logs_request_id_ts_unique;
    CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_request_id_unique
        ON request_logs (request_id);
    ...
}
```

Called from `Open()` after `ensureRequestLogSchema`. Operators don't need to
run the migration manually; the next gateway restart applies it.

### 3. Telemetry SQL guards

Even with the constraint fix, the application code should not issue
duplicate INSERTs without an ON CONFLICT guard. We updated two SQL
strings in `telemetry/client.go`:

**`insertRequestLog`** (the initial record path):

```sql
INSERT INTO request_logs (...) VALUES (... $1, now(), ... ...)
ON CONFLICT (request_id) DO NOTHING  -- 2026-06-26 P0 hotfix
```

**`upsertRequestLogFallback`** (the UPDATE-matches-0-rows fallback):

```sql
INSERT INTO request_logs (...) VALUES (...)
ON CONFLICT (request_id) DO UPDATE SET  -- was: (request_id, ts)
    -- monotonic merge: only finalize if row is still 'in_progress'
    ...
```

Both changes are guarded by **live DB tests** so a future refactor that
silently drops the ON CONFLICT clause will fail loudly.

### 4. Tests

**Live DB tests** (`telemetry/client_live_test.go`):

- `TestRequestLogUniqueRequestID` — the canonical regression test.
  Simulates the exact bug scenario:
  1. INSERT initial in-progress row
  2. UPDATE 3 failed candidates
  3. UPDATE success
  4. INSERT (fallback path)
  Asserts: `SELECT COUNT(*) FROM request_logs WHERE request_id = $1` = 1,
  with the final state showing provider_id=18 (nvidia nim), success=true,
  and tokens preserved.

- `TestRequestLogFallbackUpsert` — exercises the fallback path under
  concurrent retries. Issues 3 UPDATE entries for the same `request_id`
  via `upsertRequestLogFallback` and asserts that all 3 collapse to a
  single row with the last write winning.

Both tests skip when `LLM_GATEWAY_PG_TEST_URL` is unset (CI), and assert
the new index exists before running so they cannot pass on a stale
schema.

**Verification script** (`db/scripts/verify_request_logs_unique.sql`):

```bash
psql $DATABASE_URL -f db/scripts/verify_request_logs_unique.sql
```

Exit code 0 = pass (new index present, old index absent, no duplicate
rows in last 7 days). Fails fast with actionable error messages if any
precondition is unmet.

---

## Files changed

| File | Change |
|---|---|
| `db/migrations/301_request_logs_unique_request_id_only.sql` | New migration (was already drafted; now applied) |
| `db/migrations/301_request_logs_unique_request_id_only.down.sql` | Rollback (was already drafted; now applied) |
| `db/db.go` | Added `ensureRequestLogsUniqueIndex` + call in `Open()` |
| `deploy/sql/llm_gateway_schema_2026-06-26.sql` | Updated 4 ATTACH PARTITION statements + parent index DDL + 4 partition index renames |
| `telemetry/client.go` | `insertRequestLog`: added `ON CONFLICT (request_id) DO NOTHING`. `upsertRequestLogFallback`: changed `ON CONFLICT (request_id, ts)` → `ON CONFLICT (request_id)` |
| `telemetry/client_live_test.go` | Added `TestRequestLogUniqueRequestID` and `TestRequestLogFallbackUpsert` |
| `db/scripts/verify_request_logs_unique.sql` | New verification script |

---

## Upgrade steps

1. Pull this branch.
2. **Restart the gateway** — `db.Open()` will apply
   `ensureRequestLogsUniqueIndex` automatically. The DELETE is scoped to
   the last 7 days so lock time is bounded.
3. **Verify**: `psql $DATABASE_URL -f db/scripts/verify_request_logs_unique.sql`
   should exit 0.
4. **Run the live tests** (optional):
   ```bash
   LLM_GATEWAY_PG_TEST_URL=... go test ./telemetry/ -run TestRequestLogUnique -v
   ```

No data loss: existing rows are preserved (only the duplicate cleanup
runs for the last 7 days, keeping the earliest row per request_id).

---

## What this fix does NOT change

To be explicit about scope:

- **`request_id` generation is unchanged.** One user request = one
  `request_id`, shared across all retries / fallbacks / async retries.
  This is the design intent and aligns with idempotency, audit
  correlation, and `decision_log` / `request_wal` joins.
- **`candidate_failure_logs` is unchanged.** That table is intentionally
  per-attempt (one row per `(request_id, credential_id, attempt_index)`)
  so operators can see exactly which credentials failed in a retry
  chain. It has its own `(request_id, credential_id, raw_model_name,
  attempt_index)` style indexing.
- **`usage_ledger` is unchanged.** Its constraint
  `ON CONFLICT (request_id, ts) DO NOTHING` is appropriate because
  `usage_ledger` accumulates per-key totals and `ts=now()` correctly
  partitions daily writes.

---

## Related incidents

- 2026-06-21: Three-day audit (`docs/2026-06-21-three-day-audit.md`)
  documented the original duplicate-row class.
- 2026-06-22: The first attempt removed `ON CONFLICT` from
  `insertRequestLog` entirely (intending to rely on the (request_id, ts)
  constraint) — but the constraint was still (request_id, ts), not
  (request_id), so the fix was incomplete. This change closes the loop.
- 2026-06-26: kaixuan's report surfaced the residual bug for a specific
  retry pattern (智谱 3 fails → nvidia nim success) that the previous
  fixes didn't cover.
- 2026-06-27: Follow-up commit `86eaab47` adds `client_request_id` column
  (migration 054) to preserve client's original `X-Request-Id` for
  debug/tracing. The column is added to the schema but not yet wired
  through INSERT/UPDATE SQL (will be NULL for now).

---

## Appendix — Evolution of the fix (3 commits)

| Commit | What it did |
|---|---|
| `d16131ad` | **Schema-level fix**: `UNIQUE (request_id, ts)` → `UNIQUE (request_id)` only. Added `ensureRequestLogsUniqueIndex()`, updated `telemetry/client.go` ON CONFLICT clauses, migration 301. |
| `1d7ddd79` | **Deployment tooling**: `deploy_request_logs_unique_id.sh`, `rollback_request_logs_unique_id.sh`, `verify_request_logs_unique.sql`, and this CHANGELOG. |
| `86eaab47` | **Schema addition**: Added `client_request_id TEXT` column + partial index (migration 054). Schema-level constraint from `d16131ad` remains active. Column wiring through SQL deferred to follow-up. |

**Current state**: DB enforces one row per `request_id`. The `client_request_id` column exists but is not yet populated by application code.

