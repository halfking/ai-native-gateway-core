# Stage F Policy Hot-Reload — Audit Fix Handoff (2026-08-27)

## Scope

Targeted correctness pass for the Stage F `Pipeline.ApplyPolicy` live-path that
was committed in C1–C4 on `integration/stage-f-policy-hot-reload`. Two parallel
audits (general-purpose sub-agent and Explore sub-agent) both converged on the
same four findings. This handoff records the fixes, residual items, and
evidence collected.

## Findings & fixes

### 1. `Pipeline.ApplyPolicy` fails closed on `GovernorBackend.New` error

| | |
|---|---|
| Severity | High |
| Pre-fix | `governorForCredential` returned an `unavailableGovernor`; `ApplyPolicy` swapped it in and advanced `activePolicyRevision`. Publisher marked the revision applied, so it would not retry while traffic was silently denied at admission. |
| Post-fix | `governorForCredential` now returns `(Governor, error)`. Backend failure surfaces `ErrGovernorUnavailable`-wrapped error; `ApplyPolicy` aborts before any swap and leaves `activePolicyRevision` pinned so the publisher's retry loop replays the same delta. |
| Cold-start | `newCredForwarder` wraps the constructor in `buildForwarderGovernor` with a fail-open fallback to the in-process governor (with a structured `slog.Warn`). A transient Redis outage cannot block the very first dispatch to a fresh forwarder. |
| Test | `TestApplyPolicyFailClosedOnBackendNewError` (recovery sub-assertion proves the failure path doesn't wedge the pipeline). |
| Test updated | `TestPipelineFailsClosedWhenRedisGovernorConstructionFails` rewritten against the new contract (cold-start is intentionally fail-open). |

### 2. RPM/TPM specs route through mode-correct limits

| | |
|---|---|
| Severity | High |
| Pre-fix | `ApplyPolicy` copied `spec.Limit` into `CredentialRef.ConcurrencyLimit` only; a valid `{Mode: ModeRPM, Limit: 100}` policy staged a no-op governor because `RPMLimit` stayed at zero. |
| Post-fix | New helper `specToCredentialRef` derives `RPMLimit` / `TPMLimit` from `spec.Limit` based on `spec.Mode`, taking `max(canonical, mirror)` when the upstream populates both. Belongs in `pipeline.go` (policy-application contract) rather than `governor_spec.go` (backend-call contract). |
| Tests | `TestApplyPolicyRPMSpecRoutesCanonicalLimit`, `TestApplyPolicyTPMSpecRoutesCanonicalLimit` — both pin that the canonical `Limit` flows into the corresponding `GovernorSpec.RPMLimit` / `GovernorSpec.TPMLimit` even when the mirror is zero. |

### 3. Redis governor carries the new policy revision

| | |
|---|---|
| Severity | Medium |
| Pre-fix | `governorForCredential` passed `p.ActiveRevision()` to `backend.New`. During `ApplyPolicy`, the new revision was stored *after* governor construction, so a hot-swapped Redis governor was tagged with the previous (or zero) revision. The Redis backend uses `spec.Revision` as the cache-invalidation identity. |
| Post-fix | `governorForCredential` now takes `specRevision` and `ApplyPolicy` passes `pol.Revision`. Cold-start passes `p.ActiveRevision()` because the freshly-constructed forwarder correctly belongs to the currently-active revision. |
| Test | `TestApplyPolicyPassesNewRevisionToBackendNew` — captures `backend.newSpecs[0]` and `[1]` and asserts `Revision == 5` and `== 6` respectively. |

### 4. `recordingApplier` enforces strict-monotonic contract

| | |
|---|---|
| Severity | Medium |
| Pre-fix | `recordingApplier.ApplyPolicy` accepted any revision and overwrote `active`. Test double drifted from the production `Pipeline` (which rejects non-monotonic revisions). |
| Post-fix | `ApplyPolicy` on `pol.Revision <= r.active` is a no-op (no counter bump, no regression). |
| Test added | `TestRecordingApplierRejectsOlderRevision` — replaces the old `TestRecordingApplierAcceptsAnyRevisionForNow` Stage-A "accepts any revision" semantics that no longer apply. |
| Test updated | `TestRecordingApplierConcurrentApplySerializes` — now asserts `ActiveRevision == N` (the highest revision must win) and `1 ≤ calls ≤ N` (lower-revision calls are no-ops). |

### Bonus: `Pipeline.ApplyPolicy` serializes concurrent publishers

| | |
|---|---|
| Severity | Discovered while pinning the new contracts |
| Fix | `policyMu sync.Mutex` already in place from the C1 commit; the new tests cover both serialized (`TestApplyPolicySerializesConcurrentRevisions`) and serialized-with-backend-blocked paths. No code change needed; behavior verified end-to-end. |

## Files touched

```
domains/dispatch/pipeline.go        (governorForCredential signature, ApplyPolicy limit routing + specToCredentialRef helper)
domains/dispatch/forwarder.go       (buildForwarderGovernor fail-open fallback)
domains/dispatch/policy_applier.go  (recordingApplier monotonic guard)
domains/dispatch/policy_applier_test.go               (replace TestRecordingApplierAcceptsAnyRevisionForNow; update ConcurrentApplySerializes)
domains/dispatch/apply_policy_test.go                 (4 new tests)
domains/dispatch/governor_backend_pipeline_test.go    (rewrite TestPipelineFailsClosedWhenRedisGovernorConstructionFails against new contract)
CHANGELOG.md                                          (Stage F audit-fix Fixed section)
docs/handoff/20260827-000000-stage-f-audit-fix/README.md   (this file)
```

## Validation evidence

- `gofmt -w` clean across all touched files.
- `go vet -mod=mod ./domains/dispatch/...` clean.
- `go build -mod=mod ./domains/dispatch/...` clean.
- `go test -mod=mod -count=1 ./domains/dispatch/...` → `ok` (5.2s).
- `go test -mod=mod -race -count=1 ./domains/dispatch/...` → `ok` (7.3s).
- New tests:
  - `TestApplyPolicyRPMSpecRoutesCanonicalLimit` ✓
  - `TestApplyPolicyTPMSpecRoutesCanonicalLimit` ✓
  - `TestApplyPolicyPassesNewRevisionToBackendNew` ✓
  - `TestApplyPolicyFailClosedOnBackendNewError` ✓ (covers both error and recovery paths)

## Out of scope / residual items

### Live `max_queue_depth` / `max_queue_wait_ms` reload (audit finding #4)

Migration 566 increments and publishes a policy revision when either field
changes, but the existing `getOrCreateForwarder` reads `cred.MaxQueueDepth`
once at construction. A live forwarder retains its old queue-depth policy
after a `max_queue_depth` UPDATE. Fixing it requires either:

- (a) a `replaceDepth` on `credForwarder` that resizes `cf.queue` under lock
  (race-prone — in-flight requests may block on a smaller buffer), or
- (b) draining the forwarder and rebuilding.

Both deserve their own commit and review; not bundled into Stage F.

### Dead `unavailableGovernor` type

`unavailableGovernor` is no longer constructed anywhere in the production
path (only the type definition remains in `governor.go`). Removing it is a
clean-up commit, not behavior change. Tracked separately.

### `bg/auto_route_realtime_listener.lastPending` field

Per the audit, this field is still in active use (`lastPending = time.Now()`
at line 124). Removal is a dead-code cleanup; not part of Stage F.

### Pre-existing `bg/credential_probe_v2_test.go` corruption

The file is missing its `package bg` declaration and imports on HEAD; this
breaks `go build ./...` independently of Stage F. Out of scope here.

## Next-session prompt

When resuming Stage F work:

1. Pull the latest `origin/integration/stage-f-policy-hot-reload` and confirm
   the four audit fixes are present in `pipeline.go` /
   `governorForCredential` / `specToCredentialRef` / `recordingApplier`.
2. Decide on the queue-depth reload strategy (`replaceDepth` vs. drain) and
   open a follow-up commit. The migration trigger already exists
   (`bump_credentials_governor_revision` fires on `max_queue_depth` /
   `max_queue_wait_ms` changes) — only the consumer side is missing.
3. Decide whether to remove the now-unused `unavailableGovernor` type in a
   standalone cleanup commit.
4. Repair `bg/credential_probe_v2_test.go` (missing `package bg` /
   imports) so the broader `go build ./...` is unblocked.
