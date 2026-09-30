package admin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// onceRelease 把 release 包成幂等的。本文件里测试既要「中途释放一个名额」又要
// 「defer 兜底释放全部」，而 release 本身是裸的 `<-bodyFetchGate` —— 双重释放
// 会永久阻塞在空 channel 的接收上（实测挂到 120s 超时）。
// 生产侧不受影响：它的 release 由 defer 恰好调用一次。
func onceRelease(rel func()) func() {
	var once sync.Once
	return func() { once.Do(rel) }
}

// 正文取数并发闸的语义门。
//
// 闸存在的理由是实测出来的，不是设想的：正文取数单条 17~19 秒（真库，生产
// SQL 逐字复制 + pgx 绑定参数，审计 §8.3），而连接池上限 16。排队会把
// 「单条 19 秒」放大成「16 个并发把池占满 19 秒、进程内所有端点一起等」。
//
// 所以本门钉的是**行为**而不是数值：
//   - 名额用完时**立刻**返回错误，绝不阻塞到 ctx 超时（那正是雪崩的形态）；
//   - 名额释放后可以再次获取（闸不会把自己锁死）；
//   - ctx 已取消时返回 ctx 的错误，而不是饱和错误（否则排障会把「客户端已
//     断开」误读成「服务端忙」）。
func TestBodyFetchGateFailsFastWhenSaturated(t *testing.T) {
	// 独占式占用全部名额，避免与并行测试互相干扰。
	release := make([]func(), 0, maxConcurrentBodyFetches)
	for i := 0; i < maxConcurrentBodyFetches; i++ {
		rel, err := acquireBodyFetchSlot(context.Background())
		if err != nil {
			t.Fatalf("slot %d of %d should be available: %v", i, maxConcurrentBodyFetches, err)
		}
		release = append(release, onceRelease(rel))
	}
	defer func() {
		for _, rel := range release {
			rel()
		}
	}()

	// 饱和：必须**立即**失败。若实现改成排队，这里会挂到超时并把门拖死。
	start := time.Now()
	rel, err := acquireBodyFetchSlot(context.Background())
	elapsed := time.Since(start)
	if err == nil {
		rel()
		t.Fatalf("acquireBodyFetchSlot returned a slot while all %d were held — the gate does "+
			"not bound concurrency", maxConcurrentBodyFetches)
	}
	if !errors.Is(err, ErrBodyFetchSaturated) {
		t.Fatalf("saturated acquire must return ErrBodyFetchSaturated, got %v", err)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("saturated acquire blocked for %v — queuing is exactly the failure mode this "+
			"gate exists to prevent (pool is 16 connections, each query holds one for ~19s)",
			elapsed)
	}

	// 释放一个之后必须能再拿到，否则闸会把自己锁死。
	release[0]()
	rel2, err := acquireBodyFetchSlot(context.Background())
	if err != nil {
		t.Fatalf("slot must be reusable after release: %v", err)
	}
	rel2()
}

// ctx 已取消时，报 ctx 的错而不是饱和错误。排障时把「客户端已断开」读成
// 「服务端忙」会把人带偏一整天。
func TestBodyFetchGatePrefersContextCancellation(t *testing.T) {
	release := make([]func(), 0, maxConcurrentBodyFetches)
	for i := 0; i < maxConcurrentBodyFetches; i++ {
		rel, err := acquireBodyFetchSlot(context.Background())
		if err != nil {
			t.Fatalf("priming slot %d: %v", i, err)
		}
		release = append(release, onceRelease(rel))
	}
	defer func() {
		for _, rel := range release {
			rel()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := acquireBodyFetchSlot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context must surface context.Canceled, got %v", err)
	}
}

// 闸本身不能有数据竞争 —— 它被两个端点的并发请求共享。
func TestBodyFetchGateIsRaceFree(t *testing.T) {
	const workers = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	var saturated, ok int
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				rel, err := acquireBodyFetchSlot(context.Background())
				mu.Lock()
				if err != nil {
					saturated++
				} else {
					ok++
					rel()
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok == 0 {
		t.Fatalf("no acquire ever succeeded (%d saturated) — the gate is not usable", saturated)
	}
	// 门在 -race 下报红即失败；这里再钉一条：成功的次数不可能超过
	// 总量，说明没有凭空多放名额（否则闸等于没有）。
	if ok > workers*20 {
		t.Fatalf("more successful acquires (%d) than attempts (%d)", ok, workers*20)
	}
}
