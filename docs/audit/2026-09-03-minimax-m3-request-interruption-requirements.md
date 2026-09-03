# MiniMax-M3 Request Interruption: Requirements and Source Audit

**Audit date:** 2026-09-03
**Scope:** Anthropic/OpenAI-compatible streaming requests routed to MiniMax-M3, related URSM candidate ordering, timeout hot reload, dashboard evidence, and incident documentation.

## User-facing requirements

1. A stream that has emitted semantic content and then reaches a clean upstream EOF without `data: [DONE]` must finish successfully. It must not count as an interrupted provider failure or trigger a transparent retry.
2. Before the first SSE line, cancellation, clean EOF, timeout, and transport failures must remain distinguishable. A client cancellation or upstream EOF must not be reported as a first-byte timeout.
3. After the first non-semantic SSE frame has arrived, subsequent reads must use the inter-chunk timeout, not the first-byte timeout.
4. URSM candidate scoring must not change merely because a node is served from the process-local mirror rather than Redis.
5. Invalid timeout/retry hot-config rows must not partially overwrite a previously valid online configuration.
6. The dashboard must make request identity inspectable without widening request rows: show a compact request ID, expose the full ID in a tooltip, and show request type/model/agent context.

## Findings and disposition

| Finding | Evidence | Disposition |
|---|---|---|
| MiniMax-compatible upstreams can close an SSE stream without `[DONE]` after valid content. | Existing `domains/streaming/stream_eof_test.go` regression coverage and `stream.go` EOF handling. | Existing behavior retained: semantic output plus clean EOF is successful and non-retryable. |
| First-line read failures were collapsed into `first_byte_timeout`. | `domains/streaming/stream.go` first-line path previously assigned that reason for every error. | Fixed: classify canceled, EOF, timeout, and transport failures before producing `StreamOutcome`. |
| The empty-stream gate reused first-byte timeout for later buffered reads. | `runEmptyStreamGateWithVendor` received `firstByteTimeout` and used it for every follow-up line. | Fixed: the gate receives and uses `streamChunkTimeout` after the already-read first line. |
| The LRU node mirror retained `SR5m` but not `Samples5m`; `safeSR5m` treats zero samples as neutral. | `domains/ursm/v2/cache/nodemirror.go`, `domains/ursm/v2/manager.go`. | Fixed: round-trip `Samples5m`; regression test verifies Redis and mirror score/order parity. |
| Timeout hot reload applied individual parsed rows directly to live fields, with no relational validation. | `config/timeout_config.go`. | Fixed: build candidate snapshot, validate it, then atomically apply it; invalid snapshots leave live values unchanged. |
| Upstream 429 `Retry-After` appeared at risk of being dropped. | Trace through `upstream/client.go`, `domains/streaming/executors/executor.go`, and `domains/credential/writer.go`. | No main-path code change: the executor already copies `upstream.Error.RetryAfter` to `credential.Failure`, and persistence uses it for recovery time. The in-memory breaker intentionally accepts only error kind. Node-health/execution-recorder retry hints remain a separate observability follow-up. |
| Historical dashboard/load-balancing audit claimed nonexistent files and unproven staging validation. | `docs/audit/2026-09-02-minimax-m3-load-balancing-dashboard-audit.md` compared against commit `f4313a00a`. | Corrected to name the actual two historical files and distinguish source verification from staging/production validation. |

## Acceptance checks

### Source-level checks

- `go test ./domains/streaming ./domains/ursm/v2 ./config`
- Dashboard component test verifies request ID truncation, full-ID tooltip, and composed title.
- The timeout tests prove both valid atomic updates and invalid-snapshot rejection.
- The URSM test proves Redis and LRU paths produce the same success-rate-aware ordering.

### Deployment checks (not satisfied by source tests)

1. Synchronize the running gateway's PostgreSQL credentials with the configured `LLM_GATEWAY_DATABASE_URL`, then restart the gateway and verify config reload succeeds. The observed `SQLSTATE 28P01` authentication failure is a deployment/configuration incident, not a code fix delivered here.
2. Confirm `llmgw_node_timeout_seconds` and any relevant timeout settings in the production database are valid under the new snapshot rules.
3. Run a staging dashboard smoke test for request-ID tooltip and composed request label.
4. Inspect production routing telemetry to confirm sibling MiniMax credentials receive traffic after recovery. Source-level scoring parity does not prove real-world provider capacity or rate-limit behavior.
5. Escalate protocol compliance to MiniMax: sending `data: [DONE]` remains the preferred upstream behavior even though the gateway now safely tolerates clean EOF after semantic output.

## Explicit non-goals

- Do not force `[DONE]` when no semantic output has arrived; that would hide empty/upstream-failure streams.
- Do not rewrite provider selection or add a fixed credential-ID tie-breaker without evidence that equal-score rotation is needed.
- Do not change the in-memory breaker API solely to duplicate durable credential recovery behavior already driven by `Retry-After`.
- Do not represent deployment recovery, browser validation, or live traffic distribution as completed until independently observed.
