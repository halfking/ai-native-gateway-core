package db

import (
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newClosedTestPool 构造一个未连接任何 PG 实例的 pgxpool.Pool。
//
// 设计要点：
//   - 用一个语法合法但 host 不可达的 URL，pgxpool.ParseConfig 不会触发连接，
//     pgxpool.NewWithConfig 在 MinConns=0 时也不会预热连接。
//   - 测试拿到 pool 后立即 Close()，因此后续无论对它执行什么操作都不会触发
//     网络 I/O。这让我们能纯进程内测试 Stdlib() 的生命周期语义而无需真实 PG。
//   - 注意：pool 关闭后 d.Stdlib() 仍能成功（OpenDBFromPool 不分配连接），
//     但 d.Stdlib().Ping() / QueryContext() 会失败 —— 测试不应发起查询。
func newClosedTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://test:test@127.0.0.1:1/test?sslmode=disable")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	cfg.MinConns = 0
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig: %v", err)
	}
	pool.Close()
	return pool
}

// TestStdlib_ReturnsSamePointer 验证修复后的 Stdlib() 是单例：多次调用必须
// 返回同一个 *sql.DB 指针。修复前每次调用都通过 stdlib.OpenDB(...) 构造新的
// *sql.DB，进而在 database/sql 层创建独立的连接池 —— 这正是本次修复要消除的
// 资源泄漏根源。
func TestStdlib_ReturnsSamePointer(t *testing.T) {
	d := &DB{pool: newClosedTestPool(t)}
	defer d.Close()

	first := d.Stdlib()
	if first == nil {
		t.Fatal("Stdlib() returned nil on non-nil DB with non-nil pool")
	}
	for i := 0; i < 10; i++ {
		again := d.Stdlib()
		if again != first {
			t.Fatalf("Stdlib() returned different *sql.DB on call %d: %p vs %p", i, again, first)
		}
	}
}

// TestStdlib_NilSafety 验证 nil 接收者和空 pool 上调用 Stdlib() 不会 panic，
// 与其他 d.Stdlib() 调用站点已有的 nil-safe 约定一致。
func TestStdlib_NilSafety(t *testing.T) {
	var nilDB *DB
	if got := nilDB.Stdlib(); got != nil {
		t.Errorf("nil receiver: Stdlib() = %p, want nil", got)
	}

	d := &DB{} // 非 nil 接收者，nil pool
	if got := d.Stdlib(); got != nil {
		t.Errorf("nil pool: Stdlib() = %p, want nil", got)
	}
}

// TestStdlib_ConcurrentSingleton 在 -race 下验证多个 goroutine 同时调用
// Stdlib() 也能拿到同一个 *sql.DB 指针。这是修复前会触发 data race 的核心
// 场景：旧实现没有同步原语保护 stdlib 的构造，并发调用可能产生多份独立池。
func TestStdlib_ConcurrentSingleton(t *testing.T) {
	d := &DB{pool: newClosedTestPool(t)}
	defer d.Close()

	const goroutines = 64
	results := make([]*sql.DB, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})
	var uniq atomic.Int32

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			<-start // 同时起跑，最大化竞争
			db := d.Stdlib()
			results[idx] = db
		}(i)
	}
	close(start)
	wg.Wait()

	canonical := results[0]
	if canonical == nil {
		t.Fatal("Stdlib() returned nil for one of the goroutines")
	}
	for _, got := range results {
		if got != canonical {
			uniq.Add(1)
		}
	}
	if uniq.Load() != 0 {
		t.Fatalf("Stdlib() returned %d distinct *sql.DB pointers across %d goroutines (expected 1)",
			uniq.Load()+1, goroutines)
	}
}

