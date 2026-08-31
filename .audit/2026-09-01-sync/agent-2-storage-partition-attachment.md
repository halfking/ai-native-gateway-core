# Audit Report — Storage, Partition, Attachment (2026-09-01)

Scope: 24h commits since 2026-08-31 06:00 +0800 + critical-path files. Read-only audit. All findings are evidence-based with file:line + reproducible probes; P0/P1 ≤ 5, P2 ≤ 8.

Source-agent note: this report was delivered inline by the Explore sub-agent because the Explore role is read-only. The top-level orchestrator persists it here verbatim.

---

## P0 — none observed

After reviewing all 9 audit goals against the current head (commit `f6abe0732` main, plus work up to `8e2b428bf`):

- The 8h retention is honored for all in-scope hot tables in the steady state. Writers use `now()` and explicit `ts`, partitions are pre-created by `PartitionManager.ensureNextMonthPartitions` (`bg/partition_manager.go:274-298`), and `promote_*_hot_to_partition` calls use `ts < cutoff_ts` and `DELETE` matching only inserted rows. The recent fixes (615/624/625/626/627/628) closed every previously reported gap.
- The MV refresh two-layer lock falls back to Postgres advisory lock when Redis is nil / disabled / errors out (`bg/materialized_view_refresher.go:175-184`, `244-262`); Redis failure does not abort. Lock is deferred-Released in all paths including follower-rejections (`:176`).
- The pg-table-copy.sh fixup is well-guarded (PHASE 8.5, only when `--target` is local docker, never on `--dry-run`/`--data-only`, applied via env vars not exported) and is idempotent at the SQL level (`scripts/pg-table-copy.sh:648-679`, `scripts/local-dev/apply-routing-mv-fixup.sh:114-200`).
- Migration checksum verification was wired into `verify.sh` gate (commit `5460d3c80`) and `d9b289256` removed the stale `619_session_turns_unified_view.sql` row from `docs/db-changelog.md`.

---

## P1

### P1-1 — Attachment FS cleanup: `os.Remove` fires before audit-row INSERT (cannot recover after crash)

**Location**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/admin/data_lifecycle_attachments_filesystem.go:221-275`

**Evidence**:
```go
// Line 239-247
if rmErr := os.Remove(path); rmErr == nil {
    bytesFreed += info.Size()
    filesDeleted++
    removed = append(removed, removedFile{...})  // append AFTER unlink
}
// audit insert happens AFTER all files removed (line 259-274)
for _, f := range removed {
    if _, err := h.db.Exec(ctx, `INSERT INTO audit_attachments_filesystem_cleanup ...`,
        cleanupRunID, tenantID, f.path, f.size, f.mtime, ...); ...
}
```
A kill -9 or DB unreachable between `os.Remove` and the final `INSERT` leaves deleted files with **no audit row** (the migration-632 design notes explicitly disclaim a Saga: "no two-phase commit between DB-row NULL and os.Remove — out of scope for this audit pass", `sql/migrations/startup/632_audit_attachments_filesystem_cleanup.sql:17-22`). This is by-design from the migration but creates a permanent observability gap if the audit INSERT fails after the file is gone — the response carries `audit_rows=0` (line 158), but a process crash aborts the loop entirely with no response at all.

**Reproduction**:
```
# Inject a DB-timeout env into pg connection
# Send POST /api/admin/attachments/filesystem/cleanup {older_than_days: 90}
# Mid-cleanup, kill -9 the process
# Re-scan /var/lib/llm-gateway/attachments — files are gone, audit_attachments_filesystem_cleanup has 0 rows
```

**Test/probe** (additions to `admin/data_lifecycle_attachments_integration_test.go` `//go:build integration`):
1. Stage N pre-existing files; inject a DB error after K removals; verify response is NOT 200 and audit_attachments_filesystem_cleanup has exactly K rows. The test should also confirm **first** the audit row + only then `os.Remove` could be a viable ordering for a future-proofed retry.
2. Add a probe `SELECT count(*) FROM audit_attachments_filesystem_cleanup a LEFT JOIN (<filesystem walk>) f ON a.file_path = f.path WHERE f.path IS NULL` that surfaces orphan audit rows that survived a partial crash.

