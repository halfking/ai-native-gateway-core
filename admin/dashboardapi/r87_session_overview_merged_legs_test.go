package dashboardapi

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// R87（2026-10-27 巡检轮）门禁：session-overview 的「四条标量腿 + 两条趋势腿」合并。
//
// 为什么要有这个门：
//  1. **口径漂移风险**：合并后 23 个聚合表达式挤进一条 SELECT，任何一个被误删/误改
//     ⇒ 仪表盘上的某个指标静默变成 0 或错值，而且**不会报错**（这正是本轮要防的）。
//     ⇒ 门必须逐条钉住每个聚合表达式（而不是只数语句数）。
//  2. **扫描点回退**：合并的收益全在「同一张 session_summaries 被扫的次数变少」，
//     ⇒ 门钉住扫描点上限，防止有人把腿又拆回去（或者再加一条腿）。
//  3. **旧腿复活**：旧的分腿函数必须消失，否则同一口径会有两份实现 ⇒ 漂移温床。
//
// 判据读的是**源码文本**（不是运行时行为），所以要剥注释再比对 —— 否则注释里
// 出现的关键词会让门恒绿（本仓已有的通用坑：断言前先剥注释）。

// r87StripComments 去掉 Go 行注释与块注释，避免注释文本影响判据。
func r87StripComments(src string) string {
	var b strings.Builder
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		if inBlock {
			if idx := strings.Index(line, "*/"); idx >= 0 {
				line = line[idx+2:]
				inBlock = false
			} else {
				continue
			}
		}
		if idx := strings.Index(line, "/*"); idx >= 0 {
			if end := strings.Index(line[idx:], "*/"); end >= 0 {
				line = line[:idx] + line[idx+end+2:]
			} else {
				line = line[:idx]
				inBlock = true
			}
		}
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// r87FuncBody 截出某个方法体的源码（到下一个顶层 func 声明为止）。
func r87FuncBody(t *testing.T, src, signature string) string {
	t.Helper()
	start := strings.Index(src, signature)
	if start < 0 {
		t.Fatalf("源码里找不到方法 %q —— 门禁自身失效（不是被测坏了）", signature)
	}
	rest := src[start+len(signature):]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		return rest[:end]
	}
	return rest
}

func r87ReadSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("session_overview.go")
	if err != nil {
		t.Fatalf("读 session_overview.go 失败：%v", err)
	}
	return r87StripComments(string(data))
}

// TestR87_OldPerLegFunctionsAreGone：旧的分腿函数不得复活。
//
// ⚠ 判据必须用**带左括号的完整签名**而不是裸名字：`querySessionTrend` 是
// `querySessionTrends` 的**子串** ⇒ 用裸名字断言会把合并后的新方法误判成旧函数
// （本轮第一次跑门就踩到了：门红 ⇒ 门坏了，不是制品坏了 ⇒ 修门）。
func TestR87_OldPerLegFunctionsAreGone(t *testing.T) {
	src := r87ReadSource(t)
	for _, dead := range []string{
		"func (h *SessionOverviewHandler) queryTotalStats(",
		"func (h *SessionOverviewHandler) queryHealthDistribution(",
		"func (h *SessionOverviewHandler) queryComplianceStats(",
		"func (h *SessionOverviewHandler) queryCostStats(",
		"func (h *SessionOverviewHandler) queryCostTrend(",
		"func (h *SessionOverviewHandler) querySessionTrend(",
	} {
		if strings.Contains(src, dead) {
			t.Errorf("旧的单腿函数 %q 仍存在 —— 合并后同一口径会有两份实现（漂移温床），必须删掉", dead)
		}
	}
	// 阳性对照：合并后的两个方法必须在
	for _, want := range []string{
		"func (h *SessionOverviewHandler) querySessionSummaryAggregates(",
		"func (h *SessionOverviewHandler) querySessionTrends(",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("缺少合并后的方法 %q", want)
		}
	}
}

