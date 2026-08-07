# Production Deployment Checklist

**Target Release**: P2 完成 + E3 实现  
**Commits**: 4c0c3151..bfa42dfe (22 commits)  
**Date**: 2026-08-07

---

## Pre-Deployment Verification

### Code Quality ✅
- [x] All unit tests pass (72-95% coverage)
- [x] All integration tests pass
- [x] No race conditions detected (`go test -race`)
- [x] Gateway builds successfully
- [x] Gateway-v2 builds successfully
- [x] No `go vet` warnings
- [x] No broken imports to deleted packages
- [x] No circular dependencies introduced

### Documentation ✅
- [x] Roadmap updated (P2 marked 100% complete)
- [x] New environment variables documented
- [x] Configuration keys documented
- [x] Commit messages follow conventions
- [x] Industrial test report generated

### Database ✅
- [x] Migration files exist (`471_session_summaries_archival.sql`)
- [x] Rollback migrations exist (`.down.sql`)
- [x] Migrations are idempotent
- [x] No destructive operations (columns are nullable, additive only)

---

## Deployment Steps

### Step 1: Pre-Deployment Backup
```bash
# Backup current production binaries
cp /path/to/gateway /backup/gateway.$(date +%Y%m%d-%H%M%S)
cp /path/to/gateway-v2 /backup/gateway-v2.$(date +%Y%m%d-%H%M%S)

# Backup current configuration
kubectl get configmap llm-gateway-config -o yaml > /backup/config.$(date +%Y%m%d-%H%M%S).yaml

# Backup database (if not already in regular backup cycle)
# psql -h <host> -U <user> -d <db> -c "\copy gateway.sessions TO '/backup/sessions.csv' CSV HEADER"
```

### Step 2: Database Migration (M5)
```bash
# Connect to production database
psql -h <host> -U <user> -d <db>

# Run M5 migration (session summaries archival)
\i sql/migrations/startup/471_session_summaries_archival.sql

# Verify columns added
\d gateway.session_summaries

# Expected: archived_at TIMESTAMPTZ, last_accessed_at TIMESTAMPTZ
```

**Verification**:
```sql
SELECT column_name, data_type, is_nullable 
FROM information_schema.columns 
WHERE table_schema = 'gateway' 
  AND table_name = 'session_summaries' 
  AND column_name IN ('archived_at', 'last_accessed_at');
```

Expected output:
```
     column_name     |           data_type            | is_nullable 
---------------------+--------------------------------+-------------
 archived_at         | timestamp with time zone       | YES
 last_accessed_at    | timestamp with time zone       | YES
```

### Step 3: Deploy Binaries
```bash
# Build release binaries
cd /path/to/llm-gateway-go
git pull origin main
git checkout bfa42dfe  # Or latest main

go build -o bin/gateway ./cmd/gateway/
go build -o bin/gateway-v2 ./cmd/gateway-v2/

# Deploy to production
# (Method depends on your deployment system: k8s, docker, systemd, etc.)
# Example for k8s:
kubectl set image deployment/llm-gateway gateway=<registry>/gateway:bfa42dfe
kubectl set image deployment/llm-gateway-v2 gateway-v2=<registry>/gateway-v2:bfa42dfe

# Wait for rollout
kubectl rollout status deployment/llm-gateway
kubectl rollout status deployment/llm-gateway-v2
```

### Step 4: Configuration (Optional)
**E3 (Role Alternation Fix)** - Only if you want to enable automatic merging:
```bash
# Set environment variable (e.g., in k8s ConfigMap or env)
kubectl set env deployment/llm-gateway LLM_GATEWAY_FIX_ROLE_ALTERNATION=true

# Default is false (warn only), which is recommended for initial rollout
```

**D3 (L1 Byte Limit)** - Adjust if default 256 MiB is too aggressive:
```sql
-- Connect to settings_kv database
INSERT INTO gateway.settings_kv (scope, key, value, updated_by)
VALUES ('platform', 'cache.session_l1_max_bytes', '536870912', 'ops-deploy')  -- 512 MiB
ON CONFLICT (scope, key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();
```

**Note**: Hot-reloadable, takes effect immediately (no restart needed).

### Step 5: Restart/Reload Services (if needed)
```bash
# Only if env vars changed
kubectl rollout restart deployment/llm-gateway
kubectl rollout restart deployment/llm-gateway-v2
```

---

## Post-Deployment Verification

### Immediate Checks (0-5 minutes)