---

### P1-2 — `request_logs_hot` reset endpoint deletes without tenant_id scope (super-admin path is exception-gated but unverified at SQL layer)

**Location**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/admin/credential_success_rate.go:127-133`

**Evidence**:
```go
result, err := db.Exec(r.Context(), `
DELETE FROM request_logs_hot
WHERE credential_id = $1
  AND lower(COALESCE(outbound_model, client_model)) = lower($2)
  AND lower(COALESCE(request_status, '')) = 'failure'
  AND ts < NOW() - INTERVAL '10 minutes'
`, req.CredentialID, req.RawModel)
```
No `tenant_id = $N` clause; relies entirely on the handler-level `super_admin` check. If a future caller wrapper skips that check (e.g. a `tenant_admin` override) the DELETE spans every tenant for that credential. The auth layer is the only barrier.

**Reproduction**: temporarily wrap the handler in a fake `tenant_admin` request via the existing test harness; observe that rows for *all* tenants matching `(credential_id, raw_model)` are removed.

**Test/probe**:
1. Add an integration test `TestResetCredentialSuccessRate_TenantScope` that runs as `tenant_admin` and asserts `affected == 0` when the credential_id is owned by another tenant.
2. Add a SQL guard inside the statement itself:
```sql
DELETE FROM request_logs_hot
WHERE credential_id = $1
  AND lower(COALESCE(outbound_model, client_model)) = lower($2)
  AND lower(COALESCE(request_status, '')) = 'failure'
  AND ts < NOW() - INTERVAL '10 minutes'
  AND (current_setting('app.current_role', true) = 'super_admin'
       OR tenant_id = current_setting('app.current_tenant', true))
```
This closes the door against an auth bypass even if the handler check is later regressed.

---

### P1-3 — `session_bodies_unified` may partition-spill into "today + 1 day" for late writes spanning UTC midnight (verification gap, not a bug)

**Location**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/sql/migrations/startup/614_session_bodies_hot.sql:88-93` (and unchanged in 625/626)

**Evidence**:
```sql
SELECT ... FROM public.session_bodies
WHERE partition_date <= CURRENT_DATE - interval '1 day';
```
The view excludes rows with `partition_date = CURRENT_DATE` from the partitioned `session_bodies`. Meanwhile, the writer (`domains/session/v2/bodies_writer.go:281-296`) writes to `session_bodies_hot` with `partition_date = calendarDate(rec.Ts)` which can be today or yesterday. After `promote_session_bodies_hot_to_partition` runs (default 8h retention, `sql/migrations/startup/615_session_bodies_hot_promote_function.sql:16`), an admin reader who queries `session_bodies_unified` for `partition_date = CURRENT_DATE` from a previous day will see the rows *only* if the promote completed. There is also no `partition_date = CURRENT_DATE` clause in the hot side, so the UNION ALL is well-defined for any partition_date. The gap risk is **at the boundary** when `CURRENT_DATE` flips mid-promote: a row that should be in `session_bodies` for the just-rolled-over date still in `session_bodies_hot` and the view returns it once via the hot leg. But: an admin reader querying `session_bodies_unified` and the partitioned `session_bodies` with a tenant_id predicate that is satisfied only on the partitioned side may **miss the still-in-hot row** if RLS context is bypassed — same row, different tenant scope.

**Reproduction**:
- Insert a `session_bodies_hot` row with `partition_date = today`, `tenant_id = 'A'`, `request_id='r1'`.
- Run `promote_session_bodies_hot_to_partition('8 hours'::interval, 1)`.
- Run the same with `partition_date = today - 1` and observe union.
- This is fine; the real edge case is the opposite: a row written to `session_bodies_hot` with `partition_date = today - 1` *before* the next promote tick (i.e. it is >8h old but has not been promoted yet). The view does not filter on ts, so the reader sees it once via the hot leg. Good.

