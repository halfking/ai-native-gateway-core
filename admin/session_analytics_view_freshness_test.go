package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
)

// ── 357 会话分析视图的「陈旧度门」（2026-10-07）───────────────────────────
//
// 为什么需要第二道门：42P01 那道只认「视图不存在」。但 357 应用之后有个更难
// 发现的形态 —— 视图在，数据停在上一次刷新那一刻。2026-10-07 在 245 上实测：
// 视图 06:31:36 填过，之后全仓没有刷新排程，而 session_summaries 一直在写；
// 端点从 503 变成 200，返回 9h37m 前的数字，没有错误也没有提示。
//
// 本组用例把**放行与拦截两条分支都钉住**。只测拦截的话，「永远返回 true」
// 的实现会全绿 —— 那正是这道门最坏的失效方式（把过期数字继续发出去）。

// freshAll / staleOf 造探针：前 n 个视图说新鲜，其余说陈旧。
func freshAll(view string) bool { return true }

func TestRequireFreshSessionViews_AllFreshPasses(t *testing.T) {
	rec := httptest.NewRecorder()
	ok := requireFreshSessionViewsWith(rec, "/api/admin/session-analytics/clients", freshAll,
		"session_client_stats")
	if !ok {
		t.Fatalf("全部新鲜时必须放行，实际拦下了。body=%s", rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("放行时不应写任何响应体，实际写了：%s", rec.Body.String())
	}
}

func TestRequireFreshSessionViews_OneStaleBlocks(t *testing.T) {
	staleTask := func(view string) bool { return view != "session_task_stats" }

	rec := httptest.NewRecorder()
	ok := requireFreshSessionViewsWith(rec, "/api/admin/session-analytics/tasks", staleTask,
		"session_task_stats")
	if ok {
		t.Fatal("有陈旧视图时必须拦下，实际放行了")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("陈旧应返回 503，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "analytics_view_stale") {
		t.Fatalf("错误码必须是 analytics_view_stale（与缺视图的 analytics_view_missing 区分开）：%s", body)
	}
	if !strings.Contains(body, "session_task_stats") {
		t.Fatalf("响应必须点名是哪个视图陈旧：%s", body)
	}

	// 与缺视图门分开是两个错误码，不能合并 —— 运维看到 missing 去应用迁移，
	// 看到 stale 去查刷新器，处置完全不同。
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是 JSON：%v body=%s", err, body)
	}
}

func TestRequireFreshSessionViews_NoDBFailsClosed(t *testing.T) {
	// db == nil（no-DB 模式 / 单测）时，mvFreshWithinBudget 一律 false。
	// 这里锁的是**不许放行**：如果改成 nil db 就放行，no-DB 部署会重新开始
	// 对着可能不存在或未刷新的视图发数字。
	r := httptest.NewRequest(http.MethodGet, "/api/admin/session-analytics/clients", nil)
	probe := mvFreshWithinBudgetFor(r.Context(), nil, sessionMvFreshnessBudget)
	if probe("session_client_stats") {
		t.Fatal("db==nil 时不得报告新鲜（必须 fail-closed）")
	}
	rec := httptest.NewRecorder()
	if requireFreshSessionViewsWith(rec, r.URL.Path, probe, "session_client_stats") {
		t.Fatal("db==nil 时必须拦下")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望 503，实际 %d", rec.Code)
	}
}

// sessionMeasuredRefreshSeconds 是 245 线上实测的一轮耗时（2026-10-08 00:32:46
// → 00:33:50，三个视图 10.6s / 19.6s / 33.3s）。它不是配置项，是**量出来的**，
// 用来把下面的算术钉在真实数值上，而不是拍一个「余量」。
const sessionMeasuredRefreshSeconds = 64 * time.Second

// TestSessionMvFreshnessBudget_Arithmetic：预算不是「比间隔大一点」就够，也不是
// 越大越好。它被两侧夹住，两侧都来自算术：
//
//	下界：稳定态最坏年龄 = 间隔 + 一轮实测耗时，必须 < 预算
//	      否则每次都在刷新前一刻被判为陈旧，功能等于常年 503。
//	上界：漏一轮之后的年龄 = 2×间隔 + 一轮耗时，必须 ≥ 预算
//	      超了就等于允许把两小时前的数字当结论发出去 —— 那正是这道门要消灭的
//	      形态。90min 是刻意的选择：漏一轮后 503 到下一轮成功（约一小时自愈），
//	      期间不发过期数字。
//
// ★ 上一版这条断言是**错的**：它只查 budget > 间隔，还把「90min = 60min 间隔
//   - 一轮漏刷余量」写进了失败信息。按实测算，漏一轮后是 121min，早就超 90min。
//     判据与设计共享同一个错误前提 ⇒ 谁也发现不了。
//     现在两侧都算，且都从 bg 的真常量取（admin 本来就 import bg，无环）。
func TestSessionMvFreshnessBudget_Arithmetic(t *testing.T) {
	interval := bg.SessionViewsInterval

	steadyState := interval + sessionMeasuredRefreshSeconds
	if sessionMvFreshnessBudget <= steadyState {
		t.Fatalf("陈旧度预算 %s 必须大于稳定态最坏年龄（间隔 %s + 实测一轮 %s = %s），"+
			"否则每次刷新前一刻都会被判为陈旧，功能等于常年 503",
			sessionMvFreshnessBudget, interval, sessionMeasuredRefreshSeconds, steadyState)
	}

	oneMissedCycle := 2*interval + sessionMeasuredRefreshSeconds
	if sessionMvFreshnessBudget >= oneMissedCycle {
		t.Fatalf("陈旧度预算 %s 不得达到漏一轮后的年龄（%s）—— 那会让两小时前的数字"+
			"继续被当成结论发出去，正是这道门要消灭的形态。"+
			"（若确实要放宽，先想清楚运维是否还能看出数据已经过期）",
			sessionMvFreshnessBudget, oneMissedCycle)
	}

	// 上界算术依赖超时 < 间隔；顺带锁住这条，否则间隔被调小后上面的数会失真。
	if bg.SessionViewsTimeout >= bg.SessionViewsInterval {
		t.Fatalf("超时 %s 必须小于间隔 %s，否则刷新周期会堆积",
			bg.SessionViewsTimeout, bg.SessionViewsInterval)
	}
}

// TestSessionAnalyticsHandlers_GateRunsAfterAuthBeforeQuery：门的位置有**两个**
// 方向的要求，两个都会让任何现有用例保持全绿：
//
//	太早（权限检查之前）→ 405/403 也去查一次库；无凭据的调用方能探到库状态，
//	                   且把一次廉价拒绝变成一次数据库往返。
//	太晚（查询之后）   → 过期数据已经付过一遍查询代价。
//
// 只断言「门 < 查询」会漏掉第一种错位 —— 插在函数签名后的门也满足「门 < 查询」。
func TestSessionAnalyticsHandlers_GateRunsAfterAuthBeforeQuery(t *testing.T) {
	cases := []struct {
		file string
		fn   string
		view string
	}{
		{"session_analytics_clients.go", "handleClientAnalyticsList", "session_client_stats"},
		{"session_analytics_clients.go", "handleClientAnalyticsDetail", "session_client_stats"},
		{"session_analytics_tasks.go", "handleTaskAnalyticsList", "session_task_stats"},
		{"session_analytics_tasks.go", "handleTaskAnalyticsDetail", "session_task_stats"},
	}
	cache := map[string]string{}
	for _, c := range cases {
		src, ok := cache[c.file]
		if !ok {
			b, err := os.ReadFile(filepath.Join(".", c.file))
			if err != nil {
				t.Fatalf("读 %s：%v", c.file, err)
			}
			src = string(b)
			cache[c.file] = src
		}
		body := extractFuncBody(src, c.fn)
		if body == "" {
			t.Fatalf("%s：没定位到 %s 的函数体", c.file, c.fn)
		}
		gateAt := strings.Index(body, "requireFreshSessionViews(")
		if gateAt < 0 {
			t.Fatalf("%s：%s 没有调用新鲜度门 —— 未测量/未刷新的数据会被当成结论发出去",
				c.file, c.fn)
		}
		for _, earlier := range []struct {
			needle string
			why    string
		}{
			{"Method != http.MethodGet", "方法检查"},
			{"IsRegularUser(r)", "角色鉴权"},
			{"cross-tenant access denied", "跨租户检查"},
		} {
			at := strings.Index(body, earlier.needle)
			if at < 0 {
				t.Fatalf("%s：%s 里找不到%s，抽取可能取错了块", c.file, c.fn, earlier.why)
			}
			if gateAt < at {
				t.Fatalf("%s：%s 的门在%s之前（门 %d，%s %d）—— 拒绝路径也会去查一次库",
					c.file, c.fn, earlier.why, gateAt, earlier.why, at)
			}
		}
		qAt := strings.Index(body, "FROM "+c.view)
		if qAt < 0 {
			t.Fatalf("%s：%s 里找不到查询 %s，抽取可能取错了块", c.file, c.fn, c.view)
		}
		if gateAt > qAt {
			t.Fatalf("%s：%s 的门在查询之后（门 %d，查询 %d）—— 过期数据已经付过一遍查询代价",
				c.file, c.fn, gateAt, qAt)
		}
	}
}

// extractFuncBody 从源码里切出 `func (h *Handler) <name>(` 的整个函数体。
// 以行首 `}` 收尾（gofmt 保证），不靠大括号配平猜边界。
func extractFuncBody(src, name string) string {
	marker := "func (h *Handler) " + name + "("
	i := strings.Index(src, marker)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		return ""
	}
	return rest[:end]
}
