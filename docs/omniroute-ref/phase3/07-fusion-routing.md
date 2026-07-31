# R3: Fusion Routing

> Status: `RECONSTRUCTED-DRAFT`
> OmniRoute references are `SOURCE-VERIFIED`; Go Fusion is `NEW-DESIGN`.

## Source facts

OmniRoute has combo/target timeout concepts and a stage tracing helper at `open-sse/handlers/chatCore/stageTrace.ts`. It is enabled by `OMNIROUTE_TRACE=true` or `DEBUG=true` in `chatCore.ts`, and records trace ID, label, and elapsed milliseconds. The Go repository has no equivalent `stageTrace`, `mcp`, `a2a`, or `fusion` package.

## Proposed boundary

Define a Fusion coordinator outside the existing router/executor. It may run panel/parallel/judge work through narrow interfaces, with independent context budgets, limiter reservations, cancellation, and bounded result metadata. The first streaming version should be staged rather than speculative parallel fan-out. The existing single-candidate HTTP/protocol execution must remain the only owner of provider calls.

## Gates

Require a timeout budget model (per target and overall), cancellation/leak tests, low-cardinality trace fields, tenant/auth review, and an explicit feature flag. Stage trace is observability only and must not alter routing. Any judge/panel policy is Go-original design, not a direct OmniRoute port.
