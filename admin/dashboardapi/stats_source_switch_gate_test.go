package dashboardapi

// stats_source_switch_gate_test.go —— 「统计 API 换请求级真源」这一轮的回归门
// （2026-11-25 真源统一轮）。
//
// # 这道门守什么
//
// 2026-11-18 会话统计审计的 F2 认定：/dashboard 的 errors / performance /
// module-stats 三张卡读 `session_module_executions_hot`，而该表的唯一写方
// `moduleexec.Executor` 在 cmd/gateway **从未装配**（生产代码里
// `moduleexec.NewExecutor` 零实例化）⇒ 三张卡恒为 0 —— 把「没有数据」
// 读成了「没有错误 / 没有慢请求」。
//
// 本轮把 errors / performance 切到**请求级日志真源**
// `request_logs_with_current_month`（看板英雄区 / 趋势 / 饼图正在用的那张
// canonical 视图），并给 module-stats 加上**显式不可用声明**。本门把这几件事
// 钉死：
//
//	① errors / performance 的 SQL 必须读 request_logs_with_current_month，
//	   **不得**再读 session_module_executions_hot（那是无写者的表），
//	   **也不得**读 usage_facts（owner 2026-11-24 裁决：事实族缺口只登记 P0
//	   不补数据，看板不再读 usage_facts —— usage_facts 于 2026-11-10 被一次批量
//	   DELETE 抹掉 352,395 行，属「已知少算」的源）；
//	② 探测口径必须用 bg.ProbeTrafficExclusionPredicateView 分面，且
//	   include_probes=1 时该谓词必须消失（口径与看板同尺）；
//	③ module-stats 必须带 data_available=false + 原因 ⇒ 前端能显示
//	   「未接入」而不是把 0 当数据（缺席比假绿诚实）；
//	④ session-active 必须消费显式日历窗（此前硬编码「最近 1 小时」，
//	   把前端选的自定义区间静默丢弃）；
//	⑤ errors / performance 的响应必须披露真源与探测口径（前端徽标跟随
//	   响应值，不跟随本地开关）。
//
// # 为什么用「读源码文本」而不是真库
//
// 这些端点的真值依赖 211 生产库的既有数据；本门要在任何环境都能跑。
// 判据取自**被断言的那段源码本身**（SQL 文本 + 响应结构体），
// 属本仓既有的「文本契约门」形态（如 session_overview_probe_scope_test.go）。
// 真库对拍由部署后的 211 验收承担。

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
)

// readDashboardAPISource 读取 admin/dashboardapi 下的一个源文件。
func readDashboardAPISource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读不到 %s：%v", name, err)
	}
	return string(b)
}

// TestErrorsAndPerformanceReadRequestLevelFacts —— errors/performance 必须读
// 请求级日志真源，且不得回退到无生产写者的 session_module_executions_hot。
//
// ★ 顺带钉住「不得读 usage_facts」：owner 2026-11-24 裁决「看板不再读
// usage_facts」（该事实族被批量 DELETE 抹掉 352,395 行，属已知少算的源）。
func TestErrorsAndPerformanceReadRequestLevelFacts(t *testing.T) {
	for _, name := range []string{"errors.go", "performance.go"} {
		src := readDashboardAPISource(t, name)
		if !strings.Contains(src, "FROM request_logs_with_current_month") {
			t.Errorf("%s：没有读 request_logs_with_current_month（请求级日志真源）——三张卡会退回无写者的表、恒为 0", name)
		}
		if strings.Contains(src, "FROM session_module_executions_hot") {
			t.Errorf("%s：仍在读 session_module_executions_hot（唯一写方生产从未装配 ⇒ 恒空）", name)
		}
		if strings.Contains(src, "FROM usage_facts") {
			t.Errorf("%s：不得读 usage_facts（owner 2026-11-24 裁决：事实族缺口只登记 P0 不补数据）", name)
		}
	}
}

// TestErrorsAndPerformanceUseSharedFactsBuilder —— errors / performance 必须
// 走**共享的谓词构造器** requestLogFactsWhere，不能各写各的谓词。
//
// ★ 为什么单列一条：谓词一旦各写各的，窗口/租户/终态/探测口径就会在两张卡
//
//	之间悄悄漂移（典型症状：errors 排除了探测、performance 没排 ⇒ 同一页两
//	个数字对不上）。共享构造器是「同一宇宙」的结构性保证。
func TestErrorsAndPerformanceUseSharedFactsBuilder(t *testing.T) {
	for _, name := range []string{"errors.go", "performance.go"} {
		src := readDashboardAPISource(t, name)
		if !strings.Contains(src, "requestLogFactsWhere(") {
			t.Errorf("%s：没有调用共享构造器 requestLogFactsWhere( —— 两张卡的谓词会各自漂移", name)
		}
	}
}

