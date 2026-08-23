# 2026-08-24 Session Summary Token Ratio Overflow

## Root Cause

245 persisted long-context requests through `request_logs_hot`, which invokes
`update_session_summary()`. The trigger converted `BIGINT` token counts to
`DECIMAL(10,6)` before division. That type has only four integer digits, so a
request with more than 9,999 tokens raised PostgreSQL SQLSTATE `22003` and
rolled back the complete telemetry transaction.

## Evidence

- 245 logs recorded completed requests with HTTP 200 and full client response
  bytes, followed by `trace: request log row not found`.
- The PostgreSQL trigger definition contained the bounded token casts.
- Existing 245 session rows include prompt counts above one million tokens.
- The overflow had occurred at least 3,657 times when investigated.

## Change

Migration `571_session_summary_large_token_ratio.sql` replaces the function
body. It calculates the ratio with `v_prompt_tokens::numeric /
v_total_tokens::numeric`, then stores the result in the existing
`DECIMAL(10,6)` ratio variable. No table shape or historical data changes.

## Verification

```text
go test ./sql/migrations/startup -run 'TestMigration(563|571|NumericUp)' -count=1
go test ./bg ./domains/hooks/observability/telemetry ./domains/streaming -count=1
go vet ./sql/migrations/startup ./bg ./domains/hooks/observability/telemetry ./domains/streaming
go build ./cmd/gateway
```

All commands passed locally.

## Deployment Gate

Applying migration 571 replaces a live database function. It requires the
separate 245 DDL approval, post-apply function-definition verification, and a
long-context request-log smoke test before service deployment.

The down migration deliberately refuses execution because restoring the prior
bounded casts would immediately reintroduce request-log rollbacks for long
contexts.
