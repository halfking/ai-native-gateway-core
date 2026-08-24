# 2026-08-24 Stream Terminal And Probe Attribution

## What Changed

- Reset discarded stream-attempt capture state before replaying a candidate.
- Allow an explicit terminal success to replace an intermediate request-log failure.
- Prefix probe error kinds by `direct` or `gateway` origin.
- Validate a reused system self-check key against the current data-plane HMAC.

## Evidence

245 request `968b68f37100488c6c83074b9b6f0c7f` returned HTTP 200 with
33,552 response bytes and 109 stream chunks. Its final provider attempt was
successful with `upstream_finish_reason=tool_calls`, but earlier discarded
empty attempts left the shared capture marked interrupted and the persisted
row retained failure state.

For credential 3 and `deepseek-v4-flash`, the direct probe returned HTTP 429
with an insufficient-quota response. The paired gateway round returned HTTP
401 and was previously labelled `probe_direct_auth_failed`, despite being a
local gateway self-check failure. The direct 429 proves the upstream accepted
the credential; it is not an invalid-key finding.

## Verification

```text
go test ./bg ./domains/hooks/observability/telemetry ./domains/streaming
go vet ./bg ./domains/hooks/observability/telemetry ./domains/streaming
go build ./cmd/gateway
```

All commands passed locally.

## Follow-Up Risk

245 continues to emit `numeric field overflow` from telemetry persistence and
its file fallback has reached the daily rotation limit. This is independent of
the terminal-state and probe-attribution fixes and requires a separate
database-write diagnosis before deployment.
