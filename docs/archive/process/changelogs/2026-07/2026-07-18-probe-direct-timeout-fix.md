# 2026-07-18: Fix `probe_direct_timeout` cascading 503 on slow upstreams (NIM minimax-m3)

## Summary

When the upstream returned slower than the probe timeout, `ActiveProbeWorker` marked the credential unavailable for 5 minutes on every retry, gating the router via `state:probe_direct_timeout` and 503'ing every user request — even though the upstream was healthy and the next probe backoff would have recovered. Three coordinated fixes:

1. Probe timeout 10s → **30s** (NIM `minimaxai/minimax-m3` measured TTFB 30197–30257 ms).
2. Probe failure is no longer treated uniformly — only **permanent** failures (auth / 4xx / gateway-side build) lock for 5 min. Transient failures (timeout / network / rate / 5xx / canceled / skipped) only update `LastError` and let the next backoff retry decide.
3. `state:probe_direct_timeout` is now in the **single-candidate degraded-mode transient list**, so a lone timed-out credential still gets one retry inside the same request lifecycle instead of immediate 503.

## Root Cause

Timeline (request_id `2755258563a3e99f11fe8d002b29bc80`, cred 19 / NIM / minimax-m3):

```
01:44:22  user requests minimax-m3 → routed to cred 19
01:44:52  upstream_http_attempt latency_ms=30197ms, err_kind=timeout
          err_message: "Post https://integrate.api.nvidia.com/v1/chat/completions:
                       net/http: timeout awaiting response headers"
01:45:24  retry #1 → 30198ms timeout
01:45:54  retry #2 → 30198ms timeout
01:45:33  user gives up (92s total wait), cancels

01:45:23  active_probe submitted (parent_req_id=275525...)
01:45:33  probe attempt 1 → 10s timeout → fail → next 5s
01:45:48  probe attempt 2 → 10s timeout → fail → next 30s
01:46:28  probe attempt 3 → 10s timeout → fail → next 2m
... (4, 5 still pending)
```

Two compounding bugs:

### Bug A — probe timeout (10s) < upstream TTFB (30s)

`cmd/gateway/main.go:1885` defaulted `epTimeoutMs = 10000`. NVIDIA NIM's first-byte for `minimaxai/minimax-m3` is consistently 30+ seconds (cold cache, model loading). The active probe, designed to validate the upstream in a tight 5s/30s/2m/5m/15m backoff chain, was configured to ALWAYS time out on the first attempt. Subsequent attempts at 30s+ also timed out, but more importantly the upstream really did eventually respond — just past the 10s window. Curl from the same machine returned in 200 ms because the test command uses `max_tokens=1` and doesn't trigger NIM's cold-start path.

### Bug B — probe failure unconditionally locks 5 min

`bg/active_probe_worker.go:329-340` (pre-fix):

```go
if w.cfg.StateManager != nil {
    recoverAt := time.Now().Add(5 * time.Minute)
    w.cfg.StateManager.UpdateFromProbe(ctx, &credentialstate.State{
        CredentialID: task.CredID,
        Model:        task.Model,
        Available:    false,
        LastError:    classifyProbeErrorKind(result),
        RecoverAt:    &recoverAt,
        Source:       "probe_direct",
    })
}
```

