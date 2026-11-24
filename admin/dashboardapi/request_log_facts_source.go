// Package dashboardapi - request_log_facts_source.go
//
// request_log_facts_source.go —— 「请求级日志真源」的读面口径
// （2026-11-25 统计真源统一轮）。
//
// # 背景：errors / performance 为什么必须换源
//
// 2026-11-18 会话统计审计的 F2 认定：/api/admin/dashboard 的 errors /
// performance / module-stats 三张卡读 `session_module_executions_hot`，而该表的
// **唯一写方** `moduleexec.Executor` 在 cmd/gateway **从未装配**（全仓只有
// domains/moduleexec/executor.go:18 的注释里出现过 `moduleexec.NewExecutor`）
// ⇒ 三张卡恒为 0 —— 把「没有数据」读成了「没有错误 / 没有慢请求」（假绿）。
//
// # 为什么不是 usage_facts
//
// 2026-11-24 21:43 的提交 ee6bc4e98（已在 main 上）登记了事实族的 P0 缺口：
//
//	usage_facts 于 2026-11-10 13:36:57~13:37:15 被一次批量 DELETE 抹掉
//	**352,395 行**（2026-10-12~11-10）；autovacuum 已剪除死元组 ⇒ 物理恢复
//	路径关闭；inbox 只留 7 天 ⇒ 无重放路径。
//
// owner 裁决：① **看板整页切请求级日志** ② 事实族缺口只登记 P0 不补数据
// ⇒「看板不再读 usage_facts」。
//
// ★★ 所以本文件把 errors / performance 切到**请求级日志真源**：
//
//	request_logs_with_current_month —— 看板英雄区 / 趋势 / 饼图正在用的那张
//	canonical 视图（session_turns_hot ∪ session_turns，118 列同名同序，见
//	sql/migrations/startup/890_drop_request_logs_v1_family.sql）。
//
//	**不读 usage_facts**（那是「已知少算 2 倍」的源，再挂到两张卡上就是二次污染）
//	**不读 session_module_executions_hot**（无生产写者 ⇒ 恒空）。
//
// # 口径三件套（与看板逐字同尺）
//
//	① 窗口：sessionWindow（显式日历窗优先，否则滚动 N 天）
//	② 租户：params.TenantID
//	③ 终态集 + 探测口径：见 requestLogFactsWhere
//
// ★ 为什么窗口不直接复用 admin 包的 boardLogsWhereProbeScope：
//
//	那是 package admin 的私有函数，且它吃的是 boardTimeRange（看板专用窗口类型）
//	⇒ 跨包「复用」会把两种窗口语义混成一种。**口径要同尺，窗口各自按本族的
//	规则解析**（仪表盘族用 sessionWindow，显式日历窗优先）。
//
// # 不变量（改本文件前先读）
//
//	① 窗口的 days 路径**逐字**沿用既有字面量 `r.ts >= NOW() - INTERVAL 'N days'`
//	   （不占参数位）⇒ 现役查询的结果零变化。
//	② 探测谓词与看板**逐字同源**（bg.ProbeTrafficExclusionPredicateView，同一个
//	   别名 r、同样的三个占位）⇒ 本族与看板的「探测口径」不可能漂移。
//	③ 终态集与看板**逐字同款**：r.request_status IN ('success','failure','rate_limited')
//	   —— 只数终态行，行未终结的请求不进分母（否则错误率会被「还在飞」的行稀释）。
package dashboardapi

import (
	"fmt"

	"github.com/kaixuan/llm-gateway-go/admin/dashboarddegrade"
	"github.com/kaixuan/llm-gateway-go/bg"
)

// requestLogFactsDataSource 是本族端点声明真源时用的常量（前端据此显示口径标记）。
//
// ★ 取值 "request_logs"：指「请求级日志」这一族真源（canonical 视图
// request_logs_with_current_month）；不是表名本身，也不是已退役的 usage_facts
// ——前端徽标要区分的是**事实族**，不是某一张物理表。
const requestLogFactsDataSource = "request_logs"

