package routing

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// ───────────────────────────────────────────────────────────────────────────
// RouteNodeState 纯函数测试（不依赖 Redis）
// ───────────────────────────────────────────────────────────────────────────

func newTestState(credID int, model string) *RouteNodeState {
	return &RouteNodeState{
		CredentialID: credID,
		Model:        model,
		SlideWindow:  make([]RouteNodeRecord, 0, 16),
	}
}

func TestRouteNodeState_IsUsable_NoState(t *testing.T) {
	// nil state 视作可用（首次访问）
	var s *RouteNodeState
	if !s.IsUsable(time.Now(), DefaultRouteNodeConfig()) {
		t.Fatal("nil state should be usable")
	}
}

func TestRouteNodeState_IsUsable_FreshState(t *testing.T) {
	s := newTestState(1, "minimax-m3")
	if !s.IsUsable(time.Now(), DefaultRouteNodeConfig()) {
		t.Fatal("fresh state should be usable")
	}
}

func TestRouteNodeState_ConsecutiveFailureStreak_Basic(t *testing.T) {
	s := newTestState(1, "minimax-m3")
	cfg := DefaultRouteNodeConfig()
	now := time.Now()

	// 3 次失败
	for i := 0; i < 3; i++ {
		s.RecordFailure(now, "req-1", "rate_limit", cfg)
	}
	if streak := s.ConsecutiveFailureStreak(now, cfg); streak != 3 {
		t.Fatalf("streak=%d, want 3", streak)
	}

	// IsUsable 应返回 false
	if s.IsUsable(now, cfg) {
		t.Fatal("state should be unusable after 3 consecutive failures")
	}

	// Disabled 状态被设置
	if !s.Disabled {
		t.Fatal("state should be Disabled")
	}
	if s.DisabledUntil.IsZero() {
		t.Fatal("DisabledUntil should be set")
	}
}

func TestRouteNodeState_Streak_ResetAfterSuccess(t *testing.T) {
	s := newTestState(1, "minimax-m3")
	cfg := DefaultRouteNodeConfig()
	now := time.Now()

	// F-F-F 然后 S
	s.RecordFailure(now, "req-1", "rate_limit", cfg)
	s.RecordFailure(now.Add(time.Second), "req-2", "rate_limit", cfg)
	s.RecordFailure(now.Add(2*time.Second), "req-3", "rate_limit", cfg)

	if !s.Disabled {
		t.Fatal("should be disabled after 3 failures")
	}

	// 模拟 5 分钟后冷却到期
	future := s.DisabledUntil.Add(time.Second)
	s.RecordSuccess(future, "req-success", cfg)

	// Disabled 应被清空
	if s.Disabled {
		t.Fatal("Disabled should be cleared after success")
	}
	if s.FailureCount != 0 {
		t.Fatalf("FailureCount should reset to 0, got %d", s.FailureCount)
	}

	// 现在 streak 是 0（最新是 success）
	if streak := s.ConsecutiveFailureStreak(future, cfg); streak != 0 {
		t.Fatalf("streak=%d, want 0", streak)
	}
}

func TestRouteNodeState_Streak_OldRecordsPruned(t *testing.T) {
	s := newTestState(1, "minimax-m3")
	cfg := DefaultRouteNodeConfig()
	now := time.Now()

	// 6 分钟前 3 次失败
	oldTime := now.Add(-6 * time.Minute)
	s.RecordFailure(oldTime, "req-old-1", "rate_limit", cfg)
	s.RecordFailure(oldTime.Add(time.Second), "req-old-2", "rate_limit", cfg)
	s.RecordFailure(oldTime.Add(2*time.Second), "req-old-3", "rate_limit", cfg)

	// 这些失败超过窗口，Prune 后 streak 应为 0
	if streak := s.ConsecutiveFailureStreak(now, cfg); streak != 0 {
		t.Fatalf("old records should be pruned, streak=%d", streak)
	}
}

