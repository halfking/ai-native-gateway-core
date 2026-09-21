# Request Detail Enhancement - Executive Summary

**Document Version:** 1.0  
**Last Updated:** 2026-08-28  
**Status:** Implementation Complete, Verified

---

## Overview

This document provides an executive summary of the unified request detail enhancement, which implements a multi-layer content store with intelligent fallback mechanisms for retrieving request metadata and bodies across different persistence layers.

## Architectural Goals

1. **Unified Facade**: Single API endpoint (`GET /api/admin/request-detail/{request_id}`) serving content from multiple sources
2. **Zero-downtime Access**: Support both in-flight and persisted requests without gaps
3. **Graceful Degradation**: Return metadata when bodies are unavailable (TTL expiry, partial persistence)
4. **Query Efficiency**: Optimize database lookups with cascading queries instead of inefficient OR predicates
5. **Tenant Isolation**: Enforce tenant boundaries across all content sources

## Implementation Architecture

### Content Resolution Hierarchy

The system resolves request details through a four-layer cascade:

```
1. Memory (in-flight meta)         → fastest, ephemeral
2. Local File (in-flight bodies)   → fast, ephemeral
3. Request Logs (DB)               → authoritative, persisted
4. Session Turns (DB)              → fallback, persisted
```

**Resolution Logic:**
- **Layer 1-2**: Check in-flight store (memory + local files) for active requests
- **Layer 3**: Query `request_logs_hot` / `request_logs_with_current_month` for persisted metadata and bodies
- **Layer 4**: Fall back to `session_turns` / `session_bodies` when request_logs body is missing
- **Metadata-only**: Return metadata without bodies when all body sources are exhausted (with warning)

### Key Components

#### 1. In-Flight Store (`domains/requestdetail/store.go`)

**Purpose:** Capture request content before DB persistence completes

**Features:**
- Memory-mapped metadata (4096 entry default, 30min TTL)
- Local file storage for bodies (`{dir}/{request_id}.json`)
- Automatic eviction (LRU + TTL)
- Safe request_id validation (prevents path traversal: `^[A-Za-z0-9._-]{8,128}$`)

**Lifecycle:**
1. `CaptureFromEntry` writes meta + bodies during request processing
2. `GetMeta` / `GetFile` serve detail API for in-flight requests
3. `Clear` removes entry after telemetry DB persist

**Configuration:**
- `LLM_GATEWAY_REQUEST_DETAIL_DIR` (default: `/tmp/llmgw-request-detail`)

#### 2. Locator (`domains/requestdetail/locator.go`)

**Purpose:** Orchestrate multi-source resolution with merge semantics

**Resolution Strategy:**
- Check memory → file (in-flight)
- Query request_logs (persisted)
- On `ErrNotFound` but metadata present → try session_turns
- If session_turns succeeds → merge metadata, return `source=session_turns`
- If session_turns fails but metadata exists → metadata-only detail with warning
- If no signal from any source → `404 ErrNotFound`

**Tenant Scoping:**
- `LookupScope` passed via context (from `WithLookupScope`)
- `Unrestricted=false` → injects `AND tenant_id = $n` into DB queries
- Tenant admins see only their tenant's data; super admins bypass

#### 3. DB Reader (`admin/unified_detail.go`)

**Purpose:** Load persisted content from PostgreSQL with query optimization

**Optimizations (2026-08-27):**

**Before (inefficient):**
```sql
SELECT ... FROM request_logs_hot
WHERE (request_id = $1 OR client_request_id = $1)
LIMIT 1
```
❌ OR prevents index usage, forces sequential scan on large tables

**After (cascading):**
```sql
-- Step 1: exact request_id on hot (PK/index hit)
SELECT ... FROM request_logs_hot WHERE request_id = $1 LIMIT 1

-- Step 2: client_request_id on hot (index hit)
SELECT ... FROM request_logs_hot WHERE client_request_id = $1 ORDER BY ts DESC LIMIT 1

-- Step 3: request_id on partition view
SELECT ... FROM request_logs_with_current_month WHERE request_id = $1 LIMIT 1

-- Step 4: client_request_id on partition view
SELECT ... FROM request_logs_with_current_month WHERE client_request_id = $1 ORDER BY ts DESC LIMIT 1
```
✅ Each query uses a single index; hot table prioritized; common case (recent request_id) = 1 round-trip

**Partial Body Fallback (2026-08-28):**

When `request_logs_bodies` has incomplete data (e.g., stream response not captured):
1. Load available fields from `request_logs_bodies` (request/response/outbound)
2. For any `NULL` or empty field, query `session_bodies` via `session_turns.request_id`
3. Fill **only missing fields**; never overwrite present data
4. Preserve request_logs as authoritative source

