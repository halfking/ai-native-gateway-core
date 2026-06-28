# Fix auto-route NOTIFY manual_disabled — Deployment Runbook

> Date: 2026-06-28
> Commits: `6b93ad72` (fix) + `c8078f3b` (test)
> Migration: `db/migrations/307_auto_route_notify_manual_disabled.sql`
> Trigger target: PostgreSQL `LISTEN auto_route_refresh` → `bg/auto_route_realtime_listener.go`

## 0. TL;DR

| Action | Required? | Risk | Downtime |
|---|---|---|---|
| Apply SQL migration `307_*.sql` | **Yes** | Low (CREATE OR REPLACE FUNCTION + DROP/CREATE TRIGGER, all idempotent) | None |
| Restart gateway to pick up `bg/auto_index_refresher.go` cold-start fallback SQL | **Yes** | Low | Brief rolling restart |
| Rollback | Available via `307_*.down.sql` | None | None |

The two changes are independent:
- The DB migration wakes the listener on `manual_disabled` UPDATEs — works even on the old gateway binary.
- The cold-start fallback SQL only takes effect after the new gateway binary restarts.

If you can only do one: **apply the DB migration first** (biggest win, lowest risk). The cold-start fix is a follow-up.

---

## 1. Pre-flight checks

### 1.1 Verify the two commits are present in the deployed image

```bash
git log --oneline -3
# expected:
# c8078f3b test(admin): pin manual_disabled wakeup payload + pg_notify
# 6b93ad72 fix(autoroute): wake manual-disabled refreshes
# e6c899f2 feat(recovery): add empty-response misclassification recovery script
```

### 1.2 Verify the trigger function exists in production

```sql
SELECT proname, prosecdef
FROM pg_proc
WHERE proname = 'notify_auto_route_refresh';
```

If `0 rows`: the trigger function was never installed. Skip to §2 (the migration is `CREATE OR REPLACE FUNCTION` and is safe to run regardless).

### 1.3 Verify the current trigger definitions

```sql
SELECT tgname,
       (SELECT pg_get_triggerdef(oid)) AS def,
       tgenabled
FROM pg_trigger
WHERE tgname IN ('trg_notify_auto_route_creds',
                 'trg_notify_auto_route_providers',
                 'trg_notify_auto_route_cmb',
                 'trg_notify_auto_route_apikeys')
ORDER BY tgname;
```

Expected pre-fix state:
- `trg_notify_auto_route_creds` watches `status, availability_state, quota_state, circuit_state, concurrency_limit, lifecycle_status` (no `manual_disabled`).
- `trg_notify_auto_route_providers` does NOT exist.

### 1.4 Snapshot `credential_model_index` size for regression baseline

```sql
SELECT COUNT(*) AS rows_in_index,
       MAX(bucket) AS latest_bucket
FROM credential_model_index;
```

Snapshot this value; we'll compare it after the gateway restart to make sure the rollup query didn't blow up.

---

## 2. Apply the SQL migration

### 2.1 Run the migration

```bash
PGPASSWORD="$LLM_GW_PG_PWD" \
  psql -h "$LLM_GW_PG_HOST" -U "$LLM_GW_PG_USER" -d "$LLM_GW_PG_DB" \
  -v ON_ERROR_STOP=1 \
  -f services/llm-gateway-go/db/migrations/307_auto_route_notify_manual_disabled.sql
```

Expected output (no NOTICE/ERROR):

```
BEGIN
CREATE FUNCTION
DROP TRIGGER
CREATE TRIGGER
DROP TRIGGER
CREATE TRIGGER
COMMIT
```

### 2.2 Post-migration verification

```sql
-- (a) credentials trigger should now watch manual_disabled
SELECT pg_get_triggerdef(oid)
FROM pg_trigger
WHERE tgname = 'trg_notify_auto_route_creds';

-- (b) providers trigger should be new and enabled
SELECT tgname, tgenabled,
       (SELECT pg_get_triggerdef(oid))
FROM pg_trigger
WHERE tgname = 'trg_notify_auto_route_providers';

-- (c) trigger function should now handle 'providers' in the dispatch
SELECT prosrc
FROM pg_proc
WHERE proname = 'notify_auto_route_refresh';
```

