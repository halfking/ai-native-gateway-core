# Phase 3 LIVE Issues

**Date**: 2026-07-21  
**Environment**: local gateway on `127.0.0.1:8781`, Docker PostgreSQL `llm_gateway`

## Execution summary

Phase 3 LIVE was executed against the local gateway. No production service or database was modified.

| Run | Cases | Pass | Fail | Skip | Result |
|---|---:|---:|---:|---:|---|
| Fixed baseline models | 5 | 1 | 3 | 1 | Environment/model availability failures recorded |
| `claude-sonnet-4-5` override | 5 | 1 | 3 | 1 | Offer became unavailable during discovery |
| Doubao vision override (`ONLY_IDS=T-02,T-03`) | 5 | 0 | 2 | 3 | HTTP transport returned `000`; no successful upstream inference |
| NVIDIA NIM `meta/llama-3.2-11b-vision-instruct` rerun (`ONLY_IDS=T-02,T-03`) | 5 | 0 | 2 | 3 | T-02 transport failure (`http=000`); T-03 normalized away from offer key, no candidate |

No LIVE vision inference passed. Phase 3 vision LIVE is now **explicitly suspended**: rerunning with another random offer would repeat the same outcome. Phase 3 is therefore **executed but not passed** and will not be re-attempted without first resolving upstream/provider reachability and post-discovery offer stability.

## Issue P3-01 — env-injector could not decrypt without explicit age path

- The local age identity matches the SOPS recipient.
- `sops` decrypts successfully when `SOPS_AGE_KEY_FILE=$HOME/.config/sops/age/keys.txt` is explicit.
- `env-injector` validates `ACC_AGE_KEY_FILE` but does not propagate it to `sops`.
- Its plaintext confirmation uses `read`; a non-interactive shell returns an empty answer and reports `用户取消`.
- The supported `ACC_FORCE=true` mode was used for controlled local automation; audit and encrypted storage were not bypassed.

The encrypted production store contains the gateway encryption/admin/database keys but does not contain `OPENAI_API_KEY` or `ANTHROPIC_API_KEY`, despite the redacted index listing them. The local gateway does not require those direct upstream keys because provider secrets are stored encrypted in PostgreSQL.

## Issue P3-02 — `.env.local` points to a reduced test database

`.env.local` uses `llm_gateway_test`, which contains session-focused test tables and no provider/credential catalog. The complete local database is `llm_gateway` in `llm-gateway-pg` (317 public tables, 671 canonical models, 93 credentials at diagnosis time).

LIVE startup used the complete local database. No DSN or credential value was written to this document.

## Issue P3-03 — fixed Phase 3 models have no usable credential

`gpt-4o-mini` and `claude-3-5-sonnet-20241022` are both offered only through credential 13. At execution time it was:

- `availability_state=suspended`
- `quota_state=permanently_exhausted`
- `v_routable_credential_models.is_routable=false`

The gateway correctly returned `503 no_candidate` for T-02, T-03, and T-04. This is an environment/provider availability result, not a request-modality detection failure.

## Issue P3-04 — candidate availability changed during discovery

`claude-sonnet-4-5` initially appeared routable through credential 17. After gateway startup/discovery, its offer was updated to `available=false` with `binding_unavailable`, so the override run also returned no candidates.

This demonstrates that model selection must use post-discovery state, not a pre-start database snapshot.

## Issue P3-05 — Whisper multipart endpoint is not implemented

The active mux does not register `/v1/audio/transcriptions`, and there is no multipart transcription handler. T-05 is now an explicit skip with a reason rather than a misleading LIVE failure.

Existing audio coverage is limited to JSON multimodal capability detection (`input_audio`, Anthropic/Gemini audio blocks), routing modality rules, and probe payloads. A transcription MVP needs multipart limits/validation, audio candidate resolution, upstream forwarding, authentication, error mapping, and tests.

## Issue P3-06 — text-model modality mismatch returns structured 503

T-15 sends an image to `gpt-3.5-turbo`. The modality SQL filter returns zero candidates and the gateway responds with structured `503` / `error.code=no_candidate`.

The test plan explicitly accepts an empty candidate set as correct rejection. The runner now accepts 503 only when the response code is exactly `no_candidate`; arbitrary 503 responses remain failures.

## Issue P3-07 — runner reused stale response bodies after curl failure

During the Doubao override run, curl returned HTTP `000`. The runner then displayed an old `/tmp/case.out` response from T-15, which could misattribute the failure.

A regression test was added and the runner now truncates the response file before every request. The Doubao result must be interpreted only as transport failure (`HTTP 000`); the stale body is invalid evidence.

## Issue P3-08 — vision LIVE attempts stopped after repeated environmental failures

- Two additional attempts were made after the initial three runs:
  - `meta/llama-3.2-11b-vision-instruct` via NVIDIA NIM. T-02 returned `http=000` (transport-level failure before any upstream call). T-03 normalized to `llama-3.2-11b-vision-instruct` (without the `meta/` prefix); the offer was `available=false` after the previous LIVE session and SQL still excluded it.
  - Database inspection showed the same offer existed in two rows: one with `available=true`, one with `available=false`. Routing correctly used the live view and rejected the inactive binding.
- A fresh inspection of all `ready/ok` vision providers shows at least the following offers are currently routable at SQL level: NVIDIA NIM `meta/llama-3.2-90b-vision-instruct`, Doubao `doubao-1-5-vision-pro-32k`, apigpt `gpt-5.4`, apiclaude `claude-opus-4-5`. None of them has any production traffic (`recent_samples=0`), so recent success-rate filtering cannot pre-empt the next failure.
- Further reruns are deferred. To resume, the operator must:
  1. Confirm a real upstream key exists for at least one provider with a vision-capable model and a non-zero recent request rate.
  2. Restart the gateway and re-query `v_routable_credential_models` plus `model_probe_state` to pick a still-routable offer.
  3. Re-run the LIVE vision cases only after that.
- Until those conditions hold, Phase 3 vision LIVE is suspended, not silently rerun.

## Runner improvements

The runner now supports:

```bash
ONLY_IDS=T-02,T-03
SKIP_IDS=T-05
MODEL_OVERRIDE_T_02=doubao-1-5-thinking-vision-pro
```

It records the git revision, selected IDs, skipped IDs, and effective model overrides, without logging API keys or database secrets. `scripts/test-phase3-runner.sh` covers overrides, selection, structured 503 rejection, generic 503 failure, and stale-response isolation.

## Follow-up

1. Implement `/v1/audio/transcriptions` as a separate Phase 3A feature.
2. Re-run vision LIVE when at least one non-loadtest vision offer remains routable after discovery and its upstream is reachable.
3. Consider a pre-route 400/422 modality-mismatch response across chat/responses/messages; do not conflate it with generic no-candidate outages.
4. Fix env-injector age-path propagation and regenerate the redacted credential index.
5. Continue the source-aware `discovery.go` modality COALESCE decision separately.
