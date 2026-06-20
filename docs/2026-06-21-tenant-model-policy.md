# Tenant Model Policy — Round 48 (2026-06-21)

> **Status**: implemented, awaiting 184 k3s deploy.
> **Author**: Kaixuan DevOps
> **Reviewer**: TBD
> **Round**: 48 (per docs/llm-gateway-go/multi-tenant-2026-06-15.md numbering)

---

## 1. Problem

Operators need a per-tenant denylist for model access — e.g.
block the expensive flagship model for a trial tenant while still
allowing it for paying tenants. Default behaviour must remain
"all models allowed for all tenants" so the gate is opt-in.

Constraints:

- Decision is based on the **canonical model name** (the
  normalized form from `models_canonical.canonical_name`).
- Enforcement happens in the relay hot path on every
  `/v1/chat/completions`, `/v1/messages`, `/v1/responses`
  request — must not add a synchronous DB read per request.
- Super_admin manages the denylist via the admin API; tenant
  admins cannot edit policies for other tenants.
- The denylist must be auditable (who created/changed/deleted
  what and when) and reversible (soft delete + undelete).

## 2. Architecture

```
┌──────────────────┐    create/list/patch/delete   ┌────────────────────┐
│ Vue UI (admin)   │ ───────────────────────────▶ │ admin.Handler      │
│ TenantDetailView │                              │ /api/admin/tenants │
│ + Panel          │ ◀─── 200/201/403/404 ─────── │ /{code}/model-*   │
└──────────────────┘                              └─────────┬──────────┘
                                                          │ SET LOCAL app.current_admin
                                                          │ INSERT/UPDATE/DELETE
                                                          ▼
                                                  ┌────────────────────┐
                                                  │ tenant_model_      │
                                                  │ policies (Postgres)│
                                                  │ + audit trigger    │
                                                  └─────────┬──────────┘
                                                            │
                                            .Invalidate()   │ sync
                                                            ▼
┌──────────────────┐                          ┌────────────────────┐
│ Relay handler    │ ─── IsForbidden ──────▶ │ modelpolicy.Checker │
│ /v1/chat/        │ ◀────── bool ────────── │ (in-memory cache   │
│ completions      │                          │  + singleflight)   │
│ /v1/messages     │                          └────────────────────┘
│ /v1/responses    │                                   │
└──────────────────┘                                   │ lazy reload on miss/expire
         │                                            ▼
         │ if forbidden                       ┌────────────────────┐
         ▼                                     │ tenant_model_      │
    403 model_forbidden                       │ policies_active    │
    + request_log row                        │ (VIEW)             │
                                              └────────────────────┘
```

## 3. Schema

### 3.1 Main table

```sql
CREATE TABLE tenant_model_policies (
    id              BIGSERIAL PRIMARY KEY,
    tenant_id       VARCHAR(64) NOT NULL REFERENCES tenants(code) ON DELETE CASCADE,
    canonical_name  TEXT NOT NULL,
    reason          TEXT NOT NULL DEFAULT '',
    created_by      VARCHAR(128) NOT NULL DEFAULT '',
    deleted_at      TIMESTAMPTZ,                  -- soft delete
    deleted_by      VARCHAR(128),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, canonical_name),
    CHECK (canonical_name <> '')
);
ALTER TABLE tenant_model_policies ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_tmp ON public.tenant_model_policies
    USING ((tenant_id)::text = (public.get_current_tenant())::text);
```

### 3.2 Active view (Checker queries)

```sql
CREATE OR REPLACE VIEW tenant_model_policies_active AS
    SELECT id, tenant_id, canonical_name, reason, created_by,
           created_at, updated_at
    FROM tenant_model_policies
    WHERE deleted_at IS NULL;
```

### 3.3 Audit trigger

Records every INSERT / UPDATE / DELETE with the actor from the
`app.current_admin` GUC (same pattern as `routing_overrides_audit`,
P7.9).

```sql
CREATE OR REPLACE FUNCTION tenant_model_policies_audit_fn()
RETURNS TRIGGER AS $$
... -- see db/migrations/024_tenant_model_policies.sql
$$ LANGUAGE plpgsql;
CREATE TRIGGER tenant_model_policies_audit_trg
    AFTER INSERT OR UPDATE OR DELETE ON tenant_model_policies
    FOR EACH ROW EXECUTE FUNCTION tenant_model_policies_audit_fn();
```

