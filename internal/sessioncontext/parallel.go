package sessioncontext

// parallel.go —— 归约的逐条并行处理（2026-11-23，大请求体归约提速轮）。
//
// # 为什么可以并行
//
// 归约对「每一条消息」的处理是彼此独立的纯计算：元数据轮只看这条消息的
// 字节（结构描述 + 预算上界）；守卫/摘录轮只看这条消息的字节、编译后的
// 规则策略、以及只读的查询词项。条与条之间没有可变状态共享。
//
// # 不变量：与串行逐条处理**逐字节等价**
//
// 并行最容易悄悄改变的是**出错语义**。串行版本在下标最小的那条错误处就
// 返回（后面的条目根本不处理）。这里把它钉成规则：
//
//   - 每个条目的结果只写自己的下标槽位（条目之间无共享可变状态）；
//   - 任何一条出错之后**不再装载新条目**；已在途的条目跑完，但结果丢弃；
//   - 最终返回「下标最小的那条错误」——与串行版本返回的是同一条。
//
// # 两种资源分开
//
//   - load(i)：在调用方协程里**按下标顺序**执行。它碰的是顺序资源（归档
//     文件只有一个游标，顺序读最省）；
//   - work(i, v)：在工作协程里执行，只做 CPU 计算。
//
// # 并发安全是显式声明的
//
// 并行度只在 ctx 里有 WithReduceWorkers 时才 >1。归约器自己不假设守卫
// 可并发——守卫是任意外部注入的函数，是否可并发由注入方（streaming）声明。

import (
	"context"
	"sync"
	"sync/atomic"
)

type reduceWorkersKey struct{}

// WithReduceWorkers 声明本次归约可用的并行度（n ≤ 1 ⇒ 串行）。
//
// 调用方**只有在守卫可并发调用时**才应设置 >1 的值（见 streaming 的
// SetHistoryGuardConcurrencySafe）。未设置 ⇒ 完全串行，行为与此前逐字相同。
func WithReduceWorkers(ctx context.Context, n int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if n <= 1 {
		n = 1
	}
	return context.WithValue(ctx, reduceWorkersKey{}, n)
}

// ReduceWorkers 返回 ctx 声明的归约并行度（未声明 ⇒ 1）。给调用方做观测
// （「这次是串行还是并行」要进指标 label）。
func ReduceWorkers(ctx context.Context) int { return reduceWorkers(ctx) }

func reduceWorkers(ctx context.Context) int {
	if ctx == nil {
		return 1
	}
	if n, ok := ctx.Value(reduceWorkersKey{}).(int); ok && n > 1 {
		return n
	}
	return 1
}

// loadWindow 是装载窗口：装载第 i 条之前第 i−window 条必须已完成。
//
// 取 8×workers：窗口太小的话，遇到一条慢消息，其余协程把窗口里剩下的条目做完
// 就得空转等它；8 倍于协程数足以把耗时差异（最长/平均约几倍）吸收掉。上限侧
// 的代价：出错后至多多装载 window−1 条（每条一次守卫判定，而这次请求本来就要
// 中止），在途内存至多 window 条 ≤64KB 的消息（8 协程时 4MB）。
func loadWindow(workers int) int { return 8 * workers }

// runIndexed 用至多 workers 个工作协程处理下标 [0, n)：
//
//	load(i)        在调用方协程里按下标顺序执行（顺序资源）；
//	work(i, v)     在工作协程里执行，只能写第 i 个槽位；
//
// 返回下标最小的错误（idx=-1, err=nil 表示全部成功）。workers ≤ 1 时是
// 普通串行循环。
//
// ★ 「出错后不再装载」有硬上界：装载是**有序窗口**——装载第 i 条之前，第
// i−window 条必须已经完成（window = loadWindow(workers)）。出错的条目先置
// 失败标记、再发完成信号 ⇒ 下标 e 出错之后，装载方走到 e+window 就必然停下
// 来等 e 完成，等到时失败标记已置位 ⇒ 至多装载到下标 e+window−1。
//
// 为什么窗口必须有序：计数型窗口（「未完成条目数 ≤ window」）挡不住下面的
// 情况——出错的那条还在队列里没被处理，别的名额被后面的条目循环占用（装载
// → 瞬间完成 → 归还名额），装载方就能一直往前走；出错后装载多少条只取决于
// 调度（2026-11-23 实测过百，-race 下更多）。
//
// ★ load 的返回值会在工作协程里被使用：调用方必须交出它的所有权（之后
// 不再读写）。
func runIndexed(ctx context.Context, workers, n int, load func(i int) (any, error), work func(i int, v any) error) (int, error) {
	if n <= 0 {
		return -1, nil
	}
	if workers <= 1 || n == 1 {
		for i := 0; i < n; i++ {
			v, err := load(i)
			if err != nil {
				return i, err
			}
			if err := work(i, v); err != nil {
				return i, err
			}
		}
		return -1, nil
	}
	if workers > n {
		workers = n
	}

	type job struct {
		i    int
		v    any
		done chan struct{} // 完成时关闭；装载方等的是「第 i−window 条」的它
	}
	window := loadWindow(workers)
	slots := make([]chan struct{}, window) // slots[i%window] = 第 i 条的完成信号
	var (
		jobs   = make(chan job, workers)
		errs   = make([]error, n) // 每个槽位恰有一个写者
		failed atomic.Bool        // 任一条目出错 ⇒ 置位，装载方据此停下
		wg     sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for jb := range jobs {
				if err := work(jb.i, jb.v); err != nil {
					errs[jb.i] = err
					failed.Store(true)
				}
				close(jb.done) // 先记失败、再发完成信号（见函数注释）
			}
		}()
	}

	loadErrIdx, loadErr := -1, error(nil)
feed:
	for i := 0; i < n; i++ {
		// 先查取消再查失败再装载：顺序固定 ⇒ 「进来时 ctx 已取消」恒在下标 0
		// 返回（与「channel 可发送 / ctx 已取消同时就绪」的随机选择无关）。
		if e := ctx.Err(); e != nil {
			loadErrIdx, loadErr = i, e
			break
		}
		if failed.Load() {
			break // 已有在途条目出错：不再装载（对齐串行「第一条错就停」）
		}
		if i >= window {
			select {
			case <-slots[i%window]: // 第 i−window 条已完成
			case <-ctx.Done():
				loadErrIdx, loadErr = i, ctx.Err()
				break feed
			}
			if failed.Load() {
				break // 刚完成的可能就是那条出错的条目
			}
		}
		v, err := load(i)
		if err != nil {
			loadErrIdx, loadErr = i, err
			break
		}
		done := make(chan struct{})
		slots[i%window] = done
		select {
		case jobs <- job{i, v, done}:
		case <-ctx.Done():
			loadErrIdx, loadErr = i, ctx.Err()
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if loadErrIdx >= 0 {
		errs[loadErrIdx] = loadErr
	}
	for i, err := range errs {
		if err != nil {
			return i, err
		}
	}
	return -1, nil
}

// firstErrIndex 返回 errs 里下标最小的错误下标（没有 ⇒ -1）。
func firstErrIndex(errs []error) int {
	for i, err := range errs {
		if err != nil {
			return i
		}
	}
	return -1
}
