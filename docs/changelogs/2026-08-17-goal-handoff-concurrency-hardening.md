# Goal Handoff Concurrency Hardening

Date: 2026-08-17

## Scope

Close the four remaining TODOs from the Goal↔Handoff durable audit
(`/tmp/handoff-20260817-034449.md` §3): completion-threshold cross-tenant
race, terminal-state last-writer-wins, sqlmock integration coverage of the
durable confirmation surface, and at-rest encryption evaluation for
`handoff_pending_confirmations.goal_state`.

## Changes

- **Per-request completion threshold** (`domains/hooks/goal/completion_detector.go`):
  remove the detector's mutable `minConfidenceBits atomic.Uint64`. The
  threshold is now an argument to `IsCompleted(ctx, req, minConfidence)` and
  `checkWithLLM(ctx, req, minConfidence)`. ModeHook resolves
  `goal.completion_confidence` per request via `loadFloat(req.TenantID, ...)`
  and passes it explicitly, eliminating the cross-tenant race where one
  tenant could observe another tenant's threshold in the LLM verdict.

- **Terminal-state CAS** (`domains/hooks/goal/store.go`,
  `mode_hook.go`, `handoff_trigger.go`): add `GoalStore.CompareAndSetState`
  that mirrors `AtomicAutoContinue`'s guard:
  `UPDATE goal_sessions SET state=$3, ... WHERE tenant_id=$1 AND session_id=$2 AND state = ANY($4)`.
  Replace unconditional `UpdateSessionState` writes at the four terminal
  sites — completion-detector (non-stream and stream-end), retry-budget
  exhaustion, and `MemoryHandoffTrigger.ObserveGoalOutcome` for
  `OutcomeFailed`. Terminal states (`completed`/`failed`) are now sticky:
  a concurrent failed write logs a no-op rather than silently downgrading
  a completed session and dropping audit context.

- **sqlmock coverage of PG confirmation surface**
  (`domains/hooks/handoff/confirmation_pg_test.go`, 14 cases):
  `SavePending` insert + input validation; `Confirm` first-success,
  same-idempotency replay, different-idempotency replay rejection, budget
  exhaustion, cooldown active, expired; `GetGoalRestoreState` not-found,
  corrupt JSON, version mismatch; `MarkGoalRestored` terminal transition;
  `MarkGoalRestoreAttempt` preserves retryable status;
  `MarkGoalRestoreManualRequired` on corruption + no-op on already-restored.

- **Trimmer DB adapter** (`bg/handoff_pending_trimmer.go`): add
  `TrimOnceDB(ctx, *sql.DB)` mirroring `TrimOnce` so the expire-then-delete
  path can be exercised under sqlmock without spinning up a `pgxpool.Pool`.
  Production callers continue to use `TrimOnce`. Floor on
  `pendingConfirmationRetention()` (≥ 1 day) is preserved.

- **Migration static assertions** (`domains/hooks/handoff/migration_362_test.go`):
  three tests assert that migration `362_handoff_durable_goal_state.{sql,down.sql}`
  and its startup mirror `527_*.{sql,down.sql}` carry the load-bearing
  tokens (column additions, status widening, partial index, the
  `CASE WHEN status IN (...) THEN 'confirmed'` collapse). Divergence would
  split runtime schema from migration history.

- **Latent bug fix in `PGStore.Confirm`** (`confirmation_pg.go`): on first
  confirmation, `Confirm` previously scanned `new_session_id` from the
  pending row (always NULL) and never propagated `input.NewSessionID` back
  to the in-memory proposal, so `ConfirmationResult.NewSessionID` was empty
  on PG even though `MemoryConfirmationStore.Confirm` returned the right
  value. Mirror the memory path: assign `p.NewSessionID = in.NewSessionID`
  after the SQL update. Required by the sqlmock test asserting
  `NewSessionID == input.NewSessionID`; preserves parity with the memory
  store.

- **ADR-0001** (`docs/adr/ADR-0001-handoff-goal-state-at-rest-encryption.md`):
  document the residual risk that `goal_state JSONB` is protected only by
  heuristic redaction + 16 KiB cap + short TTL, not by encryption. Compare
  four options (pgcrypto column encryption, envelope encryption with
  KMS-wrapped DEK, field narrowing, status quo) on effort, query impact,
  rollback path, and prerequisites. Decision: defer; both encryption paths
  require a KMS not yet integrated, and field narrowing is blocked on a
  HistoryStore-backed resume-quality validation. Status quo acceptance is
  recorded with the residual risk and prerequisites-for-revisit explicit.

## Verification

- `go vet ./...` — clean.
- `go test -race ./domains/hooks/goal/... ./domains/hooks/handoff/... ./bg/...` — green.
- `go test ./...` — green across the whole repo.
- `go test -race` is exercised by the new concurrent tests
  (`TestCompletionDetector_PerTenantThreshold_NoCrossContamination`,
  `TestCompareAndSetState_ConcurrentRace`,
  `TestMemoryHandoffTrigger_DoesNotOverwriteCompleted`).

## Rollback

Revert the four commits (`d032f5bb8`, `8369a6b75`, `c24d46a71`, `c2027514e`).
No schema migration; no secret change. Behaviour falls back to last-writer-
wins on terminal states and to the shared-mutable-threshold detector.