func TestRouteNodeState_MixedSequence(t *testing.T) {
	s := newTestState(1, "minimax-m3")
	cfg := DefaultRouteNodeConfig()
	now := time.Now()

	// F-F-F-S-F-F（最新连续 2 次失败）
	s.RecordFailure(now, "r1", "rate_limit", cfg)
	s.RecordFailure(now.Add(time.Second), "r2", "rate_limit", cfg)
	s.RecordFailure(now.Add(2*time.Second), "r3", "rate_limit", cfg)

	// 验证：3 次失败触发 Disabled
	if !s.Disabled {
		t.Fatal("should be disabled after 3 failures")
	}

	// 第 4 次成功清掉 Disabled（V3 语义：成功即恢复，不等冷却到期）
	s.RecordSuccess(now.Add(3*time.Second), "r4", cfg)
	if s.Disabled {
		t.Fatal("Disabled should be cleared by RecordSuccess")
	}

	// 第 5、6 次失败
	s.RecordFailure(now.Add(4*time.Second), "r5", "rate_limit", cfg)
	s.RecordFailure(now.Add(5*time.Second), "r6", "rate_limit", cfg)

	// streak=2（最新连续两次失败）
	if streak := s.ConsecutiveFailureStreak(now.Add(6*time.Second), cfg); streak != 2 {
		t.Fatalf("streak=%d, want 2", streak)
	}

	// IsUsable 在 streak<3 且未 Disabled 时返回 true
	if !s.IsUsable(now.Add(6*time.Second), cfg) {
		t.Fatal("state should be usable (Disabled cleared by success, streak<3)")
	}

	// 再失败 1 次（达到 streak=3）
	s.RecordFailure(now.Add(7*time.Second), "r7", "rate_limit", cfg)
	// 现在 streak = F(5)-F(6)-F(7) = 3
	if streak := s.ConsecutiveFailureStreak(now.Add(7*time.Second), cfg); streak != 3 {
		t.Fatalf("streak=%d, want 3", streak)
	}
	if s.IsUsable(now.Add(7*time.Second), cfg) {
		t.Fatal("state should be unusable after 3 consecutive failures")
	}
}

func TestRouteNodeState_AutoRecoverAfterCooldown(t *testing.T) {
	s := newTestState(1, "minimax-m3")
	cfg := DefaultRouteNodeConfig()
	now := time.Now()

	// 3 次失败触发 Disabled
	s.RecordFailure(now, "r1", "rate_limit", cfg)
	s.RecordFailure(now.Add(time.Second), "r2", "rate_limit", cfg)
	s.RecordFailure(now.Add(2*time.Second), "r3", "rate_limit", cfg)

	if !s.Disabled {
		t.Fatal("should be disabled")
	}

	// 冷却期内：不可用
	if s.IsUsable(s.DisabledUntil.Add(-time.Second), cfg) {
		t.Fatal("should be unusable during cooldown")
	}

	// 冷却到期：可用 + 自动恢复
	future := s.DisabledUntil.Add(time.Second)
	if !s.IsUsable(future, cfg) {
		t.Fatal("should be usable after cooldown")
	}
	if s.Disabled {
		t.Fatal("Disabled should be cleared after cooldown")
	}
}

func TestRouteNodeState_CustomConfig(t *testing.T) {
	cfg := RouteNodeConfig{
		WindowSeconds:    10 * time.Second,
		FailStreakLimit:  5,
		DisabledCooldown: 1 * time.Second,
	}
	s := newTestState(1, "minimax-m3")
	now := time.Now()

	// 4 次失败：streak=4 但 < 5，仍可用
	for i := 0; i < 4; i++ {
		s.RecordFailure(now.Add(time.Duration(i)*time.Second), "r", "rate_limit", cfg)
	}
	if !s.IsUsable(now, cfg) {
		t.Fatal("4 failures with limit=5 should still be usable")
	}

	// 第 5 次失败：触发 Disabled
	s.RecordFailure(now.Add(4*time.Second), "r5", "rate_limit", cfg)
	if !s.Disabled {
		t.Fatal("5 failures should trigger Disabled")
	}

	// 1 秒冷却后：Disabled 清空，但 streak 仍在窗口内（10s）
	// → IsUsable 仍返回 false（streak=5 >= limit=5）
	future := now.Add(2 * time.Second)
	if s.IsUsable(future, cfg) {
		t.Fatal("streak still >= 5 within 10s window, should be unusable")
	}

	// 等待窗口清空（11s 后）：恢复可用
	future2 := now.Add(11 * time.Second)
	if !s.IsUsable(future2, cfg) {
		t.Fatal("should be usable after window+cooldown expiry")
	}
}

