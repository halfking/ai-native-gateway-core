# 2026-08-24 Local Probe Auth Contract

## Root Cause

245 runs the legacy probe mode. `NodeProbeWorker` replaced its configured
local gateway API key with a decrypted database system key. The local gateway
uses static `AuthMiddleware`, which accepts only `LLM_GATEWAY_API_KEY`, so the
gateway probe returned HTTP 401 even when the direct upstream probe accepted
the credential.

## Change

Local gateway probes now retain the configured data-plane API key. The legacy
database system-key lookup no longer overrides it. This makes the loopback
authorization contract match the gateway's static authentication boundary.

## Verification

```text
go test ./bg ./cmd/gateway ./middleware ./domains/streaming -count=1
go vet ./bg ./cmd/gateway ./middleware ./domains/streaming
go build ./cmd/gateway
```

All commands passed locally.
