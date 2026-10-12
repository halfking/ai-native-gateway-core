package bg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// ── 357 会话分析物化视图接入刷新器（2026-10-07）────────────────────────────
//
// 缺陷形态：357 建了三个视图并在迁移末尾刷新一次，之后**没有任何刷新排程**。
// 245 实测：视图 06:31:36 填过，16:07 时端点仍在返回它（陈旧 9h37m），
// 而 session_summaries 一直在写。端点没有新鲜度门 ⇒ 过期被伪装成「就是这个数」。

func TestSessionAnalyticsViews_AreTheThreeFromMigration357(t *testing.T) {
	want := []string{"session_client_stats", "session_task_stats", "session_client_task_matrix"}
	if len(SessionAnalyticsViews) != len(want) {
		t.Fatalf("视图数应为 %d，实际 %d：%v", len(want), len(SessionAnalyticsViews), SessionAnalyticsViews)
	}
	seen := map[string]bool{}
	for _, v := range SessionAnalyticsViews {
		if seen[v] {
			t.Fatalf("清单里有重复项 %q —— 一轮会刷两遍", v)
		}
		seen[v] = true
		found := false
		for _, w := range want {
			if v == w {
				found = true
			}
		}
		if !found {
			t.Fatalf("清单里有非预期视图 %q", v)
		}
	}
}

// TestSessionViewsTimeout_ExceedsMeasuredRefresh：超时必须明显大于实测耗时。
// 245 实测（623,006 行 / 919MB）：三视图聚合合计 ≈24s（8.3/4.8/10.9），
// CONCURRENTLY 另需建堆与唯一索引差，整轮量级 40-70s。超时给到 10min。
//
// 这个不等式是**安全方向**的：给小了不会报错，而是整轮被 ctx 掐断，
// 表现为「后几个视图根本没刷新」且**没有一条错误日志** —— 比整体失败更难发现。
func TestSessionViewsTimeout_ExceedsMeasuredRefresh(t *testing.T) {
	if SessionViewsTimeout <= 70*time.Second {
		t.Fatalf("SessionViewsTimeout=%s 未覆盖实测的 70s 上界 —— 整轮会被 ctx 静默掐断",
			SessionViewsTimeout)
	}
	// 与 routing 族同一条不变量：超时必须小于间隔，否则周期会堆积。
	if SessionViewsTimeout >= SessionViewsInterval {
		t.Fatalf("超时 %s 必须小于间隔 %s，否则刷新周期会堆积", SessionViewsTimeout, SessionViewsInterval)
	}
}

func TestRefreshSessionViews_NilDBIsNoOp(t *testing.T) {
	// no-DB 模式下必须安静退出：panic 会打掉整个网关的启动流程。
	r := &MaterializedViewRefresher{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.refreshSessionViews(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("db==nil 时 refreshSessionViews 必须立即返回，不能挂住")
	}
}

// TestRefreshLoop_WiresSessionTimer：接线必须真的在 refreshLoop 里。
// 只断言「存在一个 refreshSessionViews 方法」不够 —— 方法可以是死代码
// （本仓有过 coordination 指标族零调用死代码的先例）。
func TestRefreshLoop_WiresSessionTimer(t *testing.T) {
	b, err := os.ReadFile("materialized_view_refresher.go")
	if err != nil {
		t.Fatalf("读源文件：%v", err)
	}
	src := string(b)
	loopAt := strings.Index(src, "func (r *MaterializedViewRefresher) refreshLoop(")
	if loopAt < 0 {
		t.Fatal("没找到 refreshLoop")
	}
	end := strings.Index(src[loopAt:], "\n}\n")
	if end < 0 {
		t.Fatal("refreshLoop 抽取失败")
	}
	loop := src[loopAt : loopAt+end]

	if !strings.Contains(loop, "sessionTimer") {
		t.Fatal("refreshLoop 里没有 sessionTimer —— 挂进刷新器的说法不成立")
	}
	// 关键：必须断言调用在 **case <-sessionTimer.C 分支内**。
	// 只断言「函数体里出现过 r.refreshSessionViews(ctx)」是不够的 ——
	// 首轮那次调用也在体内，把循环里那次删掉照样能通过断言，
	// 而那正是「接了首轮、之后再也不刷」的形态：视图会一直停在首轮那一份。
	caseAt := strings.Index(loop, "case <-sessionTimer.C:")
	if caseAt < 0 {
		t.Fatal("refreshLoop 里没有 case <-sessionTimer.C 分支")
	}
	branch := loop[caseAt:]
	if !strings.Contains(branch, "r.refreshSessionViews(ctx)") {
		t.Fatal("sessionTimer 分支里没有调用 refreshSessionViews —— 之后不会再刷新")
	}
	if !strings.Contains(branch, "sessionTimer.Reset(") {
		t.Fatal("sessionTimer 分支里没有 Reset —— 只会刷新一次")
	}
	// 刻意不新起 goroutine：Stop() 靠 refreshLoop 里的 close(r.done) 收口。
	if strings.Contains(loop, "Go(\"session") || strings.Contains(loop, "go r.refreshSessionViews") {
		t.Fatal("session 刷新不应新起 goroutine —— Stop() 依赖 refreshLoop 收口 done")
	}
}

// TestRefreshSessionViews_RefreshesEveryListedView：每个清单里的视图都要被刷到。
// 用「方法体内出现 SessionAnalyticsViews 的 range 循环」来锁，防止有人把
// 循环写成硬编码两个视图。
func TestRefreshSessionViews_RefreshesEveryListedView(t *testing.T) {
	b, err := os.ReadFile("materialized_view_refresher.go")
	if err != nil {
		t.Fatalf("读源文件：%v", err)
	}
	src := string(b)
	at := strings.Index(src, "func (r *MaterializedViewRefresher) refreshSessionViews(")
	if at < 0 {
		t.Fatal("没找到 refreshSessionViews")
	}
	end := strings.Index(src[at:], "\n}\n")
	if end < 0 {
		t.Fatal("refreshSessionViews 抽取失败")
	}
	body := src[at : at+end]
	if !strings.Contains(body, "for _, view := range SessionAnalyticsViews") {
		t.Fatal("refreshSessionViews 没有遍历 SessionAnalyticsViews —— 清单改了这里不会跟着变")
	}
	if !strings.Contains(body, "r.refreshView(ctx, view, true)") {
		t.Fatal("refreshSessionViews 没有逐个调 refreshView")
	}
	// 单视图失败不能终止整轮：三个视图用途独立。
	if strings.Contains(body, "return\n\t}") && !strings.Contains(body, "// 不 return") {
		t.Fatal("疑似在失败分支提前 return —— 单个视图失败会作废已刷好的")
	}
}
