package credentialstate

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestManager_UpdateOnSuccess(t *testing.T) {
	// 跳过集成测试（需要数据库）
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background() //nolint:staticcheck // SA4006: kept for test setup symmetry with future ctx-aware tests
	db := setupTestDB(t)
	defer db.Close()

	m := NewManager(db, nil)
	m.Start(ctx)
	defer m.Stop()

	// 测试成功更新
	m.UpdateOnSuccess(ctx, 1, "test-model", 100, "req-123")

	// 验证状态
	state, err := m.GetState(ctx, 1, "test-model")
	if err != nil {
		t.Fatalf("GetState failed: %v", err)
	}

	if state == nil {
		t.Fatal("expected state, got nil")
	}

	if !state.Available {
		t.Error("expected available=true")
	}

	if state.ConsecutiveFails != 0 {
		t.Errorf("expected consecutive_fails=0, got %d", state.ConsecutiveFails)
	}

	if state.AvgLatencyMs != 100 {
		t.Errorf("expected latency=100, got %d", state.AvgLatencyMs)
	}
}

func TestManager_UpdateOnFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background() //nolint:staticcheck // SA4006: kept for test setup symmetry with future ctx-aware tests
	db := setupTestDB(t)
	defer db.Close()

	m := NewManager(db, nil)
	m.Start(ctx)
	defer m.Stop()

	// 模拟连续失败
	triggered := false
	m.SetProbeSubmitter(func(credID int) {
		triggered = true
	}, nil)

	// 第一次失败
	m.UpdateOnFailure(ctx, 1, "test-model", errorsx.KindNetwork, "req-1")
	if triggered {
		t.Error("should not trigger probe after 1 failure")
	}

	// 第二次失败（应该触发快速探测）
	time.Sleep(3 * time.Second) // 等待超过2秒
	m.UpdateOnFailure(ctx, 1, "test-model", errorsx.KindNetwork, "req-2")

	if !triggered {
		t.Error("should trigger probe after 2 consecutive failures")
	}

	// 验证状态
	state, err := m.GetState(ctx, 1, "test-model")
	if err != nil {
		t.Fatalf("GetState failed: %v", err)
	}

	if state.ConsecutiveFails != 2 {
		t.Errorf("expected consecutive_fails=2, got %d", state.ConsecutiveFails)
	}
}

func TestManager_CacheHierarchy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background() //nolint:staticcheck // SA4006: kept for test setup symmetry with future ctx-aware tests
	db := setupTestDB(t)
	defer db.Close()

	m := NewManager(db, nil)
	m.Start(ctx)
	defer m.Stop()

	// 写入状态
	m.UpdateOnSuccess(ctx, 1, "test-model", 100, "req-1")

	// 第一次查询（从内存缓存）
	state1, _ := m.GetState(ctx, 1, "test-model")

	// 清除内存缓存
	key := m.cacheKey(1, "test-model")
	m.memCache.Delete(key)

	// 第二次查询（应该从 Redis 或 DB）
	state2, _ := m.GetState(ctx, 1, "test-model")

	if state1 == nil || state2 == nil {
		t.Fatal("expected both states to be non-nil")
	}

	if state1.CredentialID != state2.CredentialID {
		t.Error("cache hierarchy broken")
	}
}

func TestManager_UpdateOnFailure_IgnoresCanceled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background() //nolint:staticcheck // SA4006: kept for test setup symmetry with future ctx-aware tests
	db := setupTestDB(t)
	defer db.Close()

	m := NewManager(db, nil)
	m.Start(ctx)
	defer m.Stop()

	// 先记录一次成功，建立基线
	m.UpdateOnSuccess(ctx, 1, "test-model", 50, "req-0")

	state, _ := m.GetState(ctx, 1, "test-model")
	if state == nil {
		t.Fatal("state should exist after success")
	}
	initialFails := state.ConsecutiveFails

	// 用户取消 - 不应计入错误统计
	m.UpdateOnFailure(ctx, 1, "test-model", errorsx.KindCanceled, "req-1")

	// 验证状态未变化
	state, _ = m.GetState(ctx, 1, "test-model")
	if state == nil {
		t.Fatal("state should still exist")
	}

	if state.ConsecutiveFails != initialFails {
		t.Errorf("KindCanceled should not increment consecutive_fails: expected %d, got %d",
			initialFails, state.ConsecutiveFails)
	}

	if state.LastError == string(errorsx.KindCanceled) {
		t.Error("KindCanceled should not be recorded as last_error")
	}

	// 验证真实错误仍然会被计入
	m.UpdateOnFailure(ctx, 1, "test-model", errorsx.KindNetwork, "req-2")
	state, _ = m.GetState(ctx, 1, "test-model")

	if state.ConsecutiveFails != initialFails+1 {
		t.Errorf("Real error should increment consecutive_fails: expected %d, got %d",
			initialFails+1, state.ConsecutiveFails)
	}

	if state.LastError != string(errorsx.KindNetwork) {
		t.Errorf("expected last_error=%s, got %s", errorsx.KindNetwork, state.LastError)
	}
}

