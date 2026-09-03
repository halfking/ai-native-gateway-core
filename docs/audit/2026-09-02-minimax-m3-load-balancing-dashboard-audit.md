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
| Run `640_fix_null_unavailable_recover_at.sql` against staging then prod | DBA | pending |
| Confirm sibling-node recovery in dashboard after migration runs | DBA + on-call | pending |
| Make the `qp-rq-id` truncation length configurable (env or settings) | frontend | tracked |
| Browser spot-check of dashboard `request_id` + `title` rendering | QA | pending |

## Files Touched (commit `f4313a00a`)

```
internal/gateway/spec_gateway.go
internal/gateway/spec_gateway_test.go
sql/migrations/domain/640_fix_null_unavailable_recover_at.sql
web/src/components/dashboard/QueuePerspectivePanel.vue
web/src/components/dashboard/QueuePerspectivePanel.test.ts
```