func TestRouteNodeState_RecordSuccessAutoRecovers(t *testing.T) {
	s := newTestState(1, "minimax-m3")
	cfg := DefaultRouteNodeConfig()
	now := time.Now()

	// 触发 Disabled
	s.RecordFailure(now, "r1", "rate_limit", cfg)
	s.RecordFailure(now.Add(time.Second), "r2", "rate_limit", cfg)
	s.RecordFailure(now.Add(2*time.Second), "r3", "rate_limit", cfg)
	if !s.Disabled {
		t.Fatal("should be disabled")
	}

	// 在 Disabled 状态下记录成功：应自动恢复
	s.RecordSuccess(now.Add(3*time.Second), "r-success", cfg)
	if s.Disabled {
		t.Fatal("Disabled should be cleared by RecordSuccess")
	}
	if s.FailureCount != 0 {
		t.Fatalf("FailureCount should reset, got %d", s.FailureCount)
	}
}

// ───────────────────────────────────────────────────────────────────────────
// Pool 行为测试
// ───────────────────────────────────────────────────────────────────────────

func TestRouteNodeStatePool_AcquireRelease(t *testing.T) {
	s1 := acquireRouteNodeState(1, "minimax-m3")
	if s1.CredentialID != 1 || s1.Model != "minimax-m3" {
		t.Fatal("acquireRouteNodeState should set initial fields")
	}
	if len(s1.SlideWindow) != 0 {
		t.Fatalf("SlideWindow should be empty, got %d", len(s1.SlideWindow))
	}

	releaseRouteNodeState(s1)

	s2 := acquireRouteNodeState(2, "glm-4-flash")
	if s2.CredentialID != 2 || s2.Model != "glm-4-flash" {
		t.Fatal("reuse should reset fields")
	}
	if len(s2.SlideWindow) != 0 {
		t.Fatalf("SlideWindow should be empty after reuse, got %d", len(s2.SlideWindow))
	}
	releaseRouteNodeState(s2)
}

// ───────────────────────────────────────────────────────────────────────────
// Redis 集成测试（需要本地 Redis）
// ───────────────────────────────────────────────────────────────────────────

func getTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := "localhost:6379"
	c := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		t.Skipf("redis unavailable at %s, skipping: %v", addr, err)
	}
	return c
}

func TestRouteNodeStore_GetMiss(t *testing.T) {
	client := getTestRedis(t)
	defer client.Close()
	store := NewRouteNodeStore(client, DefaultRouteNodeConfig())
	ctx := context.Background()

	const credID = 88001
	const model = "minimax-m3-test"
	t.Cleanup(func() { _ = store.Delete(ctx, credID, model) })

	state, found, err := store.Get(ctx, credID, model)
	if err != nil {
		t.Fatalf("Get err=%v", err)
	}
	if found {
		t.Fatal("expected miss")
	}
	if state != nil {
		t.Fatalf("expected nil state, got %v", state)
	}
}

