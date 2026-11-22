package dashboardapi

// session_overview_probe_scope_test.go —— 会话统计的**探测口径参数门**
// （2026-11-23 L7 修复轮补）。
//
// # 这道门守什么
//
// 「包含探测」开关（include_probes）在**后端**这一侧其实一直是通的：
// parseSessionWindow 解析它，universeJoin/universeWhere 据此决定用
// 「去重会话宇宙」还是「旧全量宇宙」。但 2026-11-23 生产实测发现页面上
// 勾选开关后数字纹丝不动 ⇒ 排查后确认断点全在**前端**：
//   ① SessionStatsPanel 的 includeProbes watch 只写 localStorage、**不重载**
//      ⇒ 参数进了 URL 但请求根本没重发；
//   ② 页头「刷新数据」只刷看板、**不刷会话统计面板**。
//
// ⇒ 后端这条链（解析 → 口径选择 → 宇宙片段）此前**零测试覆盖**：
// 哪天有人把 case 列表改窄、或把 universeJoin 的分支写反，前端修好了
// 也照样静默错。本门把它钉死。
//
// ★ 口径语义（两个都验，避免"修了开关反而改错数"）：
//   - include_probes 缺省/假 ⇒ **去重会话宇宙**（owner 非空 + 排除探测，
//     与 /admin/session-analytics 同宇宙）⇒ 排除探测口径；
//   - include_probes=1 ⇒ **旧全量宇宙**（不做 owner/探测过滤）⇒ 含探测口径。

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestParseSessionWindowIncludeProbes(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"", false},
		{"include_probes=1", true},
		{"include_probes=true", true},
		{"include_probes=on", true},
		{"include_probes=yes", true},
		{"include_probes=TRUE", true}, // 前端 buildSearchParams 传 boolean ⇒ "true"
		{"include_probes=1&days=7", true},
		{"include_probes=0", false},
		{"include_probes=false", false},
		{"include_probes=off", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/admin/dashboard/session-overview?"+c.query, nil)
		win, err := parseSessionWindow(r, QueryParams{Days: 7})
		if err != nil {
			t.Fatalf("query=%q: parseSessionWindow 报错: %v", c.query, err)
		}
		if win.includeProbes != c.want {
			t.Errorf("query=%q: includeProbes = %v，期望 %v", c.query, win.includeProbes, c.want)
		}
	}
}

func TestUniverseFragmentsFollowProbeScope(t *testing.T) {
	// 含探测 ⇒ 旧全量宇宙（两个片段都空 ⇒ 调用方保持旧 FROM 形状）。
	if got := universeJoin(true); got != "" {
		t.Errorf("universeJoin(includeProbes=true) 必须为空（旧全量宇宙），实际:\n%s", got)
	}
	if got := universeWhere(true); got != "" {
		t.Errorf("universeWhere(includeProbes=true) 必须为空（旧全量宇宙），实际:\n%s", got)
	}

	// 排除探测 ⇒ 去重会话宇宙（必须有 DISTINCT ON，否则会一对多放大）。
	join := universeJoin(false)
	if !strings.Contains(join, "DISTINCT ON (session_id, tenant_id)") {
		t.Errorf("universeJoin(false) 必须带 DISTINCT ON 去重（sessions 按天分区，直接 JOIN 会放大）:\n%s", join)
	}
	if !strings.Contains(join, "partition_date DESC") {
		t.Errorf("universeJoin(false) 必须按 partition_date DESC 取最新分区行:\n%s", join)
	}
	where := universeWhere(false)
	if !strings.Contains(where, "is_probe") || !strings.Contains(where, "session_analytics_is_probe") {
		t.Errorf("universeWhere(false) 必须含探测排除谓词（is_probe + 三维模式判定）:\n%s", where)
	}
}

// TestSessionOverviewHandlerReadsProbeScope —— 静态门：handler 必须真的走
// parseSessionWindow（否则参数解析了却没人用）。
func TestSessionOverviewHandlerReadsProbeScope(t *testing.T) {
	data, err := os.ReadFile("session_overview.go")
	if err != nil {
		t.Fatalf("读 session_overview.go 失败：%v", err)
	}
	src := string(data)
	if !strings.Contains(src, "parseSessionWindow(r, params)") {
		t.Error("session_overview.go 的 handler 必须调用 parseSessionWindow(r, params) —— " +
			"否则 include_probes 参数解析了却没人用，开关永远不生效")
	}
	// probe_scope 披露：excluded / included / degraded_included 必须在响应里可见
	// （口径不能静默切换）。
	if !strings.Contains(src, `probeScope := "excluded"`) ||
		!strings.Contains(src, `probeScope = "included"`) ||
		!strings.Contains(src, `probeScope = "degraded_included"`) {
		t.Error("session_overview.go 必须披露 probe_scope（excluded/included/degraded_included）—— " +
			"口径切换要可见，不能静默")
	}
}
