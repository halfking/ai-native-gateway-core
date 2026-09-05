# Node Health Audit Fix

## Summary

The audit found that node cooldown recovery used wall-clock time in Go while
the state transition ran in Redis Lua. This made recovery tests and deployments
with clock skew unreliable. A second issue was that database-backed integration
tests used an implicit local database and failed when that database had a
different schema.

## Changes

- Use Redis `TIME` as the timestamp source for node outcome scripts.
- Restore a node to the routing pool when cooldown expires; the router must be
  able to send a real request before its outcome can be recorded.
- Preserve the post-recovery failure reason when a recovered node fails again.
- Require `TEST_DB_URL` and `TEST_DATABASE_URL` for opt-in database integration
  tests.

## Verification

- `go test ./...`
- `go test -race ./credentialfpslot ./internal/quality ./domains/session/v2`
- `go build ./...`
- `go vet ./...`
- `docker compose config` with required local environment values

## Follow-up Correction

The initial audit interpretation incorrectly kept cooldown-expired nodes out
of routing until a successful request. That creates a deadlock because no
request can succeed if the router never selects the node. The corrected state
machine now auto-recovers on `IsUsable` after cooldown expiry, while the Lua
outcome transition still re-disables the node after the failure threshold.
