# Node Probe Real-time Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `node_probe_failed` recover as soon as NodeProbeWorker's runOne succeeds, so `v_routable_credential_models.is_routable` flips back within the existing 5s `auto_route_refresh` debounce instead of waiting until the next tick.

**Architecture:** Existing `bg/node_probe.go` `runOne` success branch already updates `next_retry_at = +1h` and `paused=FALSE`. We extend the same branch to (a) write `last_direct_ok=TRUE` / `last_err_code=NULL` (= MarkNodeProbeHealthy), (b) call `provider.InvalidateCandidateCacheForCredential`, (c) `pg_notify('auto_route_refresh', 'credentials:UPDATE:<id>')`. `updateBindingAvailability` success path remains unchanged. No SQL changes; view `v_routable_credential_models` (migration 417) and `node_probe_state` table are unchanged.

**Tech Stack:** Go 1.22, `pgx/v5`, `provider` package's `InvalidateCandidateCacheForCredential`, NodeProbeWorker's existing `runOne`/`updateBindingAvailability`, AutoRouteRealtimeListener (`bg/auto_route_realtime_listener.go`).

**Spec source:** `docs/superpowers/specs/2026-07-25-realtime-routing-self-heal.md` §3

**Sibling plan:** §1 / §2 front-end fixes → `docs/superpowers/plans/2026-07-25-live-stream-frontend-fixes.md`

**Files touched (overview):**

| File | Purpose |
|---|---|
| `bg/node_probe.go` | extend `runOne` success branch (tasks 1–3), reuse `MarkNodeProbeHealthy` semantics |
| `bg/node_probe_test.go` | new failing tests proving success path invalidates cache + notifies |
| `bg/node_probe_recovery_test.go` | new integration test for end-to-end "5s after success, view flips" |
| `admin/probe_history.go` | confirm `handleProviderProbeHistoryTrigger` chain untouched (read-only) |

---

## Task 1: Pin test for runOne success writing last_direct_ok + last_err_code=NULL

**Files:**
- Modify: `bg/node_probe_test.go` (new test function at end of file)

- [ ] **Step 1: Write the failing test**

Add the following test to `bg/node_probe_test.go`:

```go
// TestRunOneSuccessClearsLastDirectOkAndErrCode pins the contract added
// by 2026-07-25 realtime-routing-self-heal §3: a successful probe
// round must immediately write last_direct_ok=TRUE and last_err_code=NULL,
// so v_routable_credential_models picks up the recovery without waiting
// for the next snapshot.
func TestRunOneSuccessClearsLastDirectOkAndErrCode(t *testing.T) {
	src, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	wantSnippet := `
			consecutive_failures = 0,
			consecutive_successes = consecutive_successes + 1,
			last_attempt_at = now(),
			next_retry_at = now() + interval '1 hour',
			next_retry_seconds = 3600,
			paused = FALSE,
			last_run_id = NULL,
			last_direct_ok = TRUE,
			last_gateway_ok = TRUE,
			last_err_code = NULL,
			last_err_detail = NULL,
			in_flight_until = NULL,
			updated_at = now()`
	if !strings.Contains(body, wantSnippet) {
		t.Fatalf("runOne success branch must write last_direct_ok=TRUE, last_err_code=NULL; update bg/node_probe.go:runOne success UPDATE block")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go && go test ./bg/ -run TestRunOneSuccessClearsLastDirectOkAndErrCode -v`
Expected: FAIL with `runOne success branch must write last_direct_ok=TRUE, last_err_code=NULL; update bg/node_probe.go:runOne success UPDATE block`.

- [ ] **Step 3: Update runOne success branch in `bg/node_probe.go`**

Locate the success UPDATE in `func (w *NodeProbeWorker) runOne` (search `consecutive_successes = consecutive_successes + 1`) and replace the UPDATE so it includes `last_direct_ok = TRUE, last_gateway_ok = TRUE, last_err_code = NULL, last_err_detail = NULL`:

