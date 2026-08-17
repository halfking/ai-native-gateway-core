# License Security Hardening

Date: 2026-07-14

## Fixed

- Trial rate limiting now uses Redis in the production route path and fails closed when Redis is unavailable.
- IP keys are SHA-256 hashed before storage.
- Rate counter increment and TTL assignment are atomic in Redis.
- A trial email can be reserved only once for one year across Authority replicas.
- Failed database creation releases the email reservation.
- Customer trial proxy rejects credential-bearing and non-HTTPS Authority URLs outside development.
- Proxy redirects are disabled and response bodies are capped at 64 KiB.

## Verification

- `go test ./cmd/license-authority ./licensing ./cmd/gateway -count=1`
- URL normalization and one-trial-per-email regression tests.
- Full repository and frontend verification pending after this patch.

## Explicit Release Blockers

- Active upgrade push still requires a durable signed command queue and end-to-end replay/idempotency tests.
- Runtime collection still requires an explicit allowlist, retention policy, and authenticated ingest endpoint.
- Browser activation requires a configured test Authority and a real browser flow, not only a local login-gated page.
