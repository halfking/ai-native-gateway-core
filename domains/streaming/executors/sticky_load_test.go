package executors

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	ursmcache "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/cache"
)

func TestStickyLoadTrackerMemoryWindow(t *testing.T) {
	tr := NewStickyLoadTracker()
	defer tr.Close()

	tr.ObserveSession(1, "sess1")
	tr.ObserveSession(1, "sess2")
	tr.ObserveSession(2, "sess1")

	if got := tr.Info(1).Sessions; got != 2 {
		t.Fatalf("expected 2 sessions on cred 1, got %d", got)
	}
	if got := tr.Info(2).Sessions; got != 1 {
		t.Fatalf("expected 1 session on cred 2, got %d", got)
	}
	if got := tr.Info(3).Sessions; got != 0 {
		t.Fatalf("expected 0 for unknown cred, got %d", got)
	}

	// 会话超窗丢弃：直接把 sess1 的 lastSeen 拨回窗口外。
	tr.mu.Lock()
	tr.sessions[1]["sess1"] = time.Now().Add(-tr.window - time.Minute).Unix()
	tr.mu.Unlock()
	if got := tr.Info(1).Sessions; got != 1 {
		t.Fatalf("expected window prune to 1, got %d", got)
	}
}

func TestStickyLoadTrackerActivity(t *testing.T) {
	tr := NewStickyLoadTracker()
	defer tr.Close()

	if got := tr.Info(7).LastActivityMs; got != 0 {
		t.Fatalf("expected 0 before any activity, got %d", got)
	}
	tr.ObserveActivity(7)
	info := tr.Info(7)
	if info.LastActivityMs == 0 {
		t.Fatal("expected activity to be recorded")
	}
	if age := time.Now().UnixMilli() - info.LastActivityMs; age < 0 || age > 2000 {
		t.Fatalf("activity age out of range: %dms", age)
	}
}

func TestStickyLoadTrackerRedisCrossInstance(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := ursmcache.NewStickyLoadStore(rdb)

	tr := NewStickyLoadTracker()
	defer tr.Close()
	tr.SetStore(store)

	tr.ObserveSession(5, "sess1")
	tr.ObserveSession(5, "sess2")

	// Observe 的 Redis 写是异步 best-effort —— 轮询等它落地。
	deadline := time.Now().Add(2 * time.Second)
	for {
		sessions, _ := store.LoadBatch(context.Background(), []int{5}, tr.window)
		if sessions[5] == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("redis observe did not land in time")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Refresh 后 Info 应取到跨实例快照（这里单实例，值应一致）。
	tr.Refresh([]int{5})
	deadline = time.Now().Add(2 * time.Second)
	for tr.Info(5).Sessions != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("snapshot did not refresh, sessions=%d", tr.Info(5).Sessions)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// mockStickyLoadStore counts LoadBatch calls for refresh-throttle testing.
type mockStickyLoadStore struct {
	mu      sync.Mutex
	loads   int
	entries map[int]int
}

func (m *mockStickyLoadStore) Observe(_ context.Context, _ int, _ string, _ time.Time, _ time.Duration) error {
	return nil
}

func (m *mockStickyLoadStore) LoadBatch(_ context.Context, ids []int, _ time.Duration) (map[int]int, map[int]int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loads++
	out := make(map[int]int, len(ids))
	for _, id := range ids {
		out[id] = m.entries[id]
	}
	return out, map[int]int64{}
}

func TestStickyLoadTrackerRefreshThrottle(t *testing.T) {
	store := &mockStickyLoadStore{entries: map[int]int{1: 4}}
	tr := NewStickyLoadTracker()
	defer tr.Close()
	tr.SetStore(store)

	// 第一次 Refresh → 1 次 LoadBatch，快照落地。
	tr.Refresh([]int{1})
	waitFor(t, func() bool { return tr.Info(1).Sessions == 4 })

	// refresh TTL 内的重复 Refresh 被节流。
	tr.Refresh([]int{1})
	tr.Refresh([]int{1})
	time.Sleep(100 * time.Millisecond)
	store.mu.Lock()
	loads := store.loads
	store.mu.Unlock()
	if loads != 1 {
		t.Fatalf("expected refresh throttle to hold at 1 load, got %d", loads)
	}

	// 快照过期后 Refresh 再次生效。
	tr.snapMu.Lock()
	tr.snapAt = time.Now().Add(-time.Hour)
	tr.snapMu.Unlock()
	store.mu.Lock()
	store.entries[1] = 9
	store.mu.Unlock()
	tr.Refresh([]int{1})
	waitFor(t, func() bool { return tr.Info(1).Sessions == 9 })
}

// TestStickyLoadTrackerSnapshotCoversMemory: 快照覆盖到的凭据以跨实例
// 计数为准（内存计数是它的子集，不能相加）。
func TestStickyLoadTrackerSnapshotCoversMemory(t *testing.T) {
	store := &mockStickyLoadStore{entries: map[int]int{1: 7}}
	tr := NewStickyLoadTracker()
	defer tr.Close()
	tr.SetStore(store)

	tr.ObserveSession(1, "local-only") // 内存 1 个，也是写进 Redis 的同一个
	tr.Refresh([]int{1})
	waitFor(t, func() bool { return tr.Info(1).Sessions == 7 })
}

// R46 F1: 快照年龄超过 window 后失去"窗口内活跃会话数"的语义——Info
// 必须回落本实例内存镜像，不得让冻结快照无限期参与评分（Redis 持续
// 故障场景下旧快照会把空闲凭据记满 sticky 会话数）。
func TestStickyLoadTrackerStaleSnapshotFallsBackToMemory(t *testing.T) {
	store := &mockStickyLoadStore{entries: map[int]int{1: 7}}
	tr := NewStickyLoadTracker()
	defer tr.Close()
	tr.SetStore(store)

	tr.Refresh([]int{1})
	waitFor(t, func() bool { return tr.Info(1).Sessions == 7 })

	// 快照拨老到 window 之外；本地镜像有 1 个真实会话。
	tr.snapMu.Lock()
	tr.snapAt = time.Now().Add(-tr.window - time.Minute)
	tr.snapMu.Unlock()
	tr.ObserveSession(1, "local-session")

	if got := tr.Info(1).Sessions; got != 1 {
		t.Fatalf("expected stale snapshot to fall back to memory count 1, got %d", got)
	}

	// 快照回到新鲜窗口后恢复跨实例优先。
	tr.snapMu.Lock()
	tr.snapAt = time.Now()
	tr.snapMu.Unlock()
	waitFor(t, func() bool { return tr.Info(1).Sessions == 7 })
}

// R46 F1: LoadBatch panic 不得永久卡死单飞标志（refreshing 卡死会让
// 快照从此不再刷新，效果等同 Redis 永久故障）。
func TestStickyLoadTrackerRefreshPanicReleasesSingleFlight(t *testing.T) {
	store := &panicStickyLoadStore{}
	tr := NewStickyLoadTracker()
	defer tr.Close()
	tr.SetStore(store)

	tr.Refresh([]int{1})
	waitFor(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.loads >= 1
	})

	// 第一次刷新已 panic 恢复；第二次必须能再次进入（refreshing 已复位）。
	tr.Refresh([]int{1})
	waitFor(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.loads >= 2
	})
}

