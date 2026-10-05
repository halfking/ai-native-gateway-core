package bg

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeRefresher implements the indexRefresher seam for listener tests.
//
// 2026-10-05 P2：seam 升级为 RefreshOnceStatus（skipped 语义）。skip 决定
// 第 n 次调用是否返回「被跳过」—— 跳过必须返回 (true, nil)：没执行，但
// 不是错误。
type fakeRefresher struct {
	mu    sync.Mutex
	calls int
	fail  func(call int) error
	skip  func(call int) bool
}

func (f *fakeRefresher) RefreshOnceStatus(context.Context) (bool, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if f.skip != nil && f.skip(n) {
		return true, nil
	}
	if f.fail != nil {
		return false, f.fail(n)
	}
	return false, nil
}

func (f *fakeRefresher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// waitFor polls cond until true or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func TestAutoRouteRealtimeListener_StopWithoutStartDoesNotDeadlock(t *testing.T) {
	l := NewAutoRouteRealtimeListener(nil, nil)
	done := make(chan struct{})
	go func() {
		l.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop before Start deadlocked")
	}
}

func TestAutoRouteRealtimeListener_StopTwiceDoesNotPanic(t *testing.T) {
	l := NewAutoRouteRealtimeListener(nil, nil)
	l.Stop()
	l.Stop()
}

func TestAutoRouteRealtimeListener_StartWithoutPoolIsNoop(t *testing.T) {
	l := NewAutoRouteRealtimeListener(nil, nil)
	l.Start(context.Background())
	l.Start(context.Background())
	l.Stop()
}

// The pool is created lazily (no connection attempt until Acquire), so an
// unreachable address exercises the run loop's cancellable retry path.
func TestAutoRouteRealtimeListener_StartTwiceThenStopReturnsPromptly(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://nobody:nopass@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Skipf("cannot build lazy pool: %v", err)
	}
	defer pool.Close()

	l := NewAutoRouteRealtimeListener(pool, nil)
	l.Start(context.Background())
	l.Start(context.Background()) // second call must not spawn duplicate loops
	l.Stop()                      // must return despite the 5s retry sleep

	// Repeated Stop after a real Start must also return immediately.
	done := make(chan struct{})
	go func() {
		l.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("repeated Stop after Start deadlocked")
	}
}

// A burst of notifications inside one debounce window must coalesce into a
// single refresh (trailing edge).
func TestAutoRouteRealtimeListener_DebounceCoalescesBurst(t *testing.T) {
	fake := &fakeRefresher{}
	l := NewAutoRouteRealtimeListener(nil, fake)
	l.debounceWindow = 40 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); l.debounceLoop(ctx) }()

	for i := 0; i < 5; i++ {
		l.handleNotification("credentials:UPDATE:1")
	}
	if !waitFor(t, 2*time.Second, func() bool { return fake.count() >= 1 }) {
		t.Fatalf("no refresh after burst; calls=%d", fake.count())
	}
	time.Sleep(3 * l.debounceWindow)
	if got := fake.count(); got != 1 {
		t.Fatalf("burst produced %d refreshes, want exactly 1", got)
	}
	if l.PendingRefreshes() != 0 {
		t.Fatalf("pending flag not cleared after refresh; PendingRefreshes=%d", l.PendingRefreshes())
	}
}

// A notification arriving near the end of the window must extend the timer:
// the refresh runs one window after the LAST event, not the first.
func TestAutoRouteRealtimeListener_DebounceUsesTrailingEdge(t *testing.T) {
	fake := &fakeRefresher{}
	l := NewAutoRouteRealtimeListener(nil, fake)
	l.debounceWindow = 60 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); l.debounceLoop(ctx) }()

	l.handleNotification("credentials:UPDATE:1")
	time.Sleep(30 * time.Millisecond) // inside the first window
	l.handleNotification("credentials:UPDATE:2")

	// First window (60ms from the FIRST event) must not fire a refresh.
	time.Sleep(45 * time.Millisecond)
	if got := fake.count(); got != 0 {
		t.Fatalf("refresh fired %d times before trailing window elapsed, want 0", got)
	}
	if !waitFor(t, 2*time.Second, func() bool { return fake.count() == 1 }) {
		t.Fatalf("trailing refresh never fired; calls=%d", fake.count())
	}
	time.Sleep(2 * l.debounceWindow)
	if got := fake.count(); got != 1 {
		t.Fatalf("trailing debounce produced %d refreshes, want 1", got)
	}
}

