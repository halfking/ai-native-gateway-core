# Multimodal Testing — Final Report

**Date**: 2026-07-21
**Branch**: main
**Plan**: `docs/multimodal-testing/00-test-plan.md`
**Implementation Plan**: `docs/superpowers/plans/2026-07-21-multimodal-testing.md`

---

## Summary

| Phase | Scope | Status |
|---|---|---|
| **0** | Test plan + spec | ✅ Approved & committed |
| **1** | Local env prep (migration 451, test media, build) | ✅ Migration applied; gateway binary built and launched locally |
| **2** | Go integration tests (Layer A/B/C + T-21) | ✅ Pass; supplement tests added (T-09/T-11/T-20) |
| **3** | Real model LIVE execution | ⚠️ Executed four times; T-15 rejection passed, T-05 blocked by missing endpoint, vision LIVE **suspended pending upstream/provider reachability** |
| **4** | Modality inference fixes | ✅ Rules added for gemini-2.5/3-flash; local DB UPDATE applied; discovery COALESCE fix scoped to next session |
| **5** | Regression script + this report | ✅ `scripts/test-multimodal-regression.sh` exits 0 in default mode |

**Headline metric**: Phase 2 modality regression remains green. Phase 3 LIVE was run four times on 2026-07-21:

- Baseline: `pass=1 fail=3 skip=1`; T-15 correctly rejected via structured `503 no_candidate`, T-05 blocked by missing endpoint.
- Claude override: `pass=1 fail=3 skip=1`; offer became unavailable after discovery.
- Doubao vision override: `pass=0 fail=2 skip=3`; transport returned `HTTP 000`.
- NVIDIA NIM vision rerun: `pass=0 fail=2 skip=3`; T-02 transport failure, T-03 normalization mismatch.

No real vision inference passed. Phase 3 vision LIVE is now **suspended** until a routable offer with a verified upstream and recent traffic is available, rather than continuing to rerun and fail.

---

## Phase 2 — Go Integration Tests

| Layer | Test Scope | Tests | Result |
|---|---|---|---|
| A | Request-body detection (existing) | 5 functions, 18 subcases (T-01/02/03/04/05/07/08/14/18) | ✅ pass |
| A | Detection supplement (new) | `TestE2E_DetectModality_Supplement` covering T-09 tri-modal / T-11 malformed / T-20 fileData (3 mimeTypes) | ✅ pass |
| B | Resolve candidates (existing) | 2 tests verifying modality forward to resolver | ✅ pass |
| C | Probe (existing) | 11 httptest scenarios (success/rejected/auth/network/multimodal/anthropic) | ✅ pass |
| C | Admin endpoint (existing) | 4 tests (allow-list, request shape, invalid input) | ✅ pass |
| — | ModelName rule inference (extended) | 4 new subcases: gemini-2.5-flash/flash-image/pro, gemini-3-flash | ✅ pass |

**Suppression note**: `TestPickModels_*` in `bg/` package FAILS — pre-existing, **unrelated to multimodal work** (verified by `git stash` test). Regression script scopes `-run` to multimodal patterns to avoid false positives.

Full diagnostics: `docs/multimodal-testing/phase2-issues.md`.

---

## Phase 3 — Real Model Execution

5 cases are maintained under `scripts/multimodal-e2e/cases/`:

| ID | Model | Format | Expected |
|---|---|---|---|
| T-02 | `gpt-4o-mini` | OpenAI image_url (HTTP) | accept |
| T-03 | `gpt-4o-mini` | OpenAI image_url (base64) | accept |
| T-04 | `claude-3-5-sonnet-20241022` | Anthropic source base64 | accept |
| T-05 | `whisper-1` | multipart audio upload | skip — endpoint not implemented |
| T-15 | `gpt-3.5-turbo` | image to text-only model | reject |

**Dry-run validated** (`bash scripts/multimodal-e2e/run_phase3.sh --dry-run`):
```
[T-02] DRY-RUN: curl POST http://localhost:8781/v1/chat/completions model=gpt-4o-mini
[T-03] DRY-RUN: curl POST http://localhost:8781/v1/chat/completions model=gpt-4o-mini
[T-04] DRY-RUN: curl POST http://localhost:8781/v1/messages model=claude-3-5-sonnet-20241022
[T-05] DRY-RUN: curl POST http://localhost:8781/v1/audio/transcriptions -F model=whisper-1
[T-15] DRY-RUN: curl POST http://localhost:8781/v1/chat/completions model=gpt-3.5-turbo (expected=reject)
Summary: pass=0 fail=0 skip=5 total=5
```

**LIVE result (2026-07-21)**:

- Baseline: `pass=1 fail=3 skip=1 total=5`.
- T-02/T-03/T-04 returned no candidates because their only fixed credential was suspended and permanently exhausted.
- T-15 passed as a correct rejection: HTTP 503 with `error.code=no_candidate`.
- T-05 is an explicit skip because `/v1/audio/transcriptions` has no registered route or multipart handler.
- `claude-sonnet-4-5` and Doubao override attempts did not produce successful vision inference. Discovery marked the Claude binding unavailable; the Doubao attempt ended with curl HTTP `000`. A stale-response reporting bug found during that run was fixed and regression-tested.

Full evidence and environment diagnosis: `docs/multimodal-testing/phase3-issues.md`.

