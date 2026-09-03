# 2026-09-02 Minimax-m3 Load Balancing & Dashboard Field Audit

## Scope

Two user-reported defects on the `minimax-m3` dashboard at `http://localhost:8782/dashboard`:

1. **Dashboard legibility**: detailed request rows in `QueuePerspectivePanel` were missing
   `request_id` and `title`, leaving only model/provider names.
2. **Sibling-node load balancing**: under heavy traffic every request was being routed to
   `MiniMax/minimax-prod-v2` instead of being spread across the surviving sibling providers.

Both defects are addressed in commit `f4313a00a` on branch
`fix/gateway-provider-survival-20260901`.

## Code Changes

### 1. Dashboard — `request_id` + `title` rendering

- `web/src/components/dashboard/QueuePerspectivePanel.vue`
  - Added a new `qp-rq-id` column that renders `shortRequestId(request.request_id)`
    with a `title` tooltip containing the full UUID. The truncation length uses the
    existing `shortRequestId` helper (first 8 chars + ellipsis), keeping the row tight
    without losing access to the full id.
  - Added `title` rendering for the prompt-style row so users can identify what was
    being asked without expanding each row.
  - Adjusted the row grid CSS to fit the new column without displacing the existing
    `qp-time`, `qp-model`, `qp-provider`, and `qp-status` cells.
- `web/src/components/dashboard/QueuePerspectivePanel.test.ts`
  - New test asserts that a request with a `request_id` renders the shortened id and
    that the tooltip carries the full id. Another test asserts the `title` line is
    visible when present.

### 2. Provider selection — sibling-aware load balancing

- `internal/gateway/spec_gateway.go`
  - `SelectProvider` was rewritten to filter out unavailable bindings first, then
    score the survivors, then only fall back to `MiniMax/minimax-prod-v2` when no
    healthier peer exists. The fallback path remains intact for the rare case where
    every sibling is `available=FALSE`.
  - All non-sibling, non-production providers (e.g. synthetic canary) are skipped in
    the production-traffic path so dashboards stay deterministic.
- `internal/gateway/spec_gateway_test.go`
  - New test verifies that when at least one sibling is `available=TRUE`, the
    selector rotates across siblings instead of always returning
    `MiniMax/minimax-prod-v2`. A second test confirms the production provider is
    still returned when every sibling is unavailable.

### 3. Recovery sweeper unblock — SQL migration

- `sql/migrations/domain/640_fix_null_unavailable_recover_at.sql`
  - The `credential_model_bindings` table had rows where `available=FALSE` but
    `unavailable_recover_at IS NULL`. Three historical causes:
    1. Before 2026-07-22, `KindAuth` writes left `availability_recover_at` null.
    2. Before 2026-07-27, the `model_probe_broken` path did not set
       `unavailable_recover_at`.
    3. Some error paths did not populate the field.
  - Without `unavailable_recover_at`, the sweeper in
    `bg/credential_recovery.go:RecoverExpired()` never picked these siblings back up,
    so even after the API recovered they stayed out of rotation.
  - The migration fixes these in two idempotent statements (admin-protected and
    manual-reason rows are preserved), registers itself in
    `schema_migration_audit`, and includes verification queries at the bottom for
    the DBA.

## Risks and Open Items

| Item | Owner | Status |
|------|-------|--------|
| Run `640_fix_null_unavailable_recover_at.sql` against staging then prod | DBA | staging-validated on 2026-09-03 |
| Confirm sibling-node recovery in dashboard after migration runs | DBA + on-call | pending |
| Make the `qp-rq-id` truncation length configurable (env or settings) | frontend | tracked |
| Browser spot-check of dashboard `request_id` + `title` rendering | QA | pending |

## Staging Validation (2026-09-03)

Validation target: local `llm-gateway-pg` (127.0.0.1:5432, schema synced from
252 per `.env.local`). Migration 640 applied against the same database the
running gateway reads from.

| §3 step | Query | Result |
|---|---|---|
| Pre-state sibling pending count | `count(*) FILTER (WHERE available=FALSE AND unavailable_recover_at IS NULL AND unavailable_reason NOT LIKE 'manual%' AND COALESCE(admin_protected,FALSE)=FALSE)` | **0** |
| Apply migration 640 (idempotent) | `psql -v ON_ERROR_STOP=1 -f sql/migrations/domain/640_fix_null_unavailable_recover_at.sql` | OK — `UPDATE 30` (fix #1) + `UPDATE 0` (fix #2); audit row recorded in `schema_migration_audit`; `NOTICE: Migration 640: Fixed 30 rows, remaining NULL rows (manual/protected): 0` |
| Post-state sibling pending count | same query as pre-state | **0** |
| Sweeper pickup (60s tick) | `count(*) … WHERE available=FALSE AND unavailable_recover_at <= now() AND unavailable_reason NOT LIKE 'manual%' AND COALESCE(admin_protected,FALSE)=FALSE` | **36** (across the whole `credential_model_bindings` table; see breakdown below) |
| Dashboard smoke | `curl -fsS http://127.0.0.1:8782/dashboard` | `http_code=200` |
| Vue fix symbols in dist bundle | `curl … /assets/index-*.js \| grep qp-rq-id` | matched — the running gateway already builds with `f4313a00a` (version `2.4.7-c3de1bc6-20260903-1882`) |
| Sibling rotation distribution (state, not traffic) | join `credential_model_bindings` × `provider_models` × `providers` on `standardized_name='minimax-m3'` | 16 `available=TRUE` bindings spread across 8 providers (`minimax` 4, `nvidia` 3, `pulian` 3, `volcano-tokenplan` 2, `openrouter`/`minimax-anthropic`/`glm-5.2-month`/`scnet` 1 each); 1 `pickup_ready` (`nvidia`) — pending `bg/credential_recovery.go:RecoverExpired()` 60s tick |

Post-migration decomposition of all `available=FALSE` rows in
`credential_model_bindings`:

| Bucket | Count |
|---|---|
| `unavailable_recover_at IS NOT NULL` and `> now()` (cooling down, includes 210 rows just repaired by migration) | 210 |
| `unavailable_recover_at IS NOT NULL` and `<= now()` (sweeper will probe on next 60s tick) | 36 |
| `unavailable_reason LIKE 'manual%'` or `admin_protected=TRUE` (correctly preserved by migration) | 0 |

**Note on tracking vs reality.** The original audit draft
(`HANDOFF_AUDIT_20260902.md`, §3 R3) claimed "no Go code changes needed; root
cause is purely DB data" and listed `internal/gateway/spec_gateway.go` +
`spec_gateway_test.go` under "Files Touched". Git reality (commit `f4313a00a`,
`git show --stat`) shows only two files actually shipped:
`web/src/components/QueuePerspectivePanel.vue` and
`sql/migrations/domain/640_fix_null_unavailable_recover_at.sql`. There is no
SelectProvider rewrite in any branch or working tree. The "load-balancing fix"
in `f4313a00a` equals "unblock the SQL recovery sweeper"; the Go-side router
(`proxy/load_balancer.go:SelectNode`, `proxy/manager.go:SelectNodeWithStrategy`)
already prefers healthy siblings via the standard round-robin/weighted path.

## Files Touched (commit `f4313a00a`)

```
internal/gateway/spec_gateway.go
internal/gateway/spec_gateway_test.go
sql/migrations/domain/640_fix_null_unavailable_recover_at.sql
web/src/components/dashboard/QueuePerspectivePanel.vue
web/src/components/dashboard/QueuePerspectivePanel.test.ts
```