// panicStickyLoadStore: LoadBatch 必然 panic 的故障 store。
type panicStickyLoadStore struct {
	mu    sync.Mutex
	loads int
}

func (m *panicStickyLoadStore) Observe(_ context.Context, _ int, _ string, _ time.Time, _ time.Duration) error {
	return nil
}

func (m *panicStickyLoadStore) LoadBatch(_ context.Context, _ []int, _ time.Duration) (map[int]int, map[int]int64) {
	m.mu.Lock()
	m.loads++
	m.mu.Unlock()
	panic("simulated store failure")
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// R47：快照新鲜度上限的绝对顶——window 被运维调得极大时（如 86400s），
// Redis 故障下的冻结快照最多参与 stickyLoadSnapshotStaleCap 时长，之后
// 回落本实例内存镜像（R46 F1 的 window 耦合收尾）。
func TestStickyLoadTrackerSnapshotStaleCap(t *testing.T) {
	t.Setenv("LLM_GATEWAY_STICKYLOAD_WINDOW_SECONDS", "86400")
	tr := NewStickyLoadTracker()
	defer tr.Close()
	if tr.snapshotMaxAge != stickyLoadSnapshotStaleCap {
		t.Fatalf("expected snapshotMaxAge capped at %v, got %v",
			stickyLoadSnapshotStaleCap, tr.snapshotMaxAge)
	}

	// 注入超龄"冻结"快照：年龄超过绝对顶 → 不得覆盖 Info。
	tr.snapMu.Lock()
	tr.snapshot = map[int]StickyLoadInfo{7: {Sessions: 99, LastActivityMs: 1}}
	tr.snapAt = time.Now().Add(-stickyLoadSnapshotStaleCap - time.Minute)
	tr.snapMu.Unlock()
	if got := tr.Info(7); got.Sessions != 0 {
		t.Fatalf("stale snapshot beyond cap must fall back to memory, got %+v", got)
	}
}

// R47：activity 保留窗跟随 recency 地平线 env——地平线调大后 recency
// 信号不得比语义窗先归零。
func TestStickyLoadActivityRetentionFollowsHorizon(t *testing.T) {
	tr := NewStickyLoadTracker()
	defer tr.Close()
	t.Setenv("LLM_GATEWAY_ROUTING_RECENCY_HORIZON_SECONDS", "900")
	if got := tr.activityRetention(); got < 900*time.Second {
		t.Fatalf("activity retention must cover configured horizon 900s, got %v", got)
	}
}