func TestRouteNodeStore_RecordAndGet(t *testing.T) {
	client := getTestRedis(t)
	defer client.Close()
	store := NewRouteNodeStore(client, DefaultRouteNodeConfig())
	ctx := context.Background()

	const credID = 88002
	const model = "minimax-m3-test-2"
	t.Cleanup(func() { _ = store.Delete(ctx, credID, model) })

	// 第一次成功
	state, err := store.RecordSuccess(ctx, credID, model, "req-1")
	if err != nil {
		t.Fatalf("RecordSuccess err=%v", err)
	}
	if state.SuccessCount != 1 {
		t.Fatalf("SuccessCount=%d, want 1", state.SuccessCount)
	}

	// Get 应能读到
	got, found, err := store.Get(ctx, credID, model)
	if err != nil || !found {
		t.Fatalf("Get err=%v found=%v", err, found)
	}
	if got.SuccessCount != 1 {
		t.Fatalf("SuccessCount=%d, want 1", got.SuccessCount)
	}

	// 3 次失败触发 Disabled
	_, justDisabled1, _ := store.RecordFailure(ctx, credID, model, "req-f1", "rate_limit")
	_, justDisabled2, _ := store.RecordFailure(ctx, credID, model, "req-f2", "rate_limit")
	_, justDisabled3, _ := store.RecordFailure(ctx, credID, model, "req-f3", "rate_limit")

	if justDisabled1 || justDisabled2 {
		t.Fatal("justDisabled should only be true on 3rd failure")
	}
	if !justDisabled3 {
		t.Fatal("3rd failure should trigger justDisabled")
	}

	// IsUsable 应返回 false
	if store.IsUsable(ctx, credID, model) {
		t.Fatal("node should be unusable after 3 failures")
	}

	// Delete 清理
	if err := store.Delete(ctx, credID, model); err != nil {
		t.Fatalf("Delete err=%v", err)
	}

	// 再 Get 应 miss
	_, found, _ = store.Get(ctx, credID, model)
	if found {
		t.Fatal("expected miss after Delete")
	}
}

func TestRouteNodeStore_FilterUsableCandidates(t *testing.T) {
	client := getTestRedis(t)
	defer client.Close()
	store := NewRouteNodeStore(client, DefaultRouteNodeConfig())
	ctx := context.Background()

	const credA = 88100
	const credB = 88101
	const model = "minimax-m3-filter"
	// 先清理可能残留的状态（避免测试间污染）
	_ = store.Delete(ctx, credA, model)
	_ = store.Delete(ctx, credB, model)
	t.Cleanup(func() {
		_ = store.Delete(ctx, credA, model)
		_ = store.Delete(ctx, credB, model)
	})

	// credA: 3 次失败 → 不可用
	_, _, _ = store.RecordFailure(ctx, credA, model, "a1", "rate_limit")
	_, _, _ = store.RecordFailure(ctx, credA, model, "a2", "rate_limit")
	_, justDisabled, _ := store.RecordFailure(ctx, credA, model, "a3", "rate_limit")
	if !justDisabled {
		t.Fatal("credA should be disabled")
	}

	// credB: 1 次成功 → 可用
	_, _ = store.RecordSuccess(ctx, credB, model, "b1")

	// Filter
	candidates := []CandidateForRoute{
		{CredentialID: credA, Model: model},
		{CredentialID: credB, Model: model},
	}
	filtered := store.FilterUsableCandidates(ctx, candidates)
	if len(filtered) != 1 {
		t.Fatalf("filtered=%d, want 1", len(filtered))
	}
	if filtered[0].CredentialID != credB {
		t.Fatalf("filtered[0].CredentialID=%d, want %d", filtered[0].CredentialID, credB)
	}
}

func TestRouteNodeStore_NilClient(t *testing.T) {
	store := NewRouteNodeStore(nil, DefaultRouteNodeConfig())
	ctx := context.Background()

	state, found, err := store.Get(ctx, 1, "m")
	if err != nil || found || state != nil {
		t.Fatalf("nil client Get should miss, got state=%v found=%v err=%v", state, found, err)
	}
	if !store.IsUsable(ctx, 1, "m") {
		t.Fatal("nil client IsUsable should default to true")
	}
	if err := store.Save(ctx, 1, "m", &RouteNodeState{}); err != nil {
		t.Fatalf("nil client Save should be no-op, got err=%v", err)
	}
	if err := store.Delete(ctx, 1, "m"); err != nil {
		t.Fatalf("nil client Delete should be no-op, got err=%v", err)
	}

	// nil receiver
	var nilStore *RouteNodeStore
	if !nilStore.IsUsable(ctx, 1, "m") {
		t.Fatal("nil receiver IsUsable should be safe")
	}
}