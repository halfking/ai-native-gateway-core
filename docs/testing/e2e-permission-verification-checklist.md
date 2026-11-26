# End-to-End Permission Verification Checklist

**Document Purpose**: Executable testing checklist for three-tier user permission system  
**Target Environments**: 211 (蓝绿), 245 (Pre-production), 154 (Production)  
**Feature Branch**: feature/routing-v2-user-filter  
**Last Updated**: 2026-11-26

---

## Pre-Deployment Verification

### Local Development Checks

- [ ] **Code Review Complete**
  - [ ] All permission filters reviewed
  - [ ] No hardcoded credentials or test data
  - [ ] SQL queries use parameterized statements
  - [ ] Error messages don't leak sensitive information

- [ ] **Build Verification**
  ```bash
  cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-cursor
  git checkout feature/routing-v2-user-filter
  go build ./admin
  go build ./admin/dashboardapi
  go build ./cmd/gateway
  ```
  - [ ] All packages compile without errors
  - [ ] No warnings about deprecated functions

- [ ] **Test Suite Execution**
  ```bash
  # Permission tests
  go test ./admin -run "TestContextEnhanced" -v
  
  # User endpoint tests
  go test ./admin -run "TestHandleMy|TestMyEndpoints" -v
  
  # Routing permission tests
  go test ./admin -run "TestRoutingPermissions" -v
  
  # Canonical read point registry
  go test ./admin -run "TestCanonicalReadPoint" -v
  ```
  - [ ] All tests pass (0 failures)
  - [ ] No test panics or timeouts
  - [ ] Coverage includes all three roles

- [ ] **Database Schema Verification**
  - [ ] Confirm `owner_user` column exists in relevant tables
  - [ ] Verify indexes on `owner_user` column (performance)
  - [ ] Check `tenant_id` column has indexes
  - [ ] No schema migrations pending

---

## Environment-Specific Deployment

### 211 Environment (蓝绿 Testing)

**Current Version**: 3290-a88cee62 (落后 origin/main 4笔)  
**Deployment Script**: `scripts/deploy-211.sh`  
**Database**: 172.16.2.210:5432 (shared with 154/245)

#### Deployment Steps

- [ ] **Pre-Deployment Checks**
  ```bash
  # Check current version
  curl -s http://172.16.1.211:8080/api/system/version | jq
  
  # Verify database connectivity
  psql -h 172.16.2.210 -U gateway_user -d llm_gateway -c "\dt"
  
  # Check disk space
  ssh root@172.16.1.211 "df -h"
  ```

- [ ] **Deploy to 211**
  ```bash
  cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-cursor
  git checkout feature/routing-v2-user-filter
  bash scripts/deploy-211.sh
  ```
  - [ ] Build completes successfully
  - [ ] Test gate passes (ursmcheck)
  - [ ] Blue-green切换 successful
  - [ ] New container starts (check logs)
  - [ ] Readiness check returns 200

- [ ] **Post-Deployment Verification**
  ```bash
  # Verify new version
  curl -s http://172.16.1.211:8080/api/system/version | jq '.git_sha'
  
  # Check logs for errors
  ssh root@172.16.1.211 "docker logs llm-gateway-211-b --tail=100"
  
  # Verify admin login
  curl -X POST http://172.16.1.211:8080/api/auth/login \
    -H "Content-Type: application/json" \
    -d '{"username":"admin","password":"***"}' | jq
  ```

#### Rollback Plan (211)

If critical issues detected:
```bash
# Option 1: Restart old container
ssh root@172.16.1.211 "docker start llm-gateway-211-a"
ssh root@172.16.1.211 "docker stop llm-gateway-211-b"

# Option 2: Redeploy previous version
git checkout <previous-commit-sha>
bash scripts/deploy-211.sh
```

---

### 245 Environment (Pre-Production)

**Current Version**: 3178-6bbc51f5 (落后 24笔)  
**Deployment Script**: `scripts/deploy-245.sh`  
**Database**: 172.16.2.210:5432 (shared)

#### Deployment Steps

- [ ] **Pre-Deployment Checks**
  ```bash
  curl -s http://172.16.2.245:8080/api/system/version | jq
  ssh root@172.16.2.245 "df -h"
  ```