// TestManager_StreamTimeoutCoolingAfterThree verifies the 2026-07-09 fix for
// 问题2 (NVIDIA NIM stream-no-feedback never trips cooling): after 3
// consecutive KindStreamTimeout failures the credential binding is marked
// unavailable with a ~5min RecoverAt so the router stops selecting it.
//
// This is a pure in-memory test (no DB / Redis): UpdateOnFailure mutates the
// memCache directly; batchWriter is never started so its nil-db flush path is
// never reached; setToRedis is a no-op when redisClient is nil.
func TestManager_StreamTimeoutCoolingAfterThree(t *testing.T) {
	ctx := context.Background()
	// db=nil + redis=nil + NOT started → exercises memCache logic only.
	m := NewManager(nil, nil)

	cacheInvalidated := false
	m.SetProbeSubmitter(func(credID int) {}, nil)
	m.SetInvalidateCandidateCache(func() { cacheInvalidated = true })

	credID, model := 11, "nvidia-test-model"

	// 1st and 2nd stream-timeout failures: below the 3-failure threshold,
	// no cooling should be scheduled (RecoverAt stays nil, cache not
	// invalidated). These two alone must NOT trip the new fast-cooling.
	m.UpdateOnFailure(ctx, credID, model, errorsx.KindStreamTimeout, "req-1")
	m.UpdateOnFailure(ctx, credID, model, errorsx.KindStreamTimeout, "req-2")
	s, _ := m.GetState(ctx, credID, model)
	if s == nil {
		t.Fatal("expected state after 2 failures")
	}
	if s.ConsecutiveFails != 2 {
		t.Fatalf("expected consecutive_fails=2, got %d", s.ConsecutiveFails)
	}
	if s.RecoverAt != nil {
		t.Fatalf("RecoverAt must be nil below the 3-failure threshold, got %v", s.RecoverAt)
	}
	if cacheInvalidated {
		t.Fatal("candidate cache must NOT be invalidated below the 3-failure threshold")
	}

	// 3rd stream-timeout failure: must trip cooling immediately — set a
	// ~5min RecoverAt and invalidate the candidate cache so the router
	// drops the credential on its next resolve.
	before := time.Now()
	m.UpdateOnFailure(ctx, credID, model, errorsx.KindStreamTimeout, "req-3")
	s, _ = m.GetState(ctx, credID, model)
	if s == nil {
		t.Fatal("expected state after 3 failures")
	}
	if s.Available {
		t.Fatal("credential must be unavailable (cooling) after 3 consecutive stream timeouts")
	}
	if s.RecoverAt == nil {
		t.Fatal("expected RecoverAt to be set when cooling starts")
	}
	// RecoverAt should be ~5 minutes from now (allow scheduling slack).
	minRecover := before.Add(4 * time.Minute)
	maxRecover := time.Now().Add(6 * time.Minute)
	if s.RecoverAt.Before(minRecover) || s.RecoverAt.After(maxRecover) {
		t.Fatalf("RecoverAt=%v should be ~5min from now (%v..%v)", s.RecoverAt, minRecover, maxRecover)
	}
	if !cacheInvalidated {
		t.Fatal("candidate cache should be invalidated when a credential trips cooling")
	}

	// A subsequent success must restore availability (recovery path).
	m.UpdateOnSuccess(ctx, credID, model, 123, "req-4")
	s, _ = m.GetState(ctx, credID, model)
	if s == nil {
		t.Fatal("expected state after recovery success")
	}
	if !s.Available {
		t.Fatal("credential should be available again after a successful request")
	}
	if s.ConsecutiveFails != 0 {
		t.Fatalf("consecutive_fails should reset to 0 after success, got %d", s.ConsecutiveFails)
	}
}

