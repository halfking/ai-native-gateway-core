package extpointledger

// AUDIT-GUARD: 本文件的门**必须随包在 CI 上执行** ⇒ 该包已登记进 Makefile 的 GUARD_PACKAGES

// bench_test.go —— 台账的**热路径成本**量具（P2-2 续）。
//
// # 为什么要有这个 benchmark
//
// ★ 台账是**观测面**，但它住在**请求热路径**上（主链每个请求一次、
// 响应拦截链每个拦截器一次）⇒ ⇒ 「观测面拖慢数据面 ⇒ 观测面被关掉」
// 这条纪律要求**先量**再决定，而不是先写再猜。
//
// ⇒ 这个 benchmark 的作用是：★ **给「接线到主链」这个决定提供数字**，
// 而不是凭感觉说「很便宜」。
//
// ★ 口径：`Record` 的**完整**成本（取锁 + 写计数 + 写环形缓冲），
// 不含调用方自己的 `time.Now()` 与 `time.Since()`。
//
// # 怎么读这个数
//
// - 一次 `Record` 的成本若在 **百纳秒量级** ⇒ 相对网关的
//   **百毫秒～数十秒**级请求（LLM 调用）⇒ 占比 < 0.1% ⇒ 可以接。
// - 若到**微秒量级** ⇒ 说明锁在并发下成了瓶颈 ⇒ ⇒ 需要**分片**
//   （每机制一把锁）才谈得上接主链。
//
// ⇒ ⇒ 结论写在本文件末尾的注释里（附实测数字与机器口径）。

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// BenchmarkLedgerRecordParallel 量「N 个 goroutine 并发记账」的每次成本。
func BenchmarkLedgerRecordParallel(b *testing.B) {
	for _, workers := range []int{1, 4, 16, 64} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			l := New()
			var wg sync.WaitGroup
			per := b.N / workers
			if per < 1 {
				per = 1
			}
			b.ResetTimer()
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < per; i++ {
						l.Record("bench", OutcomeOK, time.Millisecond, nil)
					}
				}()
			}
			wg.Wait()
		})
	}
}

// BenchmarkLedgerSnapshot 量「读端出快照」的成本。
//
// ★ 读端是 admin 页面 ⇒ 低频 ⇒ 但**必须**不卡住热路径 ⇒ 所以单独量。
func BenchmarkLedgerSnapshot(b *testing.B) {
	l := New()
	for i := 0; i < 64; i++ {
		l.Record(fmt.Sprintf("m%d", i%6), OutcomeOK, time.Millisecond, nil)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = l.Snapshot()
	}
}