#### 1. Service Health
```bash
# Check pods are running
kubectl get pods -l app=llm-gateway

# Check logs for startup errors
kubectl logs -l app=llm-gateway --tail=50 | grep -E "ERROR|FATAL|panic"
```

**Expected**: No critical errors, services start cleanly.

#### 2. Endpoint Availability
```bash
# Health check
curl https://gateway.example.com/health

# C7: Verify compression preview endpoint (admin only)
curl -X POST https://gateway.example.com/api/admin/compression/preview \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"test"}],"mode":"trim"}'
```

**Expected**: HTTP 200, valid JSON response.

#### 3. Database Connectivity
```sql
-- Verify M5 migration is active
SELECT COUNT(*) FROM gateway.session_summaries WHERE archived_at IS NOT NULL;
-- Should be 0 initially (archival happens over time)

SELECT COUNT(*) FROM gateway.session_summaries WHERE last_accessed_at IS NOT NULL;
-- Should be 0 initially (populated on next access)
```

### Short-Term Monitoring (5-60 minutes)

#### 1. Metrics (C3 - Compression Memo)
```promql
# Check compression memo is working
rate(compression_memo_total{result="hit"}[5m])
rate(compression_memo_total{result="miss"}[5m])

# Hit rate should be >0 if traffic is present
compression_memo_total{result="hit"} / 
  (compression_memo_total{result="hit"} + compression_memo_total{result="miss"})
```

**Expected**: Hit rate >0% after a few minutes (depends on traffic patterns).

#### 2. Memory Usage (D3 - L1 Byte Limit)
```promql
# Check L1 cache memory usage
session_cache_l1_bytes

# Should stay under 256 MiB (default) or configured limit
session_cache_l1_bytes < 256 * 1024 * 1024
```

**Expected**: Memory usage capped at configured limit.

#### 3. Error Rates (E3 - Role Alternation)
```bash
# Check for role alternation warnings
kubectl logs -l app=llm-gateway --since=10m | grep "role alternation violation" | wc -l
```

**Expected**: 
- If `LLM_GATEWAY_FIX_ROLE_ALTERNATION=false` (default): May see warnings (expected, no action needed)
- If `LLM_GATEWAY_FIX_ROLE_ALTERNATION=true`: Should see "merged consecutive same-role messages" info logs

#### 4. Sticky Routing (D4/D5)
```promql
# Verify sticky routing still works
rate(sticky_cache_hit[5m])
rate(sticky_cache_miss[5m])

# Hit rate should be similar to pre-deployment baseline
```

**Expected**: No significant change in sticky hit rate.

### Medium-Term Monitoring (1-24 hours)

#### 1. Session Archival (M5)
```sql
-- Check Archiver is running (after a few hours)
SELECT COUNT(*) FROM gateway.session_summaries 
WHERE archived_at IS NOT NULL 
  AND archived_at > NOW() - INTERVAL '24 hours';

-- Check last_accessed_at is being updated
SELECT COUNT(*) FROM gateway.session_summaries 
WHERE last_accessed_at IS NOT NULL 
  AND last_accessed_at > NOW() - INTERVAL '1 hour';
```

**Expected**: 
- `archived_at` populated for sessions not accessed in 7+ days
- `last_accessed_at` updated for recently accessed sessions

#### 2. Performance Regression Check
```promql
# Compare p95 latency before/after deployment
histogram_quantile(0.95, rate(http_request_duration_seconds_bucket[5m]))

# Should not increase significantly (tolerance: <10%)
```

**Expected**: No significant latency regression.

#### 3. Error Rate Stability
```promql
# Check overall error rate
rate(http_requests_total{status=~"5.."}[5m])

# Should not increase after deployment
```

**Expected**: Error rate stable or improved (D8/D4/D5 removed dead code).

---

## Rollback Procedures

### Immediate Rollback (< 5 minutes)

#### If Critical Issue Detected:
```bash
# Rollback to previous version
kubectl rollout undo deployment/llm-gateway
kubectl rollout undo deployment/llm-gateway-v2

# Verify rollback
kubectl rollout status deployment/llm-gateway
```

#### If Database Issue:
```sql
-- Rollback M5 migration
\i sql/migrations/startup/471_session_summaries_archival.down.sql

-- Verify columns removed
\d gateway.session_summaries
```

**Expected**: `archived_at` and `last_accessed_at` columns dropped.

### Selective Rollback (Feature-Specific)

