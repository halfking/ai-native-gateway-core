package bg

// AutoIndexRefresher singleflight 互斥的行为门（2026-10-04）
//
// 背景：这个 refresher 有三个并发触发源（5 分钟 ticker、auto_route_refresh 的
// LISTEN 监听、admin 手动接口），实测生产上触发频率是每 8.8 秒一次 ——
// 设计间隔的 34 倍。而 rollup 里的 DELETE+INSERT **不是原子的**，交错会撞
// `duplicate key value violates unique constraint "idx_credential_model_index_hot_unique"`
// （2026-09-10 04:18 真实发生过，注释里有记录）。
//
// 当时的修法是给 INSERT 加 ON CONFLICT —— 让交错变得**无害**，
// 但没有减少交错的**次数**。409 万次删除 / 11.35 天就是这么来的。
// 本文件守的是源头那一半：同一时刻只允许一个 rollup。
//
// ★ 为什么不直接测 RefreshOnce：它需要真 DB（rollup 走 pgxpool），
//   而互斥是**纯内存原语**，在这里测它更快、更不受环境左右。
//   代价是：不能证明「RefreshOnce 真的调了 beginRefresh」——
//   那一半由下面第 3 条用源码契约守住。

import (
	"os"
	"strings"
	"sync"
	"testing"
)

// refresherSource 读源码原文。刻意不用 bg 包里已有的 readSource（签名不同，
// 且它服务于另一个门）—— 两个门共用一个 helper 意味着改一处会同时改动两个门
// 的行为，而其中一个的门并不在本文件里。
func refresherSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("auto_index_refresher.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	return string(b)
}

// funcBody 截出某个具名函数体：从它的声明行到下一个顶格 `}` 为止。
func funcBody(t *testing.T, src, decl string) string {
	t.Helper()
	i := strings.Index(src, decl)
	if i < 0 {
		t.Fatalf("源码里找不到 %q", decl)
	}
	rest := src[i:]
	// 下一个顶格 "}" 结束该函数；函数体里缩进的 } 不算。
	for j, line := range strings.Split(rest, "\n") {
		if j > 0 && strings.HasPrefix(line, "}") {
			return strings.Join(strings.Split(rest, "\n")[:j], "\n")
		}
	}
	t.Fatal("函数体没有闭合的顶层 }")
	return ""
}

// 1. 基本互斥：占住之后第二次必须被拒，释放之后必须能再占。
func TestBeginRefreshIsExclusive(t *testing.T) {
	r := &AutoIndexRefresher{}

	if !r.beginRefresh() {
		t.Fatal("第一次 beginRefresh 应当成功（此时没有刷新在跑）")
	}
	if r.beginRefresh() {
		t.Fatal("已有刷新在跑时，第二次 beginRefresh 必须返回 false —— " +
			"返回 true 就会让两个 rollup 并发跑，DELETE+INSERT 交错会撞 duplicate key")
	}
	r.endRefresh()

	if !r.beginRefresh() {
		t.Fatal("endRefresh 之后必须能再次占上；否则第一次刷新结束后 refresher 永久瘫痪")
	}
	r.endRefresh()
}

//  2. 并发下有且只有一个人占上。
//     这一条是第 1 条的真实并发形态：Go 的 -race 下跑，验证没有数据竞争，
//     且成功次数恰好为 1。
func TestBeginRefreshConcurrentExactlyOneWins(t *testing.T) {
	r := &AutoIndexRefresher{}

	const n = 64
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		winners  int
		refusals int
	)
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // 让所有 goroutine 同时起跑，最大化竞争
			if r.beginRefresh() {
				mu.Lock()
				winners++
				mu.Unlock()
			} else {
				mu.Lock()
				refusals++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if winners != 1 {
		t.Fatalf("并发 beginRefresh 的成功次数 = %d，必须恰好 1；"+
			">1 意味着多个 rollup 会同时执行 DELETE+INSERT（duplicate key 故障）", winners)
	}
	if refusals != n-1 {
		t.Fatalf("被拒次数 = %d，期望 %d", refusals, n-1)
	}

	// 全部释放后必须还能再用（不能锁死）。
	r.endRefresh()
	if !r.beginRefresh() {
		t.Fatal("所有并发调用结束后 refresher 必须恢复可用")
	}
	r.endRefresh()
}