- [ ] **Deploy to 245**
  ```bash
  bash scripts/deploy-245.sh
  ```
  - [ ] All pre-flight checks pass
  - [ ] Blue-green cutover successful
  - [ ] No errors in startup logs

- [ ] **Smoke Test**
  - [ ] Admin portal accessible
  - [ ] Can login with test accounts
  - [ ] API responds to health check

---

### 154 Environment (Production)

**Current Version**: 3177-b56206bc (落后 26笔)  
**Deployment Script**: `scripts/deploy-154.sh`  
**Database**: 172.16.2.210:5432 (shared)  
**Domain**: llm.itestu.cn

#### Deployment Steps

⚠️ **Production Deployment - Requires Owner Approval**

- [ ] **Owner Approval Obtained**
  - [ ] Testing in 211 completed successfully
  - [ ] Testing in 245 completed successfully
  - [ ] No critical bugs identified
  - [ ] Rollback plan reviewed

- [ ] **Maintenance Window Scheduled**
  - [ ] Notify users of potential brief disruption
  - [ ] Schedule during low-traffic period
  - [ ] Coordinate with frontend team

- [ ] **Pre-Deployment Checks**
  ```bash
  curl -s https://llm.itestu.cn/api/system/version | jq
  ssh root@172.16.1.154 "df -h"
  ```

- [ ] **Deploy to 154**
  ```bash
  bash scripts/deploy-154.sh
  ```
  - [ ] All safety gates pass
  - [ ] Blue-green切换 successful
  - [ ] New process starts cleanly

- [ ] **Post-Deployment Monitoring** (First 30 minutes)
  - [ ] Error rate < 1%
  - [ ] Response time < 1000ms (p95)
  - [ ] No increase in 5xx errors
  - [ ] Database query performance stable

---

## Functional Testing Matrix

### Test Account Setup

Create three test accounts (or use existing):

| Role | Username | Tenant | Purpose |
|------|----------|--------|---------|
| **user** | test_user_alice | tenant_a | Regular user testing |
| **tenant_admin** | test_admin_bob | tenant_a | Tenant admin testing |
| **super_admin** | test_superadmin | (platform) | Super admin testing |

**Setup Script** (run once per environment):
```sql
-- Connect to database
\c llm_gateway

-- Create test users (if not exist)
INSERT INTO users (username, email, role, tenant_id, password_hash)
VALUES 
  ('test_user_alice', 'alice@tenant-a.com', 'user', 'tenant_a', '<hash>'),
  ('test_admin_bob', 'bob@tenant-a.com', 'tenant_admin', 'tenant_a', '<hash>'),
  ('test_superadmin', 'super@platform.com', 'super_admin', NULL, '<hash>')
ON CONFLICT (username) DO NOTHING;

-- Create test sessions for filtering tests
INSERT INTO session_turns (session_id, owner_user, tenant_id, model_name, created_at)
VALUES
  ('sess_alice_1', (SELECT user_id FROM users WHERE username='test_user_alice'), 'tenant_a', 'gpt-4', NOW() - INTERVAL '1 hour'),
  ('sess_alice_2', (SELECT user_id FROM users WHERE username='test_user_alice'), 'tenant_a', 'claude-3', NOW() - INTERVAL '2 hours'),
  ('sess_bob_1', (SELECT user_id FROM users WHERE username='test_admin_bob'), 'tenant_a', 'gpt-4', NOW() - INTERVAL '3 hours');
```

---

### Test Case 1: Role Detection

**Endpoint**: `GET /api/auth/me`

- [ ] **Test 1.1: User Role**
  ```bash
  TOKEN=$(login_as test_user_alice)
  curl -H "Authorization: Bearer $TOKEN" \
    http://172.16.1.211:8080/api/auth/me | jq '.role'
  ```
  - [ ] Expected: `"user"`
  - [ ] `tenant_id` is `"tenant_a"`

- [ ] **Test 1.2: Tenant Admin Role**
  ```bash
  TOKEN=$(login_as test_admin_bob)
  curl -H "Authorization: Bearer $TOKEN" \
    http://172.16.1.211:8080/api/auth/me | jq '.role'
  ```
  - [ ] Expected: `"tenant_admin"`
  - [ ] `tenant_id` is `"tenant_a"`