// Cancelling the lifecycle context during the debounce window must suppress
// the pending refresh entirely.
func TestAutoRouteRealtimeListener_StopCancelsPendingRefresh(t *testing.T) {
	fake := &fakeRefresher{}
	l := NewAutoRouteRealtimeListener(nil, fake)
	l.debounceWindow = 40 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); l.debounceLoop(ctx) }()

	l.handleNotification("credentials:UPDATE:1")
	cancel()
	wg.Wait()
	time.Sleep(4 * l.debounceWindow)
	if got := fake.count(); got != 0 {
		t.Fatalf("cancelled listener still ran %d refreshes, want 0", got)
	}
}

// A failing refresh keeps the dirty flag set and retries once per window
// instead of silently dropping the notification.
func TestAutoRouteRealtimeListener_FailedRefreshRetries(t *testing.T) {
	fake := &fakeRefresher{fail: func(call int) error {
		if call == 1 {
			return errors.New("transient refresh failure")
		}
		return nil
	}}
	l := NewAutoRouteRealtimeListener(nil, fake)
	l.debounceWindow = 40 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); l.debounceLoop(ctx) }()

	l.handleNotification("credentials:UPDATE:1")
	if !waitFor(t, 2*time.Second, func() bool { return fake.count() >= 2 }) {
		t.Fatalf("failed refresh was not retried; calls=%d", fake.count())
	}
	if !waitFor(t, 2*time.Second, func() bool { return l.PendingRefreshes() == 0 }) {
		t.Fatalf("pending flag not cleared after successful retry")
	}
	time.Sleep(3 * l.debounceWindow)
	if got := fake.count(); got != 2 {
		t.Fatalf("retry storm: got %d calls, want 2 (fail + success)", got)
	}
}

// An overflowing notify channel must not block the notification producer;
// the coalescing semantics absorb the dropped event.
func TestAutoRouteRealtimeListener_HandleNotificationNeverBlocks(t *testing.T) {
	fake := &fakeRefresher{}
	l := NewAutoRouteRealtimeListener(nil, fake)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			l.handleNotification("burst")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleNotification blocked on a full notify channel")
	}
}

// 缺陷 1（R44 移交 §R44/移交.2）：skipped ≠ 已处理。
//
// refresher「跳过」本次触发（singleflight 忙 / 同 bucket 合并）时返回
// (skipped=true, nil) —— 旧逻辑只看 error，把跳过当成功清掉 pending，
// 该 NOTIFY 永不再重试，而跑着那次 rollup 的 SELECT 早于本次提交，
// 内存索引丢一次路由配置直到下个 ticker。本门守：跳过必须保持 pending
// 并在下一轮窗口重试，重试执行后才允许清 pending。
func TestAutoRouteRealtimeListener_SkippedRefreshKeepsPendingAndRetries(t *testing.T) {
	fake := &fakeRefresher{skip: func(call int) bool { return call == 1 }}
	l := NewAutoRouteRealtimeListener(nil, fake)
	l.debounceWindow = 40 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); l.debounceLoop(ctx) }()

	l.handleNotification("credentials:UPDATE:1")
	if !waitFor(t, 2*time.Second, func() bool { return fake.count() >= 2 }) {
		t.Fatalf("被跳过的刷新没有重试（calls=%d）—— skipped 被当成已处理，pending 被清了", fake.count())
	}
	if !waitFor(t, 2*time.Second, func() bool { return l.PendingRefreshes() == 0 }) {
		t.Fatalf("重试执行成功后 pending 未清")
	}
	time.Sleep(3 * l.debounceWindow)
	if got := fake.count(); got != 2 {
		t.Fatalf("skip + 重试产生 %d 次调用，want 2（跳过 1 次 + 执行 1 次）", got)
	}
}

// 缺陷 1 的另一面：连续被跳过时 pending 必须始终为 1（欠账不丢），
// 且重试按窗口节流（不能变成忙等风暴）。
func TestAutoRouteRealtimeListener_RepeatedlySkippedRefreshStaysPending(t *testing.T) {
	fake := &fakeRefresher{skip: func(int) bool { return true }}
	l := NewAutoRouteRealtimeListener(nil, fake)
	l.debounceWindow = 40 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); l.debounceLoop(ctx) }()

	l.handleNotification("credentials:UPDATE:1")
	// 5 个窗口内每次都跳过：pending 必须一直在，调用按窗口节流。
	if !waitFor(t, 2*time.Second, func() bool { return fake.count() >= 3 }) {
		t.Fatalf("持续跳过时未按窗口重试（calls=%d）", fake.count())
	}
	if got := l.PendingRefreshes(); got != 1 {
		t.Fatalf("持续跳过时 pending = %d, want 1（NOTIFY 的欠账不能丢）", got)
	}
	// 窗口节流：40ms 窗口跑了 ≥3 次 ≈ 每窗 1 次，不是每 tick 一次的忙等。
}