Expected:
- (a) UPDATE OF column list ends with `..., manual_disabled`.
- (b) `tgenabled = O` (origin/enabled).
- (c) the IF/ELSIF includes `providers` in the second branch.

### 2.3 Rollback (if needed)

```bash
PGPASSWORD="$LLM_GW_PG_PWD" \
  psql -h "$LLM_GW_PG_HOST" -U "$LLM_GW_PG_USER" -d "$LLM_GW_PG_DB" \
  -v ON_ERROR_STOP=1 \
  -f services/llm-gateway-go/db/migrations/307_auto_route_notify_manual_disabled.down.sql
```

The down migration:
- Drops `trg_notify_auto_route_providers`.
- Restores `trg_notify_auto_route_creds` to the original 6-column list (no `manual_disabled`).
- Restores the trigger function to dispatch only `credentials / api_keys / credential_model_bindings`.

After rollback, run §1.3 again — the definitions should match the pre-fix state.

---

## 3. Rolling restart of the gateway

The new gateway binary contains:
- `bg/auto_index_refresher.go` cold-start fallback (UNION ALL half-2)
- `admin/credential_monitor.go` / `admin/provider_offer_force_recover.go` direct `pg_notify` + `InvalidateAllCandidateCache` wakeup on the four `manual_disabled` handlers

```bash
# Example for k8s — adjust to your environment
kubectl -n llm-gateway rollout restart deployment/llm-gateway-go
kubectl -n llm-gateway rollout status  deployment/llm-gateway-go --timeout=5m
```

### 3.1 Confirm gateway started the listener

```bash
kubectl -n llm-gateway logs -l app=llm-gateway-go --tail=200 | grep -E "auto route realtime listener|auto index refresher started"
```

Expected (within the first 30s of startup):
```
auto route realtime listener started channel=auto_route_refresh debounce=5s
auto index refresher started interval=5m0s timeout=30s
```

### 3.2 Confirm the new SQL shape is in the rollup

The rollup runs every 5 minutes by default. To force a refresh for the smoke test:

```bash
curl -sS -X POST -H "Authorization: Bearer $ADMIN_KEY" \
  "$LLM_GW_BASE/api/admin/auto-route/refresh"
```

Expected JSON:
```json
{"refreshed": true, "refreshed_at": "2026-06-28T..."}
```

Then:

```sql
-- Half-2 cold-start fallback rows have conservative scores (50)
SELECT credential_id, raw_model, success_rate, score_smart
FROM credential_model_index
WHERE bucket = (SELECT MAX(bucket) FROM credential_model_index)
  AND score_smart = 50.0000
ORDER BY credential_id
LIMIT 20;
```

This will show every routable credential that has no recent traffic — exactly the cohort that was previously invisible to auto-route after a `manual_disabled` toggle.

### 3.3 Compare `credential_model_index` size to baseline (§1.4)

```sql
SELECT COUNT(*) AS rows_in_index,
       MAX(bucket) AS latest_bucket
FROM credential_model_index;
```

Expected: row count is **larger** than baseline (because half-2 adds baseline rows for routable credentials without recent traffic). Stable size means the rollup didn't blow up.

---

## 4. End-to-end verification

### 4.1 LISTEN/NOTIFY sanity check

Open an interactive psql session in a separate terminal:

```bash
PGPASSWORD="$LLM_GW_PG_PWD" \
  psql -h "$LLM_GW_PG_HOST" -U "$LLM_GW_PG_USER" -d "$LLM_GW_PG_DB" <<'SQL'
LISTEN auto_route_refresh;
\! sleep 30
SQL
```

While that session is sleeping (within the 30s window), trigger a `manual_disabled` toggle on any credential in the admin UI. Expected output:

```
Asynchronous notification "auto_route_refresh" with payload "credentials:UPDATE:<id>" received from server process ...
```

If you see this: the trigger + function + dispatch chain is correct end-to-end.

### 4.2 Business layer: provider 314 (gpt-5.4) acceptance

This is the original incident — credential was restored but auto-route stayed blind.

1. Pick any credential under provider 314. Confirm its current state:

   ```sql
   SELECT c.id, c.manual_disabled, c.availability_state,
          EXISTS(SELECT 1 FROM credential_model_index cmi
                 WHERE cmi.credential_id = c.id) AS in_index
   FROM credentials c
   WHERE c.provider_id = 314;
   ```