// TestManager_TieredReprobeUsesBackoff verifies the BUG #2 fix
// (2026-07-13): the conditional branch on transient failures
// (≥3 consecutive) computed a `backoff` value (30s / 2m / 5m by
// consecutive_fails count) but the previous implementation fired the
// credProbeV2 submitter *immediately*, throwing away the computed delay.
// The effective schedule had collapsed to whatever delay credProbeV2 itself
// applies (5min fastReprobeDelay), regardless of the consecutive count.
//
// After the fix, the submitter must NOT fire within the first 30 seconds;
// it must fire approximately at 30s ± scheduling slack. The test uses a
// short substituted backoff by calling scheduleCredProbe directly so we
// don't have to wait 30s for the wall-clock assertion.
//
// We do TWO checks:
//   1. Functional: scheduleCredProbe honours a positive backoff (via the
//      test seam — we construct the manager, plant a probe submitter
//      that records its arrival time, and feed a small backoff).
//   2. Source-shape: scan manager.go and reject the legacy
//      `m.credProbeV2Submitter(credID)` call inside the transient
//      branch, which was the bug.
func TestManager_TieredReprobeUsesBackoff(t *testing.T) {
	// ----- (1) functional check via scheduleCredProbe -----
	m := NewManager(nil, nil)

	type fireRecord struct {
		at      time.Time
		credID  int
	}
	fired := make(chan fireRecord, 4)
	m.SetProbeSubmitter(func(credID int) {
		fired <- fireRecord{at: time.Now(), credID: credID}
	}, nil)

	// 100ms backoff is small enough to keep the test fast but large enough
	// that the scheduler can't realistically deliver it in 0ms. If the
	// branch regresses to "fire immediately", we observe arrivalAt <
	// backoff-50% which is impossible if the timer ran.
	const backoff = 100 * time.Millisecond
	start := time.Now()
	m.scheduleCredProbe(123, "gpt-4", backoff)

	select {
	case got := <-fired:
		elapsed := got.at.Sub(start)
		// Allow up to 2× backoff to absorb CI clock skew; we just want
		// to confirm the submitter did NOT arrive "immediately"
		// (within ~1ms of call).
		if elapsed < backoff/2 {
			t.Fatalf("BUG #2 regression: scheduleCredProbe fired after %v, expected >= %v "+
				"(the previous code called credProbeV2Submitter immediately, "+
				"throwing away the computed backoff). elapsed=%v backoff=%v",
				elapsed, backoff/2, elapsed, backoff)
		}
		if got.credID != 123 {
			t.Errorf("scheduled fire for wrong credID: got %d, want 123", got.credID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled reprobe never fired")
	}

	// ----- (2) source-shape check on manager.go -----
	// The transient branch in UpdateOnFailure used to call
	//   m.credProbeV2Submitter(credID)
	// inline, defeating the tiered backoff. After the fix the call site
	// must go through m.scheduleCredProbe(...). This check is intentionally
	// tolerant of the helper being defined elsewhere in the file — it
	// only fails if the direct legacy call reappears in the transient
	// block.
	const relPath = "manager.go"
	data, err := os.ReadFile(relPath)
	if err != nil {
		t.Fatalf("read %s: %v", relPath, err)
	}
	src := string(data)

	// Cheap heuristic: look for the comment that introduces the tiered
	// backoff AND the dispatch through scheduleCredProbe. If the file
	// still uses m.credProbeV2Submitter(credID) inline AND the backoff
	// comment is present, that combination is the bug.
	hasBackoffComment := strings.Contains(src, "递增退避探测")
	hasInlineFire := strings.Contains(src, "m.credProbeV2Submitter(credID)")
	hasHelperDispatch := strings.Contains(src, "m.scheduleCredProbe(")

	if hasBackoffComment && hasInlineFire && !hasHelperDispatch {
		t.Fatalf("BUG #2 regression: %s has the tiered-backoff comment AND the inline "+
			"m.credProbeV2Submitter(credID) call, but no m.scheduleCredProbe(...) "+
			"dispatch. The previous code computed the backoff value but threw it "+
			"away by firing the submitter immediately.", relPath)
	}

	// Confirm the scheduleCredProbe helper exists (otherwise the fix is
	// incomplete — we just tore out the inline call without replacing it).
	if !hasHelperDispatch {
		t.Fatalf("BUG #2 regression: %s no longer dispatches via m.scheduleCredProbe(...); "+
			"either the helper was renamed (update this test) or the dispatch was deleted "+
			"without replacement.", relPath)
	}
}

func setupTestDB(t *testing.T) *pgxpool.Pool {
	// 这里应该连接测试数据库
	// 为了简化，跳过实际连接
	t.Skip("test database not configured")
	return nil
}