// TestR87_MergedAggregatesKeepEveryMetric：逐条钉住合并后标量腿的每个聚合表达式。
//
// 这 23 个表达式逐字来自合并前的四条腿（总会话/健康/合规/成本），
// 任何一个缺失 ⇒ 对应指标静默变 0 ⇒ 门必须红。
func TestR87_MergedAggregatesKeepEveryMetric(t *testing.T) {
	src := r87ReadSource(t)
	body := r87FuncBody(t, src, "func (h *SessionOverviewHandler) querySessionSummaryAggregates(")

	needles := []string{
		// 窗口与租户口径（原四条腿逐字相同）
		"first_request_at >= NOW() - INTERVAL '%d days'",
		"tenant_id = $%d",
		"FROM session_summaries",
		// 腿 1：总会话数
		"COUNT(*) as total",
		"COUNT(*) FILTER (WHERE last_request_at >= NOW() - INTERVAL '24 hours') as active",
		"COUNT(*) FILTER (WHERE first_request_at >= NOW() - INTERVAL '24 hours') as new_24h",
		"COUNT(*) FILTER (WHERE last_request_at < NOW() - INTERVAL '24 hours' AND first_request_at < NOW() - INTERVAL '24 hours') as closed_24h",
		// 腿 2：健康分布
		"COUNT(*) FILTER (WHERE health_grade = 'A') as health_a",
		"COUNT(*) FILTER (WHERE health_grade = 'B') as health_b",
		"COUNT(*) FILTER (WHERE health_grade = 'C') as health_c",
		"COUNT(*) FILTER (WHERE health_grade = 'D') as health_d",
		"COUNT(*) FILTER (WHERE health_grade = 'F') as health_f",
		"COALESCE(AVG(health_score), 0) as avg_score",
		// 腿 3：合规统计
		"COUNT(*) FILTER (WHERE compliance_status = 'compliant' OR compliance_status IS NULL) as compliant",
		"COUNT(*) FILTER (WHERE compliance_status = 'warning') as warning",
		"COUNT(*) FILTER (WHERE compliance_status = 'violation') as violation",
		"COUNT(*) FILTER (WHERE prompt_injection_detected = true) as prompt_injection",
		"COUNT(*) FILTER (WHERE pii_detected = true) as pii",
		"COUNT(*) FILTER (WHERE toxic_output_detected = true) as toxic",
		// 腿 4：成本统计
		"COALESCE(SUM(total_cost_usd), 0) as total_cost",
		"COALESCE(AVG(total_cost_usd), 0) as avg_cost",
		"COALESCE(MAX(total_cost_usd), 0) as max_cost",
		"COALESCE(SUM(input_cost_usd), 0) as input_cost",
		"COALESCE(SUM(output_cost_usd), 0) as output_cost",
		"COALESCE(SUM(request_count), 0) as total_requests",
		"COALESCE(AVG(avg_latency_ms), 0) as avg_latency",
	}
	for _, needle := range needles {
		if !strings.Contains(body, needle) {
			t.Errorf("合并后的标量腿缺少聚合表达式 %q —— 对应指标会静默失真", needle)
		}
	}

	// SELECT 列的**别名集合**必须逐字等于期望值（不多不少不改名）——
	// 只断言「存在」会漏掉「多一列」这种追加型漂移（多一列 ⇒ Scan 数量不匹配 ⇒ 运行时报错；
	// 少一列同样错）。这里用正则抽出别名做集合比对。
	got := r87SQLAliases(body)
	want := []string{
		"total", "active", "new_24h", "closed_24h",
		"health_a", "health_b", "health_c", "health_d", "health_f", "avg_score",
		"compliant", "warning", "violation", "prompt_injection", "pii", "toxic",
		"total_cost", "avg_cost", "max_cost", "input_cost", "output_cost",
		"total_requests", "avg_latency",
	}
	if len(got) != len(want) {
		t.Errorf("合并后的标量腿别名数 = %d（%v），期望 %d（%v）", len(got), got, len(want), want)
		return
	}
	for _, name := range want {
		found := false
		for _, g := range got {
			if g == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("合并后的标量腿缺少别名 %q（实际 %v）", name, got)
		}
	}
}

// r87SQLAliases 抽出函数体里 SELECT 的别名序列（形如 "xxx as yyy"）。
func r87SQLAliases(body string) []string {
	re := regexp.MustCompile(`(?i)\bas\s+([a-z_][a-z0-9_]*)`)
	matches := re.FindAllStringSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// TestR87_MergedTrendsKeepEveryMetric：逐条钉住合并后趋势腿的每个聚合表达式。
func TestR87_MergedTrendsKeepEveryMetric(t *testing.T) {
	src := r87ReadSource(t)
	body := r87FuncBody(t, src, "func (h *SessionOverviewHandler) querySessionTrends(")

	needles := []string{
		"DATE(first_request_at) as date",
		"COALESCE(SUM(total_cost_usd), 0) as cost",
		"COUNT(*) as sessions",
		"COALESCE(SUM(request_count), 0) as requests",
		"COUNT(*) FILTER (WHERE first_request_at >= NOW() - INTERVAL '%d days') as new_sessions",
		"COUNT(*) FILTER (WHERE last_request_at >= NOW() - INTERVAL '24 hours') as active_count",
		"COUNT(*) FILTER (WHERE last_request_at < NOW() - INTERVAL '24 hours' AND first_request_at < NOW() - INTERVAL '24 hours') as closed_count",
		"FROM session_summaries",
		"GROUP BY DATE(first_request_at)",
		"ORDER BY date",
		"first_request_at >= NOW() - INTERVAL '%d days'",
		"tenant_id = $%d",
	}
	for _, needle := range needles {
		if !strings.Contains(body, needle) {
			t.Errorf("合并后的趋势腿缺少 %q —— 趋势点会静默缺列/缺序", needle)
		}
	}
}

// TestR87_SessionSummariesScanPointsCapped：钉住 session_summaries 的扫描点上限。
//
// 合并前 11 处引用 / 10 条语句；合并后 7 处引用 / 6 条语句。
// 收益全在「同一张大表被扫的次数变少」⇒ 这条门是本轮改动的收益护栏。
func TestR87_SessionSummariesScanPointsCapped(t *testing.T) {
	src := r87ReadSource(t)
	const want = 7
	got := strings.Count(src, "FROM session_summaries")
	if got != want {
		t.Errorf("session_summaries 扫描点 = %d，期望 %d（合并前是 11）—— 腿被拆回去了，或新增了扫描腿", got, want)
	}
}

// TestR87_OverviewRunsSixConcurrentLegs：编排段必须是 6 段并行（合并后）。
func TestR87_OverviewRunsSixConcurrentLegs(t *testing.T) {
	src := r87ReadSource(t)
	body := r87FuncBody(t, src, "func (h *SessionOverviewHandler) HandleSessionOverview(")
	got := strings.Count(body, "g.Go(func() error {")
	if got != 6 {
		t.Errorf("HandleSessionOverview 的并行段数 = %d，期望 6（合并后 9→6）", got)
	}
	// 每段失败都要带段名（错误语义保持）
	if !strings.Contains(body, "dashboardStatsStageError{") {
		t.Error("并行段失败时没有带段名的 dashboardStatsStageError —— 错误语义被削弱")
	}
}
