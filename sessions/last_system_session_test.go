package sessions

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// 这些测试需要 Redis。如未启动 Redis，跳过（miniredis 也可，但避免引入新依赖）。
// 用环境变量 LLM_GATEWAY_TEST_REDIS_ADDR 控制：
//   unset → skip
//   set   → 连接该地址运行集成测试

func redisAddr() string {
	// 默认本地 Redis（与项目其它集成测试保持一致）
	return "localhost:6379"
}

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := redisAddr()
	c := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		t.Skipf("redis unavailable at %s, skipping: %v", addr, err)
	}
	return c
}

func TestLastSystemSessionIndex_GetSet(t *testing.T) {
	c := newTestRedis(t)
	defer c.Close()
	idx := NewLastSystemSessionIndex(c)
	ctx := context.Background()

	const apiKeyID = 99001
	// 先清理可能残留的 key，确保初始状态干净（避免上次运行残留导致 flaky）
	_ = idx.Delete(ctx, apiKeyID)
	t.Cleanup(func() {
		_ = idx.Delete(ctx, apiKeyID)
	})

	// 初始未命中
	entry, found, err := idx.Get(ctx, apiKeyID)
	if err != nil || found || entry != nil {
		t.Fatalf("expected empty hit, got entry=%v found=%v err=%v", entry, found, err)
	}

	// Set 后 Get 命中
	want := &LastSystemSessionEntry{
		SessionID:  "gw_test_xyz",
		DeviceSeed: "device-a",
		TaskID:     "task-1",
	}
	if err := idx.Set(ctx, apiKeyID, want); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	got, found, err := idx.Get(ctx, apiKeyID)
	if err != nil || !found {
		t.Fatalf("expected hit, got found=%v err=%v", found, err)
	}
	if got.SessionID != want.SessionID {
		t.Fatalf("SessionID mismatch: got=%s want=%s", got.SessionID, want.SessionID)
	}
	if got.DeviceSeed != want.DeviceSeed {
		t.Fatalf("DeviceSeed mismatch: got=%s want=%s", got.DeviceSeed, want.DeviceSeed)
	}
	if got.TaskID != want.TaskID {
		t.Fatalf("TaskID mismatch: got=%s want=%s", got.TaskID, want.TaskID)
	}
	if got.LastAssignedAt.IsZero() {
		t.Fatal("LastAssignedAt should be set by Set()")
	}
}

func TestLastSystemSessionIndex_TTLExpiry(t *testing.T) {
	c := newTestRedis(t)
	defer c.Close()
	idx := NewLastSystemSessionIndex(c)
	ctx := context.Background()

	const apiKeyID = 99002
	_ = idx.Delete(ctx, apiKeyID)
	t.Cleanup(func() {
		_ = idx.Delete(ctx, apiKeyID)
	})

	// 模拟"5 分钟前赋值"的 entry（绕过 TTL 直接构造一个过期 entry）
	stale := &LastSystemSessionEntry{
		SessionID:      "gw_stale",
		LastAssignedAt: time.Now().Add(-10 * time.Minute), // 已过期
	}
	key := lastSystemSessionRedisKey(apiKeyID)
	data, _ := json.Marshal(stale)
	// 用 1s TTL 让 Redis 先存上，然后 Get 时 entry.LastAssignedAt 已过期
	if err := c.Set(ctx, key, data, 1*time.Second).Err(); err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	time.Sleep(2 * time.Second) // 等 Redis TTL 过期
	// 此时 Get 应该返回 (nil, false)（Redis 已过期）
	_, found, err := idx.Get(ctx, apiKeyID)
	if err != nil || found {
		t.Fatalf("expected miss after TTL, got found=%v err=%v", found, err)
	}
}

func TestLastSystemSessionIndex_StaleTimestampRejected(t *testing.T) {
	c := newTestRedis(t)
	defer c.Close()
	idx := NewLastSystemSessionIndex(c)
	ctx := context.Background()

	const apiKeyID = 99003
	_ = idx.Delete(ctx, apiKeyID)
	t.Cleanup(func() {
		_ = idx.Delete(ctx, apiKeyID)
	})

	// Redis key 还在 TTL 内，但 entry.LastAssignedAt 已超过 5 分钟
	// （模拟人为篡改或时钟漂移）
	stale := &LastSystemSessionEntry{
		SessionID:      "gw_stale",
		LastAssignedAt: time.Now().Add(-10 * time.Minute),
	}
	// 用 30min TTL 写入，Redis 不会过期；但 entry 时间戳过期
	key := lastSystemSessionRedisKey(apiKeyID)
	data, _ := json.Marshal(stale)
	if err := c.Set(ctx, key, data, 30*time.Minute).Err(); err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	_, found, err := idx.Get(ctx, apiKeyID)
	if err != nil || found {
		t.Fatalf("expected miss due to stale timestamp, got found=%v err=%v", found, err)
	}
}

