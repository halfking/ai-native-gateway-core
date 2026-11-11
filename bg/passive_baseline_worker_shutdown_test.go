// passive_baseline_worker_shutdown_test.go —— 退出路径的两道钉测（★ 2026-11-11 审计 P2-L / P2-M）
//
// ★ 本文件的两条用例**各自都有变异臂**（★ 手工验证过 ⇒ 见提交说明）：
// ★ ① P2-L：★ 把 `w.stopOnce.Do(close)` 换回 `select { case <-w.stop: default: close(w.stop) }`
//   ⇒ ★★ 并发 Stop() ⇒ ★★ panic: close of closed channel ⇒ ★★ 用例红
// ★ ② P2-M：★ 把退出路径的 `boundedFlushCtx()` 换回 `context.Background()`
//   ⇒ ★★ 退出期刷库**无上界** ⇒ ★★ 用例红（★ sink 收到的 ctx 没有 deadline）

package bg

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
)

// ctxDeadlineSink 记录每次 Upsert 收到的 ctx 的 deadline（★ 用来观测「退出期刷库是否有界」）。
type ctxDeadlineSink struct {
	mu        sync.Mutex
	deadlines []time.Time
	hasDl     []bool
}

func (s *ctxDeadlineSink) UpsertPassiveBaseline(ctx context.Context, fp *modelcredibility.PassiveFingerprint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dl, ok := ctx.Deadline()
	s.deadlines = append(s.deadlines, dl)
	s.hasDl = append(s.hasDl, ok)
	return nil
}

func (s *ctxDeadlineSink) LoadPassiveBaseline(ctx context.Context, tenantID string, credentialID int64, modelName string) (*modelcredibility.PassiveFingerprint, error) {
	return nil, nil
}

func (s *ctxDeadlineSink) snapshot() ([]time.Time, []bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.deadlines...), append([]bool(nil), s.hasDl...)
}

// TestPassiveBaselineWorkerConcurrentStopNoPanic 钉住 P2-L：
//
// ★ 缺陷：★ `Stop()` 用 `select { case <-w.stop: default: close(w.stop) }` 关通道
// ★ ★⇒ ★★ select+default **不是原子** ⇒ ★★ 两个 goroutine 可同时判定「通道未关」
// ★ ★⇒ ★★ 第二个 close ⇒ ★★ panic: close of closed channel
// ★ ★⇒ ★★ 且 panic 在**退出路径**上 ⇒ ★★ 进程退出被打断 ⇒ ★★ 残余窗口没刷净。
//
// ★ 修法：★ `sync.Once` 保证「关通道」恰好一次（★ Stop 仍幂等）。
//
// ★ 为什么这条门有意义：★ Stop() 的**幂等**是文档承诺（★ 「幂等；未启动时 no-op」），
// ★★ 而「幂等」的定义就包含「并发调用安全」⇒ ★★ 文档承诺与实现不符。
func TestPassiveBaselineWorkerConcurrentStopNoPanic(t *testing.T) {
	// ★ 多轮 × 多 goroutine：★ 放大「两个 goroutine 同时落在 select/default 之间」的窗口
	for round := 0; round < 60; round++ {
		w := NewPassiveBaselineWorker(&idlePassiveSink{}, time.Millisecond)
		w.Start(context.Background())

		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				w.Stop() // ★ 并发 Stop() ⇒ 旧实现会 panic
			}()
		}
		wg.Wait()
	}
}

// TestPassiveBaselineWorkerExitFlushHasBoundedContext 钉住 P2-M：
//
// ★ 缺陷：★ 退出路径（★ shutdown / stop）用 `context.Background()` 刷库 ⇒ ★ 无上界
// ★ ★⇒ ★★ DB 挂起 / 网络不可达 ⇒ ★★ flushOnce 无限阻塞 ⇒ ★★ 进程**永不退出**
// ★ ★⇒ ★★ 且越是在故障时越容易卡死（★ 故障与退出叠加）。
//
// ★ 修法：★ 退出路径改用**有界** ctx（★ passiveBaselineFlushTimeout = 30s）。
//
// ★ 断言方式：★ **观测 sink 实际收到的 ctx**（★ 不是断言源码里有没有那个常量）⇒
// ★ ★⇒ ★★ 退出期那次落库若没有 deadline ⇒ ★★ 门红。
func TestPassiveBaselineWorkerExitFlushHasBoundedContext(t *testing.T) {
	sink := &ctxDeadlineSink{}
	// ★ 间隔取 1h ⇒ ★ 期间**只有退出那一次**刷库（★ 不让 tick 臂混进来）
	w := NewPassiveBaselineWorker(sink, time.Hour)

	// ★ 投一条特征 ⇒ ★ 退出期 flush 真的会产生一次落库（★ 空 worker 不写 ⇒ 断言无意义）
	w.Submit(&modelcredibility.PassiveFeatures{
		TenantID:     "tenant-shutdown",
		ModelName:    "unit-model",
		CredentialID: 42,
		RequestID:    "req-shutdown",
		MeasuredAt:   time.Now(),
		TTFT:         10 * time.Millisecond,
	})

	w.Start(context.Background())
	w.Stop() // ★ 走 stop 分支 ⇒ 那一次 flush 必须带 deadline

	deadlines, hasDl := sink.snapshot()
	if len(deadlines) == 0 {
		t.Fatalf("exit flush must actually write a baseline row (★ got 0 writes ⇒ 断言无意义)")
	}
	for i, ok := range hasDl {
		if !ok {
			t.Fatalf("exit flush #%d carried a ctx with NO deadline (★ ⇒ 刷库无上界 ⇒ ★ 进程可能永不退出)", i)
		}
	}
	for i, dl := range deadlines {
		if until := time.Until(dl); until > passiveBaselineFlushTimeout+2*time.Second {
			t.Fatalf("exit flush #%d deadline too far: %s (★ want <= %s)", i, until, passiveBaselineFlushTimeout)
		}
	}
}
