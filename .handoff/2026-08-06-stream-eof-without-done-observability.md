# Stream EOF Without [DONE] Observability Hot-Patch (2026-08-06)

Author: halfking (via ZCode session)
Status: P1 hot-patch LANDED on `main` as `0818a07c`
Next review: 24h after deploy (counter `llm_gateway_stream_synthesized_done_total`
should grow at ~13% of minimax upstream stream QPS).

## Background

154 (`llm-gateway-go`) logs showed `upstream EOF without [DONE]` 78×/24h, all
on `client_model=minimax-m3 / credential_id=21 / provider_id=14`. The
gateway already classifies these as benign (`isBenignEOF` in
`executor_chat.go:975` + `classifyStreamInterruption` in `handler.go:5609`),
so `success=true` is correct, but operator dashboards cannot tell the
known-benign pattern from real stream interruptions.

The code comment at `handler.go:5625–5680` already states:

> 2026-07-29: Decomposed from the "stream_read_error" bucket …
> observed on MiniMax, ~13% of streams as of 2026-07-28. …
> Mirrors executor_chat.go isBenignEOF: chunk_count > 0 is classified
> as success and never reaches this mapping.

So the existing code is correct; the gap is purely observability.

## What this commit changes

File: `domains/streaming/stream.go`, branch `streamReadEOF`

| Before | After |
|---|---|
| SafeSends `data: [DONE]\n\n` and flushes regardless of whether upstream sent [DONE]. | Same, but now also captures `synthesizedDone := !upstreamDoneReceived` and emits a single `slog.Warn("stream synthesized [DONE] terminator", ...)` + `metrics.Global().RecordStreamSynthesizedDone()` IFF the upstream DID NOT send [DONE]. |

Behaviour: ZERO change. `isBenignEOF`, `success=true/false`,
`stream_chunks`, `streamErrorKindForDetailCode` are all untouched.

## Test additions

1. `metrics/metrics_test.go`
   - `TestNoopRecorder_StreamSynthesizedDoneNoCrash` — pins Noop safety
   - `TestPrometheusRecorder_StreamSynthesizedDoneCounter` — pins
     `llm_gateway_stream_synthesized_done_total` increments by 1

2. `domains/streaming/stream_eof_test.go`
   - `TestStreamChatWithPendingCapture_SynthesizedDoneIncrementsMetric`
     — confirms the EOF branch fires the metric when upstream DID NOT
     send [DONE]
   - `TestStreamChatWithPendingCapture_UpstreamDoneNoSynthMetric` —
     inverse case: counter must stay 0 when upstream sends [DONE]
     naturally

All four pass. Existing `TestStreamChatWithPendingCapture_EOFWithoutDoneAppendsDone`
and `TestStreamChatWithPendingCapture_EOFWithoutDoneZeroChunks` continue to pass.

## What this commit does NOT do (out of scope)

- nginx 154 config — already correct: `proxy_buffering off`,
  `proxy_read_timeout 3600s`, `proxy_send_timeout 1200s`,
  `proxy_buffer_size=default`. Verified via `~/llm-kxpms-cn.conf`.
- `client_response` raw_data logging on streaming path — would require
  buffering the entire response body just to log it; cost > value.
- `sessionv2mirror: V2 shadow write failed` timeout root cause — separate
  issue, see leftover in §"Open questions" below.
- `trace.FlushToPG: request log row not found` failure — separate issue.

## Specific analysis of `51ff49f3d3b5292581dddd65ba16395d`

(Refer to the session conversation transcript for the full play-by-play.
Key facts retained here:)

| Phase | Time (CST) | Observation |
|---|---|---|
| routing_resolve | 01:07:10.105 | top_provider_id=14, candidates_count=2 |
| sanitize_tool | 01:07:10.822 | removed 131 of 221 tool messages |
| validate_and_fix | 01:07:10.823 | 221 → 67 messages |
| finalizeBody | 01:07:10.826 | provider_id=**34** / credential_id=11 → **byte-volcados (NOT 14!)** |
| upstream 1st attempt (byte-volcados) | 01:07:11.089 | **HTTP 429 AccountQuotaExceeded** (5h quota used up) |
| upstream retry (minimax) | 01:07:16.619 | status=200, finally works |
| upstream chunks | 01:07:16.700–17.046 | 7 data chunks sent |
| last chunk | 01:07:17.193 | usage frame, NO `data: [DONE]\n\n` after |
| audit completed | 01:07:17.807 | `success=True, stream_chunks=7, stream_ttfb_ms=5343` |
| http_request | 01:07:17.816 | status=200, response_bytes=1996, duration=11017ms |

This request exhibited THREE phenomena:

1. **byte-volcados quota exhaustion triggered failover to minimax** — but
   `routing_resolve` reported `top_provider_id=14` (minimax). Routing/finalize
   alignment is a separate question.
2. **minimax legitimately did not send `data: [DONE]\n\n`** despite finishing
   with `finish_reason=stop + usage`. Gateway synthesized the terminator.
3. **No `client_response` log row in raw_data** because the streaming path
   only logs `upstream_response`, not what was actually written to the
   client. This is the same blind spot the new metric helps with.

## Open questions / independent follow-up

1. `request_trace: flush attempt failed` — 24h 3059 hits vs `audit:
   request completed` 1893 hits. `trace.FlushToPG: request log row not
   found, retaining Redis trace` 3045 hits. Trace-to-PG pipeline failure
   rates look systemic, not incidental. **Independent PR candidate.**

2. `sessionv2mirror: V2 shadow write failed ... timeout: context
   deadline exceeded` — 506 hits in the journald window (24h). All
   error strings identical. Recent commit `ea2ec2f0 fix(sessionv2mirror)`
   partially mitigated but the failure mode persists. **Independent PR.**

3. `routing_resolve.top_provider_id=14` but `finalizeOpenAIUpstreamBody
   .provider_id=34` — top-provider selector and first-attempt alignment
   could be investigated. Not a client-visible regression here because the
   failover path was fine, but worth a debug log.

## Deployment observation checklist (24h post-deploy)

1. `journalctl _SYSTEMD_UNIT=llm-gateway-go.service | grep "stream
   synthesized [DONE] terminator"` — should appear ~13% of minimax streams.
2. Prom scrape `llm_gateway_stream_synthesized_done_total` — counter
   graph in Grafana. If rate matches `minimax_streams_total * 0.13`,
   diag is correct; if not, dive into the delta.
3. Compare `audit: request completed success=true` count vs
   `upstream EOF without [DONE]` count — should be ~1:1 in minimax.
4. No new ERROR / panic logs should appear in the deploying window.

## Rollback

If deploy causes any regression:
```bash
git revert 0818a07c     # local revert
git push origin main    # rollout
```

Or, on 154:
```bash
# journal-based deploy hook (per project convention):
git pull --ff-only origin main
./bin/restart-llm-gateway.sh
```

The patch only adds one slog.Warn + one prom counter Inc() on the existing
EOF branch; rollback risk is minimal.

## Files touched

```
domains/streaming/stream.go            | +18 lines (1 hot branch insert)
domains/streaming/stream_eof_test.go   | +135 lines (countingRecorder + 2 tests)
metrics/interface.go                   | +14 lines (1 interface method + Noop)
metrics/prometheus.go                  | +32 lines (~) (counter field + impl)
metrics/metrics_test.go                | +34 lines (2 tests)
```

Total: 233 insertions, 4 deletions across 5 files. Zero deletions of
existing behaviour — pure additive observability.
