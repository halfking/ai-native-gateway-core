# Request Survival Feature Audit

**Date:** 2026-09-03
**Scope:** request survival for unavailable providers/models, with emphasis on `glm-5.2`, streaming protocols, retry ownership, durable foreground settlement, and client cancellation.
**Sources:** `docs/archive/process/revision-0811/18-供应商耗尽长时保活与持久恢复设计-2026-08-14.md`, `docs/03-design/02-feature-design/会话优化v4/客户端会话保持.md`, `internal/streamretry/README.md`, the approved implementation plan in `.zcode/plans/plan-sess_35992ed0-ed6b-4df7-b1fa-c8b08d12d21d.md`, and the current streaming implementation/tests.

## Executive summary

The foreground streaming path has a coherent retry owner and commit gate. Known-model, survival-enabled streaming requests can enter recovery when the initial candidate set is empty; retry notices use SSE comments; cancellation checks prevent ordinary requests from continuing after disconnect; and the shared attempt budget distinguishes retries from actual upstream calls.

This audit found and fixed three correctness issues:

1. An explicitly configured fixed retry interval was still passed through ±20% jitter, so `30s` could become `24–36s`.
2. Durable settlement treated every request-context error as a client disconnect, so `deadline_exceeded` could be released to the worker instead of being terminalized.
3. Durable write-ahead checkpoints inherited a canceled client context despite the durable contract requiring ownership to survive client disconnect.

## Requirements matrix

| Requirement | Implementation status | Evidence |
|---|---|---|
| Known model + streaming + survival enabled + no candidates enters recovery | Pass | `domains/streaming/handler.go`, `messages.go`, `responses.go`; survival eligibility gate preserves non-streaming and disabled 503 behavior. |
| Retryable upstream/no-channel failures remain recoverable | Pass | `domains/streaming/attempt_outcome.go`; `SurvivalCoordinator` maps recoverable decisions to retry/wait. |
| Only one outer retry owner | Pass for foreground | `retryowner.FreezeOwner`, `ExecuteAttempt` forces `SurvivalAttempt=true`, startup disables conflicting stream-retry wrapper. |
| Safe think progress without upstream body/provider/key leakage | Pass | `survival_wiring.go` routes `RetryNotice` through `OnNodeJump`/pre-stream comment writer; protocol terminal tests cover wire shape. |
| 100 daytime retries / 600 retries after 20:00 Asia/Shanghai | Pass for foreground | `SurvivalOptions.retriesFor`, `MaxUpstreamAttemptLimit=601`, coordinator allocates `maxRetries+1` calls when it owns the budget. |
| Approximately 30-second retry cadence | Pass after this audit | Fixed `RetryInterval` is exact; legacy exponential path retains bounded jitter; explicit `Retry-After` remains authoritative. |
| Five-hour interactive window and 24-hour durable window | Pass in configuration/wiring | `config.NormalizeRequestSurvival` and startup options wiring. |
| Client cancellation stops ordinary recovery | Pass | Context checks before/after attempt and during wait; no refresh or retry after cancellation. |
| Durable client disconnect survives through lease/worker | Improved and covered at checkpoint boundary | Durable checkpoint uses detached context; settlement distinguishes cancellation from deadline. |
| No replay after semantic content commit | Pass | `AttemptCommitGate` and durable content checkpoint route committed failures to resume-safety handling. |

## Data-flow trace

`HTTP request` → model/profile/session resolution → candidate resolver → pre-stream SSE setup → `SurvivalCoordinator` → one buffered `AttemptCommitGate` per attempt → `ExecuteAttempt`/executor budget → candidate refresh → serialized SSE output or durable capture → terminal settlement and journey/metrics records.

Only transport-only keepalive and retry comments are emitted before semantic commitment. Upstream bodies are buffered behind the gate and discarded on recoverable failures. Durable output is captured separately and persisted only after a successful terminal attempt.

## State and process closure

The normal foreground state machine is:

```text
running
  ├─ succeed → succeed
  ├─ recoverable + uncommitted → retry_now / waiting_recovery
  │                              → refresh → running
  ├─ recoverable + committed → resume_blocked
  ├─ terminal classification → fail_terminal
  ├─ deadline → expired/fail_closed
  └─ client cancellation → cancelled/fail_closed
```

Durable foreground settlement then follows:

- success with captured body → completed;
- success without an honest body → `durable_result_unavailable` failed terminal;
- client disconnect before content → release to worker;
- client disconnect after content → safety reaper owns terminalization;
- deadline or other fail-closed reason → terminal failure, never worker release.

Each non-terminal recovery state has a bounded deadline, bounded wait, and context cancellation path.

## Concurrency and compatibility review

- `SerializedStreamWriter` and `AttemptCommitGate` serialize client-visible writes.
- Durable checkpoint writes retain lease owner/fencing token and use a bounded five-second store timeout.
- Detached durable checkpoint context does not remove fencing; it only prevents a client disconnect from canceling an owned durable write.
- No schema change is part of this audit patch. The new behavior is configuration-compatible and preserves existing disabled/non-streaming paths.

## Findings not included in this patch

The following remain explicit follow-up work rather than being represented as complete:

- durable worker count is configured but not yet wired to a concurrent worker pool;
- durable worker executions do not yet carry a request-wide upstream budget across task reclaims/restarts;
- durable Responses worker reconstruction needs a dedicated review of `ResponsesBodyBytes`, transformation, and policy restoration;
- cross-HTTP-request session-level single-success enforcement needs a database-level final-success contract;
- no production traffic or shared database/container was used for this audit.

## Verification

Focused tests, race tests, `go vet`, `go build ./...`, the full Go test suite, and the local `httptest` GLM-5.2 mock-provider tests are required gates for this patch. Production `glm-5.2` no-node validation remains intentionally skipped.
