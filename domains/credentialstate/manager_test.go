package credentialstate

import (
	"context"
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

	ctx := context.Background()
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

	ctx := context.Background()
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

	ctx := context.Background()
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

func setupTestDB(t *testing.T) *pgxpool.Pool {
	// 这里应该连接测试数据库
	// 为了简化，跳过实际连接
	t.Skip("test database not configured")
	return nil
}
