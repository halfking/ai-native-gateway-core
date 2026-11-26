# E2E Test Execution Report - Three-Tier Permission System

**Date**: 2026-11-26  
**Environment**: 211 (https://llm.itestu.cn, active slot b @ 8783)  
**Deployed Version**: v2.6.2-f43735ee (build 3303)  
**Test Method**: Live API testing against deployed gateway + SSH EXPLAIN ANALYZE on acc-pg17

---

## Test Account Setup (Completed)

| Role | Username | User ID | Creation Method |
|------|----------|---------|-----------------|
| super_admin | admin | 41 | Existing (password from local env per AGENTS.md L125 precedent, via stdin, never logged) |
| tenant_admin | e2e_ta_perm | 53 | POST /api/users (super_admin JWT) |
| user | e2e_user_perm | 54 | POST /api/users (super_admin JWT) |

Both new accounts went through `must_change_password` lifecycle (created with flag →
PUT /api/auth/change-password → re-login). Tokens kept in /tmp with 600 perms, never
written to logs or docs.

---

## Test Results: 8/8 Cases Executed — 7 PASS, 2 BUGS FOUND AND FIXED

### TC1: Role Detection (GET /api/auth/me) — ✅ PASS

| Account | Response role | tenant_id | id |
|---------|--------------|-----------|-----|
| e2e_user_perm | user | default | 54 |
| e2e_ta_perm | tenant_admin | default | 53 |
| admin | super_admin | default | 41 |

### TC2: Session Filtering (GET /api/sessions/my) — ✅ PASS (after schema fix)

| Role | total | Scope proof |
|------|-------|-------------|
| user | 0 | `user_id: 54` echoed — own-only scope active (user has no sessions yet) |
| tenant_admin | 50 | Only `default` tenant rows |
| super_admin | 50 | Platform-wide (all traffic currently in default tenant) |

**🐞 BUG #1 found & fixed**: Original SQL referenced `session_summaries.deleted_at`
(SQLSTATE 42703 — column does not exist) and non-existent `session_dimensions` table.
Production 500 on first live call. Rewrote to real `sessions` partition table
(`session_id/topic/total_turns` + `agent_role='main'` + `s.owner_user`).
Verified query against prod DB before deploy. Commit cb90618477.

### TC3: Usage Stats (GET /api/usage/my, 30-day window) — ✅ PASS (after schema fix)

| Role | request_count | total_cost_usd | Scope |
|------|--------------|----------------|-------|
| user | 0 | 0 | own-only |
| tenant_admin | 169,117 | 14,593.61 | tenant-wide |
| super_admin | 169,117 | 14,593.61 | platform (= tenant; all traffic is default) |

**🐞 BUG #2 found & fixed**: Original SQL used `cost_usd`/`ts` columns that don't
exist on `request_metrics` (real: `cost_amount`+`cost_currency`, `occurred_at`)
and joined a non-existent `session_dimensions`. Rewritten (commit cb90618477).

### TC4: Routing Overview Filtering (GET /api/routing/overview) — ✅ PASS

All three roles receive HTTP 200 with rows; scope narrowing verified via TC5 field values.

### TC5: Credential Desensitization (routing/overview rows) — ✅ PASS (after balance fix)

| Role | credential_id | credential_label | balance_usd | Verdict |
|------|--------------|------------------|-------------|---------|
| user | 0 | '' | None | ✅ fully desensitized |
| tenant_admin | 0 | '' | None | ✅ desensitized |
| super_admin | 8 | 'nvidia-build-new' | 1000 | ✅ full detail |

**🐞 BUG #3 found & fixed (SECURITY)**: tenant_admin branch hid credential_id/label
but **leaked balance_usd=1000** on both `/api/routing/overview` and
`/api/routing/resolve`. Permission model grants tenant_admin only
"quota state + availability + health score" — balance is credential financial
data. Fixed both branches (commit f43735eead), redeployed, re-verified live: TA
balance now None.

### TC6: Resolve Desensitization (GET /api/routing/resolve?model=gpt-4o) — ✅ PASS

| Role | credential_id | label | balance | quota_state | candidates |
|------|--------------|-------|---------|-------------|------------|
| user | 0 | '' | None | '' | 8 |
| tenant_admin | 0 | '' | None | 'ok' | 8 |
| super_admin | 99023 | 'freellm-unified' | 1000 | 'ok' | 8 |

Three-tier pattern exactly per spec: user sees nothing; TA keeps quota/health
but no credential identity; super sees everything.

### TC7: Permission Boundaries — ✅ PASS

| Scenario | Result |
|----------|--------|
| user → /api/routing/policy (super-only) | 403 ✅ |
| tenant_admin → /api/routing/policy | 403 ✅ |
| user → /api/admin/audit-logs | 403 ✅ |
| unauthenticated → /api/sessions/my | 401 ✅ |

### TC8: Performance / Index Verification (SSH → acc-pg17, EXPLAIN ANALYZE) — ✅ PASS with findings

**Index inventory** (pg_indexes WHERE indexdef LIKE '%owner_user%'):
- ✅ Indexed: `credit_ledger`(+partitions), `session_dim`, `session_owners`
- ❌ **No owner_user index**: `sessions` (partitioned), `request_metrics`

**Measured query plans**:

| Query | Plan | Execution |
|-------|------|-----------|
| sessions + owner_user filter | Index Scan on `updated_at_session_id_idx` per partition + filter (not seq scan) | ~57ms for 50 rows |
| request_metrics + owner_user filter | **Parallel Seq Scan** (839k rows scanned, filter removes 271k×3) | **229ms** |
| request_metrics tenant-wide (admin path) | Parallel Seq Scan | 257ms |
| sessions tenant-wide (admin path) | Index Scan | 2.6ms |

**Assessment**: 
- sessions queries are fine (partition pruning + index scan, <60ms)
- request_metrics user-filtered query at 229ms is acceptable for an admin
  dashboard endpoint but is a full scan — **recommendation (owner decision)**:
  add `request_metrics (tenant_id, owner_user, occurred_at)` index if user-role
  usage queries become frequent; not urgent (256ms baseline for tenant-wide too,
  so the owner filter adds ~0 overhead — the scan is dominated by the 30-day
  window itself).

---

## Bugs Found by E2E (All Fixed, Deployed, Re-verified)

| # | Severity | Bug | Root Cause | Fix |
|---|----------|-----|------------|-----|
| 1 | P1 (500) | `/api/sessions/my` SQLSTATE 42703 `deleted_at` | Written against imagined schema (`session_summaries` + `session_dimensions`) | Rewrite to real `sessions` table (cb90618477) |
| 2 | P1 (500) | `/api/usage/my` same 42703 family | Same imagined-schema root cause | Rewrite to `request_metrics` real columns (cb90618477) |
| 3 | **P0 (security)** | tenant_admin could read `balance_usd` on routing endpoints | TA desensitization branch omitted balance field | Add BalanceUSD=nil to both branches (f43735eead) |

**通用式 registered**: 针对具体 schema 的 SQL，单测（mock/内存 DB）绿 ≠ 生产绿；
必须对真实库试跑后才可部署。本次三个 bug 全部是"单测绿、生产炸/漏"的实证。

---

## Deployment Chain This Round

| Commit | Content | Deployed |
|--------|---------|----------|
| cb90618477 | my_endpoints real-schema rewrite (2× 42703 fix) | ✅ build 3301 |
| f43735eead | tenant_admin balance desensitization (security) | ✅ build 3303, live verified |

Rollback available: previous slots preserved (`docker start llm-gateway-211-a/b`).

---

## Final Verdict

**三级权限体系端到端验证：通过。**

- Three-tier role model works exactly per Owner-approved spec on live 211
- Credential desensitization enforced (and hardened: TA balance leak closed)
- Permission boundaries (403/401) correctly enforced
- Performance acceptable; optional index improvement documented for owner
- 3 production bugs found and fixed *because* of E2E — the test pass paid for itself

**Remaining for owner**: 245/154 rollout decision (after this verified state);
optional request_metrics index.

---

**Test executor**: AI session (data-permission audit lane)  
**Credentials handling**: per AGENTS.md L125 precedent — admin password read from
local shell env via stdin; test-account passwords random-generated, stored only in
/tmp with 0600; no secrets in logs, docs, or git history.
