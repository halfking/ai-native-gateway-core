# Live Stream Audit Follow-up

## Changes

- Kept Redis pub/sub as the success-path SSE delivery mechanism; direct enqueue remains the fallback when Redis persistence fails.
- Added a `broadcast_drops` hub metric and warning log when the bounded broadcast queue drops a request.
- Added warning diagnostics when a computed tenant or active super-admin scope has no delta.
- Added a Minimax-only warning when successful telemetry receives an empty inbound request body.

## Verification

- `go build ./admin/`
- `go vet ./admin/`
- `go test ./admin/ -count=1 -timeout 60s`
- `go build ./domains/streaming/`