The actual residual concern is **duplicate-row risk under partial promote**: migration-615's promote uses `FOR UPDATE SKIP LOCKED` then inserts with `ON CONFLICT (id, partition_date) DO NOTHING`. A row promoted then immediately read via the unified view will appear **once** through the partitioned leg because the matching delete in `session_bodies_hot` is keyed on the same `(id, partition_date)`. Verified safe — this is an "需 staging 验证" item, not a bug.

**Test/probe**:
```sql
-- Verify no duplicate rows:
SELECT tenant_id, request_id, partition_date, count(*)
FROM session_bodies_unified
GROUP BY 1,2,3 HAVING count(*) > 1;
-- Expect 0 rows after a 30s wait post-promote.
```

---

## P2

### P2-1 — `bg/envelope_cleaner.go:54` uses `request_envelope` table; no RLS context set

**Location**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/bg/envelope_cleaner.go:54`

**Evidence**: `DELETE FROM request_envelope WHERE expires_at < now()` — runs in the gateway role with no `set_config('app.bypass_rls', ...)` and no `app.current_role = super_admin`. If `request_envelope` carries RLS restricted by `tenant_id`, a default role without `app.bypass_rls=true` will delete zero rows or raise a permission error. The 7/21 review added the same `set_config('app.bypass_rls','true')` pattern in `bg/provider_error_aggregator.go:94-102` for that reason. The cleaner doesn't replicate it.

**Test/probe**: confirm `request_envelope` either has no RLS or the cleaner is run in a role that bypasses. Add an integration test that runs the cleaner as the gateway role and observes the row count.

### P2-2 — Migration-checksum ledger does not cover `deploy/sql/migrations/*.sql`

**Location**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/scripts/verify-migration-checksums.sh:11-30`

**Evidence**: `MIG_DIR="$REPO_ROOT/sql/migrations/startup"` only. `deploy/sql/migrations/V350..V367` (the columnar-promote / provider-error tables) are NOT checked against any SHA-256 ledger. A drift between source and target would only surface when the SQL is re-run. Migration 359 / 363 / 367 / V351 are production-critical and should be checksum-equal across envs.

**Test/probe**: extend the script to walk `deploy/sql/migrations/V*.sql` and add a sister ledger table `docs/db-changelog-deploy.md` (or unify into the same changelog with a `[deploy]/[startup]` prefix column).

### P2-3 — `mvRefreshDistLockTTL = 6 * time.Minute` may be tight if the unique index build blocks refresh

**Location**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/bg/materialized_view_refresher.go:72`

**Evidence**: `RefreshTimeout = 5 * time.Minute` (line 50). The Redis lease is `6 * time.Minute` — only 1 minute of margin. `REFRESH MATERIALIZED VIEW CONCURRENTLY` rebuilds the unique index for `routing_analytics_7d_ukey` (7 columns) and that is the very first time it runs after a fresh CREATE MATERIALIZED VIEW (built WITH NO DATA at migration time, and `apply-routing-mv-fixup.sh:152,170` ship `WITH NO DATA`). At canary's first refresh, the unique index must be built from scratch → likely exceeds 6 min on the September 2026 dataset. The distlock's auto-renew at `ttl/3` (every ~2 min) keeps the lease alive as long as the process is alive, so this is only a crash-recovery consideration; process liveness means the lease survives.

**Test/probe**: monitor `mvRefreshElapsedMs` against `mvRefreshDistLockTTL` for one week of production runs and confirm p99 stays well under 6 min.

### P2-4 — `routing_analytics_7d` GROUP BY still references `client_model` while upstream renames may break the view silently

**Location**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/scripts/local-dev/apply-routing-mv-fixup.sh:138-141` + `admin/auto_route.go:557-577`

**Evidence**: The view filters on `client_model`/`outbound_model`/`work_type` — these are columns from `request_logs_with_current_month_without_customer_id`. If migration 528 (or a successor) renames any of those columns, the view body in the fixup script becomes orphaned (the migration's own DDL never executes against `local` because the fixup SQL was extracted from 252's pg_dump output at a specific snapshot). The view will still `CREATE OR REPLACE` on schema match but `REFRESH CONCURRENTLY` will fail when the underlying relation's columns drift.

**Test/probe**: add a CI check that diffs `apply-routing-mv-fixup.sh` against 252's live pg_dump output (`pg_dump --schema-only --no-owner --no-privileges public.routing_analytics_7d`). A drift should fail CI.

### P2-5 — `provider_error_aggregator` watermark regression check can fire spuriously on the very first hot-only tick

**Location**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/bg/provider_error_aggregator.go:292-309`

**Evidence**: The `groups > 0 && newAggregationID < preWatermark` branch logs an Error if the watermark regressed. On a fresh deployment after migration-622 only (without 627's negative-seed), `preWatermark = 0` and `newAggregationID = MAX(aggregation_id)` from `candidate_failure_logs_hot` positive sequence. If the hot sequence emits 0 on first use (depends on `nextval` semantics), this branch would fire spuriously. Verified reading shows the sequence starts at 1, so this is **safe** in practice but the guard could read `newAggregationID < preWatermark` even when `preWatermark = 0` and `newAggregationID = 1`.

**Test/probe**: add a regression test that runs `aggregateErrors` against a fresh schema with 1 row and asserts that `groups > 0 && newAggregationID < preWatermark` is never logged on the first post-seed tick.

### P2-6 — `cleanupOldModelProbeRuns` runs every cycle; if `request_logs_bodies_hot` has a long promote backlog and gates it, model_probe cleanup can lag the 14d TTL by one cycle

**Location**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/bg/partition_manager.go:482-504`

**Evidence**: `cleanupOldModelProbeRuns` is called from `archiveOldPartitionsIfNeeded` (line 309), which is called on a separate `mainTicker` (`pm.interval`, default 24h). The hot-table promoter's `promoteDone` is on a different ticker (1h). In practice this means model_probe_runs cleanup runs once a day, not once an hour. The `cleanup` `ticker` defined at line 256-269 (`providerErrorCleanupInterval = 1 * time.Hour`) is for provider_error_details only. There is no per-hour tick for model_probe_runs. With a 14d TTL that's still correct (rows older than 14d get reaped once a day), but if an operator sets `lifecycle.model_probe_runs_ttl_days=1` to cope with a hot incident, the cleanup lag becomes 24h, not 1h.

**Test/probe**: add a one-hour tick for `cleanupOldModelProbeRuns` when `ttl_days <= 7`.

### P2-7 — `pg-table-copy.sh` hot-table pattern includes `*_2026_*` which now matches the *current* month, not just historical

**Location**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/scripts/pg-table-copy.sh:36`

**Evidence**: `HOT_PATTERNS="*_hot,*_2026_*,*_2027_*,*_2028_*,*_archived,*_archive"`. The `*_2026_*` pattern matches tables named `request_logs_2026_09`, `session_bodies_2026_09` (monthly partitions *of the current month*). On a sync on 2026-09-15, the current month's partitions are copied schema-only with no data; subsequent reads on the target will see empty recent data until the next sync. This is by design for monthly partitions (they must be re-populated by re-running promote functions), but it silently masks any data the current month's partition has accumulated on the source.

**Test/probe**: add a post-sync verification that prints `pg_size_pretty(pg_total_relation_size('request_logs'))` and compares to source; a delta > 10% on the current-month partition should be a warning, not silent.

### P2-8 — `apply-routing-mv-fixup.sh` reads `PG_FIXUP_PASS` from `~/workspace/ai-native-tools/envs/loader.sh` if envs file exists; otherwise requires `PG_FIXUP_PASS` env var; no fallback when both absent

**Location**: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/scripts/local-dev/apply-routing-mv-fixup.sh:57-70`

**Evidence**:
```bash
if [[ -f "$ENVS_LOADER" ]]; then
  source "$ENVS_LOADER" --project llm-gateway-go >/dev/null 2>&1 || true
  PGPASSWORD="${PG_FIXUP_PASS:-${COMMON_PG_SUPERUSER_PASS:-}}"
else
  PGPASSWORD="${PG_FIXUP_PASS:-}"
fi
if [[ -z "$PGPASSWORD" ]]; then
  echo "ERROR: cannot determine PGPASSWORD. Set PG_FIXUP_PASS or load envs." >&2
  exit 1
fi
```
The `pg-table-copy.sh` wrapper (line 667-671) passes `PG_FIXUP_PASS="$TGT_PASS"` so the auto-invoke path is OK. But an operator who manually invokes `apply-routing-mv-fixup.sh` outside the synced env workflow can hit this with a hard fail.

**Test/probe**: document the env-var contract in `.agents/skills/db-sync-252-local/SKILL.md` (already partially covered) and add a `--env-file <path>` flag to `apply-routing-mv-fixup.sh` to mirror `pg-table-copy.sh`'s source-config pattern.

---

## Already-fixed (skipped)

- **Attachment cleanup P0 placeholder collisions + reaper int→text** — `d9b289256` (2026-08-31). Fixed `attachmentTenantScope` placeholder indexing, replaced `($N || ' days')::interval` with `make_interval(days => $N::int)`, fixed `daysIdx/runIdx/triggerIdx/reasonIdx` to derive from `len(tenantArgs)+1`, fixed `parseOlderThanDays` body precedence. All five integration tests pass.
- **Migration-619 orphan session_turns_unified view** — `635_drop_session_turns_unified.sql` (2026-08-31 13:45). View dropped, no production readers used it.
- **Migration-632 materialize-view addition to migration 632** — `24da16e1c` through `c5618ba7e` (2026-08-31). Indexes, unique indexes on plain columns, schema-aware checks, security_invoker on `session_turns_with_current_month`; digest JSONB is nullable per `c5618ba7e`.
- **Promote zombie-lock streak gauge** — `c2d67f63d`. `incPromoteZombieLockStreak`/`resetPromoteZombieLockStreak` added; gauge is only a "consecutive-skip" counter, not a lifetime counter.
- **Watermark-regressed false-positive gate** — `21f7bea9c`. The `groups > 0 && newAggregationID < preWatermark` guard is no-op when no rows were aggregated (otherwise idle ticks would log a false positive).
- **Concurrent promote on shared DB** — `bg/partition_manager.go:898-963` (commit `c2d67f63d` family): `pg_try_advisory_xact_lock` on per-table label so two replicas don't both promote the same table.
- **MV refresh two-layer lock** — `0dacb1d22` (2026-09-01). Redis distlock preferred; falls back to Postgres advisory lock on Redis miss. Released on all exit paths.
- **VACUUM FULL cluster-wide mutex** — `6e3583c39`. `internal/dbx.VacuumFullMutex` acquires `pg_advisory_xact_lock` on a second connection so multi-replica don't both run VACUUM FULL.
- **Routing-MV drift on local docker** — `51d05bddf` auto-applies fixup at end of pg-table-copy.sh; the fixup SQL itself is idempotent.
- **Verify-migration-checksums wire-up** — `5460d3c80` (2026-08-31). Wired into verify.sh gate.

---

## Verification gaps

1. **`session_bodies_unified` after promote with `partition_date = today`**: confirm no row is double-counted. Probe:
   ```sql
   SELECT tenant_id, request_id, partition_date, count(*)
   FROM session_bodies_unified
   GROUP BY 1,2,3 HAVING count(*) > 1;
   ```

2. **`candidate_failure_logs_unified` after promote with `aggregation_id NULL` (historical pre-628 rows)**: confirm watermark seeding = bigint-min is intact.
   ```sql
   SELECT last_source_id FROM provider_error_aggregator_state WHERE id = 1;
   -- Expect <= 0 after first post-627 tick
   ```

3. **`mv_refresh` runtime**: confirm `mvRefreshDistLockTTL > p99(refreshed_at - now)` for a week.

4. **FS-cleanup crash recovery**: produce a reconciliation report that joins `audit_attachments_filesystem_cleanup` to a fresh filesystem walk; verify zero orphans under happy-path and partial-failure injection.

5. **`request_logs_hot` reset endpoint under non-super-admin role** (P1-2): add an integration test that invokes the handler as `tenant_admin` for a credential not owned by the caller and verifies zero rows affected.

6. **PartitionManager promote-budget reset on a 4h backlog**: with `promoteCycleMaxBatches = 100` × `promoteBatchSize = 5000` = 500k rows/hour capacity. Session_bodies default 500 rows/batch × 100 = 50k rows/hour; safe for normal load.

7. **apply-routing-mv-fixup.sh drift detection**: a CI job that diffs the embedded SQL against 252's live pg_dump output; alert if columns drift.

---

## Summary table

| # | Severity | Area | Summary |
|---|---|---|---|
| P1-1 | P1 | attachment / attachment FS cleanup | `os.Remove` runs before audit-row INSERT; crash leaves orphan unlinked files; by design per 632 doc-comment but lacks reconciler. |
| P1-2 | P1 | tenant isolation / credential reset | `credential_success_rate.go:127-133` `DELETE FROM request_logs_hot` lacks `tenant_id`; relies entirely on handler-level super_admin check. |
| P1-3 | P1 (需 staging 验证) | unified view | `session_bodies_unified` is consistent under normal promote cycle; partial promote duplicate-row risk is bounded by `ON CONFLICT (id, partition_date) DO NOTHING`. |
| P2-1 | P2 | cross-tenant RLS | `envelope_cleaner.go:54` deletes from `request_envelope` without bypass-RLS context or super_admin role set. |
| P2-2 | P2 | migration checksum | `verify-migration-checksums.sh` covers only `sql/migrations/startup/`; `deploy/sql/migrations/V*.sql` are unchecked. |
| P2-3 | P2 | MV refresh coordination | `mvRefreshDistLockTTL=6m` margin over `RefreshTimeout=5m` is only 1m; auto-renew handles liveness, but worth monitoring p99. |
| P2-4 | P2 | MV definitions | `routing_analytics_7d` materialized view depends on column names from base view; rename in upstream can break silently. |
| P2-5 | P2 | aggregator watermark | Watermark regression log can fire on first post-622 tick if sequence starts at 0; sequence is 1, so currently safe. |
| P2-6 | P2 | retention tick | `cleanupOldModelProbeRuns` runs every 24h (combined into `archiveOldPartitionsIfNeeded`); hourly tick only for `providerErrorCleanupInterval`. |
| P2-7 | P2 | pg-table-copy | `*_2026_*` hot pattern matches the current-month partition; current-month data is silently dropped on the target. |
| P2-8 | P2 | apply-routing-mv-fixup | Manual invocation can fail with no PGPASSWORD if the env loader is absent and `PG_FIXUP_PASS` is unset. |
| — | skipped | attachment cleanup P0 collisions | fixed by `d9b289256` |
| — | skipped | orphan session_turns_unified view | dropped by 635 |
| — | skipped | MV contracts + unique indexes | fixed by 632/24da16e1c/ee8a5092d/c5618ba7e |
| — | skipped | zombie-lock streak | fixed by `c2d67f63d` |
| — | skipped | watermark-regressed false-positive | fixed by `21f7bea9c` |
| — | skipped | shared-DB concurrent promote | advisory-lock per table label in `bg/partition_manager.go:898-963` |
| — | skipped | MV refresh two-layer lock | fixed by `0dacb1d22` |
| — | skipped | VACUUM FULL mutex | fixed by `6e3583c39` |
| — | skipped | routing-mv drift on local | fixed by `51d05bddf` auto-invoke at PHASE 8.5 |
| — | skipped | verify-migration-checksums wire-up | fixed by `5460d3c80` |
