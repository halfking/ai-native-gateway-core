# LLM Gateway Go v2.0 — Auto Route Mode Operations Manual

**For**: ops, SRE, on-call
**Date**: 2026-06-14

---

## 1. Health checks

### Quick smoke test (curl)

```bash
# Healthz
curl -i https://llmgo.kxpms.cn/healthz

# Auto route — code task
curl -i -X POST https://llmgo.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "auto",
    "messages": [{"role":"user","content":"用python写一个快排"}]
  }'

# Verify response header
curl -is -X POST https://llmgo.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"auto","messages":[{"role":"user","content":"hi"}]}' \
  | grep -i 'x-gw-auto-decision'
```

### Admin endpoints

```bash
# Recent decisions
curl -s "https://llmgo.kxpms.cn/api/admin/auto-route/decisions?limit=10" \
  -H "Authorization: Bearer $ADMIN_TOKEN" | jq

# Current index snapshot
curl -s "https://llmgo.kxpms.cn/api/admin/auto-route/index?top=20" \
  -H "Authorization: Bearer $ADMIN_TOKEN" | jq

# Set sticky profile for an API key
curl -s -X PUT "https://llmgo.kxpms.cn/api/admin/auto-route/profile" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"api_key_id": 42, "profile": "cost_first"}' | jq

# Aggregated stats
curl -s "https://llmgo.kxpms.cn/api/admin/auto-route/audit" \
  -H "Authorization: Bearer $ADMIN_TOKEN" | jq

# Manually trigger index refresh
curl -s -X POST "https://llmgo.kxpms.cn/api/admin/auto-route/refresh" \
  -H "Authorization: Bearer $ADMIN_TOKEN" | jq
```

---

## 2. SQL Migrations

### Apply (184 k3s PG)

```bash
# kubectl exec into the PG pod, then pipe the SQL
kubectl -n pms-test exec -i postgres-0 -- psql -U stockuser -d llmgw < \
  services/llm-gateway-go/docs/2026-06-15-auto-route-mode.sql
```

### Rollback

```bash
kubectl -n pms-test exec -i postgres-0 -- psql -U stockuser -d llmgw < \
  services/llm-gateway-go/docs/2026-06-15-auto-route-mode.down.sql
```

### Verify

```sql
-- After apply
\dt credential_model_index
\dt model_task_index
\dt api_key_auto_profile
\d request_logs

-- Should show new columns
SELECT is_auto_request, task_type, auto_profile, auto_decision, auto_confidence
FROM request_logs LIMIT 1;
```

---

## 3. Env vars (production defaults)

| Var                                          | Default | Purpose                                       |
|----------------------------------------------|---------|-----------------------------------------------|
| `LLM_GATEWAY_ENABLE_AUTO_ROUTE`              | `true`  | Master switch for auto-route                  |
| `LLM_GATEWAY_AUTO_CLASSIFY_LLM_FALLBACK`     | `true`  | Enable LLM fallback for low-confidence        |
| `LLM_GATEWAY_AUTO_LLM_CONFIDENCE_THRESHOLD` | `0.7`   | Heuristic confidence threshold                |
| `LLM_GATEWAY_AUTO_INDEX_REFRESH_INTERVAL`    | `5m`    | How often bg worker refreshes the index       |
| `LLM_GATEWAY_AUTO_DEFAULT_PROFILE`           | `smart` | Default when no header + no sticky            |
| `LLM_GATEWAY_AUTO_STICKY_TTL`                | `1800`  | Sticky profile TTL in seconds (30 min)        |

To **disable auto-route entirely**: set `LLM_GATEWAY_ENABLE_AUTO_ROUTE=false`.

---

## 4. Failure modes & recovery

### Auto route broken (decider errors)

**Symptom**: every `model=auto` request returns 502 or falls back to
claude-sonnet-4.5.

**Check**:
```bash
# Check pod logs
kubectl -n pms-test logs deploy/llm-gateway-go --tail=200 | grep -i 'autoroute'

# Check index freshness
curl -s "https://llmgo.kxpms.cn/api/admin/auto-route/audit" \
  -H "Authorization: Bearer $ADMIN_TOKEN" | jq '.total_auto_requests'
```

**Fix**:
1. Manual refresh: `POST /api/admin/auto-route/refresh`
2. If still broken, restart pod: `kubectl -n pms-test rollout restart deploy/llm-gateway-go`
3. As last resort: `LLM_GATEWAY_ENABLE_AUTO_ROUTE=false` (falls back to
   explicit-model routing, same as v0.79)

### Index never refreshes

**Symptom**: `auto-route/index` endpoint returns stale bucket.

**Check**:
```bash
# How old is the latest bucket?
curl -s "https://llmgo.kxpms.cn/api/admin/auto-route/index?top=1" \
  -H "Authorization: Bearer $ADMIN_TOKEN" | jq '.[0].bucket'
```

If bucket is older than 30 min, the bg worker is stuck. Restart it:
```bash
kubectl -n pms-test rollout restart deploy/llm-gateway-go
```

### LLM fallback flooding errors

**Symptom**: `slog` logs show repeated "LLM fallback failed" warnings.

**Fix**: set `LLM_GATEWAY_AUTO_CLASSIFY_LLM_FALLBACK=false`. Requests
will use heuristic-only classification (lower accuracy but no LLM cost).

---

## 5. Cost analysis

Worst case per-request cost for `model=auto`:

| Path                       | Cost                        |
|----------------------------|-----------------------------|
| Heuristic-only             | 0 tokens (in-process)       |
| LLM fallback (~30% cases)  | ~250 tokens (cheap model)   |
| Chosen upstream model      | varies (logged in usage)    |

A 30% LLM fallback rate at 250 tokens × $0.0001/token ≈ **$0.025 per
1000 requests** of overhead. Negligible compared to actual model cost.

---

## 6. Monitoring

### Key metrics to alert on

| Metric                                | Threshold            | Severity  |
|---------------------------------------|----------------------|-----------|
| `auto_route/decisions_total`          | < 10 / 5min          | warn      |
| `auto_route/refresh_failures_total`   | > 0 / 5min           | page      |
| `auto_route/llm_fallback_failure`     | > 50% of attempts    | warn      |
| `auto_route/p99_latency_ms`           | > 1000ms             | warn      |
| `credential_model_index/staleness_s`  | > 900s (15 min)      | page      |

### Grafana queries (suggested)

```promql
# Auto-route request rate
rate(request_logs_total{is_auto_request="true"}[5m])

# Auto-route success rate
sum(rate(request_logs_total{is_auto_request="true",success="true"}[5m])) /
sum(rate(request_logs_total{is_auto_request="true"}[5m]))

# Index staleness
time() - max(credential_model_index_bucket_timestamp)
```

---

## 7. Migration safety

The v2.0 SQL migration is **forward-compatible**:
- New tables start empty (no impact on existing requests)
- New request_logs columns have DEFAULT values (existing INSERTs work)
- No existing indexes or constraints are modified
- Gateway binary works against old + new DB schema (uses IF NOT EXISTS)

Rollback is safe because:
- No existing data is deleted
- request_logs columns can be dropped without losing rows
- v0.79 binary ignores the new tables (no SELECT on them)

---

## 8. Reference

- Design doc: `docs/2026-06-15-auto-route-mode-design.md`
- 24h audit report: `docs/2026-06-15-24h-audit-report.md`
- Source: `autoroute/`, `bg/auto_index_refresher.go`,
  `admin/auto_route.go`, `relay/auto_route.go`
- Tests: `go test ./autoroute/... ./bg/... ./admin/...`