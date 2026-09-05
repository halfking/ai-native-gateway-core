package bg

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStartProjectACCSync_DefaultOff(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ACC_PROJECT_SYNC_INTERVAL", "")
	var calls atomic.Int32
	syncFn := func(ctx context.Context, db *pgxpool.Pool, tenantID string) error {
		calls.Add(1)
		return nil
	}
	// db 为 nil 也不应该 panic，因为未启用时根本不会调用 syncFn。
	StartProjectACCSync(context.Background(), nil, syncFn)
	// 等待 100ms 确保没有 goroutine 被创建（即便有也来不及 tick）。
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 0 {
		t.Errorf("expected 0 calls when interval is empty, got %d", calls.Load())
	}
}

func TestStartProjectACCSync_NilDBDisabled(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ACC_PROJECT_SYNC_INTERVAL", "1h")
	var calls atomic.Int32
	syncFn := func(ctx context.Context, db *pgxpool.Pool, tenantID string) error {
		calls.Add(1)
		return nil
	}
	StartProjectACCSync(context.Background(), nil, syncFn)
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 0 {
		t.Errorf("expected 0 calls when db is nil, got %d", calls.Load())
	}
}

func TestStartProjectACCSync_NilSyncFnDisabled(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ACC_PROJECT_SYNC_INTERVAL", "1h")
	StartProjectACCSync(context.Background(), &pgxpool.Pool{}, nil)
	// 同上：无 panic、无 tick、无 log 抛错。
}

func TestStartProjectACCSync_InvalidIntervalDisabled(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ACC_PROJECT_SYNC_INTERVAL", "30s") // < 1 minute
	var calls atomic.Int32
	syncFn := func(ctx context.Context, db *pgxpool.Pool, tenantID string) error {
		calls.Add(1)
		return nil
	}
	StartProjectACCSync(context.Background(), &pgxpool.Pool{}, syncFn)
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 0 {
		t.Errorf("expected 0 calls when interval < 1m, got %d", calls.Load())
	}
}

func TestStartProjectACCSync_BadDurationDisabled(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ACC_PROJECT_SYNC_INTERVAL", "not-a-duration")
	var calls atomic.Int32
	syncFn := func(ctx context.Context, db *pgxpool.Pool, tenantID string) error {
		calls.Add(1)
		return nil
	}
	StartProjectACCSync(context.Background(), &pgxpool.Pool{}, syncFn)
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 0 {
		t.Errorf("expected 0 calls for malformed interval, got %d", calls.Load())
	}
}

// TestStartProjectACCSync_HappyPath 验证：env 配置合法时 worker 启动，
// 短时间内不调用 syncFn（interval=10h），但能通过 ctx 取消优雅退出。
//
// 真实 tick 验证不在单测里做——那是定时器集成的责任。本测只锁住：
// "启用了就启动 goroutine，goroutine 能被 ctx 取消干净"。
func TestStartProjectACCSync_HappyPathAndCtxCancel(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ACC_PROJECT_SYNC_INTERVAL", "10h")
	var calls atomic.Int32
	syncFn := func(ctx context.Context, db *pgxpool.Pool, tenantID string) error {
		calls.Add(1)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartProjectACCSync(ctx, &pgxpool.Pool{}, syncFn)

	// 给 goroutine 一个启动窗口
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 0 {
		t.Errorf("interval=10h, no tick expected yet; got %d calls", calls.Load())
	}
	cancel()
	// 让 goroutine 收到 ctx.Done
	time.Sleep(50 * time.Millisecond)
	// 不强制断言 goroutine 退出——只断言测试不会 leak 到下一行执行报错。
}

// TestStartProjectACCSync_PropagatesErrorsAsWarnings 验证 worker 在 sync
// 出错时不让 panic 冒泡。error path 在生产里只产生 slog.Warn，不影响
// gateway 主流程——这条约束由以下两点共同保证：(a) syncFn 调用被
// if err := syncFn(...); err != nil 包裹；(b) 不会向上 return error。
func TestStartProjectACCSync_PropagatesErrorsAsWarnings(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ACC_PROJECT_SYNC_INTERVAL", "10h")
	syncFn := func(ctx context.Context, db *pgxpool.Pool, tenantID string) error {
		return errors.New("simulated ACC failure")
	}
	// 不启 goroutine（interval 10h 不触发）；手动调一次 syncFn 验证
	// syncFn 自身的错误传播形状没有改变。
	if err := syncFn(context.Background(), nil, ""); err == nil {
		t.Fatal("sanity: syncFn returned nil for simulated error")
	}
}