// TestRequestLogFactsWhereProbeScope —— 探测口径必须用
// bg.ProbeTrafficExclusionPredicateView 分面，且 include_probes=1 时该谓词
// 必须消失（与看板同尺）。
//
// ★ 判据用**渲染后的实际谓词**（同一个 var、同一个别名 r 渲染三次），不写死
//
//	整条长串 —— 谓词定义变了这条门会跟着走（而硬编码长串会在定义变更时
//	变成「测的是旧字符串」）。
func TestRequestLogFactsWhereProbeScope(t *testing.T) {
	probePredicate := fmt.Sprintf(bg.ProbeTrafficExclusionPredicateView, "r", "r", "r")

	excluded, _, _ := requestLogFactsWhere(sessionWindow{days: 7}, QueryParams{})
	if !strings.Contains(excluded, probePredicate) {
		t.Errorf("缺省口径缺少探测排除谓词，得到：%s", excluded)
	}
	// 稳定片段兜底：谓词渲染层若被改成多别名形态，这条仍能指出「探测口径没挂上」。
	if !strings.Contains(excluded, "probe_triggered") {
		t.Errorf("缺省口径缺少探测排除谓词的稳定片段（probe_triggered），得到：%s", excluded)
	}

	included, _, _ := requestLogFactsWhere(sessionWindow{days: 7, includeProbes: true}, QueryParams{})
	if strings.Contains(included, probePredicate) {
		t.Errorf("include_probes=1 时不得带探测排除谓词（那是全量口径），得到：%s", included)
	}
}

// TestModuleStatsDeclaresUnavailable —— module-stats 必须显式声明不可用
// （无生产写方），否则前端会把 0 读成「没有模块被执行」。
func TestModuleStatsDeclaresUnavailable(t *testing.T) {
	src := readDashboardAPISource(t, "module_stats.go")
	for _, frag := range []string{
		"DataAvailable",
		"DataUnavailableReason",
		"moduleStatsUnavailableReason",
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("module_stats.go 缺少 %q —— 端点没有声明「无生产写方」", frag)
		}
	}
	if moduleStatsUnavailableReason == "" {
		t.Error("moduleStatsUnavailableReason 不能为空（前端要据此显示「未接入」）")
	}
}

// TestSessionActiveHonoursCalendarWindow —— session-active 必须消费显式
// 日历窗（此前硬编码「最近 1 小时」，把用户选的区间静默丢弃）。
//
// ★ 这条断言是**行为门**而不是 grep 门：直接调用 activeWindowClause 看它
//
//	在两种窗口下分别给出什么谓词。
//	（第一版的 grep 版被变异验证当场抓穿：把 `if win.calendar` 改成
//	  `if false && win.calendar` —— 构建合法、行为已废、grep 门仍绿。
//	  仓内纪律：「门要变异验证才算门」，字符串门不算。）
func TestSessionActiveHonoursCalendarWindow(t *testing.T) {
	// 滚动窗口：逐字保留既有的「最近 1 小时」语义，且**不追加参数**
	// （不变量①：days 路径逐字不变 ⇒ 现役结果零变化）。
	rollingArgs := []interface{}{}
	rollingIdx := 1
	rolling := activeWindowClause(sessionWindow{days: 7}, &rollingArgs, &rollingIdx)
	if !strings.Contains(rolling, "NOW() - INTERVAL '1 hour'") {
		t.Errorf("滚动窗口应保留「最近 1 小时」字面量，得到：%s", rolling)
	}
	if len(rollingArgs) != 0 {
		t.Errorf("滚动窗口不应追加参数（否则占位符与实参错位），得到 %d 个", len(rollingArgs))
	}

	// 日历窗口：谓词必须参数化、落在 last_request_at 上，并带上 UTC 边界。
	calArgs := []interface{}{}
	calIdx := 1
	cal := activeWindowClause(sessionWindow{
		calendar: true,
		startUTC: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC),
		endUTC:   time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC),
	}, &calArgs, &calIdx)
	if !strings.Contains(cal, "ss.last_request_at >= $1 AND ss.last_request_at < $2") {
		t.Errorf("日历窗口应给出参数化的 last_request_at 区间谓词，得到：%s", cal)
	}
	if len(calArgs) != 2 {
		t.Errorf("日历窗口应追加 2 个边界参数，得到 %d 个", len(calArgs))
	}
}

// TestStatsResponsesDiscloseCaliber —— errors/performance 的响应必须披露
// 真源与探测口径（前端徽标跟随响应值，不跟随本地开关）。
func TestStatsResponsesDiscloseCaliber(t *testing.T) {
	for _, name := range []string{"errors.go", "performance.go"} {
		src := readDashboardAPISource(t, name)
		for _, frag := range []string{"data_source", "probe_scope"} {
			if !strings.Contains(src, frag) {
				t.Errorf("%s 的响应缺少 %q 披露字段（前端无从知道这个面是哪种口径）", name, frag)
			}
		}
	}
}
