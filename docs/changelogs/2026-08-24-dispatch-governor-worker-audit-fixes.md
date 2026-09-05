# Dispatch Governor and Probe Worker Audit Fixes

## Changes

- Wired `redis_enforce` into new concurrency-mode credential forwarders.
- Preserved strict behavior: a Redis governor construction failure produces a fail-closed governor rather than a local fallback.
- Kept RPM and TPM on their existing local token buckets because the Redis lease backend does not yet implement token-cost accounting.
- Normalized an unset credential mode to concurrency for parity with the previous local governor factory.
- Made probe workers explicitly claim one task each; worker count remains the concurrency control and prevents serial processing from losing later task leases.
- Linearized model lane depth updates with their queue observations, preventing stale Redis and SSE depths after a completed request.

## Verification

- Focused governor construction and fail-closed tests.
- Probe worker batch-size contract test.
- `go test ./domains/dispatch ./bg`
- `go vet ./domains/dispatch ./bg`
- `go test ./domains/dispatch -run 'TestPipelineWiresQueueMirror|TestPipelineQueueStats_RealPipelineTraffic' -count=10`

## Follow-up

- Build distributed RPM/TPM token-bucket Lua semantics before routing those modes through Redis enforcement.
- Stabilize `TestPipelineQueueStats_RealPipelineTraffic`, which remains intermittent only in full package runs.
- Implement the requested total execution queue, priority clusters, minute aggregation, and tentative restore lifecycle as separately reviewed work.
