# 2026-07-18: Fix Minimax-m3 "no available model" — credentialhealth degraded by NIM empty-stream quirk

## Summary

LLM Gateway returned `no_candidates_from_router` ("无可用模型") and `model_not_found` for `minimax-m3` requests through `llm.kxpms.cn`, even though upstream supplier tests (`model_probe_runs`) consistently reported HTTP 200 OK on the same `(credential, model)` pair. Root cause was `credentialhealth.Checker` counting NIM's known 13% empty-stream rate (`errorsx.KindEmptyResponse`) toward the 80% degradation threshold. After ~30 empty streams over a 1-hour sliding window, the only NVIDIA NIM credential (id 19, label "endless") was marked `unavailable_recover_at = now+1h`, after which all `minimax-m3` requests hit zero available candidates and fell through to 503.

## Root Cause

Three layered bugs:

### Bug 1 — degradation threshold counts a transient quirk as a permanent failure

`credentialhealth/checker.go` (pre-fix) skip-list:
```go
if e.ErrorKind == "network" ||
    e.ErrorKind == string(errorsx.KindCanceled) ||
    e.ErrorKind == string(errorsx.KindTransient) ||
    errorsx.IsClientBug(errorsx.ErrorKind(e.ErrorKind)) {
    continue
}
```

The skip-list excluded network errors, client cancellations, transient 5xx, and client bugs — but **not** `KindEmptyResponse`. The `errorsx/classify.go:60-78` design doc explicitly says:
> "KindEmptyResponse: NOT in IsCredentialFatal: a transient empty burst must not hard-exclude the credential — it may succeed on retry."

`KindEmptyResponse` is the NIM failure mode (Provider 18, NVIDIA NIM): the stream opens, sends ~1-3 chunks with empty choices, then `[DONE]`, producing 0 completion tokens. The executor handles this at `executor_chat.go:838-840` by classifying as `KindEmptyResponse` and routing to the next candidate (per-request retry). But the `credentialhealth.Checker` was double-counting it as a credential-health signal for the 1-hour degradation.

For credential 19 (NIM / endless), the user reported 34 recent calls with `failure_rate = 0.97`, `error_kinds = {concurrent: 12, empty_response: 21}`. The Checker ran a probe before degradation (good!), but the probe also timed out at 10s (ActiveProbeWorker default `TimeoutMs = 10000`), so the credential was marked degraded. Even though subsequent scheduler probes returned 200 OK, the `unavailable_recover_at = 2026-07-18T01:12:36.955809026+08:00` held the credential out of the candidate pool for the full hour.

### Bug 2 — `state:empty_response` not in degraded-mode transient list

`domains/streaming/executors/router.go:853-867` `isTransientUnavailableReason` covered four `state:*` kinds (timeout / stream_timeout / rate_limit / upstream_down) but not `empty_response`. When the StateManager marked credential 19 as `state:empty_response` and the router filtered it out (single-candidate scenario), `tryDegradedMode` (router.go:803) saw the reason wasn't transient → returned nil → caller got `no_candidates_from_router` → 503.

### Bug 3 (diagnostic gap) — executor doesn't surface StateManager reason

`executor.go:1131-1135` computes the "no_candidates" reason breakdown purely from `c.UnavailableReason()` (DB-derived). When both DB fields and StateManager say "available" but the router still dropped the candidate via `filterAvailableWithStateManager`, the executor log line read `reasons={"unknown":1}` — the real StateManager reason (`state:empty_response`) was only visible in the upstream `router.go:145` `slog.Warn("router: all candidates unavailable", "reasons", ..., "sample", ...)` line, forcing operators to correlate two log lines.

The `e.StateObserver` interface (executor.go:623) does not expose `IsAvailable` — only `UpdateOnSuccess / UpdateOnFailure / OnNoCandidates`. Bridging would have required touching many call sites, so we kept the diagnostic gap and documented the correlation requirement in the executor comment.

## Fix

