# 2026-07-22 — auto-recovery chain fix (6-bug root-cause analysis)

**Branch:** `fix/auto-recovery-auth-failed-6bugs`
**PRs (chronological):**

1. `9c2a8d35f` fix(credential): auto-recover auth_failed credentials via 60s ticker (BUG #2 + #3)
2. `d749098b4` fix(circuit): KindAuth uses exponential cooling instead of permanent quarantine (BUG #1)
3. `47539e23e` fix(selfcheck): wire up credential_most_used_model + per-cred LATERAL filter (BUG #4 + #5)
4. `56c19b679` fix(probe): re-probe healthy credentials every 1h instead of every 24h (BUG #6)

**Severity:** 🔴 P0 (production outage)
**Affected:** llm-gateway-go credential auto-recovery chain (9 workers)

## Summary

On 2026-07-22 a user reported "claude-sonnet-5 returns 403 / no candidate" after the opencode tool auto-rotated apikey. Root cause analysis revealed 6 distinct bugs across the auto-recovery chain — combined effect: auth_failed credentials were stuck for 15+ hours with **no automatic recovery** because every layer of the recovery path was broken:

- DB state writer wrote `recover_at=NULL` (couldn't be queried by recovery ticker)
- Recovery ticker's whitelist omitted `auth_failed` (would've ignored the row anyway)
- In-memory circuit breaker used `RecoveryPermanent` (no auto-recovery possible)
- node_probe backoff chain capped at 24h after success (creds sat un-probed for a full day after one good probe)
- credential_selfcheck worker called a PG function that didn't exist (silent error for 30+ hours)
- pickDueCredential's LATERAL shared tenant-wide `last_at` (always picked the lowest-ID credential)

## Bug-by-bug fix

### BUG #1 — breaker.go:103 KindAuth RecoveryPermanent

`breaker.go:103` declared KindAuth as `RecoveryPermanent` with `InitialCooling=0, MaxCooling=0`. Two consecutive 401/403 failures pinned the credential to `StateQuarantined` indefinitely with no auto-recovery.

**Fix:** switched to `RecoveryExponential{InitialCooling: 15min, MaxCooling: 24h, ShrinkFactor: 0.5}`. The 15-min InitialCooling matches the new `availability_recover_at = now()+15min` written by BUG #2 fix. 24h MaxCooling preserves the old "effectively permanent" UX for genuinely-broken apikeys.

**Tests updated:** `TestConfirmedAuthFailureOpens` (renamed from Quarantines), `TestManagerStats`, `TestReset`, `TestBreaker_QuarantinedStateExists` — all exercise `StateQuarantined` via `KindQuota` instead of `KindAuth`.

### BUG #2 — writer.go:218 KindAuth writes recover_at=NULL

`writer.go:218` wrote `availability_recover_at = NULL` for KindAuth failures. The 60s `credential_recovery.go` ticker requires `availability_recover_at IS NOT NULL AND <= now()` — making the row impossible to ever match the WHERE clause.

**Fix:** now writes `availability_recover_at = now() + 15min`. Pinned by `TestWriteOnError_KindAuth_SetsRecoverAt` (4-arg SQL contract test) + updated `TestWriteOnError_CredentialWideKind/auth` argCount from 3 → 4.

### BUG #3 — credential_recovery.go:56 whitelist missing 'auth_failed'

`bg/credential_recovery.go:56` UPDATE only handled `availability_state IN ('cooling','rate_limited','unreachable')`. Even if BUG #2 wrote a future timestamp, the WHERE clause would skip auth_failed rows.

**Fix:** added `'auth_failed'` to the whitelist. Pinned by `TestAuthFailedRecoverySQLGuard` (source-grep test that survives SQL body refactors).

### BUG #4 — credential_selfcheck.go:218 LATERAL tenant-wide max

`bg/credential_selfcheck.go:218` LATERAL subquery had only `scr.tenant_id = c.tenant_id` — the tenant-wide max was the same timestamp for every credential. Combined with `ORDER BY l.last_at NULLS FIRST, c.id LIMIT 1`, the worker always picked `credentials.id = 2` (the lowest active ID) and cycled there indefinitely.

**Fix:** added `scr.model_name = 'cred-' || c.id::text` filter so each credential sees its OWN last selfcheck time. Pinned by `TestPickDueCredential_FiltersByModelName` source-grep test.

### BUG #5 — credential_most_used_model PG function missing

`bg/credential_selfcheck.go:421` called `SELECT raw_model_name FROM credential_most_used_model($1, 24)` but the function did not exist in PG (SQLSTATE 42883). The error was swallowed by `err.Error() != "no rows in result set"` filter, so the worker appeared healthy but produced no `self_check_runs` rows.

**Fix:** added `deploy/sql/migrations/V351__credential_most_used_model.sql` creating the function. Reads from `request_logs_hot` first (fast hot path), falls back to `request_logs` (cold partition). Returns top-1 `raw_model_name` by successful-request count in last N hours. Pinned by `TestSelfcheckWorker_DependsOnCredentialMostUsedModel` (source-grep + content assertion on V351).

### BUG #6 — node_probe.go:1045 24h success cooldown

`bg/node_probe.go:1045` (and lines 493, 498 in `MarkNodeProbeHealthy`) wrote `next_retry_at = now() + interval '24 hours'` after every successful probe. After one good probe, the credential sat un-probed for a full day — long enough for the upstream to silently change (key rotation, quota change, model deprecation) without the gateway noticing.

**Fix:** reduced to `next_retry_at = now() + interval '1 hour'`. Note: the FAILURE backoff chain `5s → 30s → 60s → 5m → 1h → 2h → 24h` is unchanged — failures still back off up to 24h. Only the SUCCESS post-retry interval is reduced. Pinned by `TestNodeProbeSuccessNextRetryOneHour` (source-grep + literal-presence test).

## Migration

`deploy/sql/migrations/V351__credential_most_used_model.sql` creates `credential_most_used_model(INT, INT)` returning TEXT. Idempotent (`CREATE OR REPLACE`). Applied to 252 PG on 2026-07-22 17:29 by fix/auto-recovery-auth-failed-6bugs deploy.

Note: `deploy/sql/migrations/V350__routing_attempts_tracking.sql` was also applied during the same window — it pre-existed in the schema (ADD COLUMN IF NOT EXISTS) but had never been recorded in `schema_migrations`. The PR didn't touch V350.

## Verification (245 pre-prod, seq=1302)

- L1 `/healthz` → 200, version `2.4.7-56c19b67-20260722-1302-56c19b67`
- L2 DB ready, 401 background-tasks
- L3 smoke echo + log + DB all consistent
- L4 **business verification (the real test):**
  - 17:33:23 — manually set `cred=17.availability_recover_at = now()-1min`
  - 17:34:36 — `credential_recovery` worker logged `"credential availability recovered","count":1`
  - 17:33 cred=17 `availability_state: auth_failed → ready`
  - After flushing the other 5 stuck auth_failed creds (12, 16, 17, 29, 30) with `availability_recover_at = now()-1min`, all 6 are now `ready`

The recovery chain works end-to-end: KindAuth failure → writer.go writes recover_at+15min → credential_recovery.go 60s ticker sees it → flips back to ready → active_probe can re-verify the credential.

## What still needs attention (154 production deployment)

Per the user's authorization split (245 mine, 154 human_only per rule 10 §2.3), 154 still runs the pre-fix binary. **Before 154 deployment, ensure:**

1. V351 PG function has been applied to 252 PG (✅ done 2026-07-22 17:29)
2. V351 is recorded in `schema_migrations` (✅ done)
3. The 5 stuck auth_failed credentials on 154 (production DB) need the same one-shot `UPDATE … SET availability_recover_at = now()-1min WHERE availability_state = 'auth_failed' AND availability_recover_at IS NULL` flush — the same SQL I ran on 245 PG, applied to 154 PG via the 252 PG (they share the same DB).

**154 deployment command (you run):**
```bash
bash scripts/deploy-154.sh
```

The deploy-seamless.sh wrapper handles:
- upload release bundle (sha256-verified)
- atomic symlink switch + restart (~11s downtime)
- healthz + DB verification (60s budget)
- auto-rollback on any verification failure