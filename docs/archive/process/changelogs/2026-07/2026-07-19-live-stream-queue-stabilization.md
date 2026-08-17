# Live Stream Queue Stabilization Fix

**Date**: 2026-07-19
**Commit**: `fdd38a305`
**Priority**: P0
**Type**: Bug Fix (Root Cause Resolution)

## Summary

Eliminated periodic flicker and queue length drift in the live request stream dashboard. The swim lane display now maintains stable visual state across periodic server refreshes and request lifecycle updates.

## Root Causes Fixed

### 1. Cross-Scope Snapshot Delivery
**Problem**: Periodic `snapshot_refresh` events (every 2 hours) were broadcast to all SSE clients regardless of tenant/super scope. A super admin's snapshot could overwrite a tenant dashboard, or vice versa.

**Fix**: Introduced `fanOutScope(scope, env)` to deliver scoped refreshes only to matching clients.

**Impact**: Multi-tenant dashboards no longer interfere with each other during periodic updates.

### 2. Frontend Queue Clearing on Snapshot Refresh
**Problem**: `handleEnvelope` for `snapshot_refresh` explicitly cleared `requests = []` and `idIndex.clear()`, causing the entire queue to briefly render empty before repopulating.

**Fix**: Changed `snapshot_refresh` handler to use `mergeSnapshotFromServer` (in-place lane merge) instead of clearing state.

**Impact**: The flat request replay buffer now persists across periodic aggregation reconciles.

### 3. Request Update Triggering Re-insert Animation
**Problem**: When a request transitioned from `in_progress` to terminal state, `pushOrQueue` spliced the old tile out and pushed a new one to the tail. Vue TransitionGroup interpreted this as remove+insert, causing visual jump and queue reorder.

**Fix**: Same `request_id` now updates in-place via `requests[existingIndex] = item`, preserving queue position and Vue key stability.

**Impact**: Lifecycle updates (blue → green) render as smooth color transitions without position change or length drift.

## Changes

| File | Type | Lines | Description |
|------|------|-------|-------------|
| `admin/live_stream_sse.go` | Modified | +27/-1 | Added `fanOutScope` for tenant-isolated refresh delivery |
| `admin/live_stream_sse_audit_test.go` | Modified | +26/-0 | Regression test for cross-scope isolation |
| `web/src/composables/liveStreamStore.ts` | Modified | +14/-39 | In-place request update + snapshot merge without clearing |
| `web/src/composables/liveStreamStore.test.ts` | Modified | +39/-13 | 3 new test cases for queue stability scenarios |

**Net**: +106 insertions, -53 deletions (minimal precision fix per rule 37 principle 3).

## Verification

### Backend
- ✅ `go test ./admin` — All tests pass including new scope isolation test
- ✅ `go test ./...` — Full suite passes
- ✅ `go build ./cmd/gateway` — Clean build
- ✅ `go vet ./...` — No warnings

### Frontend
- ✅ `npx vitest run src/composables/liveStreamStore.test.ts` — 10/10 tests pass
- ✅ `npm run build` — Production build succeeds
- ✅ Pre-commit hooks — All 5 checks pass (go vet, SQL, migrations, vue-tsc)

### Behavioral
- Queue length remains stable during `idle_marker` (5min) and `snapshot_refresh` (2h) cycles
- Same-request status transitions (in_progress → success) preserve position
- Multi-tenant dashboards operate independently

## Historical Context

This fix completes a series of swim lane stability improvements:

| Commit | Date | Approach | Classification |
|--------|------|----------|----------------|
| `c5261c7ea` | ~4 weeks ago | idle markers, initial flicker mitigation | Symptomatic |
| `08718b9df` | ~3 weeks ago | 2h TTL, 20-tile window trim | Symptomatic |
| `92e409439` | ~2 weeks ago | Extend snapshot interval | Symptomatic |
| **`fdd38a305`** | **2026-07-19** | **Eliminate destructive periodic updates** | **Root Cause** |

Previous fixes reduced flicker frequency by tuning intervals. This fix eliminates the root cause: periodic events no longer trigger destructive state resets.

## Follow-up

### Immediate (Pre-Deployment)
- [ ] Deploy to 245 (pre-production test environment)
- [ ] Monitor at least one `idle_marker` cycle (5 min) and one `snapshot_refresh` cycle (2 h)
- [ ] Verify queue length and lane rendering stability in multi-tenant scenario

### Post-Deployment (Production 154)
- [ ] 1-hour check: No console errors, queue renders smoothly
- [ ] 6-hour check: No reported flicker from users
- [ ] 24-hour check: SSE reconnect rate unchanged, no performance regression

### Optional (Future)
- Consider reducing `SnapshotRefreshInterval` from 2h → 30min now that refresh is non-destructive
- Add Prometheus metrics for `snapshot_refresh` / `idle_marker` event counts

## References

- Original issue: Dashboard swim lanes flicker every few minutes, queue length jumps
- Related commits: `c5261c7ea`, `08718b9df`, `92e409439`
- Related test: `web/src/composables/liveStreamStore.test.ts`
- Design: `admin/live_stream_sse.go` SSE hub architecture

---

**Status**: ✅ Merged to main
**Deployment**: Pending (245 → 154 gate)
**Reporter**: Operations team observing periodic dashboard instability
**Resolver**: ACC Agent (Kiro)
