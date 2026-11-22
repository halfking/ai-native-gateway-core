package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// 2026-11-23 批判式审计轮：统一统计端点的时间范围解析（真实区间 + 天数预设双入口）。
//
// ★ 本文件要解决的问题（审计取证，非推测）：
//
//	全站统计页面统一用 KxDateRangePicker 选择时间范围，但绝大多数页面的后端
//	只接受 `?days=N`，并把 N 解释为「相对现在的滚动窗口」
//	（如 `ts >= NOW() - INTERVAL '24 hours'`）。前端 useSpanDaysRange 又把
//	用户选的**自定义日期区间**压成「跨度天数」再写进 days。
//	⇒ 用户在组件里选 2026-09-01 ~ 2026-09-10，实际拿到的是**最近 10 天**。
//	这是静默错误答案（不是量具问题）：界面显示自定义区间，数据却是另一个窗口。
//
//	实测证据（2026-11-23，本地真实网关）：
//	  GET /api/admin/work-types/stats?start_date=2026-09-01&end_date=2026-09-10
//	  → 200，且响应 window="24h"（参数被静默忽略、回落默认窗口）。
//
// ★ 修法（与既有 admin/usage.go:resolveUsageTimeRange 同口径，不新造轮子）：
//
//	1. 后端：统计端点同时接受 `start`/`end`（YYYY-MM-DD，系统时区）与
//	   `days`（天数预设）。给了 start/end ⇒ 用真实区间；否则回落 days 预设。
//	   两者都缺 ⇒ 用端点自己的默认天数（保持旧行为不变）。
//	2. 前端：useRangeDays 在自定义区间时透传真实 start/end，不再压成天数。
//
// ★ 为什么 days 分支保持「零参数 + 字面量 interval」：
//
//	- 旧语义逐字不变（`NOW() - INTERVAL '24 hours'` 在库里求值，客户端时钟
//	  不参与；改成 `INTERVAL '1 day' * $1` 在 DST 时区会差出 1 小时）；
//	- **参数序号不变** ⇒ 端点里其它占位符（tenant 片段等）不需要重排，
//	  改造面与回归风险最小。
//
// ★ 区间上限：90 天（与 work-types/analytics 的 days 上界一致）。
//   超过 ⇒ 400，不静默夹紧（静默夹紧会让用户以为看到的是自己选的窗口）。

// statsTimeRange 是统计端点解析后的时间窗口。
type statsTimeRange struct {
	// Start/End 仅在 Custom=true 时有效：半开区间 [Start, End)（UTC 瞬间，
	// 与 timestamptz 比较口径一致）。
	Start time.Time
	End   time.Time
	// Days 是跨度天数（≥1），用于响应标签与前端展示。
	Days int
	// Hours 是 days 分支的窗口小时数（= Days*24）。
	Hours int
	// Custom 为真表示来自 start/end 的真实区间（而非 days 预设）。
	Custom bool
	// StartDate/EndDate 是**原始日期串**（系统时区的日历日，YYYY-MM-DD）。
	// ★ 标签必须用它们，不能用 Start/End 的 UTC 瞬时去 Format：
	//   UTC+8 下 2026-09-01 的零点 = 2026-08-31T16:00Z ⇒ 会显示成前一天。
	StartDate string
	EndDate   string
}

// statsDateRangeMaxDays 是统计区间的跨度上限（天）。
const statsDateRangeMaxDays = 90

