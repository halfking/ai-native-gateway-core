# DEPLOY — request_logs unique_id fix (P0 hotfix)

> **Target database**: `llm_gateway` on Postgres 15.3 (production)
> **Date**: 2026-06-26
> **Severity**: P0 (data correctness — duplicate rows under retry storms)
> **Commits**: `d16131ad` (fix) + `1d7ddd79` (deployment notes)

---

## TL;DR

```bash
# 1. Apply the schema migration (one-time per cluster)
LLM_GATEWAY_DATABASE_URL="postgres://user:pass@host:5432/llm_gateway?sslmode=disable" \
  ./scripts/deploy_request_logs_unique_id.sh

# 2. Rebuild + redeploy the gateway (so ensureRequestLogsUniqueIndex runs at startup)
#    e.g. for the 71 server:
cd /opt/official-deploy/services/llm-gateway-go
docker build --no-cache -t kx-llm-gateway-go:fix-request-logs .
docker stop llm-gateway-go && docker rm llm-gateway-go
docker run -d --name llm-gateway-go --network host \
  --env-file /etc/llm-gateway-go/env \
  -v /opt/llm-gateway-go/data:/opt/llm-gateway-go/data \
  --restart unless-stopped kx-llm-gateway-go:fix-request-logs

# 3. Verify (after gateway is up)
LLM_GATEWAY_DATABASE_URL="..." bash scripts/deploy_request_logs_unique_id.sh  # step 4 fires the verify SQL
docker logs llm-gateway-go 2>&1 | grep "request_logs unique constraint enforced"

# 4. Run live tests (optional but recommended)
LLM_GATEWAY_PG_TEST_URL="postgres://..." \
  go test ./telemetry/ -run TestRequestLogUnique -v
```

---

## What was fixed

**Symptom (kaixuan, 2026-06-26):**
> 请求 glm-5.1 → 智谱重试3次 → fallback 到 nvidia nim → 4 条记录全部更新成 nvidia nim success.

**Root cause:**
The `request_logs` table had `UNIQUE (request_id, ts)`, but
`insertRequestLog` uses `ts=now()` (which differs on every call). So a retry
storm produced multiple rows for the same logical request, and the subsequent
`UPDATE ... WHERE request_id = $1` matched all of them, mass-overwriting
with the final state.

**Fix:** `UNIQUE (request_id)` only. One logical request = exactly one row,
regardless of retry / fallback / async-retry activity.

---

## Files changed (commits `d16131ad` + `1d7ddd79`)

### Schema & runtime
| File | Change |
|---|---|
| `db/migrations/301_request_logs_unique_request_id_only.sql` | Migration: drop `(request_id, ts)` index, dedupe, create `(request_id)` |
| `db/migrations/301_request_logs_unique_request_id_only.down.sql` | Rollback (restore `(request_id, ts)`) |
| `db/migrations/020_request_logs_unique_request_id.sql` | Header comment updated: superseded by 301 |
| `db/db.go` | Added `ensureRequestLogsUniqueIndex()` and called from `Open()` |
| `deploy/sql/llm_gateway_schema_2026-06-26.sql` | Snapshot updated: 5 index DDL changes (parent + 4 partitions) |

### Application code
| File | Change |
|---|---|
| `telemetry/client.go` | `insertRequestLog`: added `ON CONFLICT (request_id) DO NOTHING`. `upsertRequestLogFallback`: `ON CONFLICT (request_id, ts)` → `ON CONFLICT (request_id)` |

### Tests
| File | Change |
|---|---|
| `telemetry/client_live_test.go` | Added `TestRequestLogUniqueRequestID` + `TestRequestLogFallbackUpsert` |
| `telemetry/client_test.go` | Test note explaining why contract test lives in `client_live_test.go` |

### Scripts
| File | Change |
|---|---|
| `scripts/deploy_request_logs_unique_id.sh` | New — guided deploy with precheck, migration, verify steps |
| `scripts/rollback_request_logs_unique_id.sh` | New — companion rollback script |
| `db/scripts/verify_request_logs_unique.sql` | New — exit-0 verification query |
| `db/scripts/pre_migration_check.sql` | Pre-flight: index existence + duplicate counts |

### Documentation
| File | Change |
|---|---|
| `CHANGELOG_request_logs_unique_id.md` | New — full root-cause writeup |
| `HOTFIX_REQUEST_LOGS.md` | Pointer to new CHANGELOG (historical content preserved) |
| `version.json`, `web/public/version.json` | Bumped |

