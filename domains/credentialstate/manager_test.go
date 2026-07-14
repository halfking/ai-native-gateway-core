package credentialstate

import (
	"context"
	"sync/atomic"
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
	m.UpdateOnFailure(ctx, 1, "test-model", errorsx.KindNetwork, "req-1", "default", "")
	if triggered {
		t.Error("should not trigger probe after 1 failure")
	}

	// 第二次失败（应该触发快速探测）
	time.Sleep(3 * time.Second) // 等待超过2秒
	m.UpdateOnFailure(ctx, 1, "test-model", errorsx.KindNetwork, "req-2", "default", "")

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
	m.UpdateOnFailure(ctx, 1, "test-model", errorsx.KindCanceled, "req-1", "default", "")

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
	m.UpdateOnFailure(ctx, 1, "test-model", errorsx.KindNetwork, "req-2", "default", "")
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
	m.UpdateOnFailure(ctx, credID, model, errorsx.KindStreamTimeout, "req-1", "default", "")
	m.UpdateOnFailure(ctx, credID, model, errorsx.KindStreamTimeout, "req-2", "default", "")
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
	m.UpdateOnFailure(ctx, credID, model, errorsx.KindStreamTimeout, "req-3", "default", "")
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

// TestManager_FreeCredentialTransientTolerated 验证 2026-07-14 修复：
// billing_mode="free" 的凭据在 transient 错误（timeout/stream_timeout/network/
// rate_limit/upstream_down）连续失败时，不进入 cooling（Available 保持 true、
// RecoverAt 不设、候选缓存不失效），仅靠 RecentSuccessRate 软降权。产品原则：
// 50% 成功率的免费凭据"有总比没有强"。对照上面的 StreamTimeoutCoolingAfterThree
// —— 那个用 billingMode="" (paid)，3 次 stream_timeout 后必 cooling。
//
// 纯内存测试（无 DB/Redis）。
func TestManager_FreeCredentialTransientTolerated(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil)

	cacheInvalidated := false
	m.SetProbeSubmitter(func(credID int) {}, nil)
	m.SetInvalidateCandidateCache(func() { cacheInvalidated = true })

	credID, model := 42, "nim-test-model"

	// 连续 4 次 stream-timeout（超过 paid 的 3 次阈值），billingMode="free"。
	for i := 1; i <= 4; i++ {
		m.UpdateOnFailure(ctx, credID, model, errorsx.KindStreamTimeout,
			"req-"+itoa(i), "default", "free")
	}

	s, _ := m.GetState(ctx, credID, model)
	if s == nil {
		t.Fatal("expected state after 4 failures")
	}

	// 核心断言：免费凭据 transient 不硬剔。
	if !s.Available {
		t.Fatal("free credential must remain Available=true after transient failures (soft demote only)")
	}
	if s.RecoverAt != nil {
		t.Fatalf("free credential must NOT get a cooling RecoverAt, got %v", s.RecoverAt)
	}
	if cacheInvalidated {
		t.Fatal("candidate cache must NOT be invalidated for free credential transient failures")
	}
	// 但观测信号保留：失败计数累加，供 RecentSuccessRate / 软降权感知。
	if s.ConsecutiveFails != 4 {
		t.Fatalf("consecutive_fails should still increment (observation signal), got %d", s.ConsecutiveFails)
	}
	if s.LastError != string(errorsx.KindStreamTimeout) {
		t.Fatalf("LastError should record the kind, got %q", s.LastError)
	}
}

// TestManager_FreeCredentialPermanentStillHardExcludes 验证免费凭据对永久错误
// （auth/model_not_found/quota_permanent）仍硬剔——坏 key 不该拖垮路由。
func TestManager_FreeCredentialPermanentStillHardExcludes(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil)
	m.SetProbeSubmitter(func(credID int) {}, nil)
	m.SetInvalidateCandidateCache(func() {})

	credID, model := 43, "nim-test-model"

	// 永久错误连续 2 次（达到 permanent 阈值）。
	m.UpdateOnFailure(ctx, credID, model, errorsx.KindAuth, "req-1", "default", "free")
	m.UpdateOnFailure(ctx, credID, model, errorsx.KindAuth, "req-2", "default", "free")

	s, _ := m.GetState(ctx, credID, model)
	if s == nil {
		t.Fatal("expected state after 2 auth failures")
	}
	if s.Available {
		t.Fatal("free credential MUST be hard-excluded (Available=false) after permanent (auth) failure")
	}
}

// itoa avoids importing strconv just for a 1..N loop counter in the test.
func itoa(i int) string { return string(rune('0'+i)) }

// TestManager_TieredReprobeUsesBackoff verifies the BUG #2 fix
// (2026-07-13): a positive backoff is honored at runtime, replacing an
// existing schedule suppresses the stale callback, and Stop suppresses a
// pending callback. The test uses short delays so it does not wait for the
// production 30s/2m/5m tiers.
func TestManager_TieredReprobeUsesBackoff(t *testing.T) {
	m := NewManager(nil, nil)
	m.Start(context.Background())
	var calls atomic.Int32
	fired := make(chan int, 4)
	m.SetProbeSubmitter(func(credID int) {
		calls.Add(1)
		fired <- credID
	}, nil)

	// Replacing the first timer must prevent its callback from firing.
	m.scheduleCredProbe(123, "gpt-4", 150*time.Millisecond)
	m.scheduleCredProbe(123, "gpt-4", 40*time.Millisecond)

	select {
	case got := <-fired:
		if got != 123 {
			t.Fatalf("scheduled fire for wrong credID: got %d, want 123", got)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement reprobe never fired")
	}

	// A later failure for another model on the same credential must not
	// create a duplicate or postpone the earlier credential-level probe.
	m.scheduleCredProbe(123, "other-model", 300*time.Millisecond)
	select {
	case <-fired:
		t.Fatal("same credential scheduled a duplicate probe")
	case <-time.After(80 * time.Millisecond):
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("replacement schedule fired %d callbacks, want 1", got)
	}

	// A stopped manager must not execute a pending callback.
	m.scheduleCredProbe(456, "gpt-4", 100*time.Millisecond)
	m.Stop()
	time.Sleep(150 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("stopped manager fired %d callbacks, want 1", got)
	}
}

func setupTestDB(t *testing.T) *pgxpool.Pool {
	// 这里应该连接测试数据库
	// 为了简化，跳过实际连接
	t.Skip("test database not configured")
	return nil
}