// TestClose_Idempotent 验证 Close() 可重复调用而不 panic 或泄漏资源。
// 修复后 Close() 必须幂等（清空 stdlibDB 后第二次调用直接跳过），这是重启
// 与优雅停机路径都会触发的代码路径。
func TestClose_Idempotent(t *testing.T) {
	d := &DB{pool: newClosedTestPool(t)}

	// 第一次 Close：触发 stdlib.OpenDBFromPool 的懒构造 + 完整释放。
	// 这里必须先 Stdlib() 一下，否则 stdlibDB==nil，Close 路径走的是简化分支，
	// 不足以验证 stdlib + pool 同时被关闭的代码路径。
	if d.Stdlib() == nil {
		t.Fatal("Stdlib() unexpectedly nil")
	}
	d.Close()

	// 重复 Close 不应 panic，且 stdlibDB 已被清空。
	d.Close()
	d.Close()

	if d.stdlibDB != nil {
		t.Errorf("after Close, stdlibDB = %p, want nil", d.stdlibDB)
	}
}

// TestClose_NilSafety 验证 nil 接收者上 Close() 是 no-op。这是 cmd/gateway
// 在 dbConn==nil 早期退出路径上必须成立的契约。
func TestClose_NilSafety(t *testing.T) {
	var d *DB
	d.Close() // 必须不 panic
}

// TestClose_OrderIndependence 验证 stdlib *sql.DB 和 pool 的关闭顺序可以
// 任意交错而不 panic —— 因为 stdlibDB 与 pool 解耦（pgx connector 不持有
// 物理连接，仅在每次查询时从 pool 借），关闭任一侧都不会让另一侧的内部状态
// 处于非法状态。
//
// 这一性质对优雅停机至关重要：可能先收到 SIGTERM 关掉上层 worker（导致
// stdlibDB 被释放），再调 dbConn.Close() 关闭 pool；反过来也可能。
func TestClose_OrderIndependence(t *testing.T) {
	// 正向顺序：Close 先关 stdlibDB，再关 pool。
	d := &DB{pool: newClosedTestPool(t)}
	if d.Stdlib() == nil {
		t.Fatal("Stdlib() unexpectedly nil")
	}
	d.Close()

	// 反向顺序：手动模拟「先关 pool，再关 stdlib」。
	// 由于我们的 Close() 内部就是 stdlib-first 的，要测的是反过来在外部代码里
	// 调用 d.pool.Close() 之后 d.Close() 是否仍然安全。
	d2 := &DB{pool: newClosedTestPool(t)}
	if d2.Stdlib() == nil {
		t.Fatal("Stdlib() unexpectedly nil")
	}
	d2.pool.Close() // 先关 pool（外部 shutdown 路径可能这么做）
	d2.Close()      // 再走我们的 Close：必须不 panic
}

// TestStdlib_AfterClose_ReturnsNil 验证 Close() 之后再调用 Stdlib() 必须返回
// nil，而不是已经关闭的 *sql.DB。修复前 sync.Once 已经发起，Close 后再调
// Stdlib() 会返回同一个已关闭的 *sql.DB，导致后续查询全部失败 —— 这是
// 测试与 hot-reload 场景下的常见踩坑点。
func TestStdlib_AfterClose_ReturnsNil(t *testing.T) {
	d := &DB{pool: newClosedTestPool(t)}
	if d.Stdlib() == nil {
		t.Fatal("Stdlib() unexpectedly nil")
	}
	d.Close()

	// 第二次 Stdlib() 必须返回 nil —— pool 已经被关闭，再借连接无意义。
	// 注意：sync.Once 仍然生效过一次；Close 已经把 stdlibDB 复位为 nil。
	if got := d.Stdlib(); got != nil {
		t.Errorf("Stdlib() after Close = %p, want nil", got)
	}
}

// TestStdlib_SharesUnderlyingPool 验证多次 Stdlib() 调用返回的 *sql.DB 实例
// 共享同一个底层 pgxpool。这通过 OpenDBFromPool 的实现保证（connector 内
// 持有的是同一个 pool 引用），且我们的 stdlibDBOnce 进一步保证 *sql.DB 本身
// 也是单例。
func TestStdlib_SharesUnderlyingPool(t *testing.T) {
	d := &DB{pool: newClosedTestPool(t)}
	defer d.Close()

	a := d.Stdlib()
	b := d.Stdlib()
	if a == nil || b == nil {
		t.Fatal("Stdlib() returned nil")
	}
	if a != b {
		t.Fatalf("Stdlib() not singleton: %p != %p", a, b)
	}
	// 不应有任何 goroutine 残留：Close 必须幂等。
	d.Close()
}
