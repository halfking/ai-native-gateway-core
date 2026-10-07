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

// TestSessionMvFreshnessBudget_ExceedsInterval：预算必须大于刷新间隔，否则
// 门会判定「刚刷完就已经陈旧」。session 族间隔是 60min（bg.SessionViewsInterval），
// 这里锁住「预算 > 一小时」这个不等式本身，不去跨包引常量（会造依赖环）。
func TestSessionMvFreshnessBudget_ExceedsSessionRefreshInterval(t *testing.T) {
	if sessionMvFreshnessBudget <= 60*time.Minute {
		t.Fatalf("陈旧度预算 %s 必须大于 60min 的刷新间隔，否则门会把刚刷新的数据判为陈旧",
			sessionMvFreshnessBudget)
	}
	if sessionMvFreshnessBudget != 90*time.Minute {
		t.Fatalf("预算应为 90min（60min 间隔 + 一轮漏刷余量），实际 %s", sessionMvFreshnessBudget)
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