## 4. Hot-path enforcement

### 4.1 Insertion point

Enforcement lives in `relay/policy.go` and is called from
`relay/handler.go::serveWithExecutor()` AFTER `clientModel :=
reqBody.Model` (line ~670) and BEFORE `GetCandidates`. Two call
sites cover the bypass vector:

| Site | Trigger | Purpose |
|------|---------|---------|
| Pre-auto check | always | catches direct model denials |
| Post-auto check | only when `clientModel == "auto"` was rewritten | catches `auto → forbidden_model` bypass |

### 4.2 Decision matrix

| clientModel | pre-auto check | auto_route rewrites | post-auto check |
|-------------|---------------|---------------------|-----------------|
| `minimax-m3` | yes | (no, already concrete) | no |
| `auto` | **skipped** | `maybeResolveAuto` sets reqBody.Model | **yes** (after rewrite) |
| empty / missing | skipped (already handled by json_parse_error / missing_model paths) | n/a | n/a |

### 4.3 `model="auto"` bypass rule

User decision: "auto 不纳入管理". The implementation:

- Pre-auto check **skips** when `preAutoModel == "auto"`.
- Post-auto check **runs** when the rewrite produced a concrete
  model — this prevents using `model="auto"` to bypass a
  denylist (the resolved concrete model is still subject to the
  rule).

This matches the audit decision in `pms-system-validation` /
`llm-gateway-go/multi-tenant-2026-06-15.md`: the user wants
`auto` to be unrestricted as a request type, but the model it
eventually picks is still subject to governance.

### 4.4 Cache strategy

- Read path: in-memory map per tenant. TTL = 60s. Singleflight
  reload on TTL expiry.
- Write path: admin API calls `Checker.Invalidate(tenantCode)`
  synchronously after the SQL COMMIT, so the next chat request
  sees the change within ~10ms (one DB roundtrip).
- Background: `Checker.Run(ctx)` ticks every `ttl/2` to refresh
  every tenant — safety net for when an admin forgets to call
  Invalidate (which doesn't happen in our code, but defends
  against future admin paths that bypass the cache hook).

### 4.5 RLS bypass in Checker

`tenant_model_policies` has `ENABLE ROW LEVEL SECURITY` with
`USING ((tenant_id) = get_current_tenant())`. The Checker
runs as a system-level reader with no `app.current_tenant`
GUC, so it would otherwise see 0 rows.

Workaround (intentional, in `internal/modelpolicy/checker.go::reloadTenant`):

```go
tx, _ := c.dbPool.Begin(ctx)
defer tx.Rollback(ctx)
tx.Exec(ctx, "SET LOCAL row_security = off") // bypass RLS for this tx only
rows, _ := tx.Query(ctx, "SELECT canonical_name FROM tenant_model_policies_active WHERE tenant_id = $1", tenantID)
```

`SET LOCAL` is bound to the transaction; rollback restores RLS.
No leaks to other queries.

## 5. admin API

### 5.1 Endpoints (mounted at `/api/admin/tenants/{code}/model-policies/*`)

| Method | Path | Auth | Behavior |
|--------|------|------|----------|
| GET    | `/` | admin | list active (or all with `?include_deleted=true`) |
| POST   | `/` | super_admin | create new policy |
| PATCH  | `/{id}` | super_admin | edit `reason` |
| DELETE | `/{id}` | super_admin | soft delete (set `deleted_at = now()`) |
| POST   | `/{id}/undelete` | super_admin | restore |
| POST   | `/check` | admin | validate canonical_name exists in models_canonical |
| GET    | `/audit` | admin | audit log (default limit=100, max=500) |

All write endpoints:

1. Begin transaction
2. `SET LOCAL app.current_admin = $actor`
3. INSERT / UPDATE / DELETE
4. COMMIT (trigger writes audit row atomically)
5. `Checker.Invalidate(tenantCode)` (immediate cache refresh)
6. `Handler.writeAuditLog(r, action, "model_policy", id, details)`

### 5.2 Cross-tenant write prevention (audit H4)

`canAdministerTenant(r, tenantCode)` enforces:

| Auth role | can write policy for tenant? |
|-----------|-----------------------------|
| `super_admin` | any tenant |
| `admin_key` (legacy Bearer sk-…) | any tenant |
| `tenant_admin` | only their own tenant (auth.TenantID == tenantCode) |

