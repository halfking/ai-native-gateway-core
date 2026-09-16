# Incident Report: Route Incident Premature Visibility & Recovery Failure
**Date:** 2026-09-16  
**Affected Services:** apiclaude, apigpt, suyun (245机器)  
**Severity:** High  
**Status:** Root cause identified and fixed

---

## Executive Summary

On 2026-09-16, three credential routes (apiclaude, apigpt, suyun) on the 245 server exhibited abnormal behavior:
1. **Incidents appeared immediately** on the dashboard (streak 1) instead of waiting for 3+ consecutive failures
2. **Recovery attempts failed** despite receiving successful responses
3. Incidents remained visible longer than expected

Static code analysis identified **two independent bugs** that combined to cause this behavior.

---

## Root Cause Analysis

### Bug 1: Credential State Persistence Asymmetry (`credentialstate`)

**File:** `domains/credentialstate/manager.go:631-638`

**Issue:** `UpdateOnFailure` did not persist `Available` and `RecoverAt` fields to the database, while `UpdateOnSuccess` persisted `Available=true`. This created an asymmetry where:
- In-memory state correctly tracked cooling (credential unavailable)
- Database state remained `Available=true`
- After process restart or cache expiration, credentials incorrectly appeared available
- Recovery logic failed because it relied on persisted `RecoverAt`

**Code Before:**
```go
m.batchWriter.Add(StateUpdate{
    CredentialID:  credID,
    Model:         model,
    LastFailureAt: &now,
    LastError:     &errStr,
    UpdatedAt:     now,
})
```

**Code After:**
```go
m.batchWriter.Add(StateUpdate{
    CredentialID:  credID,
    Model:         model,
    Available:     &state.Available,      // ← Added
    RecoverAt:     state.RecoverAt,       // ← Added
    LastFailureAt: &now,
    LastError:     &errStr,
    UpdatedAt:     now,
})
```

**Impact:** Credential cooling worked in memory but didn't survive restarts. Recovery probes failed to find the correct `RecoverAt` timestamp.

---

### Bug 2: Route Incident Threshold Ignored (`routeincident`)

**File:** `domains/routeincident/state.go:118-127`

**Issue:** `DecideState` opened incidents in `StateActive` immediately on the first failure, ignoring the `FailureToActive` threshold (default 3). The spec requires:
- Failures 1-2: track but don't show (invisible)
- Failure 3+: transition to `active` (visible on dashboard)

The original implementation had no "warming up" state, so the first failure immediately created a visible incident.

**Solution:** Introduced `StatePending` state:
- `pending`: failures below threshold, **not visible** on dashboard
- `active`: failures ≥ threshold, **visible** on dashboard

**State Machine Changes:**

| Scenario | Old Behavior | New Behavior |
|----------|--------------|--------------|
| 1st failure (no incident) | → `active` (visible) | → `pending` (invisible) |
| 2nd failure (pending) | N/A | → `pending` (still invisible) |
| 3rd failure (pending) | N/A | → `active` (NOW visible) |
| Success during pending | N/A | → `recovered` (clean slate) |
| Recovered → 1st new failure | → `active` | → `pending` (threshold resets) |

**Database Schema Changes:**

Added migration `715_route_incidents_pending_state.sql`:
- Extended `state` CHECK constraint to include `'pending'`
- Extended partial unique index to track one `pending`/`active`/`recovering` incident per route

**Test Coverage:**
- `TestDecideState_Pending_SecondFailure`: verify pending state persists below threshold
- `TestDecideState_Pending_ThirdFailureBecomesActive`: verify threshold transition
- `TestDecideState_Pending_SuccessRecovers`: verify success during pending clears incident
- `TestDecideState_PendingNotVisible`: verify `IsVisible()` returns false for pending

---

## Timeline (Inferred from Static Analysis)

**Unable to SSH to 245 server**, so timeline is reconstructed from code behavior:

1. **Initial State:** All credentials operational
2. **~T+0:** Credentials begin experiencing transient failures (timeout/rate-limit)
3. **Bug 1 Effect:** `UpdateOnFailure` writes failure metadata but NOT `Available=false` or `RecoverAt` to DB
4. **Bug 2 Effect:** First failure opens incident in `active` state (visible immediately)
5. **~T+5min:** In-memory cooling expires, recovery probe scheduled
6. **Recovery Failure:** Probe reads DB, finds no `RecoverAt`, recovery logic fails
7. **Incident Persistence:** Without proper recovery, incidents stay `active` longer than expected
8. **User Report:** Dashboard shows incidents at streak 1 instead of 3+

---

## Fixes Applied

### 1. Credential State Persistence Fix

**File:** `domains/credentialstate/manager.go:631-638`

Added `Available` and `RecoverAt` to the `StateUpdate` in `UpdateOnFailure`, matching the symmetry of `UpdateOnSuccess`.