func TestLastSystemSessionIndex_Touch(t *testing.T) {
	c := newTestRedis(t)
	defer c.Close()
	idx := NewLastSystemSessionIndex(c)
	ctx := context.Background()

	const apiKeyID = 99004
	_ = idx.Delete(ctx, apiKeyID)
	t.Cleanup(func() {
		_ = idx.Delete(ctx, apiKeyID)
	})

	want := &LastSystemSessionEntry{SessionID: "gw_touch"}
	if err := idx.Set(ctx, apiKeyID, want); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Touch 后 Get 仍能命中
	if err := idx.Touch(ctx, apiKeyID); err != nil {
		t.Fatalf("Touch failed: %v", err)
	}
	got, found, _ := idx.Get(ctx, apiKeyID)
	if !found || got.SessionID != "gw_touch" {
		t.Fatalf("expected hit after Touch, got entry=%v found=%v", got, found)
	}
}

func TestLastSystemSessionIndex_Delete(t *testing.T) {
	c := newTestRedis(t)
	defer c.Close()
	idx := NewLastSystemSessionIndex(c)
	ctx := context.Background()

	const apiKeyID = 99005
	_ = idx.Delete(ctx, apiKeyID)
	_ = idx.Set(ctx, apiKeyID, &LastSystemSessionEntry{SessionID: "gw_del"})

	if err := idx.Delete(ctx, apiKeyID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	_, found, _ := idx.Get(ctx, apiKeyID)
	if found {
		t.Fatal("expected miss after Delete")
	}
}

func TestLastSystemSessionIndex_DifferentClients(t *testing.T) {
	c := newTestRedis(t)
	defer c.Close()
	idx := NewLastSystemSessionIndex(c)
	ctx := context.Background()

	const clientA = 99100
	const clientB = 99101
	_ = idx.Delete(ctx, clientA)
	_ = idx.Delete(ctx, clientB)
	t.Cleanup(func() {
		_ = idx.Delete(ctx, clientA)
		_ = idx.Delete(ctx, clientB)
	})

	// Client A 写入
	_ = idx.Set(ctx, clientA, &LastSystemSessionEntry{SessionID: "gw_A"})
	// Client B 写入
	_ = idx.Set(ctx, clientB, &LastSystemSessionEntry{SessionID: "gw_B"})

	// 各自独立
	gotA, _, _ := idx.Get(ctx, clientA)
	gotB, _, _ := idx.Get(ctx, clientB)
	if gotA == nil || gotA.SessionID != "gw_A" {
		t.Fatalf("client A session mismatch: %v", gotA)
	}
	if gotB == nil || gotB.SessionID != "gw_B" {
		t.Fatalf("client B session mismatch: %v", gotB)
	}
}

func TestLastSystemSessionIndex_NilClient(t *testing.T) {
	// nil 索引应安全降级（不 panic、不报错）
	idx := NewLastSystemSessionIndex(nil)
	ctx := context.Background()

	_, found, err := idx.Get(ctx, 1)
	if found || err != nil {
		t.Fatalf("nil client Get should return miss, got found=%v err=%v", found, err)
	}
	if err := idx.Set(ctx, 1, &LastSystemSessionEntry{SessionID: "x"}); err != nil {
		t.Fatalf("nil client Set should be no-op, got err=%v", err)
	}
	if err := idx.Touch(ctx, 1); err != nil {
		t.Fatalf("nil client Touch should be no-op, got err=%v", err)
	}
	if err := idx.Delete(ctx, 1); err != nil {
		t.Fatalf("nil client Delete should be no-op, got err=%v", err)
	}

	// nil receiver 也安全
	var nilIdx *LastSystemSessionIndex
	if _, _, err := nilIdx.Get(ctx, 1); err != nil {
		t.Fatalf("nil receiver Get should be safe, got err=%v", err)
	}
}