- [ ] **Test 1.3: Super Admin Role**
  ```bash
  TOKEN=$(login_as test_superadmin)
  curl -H "Authorization: Bearer $TOKEN" \
    http://172.16.1.211:8080/api/auth/me | jq '.role'
  ```
  - [ ] Expected: `"super_admin"`
  - [ ] `tenant_id` may be null

---

### Test Case 2: User-Level Session Filtering

**Endpoint**: `GET /api/sessions/my`

- [ ] **Test 2.1: User Sees Only Own Sessions**
  ```bash
  TOKEN=$(login_as test_user_alice)
  curl -H "Authorization: Bearer $TOKEN" \
    http://172.16.1.211:8080/api/sessions/my | jq '.sessions[].session_id'
  ```
  - [ ] Expected: Only `sess_alice_1`, `sess_alice_2`
  - [ ] Does NOT include `sess_bob_1`
  - [ ] Session count matches user's actual sessions

- [ ] **Test 2.2: Tenant Admin Sees All Tenant Sessions**
  ```bash
  TOKEN=$(login_as test_admin_bob)
  curl -H "Authorization: Bearer $TOKEN" \
    http://172.16.1.211:8080/api/sessions/my | jq '.sessions[].session_id'
  ```
  - [ ] Expected: `sess_alice_1`, `sess_alice_2`, `sess_bob_1`
  - [ ] Includes all users in tenant_a
  - [ ] Does NOT include sessions from other tenants

- [ ] **Test 2.3: Super Admin Sees Platform-Wide**
  ```bash
  TOKEN=$(login_as test_superadmin)
  curl -H "Authorization: Bearer $TOKEN" \
    http://172.16.1.211:8080/api/sessions/my | jq '.sessions | length'
  ```
  - [ ] Expected: All sessions across all tenants
  - [ ] Count should be significantly larger

---

### Test Case 3: User-Level Usage Statistics

**Endpoint**: `GET /api/usage/my`

- [ ] **Test 3.1: User Sees Only Own Usage**
  ```bash
  TOKEN=$(login_as test_user_alice)
  curl -H "Authorization: Bearer $TOKEN" \
    "http://172.16.1.211:8080/api/usage/my?start_date=2026-11-01&end_date=2026-11-30" \
    | jq '.summary.total_requests'
  ```
  - [ ] Expected: Only alice's request count
  - [ ] Verify against database: `SELECT COUNT(*) FROM request_metrics WHERE owner_user = alice_user_id`

- [ ] **Test 3.2: Tenant Admin Sees Tenant Usage**
  ```bash
  TOKEN=$(login_as test_admin_bob)
  curl -H "Authorization: Bearer $TOKEN" \
    "http://172.16.1.211:8080/api/usage/my?start_date=2026-11-01&end_date=2026-11-30" \
    | jq '.summary.total_requests'
  ```
  - [ ] Expected: All tenant_a requests (alice + bob)
  - [ ] Verify: Count > alice's count alone

---

### Test Case 4: Routing Statistics Permission

**Endpoint**: `GET /api/routing/overview`

- [ ] **Test 4.1: User Sees Personal Stats Only**
  ```bash
  TOKEN=$(login_as test_user_alice)
  RESPONSE=$(curl -s -H "Authorization: Bearer $TOKEN" \
    http://172.16.1.211:8080/api/routing/overview)
  
  echo "$RESPONSE" | jq '.my_stats'
  echo "$RESPONSE" | jq '.models[0].credential_id'
  ```
  - [ ] `my_stats` section exists and has data
  - [ ] `credential_id` is null or not present
  - [ ] No tenant-wide or platform stats visible

- [ ] **Test 4.2: Tenant Admin Sees Quota, No Credentials**
  ```bash
  TOKEN=$(login_as test_admin_bob)
  RESPONSE=$(curl -s -H "Authorization: Bearer $TOKEN" \
    http://172.16.1.211:8080/api/routing/overview)
  
  echo "$RESPONSE" | jq '.tenant_stats.quota_used'
  echo "$RESPONSE" | jq '.models[0].credential_id'
  echo "$RESPONSE" | jq '.models[0].availability_score'
  ```
  - [ ] `tenant_stats` section exists
  - [ ] `quota_used` and `quota_remaining` visible
  - [ ] `credential_id` is null (desensitized)
  - [ ] `credential_label` is null (desensitized)
  - [ ] `balance` is null (desensitized)
  - [ ] `availability_score` is visible (health metrics OK)

