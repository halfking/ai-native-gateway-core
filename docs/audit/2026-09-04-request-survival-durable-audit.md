# Request Survival Durable Recovery Audit (Round 2)

**Date:** 2026-09-04
**Scope:** durable recovery parity for request survival — `DurableAttemptRunner`, `DurableRecoveryWorker`, durable snapshot fidelity, and the retry-budget contract between the detached worker path and the foreground coordinator.
**Predecessor:** `docs/audit/2026-09-03-request-survival-feature-audit.md` (foreground fixes: exact fixed retry interval, durable settlement classification, detached checkpoint context).
**Requirement sources:** `docs/archive/process/revision-0811/18-供应商耗尽长时保活与持久恢复设计-2026-08-14.md` (durable tasks continue after disconnect/restart under lease/fencing; snapshot freezes request inputs), `docs/03-design/02-feature-design/会话优化v4/客户端会话保持.md` (retry budget 100 with combination-exhaustion priority; client-cancel boundary), and the user requirement of 100 retries (600 after 20:00 Asia/Shanghai) at a ~30-second cadence with a 5-hour interactive window.

## Requirement summary being audited

- Interactive streaming survival: known model + streaming + survival enabled + zero candidates enters recovery instead of 503; retries at a fixed ~30s cadence; 100 retries daytime / 600 after 20:00 Asia/Shanghai; 5h interactive deadline; client disconnect stops immediately (verified in round 1 and by the new exact-count test).
- Durable survival: a task accepted at the snapshot cut point must be re-executed detached with the same logical request — same model, same client profile, same protocol payload, same routing policy — under lease/fencing, bounded by a retry budget, and settle honestly.

## Findings and fixes (this round)

### F1 (High) — detached executions multiplied the retry budget

Each worker execution pass built fresh `ExecParams` without `UpstreamAttempts`, so the executor allocated a new default 100-call budget per pass. With `MaxRetries` task executions the worst-case upstream-call ceiling was executions × per-pass budget instead of one shared request-wide ceiling, contradicting the attempt-budget contract shared with the foreground coordinator.

**Fix:** the worker now owns a per-task `UpstreamAttemptBudget` registry (`BudgetForTask`), sized `MaxRetries+1` (capped at `MaxUpstreamAttemptLimit`), exposed to the runner through `DurableAttemptRunnerImpl.BudgetProvider`, and wired in `cmd/gateway/main.go`. Budgets are released on terminal settlement and when the deadline/safety reapers take a task; the registry is hard-capped. Budgets still do not survive process restarts — persisting the used count on the task row remains a documented follow-up.

### F2 (High) — durable `/v1/responses` recovery could not use native Responses providers

The runner never set `ResponsesBodyBytes`, so native Responses candidates failed with `unsupported_feature` on every detached pass and burned the retry budget.

**Fix:** for `/v1/responses` snapshots the runner now carries the preserved native body, matching the foreground parameter pair.

### F3 (High) — detached recovery dropped the routing policy

`resolveCandidatesForRequest` returned a policy that the runner discarded, leaving `ExecParams.Policy` nil for route planning.

**Fix:** the resolved policy is passed through to the detached attempt.

### F4 (Medium/High) — profile drift between acceptance and recovery

The runner derived the client profile from the key's *current* default, but the snapshot input already froze `ClientProfile` at acceptance; it was simply never persisted. An admin changing a key's default profile would re-route the recovery under a different identity than the authorized request.

**Fix:** `DurableRequestSnapshotV1` now persists `client_profile` (omitempty — old snapshots decode unchanged), the wiring populates it, and the runner prefers it, falling back to the key default for legacy snapshots.

### F5 (Medium) — configured worker pool size was ignored

`request_survival_worker_count` (default 4) was normalized and logged but never used: exactly one worker executed claimed tasks serially.

**Fix:** `DurableWorkerOptions.WorkerCount` (0→1, clamp 32) bounds concurrent detached executions via a semaphore while the claim loop stays single-owner; `main.go` wires the config and the startup log records it.

### F6 (Medium) — worker settlement failures were silent

`settleTerminal` returned early when `PersistSettlementIntent` failed with a non-lease error: no log, no metric, task stuck in `running`.

**Fix:** the failure now increments `durable_settlement_stage_failures_total{stage=worker_persist_intent}` and logs; the claim-failure branch also logs.

### F7 (Medium) — empty-body success stranded the task

A successful detached execution with no extractable body was settled as `completed`; the store rejects empty completed bodies, the error was ignored, and the task stayed `running` until lease reclaim. The foreground converts the same case to `durable_result_unavailable`.

**Fix:** the worker now records the honest `failed/durable_result_unavailable` terminal, mirroring the foreground.

### F8 (Medium) — nil key verifier panicked every attempt

**Fix:** the runner fails closed with a bounded runner error (worker reschedules) instead of panicking.

### F9 (Low) — stale config comment

`request_survival_retry_base_seconds` documented a 2s default while normalization (and the ~30s cadence requirement) uses 30s; the comment also now points at the fixed-interval option that supersedes exponential pacing.

## Verification added

- `TestSurvivalCoordinatorNightBudgetStopsAtExactlySixHundredRetries` — pins the exact 601-call night contract at the 30s cadence under a fake clock.
- Runner: snapshot-profile preference, legacy-snapshot fallback, responses native body + policy passthrough, shared task budget, nil-verifier fail-closed.
- Worker: empty-body honest terminal, budget sharing/forget lifecycle, worker-count clamp, settlement persist-failure observability.

## Remaining follow-ups (explicitly not claimed done)

- Persisting the per-task upstream-call count across gateway restarts (needs a task-column migration).
- Durable transformation/resolution reconstruction beyond policy (transform matrix results are still re-derived inside the executor).
- Durable retry cadence is exponential (base/cap), not the interactive fixed 30s interval; the user-facing 30s contract applies to the in-connection coordinator.
- Handler-level end-to-end HTTP test for the initial-empty-candidates branch, and production `glm-5.2` no-node validation.
