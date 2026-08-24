# 2026-08-24 API Key Minute-Bucket Queue

## What Changed

- Replaced immediate per-key RPM admission failure with a minute-bucket queue.
- Current bucket capacity is `L`; the FIFO wait queue capacity is `L`.
- The `(2L)+1` request is rejected with the existing 429 envelope.
- Waiters honor request cancellation and are released FIFO at the next bucket.
- Redis admission uses atomic scripts; unavailable Redis falls back to the
  same bounded process-local behavior.
- Streaming clients receive `rate_limit_waiting` SSE notification frames;
  non-streaming clients receive no body until the final response.

## Verification

```text
go test ./ratelimit ./domains/streaming
go vet ./ratelimit ./domains/streaming
```

Focused queue, Redis, protocol call-site, static-key bypass, cancellation, and
waiting-notification tests pass. Full repository verification remains required
before merge.