- [ ] **Test 4.3: Super Admin Sees Full Details**
  ```bash
  TOKEN=$(login_as test_superadmin)
  RESPONSE=$(curl -s -H "Authorization: Bearer $TOKEN" \
    http://172.16.1.211:8080/api/routing/overview)
  
  echo "$RESPONSE" | jq '.platform_stats'
  echo "$RESPONSE" | jq '.models[0].credential_id'
  echo "$RESPONSE" | jq '.models[0].credential_label'
  echo "$RESPONSE" | jq '.models[0].balance'
  ```
  - [ ] `platform_stats` section exists
  - [ ] `credential_id` is visible (non-null integer)
  - [ ] `credential_label` is visible (string)
  - [ ] `balance` is visible (number)

---

### Test Case 5: Credential Desensitization

**Endpoint**: `POST /api/routing/resolve`

- [ ] **Test 5.1: User Gets No Credential Details**
  ```bash
  TOKEN=$(login_as test_user_alice)
  curl -X POST -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"model":"gpt-4","tenant_id":"tenant_a"}' \
    http://172.16.1.211:8080/api/routing/resolve \
    | jq '.candidates[0]'
  ```
  - [ ] `resolved: true` if credentials available
  - [ ] `credential_id` field not present
  - [ ] `credential_label` field not present
  - [ ] `credential_available: true/false` is OK to show

- [ ] **Test 5.2: Tenant Admin Gets No Credential Details**
  ```bash
  TOKEN=$(login_as test_admin_bob)
  # Same test as above
  ```
  - [ ] Same expectations as Test 5.1

- [ ] **Test 5.3: Super Admin Gets Full Credential Details**
  ```bash
  TOKEN=$(login_as test_superadmin)
  curl -X POST -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"model":"gpt-4","tenant_id":"tenant_a"}' \
    http://172.16.1.211:8080/api/routing/resolve \
    | jq '.candidates[0]'
  ```
  - [ ] `credential_id` present (integer)
  - [ ] `credential_label` present (string)
  - [ ] `provider` present
  - [ ] All routing details visible

---

### Test Case 6: Access Control Enforcement

**Test Cross-Tenant Access Prevention**

- [ ] **Test 6.1: User Cannot Access Other User's Sessions**
  ```bash
  # Try to access bob's session directly (should fail)
  TOKEN=$(login_as test_user_alice)
  curl -H "Authorization: Bearer $TOKEN" \
    http://172.16.1.211:8080/api/sessions/sess_bob_1 \
    -w "\nHTTP Status: %{http_code}\n"
  ```
  - [ ] Expected HTTP Status: 403 Forbidden or 404 Not Found
  - [ ] Error message indicates permission denied

- [ ] **Test 6.2: Tenant Admin Cannot Access Other Tenant's Data**
  ```bash
  # Create session in tenant_b, verify tenant_a admin can't see it
  TOKEN=$(login_as test_admin_bob)
  curl -H "Authorization: Bearer $TOKEN" \
    "http://172.16.1.211:8080/api/sessions/my" \
    | jq '.sessions[] | select(.tenant_id != "tenant_a")'
  ```
  - [ ] Expected: Empty result (no tenant_b sessions)

---

## Performance Verification

### Database Index Checks

- [ ] **Verify Indexes Exist**
  ```sql
  \c llm_gateway
  
  -- Check indexes on owner_user column
  SELECT tablename, indexname, indexdef
  FROM pg_indexes
  WHERE indexname LIKE '%owner_user%';
  
  -- Check indexes on tenant_id column
  SELECT tablename, indexname, indexdef
  FROM pg_indexes
  WHERE indexname LIKE '%tenant_id%';
  ```
  - [ ] `session_turns` has index on `owner_user`
  - [ ] `request_metrics` has index on `owner_user`
  - [ ] Both tables have index on `tenant_id`

### Query Performance Tests

- [ ] **User Session Query Performance**
  ```sql
  EXPLAIN ANALYZE
  SELECT * FROM session_turns
  WHERE owner_user = 123
  ORDER BY created_at DESC
  LIMIT 20;
  ```
  - [ ] Uses index scan (not sequential scan)
  - [ ] Execution time < 10ms
  - [ ] Planning time < 5ms