**Example:**
```
request_logs_bodies:  {request: {...}, response: NULL, outbound: {...}}
session_bodies:       {request: {...}, response: {...}, outbound: {...}}
Merged result:        {request: from request_logs, response: from session, outbound: from request_logs}
```

#### 4. HTTP Handler (`admin/unified_detail.go::handleUnifiedRequestDetail`)

**Endpoint:** `GET /api/admin/request-detail/{request_id}[?omit_body=1]`

**Features:**
- Request ID validation (8-128 chars, alphanumeric + `._-`)
- `omit_body=1` skips body loading (metadata-only, fast)
- Tenant isolation gate (2026-08-26 P1-29 fix):
  - Applied to **all sources** (memory, file, DB)
  - Empty `TenantID` → fail-closed for tenant admins
  - Prevents cross-tenant leakage via guessed request_id

**Response Schema:**
```json
{
  "source": "memory|file|request_logs|session_turns",
  "persistence": "in_flight|persisted",
  "meta": {
    "request_id": "...",
    "tenant_id": "...",
    "gw_session_id": "...",
    "gw_task_id": "...",
    "client_model": "...",
    "status": "...",
    "success": true,
    "latency_ms": 123,
    "turn_number": 2
  },
  "bodies": {
    "request_body": {...},
    "response_body": {...},
    "outbound_body": {...}
  },
  "warning": "request body row not found in request_logs or session_turns; metadata-only fallback"
}
```

---

## Verification & Testing

### Test Coverage

#### Unit Tests (all passing)

**`admin/unified_detail_test.go`:**
- ✅ Memory-only detail
- ✅ Store not configured → 503
- ✅ Tenant isolation (memory, file, DB sources)
- ✅ Empty tenant_id → deny for tenant admin
- ✅ Super admin bypass
- ✅ Invalid request_id → 400

**`domains/requestdetail/store_test.go`:**
- ✅ Put/Get/Clear lifecycle
- ✅ Unsafe request_id rejection (path traversal prevention)
- ✅ Malformed/mismatched file → miss (no crash)
- ✅ Memory → file → DB fallback order
- ✅ Metadata-only fallback when bodies absent
- ✅ Session_turns fills missing request_logs bodies
- ✅ `omit_body=1` skips body queries

### Production Verification

**Environment:** 245 (llmgo.kxpms.cn)  
**Verified:** 2026-08-27 21:29 (build 1771, git_sha 7928664f)

**Evidence:**
```bash
# Store wired
$ grep "request detail content store wired" /data/llm-gateway-go/logs/stderr.log
2026-08-27T21:29:34.567 INFO request detail content store wired dir=/tmp/llmgw-request-detail

# API functional
$ curl -s 'https://llmgo.kxpms.cn/api/admin/request-detail/9d0735a9...?omit_body=1'
{"source":"request_logs","persistence":"persisted","meta":{...}} # 200 OK, 17ms

# Views exist
$ psql -h <env:HOST_252_DB_IP> -U llmgw_admin -d llmgw -c "SELECT viewname FROM pg_views WHERE viewname LIKE '%_with_current_month';"
 request_logs_with_current_month
 request_logs_bodies_with_current_month
 session_turns_with_current_month
```

**Performance:**
- Metadata-only (`omit_body=1`): 8-17ms
- Full body (300KB): 8.7ms (local), 57ms (public → 252 DB)
- Cascade miss (4 queries): acceptable for rare client_request_id + old partition case

---

## Security & Compliance

### Tenant Isolation (P1-29 Fix, 2026-08-26)

**Issue:** Pre-fix implementation only enforced tenant isolation for persisted details, leaving in-flight store vulnerable to cross-tenant access via guessed `request_id`.

**Resolution:**
- Unified gate applied **after** locator resolution, **before** response
- Checks `detail.Meta.TenantID` against `GetTenantID(r)` for tenant admins
- Empty `TenantID` → fail-closed (deny) for tenant admins
- Super admins (`IsSuperAdminOrLegacy`) bypass tenant check

**Test Coverage:**
- ✅ `TestHandleUnifiedRequestDetail_TenantIsolation_FromFile`
- ✅ `TestHandleUnifiedRequestDetail_TenantIsolation_FromMemory`
- ✅ `TestHandleUnifiedRequestDetail_TenantIsolation_EmptyTenantID`

### Path Traversal Prevention

- Request ID validation: `^[A-Za-z0-9._-]{8,128}$`
- Prevents `../`, absolute paths, shell metacharacters
- Enforced at:
  - `ValidateRequestID` (exported for HTTP layer)
  - `Store.Put*` / `Store.GetFile` (fail fast)

---

## Known Limitations & Follow-up

### Non-Goals (Verified Out-of-Scope)

❌ **Reconstruction of lost responses:** If both `request_logs_bodies.response_body` and `session_bodies.response_delta` are `NULL`, the system returns metadata-only with a warning. Root-cause investigation of stream capture gaps is a separate telemetry pipeline task.