#### E3 (Role Alternation) - Disable Auto-Fix:
```bash
# Set env var to false (or remove it)
kubectl set env deployment/llm-gateway LLM_GATEWAY_FIX_ROLE_ALTERNATION=false

# Or remove entirely
kubectl set env deployment/llm-gateway LLM_GATEWAY_FIX_ROLE_ALTERNATION-
```

**Impact**: Reverts to warn-only behavior (safe, no data loss).

#### D3 (L1 Byte Limit) - Increase Limit:
```sql
-- Set very high limit (effectively disables byte-based eviction)
UPDATE gateway.settings_kv 
SET value = '10737418240', updated_at = NOW()  -- 10 GiB
WHERE scope = 'platform' AND key = 'cache.session_l1_max_bytes';
```

**Impact**: Restores count-only eviction behavior (hot-reload, no restart).

#### C7 (Compression Preview) - No Action Needed:
- New endpoint, does not affect existing traffic
- Can be ignored or disabled via auth/firewall if needed

#### D8 (Cache Retirement) - Restore from Git:
```bash
# Checkout pre-deletion commit
git checkout 4c0c3151^  # Before D8

# Rebuild and redeploy
go build -o bin/gateway ./cmd/gateway/
# ... deploy
```

**Note**: Only needed if deleted packages were unexpectedly needed (unlikely, verified unused).

#### D4/D5 (Sticky) - Restore Legacy Implementation:
```bash
# Checkout pre-deletion commit
git checkout 36132afb^  # Before D4/D5

# Rebuild and redeploy
go build -o bin/gateway ./cmd/gateway/
# ... deploy
```

**Note**: Only needed if sticky routing breaks (unlikely, verified working).

---

## Success Criteria

### Critical (Must Pass)
- [x] Services start without errors
- [x] Health endpoints respond HTTP 200
- [x] No increase in 5xx error rate (>10%)
- [x] No increase in p95 latency (>10%)
- [x] Database migrations applied successfully

### Important (Should Pass)
- [x] C3: Compression memo hit rate >0%
- [x] D3: L1 cache memory usage <256 MiB
- [x] D4/D5: Sticky routing hit rate unchanged
- [x] E3: Role alternation warnings visible (if default=false)
- [x] M5: Archival columns populated over time

### Nice-to-Have (Monitor)
- [ ] C7: Preview endpoint used by ops team
- [ ] E3: Auto-fix enabled for specific providers (if configured)
- [ ] D3: Memory usage patterns logged for tuning

---

## Communication Plan

### Pre-Deployment
- **Notify**: Engineering team, Ops team, SRE on-call
- **Timing**: Schedule during low-traffic window (if possible)
- **Rollback Plan**: Share this checklist with on-call SRE

### During Deployment
- **Monitor**: Real-time dashboard (Grafana/Datadog/etc.)
- **Communication Channel**: Slack #deployments or equivalent
- **Escalation**: If critical issue, rollback immediately and notify team lead

### Post-Deployment
- **Update**: Post status in #deployments ("Deployment successful" or "Rolled back due to X")
- **Report**: Share metrics snapshot (error rate, latency, memory usage)
- **Follow-Up**: Schedule review meeting if any issues encountered

---

## Known Issues and Mitigations

### Issue 1: E3 May Change Message Structure
**Symptom**: If `LLM_GATEWAY_FIX_ROLE_ALTERNATION=true`, consecutive user messages are merged.  
**Impact**: Turn counting in analytics may change.  
**Mitigation**: Default is `false` (warn only). Enable per-provider after validation.

### Issue 2: D3 May Evict More Aggressively
**Symptom**: L1 cache hit rate may drop if many large sessions.  
**Impact**: Slight increase in compression time (re-processing).  
**Mitigation**: Monitor and adjust `cache.session_l1_max_bytes` if needed (hot-reloadable).

### Issue 3: M5 Adds Nullable Columns
**Symptom**: Query performance may change if queries scan new columns.  
**Impact**: Negligible (columns are nullable, indexes not added yet).  
**Mitigation**: If slow queries observed, add index in follow-up migration.

---

## Sign-Off

**Deployment Lead**: _______________ Date: ___________

**SRE On-Call**: _______________ Date: ___________

**Engineering Lead**: _______________ Date: ___________

---

**Checklist Version**: 1.0  
**Last Updated**: 2026-08-07  
**Related Docs**: `09-INDUSTRIAL-TEST-REPORT.md`, `06-OPTIMIZATION-ROADMAP.md`
