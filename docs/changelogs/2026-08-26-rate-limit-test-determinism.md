# 2026-08-26 - Rate-Limit Test Determinism

## Context

Two predecessor stashes contained test-only changes:

- `domains/streaming/request_log_pipeline_test.go`: outbound-body regression
  context, but it also removed the newly added rate-limit placeholder
  regression test.
- `domains/streaming/rate_limit_test.go`: an attempted workaround for a
  minute-boundary flaky test.

The rate-limit test pre-filled a real minute bucket, then assumed the next
bucket was about 60 seconds away. Near a minute boundary, the true estimate
can be any value from 1 to 60 seconds; the test intermittently waited through
the boundary and was admitted instead of reject-fast.

## Decision

- Keep the outbound-body regression context comments.
- Restore `TestInsertRateLimitedPlaceholder_SkipsWhenLoggedOrDisabled`; the
  placeholder path is required for rate-limited requests to persist a
  `request_logs_hot` row before the terminal update.
- Replace the wall-clock minute-bucket setup in
  `TestCheckGatewayRateLimit_QueuedBeyondBudgetFailsFast` with a deterministic
  `RPMBudgetedAdmission` fake that returns `ErrQueueBudgetExceeded` and a
  fixed estimated wait.

This tests the gateway boundary contract, while the minute-bucket package
retains responsibility for its clock-sensitive admission tests.

## Verification

```text
go build ./...                                      PASS
go vet ./domains/streaming/...                      PASS
go test ./domains/streaming/... -count=1            PASS
go test ./...                                       PASS
```

## Scope

Only tests and documentation changed. No production rate-limit or telemetry
behavior changed.
