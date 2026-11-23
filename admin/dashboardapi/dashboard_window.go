package dashboardapi

// dashboard_window.go —— 仪表盘族端点共享的「窗口」解析与 SQL 谓词片段
// （2026-11-23 会话统计/看板日期范围对齐轮）。
//
// # 为什么要有这一层
//
// 看板页（/dashboard?tab=board）的时间范围控件支持**日历日区间**（自定义起止），
// 后端 /dashboard/board 走 resolveUsageTimeRange（admin/usage.go）：start/end 按
// **系统时区**日切解析，end 独占（end+1 天 00:00）。
//
// 会话统计页（/dashboard?tab=stats）此前只认 days ⇒ 同一个「日期范围」在两页
// 既**控件不同**（预设档位/自定义入口不同）又**窗口不同**（滚动 NOW()-N vs 日历日）：
//   - 看板「近 7 天」= 日历日 [今天-6, 今天]；
//   - 会话统计「近 7 天」= 滚动 [now-7d, now]。
// ⇒ 同一句「近 7 天」在两页指的不是同一个区间（这是用户报的「日期范围不对」）。
//
// 本文件把「显式日历窗口」做成仪表盘族的**共享能力**：会话总览/会话趋势/
// 健康度/性能/错误这五族端点在收到 start_date/end_date 时，都按同一把尺取数
// （系统时区日历日、end 独占、跨度上限 366 天，与看板 resolveUsageTimeRange 同款）。
//
// # 不变量（改本文件前必须先读）
//
//	① **days 路径逐字不变**：没有 start_date/end_date 时，SQL 仍是
//	   `first_request_at >= NOW() - INTERVAL 'N days'`（字面量、不带参数）
//	   ⇒ 现役调用方的查询计划与结果**零变化**（本轮不碰现役口径）。
//	② calendar 路径才引入参数化边界 [start, end)（end 独占 ⇒ 末日整天被包含）。
//	③ 日桶（趋势图横轴）在 calendar 路径按**系统时区**日历日分桶
//	   （`AT TIME ZONE`）——否则 +08 时区下横轴会比所选区间整体前移一天
//	   （PG 会话时区不是钉死的 Asia/Shanghai，见 domains/analytics 的显式
//	   `AT TIME ZONE 'Asia/Shanghai'` 惯例）。
//	④ 探测口径（include_probes）与窗口正交：两条路都保留。

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// maxCalendarSpanDays 显式日历窗口的跨度上限（天）。
// 与看板 resolveUsageTimeRange 的 366 天同款：防止恶意调用方用超大区间
// 逼出全表扫描。
const maxCalendarSpanDays = 366

// sessionWindow 承载本请求的窗口与探测口径。degraded 指针由调用方传入
// （各腿并发置位），标记「宇宙依赖缺失 → 全量降级」发生过。
type sessionWindow struct {
	calendar      bool
	days          int
	startUTC      time.Time // calendar 模式：[startUTC, endUTC)（系统时区日历折 UTC 界）
	endUTC        time.Time
	startDate     string
	endDate       string
	includeProbes bool
	degraded      *atomic.Bool
}

// markDegraded 标记本请求发生过宇宙降级（响应 probe_scope=degraded_included）。
func (w sessionWindow) markDegraded() {
	if w.degraded != nil {
		w.degraded.Store(true)
	}
}

// parseSessionWindow 解析 start_date/end_date（可选）与 include_probes（可选）。
// 日期对非法（格式错 / end 早于 start / 只给一天 / 跨度超限）时报错，
// 由调用方回 400（参数错不是服务端故障）。
//
// ★ 与看板同把尺：settings.SystemDayRangeUtc = 系统时区日历日、end 独占
//
//	（end+1 天 00:00）。默认系统时区 Asia/Shanghai 时与物化视图的
//	`AT TIME ZONE 'Asia/Shanghai'` 日桶同尺 ⇒ 物化视图口径的行不会漏。
func parseSessionWindow(r *http.Request, params QueryParams) (sessionWindow, error) {
	w := sessionWindow{days: params.Days}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("include_probes"))) {
	case "1", "true", "on", "yes":
		w.includeProbes = true
	}
	sd := strings.TrimSpace(r.URL.Query().Get("start_date"))
	ed := strings.TrimSpace(r.URL.Query().Get("end_date"))
	if sd == "" && ed == "" {
		return w, nil
	}
	if sd == "" || ed == "" {
		return w, fmt.Errorf("start_date and end_date must be given together")
	}
	startUTC, endUTC, err := settings.SystemDayRangeUtc(sd, ed)
	if err != nil {
		return w, err
	}
	if endUTC.Sub(startUTC) > maxCalendarSpanDays*24*time.Hour {
		return w, fmt.Errorf("date range cannot exceed %d days", maxCalendarSpanDays)
	}
	w.calendar = true
	w.startUTC, w.endUTC = startUTC, endUTC
	w.startDate, w.endDate = sd, ed
	return w, nil
}

