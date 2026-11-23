package dashboardapi

// calendar_universe_materialization_test.go —— 「calendar 窗口慢路径」的行为契约
// （2026-11-24 统一轮 P0 修复）。
//
// # 这道门守什么
//
// 生产实测（211，2026-11-24）：`/api/admin/dashboard/session-overview` 带显式
// 日历窗口（start_date/end_date）时 **30s 超时 ⇒ 500**（错误段名 "top clients"），
// 而同天数的 rolling 路径（只带 days）**743ms 正常**。
//
// 根因不是数据，是**窗口谓词形态**改变了优化器的行数估算：
//
//	· rolling：`first_request_at >= NOW() - INTERVAL '1 days'`
//	  ⇒ NOW() 在计划期求值 ⇒ 估算落在直方图分布内（估 1860 行）⇒ 计划选
//	    **Merge Join**（两个大侧各自排序后归并）⇒ 快；
//	· calendar：`first_request_at >= $1 AND first_request_at < $2`
//	  ⇒ **常量区间**落在直方图分布**之外** ⇒ 估成 **1 行** ⇒ 计划翻成
//	    **Nested Loop**，把 27 万行的 `sessions DISTINCT ON` 子查询当**内层
//	    逐行重算** ⇒ 单腿 >40s。
//
// 修法：把 sessions 宇宙子查询**物化**成 `MATERIALIZED` CTE（只算一次 ⇒
// 不再是逐行重算的内层）⇒ 计划与 rolling 路径同构 ⇒ 实测 851ms。
//
// ⇒ 本门钉三条不变量：
//
//	① **days 路径逐字不变**（现役口径零变化）：rolling 模式下不产 CTE，
//	   连接子句与改动前**逐字相同**（dashboard_window.go 的不变量①）。
//	② **calendar 路径必须物化**：否则那条腿会退回 Nested Loop 逐行重算 ⇒
//	   端点 500（本门在「去掉物化」时必须红 ⇒ 判据有牙）。
//	③ **物化不改变行集**：CTE 的内容与原 DISTINCT ON 子查询**逐字等价**
//	   （同样的列、同样的 ORDER BY）⇒ 连接键与 WHERE 合取项都不动。

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/analytics"
)

// calendarWindow 造一个 calendar 模式的窗口（显式日历日区间）。
func calendarWindow(t *testing.T, includeProbes bool) sessionWindow {
	t.Helper()
	withFixedTZ(t)
	q := "/api/admin/dashboard/session-overview?days=1&start_date=2026-11-24&end_date=2026-11-24"
	if includeProbes {
		q += "&include_probes=1"
	}
	r := httptest.NewRequest("GET", q, nil)
	win, err := parseSessionWindow(r, QueryParams{Days: 1})
	if err != nil {
		t.Fatalf("parseSessionWindow 报错: %v", err)
	}
	return win
}

// rollingWindow 造一个 rolling 模式的窗口（只带 days ⇒ 现役口径）。
func rollingWindow(t *testing.T) sessionWindow {
	t.Helper()
	withFixedTZ(t)
	r := httptest.NewRequest("GET", "/api/admin/dashboard/session-overview?days=7", nil)
	win, err := parseSessionWindow(r, QueryParams{Days: 7})
	if err != nil {
		t.Fatalf("parseSessionWindow 报错: %v", err)
	}
	return win
}

// TestUniverseJoinRollingPathIsByteIdentical：rolling 路径**逐字不变**。
//
// ★ 这是「现役调用方零影响」的唯一保证：没有显式区间时，连接子句必须与
// 改动前逐字相同，且**不产 CTE**（CTE 只对 calendar 生效）。
func TestUniverseJoinRollingPathIsByteIdentical(t *testing.T) {
	win := rollingWindow(t)
	if win.calendar {
		t.Fatal("只带 days 却落进 calendar 模式 ⇒ 测试前提不成立")
	}
	cte, join := universeJoinFor(win)
	if cte != "" {
		t.Errorf("rolling 路径产出了 CTE 前缀（%.80q）⇒ 现役 SQL 形状被改", cte)
	}
	const want = "JOIN (\n  SELECT DISTINCT ON (session_id, tenant_id)\n    session_id, tenant_id, is_probe, owner_user, client_type, title\n  FROM sessions\n  ORDER BY session_id, tenant_id, partition_date DESC\n) s ON s.session_id = ss.session_key AND s.tenant_id = ss.tenant_id\nLEFT JOIN session_dim sd ON sd.session_key = ss.session_key AND sd.tenant_id = ss.tenant_id"
	if join != want {
		t.Errorf("rolling 路径的连接子句被改了：\n got: %q\nwant: %q", join, want)
	}
	if strings.Contains(join, "MATERIALIZED") {
		t.Error("rolling 路径的连接子句里出现了 MATERIALIZED ⇒ 现役路径被改")
	}
}