2. From the admin UI, **toggle** that credential's `manual_disabled` (set then clear, or vice versa).

3. Within 5–10 seconds (NOTIFY debounce is 5s), re-run the query above. Expected:

   ```
   in_index = t
   ```

4. From the admin UI, hit **POST /api/admin/auto-route/refresh** as a belt-and-suspenders step.

5. Send a real chat request:

   ```bash
   curl -sS "$LLM_GW_BASE/v1/chat/completions" \
     -H "Authorization: Bearer $API_KEY" \
     -H "Content-Type: application/json" \
     -d '{"model":"gpt-5.4","messages":[{"role":"user","content":"ping"}]}'
   ```

   Expected: `200 OK` with content from the gpt-5.4 upstream (or whatever proxy the credential points to).

### 4.3 Auto-route path

```bash
curl -sS "$LLM_GW_BASE/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"auto","messages":[{"role":"user","content":"hello"}]}'
```

Then check:

```sql
SELECT rl.outbound_model, rl.credential_id, rl.success
FROM request_logs rl
WHERE rl.ts >= NOW() - INTERVAL '1 minute'
  AND rl.is_auto_request = TRUE
ORDER BY rl.ts DESC
LIMIT 5;
```

If provider 314 (gpt-5.4) appears in the outbound_model column: the cold-start fix is doing its job.

---

## 5. Monitoring during the first 24 hours

### 5.1 Things to watch

| Signal | Where | What to look for |
|---|---|---|
| `credential_model_index` row count | `SELECT COUNT(*)` from §3.3 | Should stabilize within 1–2 buckets (5–10 min). Sharp drops mean the half-2 filter is wrong. |
| `auto_route_refresh` notification rate | gateway logs | Should spike on operator actions, return to ~0 within seconds. Sustained high rate means a hot loop somewhere. |
| `request_logs` insert rate | pg stat | Should NOT change. |
| `pg_listening_channels()` count | per gateway pod | Should be 1 (just `auto_route_refresh`). |
| 5xx rate on admin endpoints | gateway logs | The new `invalidateRoutingCaches` helper is best-effort; failures are swallowed. |

### 5.2 Alert thresholds (suggested)

- `credential_model_index` row count > 3× baseline for 15 min → page on-call. Means half-2 is over-emitting baseline rows.
- `auto_route_refresh` rate > 10/s sustained for 5 min → page on-call. Means some hot UPDATE path is firing too often.
- Gateway log error "auto_route listener: refresh failed" rate > 0 for 5 min → page on-call. Means the rollup query has a regression.

---

## 6. Rollback plan (full)

If something goes wrong that the down migration can't fix:

1. **Roll back the gateway binary** to the previous release. The cold-start fallback SQL disappears; half-1 only. Credential visibility returns to pre-fix behaviour (still better than today — the trigger migration is independent and stays in place).

2. **Optionally roll back the DB migration** with §2.3. This removes the `manual_disabled` wakeup at the trigger level. Combined with #1, you get full pre-fix state.

3. **Notify stakeholders** with the incident timeline. Include:
   - When the new binary rolled out.
   - When the DB migration was applied.
   - The symptom (e.g. "credential_model_index over-emitting", "auto-route stuck on stale credential", etc.).
   - The rollback timestamps.

---

## 7. Reference

- Migration files:
  - `db/migrations/307_auto_route_notify_manual_disabled.sql`
  - `db/migrations/307_auto_route_notify_manual_disabled.down.sql`
- Code commits:
  - `6b93ad72` fix(autoroute): wake manual-disabled refreshes
  - `c8078f3b` test(admin): pin manual_disabled wakeup payload + pg_notify
- Original incident: provider 314 (gpt-5.4) — admin corrected `manual_disabled`, auto-route did not pick up the restored credential within the 5-minute refresh window.
- Related code paths:
  - `bg/auto_route_realtime_listener.go` (the LISTEN loop)
  - `bg/auto_index_refresher.go` (the rollup)
  - `admin/credential_monitor.go` (`handleSet/ClearManualDisabled`)
  - `admin/provider_offer_force_recover.go` (`setProvider/CredentialManualDisabled`)