| File | Change |
|------|--------|
| `credentialhealth/checker.go:132-150` | Added `errorsx.KindEmptyResponse` to the skip-list (same comment block as the existing skip rules, with cross-reference to `errorsx/classify.go:60-78`). |
| `credentialhealth/checker_test.go` | New `TestChecker_CheckAndUpdate_ExcludeEmptyResponse` — 10 `empty_response` + 10 success must NOT trigger DB UPDATE. |
| `domains/streaming/executors/router.go:853-868` | Added `state:empty_response` case to `isTransientUnavailableReason`, so single-candidate degraded mode activates. |
| `domains/streaming/executors/router_degraded_mode_test.go:227-232` | New test case `state:empty_response → want=true`. |
| `domains/streaming/executors/executor.go:1123-1147` | Documentation-only: explicit comment pointing to `router.go:145` for the real StateManager reason. No logic change (StateObserver interface doesn't expose IsAvailable). |

## Files Changed

```
 credentialhealth/checker.go                              | 10 +++
 credentialhealth/checker_test.go                         | 72 ++++++++++++++++++++++
 domains/streaming/executors/executor.go                  | 36 ++++++-----
 domains/streaming/executors/router.go                    | 15 +++--
 domains/streaming/executors/router_degraded_mode_test.go |  4 ++
```

Net: +119 / -18 lines across 5 files. No new imports, no schema changes, no API changes.

## Verification

```
go build ./...                                                                  # PASS
go vet ./credentialhealth/... ./domains/streaming/executors/...                  # PASS
go test ./credentialhealth/...   -count=1                                       # PASS (TestChecker_* 6/6)
go test ./domains/streaming/executors/... -count=1 -run TestIsTransient*          # PASS (state:empty_response → true)
go test ./provider/... ./errorsx/... -count=1                                    # PASS (no regression)
```

## Manual Recovery Required

The 1-hour `unavailable_recover_at` for credential 19 is still in effect until `2026-07-18T01:12:36+08:00` — code fix alone does NOT restore the credential. To make the gateway green immediately (without waiting for natural recovery), run on PG `172.16.2.210:5432` database `llm_gateway`:

```sql
UPDATE credential_model_bindings
SET available = TRUE,
    unavailable_reason = NULL,
    unavailable_at = NULL,
    unavailable_recover_at = NULL,
    updated_at = now()
WHERE credential_id = 19
  AND provider_model_id IN (
    SELECT id FROM provider_models
    WHERE provider_id = 18 AND raw_model_name = 'minimaxai/minimax-m3'
  );

UPDATE model_offers
SET available = TRUE,
    unavailable_reason = NULL,
    unavailable_at = NULL
WHERE credential_id = 19
  AND raw_model_name = 'minimaxai/minimax-m3';
```

The gateway candidate cache has a 30-second TTL (`client.go:432`), so the next request after the SQL runs will pick up the fresh `available = TRUE` value automatically (no restart needed).

## Deployment Timeline

| Time (CST) | Server | Action | Result |
|------------|--------|--------|--------|
| TBD | 184 / 245 / 154 | Manual fix-deploy per `scripts/deploy-245.sh` | TBD |
| TBD | 252 PG | Run above UPDATE statements | TBD |
| TBD | Any | `curl -X POST https://llm.kxpms.cn/v1/chat/completions -H "Authorization: Bearer <KEY>" -d '{"model":"minimax-m3",...}'` | Should return 200 OK |

## Residual Risk

- `empty_response` no longer contributes to the 1-hour degradation. The only remaining safety net for upstream outages is the `active_probe` 5s→30s→2m→5m→15m backoff chain (`active_probe_backoff.go`). If NIM truly breaks (not just empties), the active probe will fail after at most one 15-minute cycle.
- Recommend monitoring `active_probe.failed_attempts` for credential 19 over the next 24h to confirm no real upstream outage is masked by this fix.
- The other "transient" kinds covered in `isTransientUnavailableReason` (timeout / stream_timeout / rate_limit / upstream_down) were already triggering degraded mode. `empty_response` was the only "soft" kind missing — the design intent in `errorsx/classify.go` is now consistent end-to-end.

## Related Incidents

- `2026-07-16-minimax-m3-timeout-analysis.md` — earlier 154 timeout spike, same model family
- `2026-07-16-minimax-upstream-slow-rca.md` — upstream slowness RCA, sibling incident
