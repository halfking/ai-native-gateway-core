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

// TestSessionAnalyticsViews_CoverBothViewFamilies 钉测：清单必须同时覆盖
// 357 族（3 个）与 981 族（4 个），且无重复、无意外项。
//
// ★ 为什么从 "_AreTheThreeFromMigration357" 改名：本清单在 981 轮扩容了
//
//	（981 建的 4 个会话分析视图也必须被刷新，见 refresher 里的注释）。
//	⇒ 断言从「恰好 3 个」改成「恰好 7 个且分属两族」——
//	**把「数量写死」改成「集合写死」**：数量是易过期数字，
//	集合才是判别力所在（多一个少一个都要能看出来）。
func TestSessionAnalyticsViews_CoverBothViewFamilies(t *testing.T) {
	want := []string{
		// 357 族（迁移 357）
		"session_client_stats", "session_task_stats", "session_client_task_matrix",
		// 981 族（迁移 981：会话分析 4 个视图）
		"user_session_aggregates", "project_task_aggregates",
		"agent_usage_aggregates", "model_usage_aggregates",
	}
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
	for _, w := range want {
		if !seen[w] {
			t.Fatalf("清单缺少视图 %q —— 它不会被刷新 ⇒ 视图会停在迁移那一刻", w)
		}
	}
}

// TestSessionViewRefreshBudget_ExceedsMeasuredRefresh：单视图预算必须明显大于
// 实测耗时。252 生产 pg_stat_statements（2026-11-22，338~64 次完成样本）：
// 单视图最慢 user_session_aggregates max 577.8s（被杀的运行不进 pgss ⇒ 尾部
// 被低估，但预算算术的主判据是「锁等待 + 语句上限」，见
// TestSessionViewRefreshBudget_CoversLockBudgetPlusStatement）。
//
// 这个不等式是**安全方向**的：给小了不会报错，而是该视图被自己的预算掐断，
// 表现为「这个视图本轮没刷新」。
//
// ★ 2026-11-22 从「整轮 70s 上界」改钉「单视图 580s 下界」：整轮共享 ctx
// 已废除（252 实测被 981 族吃光、尾部视图饿死），预算的对象换成单视图。
func TestSessionViewRefreshBudget_ExceedsMeasuredRefresh(t *testing.T) {
	// 252 实测单视图最坏完成耗时 577.8s，向上取整到 10s 整数倍。
	const measuredWorstSingleView = 580 * time.Second
	if SessionViewRefreshBudget <= measuredWorstSingleView {
		t.Fatalf("SessionViewRefreshBudget=%s 未覆盖实测单视图最坏耗时 %s —— 视图会被自己的预算静默掐断",
			SessionViewRefreshBudget, measuredWorstSingleView)
	}
	// 与 routing 族同一条不变量：预算必须小于间隔，否则单视图会拖进下一轮。
	if SessionViewRefreshBudget >= SessionViewsInterval {
		t.Fatalf("预算 %s 必须小于间隔 %s，否则刷新周期会堆积", SessionViewRefreshBudget, SessionViewsInterval)
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
	// ★ 2026-10-22：判据从 `r.refreshView(ctx, view, true)` 放宽为
	// `r.refreshView(ctx, view, true,` —— **意图不变**（逐个视图调用、
	// 且必须走 advisory 锁路径），只是不再把「参数个数」也钉死：
	// session 族新增了分族语句上限参数（sessionViewsStatementTimeout）。
	// 保持 `true`（useAdvisoryLock）仍然被断言 ⇒ session 族不会偷偷绕开锁。
	if !strings.Contains(body, "r.refreshView(ctx, view, true,") {
		t.Fatal("refreshSessionViews 没有逐个调 refreshView（且必须走 advisory 锁路径）")
	}
	// 单视图失败不能终止整轮：三个视图用途独立。
	if strings.Contains(body, "return\n\t}") && !strings.Contains(body, "// 不 return") {
		t.Fatal("疑似在失败分支提前 return —— 单个视图失败会作废已刷好的")
	}
}