---

## Deployment runbook

### Step 0 — pre-flight

```bash
# Verify connectivity
LLM_GATEWAY_DATABASE_URL="postgres://..." psql -c "SELECT 1"

# Run the precheck to estimate impact
LLM_GATEWAY_DATABASE_URL="..." psql -f db/scripts/pre_migration_check.sql
```

Look at:
- **Duplicate rows in last 7 days**: this is what migration 301 will DELETE.
- **Table size**: ensures the migration's CREATE INDEX won't take excessive lock time.

### Step 1 — apply schema migration

```bash
LLM_GATEWAY_DATABASE_URL="..." bash scripts/deploy_request_logs_unique_id.sh
```

The script will:
1. Run the precheck (read-only).
2. Prompt for confirmation.
3. Apply migration 301 (DROP old index, DELETE duplicates, CREATE new index).
4. Run `verify_request_logs_unique.sql` (asserts: new index exists, old
   index gone, no duplicates in last 7 days).

`DRY_RUN=1` previews everything without writing.

### Step 2 — restart the gateway

The runtime fix (`ensureRequestLogsUniqueIndex`) auto-applies on any fresh
gateway boot, but the existing prod cluster needs a rebuild + restart to
pick up the code change. The 71 server procedure:

```bash
cd /opt/official-deploy/services/llm-gateway-go
git pull  # includes commits d16131ad + 1d7ddd79
docker build --no-cache -t kx-llm-gateway-go:fix-request-logs .
docker stop llm-gateway-go
docker rm llm-gateway-go
docker run -d --name llm-gateway-go \
  --network host --env-file /etc/llm-gateway-go/env \
  -v /opt/llm-gateway-go/data:/opt/llm-gateway-go/data \
  --restart unless-stopped \
  kx-llm-gateway-go:fix-request-logs
```

Watch for the success line:
```
request_logs unique constraint enforced on (request_id) only
```

### Step 3 — verify

```bash
# 1. Re-run the deploy script (will execute step 4 = verify SQL only)
LLM_GATEWAY_DATABASE_URL="..." bash scripts/deploy_request_logs_unique_id.sh

# 2. Check live tests (requires LLM_GATEWAY_PG_TEST_URL)
LLM_GATEWAY_PG_TEST_URL="..." go test ./telemetry/ -run TestRequestLogUnique -v

# Expected:
#   - TestRequestLogUniqueRequestID    PASS (8 retries → 1 row)
#   - TestRequestLogFallbackUpsert     PASS (3 concurrent upserts → 1 row)
#   - TestRequestLogInsertParamCount   PASS (existing test, regression check)
```

### Step 4 — monitor

In the 24 hours after rollout:

```sql
-- 1. No new duplicate rows should appear
SELECT request_id, COUNT(*) AS row_count
FROM request_logs
WHERE ts > now() - interval '1 day'
GROUP BY request_id
HAVING COUNT(*) > 1
LIMIT 10;

-- 2. Success rate should recover for previously-affected models
SELECT
    client_model,
    COUNT(*) FILTER (WHERE success = TRUE) * 1.0 / COUNT(*) AS success_rate,
    COUNT(*) AS total_requests
FROM request_logs
WHERE ts > now() - interval '1 day'
  AND client_model IN ('glm-5.1', 'glm-5', 'gpt-4o', 'deepseek-v3')
GROUP BY client_model
ORDER BY client_model;

-- 3. Index sanity
SELECT indexname, indexdef
FROM pg_indexes
WHERE tablename = 'request_logs'
  AND indexname LIKE '%request_id%';
-- Expected: only idx_request_logs_request_id_unique (parent) + 4 partition ATTACH rows.
```

---

## Rollback

If something goes wrong:

```bash
LLM_GATEWAY_DATABASE_URL="..." bash scripts/rollback_request_logs_unique_id.sh
```

This restores `(request_id, ts)` and drops the new index. **Important:**
you must also revert the application code (telemetry/client.go + db/db.go),
otherwise new INSERTs will create duplicates again.

---

## Open follow-up (post-deploy)

- [ ] Add a Prometheus alert for `request_logs_duplicate_count > 0` to
      catch any future regressions.
- [ ] Audit `routing_decision_log` and other tables for similar
      `(id, ts)`-style constraints that may have the same latent issue.
- [ ] Backfill `decision_log` → `request_logs` join verification for the
      last 30 days to spot any analytics that were misled by duplicates.