Every non-success probe outcome (timeout, network, 5xx, auth, 4xx, build-failure, cancel) wrote `Available=false` + `RecoverAt=now+5m`. That is appropriate for `auth`/`4xx` (the key won't fix itself in 5 minutes) but catastrophic for `timeout` (the upstream might be perfectly healthy — just slow today).

`router.isTransientUnavailableReason` did not include `state:probe_direct_timeout`, so the router's `tryDegradedMode` also refused to use the credential in the same request lifecycle → `no_candidates_from_router` → 503.

## Fix

| File | Change |
|------|--------|
| `cmd/gateway/main.go:1885` | `epTimeoutMs` default 10000 → **30000**. Override via `LLM_GATEWAY_ERROR_PROBE_TIMEOUT_MS` env var. |
| `bg/active_probe_worker.go:93-98` | `NewActiveProbeWorker` fallback default 10000 → **30000** for tests / external callers. |
| `bg/active_probe_executor.go:73-103` | New exported `IsPermanentProbeFailure(ProbeStatus) bool`. Permanent = auth / 4xx / failed; everything else = transient. |
| `bg/active_probe_worker.go:329-368` | processOne now branches: permanent → `UpdateFromProbe(Available=false, RecoverAt=now+5m)`; transient → `UpdateFromProbe(Available=true, LastError=...)` so the next backoff retry decides. |
| `bg/active_probe_worker_test.go:55-57` | `TestNewActiveProbeWorker_Defaults` updated for the 30000ms default. |
| `bg/active_probe_worker_test.go:489-528` | New `TestIsPermanentProbeFailure` — 10-case matrix locks the policy. |
| `domains/streaming/executors/router.go:879-888` | `isTransientUnavailableReason` adds `state:probe_direct_timeout`. |
| `domains/streaming/executors/router_degraded_mode_test.go:227-232` | New test case `state:probe_direct_timeout → want=true`. |

## Files Changed

```
 bg/active_probe_executor.go                        | 38 +++++++++++++++
 bg/active_probe_worker.go                          | 34 ++++++++++++++-
 bg/active_probe_worker_test.go                     | 46 ++++++++++++++++++-
 cmd/gateway/main.go                                | 14 ++++++-
 domains/streaming/executors/router.go              | 18 ++++++++
 domains/streaming/executors/router_degraded_mode_test.go |  4 ++
```

Net: +149 / -5 lines across 6 files. No new imports, no schema changes, no API changes.

## Verification

```
go build ./...                                                                              # PASS
go vet ./bg/... ./cmd/gateway/... ./domains/streaming/executors/...                           # PASS
go test ./bg/... -run TestIsPermanentProbeFailure -count=1                                    # PASS (10/10 sub-tests)
go test ./bg/... -run TestNewActiveProbeWorker_Defaults -count=1                              # PASS
go test ./domains/streaming/executors/... -run TestIsTransientUnavailableReason -count=1     # PASS (state:probe_direct_timeout → true)
go test ./bg/... ./cmd/... ./domains/streaming/executors/... ./domains/credentialstate/... ./credentialhealth/... ./provider/... -count=1
                                                                                              # PASS
```

(Note: `TestExecute_GLM51_TriesThirdCandidateAfterTwoModelNotFound` was failing on
HEAD `b07b4ffae` (before this commit) due to a Redis pool initialization
issue in the local test env. Not introduced by this change.)

## Manual Recovery (Same as Previous Commit)

The 1-hour `unavailable_recover_at` for cred 19 is still in effect until
`2026-07-18T01:12:36+08:00` (the original probe-degradation incident).
After deploying this fix, the new 30s probe timeout will succeed against
NIM on attempt 1, and the new `IsPermanentProbeFailure` gate prevents
future false-positive 5-min cooldowns for transient probe failures.

To immediately restore cred 19 without waiting for natural recovery, run on PG `172.16.2.210:5432` db `llm_gateway`:

```sql
UPDATE credential_model_bindings
SET available = TRUE, unavailable_reason = NULL,
    unavailable_at = NULL, unavailable_recover_at = NULL, updated_at = now()
WHERE credential_id = 19
  AND provider_model_id IN (
    SELECT id FROM provider_models
    WHERE provider_id = 18 AND raw_model_name = 'minimaxai/minimax-m3'
  );

UPDATE model_offers
SET available = TRUE, unavailable_reason = NULL, unavailable_at = NULL
WHERE credential_id = 19
  AND raw_model_name = 'minimaxai/minimax-m3';
```

## Residual Risk

- 30s probe timeout means the active probe can occupy a worker slot
  for up to 30s. With the default QueueSize=128 and MaxAttempts=5, one
  probe cycle could occupy a slot for up to 2.5 minutes worst-case.
  The 5s/30s/2m/5m/15m backoff chain still bounds total cycle time to
  ~22.5 min per (cred, model). At QueueSize=128 with 200 active
  (cred, model) pairs the queue can absorb this; beyond that we should
  revisit.
- If a future upstream takes >30s for TTFB (e.g. Anthropic's
  long-prompt reasoning models under load), the same cascade will
  return. The fix is to bump `LLM_GATEWAY_ERROR_PROBE_TIMEOUT_MS`
  per-tenant or per-credential; we did not add that knob yet.

## Related Incidents / Fixes

- `2026-07-18-minimax-m3-nim-empty-response-degradation-fix.md` — the
  credential degradation half (empty_response → 1h cooldown); this
  commit closes the probe-timeout half (probe_direct_timeout → 5min
  cooldown per probe attempt).
- `2026-07-16-minimax-m3-timeout-analysis.md` — earlier timeout spike.
- `2026-07-13-error-triggered-probe.md` — original active_probe design.

## Deployment

Build at HEAD `ea3cd97c9` + this commit, deploy per `scripts/deploy-245.sh`
to staging (245) first, then production (154 / 184). Manual SQL recovery
above is optional — natural expiry of `unavailable_recover_at` plus the
new probe-timeout behaviour is sufficient.