**Runner controls**:
```bash
export LLM_GATEWAY_API_KEY=<key>
export GATEWAY_URL=http://localhost:8781
export ONLY_IDS=T-02,T-03
export MODEL_OVERRIDE_T_02=<healthy-vision-model>
export MODEL_OVERRIDE_T_03=<healthy-vision-model>
bash scripts/multimodal-e2e/run_phase3.sh
```

Cost guardrails: ≤2 calls per case, ≤15 total, 30s timeout, no retries on 5xx.

---

## Phase 4 — Findings & Fixes

### Root cause: stale modality values in `models_canonical`

`discovery/discovery.go:691` uses `modality = COALESCE(models_canonical.modality, $4)` — protects previously-written values from being overwritten by new rule inference. This is the **right design** (preserves admin overrides), but it means rule-table additions cannot retroactively fix already-discovered models.

### Fixes applied (this session)

1. **Rule table — `modelname/modality_defaults.go`**:
   - Added exact rules: `gemini-2.5-flash`, `gemini-2.5-flash-image`, `gemini-2.5-pro`, `gemini-3-flash` → `multimodal`
   - Closes T-21 consistency gap identified during Phase 2 diagnosis

2. **Local DB — manual `UPDATE`** (per scope decision: code-level COALESCE fix deferred):
   ```sql
   UPDATE models_canonical SET modality='audio'     WHERE canonical_name='whisper-1';
   UPDATE models_canonical SET modality='vision'    WHERE canonical_name='claude-3-5-sonnet-20241022';
   UPDATE models_canonical SET modality='multimodal' WHERE canonical_name IN ('gemini-2.0-flash-exp','gemini-2.5-flash-image');
   ```

3. **Test coverage** — added 4 subcases to `modelname/modality_defaults_test.go` covering the new rules.

### Fixes NOT applied (deferred per scope decision)

- **`discovery.go:691` COALESCE behaviour**: keeping admin overrides protected requires either source-aware COALESCE (e.g. only protect `source='admin'`) or a migration-time backfill. Both require cross-team alignment and were scoped out of this session.
- **`gpt-4o` / `gpt-4o-mini` rule returns `vision` but DB has `multimodal`**: design-level decision (DB value allows wider SQL filter acceptance). Logged for future discussion.

---

## Deliverables

```
docs/multimodal-testing/
├── 00-test-plan.md                  ← Phase 0 design
├── phase2-issues.md                 ← Layer A/B/C test results + T-21 diagnosis
├── phase3-issues.md                 ← LIVE execution evidence and blockers
├── test-results.md                  ← this report
└── samples/                         ← gitignored; 6 fixtures (image/video/audio/invalid-base64/large)

domains/streaming/modality_e2e_test.go   ← Layer A supplement (T-09/T-11/T-20)
modelname/modality_defaults.go           ← +4 rules (gemini-2.5/3-flash)
modelname/modality_defaults_test.go      ← +4 test subcases

scripts/multimodal-e2e/
├── run_phase3.sh                    ← Phase 3 runner (dry-run + live)
└── cases/
    ├── T-02.json, T-03.json, T-04.json, T-05.json, T-15.json

scripts/test-phase3-runner.sh              ← isolated runner behavior regression
scripts/test-multimodal-regression.sh      ← one-shot regression; exits 0 in default mode
```

---

## Reproduce

```bash
# Layer A/B/C + rules:
go test -run 'TestDetectRequestModality|TestE2E_DetectModality_Supplement|TestResolveCandidatesForRequest|TestProbeModality|TestValidModalities|TestUpdateModelModality|TestInferModality' \
  ./domains/streaming/... ./bg/... ./admin/... ./modelname/...

# Phase 3 dry-run:
bash scripts/multimodal-e2e/run_phase3.sh --dry-run

# Phase 3 runner CLI selectors (use instead of env):
bash scripts/multimodal-e2e/run_phase3.sh --id T-02,T-03
bash scripts/multimodal-e2e/run_phase3.sh --id-file scripts/multimodal-e2e/cases/regression.live-ids
bash scripts/multimodal-e2e/run_phase3.sh --skip T-05

# Full regression:
bash scripts/test-multimodal-regression.sh

# Phase 3 LIVE (requires LLM_GATEWAY_API_KEY):
export LLM_GATEWAY_API_KEY=<key> GATEWAY_URL=http://localhost:8781
bash scripts/multimodal-e2e/run_phase3.sh --id-file scripts/multimodal-e2e/cases/regression.live-ids

# Phase 3 runner self-tests (no real model needed):
bash scripts/test-phase3-runner.sh
```

---

## Outstanding Items

1. **Vision LIVE rerun**: retry when a non-loadtest vision offer remains routable after discovery and its upstream is reachable
2. **Audio transcription MVP**: implement `/v1/audio/transcriptions` multipart validation, routing, forwarding, error mapping, and tests
3. **env-injector consistency**: propagate `ACC_AGE_KEY_FILE` to SOPS and regenerate the redacted credential index
4. **Modality mismatch API semantics**: consider a consistent pre-route 400/422 across chat/responses/messages instead of generic 503
5. **COALESCE write-path fix**: design + implement `source`-aware modality reconciliation in `discovery.go`
6. **Migration 451 application**: applied locally only; decide whether to roll forward to staging/prod