// probeScopeValue 返回当前请求的探测口径披露值。
//
// ★ 取值与 dashboardapi/session_overview.go 的 probe_scope 同一词表
//
//	（excluded / included / degraded_included），前端徽标跟随响应值。
//
// ★ degraded_included 由**调用方**在宇宙降级时另行置位（session_overview.go
//
//	在依赖缺失重试后写 "degraded_included"）；本函数只负责「请求显式要了没有」
//	这一位 ⇒ 本族没有降级重试链时不会自己产出 degraded_included。
func probeScopeValue(win sessionWindow) string {
	if win.includeProbes {
		return "included"
	}
	return "excluded"
}

// requestLogFactsWhere 返回读请求级日志真源的 WHERE 片段。
//
// # 谓词构成（按拼接顺序，★ 参数编号顺序必须与 append 顺序一致）
//
//	① 窗口：win.clause("r.ts", …)
//	   - rolling：`r.ts >= NOW() - INTERVAL 'N days'`（字面量，**不占参数位**）
//	   - calendar：`r.ts >= $1 AND r.ts < $2`（占 $1/$2，实参 startUTC/endUTC）
//	② 租户：params.TenantID != "" 时 `r.tenant_id = $N`（N 接在窗口之后）
//	③ 终态集：`r.request_status IN ('success','failure','rate_limited')`（不占参数）
//	④ 探测口径：!win.includeProbes 时追加 bg.ProbeTrafficExclusionPredicateView
//	   （不占参数）
//
// # 返回值
//
//	(where, args, nextArgIdx)：nextArgIdx 是**下一个可用占位符编号**，调用方还要
//	追加参数时（如日桶表达式要追加时区名）必须接着它排，否则占位符编号与实参
//	错位（42P08 / 参数对不上）。
//
// ★ 为什么不用 appendExecutionScope：
//
//	那条 helper 会额外加一条 owner 维度的 EXISTS
//	（session_summaries s WHERE s.session_key = gw_session_id AND s.owner_user = $N）。
//	① 看板英雄区（同一宇宙的参照实现 boardLogsWhereProbeScope）**没有**这条谓词
//	   ⇒ 本族若带上它，本族与看板的「同一个 7 天窗口」就不是同一批行。
//	② 它把「这行属于谁」与「这行是否存在」耦合：请求级真源里没有 session 映射的
//	   行（视图的 gw_session_id 对 sys:% 会话恒为 NULL）会被 EXISTS 整批滤掉
//	   ⇒ 又是另一种「假绿」（少算而不是报错）。
//	⇒ 本族只按 窗口 + 租户 + 终态 + 探测口径 过滤（租户隔离仍由 params.TenantID
//	  保证，见 normalizeDashboardScope）。
func requestLogFactsWhere(win sessionWindow, params QueryParams) (string, []interface{}, int) {
	where := []string{}
	args := []interface{}{}
	argIdx := 1

	where = append(where, win.clause("r.ts", &args, &argIdx))
	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("r.tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	// 终态集：与看板逐字同款（boardLogsWhereTerminal）。
	where = append(where, "r.request_status IN ('success','failure','rate_limited')")
	if !win.includeProbes {
		// ★ 三个占位符全部填同一个别名 r —— 该谓词是 var（含 3 个 %s），
		// 少填一个就会把 %!s(MISSING) 渲染进 SQL（远端才炸）。
		where = append(where, fmt.Sprintf(bg.ProbeTrafficExclusionPredicateView, "r", "r", "r"))
	}
	return "WHERE " + joinStrings(where, " AND "), args, argIdx
}

// requestLogFactsMissingRelation 判断错误是否来自「真源视图不存在」。
//
// 请求级日志真源在已投递迁移的环境里必然存在；未迁移的旧库会报 42P01。
// 这时按「缺表降级」处理（与本族其它端点同款），而不是把降级报成 500。
func requestLogFactsMissingRelation(err error) bool {
	return dashboarddegrade.IsMissingRelationError(err)
}
