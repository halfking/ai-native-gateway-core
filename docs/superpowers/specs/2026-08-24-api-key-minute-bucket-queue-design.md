# API Key Minute-Bucket Queue Design

## Status

Approved by operator on 2026-08-24.

## Goal

Replace immediate gateway-side API-key RPM rejection with a bounded FIFO wait:
for a key limit `L`, admit at most `L` requests in the current minute bucket,
hold at most `L` additional requests for the next bucket, and return HTTP 429
for request `2L + 1`.

## Non-goals

- Do not change upstream provider rate-limit handling.
- Do not bypass credential, pool, identity, or global concurrency governors.
- Do not change non-streaming response bodies before the final JSON response.

## Current Context

- Gateway RPM entrypoint: `domains/streaming/rate_limit.go`.
- Protocol call sites: chat, messages, responses, and embeddings handlers.
- Existing distributed limiter: `ratelimit/redis_sliding.go` with local fallback.
- Existing static data-plane admission key is marked by the auth middleware
  context and must remain outside tenant API-key RPM accounting.

## Capacity Contract

For each ordinary database API key:

- `L = KeyInfo.EffectiveRPM()`.
- Current minute bucket capacity is `L`.
- FIFO waiting capacity is `L`.
- Total admitted-or-waiting capacity is `2L`.
- Unlimited keys (`L <= 0`) bypass the bucket queue.
- Static data-plane admission keys bypass this tenant RPM queue.

The bucket identity is `(api_key_id, floor(unix_time / 60))`. Admission and
queue reservation must be atomic across gateway instances.

## Queue Behavior

1. Atomically prune expired bucket state and inspect the current bucket.
2. If the current bucket has capacity and no earlier waiter exists, increment
   the bucket and admit immediately.
3. Otherwise append a FIFO waiter if queue length is below `L`.
4. If queue length is already `L`, return HTTP 429 with `Retry-After`.
5. A waiter polls or receives a notification at the next bucket boundary,
   claims capacity in FIFO order, and then continues normal routing.
6. Request context cancellation removes the waiter reservation.

The implementation must use a Redis Lua transaction or equivalent atomic
operation for admission, queue insertion, queue claim, and cancellation.
Local fallback mode may provide process-local FIFO semantics, but it must keep
the same `2L` cap and cancellation behavior.

## Client Behavior

- Streaming protocols may emit a protocol-native waiting event before headers
  or content are sent, with a stable event type such as
  `rate_limit_waiting` and message `rate limited; waiting for next time bucket`.
- Non-streaming protocols hold the HTTP connection and emit no body until the
  final response is ready.
- All protocols expose queue metadata through response headers when available:
  `X-RateLimit-Queue-Limit`, `X-RateLimit-Queue-Remaining`, and
  `X-RateLimit-Queue-Position`.
- A full queue returns the existing canonical 429 envelope and `Retry-After`.

## Failure Boundaries

- A client disconnect cancels the wait and releases its queue reservation.
- A bounded wait timeout returns a typed temporary error rather than retaining
  a connection indefinitely.
- Redis failures use the existing local fallback policy and emit a structured
  `rate_limit_queue_backend_unavailable` signal.
- Queue operations must never consume or release provider execution tokens;
  admission happens before dispatch acquisition.

## Test Contract

- Current bucket admits exactly `L` requests.
- The next `L` requests wait in FIFO order.
- Request `2L + 1` receives 429.
- Crossing a minute boundary releases waiters before new arrivals.
- Concurrent admission never exceeds bucket or total capacity.
- Cancellation releases queue capacity.
- Static data-plane keys and unlimited keys bypass the queue.
- Chat, messages, responses, and embeddings share the same admission result.

## Implementation Status

- Pure Go bounded FIFO admission and cancellation: implemented.
- Redis atomic minute-bucket admission and FIFO queue: implemented.
- Four streaming handler call sites share the admission seam: implemented.
- Non-streaming requests remain body-silent while waiting: implemented.
- Streaming waiting event and queue metadata headers: implemented.
