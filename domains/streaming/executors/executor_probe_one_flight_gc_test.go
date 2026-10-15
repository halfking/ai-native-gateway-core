package executors

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestMemoryProbeOneFlight_PrunesExpiredKeys 钉住 2026-10-15 审计 P1 的修复：
// held 的键来自 params.ClientModel（客户端可控字符串），原实现只覆写不删除，
// 于是 Redis 缺席（fpSlotRedis==nil → main.go 不注入 RedisProbeOneFlight）时
// 进程内回落随不同 model 串**单调增长**，构成无界内存增长。
//
// 反向钉：注入一批不同 model（模拟噪声/恶意客户端），等 TTL 过期后再次
// TryAcquire，held 必须回落到「只含本次这一个键」。若剪枝被删掉，len(held)
// 会停在 N 不降，本用例转红。
func TestMemoryProbeOneFlight_PrunesExpiredKeys(t *testing.T) {
	m := NewMemoryProbeOneFlight()
	ctx := context.Background()

	const n = 64
	tiny := 5 * time.Millisecond
	for i := 0; i < n; i++ {
		model := fmt.Sprintf("client-model-%d", i)
		if !m.TryAcquire(ctx, model, tiny) {
			t.Fatalf("model %s: first acquire should succeed", model)
		}
	}
	if got := len(m.held); got != n {
		t.Fatalf("after %d distinct models: len(held)=%d, want %d", n, got, n)
	}

	// 等全部 TTL 过期。剪枝若缺失，这里的键会一直留着。
	time.Sleep(tiny * 4)

	if !m.TryAcquire(ctx, "fresh-model", tiny) {
		t.Fatalf("fresh model acquire should succeed")
	}
	// 关键断言：剪枝后只剩刚写入的那一个键。
	if got := len(m.held); got != 1 {
		t.Fatalf("after expiry + one fresh acquire: len(held)=%d, want 1 (expired keys not pruned => unbounded growth)", got)
	}
	if _, ok := m.held["fresh-model"]; !ok {
		t.Fatalf("fresh-model missing from held: %v", m.held)
	}
}

// TestMemoryProbeOneFlight_ConcurrentTryAcquire 确认剪枝没有引入竞态：
// 并发 TryAcquire 下 held 的长度不得超过「并发写入的不同 model 数」，
// 且同一 model 在 TTL 内最多成功一次。
func TestMemoryProbeOneFlight_ConcurrentTryAcquire(t *testing.T) {
	m := NewMemoryProbeOneFlight()
	ctx := context.Background()

	const workers = 64
	results := make(chan bool, workers)
	for i := 0; i < workers; i++ {
		go func() {
			results <- m.TryAcquire(ctx, "shared-model", time.Second)
		}()
	}

	granted := 0
	for i := 0; i < workers; i++ {
		if <-results {
			granted++
		}
	}
	if granted != 1 {
		t.Fatalf("same model acquired %d times within TTL, want exactly 1", granted)
	}
	if got := len(m.held); got != 1 {
		t.Fatalf("len(held)=%d after concurrent same-model race, want 1", got)
	}
}

// TestMemoryProbeOneFlight_ExpiredKeyIsReacquirable 钉住剪枝后的语义：
// 过期键必须可被重新获取（剪枝不能把「过期」误判成「仍被持有」）。
func TestMemoryProbeOneFlight_ExpiredKeyIsReacquirable(t *testing.T) {
	m := NewMemoryProbeOneFlight()
	ctx := context.Background()

	if !m.TryAcquire(ctx, "m1", 5*time.Millisecond) {
		t.Fatal("first acquire should succeed")
	}
	if m.TryAcquire(ctx, "m1", 5*time.Millisecond) {
		t.Fatal("second acquire within TTL should be refused")
	}
	time.Sleep(20 * time.Millisecond)
	if !m.TryAcquire(ctx, "m1", 5*time.Millisecond) {
		t.Fatal("acquire after TTL expiry should succeed again")
	}
}