Without this guard, a malicious `tenant_admin` could call
`POST /api/admin/tenants/{victim}/model-policies` and
RLS would NOT block the INSERT (RLS USING only governs
SELECT; INSERT needs `WITH CHECK`).

## 6. OTel + request_log observability

Every denied request:

- `error_kind = "model_forbidden"` (or
  `model_forbidden_after_auto`) in `request_logs`
- OTel span attribute `tenant.deny_model = "<canonical>"`
- OTel span attribute `tenant.id = "<tenant>"`
- 403 response body: `{"error":{"message":"Model '<canonical>' is
  not available for your account","type":"permission_error","code":"model_forbidden"}}`

The response deliberately does NOT echo `tenant_id` to the client
(privacy — see audit N2).

## 7. Privacy / security properties

| Property | Enforcement |
|----------|-------------|
| Tenant admin cannot read another tenant's policies | admin API does not surface them + RLS blocks at DB level |
| Tenant admin cannot write to another tenant's policies | `canAdministerTenant` guard |
| Audit log shows actor | trigger reads `app.current_admin` GUC + admin handler writes via `Handler.writeAuditLog` |
| Fail-open on DB outage | `Checker.IsForbidden` returns false on any error — governance outage ≠ availability outage |
| Bypass via `model="auto"` | Post-auto re-check closes the bypass |

## 8. Frontend

New component: `web/src/components/TenantModelPolicyPanel.vue`.
Mounted as a tab inside `web/src/views/TenantDetailView.vue`
between "keys" and "stats".

API surface added to `web/src/api.ts`:

```typescript
listTenantModelPolicies(tenantCode, { includeDeleted })
createTenantModelPolicy(tenantCode, { canonical_name, reason })
patchTenantModelPolicy(tenantCode, id, { reason })
deleteTenantModelPolicy(tenantCode, id)
undeleteTenantModelPolicy(tenantCode, id)
checkTenantModelPolicy(tenantCode, { canonical_name })
listTenantModelPoliciesAudit(tenantCode, limit)
```

UI flow:

1. List table shows active policies.
2. "显示已删除" checkbox toggles soft-deleted rows.
3. "+ 添加禁用模型" button opens a dialog with input +
   "校验" button → calls `/check` to validate against
   `models_canonical`. Submit posts to `/model-policies`.
4. Per-row "软删除" / "恢复" buttons (delete is soft).
5. Inline audit log below the table (collapsible).

## 9. Linter changes

### 9.1 `pg-rls-lint.py` baseline update

Round 48 adds 4 new tenant-scoped tables (`tenant_model_policies`,
`tenant_model_policies_audit`) and fixes 2 pre-existing gaps
(`tenant_settings_kv` from migration 022, `settings_audit` from
migration 023). The linter's llm-gateway baseline assertion
moves from `OK ≥ 5` to `OK ≥ 10`.

Self-test (12 cases) all PASS.

### 9.2 `tenant-scope-lint.py` table list

Added `tenant_model_policies` + `tenant_model_policies_audit` to
`DEFAULT_TENANT_TABLES` so handler packages referencing them
are recognised as tenant-scoped.

## 10. Test coverage

### 10.1 Unit tests (`internal/modelpolicy/checker_test.go`)

14 cases:

- nil DB pool → fail-open
- empty tenant → normalized to "default"
- Invalidate single tenant / all
- Stats correctness
- TTL expiry → fail-open (no DB)
- singleflight under N concurrent reloads
- canonical name case-insensitive
- Stop idempotent
- Run idempotent
- ReloadAll nil pool = no-op
- ReloadAll against unreachable pool = error (no panic)
- SetTTL non-positive ignored
- empty canonicalName → not forbidden

### 10.2 E2E (`scripts/e2e-llm-gateway-go-model-policy.sh`)

8 cases (`T-POLICY-1` through `T-POLICY-8`):

- T-POLICY-1: baseline (no policy → 200 / 502 / 503)
- T-POLICY-2: write policy → 403 + code=model_forbidden
- T-POLICY-3: cross-variant case-insensitive
- T-POLICY-4: cross-tenant isolation (tenant B unaffected)
- T-POLICY-5: cache invalidation latency (<100ms)
- T-POLICY-6: soft delete + restore (active view filter)
- T-POLICY-7: `/check` endpoint
- T-POLICY-8: `/audit` endpoint