// parseStatsTimeRange 解析统计端点的时间范围：
//   - `start` + `end` 同时存在 ⇒ 真实区间（Custom=true），Days=跨度天数；
//   - 只给其一 ⇒ 报错（半截区间无法解释，回落预设会静默给出错误窗口）；
//   - `days` ⇒ 天数预设（相对现在的滚动窗口，旧行为）；
//   - 都缺 ⇒ defaultDays 预设。
//
// 返回的窗口在 Custom 时是 [Start, End)；end 日全天计入（end+1 天零点）。
func parseStatsTimeRange(r *http.Request, defaultDays int) (statsTimeRange, error) {
	if defaultDays < 1 {
		defaultDays = 1
	}
	if defaultDays > statsDateRangeMaxDays {
		defaultDays = statsDateRangeMaxDays
	}

	startStr := queryString(r, "start")
	endStr := queryString(r, "end")

	if startStr != "" || endStr != "" {
		if startStr == "" || endStr == "" {
			return statsTimeRange{}, fmt.Errorf("start and end must both be provided when using a custom range")
		}
		// ★ 用 settings.SystemDayRangeUtc（现成工具）：把系统时区的
		//   [startDate, endDate] 闭区间转成 UTC 瞬间对，end 独占 = end 日 +1 天的
		//   零点（含 end<start 的校验）。自拼 ParseSystemDate(...).UTC() 会在
		//   UTC+8 下把起点显示成前一天（时区轮的单测已钉过这条）。
		startUtc, endUtc, err := settings.SystemDayRangeUtc(startStr, endStr)
		if err != nil {
			return statsTimeRange{}, fmt.Errorf("invalid date range: expected YYYY-MM-DD start and end")
		}
		// ★ End 已是「end 日 +1 天零点」（独占端），故跨度天数直接是
		//   (End-Start)/24h，**不能再 +1**（否则 10 天区间会被算成 11 天）。
		spanDays := int(endUtc.Sub(startUtc) / (24 * time.Hour))
		if spanDays < 1 {
			spanDays = 1
		}
		if spanDays > statsDateRangeMaxDays {
			return statsTimeRange{}, fmt.Errorf("date range cannot exceed %d days", statsDateRangeMaxDays)
		}
		return statsTimeRange{
			Start:     startUtc,
			End:       endUtc,
			Days:      spanDays,
			Hours:     spanDays * 24,
			Custom:    true,
			StartDate: startStr,
			EndDate:   endStr,
		}, nil
	}

	// days 分支：旧行为逐字保留——「相对现在」的滚动窗口。
	days := defaultDays
	if raw := queryString(r, "days"); raw != "" {
		n, err := parsePositiveDays(raw)
		if err != nil {
			return statsTimeRange{}, err
		}
		days = n
	}
	return statsTimeRange{
		Days:   days,
		Hours:  days * 24,
		Custom: false,
	}, nil
}

// parsePositiveDays 解析 days 参数：必须是 1..statsDateRangeMaxDays 的纯整数。
// 越界 ⇒ 报错（不静默回落）——静默回落会让用户以为看到的是自己选的窗口。
func parsePositiveDays(raw string) (int, error) {
	// strconv.Atoi 本身即严格解析（"7abc" 会报错），不必再自写。
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("invalid days: expected integer 1..%d", statsDateRangeMaxDays)
	}
	if n < 1 || n > statsDateRangeMaxDays {
		return 0, fmt.Errorf("invalid days: expected integer 1..%d", statsDateRangeMaxDays)
	}
	return n, nil
}

// sqlPredicate 产出可直接拼进 WHERE 的时间谓词与参数。
//
//	Custom=false ⇒ "ts >= NOW() - INTERVAL '24 hours'" 口径不变（零参数）
//	Custom=true  ⇒ "ts >= $N AND ts < $M"（真实区间，参数化时间戳）
//
// 参数化只在自定义区间时引入 ⇒ days 路径的占位符编号完全不变。
func (tr statsTimeRange) sqlPredicate(tsColumn string, argIdx int) (string, []any) {
	if tr.Custom {
		return fmt.Sprintf("%s >= $%d AND %s < $%d", tsColumn, argIdx, tsColumn, argIdx+1),
			[]any{tr.Start, tr.End}
	}
	// 旧形态：`NOW() - INTERVAL '{hours} hours'`（字面量，零参数）。
	return fmt.Sprintf("%s >= NOW() - INTERVAL '%d hours'", tsColumn, tr.Hours), nil
}

// label 是响应里的窗口标签：自定义区间显示日期串，days 显示天数档位。
//
// ★ 旧标签形态逐字保留（"24h" / "7d" / "30d"）⇒ 前端既有取值逻辑不变。
//   旧 parseStatsWindow 的规则判的是**天数 n**（n%24==0 且 n>=24 ⇒ "Nh"，
//   否则 "Nd"）：n=1 ⇒ "1d"、n=7 ⇒ "7d"、n=30 ⇒ "30d"。
//   ⚠️ 注意别按 Hours 判定：7 天 = 168h，168%24==0 ⇒ 会误成 "168h"。
func (tr statsTimeRange) label() string {
	if tr.Custom {
		return tr.StartDate + "~" + tr.EndDate
	}
	if tr.Days%24 == 0 && tr.Days >= 24 {
		return fmt.Sprintf("%dh", tr.Days*24)
	}
	return fmt.Sprintf("%dd", tr.Days)
}
