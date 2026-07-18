# Realtime Probe Queue

## Summary

Added the first implementation slice for realtime credential failover and a
durable self-check queue.

## Changes

- Classifies upstream auth, quota, rate-limit, overload, and transient errors
  into failover scope and probe policy.
- Adds `credential_probe_queue` with 300-second TTL, deduplication, leases, and
  PostgreSQL `SKIP LOCKED` claims.
- Adds configurable probe workers and multi-round retry scheduling.
- Reuses `ActiveProbeExecutor.RunCommand` for queued and direct probes.
- Keeps the durable queue behind `LLM_GATEWAY_PROBE_QUEUE_ENABLED` and does not
  enable the deprecated unified scheduler.

## Verification

- `go test ./errorsx ./bg ./cmd/gateway -count=1`
- `go test -race ./errorsx ./bg -count=1`
- `go build ./cmd/gateway/`
- `go vet ./errorsx ./bg ./cmd/gateway`
- PostgreSQL migration up/down on local PG17