// TestUniverseJoinCalendarPathMaterializes：calendar 路径**必须物化**。
//
// ★ 判据有牙：把 universeJoinFor 的物化分支去掉 ⇒ 本测试立刻红
//   （这正是 2026-11-24 生产 500 的那条路径）。
func TestUniverseJoinCalendarPathMaterializes(t *testing.T) {
	win := calendarWindow(t, false)
	if !win.calendar {
		t.Fatal("传了 start_date/end_date 却没落进 calendar 模式 ⇒ 测试前提不成立")
	}
	cte, join := universeJoinFor(win)
	if !strings.Contains(cte, "AS MATERIALIZED") {
		t.Errorf("calendar 路径没有物化宇宙（cte 前缀 = %.120q）⇒ 计划会退回 Nested Loop 逐行重算 ⇒ 端点 500（2026-11-24 生产实测）", cte)
	}
	if !strings.Contains(cte, "WITH universe AS MATERIALIZED") {
		t.Errorf("calendar 路径的 CTE 前缀形态不对：%.120q", cte)
	}
	// 连接子句必须改为引用物化 CTE，且**连接键与原形态逐字相同**。
	if !strings.Contains(join, "JOIN universe s ON s.session_id = ss.session_key AND s.tenant_id = ss.tenant_id") {
		t.Errorf("物化形态的连接子句缺少与原形态相同的连接键：%q", join)
	}
	if !strings.Contains(join, "LEFT JOIN session_dim sd ON sd.session_key = ss.session_key AND sd.tenant_id = ss.tenant_id") {
		t.Errorf("物化形态的连接子句缺少 session_dim 左连接：%q", join)
	}
	if strings.Contains(join, "SELECT DISTINCT ON") {
		t.Errorf("物化形态的连接子句里还留着内联子查询（会退回逐行重算）：%q", join)
	}
}

// TestUniverseJoinIncludeProbesKeepsOldShape：includeProbes 时**不套宇宙** ⇒ 不产 CTE。
func TestUniverseJoinIncludeProbesKeepsOldShape(t *testing.T) {
	win := calendarWindow(t, true)
	cte, join := universeJoinFor(win)
	if cte != "" {
		t.Errorf("includeProbes 模式下产出了 CTE（%.80q）⇒ 查询形状被改", cte)
	}
	if join != "" {
		t.Errorf("includeProbes 模式下产出了连接子句（%q）⇒ 查询形状被改", join)
	}
}

// TestMaterializedUniverseIsRowSetEquivalent：物化**不改变行集**。
//
// ★ 这是「只改执行形态、不改结果」的机器可核证据：把物化 CTE 的内容与
//   原 DISTINCT ON 子查询的内容**归一化后逐字比对**。
//   ⇒ 若有人改了 CTE 的列清单 / ORDER BY / 表名，本测试立刻红
//   （那会改变宇宙的行集 ⇒ 面板口径漂移）。
func TestMaterializedUniverseIsRowSetEquivalent(t *testing.T) {
	orig := analytics.SessionUniverseJoin("ss", "s", "sd")
	// 原形态：JOIN (\n<body>\n) s ON ...
	bodyRe := regexp.MustCompile(`(?s)JOIN \(\n(.*?)\n\) s ON `)
	m := bodyRe.FindStringSubmatch(orig)
	if m == nil {
		t.Fatalf("原形态的宇宙子查询没解析出来：%q", orig)
	}
	origBody := normalizeSQL(m[1])

	cteRe := regexp.MustCompile(`(?s)WITH universe AS MATERIALIZED \(\n(.*?)\n\)`)
	m2 := cteRe.FindStringSubmatch(universeCTEMaterialized)
	if m2 == nil {
		t.Fatalf("物化 CTE 的内容没解析出来：%q", universeCTEMaterialized)
	}
	cteBody := normalizeSQL(m2[1])

	if cteBody != origBody {
		t.Errorf("物化 CTE 的内容与原 DISTINCT ON 子查询**不等价** ⇒ 宇宙行集会漂移：\n orig: %q\n  cte: %q", origBody, cteBody)
	}
}

// normalizeSQL 归一化 SQL 片段（压空白、去行尾空格）后用于逐字比对。
func normalizeSQL(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// TestAllOverviewLegsUseUniverseJoinFor：六条腿**全部**走 universeJoinFor。
//
// ★ 为什么需要这道结构钉：本次修法改的是**六处**buildQuery 里的连接片段
//   （聚合 / 上一周期成本 / 模型榜 / 客户端榜 / 任务榜 / 趋势）。
//   ⇒ 漏改任何一处 ⇒ 那条腿在 calendar 模式下仍会 40s 超时 ⇒ 端点照样 500。
//   ⇒ 源码级扫描把「六处都改了」这件事钉住（变异：把任意一处改回
//   universeJoin(win.includeProbes) ⇒ 本测试立刻红）。
func TestAllOverviewLegsUseUniverseJoinFor(t *testing.T) {
	path := filepath.Join("session_overview.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", path, err)
	}
	text := string(src)

	if strings.Contains(text, "universeJoin(win.includeProbes)") {
		t.Errorf("源码里还有 %q ⇒ 某条腿漏改（calendar 模式下那条腿仍会 40s 超时 ⇒ 端点 500）", "universeJoin(win.includeProbes)")
	}
	if got := strings.Count(text, "universeJoinFor(win)"); got != 6 {
		t.Errorf("universeJoinFor(win) 出现 %d 次，期望 6（聚合/上一周期成本/模型榜/客户端榜/任务榜/趋势 六条腿都要走）", got)
	}
	// 每条腿都必须把 CTE 前缀拼到语句最前（否则 CTE 不在语句头部 ⇒ SQL 语法错）。
	// ★ 两种写法都算：`cte + `（普通拼接）与 `fmt.Sprintf(cte+`（带格式串）⇒ 用正则数。
	if got := len(regexp.MustCompile(`cte\s*\+`).FindAllString(text, -1)); got != 6 {
		t.Errorf("CTE 前缀拼接只出现 %d 次，期望 6（每条腿都要把 CTE 拼到语句最前）", got)
	}
}
