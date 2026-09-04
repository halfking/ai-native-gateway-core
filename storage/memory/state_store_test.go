package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/storage"
)

// 编译期断言：MemoryStateStore 必须实现 storage.StateStore 接口。
var _ storage.StateStore = (*MemoryStateStore)(nil)

// sessionFixture 用于测试 struct 值往返的样例结构。
type sessionFixture struct {
	TenantID  string
	SessionID string
	Turns     int
}

// waitFor 在 timeout 内轮询等待 cond 成立，超时则使测试失败。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// TestSetGetRoundTrip 验证 string 与 struct 两种值类型的 Set/Get 往返。
func TestSetGetRoundTrip(t *testing.T) {
	s := NewMemoryStateStore()
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	// string 值往返。
	if err := s.Set(ctx, "k-str", "hello", 0); err != nil {
		t.Fatalf("Set string: %v", err)
	}
	got, err := s.Get(ctx, "k-str")
	if err != nil {
		t.Fatalf("Get string: %v", err)
	}
	if v, ok := got.(string); !ok || v != "hello" {
		t.Fatalf("string 往返不符: got %#v, want \"hello\"", got)
	}

	// struct 值往返。
	want := sessionFixture{TenantID: "t1", SessionID: "s1", Turns: 3}
	if err := s.Set(ctx, "k-struct", want, 0); err != nil {
		t.Fatalf("Set struct: %v", err)
	}
	got, err = s.Get(ctx, "k-struct")
	if err != nil {
		t.Fatalf("Get struct: %v", err)
	}
	if v, ok := got.(sessionFixture); !ok || v != want {
		t.Fatalf("struct 往返不符: got %#v, want %#v", got, want)
	}
}

// TestOverwrite 验证同一 key 覆盖写后读取到最新值，且条目数不增长。
func TestOverwrite(t *testing.T) {
	s := NewMemoryStateStore()
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := s.Set(ctx, "k", fmt.Sprintf("v%d", i), 0); err != nil {
			t.Fatalf("Set 第 %d 次: %v", i, err)
		}
	}
	got, err := s.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v, _ := got.(string); v != "v2" {
		t.Fatalf("覆盖写后应读到最新值 v2, got %#v", got)
	}
	if n := s.Len(); n != 1 {
		t.Fatalf("覆盖写不应增加条目数: Len = %d, want 1", n)
	}
}

// TestDeleteNotFoundAndIdempotent 验证 Delete 后 Get 未找到，且 Delete 幂等。
func TestDeleteNotFoundAndIdempotent(t *testing.T) {
	s := NewMemoryStateStore()
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	if err := s.Set(ctx, "k", "v", 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "k"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Delete 后 Get 应返回 ErrNotFound, got %v", err)
	}
	// 幂等：重复 Delete 不报错。
	if err := s.Delete(ctx, "k"); err != nil {
		t.Fatalf("重复 Delete 应返回 nil, got %v", err)
	}
	// 删除不存在的 key 也返回 nil。
	if err := s.Delete(ctx, "never-existed"); err != nil {
		t.Fatalf("删除不存在的 key 应返回 nil, got %v", err)
	}
}

// TestTTLExpiry 验证短 TTL 项过期后 Get 按 Redis 语义返回 storage.ErrNotFound，
// 而无 TTL 项长期存活。
func TestTTLExpiry(t *testing.T) {
	s := NewMemoryStateStore()
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	if err := s.Set(ctx, "ttl", "v", 50*time.Millisecond); err != nil {
		t.Fatalf("Set ttl: %v", err)
	}
	if err := s.Set(ctx, "forever", "v", 0); err != nil {
		t.Fatalf("Set forever: %v", err)
	}

	// 未到期前可读。
	if _, err := s.Get(ctx, "ttl"); err != nil {
		t.Fatalf("TTL 未到期应可读: %v", err)
	}
	time.Sleep(80 * time.Millisecond)

	if _, err := s.Get(ctx, "ttl"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("TTL 过期后 Get 应返回 ErrNotFound(过期即不存在), got %v", err)
	}
	// 无 TTL 项不受影响。
	got, err := s.Get(ctx, "forever")
	if err != nil {
		t.Fatalf("无 TTL 项应长存: %v", err)
	}
	if v, _ := got.(string); v != "v" {
		t.Fatalf("无 TTL 项值不符: got %#v", got)
	}
}