// clauseAt 返回窗口谓词片段的**文本**，但**不追加参数**。
//
// ★ 用途：同一条 SQL 里窗口可能出现两次（WHERE 谓词 + 聚合 FILTER）——
//
//	两次必须引用**同一组占位符**（同一组边界参数），否则要么参数对不上，
//	要么同一窗口被追加两遍参数。
//	⇒ 调用方先调一次 clause()（追加边界参数、拿到起始占位符），
//	再用 clauseAt() 复用同一组占位符的不同列名写法。
//
// argIdx 是边界参数的**首个**占位符编号（= 调用 clause() 之前的 argIdx）。
func (w sessionWindow) clauseAt(col string, argIdx int) string {
	if !w.calendar {
		return fmt.Sprintf("%s >= NOW() - INTERVAL '%d days'", col, w.days)
	}
	return fmt.Sprintf("%s >= $%d AND %s < $%d", col, argIdx, col, argIdx+1)
}

// clause 返回窗口谓词片段，并把参数按占位符顺序追加进 args。
//
//   - rolling 模式（无显式日历窗口）：**逐字**沿用既有字面量
//     `col >= NOW() - INTERVAL 'N days'`（不占参数位）⇒ 现役查询零变化；
//   - calendar 模式：`col >= $n AND col < $n+1`（end 独占 ⇒ 末日整天被包含）。
//
// col 由调用方给出（session_summaries.first_request_at / request_stats.created_at …）。
func (w sessionWindow) clause(col string, args *[]interface{}, argIdx *int) string {
	if !w.calendar {
		return fmt.Sprintf("%s >= NOW() - INTERVAL '%d days'", col, w.days)
	}
	clause := fmt.Sprintf("%s >= $%d AND %s < $%d", col, *argIdx, col, *argIdx+1)
	*args = append(*args, w.startUTC, w.endUTC)
	*argIdx += 2
	return clause
}

// prevClause 返回「上一周期」窗口谓词片段（成本/会话增长率的分母）。
//
//   - rolling 模式：逐字沿用 `NOW() - INTERVAL '2N days' … NOW() - INTERVAL 'N days'`；
//   - calendar 模式：上一周期 = [start-窗口长, start)（与当前窗口等长）。
func (w sessionWindow) prevClause(col string, args *[]interface{}, argIdx *int) string {
	if !w.calendar {
		return fmt.Sprintf(
			"%s >= NOW() - INTERVAL '%d days' AND %s < NOW() - INTERVAL '%d days'",
			col, w.days*2, col, w.days,
		)
	}
	span := w.endUTC.Sub(w.startUTC)
	prevStart := w.startUTC.Add(-span)
	clause := fmt.Sprintf("%s >= $%d AND %s < $%d", col, *argIdx, col, *argIdx+1)
	*args = append(*args, prevStart, w.startUTC)
	*argIdx += 2
	return clause
}

// dayExpr 返回日桶表达式（趋势图横轴的日期）。
//
//   - rolling 模式：逐字沿用 `DATE(col)`（现役口径不变）；
//   - calendar 模式：`(col AT TIME ZONE $tz)::date` —— 按**系统时区**日历日分桶，
//     否则 PG 会话时区（未钉死 Asia/Shanghai）会让横轴整体前移一天。
//
// ★ 返回的表达式必须在 SELECT 与 GROUP BY 里**逐字复用**同一个字符串
//
//	（同一个占位符号）⇒ 调用方只调一次本方法，参数也只追加一次。
func (w sessionWindow) dayExpr(col string, args *[]interface{}, argIdx *int) string {
	if !w.calendar {
		return fmt.Sprintf("DATE(%s)", col)
	}
	expr := fmt.Sprintf("(%s AT TIME ZONE $%d)::date", col, *argIdx)
	*args = append(*args, settings.SystemTimezoneName())
	*argIdx++
	return expr
}
