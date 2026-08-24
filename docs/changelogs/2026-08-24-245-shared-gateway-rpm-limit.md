# 2026-08-24 245 Shared Gateway RPM Limit

## What Changed

- Classified the static data-plane key after `AuthMiddleware` verifies it.
- Exempted that server-authenticated shared admission key from the gateway's
  per-API-key RPM window across chat, Responses, Messages, and embeddings.
- Preserved database API-key budgets, upstream provider rate limits, and all
  credential, pool, identity, and global concurrency controls.

## Evidence

The 245 request log recorded 3,347 external gateway-side
`rate_limit_exceeded` rows in the two-hour inspection window. All 3,396
rejections in the key-level aggregation mapped to database API key 105, whose
explicit RPM value was 30. The same interval contained successful upstream
attempts, so the response was emitted before provider dispatch rather than by
an upstream rate limit or credential concurrency governor.

Auto-title loopback requests also used the shared data-plane key and produced
27 of the inspected rate-limit rows. They are covered by the same static-key
policy without granting a bypass to database API keys.

## Verification

```text
go test ./domains/streaming ./middleware
go vet ./...
go build ./...
go test ./...
```

All commands passed locally. Regression coverage verifies that the
server-authenticated static key skips the shared RPM limit while an ordinary
database key remains blocked after exceeding its configured limit.

## Risk

The shared static key is an admission credential, not a tenant quota identity.
The change intentionally moves tenant-level traffic shaping to database API
keys or other explicit policies. Upstream and dispatch-layer limits continue to
protect provider capacity.
