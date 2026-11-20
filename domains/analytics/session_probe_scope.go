package analytics

// session_probe_scope.go —— 会话级「探测口径」与「日历窗口」的可复用片段
// （2026-11-21 统计口径对齐轮）。
//
// # 这组片段在守什么
//
// /admin/session-analytics 的四个物化视图（迁移 981/994）有一个**会话宇宙**
// 定义：session_summaries × sessions × session_dim 三表连接 + owner 双源键
// 非空 + is_probe 运维排除位 + session_analytics_is_probe(owner, client, title)
// 三维模式判定。任何一个新读面（如 /dashboard 的会话统计）想与物化视图的
// 数字对齐，**必须**用同一个宇宙——不是「写个差不多的过滤」，而是逐字同
// 一份谓词。本文件把这份谓词从迁移 SQL 里提为 Go 常量（迁移侧由
// TestSessionProbeScopeMatches981 契约门钉住两边一致），任何读面都从这里取，
// 不许手抄。
//
// # 判定层级（为什么三层都要）
//
//   - sessions.is_probe：981 物化缓存 + 运维手工排除位（手工置真 ⇒ 排除）；
//   - session_analytics_is_probe(owner, client_type, title)：函数本体的三维
//     模式判定（self-check/deployment-gate/test/verify/e2e/probe/…）；
//   - owner 非空：物化视图按 owner 键聚合，owner 空的会话**不在**视图里
//     ⇒ 对齐面也必须排除（否则会话数比对视图多）。
//
// # 日历窗口为什么不用 settings.SystemDayRangeUtc
//
// 981/994 的日桶是 `(first_request_at AT TIME ZONE 'Asia/Shanghai')::date`
// ——**SQL 里写死 Asia/Shanghai**，不随部署机系统时区走。要对齐物化视图，
// 调用方把日期串折算成窗口界时也必须用同一把尺（+08 固定偏移，无夏令时）；
// settings.SystemDayRangeUtc 折的是**系统时区**，两者在非 +08 部署机上会
// 错位一天。这里用固定 +08（与 992「面向用户的统计 API 用 CST 业务函数」
// 的产品口径一致）。

import (
	"fmt"
	"time"
)

// SessionOwnerKeyExpr 是 994 的 owner 双源键表达式（%s×2 = sessions 别名、
// session_dim 别名）。
const SessionOwnerKeyExpr = "NULLIF(COALESCE(%[1]s.owner_user, %[2]s.api_key_owner_user), '')"

// SessionUniverseJoin 返回 user_session_aggregates（994）的连接子句：
// sessions 内连接（会话宇宙只含 sessions 表登记的会话）+ session_dim 左连接
// （owner 双源回退）。连接键都带 tenant_id（与 994 逐字同构）。
//
// 参数：ssAlias = session_summaries 别名，sAlias = sessions 别名，
// sdAlias = session_dim 别名。
func SessionUniverseJoin(ssAlias, sAlias, sdAlias string) string {
	return fmt.Sprintf(
		"JOIN sessions %[1]s ON %[1]s.session_id = %[2]s.session_key AND %[1]s.tenant_id = %[2]s.tenant_id\n"+
			"LEFT JOIN session_dim %[3]s ON %[3]s.session_key = %[2]s.session_key AND %[3]s.tenant_id = %[2]s.tenant_id",
		sAlias, ssAlias, sdAlias)
}

// SessionUniverseWhere 返回会话宇宙的 WHERE 合取项（owner 非空 + 探测排除）。
// 与 994 重建 user_session_aggregates 的 WHERE 逐字同语义；sAlias = sessions
// 别名，sdAlias = session_dim 别名。
func SessionUniverseWhere(sAlias, sdAlias string) string {
	owner := fmt.Sprintf(SessionOwnerKeyExpr, sAlias, sdAlias)
	return fmt.Sprintf(
		"%[1]s IS NOT NULL\n"+
			"  AND NOT COALESCE(%[2]s.is_probe, false)\n"+
			"  AND NOT session_analytics_is_probe(\n"+
			"      COALESCE(%[2]s.owner_user, %[3]s.api_key_owner_user), %[2]s.client_type, %[2]s.title)",
		owner, sAlias, sdAlias)
}

// shanghaiLoc 是与迁移 SQL `'Asia/Shanghai'` 同一把尺的固定 +08 时区
// （Asia/Shanghai 无夏令时，FixedZone 语义等价且不依赖系统 tzdata）。
var shanghaiLoc = time.FixedZone("CST", 8*60*60)

// ShanghaiCalendarDayStart 取某时刻在 Asia/Shanghai 日历下的当日 00:00。
func ShanghaiCalendarDayStart(t time.Time) time.Time {
	local := t.In(shanghaiLoc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, shanghaiLoc)
}

// ShanghaiCalendarRangeUtc 把 [startDay, endDay]（YYYY-MM-DD，按上海日历解释）
// 折成半开窗口 [start, end) 的 UTC 界——与物化视图日桶
// `(first_request_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN start AND end`
// 的行集一致。
func ShanghaiCalendarRangeUtc(startDay, endDay string) (time.Time, time.Time, error) {
	start, err := time.ParseInLocation("2006-01-02", startDay, shanghaiLoc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start date: %w", err)
	}
	end, err := time.ParseInLocation("2006-01-02", endDay, shanghaiLoc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid end date: %w", err)
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("end must be on or after start")
	}
	return start.UTC(), end.AddDate(0, 0, 1).UTC(), nil
}