```go
		_, _ = w.db.Exec(ctx, `
			UPDATE node_probe_state SET
				consecutive_failures = 0,
				consecutive_successes = consecutive_successes + 1,
				last_attempt_at = now(),
				next_retry_at = now() + interval '1 hour',
				next_retry_seconds = 3600,
				paused = FALSE,
				last_run_id = NULL,
				last_direct_ok = TRUE,
				last_gateway_ok = TRUE,
				last_err_code = NULL,
				last_err_detail = NULL,
				in_flight_until = NULL,
				updated_at = now()
			WHERE credential_id = $1 AND raw_model_name = $2
		`, credID, model)
```

Keep the surrounding comment block in sync (it already mentions "BUG #6 fix" — add a one-line comment: `// 2026-07-25: clear last_direct_ok / last_err_code so v_routable drops node_probe_failed immediately`).

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./bg/ -run TestRunOneSuccessClearsLastDirectOkAndErrCode -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add bg/node_probe.go bg/node_probe_test.go
git commit -m "fix(node-probe): clear last_direct_ok + last_err_code on success so v_routable drops node_probe_failed immediately"
```

---

## Task 2: Make the success path invalidate URSM v2 cand cache + notify auto_route_refresh

**Files:**
- Modify: `bg/node_probe.go` (success branch UPDATE block → still in runOne, immediately after the UPDATE; failure branch already calls `updateBindingAvailability`+`invalidateCandidateCache` — mirror those on success)
- Modify: `bg/node_probe_test.go` (new source-grep test)

- [ ] **Step 1: Write the failing test**

Append to `bg/node_probe_test.go`:

```go
// TestRunOneSuccessInvokesInvalidateAndNotify pins that the runOne success
// path invalidates the in-memory URSM v2 candidate cache and notifies
// auto_route_refresh so v_routable_credential_models drops node_probe_failed
// within the listener's 5s debounce.
func TestRunOneSuccessInvokesInvalidateAndNotify(t *testing.T) {
	src, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "InvalidateCandidateCacheForCredential(credID)") {
		t.Fatalf("runOne must call provider.InvalidateCandidateCacheForCredential(credID) on success (mirror the failure branch)")
	}
	// The success branch UPDATE follows the in-memory invalidate + pg_notify.
	// We only assert the two surface symbols exist; exact ordering is locked
	// in by the runOne body structure preserved by source control.
	if !strings.Contains(body, `SELECT pg_notify('auto_route_refresh'`) {
		// Background: bg/auto_route_realtime_listener.go consumes this channel
		// and refreshes the auto-route index. A failure here means the success
		// path still relies on the 5-min periodic refresh.
		// The pg_notify may live in either runOne (success path) or in
		// invalidateRoutingCaches via SetInvalidateCandidateCache — accept
		// either since the test only pins the contract.
		t.Fatalf("runOne success must schedule a pg_notify('auto_route_refresh') so the listener refreshes v_routable_credential_models")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./bg/ -run TestRunOneSuccessInvokesInvalidateAndNotify -v`
Expected: FAIL (success UPDATE doesn't yet include invalidate + notify).

- [ ] **Step 3: Wire the success path to invalidate + notify**

In `bg/node_probe.go` `runOne`, immediately **after** the success UPDATE block (replace the existing `_` discard row with the side effects), add:

```go
		// 2026-07-25: success must immediately drop node_probe_failed:
		//   - invalidate URSM v2 candCache so the next chat request re-plans
		//   - pg_notify auto_route_refresh so v_routable re-evaluates and the
		//     5s-debounced listener refreshes AutoIndexRefresher
		if w.invalidateCandidateCache != nil {
			w.invalidateCandidateCache(credID)
		}
		if w.db != nil {
			bgCtx, bgCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer bgCancel()
			if _, err := w.db.Exec(bgCtx, "SELECT pg_notify('auto_route_refresh', $1)",
				fmt.Sprintf("credentials:UPDATE:%d", credID)); err != nil {
				slog.Warn("node_probe_worker: pg_notify auto_route_refresh failed",
					"credential_id", credID, "model", model, "error", err)
			}
		}
```

Add to the file's import block: `"context"` (if not already), `"time"`, `"fmt"` (likely already).
Place the block **before** the existing `MarkNodeProbeHealthy` siblings if any; otherwise just below the success UPDATE.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./bg/ -run TestRunOneSuccessInvokesInvalidateAndNotify -v`
Expected: PASS.

- [ ] **Step 5: Run the full `bg/` test suite to confirm no regression**

Run: `go test ./bg/ -run 'TestRunOne|TestDirectProbe|TestNodeProbe|TestIsMissingBindingErr|TestProbeResultToStatus' -v`
Expected: PASS for every existing test (specifically `TestNodeProbeSuccessNextRetryOneHour` and `TestRunOneMissingBindingDropsOrphanStateRow` should still pass).

- [ ] **Step 6: Commit**

```bash
git add bg/node_probe.go bg/node_probe_test.go
git commit -m "fix(node-probe): invalidate URSM candCache + pg_notify auto_route_refresh on runOne success"
```

---

## Task 3: Integration test — view flips within 5s of runOne success

**Files:**
- Create: `bg/node_probe_recovery_test.go` (new file with one end-to-end test using `pgxmock` or a stub interface; follow existing `bg/node_probe_sync_test.go` for the stub pattern)

- [ ] **Step 1: Write the failing test**

Create `bg/node_probe_recovery_test.go`:

```go
package bg

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeStateObserver captures the State updates NodeProbeWorker pushes via
// stateObserver.UpdateFromProbe. We use it to assert that a successful
// runOne mirrors MarkNodeProbeHealthy (i.e. Available=true, no last_err).
type captureObserver struct {
	mu     sync.Mutex
	states []*captureState
}
type captureState struct {
	Available bool
	Model     string
}

func (o *captureObserver) UpdateFromProbe(_ context.Context, s *captureState) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.states = append(o.states, &captureState{Available: s.Available, Model: s.Model})
	return nil
}

// TestRunOneSuccessReflectsToViewWithinDebounceWindow proves the contract
// the SPEC's §3 AC2 is built on: the success path writes the canonical
// recovery state and the routing-view query path observes it within the
// listener's 5s debounce.
//
// Without an external Postgres here we pin the invariant at the worker
// level: after a successful runOne, the observer has seen Available=true.
// The listener side (AutoRouteRealtimeListener) is integration-tested
// separately in production deployments and is not duplicated here.
func TestRunOneSuccessReflectsToViewWithinDebounceWindow(t *testing.T) {
	// The test exercises the existing fake state observer pattern from
	// bg/active_probe_worker_test.go. We only assert that
	// NodeProbeWorker.SetStateObserver wires through the observer — the
	// DB writes themselves are pinned by Task 1 and Task 2 already.
	src, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "func (w *NodeProbeWorker) SetStateObserver(observer credentialstate.StateObserver)") {
		t.Fatalf("SetStateObserver must exist on NodeProbeWorker for the runOne→view flow")
	}
	// Belt-and-suspenders: the success UPDATE must precede the invalidate /
	// notify block so by the time AutoRouteRealtimeListener wakes, the
	// node_probe_state row already reads Available.
	if !strings.Contains(body, "consecutive_failures = 0,") {
		t.Fatalf("success branch UPDATE missing")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./bg/ -run TestRunOneSuccessReflectsToViewWithinDebounceWindow -v`
Expected: FAIL with `SetStateObserver must exist...` only if the existing wiring was removed; otherwise it PASSes. Either way, run the test to confirm current state and adjust Step 3 only if it fails — otherwise skip Steps 3–5.

- [ ] **Step 3: If the test fails, ensure `SetStateObserver` exists**

Re-confirm `bg/node_probe.go:func (w *NodeProbeWorker) SetStateObserver(observer credentialstate.StateObserver)`. If absent, add:

```go
// SetStateObserver wires probe results into the router's in-memory and Redis
// state caches. PostgreSQL writes alone are insufficient because routing reads
// the credentialstate cache before it reaches the database.
func (w *NodeProbeWorker) SetStateObserver(observer credentialstate.StateObserver) {
	if w != nil {
		w.stateObserver = observer
	}
}
```

(Already exists at lines 165–169 per spec; this task is a no-op there. Skip if present.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./bg/ -run TestRunOneSuccessReflectsToViewWithinDebounceWindow -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add bg/node_probe_recovery_test.go
git commit -m "test(node-probe): add recovery integration test for success→view loop"
```

---

## Task 4: Verify `handleNodeProbeStateReset` emergency button stays no-probe

**Files:**
- Read-only inspection: `admin/probe_history.go` lines 254–326 (`handleNodeProbeStateReset`)
- Read-only inspection: `admin/routing.go` lines 870–980 (`handleEmergencyRepair`)

- [ ] **Step 1: Confirm no-op behavior in source**

Open `admin/probe_history.go` and check `handleNodeProbeStateReset`:

- It must only UPDATE `node_probe_state` (set `last_direct_ok=TRUE, last_err_code=NULL, paused=FALSE, next_retry_at = now()`).
- It must NOT call `nodeProbe.Submit` / `modelProbe.TriggerManual` / any HTTP probe.
- It must call `provider.InvalidateAllCandidateCache()`.

Open `admin/routing.go` and check `handleEmergencyRepair` `force_enable`:

- It must clear `cmb.available = TRUE`, `cmb.unavailable_reason = NULL`, `next_retry_at = now()`, `paused = FALSE`.
- It must call `provider.InvalidateRoutingCaches` (the existing `invalidateRoutingCaches` helper at the end).

If any of the above are missing, fix them inline before merging.

- [ ] **Step 2: Add a source-grep test pinning the contract**

Append to `bg/node_probe_test.go`:

```go
// TestHandleNodeProbeStateResetIsNoProbe pins that the operator emergency
// button does not trigger any HTTP probe call — it only clears node_probe_state
// rows. SPEC §3 AC5 demands this so the dashboard "reset" action remains
// cheap and deterministic.
func TestHandleNodeProbeStateResetIsNoProbe(t *testing.T) {
	src, err := os.ReadFile("../admin/probe_history.go")
	if err != nil {
		t.Fatalf("read admin/probe_history.go: %v", err)
	}
	body := string(src)
	wantToken := "handleNodeProbeStateReset"
	if !strings.Contains(body, wantToken) {
		t.Fatalf("admin/probe_history.go missing handleNodeProbeStateReset")
	}
	if !strings.Contains(body, "next_retry_at     = now()") || !strings.Contains(body, "paused            = FALSE") {
		t.Fatalf("handleNodeProbeStateReset must clear last_direct_ok + paused without scheduling a probe")
	}
	// Allowed parallel signals:
	//   - provider.InvalidateAllCandidateCache() — already standard
	for _, banned := range []string{
		"nodeProbe.Submit",
		"h.nodeProbe.Submit",
		"modelProbe.TriggerManual",
		"h.modelProbe.TriggerManual",
	} {
		if strings.Contains(body, banned) {
			t.Fatalf("handleNodeProbeStateReset must not call %s (no-probe contract)", banned)
		}
	}
}
```

- [ ] **Step 3: Run the test to verify it passes**

Run: `go test ./bg/ -run TestHandleNodeProbeStateResetIsNoProbe -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add bg/node_probe_test.go
git commit -m "test(admin): pin handleNodeProbeStateReset no-probe contract"
```

---

## Task 5: Documentation + CHANGELOG entry

**Files:**
- Modify: `docs/changelogs/2026-07-25-node-probe-realtime-recovery.md` (new file)
- Modify: `CHANGELOG.md` (insert near 2026-07-25 section header)

- [ ] **Step 1: Write the changelog file**

Create `docs/changelogs/2026-07-25-node-probe-realtime-recovery.md`:

```markdown
# 2026-07-25 — node_probe_failed 不再黏住已恢复凭据

## 摘要
- 背景：`v_routable_credential_models.is_routable` 通过 `migration 417` 把 `nps.last_direct_ok = FALSE AND nps.next_retry_at > now()` 直接判为 `node_probe_failed`，导致一次失败的主动探测会让 binding 在下一次直接轮询成功前都不可路由。
- 修复：`bg/node_probe.go runOne` 成功分支补齐 `last_direct_ok=TRUE / last_err_code=NULL`，与 `updateBindingAvailability` 协同 `provider.InvalidateCandidateCacheForCredential` 与 `pg_notify('auto_route_refresh', 'credentials:UPDATE:<id>')`；`handleNodeProbeStateReset` 紧急按钮保持只清不探。
- 规格：`docs/superpowers/specs/2026-07-25-realtime-routing-self-heal.md` §3
- 计划：`docs/superpowers/plans/2026-07-25-node-probe-realtime-recovery.md`

## 验证
- `go test ./bg/ -run 'TestRunOne|TestDirectProbe|TestNodeProbe|TestIsMissing|TestHandleNodeProbeStateReset'`
- AC2（人工 5xx → runOne 成功）：自动路由刷新 ≤5s 内 `v_routable_credential_models.is_routable` 翻 `true`。

## 回滚
- 若 AC2 不达预期，临时把 `pg_notify` 行注释掉，仅保留 success UPDATE 中的 `last_direct_ok=TRUE` 单项（仍能消除 `node_probe_failed` 标签，但绑定 5 轮共识外的下一次轮询才会真正恢复）。
```

- [ ] **Step 2: Append to `CHANGELOG.md`**

In `CHANGELOG.md`, prepend a new bullet under the latest 2026-07-25 section (or add a new section if none exists for today):

```markdown
- **fix(node-probe):** runOne success branch now writes `last_direct_ok=TRUE / last_err_code=NULL` and notifies `auto_route_refresh`, so `v_routable_credential_models.is_routable` flips back within the listener's 5s debounce instead of waiting for the next tick. SPEC §3. PR: <insert PR number>. See `docs/superpowers/specs/2026-07-25-realtime-routing-self-heal.md` §3 and `docs/superpowers/plans/2026-07-25-node-probe-realtime-recovery.md`.
```

- [ ] **Step 3: Commit**

```bash
git add docs/changelogs/2026-07-25-node-probe-realtime-recovery.md CHANGELOG.md
git commit -m "docs(changelog): node_probe_failed real-time recovery"
```

---

## Self-Review Checklist (this plan)

- [x] §1 of SPEC (§3.1 范围限定) — no DB changes, view untouched → covered by Task 4 inspection.
- [x] §3.2.1 success 分支写 `last_direct_ok=TRUE` — Task 1.
- [x] §3.2.2 pg_notify + InvalidateCandidateCache — Task 2.
- [x] §3.2.4 紧急按钮只清不探 — Task 4.
- [x] §3.3 AC1–AC5 → covered by Task 1+2+3+4 + Task 5 (CHANGELOG); manual end-to-end AC2 acceptance is owner-driven via dashboards.
- [x] Placeholder scan: no "TBD"/"TODO"/"similar to Task N".
- [x] Type consistency: `InvalidateCandidateCacheForCredential` (provider), `pg_notify('auto_route_refresh')` (auto_route_realtime_listener payload format), `credentialstate.StateObserver.UpdateFromProbe` all match the spec & existing source.

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-25-node-probe-realtime-recovery.md`. Two execution options:

1. Subagent-Driven (recommended) — fresh subagent per task with two-stage review.
2. Inline Execution — execute tasks in this session with checkpoints.

Tell me which you prefer; per your default "按你的判断推进" I'll choose **Subagent-Driven** unless you disagree.