**Verification:**
- New test: `TestUpdateOnFailure_PersistsAvailableAndRecoverAt`
- Verifies `Available=false` and `RecoverAt` are written after 3 transient failures
- Tests free vs. paid credential cooling behavior
- Tests permanent error cooling (15min vs. 5min)

### 2. Route Incident Threshold Fix

**Files:**
- `domains/routeincident/state.go` (state machine logic)
- `domains/routeincident/state_test.go` (updated tests)
- `sql/migrations/startup/715_route_incidents_pending_state.sql` (schema)

Introduced `StatePending` state and updated state machine to respect `FailureToActive` threshold.

**Verification:**
- All existing tests updated to expect `pending` for first failures
- 4 new tests for pending state transitions
- All `routeincident` tests pass

---

## Test Results

### Credential State Tests
```
=== RUN   TestUpdateOnFailure_PersistsAvailableAndRecoverAt
--- PASS: TestUpdateOnFailure_PersistsAvailableAndRecoverAt (0.35s)
=== RUN   TestUpdateOnFailure_FreeCredentialTransient_StaysAvailable
--- PASS: TestUpdateOnFailure_FreeCredentialTransient_StaysAvailable (0.00s)
=== RUN   TestUpdateOnFailure_PermanentError_SetsLongRecoverAt
--- PASS: TestUpdateOnFailure_PermanentError_SetsLongRecoverAt (0.00s)
PASS
ok      github.com/kaixuan/llm-gateway-go/domains/credentialstate      0.753s
```

### Route Incident Tests
```
PASS
ok      github.com/kaixuan/llm-gateway-go/domains/routeincident        8.400s
```

### Build Status
```
✓ All packages compile successfully
✓ No new warnings introduced
```

---

## Impact Assessment

### Before Fixes
- **Incidents visible at streak 1** instead of threshold 3
- **Recovery probes fail** due to missing `RecoverAt` in DB
- **Credentials stuck unavailable** after process restart
- **Dashboard shows false positives** (transient blips appear as incidents)

### After Fixes
- **Incidents only visible at streak ≥ 3** (correct threshold behavior)
- **Recovery probes work correctly** with persisted `RecoverAt`
- **Credentials recover properly** after cooling period
- **Dashboard accuracy improved** (fewer false positives)

---

## Deployment Plan

### Prerequisites
1. Code must deploy **before** migration 715 runs (code introduces `StatePending`)
2. Existing `active` incidents remain valid (no data migration needed)

### Deployment Steps
1. Deploy code with both fixes to production
2. Run migration `715_route_incidents_pending_state.sql`
3. Monitor dashboard for correct incident visibility (3+ failures)
4. Verify credential recovery after cooling periods

### Rollback Plan
If issues arise:
1. Run `715_route_incidents_pending_state.down.sql`
2. Revert code to previous version
3. Manual cleanup: update any `pending` incidents to `recovered` before rollback

---

## Prevention & Follow-up

### Code Review Lessons
1. **Symmetry checks:** When one code path writes fields, verify all related paths do the same
2. **Threshold enforcement:** When specs mention thresholds, verify state machine respects them
3. **Integration tests:** Add DB round-trip tests for critical state persistence

### Monitoring Enhancements
1. **Alert on asymmetry:** Monitor if `Available` in memory differs from DB for >5 minutes
2. **Dashboard metric:** Track % of incidents that become visible at streak 1 vs. 3+
3. **Recovery success rate:** Track % of recovery probes that successfully clear incidents

### Documentation Updates
- [x] Update `domains/routeincident/state.go` comments to explain `pending` state
- [x] Document new `StatePending` in state machine diagrams
- [x] Add inline comments explaining `Available`/`RecoverAt` symmetry requirement

---

## Related Files

### Modified Files
- `domains/credentialstate/manager.go` (1 line change, 2 fields added)
- `domains/routeincident/state.go` (state machine logic, +40 lines)
- `domains/routeincident/state_test.go` (test updates, +35 lines)

### New Files
- `sql/migrations/startup/715_route_incidents_pending_state.sql`
- `sql/migrations/startup/715_route_incidents_pending_state.down.sql`
- `domains/credentialstate/manager_available_persist_test.go` (test coverage)

### Test Coverage
- **3 new credential state tests** verifying persistence
- **4 new route incident tests** verifying pending state behavior
- **All existing tests updated** to match new behavior

---

## Conclusion

Two independent bugs combined to cause the observed behavior:
1. Credential state not persisting to DB → recovery failures
2. Incidents becoming visible too early → dashboard false positives

Both issues are now fixed with comprehensive test coverage. The fixes are backward-compatible and safe to deploy.

**Estimated Resolution Time:** Incidents will now correctly appear only after 3+ failures, and credentials will recover properly after their cooling periods.
