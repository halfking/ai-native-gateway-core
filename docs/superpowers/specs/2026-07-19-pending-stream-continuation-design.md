# Pending Stream Continuation Repair Design

## Context

Session-backed streaming requests continue consuming an upstream response after a downstream disconnect so a client can later retrieve a pending replay. The prior implementation added capture and persistence but left protocol ownership, detached context values, authorization, retention, and timeout behavior inconsistent.

## Decisions

- Each upstream `http.Response.Body` has exactly one stream consumer. Protocol-specific bridges take priority over generic wrappers.
- Detached stream contexts use `context.WithoutCancel` before applying the stream deadline. Tenant and other request values remain available after downstream cancellation.
- Pending records are tenant-bound. Every write requires a non-empty tenant ID; public and admin reads require a verified session and exact tenant match. Missing tenant data is rejected rather than replayed for compatibility.
- Pending response retention is independent of session retention and defaults to 300 seconds.
- Capture remains bounded to 1 MiB. Overflow is terminally failed as `pending_capture_overflow`; a partial SSE body is never marked completed.
- Stream lifetime is owned by the request context and stream deadline, not a 120-second `http.Client.Timeout`. Transport-level dial and response-header deadlines remain bounded.
- Anthropic bridge read timeouts close the response body and drain the reader result so blocked read goroutines do not leak.

## Acceptance Criteria

1. Q2 OpenAI-to-Anthropic streaming has one body consumer and saves translated replay data after client disconnect.
2. All pending writes, including async retry, retain the authenticated tenant through detached contexts.
3. Missing, expired, foreign, and tenant-less pending records cannot be replayed.
4. Pending TTL is 300 seconds by default, independent of session TTL.
5. A capture overflow produces a failed pending record, not a partial completed replay.
6. Streaming is limited by request and chunk deadlines, not an unconditional 120-second HTTP client deadline.