- [ ] **Tenant Session Query Performance**
  ```sql
  EXPLAIN ANALYZE
  SELECT * FROM session_turns
  WHERE tenant_id = 'tenant_a'
  ORDER BY created_at DESC
  LIMIT 100;
  ```
  - [ ] Uses index scan
  - [ ] Execution time < 50ms

- [ ] **API Response Time**
  ```bash
  # Measure end-to-end latency
  for i in {1..10}; do
    TOKEN=$(get_token test_user_alice)
    time curl -s -H "Authorization: Bearer $TOKEN" \
      http://172.16.1.211:8080/api/sessions/my > /dev/null
  done
  ```
  - [ ] Average response time < 500ms
  - [ ] No timeouts
  - [ ] Consistent performance across runs

---

## Rollback Procedures

### Immediate Rollback (Critical Issues)

If critical bugs detected in production (154):

1. [ ] **Stop New Container**
   ```bash
   ssh root@172.16.1.154 "docker stop llm-gateway-154-new"
   ```

2. [ ] **Restart Old Container**
   ```bash
   ssh root@172.16.1.154 "docker start llm-gateway-154-old"
   ```

3. [ ] **Verify Service Recovery**
   ```bash
   curl https://llm.itestu.cn/api/health
   ```

4. [ ] **Notify Team**
   - Report issue in team channel
   - Document failure symptoms
   - Schedule post-mortem

### Gradual Rollback (Non-Critical Issues)

For minor issues that don't require immediate action:

1. [ ] **Document Issues**
   - Create bug tickets
   - Assign priority
   - Estimate fix timeline

2. [ ] **Schedule Maintenance**
   - Deploy fix to 211 first
   - Test thoroughly
   - Deploy to 245/154 after validation

---

## Sign-Off Checklist

### 211 Environment

- [ ] All functional tests passed
- [ ] No errors in logs (30-minute window)
- [ ] Performance metrics acceptable
- [ ] Rollback tested successfully
- [ ] **Signed off by**: _____________ Date: _______

### 245 Environment

- [ ] All functional tests passed
- [ ] No errors in logs (1-hour window)
- [ ] Performance metrics acceptable
- [ ] **Signed off by**: _____________ Date: _______

### 154 Environment (Production)

- [ ] Owner approval obtained
- [ ] All functional tests passed
- [ ] No errors in logs (24-hour window)
- [ ] Performance metrics stable
- [ ] User feedback monitored (no complaints)
- [ ] **Signed off by**: _____________ Date: _______

---

## Post-Deployment Monitoring (First Week)

### Daily Checks

- [ ] **Day 1**: Check error logs every 2 hours
- [ ] **Day 2**: Check error logs every 4 hours
- [ ] **Day 3-7**: Check error logs daily

### Metrics to Monitor

- [ ] **Error Rate**: < 1% (5xx errors)
- [ ] **Response Time**: p95 < 1000ms, p99 < 2000ms
- [ ] **Permission Denial Rate**: Track 403 errors (should be < 0.1%)
- [ ] **Database Query Time**: User queries < 50ms, tenant queries < 100ms

### Alert Thresholds

Set up alerts for:
- Error rate > 2%
- Response time p95 > 2000ms
- Database query time > 500ms
- Sudden spike in 403 errors (> 10/min)

---

## Troubleshooting Guide

### Common Issues

**Issue 1: User sees "Access Denied" (403)**
- Check user role in database: `SELECT role FROM users WHERE username='...'`
- Verify JWT token includes role claim
- Check endpoint permission requirements

**Issue 2: User sees other users' data**
- Verify `owner_user` filter is applied in query
- Check test data: ensure sessions have correct owner_user
- Review SQL query logs for missing WHERE clause

**Issue 3: Credential details visible to non-super_admin**
- Check desensitization logic in handlers
- Verify view or SELECT statement excludes sensitive columns
- Test with actual tenant_admin account (not super_admin)

**Issue 4: Slow query performance**
- Run EXPLAIN ANALYZE on the query
- Verify indexes exist and are being used
- Check table statistics are up to date: `ANALYZE table_name;`

---

**Document Version**: 1.0  
**Last Updated**: 2026-11-26  
**Next Review**: After production deployment