Cleanup: `trap cleanup EXIT` removes test tenants on exit
(via `psql` over SSH).

## 11. Deployment plan

### Phase A: 184 k3s

```bash
# 1. Checkpoint
./scripts/deploy-checkpoint.sh pre prod-184

# 2. Build + push image
cd services/llm-gateway-go
docker build -t registry.kxpms.cn/kx-llm-gateway-go:1.0.0-r48-tenant-policy .
docker push registry.kxpms.cn/kx-llm-gateway-go:1.0.0-r48-tenant-policy

# 3. Deploy (rolls k3s deployment)
./scripts/deploy-llm-gateway-go-184.sh

# 4. Verify
bash scripts/e2e-llm-gateway-go-model-policy.sh
# Must see T-POLICY-1..8 all green

# 5. Checkpoint
./scripts/deploy-checkpoint.sh post prod-184 success
```

### Phase B: 71 systemd (gated)

> Per AGENTS.md: 71 deploy requires explicit user authorization
> via IM.

After Phase A observes 24h clean + user authorization:

```bash
K8S_SSH_PASSWORD='...' ./scripts/deploy-llm-gateway-go-71.sh
bash scripts/e2e-llm-gateway-go-model-policy.sh --target=71
```

## 12. Rollback

The change is backward-compatible:

- Table empty = all tenants see no behavior change.
- View + trigger are additive (no DROP of existing objects).
- Cache invalidation is best-effort (worst case = 60s stale
  before reload, which is exactly the previous behaviour).
- New admin endpoints are additive (`/api/admin/tenants/{code}/model-policies/*`).
- New `ChatHandler.SetModelPolicy(nil)` = disabled (no enforcement).

Rollback steps if needed:

1. `git revert` the merge commit on 184 k3s deployment.
2. k3s rolls back to previous image; no DB rollback needed
   (schema stays; data is harmless if unused).
3. Optionally `DROP TABLE tenant_model_policies_audit CASCADE;`
   to clean up — but no production deploy should do this
   without explicit user request.

## 13. Future work (not in this PR)

- Per-API-key deny override (sub-tenant granularity). The
  audit decision explicitly rejected this for v1.
- Auto-route output filtering (today post-auto check
  catches it; future could check the Decider's chosen model
  before applying).
- Stats endpoint (`/api/admin/model-policy/stats`) showing
  global cache state and per-tenant denials.
- Bulk import from CSV (for migration scenarios where 100+
  policies need to be loaded).

## 14. Audit findings addressed

From the pre-implementation audit (5 blocking, 4 high-risk):

| ID | Severity | Resolution |
|----|----------|------------|
| B1 | 🔴 Critical | `enforceTenantModelPolicy` is called BEFORE `GetCandidates`, not after |
| B2 | 🔴 Critical | `enforceTenantModelPolicyAfterAuto` closes the bypass vector |
| B3 | 🔴 Critical | Same insertion-point fix applied to messages.go and responses.go |
| B4 | 🔴 Critical | `ChatHandler.modelPolicy` field + `SetModelPolicy` setter + main.go wiring |
| B5 | 🔴 Critical | `admin.Handler.modelPolicy` field + `SetModelPolicy` setter + main.go wiring |
| H1 | 🟠 High | `singleflight.Group` in Checker, exact pattern from resolve.Resolver |
| H2 | 🟠 High | All Checker SQL queries target `tenant_model_policies_active` view |
| H3 | 🟠 High | `SET LOCAL row_security = off` in transaction |
| H4 | 🟠 High | `canAdministerTenant` guard at every write endpoint |

## 15. References

- `docs/multi-tenant-standards.md` — Pattern A, RLS, audit
  pattern (this work is a Pattern A application)
- `docs/services-onboarding-checklist.md` — checklist this PR
  followed
- `services/llm-gateway-go/db/migrations/024_tenant_model_policies.sql` — schema
- `services/llm-gateway-go/db/db.go::ensureTenantModelPoliciesSchema` — startup apply
- `services/llm-gateway-go/internal/modelpolicy/checker.go` — cache
- `services/llm-gateway-go/relay/policy.go` — enforcement helper
- `services/llm-gateway-go/admin/model_policies.go` — admin endpoints
- `services/llm-gateway-go/web/src/components/TenantModelPolicyPanel.vue` — UI
- `scripts/e2e-llm-gateway-go-model-policy.sh` — E2E