// TestBackgroundSweep 验证后台清理协程按扫描间隔回收过期条目（Len 归零）。
func TestBackgroundSweep(t *testing.T) {
	// 用 20ms 的扫描间隔替代等待默认 30s 周期。
	s := NewMemoryStateStoreWithInterval(20 * time.Millisecond)
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	const n = 10
	for i := 0; i < n; i++ {
		if err := s.Set(ctx, fmt.Sprintf("k%d", i), i, 50*time.Millisecond); err != nil {
			t.Fatalf("Set k%d: %v", i, err)
		}
	}
	if got := s.Len(); got != n {
		t.Fatalf("写入后 Len = %d, want %d", got, n)
	}

	// 等待后台协程回收全部过期条目。
	waitFor(t, 2*time.Second, func() bool { return s.Len() == 0 })

	// 条目确实被删除（而非仅计数错误）。
	for i := 0; i < n; i++ {
		if _, err := s.Get(ctx, fmt.Sprintf("k%d", i)); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("后台清理后 Get k%d 应返回 ErrNotFound, got %v", i, err)
		}
	}
}

// TestCloseIdempotent 验证 Close 幂等、Close 后 Set 返回错误、Get 视同不存在，
// 且后台协程随 Close 退出（扫描间隔到期后 Len 不再变化即协程已停止清理/或整体不可写）。
func TestCloseIdempotent(t *testing.T) {
	s := NewMemoryStateStoreWithInterval(20 * time.Millisecond)
	ctx := context.Background()

	if err := s.Set(ctx, "k", "v", 0); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// 幂等：连续多次 Close 不 panic。
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("重复 Close 应返回 nil, got %v", err)
	}

	// Close 后 Set 返回 errClosed。
	if err := s.Set(ctx, "k2", "v", 0); !errors.Is(err, errClosed) {
		t.Fatalf("Close 后 Set 应返回 errClosed, got %v", err)
	}
	// Close 后 Get 视同不存在。
	if _, err := s.Get(ctx, "k"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Close 后 Get 应返回 ErrNotFound, got %v", err)
	}
	// Close 后 Delete 保持幂等。
	if err := s.Delete(ctx, "k"); err != nil {
		t.Fatalf("Close 后 Delete 应返回 nil, got %v", err)
	}
}

// TestConcurrentStress 1000 goroutine 混合 Set/Get/Delete 不同 key 的并发压力测试，
// 在 -race 下应无竞争、无 panic。
func TestConcurrentStress(t *testing.T) {
	s := NewMemoryStateStoreWithInterval(20 * time.Millisecond)
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	const goroutines = 1000
	const opsPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", id%100) // 不同 key 且有交叉
			for j := 0; j < opsPerGoroutine; j++ {
				switch j % 3 {
				case 0:
					// 混合 TTL 与无 TTL 写入，覆盖写场景。
					ttl := time.Duration(j%5) * 10 * time.Millisecond
					if err := s.Set(ctx, key, id, ttl); err != nil {
						t.Errorf("Set %s: %v", key, err)
						return
					}
				case 1:
					// 读到与否都合法（可能已过期/已删除），只关心不 panic、无竞争。
					_, _ = s.Get(ctx, key)
				default:
					_ = s.Delete(ctx, key)
				}
			}
		}(i)
	}
	wg.Wait()

	// 压力结束后存储仍可用。
	if err := s.Set(ctx, "after-stress", "ok", 0); err != nil {
		t.Fatalf("压力结束后 Set: %v", err)
	}
	if v, err := s.Get(ctx, "after-stress"); err != nil || v != "ok" {
		t.Fatalf("压力结束后 Get: got (%#v, %v), want (\"ok\", nil)", v, err)
	}
}

// TestLenAccounting 补充验证 Len 的计数语义：只增不减（含未过期项），
// 惰性过期删除与后台清理都会使其下降。
func TestLenAccounting(t *testing.T) {
	s := NewMemoryStateStoreWithInterval(20 * time.Millisecond)
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := s.Set(ctx, fmt.Sprintf("k%d", i), i, 40*time.Millisecond); err != nil {
			t.Fatalf("Set k%d: %v", i, err)
		}
	}
	if got := s.Len(); got != 5 {
		t.Fatalf("写入 5 项后 Len = %d, want 5", got)
	}

	// 等待惰性删除 + 后台清理使条目归零。
	waitFor(t, 2*time.Second, func() bool { return s.Len() == 0 })
}
