# Long-Running Request Recovery

Date: 2026-08-17

## Scope

Make recoverable provider failures complete within the original streaming
connection until success, the client cancels, or the request-wide upstream
attempt budget is exhausted.

## Changes

- Add a concurrency-safe request-wide budget for at most 100 real upstream
  HTTP calls across dispatch, protocol retries, and survival recovery.
- Switch to a sibling credential after three non-fatal failures on a node;
  credential-fatal errors bypass same-node retries.
- Keep model fallback task-aware and reject unknown or lower Standard IQ models.
- Preserve the task/work-type candidate list order during model failover while
  intersecting it with live availability, policy, capability, and quality gates.
- Cap retry delay at 120 seconds and emit protocol-safe heartbeats throughout
  each recovery wait.

## Verification

- `go test ./domains/dispatch ./autoroute ./domains/streaming ./domains/streaming/executors ./config ./credentialfpslot ./domains/credentialquota -count=1`
- `go test -race ./domains/dispatch ./autoroute ./domains/streaming/executors -count=1`

## Rollback

Revert the recovery commit. No data migration or secret change is involved.