❌ **Real-time notification of body availability:** The metadata-only warning is static text; no webhook/polling mechanism for delayed body writes.

❌ **Full-screen UI overhaul:** See `docs/superpowers/plans/2026-08-26-request-detail-fullscreen.md` (separate feature, not yet implemented).

### Future Enhancements

**P2 - Waterfall Integration:**
- `GET /api/admin/dispatch/waterfall/request/{id}` (planned in fullscreen plan)
- Embed T0-T9 stages + routing attempts in unified detail response

**P2 - Extended Metadata Fields:**
- `vendor`, `credential`, `tokens`, `cost`, `finish_reason` (available in request_logs, not yet exposed)

**P3 - Trace Summary:**
- `GET /api/admin/requests/{id}/trace?summary=1` (deferred)

---

## Deployment Requirements

### Environment Configuration

```bash
# Optional: override default in-flight store directory
LLM_GATEWAY_REQUEST_DETAIL_DIR=/custom/path

# Default: /tmp/llmgw-request-detail
```

### Database Prerequisites

**Required views:**
- `request_logs_with_current_month` (metadata fallback to partitions)
- `request_logs_bodies_with_current_month` (body fallback)
- `session_turns_with_current_month` (session fallback)

**Verification:**
```sql
SELECT viewname FROM pg_views WHERE viewname IN (
  'request_logs_with_current_month',
  'request_logs_bodies_with_current_month',
  'session_turns_with_current_month'
);
-- Must return all 3 rows
```

### Wiring (cmd/gateway/main.go)

```go
// main.go:2479-2492 (upstream commit d542a2caa, 2026-08-25)
detailDir := strings.TrimSpace(os.Getenv("LLM_GATEWAY_REQUEST_DETAIL_DIR"))
if detailDir == "" {
    detailDir = filepath.Join(os.TempDir(), "llmgw-request-detail")
}
if detailStore, err := requestdetail.NewStore(detailDir); err != nil {
    slog.Warn("request detail content store disabled", "dir", detailDir, "error", err)
} else {
    requestdetail.SetGlobal(detailStore)           // ← global capture hook
    adminHandler.SetRequestDetailStore(detailStore) // ← API handler injection
    slog.Info("request detail content store wired", "dir", detailDir)
}
```

**Critical:** Both `SetGlobal` and `SetRequestDetailStore` must be called. Missing `SetGlobal` → capture never writes; missing `SetRequestDetailStore` → 503 on API calls.

---

## Change History

| Date | Commit | Description |
|------|--------|-------------|
| 2026-08-25 | d542a2caa | Initial unified detail store wiring in main.go |
| 2026-08-26 | (P1-29) | Tenant isolation extended to in-flight sources |
| 2026-08-27 | 653e1990f | Query cascade optimization (OR → 4-step fallback) |
| 2026-08-27 | | Dedupe redundant wiring, fix sqlmock tests |
| 2026-08-28 | cf9c966e8 | Partial body fallback (fill missing fields from session_turns) |
| 2026-08-28 | 03a762b19 | Tenant scope context + approval config fixes |

---

## References

### Related Documents

- **Audit:** `docs/audits/request-detail-二次审计-20260827.md` (二次审计通过)
- **Audit:** `docs/audits/request-detail-body-fallback-20260828.md` (partial body fallback verification)
- **Fix Log:** `docs/fix-request-detail-503-2026-08-27.md` (503 root cause + query optimization)
- **Future Plan:** `docs/superpowers/plans/2026-08-26-request-detail-fullscreen.md` (全屏化计划)

### Key Files

**Backend:**
- `admin/unified_detail.go` - HTTP handler + DB reader
- `domains/requestdetail/locator.go` - Multi-source resolution
- `domains/requestdetail/store.go` - In-flight memory + file store
- `cmd/gateway/main.go:2479-2492` - Wiring block

**Tests:**
- `admin/unified_detail_test.go` - Handler integration tests
- `domains/requestdetail/store_test.go` - Store + locator unit tests

---

## Conclusion

The unified request detail enhancement is **complete and production-verified** as of 2026-08-28. All stated goals have been achieved:

✅ Multi-layer content store with graceful fallback  
✅ Query optimization (cascading lookups)  
✅ Partial body recovery from session_turns  
✅ Tenant isolation across all sources  
✅ Comprehensive test coverage  
✅ Production deployment verified (245)

The implementation is ready for broader rollout. Future enhancements (fullscreen UI, waterfall integration) are documented separately and do not block current functionality.

---

**Approval Status:** ✅ Implementation Complete  
**Production Status:** ✅ Deployed to 245 (llmgo.kxpms.cn)  
**Test Status:** ✅ All unit tests passing  
**Documentation Status:** ✅ This executive summary + audit trails