//  3. 契约门：RefreshOnceStatus 必须真的调 tryBeginRefresh/endRefresh。
//     上面两条只测原语；若有人重构时忘了把 RefreshOnce 接到原语上，
//     门会全绿而缺陷依旧存在 —— 这是「门只测了零件、没测装配」的老形态。
//
//     2026-10-05 P2（R44 移交 §R44/移交.2）改写：旧门断言「跳过分支返回
//     nil」，那正是缺陷 1 的病根 —— 跳过返回 nil error 后 LISTEN 监听器把
//     「没执行」当「已处理」清掉 pending，该 NOTIFY 永不再重试，内存索引
//     丢一次路由配置直到下个 ticker。新契约：跳过分支必须以 skipped 语义
//     返回 (true, nil)，由调用方（listener / admin）区分执行与跳过。
func TestRefreshOnceWiresTheSingleflightGuard(t *testing.T) {
	src := refresherSource(t)
	body := funcBody(t, src, "func (r *AutoIndexRefresher) RefreshOnceStatus(")

	if !strings.Contains(body, "r.tryBeginRefresh(") {
		t.Fatal("RefreshOnceStatus 没有调用 tryBeginRefresh ⇒ 互斥与同 bucket 合并形同虚设，" +
			"三个触发源仍会并发/重复跑 rollup")
	}
	if !strings.Contains(body, "defer r.endRefresh()") {
		t.Fatal("RefreshOnceStatus 没有 defer endRefresh ⇒ 一旦 rollup 返回错误或 panic，" +
			"inFlight 永远不会复位，refresher 从此永久跳过所有刷新（比原缺陷更糟：静默失效）")
	}
	// 跳过分支必须返回 skipped 语义 (true, nil)：把「没执行」如实上报 ——
	// 返回 error 会变成「刷新失败」的假告警，返回 (false, nil) 则是伪成功
	// （缺陷 1 本体）。
	skip := branchBody(t, body, "if !r.tryBeginRefresh(gateBucket) {")
	if !strings.Contains(skip, "return true, nil") {
		t.Fatalf("跳过分支必须返回 (skipped=true, nil) —— 没执行不是错误，但也绝不是已处理：\n%s", skip)
	}
	for _, line := range strings.Split(skip, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "return ") && trimmed != "return true, nil" {
			t.Fatalf("跳过分支里出现了其他返回形态 %q —— skipped 语义被破坏（伪成功或假失败）", trimmed)
		}
	}
	// lastBucket 记账必须以 err==nil 为前提：失败不能被记成已执行，否则
	// 失败后的同 bucket 重试（既有失败重试语义）会被合并节流挡住。
	if !strings.Contains(body, "if err == nil") {
		t.Fatal("RefreshOnceStatus 缺少 err==nil 守卫的 lastBucket 记账 ⇒ " +
			"失败的执行也会被记成已执行，同 bucket 的后续重试会被合并挡住")
	}
}

// branchBody 截出以 decl 开头的那一个 {...} 块的内容（不含大括号本身）。
func branchBody(t *testing.T, body, decl string) string {
	t.Helper()
	i := strings.Index(body, decl)
	if i < 0 {
		t.Fatalf("函数体里找不到 %q", decl)
	}
	rest := body[i+len(decl):]
	depth := 1
	for j, line := range strings.Split(rest, "\n") {
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth <= 0 {
			return strings.Join(strings.Split(rest, "\n")[:j], "\n")
		}
	}
	t.Fatalf("以 %q 开头的分支没有闭合", decl)
	return ""
}
