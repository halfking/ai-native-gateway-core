# Main integration: data-closure audit handoff

> Date: 2026-08-30
> Repository: `services/llm-gateway-go`
> Integration branch: `integration/main-audit-20260830`
> Baseline: `origin/main` at `ec94734a4`
> Local conclusion: **GO_WITH_LIMITATIONS**

## Completed local fixes

1. Added migration 626 rather than editing checksum-frozen migrations 615/625.
   - `promote_session_bodies_hot_to_partition` now uses a transaction advisory lock.
   - A destination `(id, partition_date)` conflict is reconciled by removing the duplicate hot row in the same transaction; old 615 retried it indefinitely.
   - `session_bodies_unified` is explicitly set to `security_invoker=true`.
   - The migration is intentionally non-reversible: its down file does not restore the known data-integrity or RLS weakness.
2. Session V1 fallback summary reads the single hot+partition unified request-log view, pairs bodies with `request_id + ts`, checks `rows.Err()`, and applies `up_to_turn` in SQL.
3. Live Redis request details fail closed for a tenant-scoped caller when the stored record has an empty or mismatched tenant.
4. `StorageRetentionWorker` start/stop is idempotent and safe before start, after cancellation, and on repeated calls.
5. Partial/nil Session V2 cache tiers no longer panic in optional/test configurations.
6. Added the missing common `never` and navigation `proxy` locale entries used by the current frontend.

## Local verification evidence

Passed on the isolated worktree:

```text
go test -count=1 ./admin ./bg ./domains/session/v2 ./domains/requestdetail ./domains/requestjourney ./domains/routing ./domains/transformation ./domains/streaming ./domains/streaming/executors ./sql/migrations/startup
go test -race -count=1 ./admin ./bg ./domains/session/v2 ./domains/requestdetail ./domains/requestjourney ./domains/routing ./domains/transformation ./domains/streaming ./domains/streaming/executors
go vet ./...
go build ./...
go test -mod=vendor -count=1 ./admin ./domains/requestdetail ./sql/migrations/startup
pnpm run typecheck
pnpm test
pnpm run i18n:check
```

Frontend verification used the existing lock-compatible pnpm dependency tree read-only; it was removed from the integration worktree before commit. Vitest passed 96 files / 657 tests. Existing Vue/i18n warnings were non-fatal and pre-existing fallback coverage, not test failures.

## Remaining release gates (manual_required)

Do not declare production release readiness until an authorized isolated PostgreSQL environment proves:

- migration 614 -> 625 -> 626 upgrade plus 626 down behavior;
- `security_invoker=true` and tenant/super-admin RLS reads on the view;
- hot-to-partition promotion with an existing destination conflict, concurrent promoters, rollback on insert failure, and no duplicate/missing rows;
- 10 -> 100 session write/promote/read reconciliation.

Also still required: real Redis cache expiry and tenant-key tests, provider/credential fallback traffic with redacted error capture, Prometheus scrape/alert evidence, attachment lifecycle tests against hot and historical rows, canary/rollback evidence, and browser-level management UI validation.

## Deferred audit findings

- Provider-error aggregation currently needs a real-PostgreSQL proof that a failure promoted before aggregation remains visible to the aggregator watermark path.
- Attachment list/stats/preview/delete semantics must be reconciled across hot and historical request logs. Historical columnar data must remain append-only; cleanup needs an auditable mark/reclaim flow rather than direct historical updates.
- Session aggregate snapshots remain best-effort in some write paths; add a durable retry/outbox before claiming full feedback-closure guarantees.
- `adapter/unified` is not canonical IR. Production protocol work should continue through `internal/ir` and `domains/transformation`; pin Responses SSE extension-loss behavior with explicit compatibility tests before enabling native upstream Responses broadly.

## Continuation prompt

```text
Continue in /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go without overwriting the primary worktree WIP. Start from main after the 2026-08-30 data-closure integration.

First obtain an authorized isolated PostgreSQL target, then validate migrations 614/615/625/626 upgrade/down, RLS under tenant and super_admin roles, hot+partition reads, destination-conflict promotion, concurrent promotion, and 10->100 session reconciliation. Treat migration 626 as non-reversible for correctness; do not edit checksum-frozen migrations.

Next, make provider-error aggregation robust across hot promotion, unify attachment lifecycle reads across hot/history while keeping historical columnar partitions append-only, and provide durable retry/outbox semantics for Session V2 aggregate snapshots. Preserve canonical protocol handling in internal/ir + domains/transformation; do not expand adapter/unified into a second IR.

For every change, add narrow regression tests, run affected Go tests plus race tests, validate frontend typecheck/Vitest/i18n where UI changes, and record exact environment evidence. Never claim release readiness without the real PG/Redis/provider/Prometheus/canary evidence above.
```
