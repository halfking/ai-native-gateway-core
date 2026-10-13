//go:build !integration

package admin

import (
	"regexp"
	"strings"
	"testing"
)

// S3 波 4：`/api/admin/compression/sessions` 的 bodies 读源钉桩。
//
// # 这道门守的是什么
//
// 停写（`storage.request_logs_write_enabled=false`）之后，这个端点的整页数据
// 曾经**只剩停写前的历史**——不是少一个字段，是停写后的会话一行都不出现。
// 真因有两层，都不在原来的 LATERAL 里：
//
//  1. 顶层 `whereClause` 的 `rb.outbound_body IS NOT NULL` 配 `LEFT JOIN
//     request_logs_bodies_with_current_month` —— LEFT JOIN 的 IS NOT NULL 落在
//     WHERE 里等价于内连接，于是停写后 `rb` 对新会话恒 NULL、整行被滤掉。
//  2. 改跨族之后 `LIMIT` 仍排在 `LATERAL` **之后**：LATERAL 是 `ON true` 的
//     一对一连接，会对**全部分组会话**求值才轮到 LIMIT 生效。24h 窗候选从
//     1,125 涨到 9,255 之后同窗耗时 9.9s，贴着本端点 10s 超时。
//
// # 判据为什么不写在端点上
//
// 端到端打这个端点需要「停写已生效 + 会话家族有流量」两个前置，而且第一版
// 的写法（只看 LATERAL 的谓词）就是这么漏掉顶层过滤器的。判据改为**同层驱动
// 发给 PostgreSQL 的那两条 SQL 文本**，并对第 2 条用**位置断言**（LIMIT 的
// 序号必须出现在 LATERAL 之前），因为「LIMIT 在 LATERAL 之后」与「LIMIT 不存在」
// 对结果集的影响完全不同，只有位置能区分。

// countOccurrences 数子串出现次数。判据纪律第 7 条：对「N 条同构腿」必须
// count==N 而不是 Contains —— 只查在场时，「删掉一条腿」的变异仍然全绿，
// 而那正是最危险的静默回退（去掉会话腿 = 停写后整页归零）。
func countOccurrences(haystack, needle string) int {
	if needle == "" {
		return -1 // 空 needle 会让任何 Contains 类断言恒真
	}
	return strings.Count(haystack, needle)
}

func TestCompressionSessionsPredicateServesBothFamilies(t *testing.T) {
	pred := compressionOutboundBodyPredicate

	// 两条腿必须都在场，且**恰好两条**：v1 腿 + 会话族腿。
	if got := countOccurrences(pred, "EXISTS (SELECT 1 FROM"); got != 2 {
		t.Errorf("compressionOutboundBodyPredicate 里有 %d 个 EXISTS 分支，期望 2（v1 腿 + 会话族腿）：\n%s\n"+
			"少一条 = 停写后有一族会话被整行滤掉，且症状是「页面看起来正常、只是少一半」，"+
			"没有任何错误信号（判据纪律第 3 条：必须单独判「看不见」）。", got, pred)
	}
	if !strings.Contains(pred, "request_logs_bodies_with_current_month") {
		t.Errorf("谓词里没有 v1 腿 —— 停写前的历史会话会被整页丢掉：\n%s", pred)
	}
	// 会话腿必须走同族 helper（hot ∪ parent 单键源），而不是裸表名。
	if !strings.Contains(pred, "session_bodies_hot sb") || !strings.Contains(pred, "FROM public.session_bodies sb") {
		t.Errorf("谓词的会话腿不是 SessionFamilyBodiesSourceSQL() 的 hot ∪ parent 双腿形态：\n%s\n"+
			"只查 session_bodies_hot 会漏掉已 promote 的行（本地实测父表 %s 行）。",
			pred, "session_bodies")
	}
	// ★ 反向钉桩：旧的「LEFT JOIN + WHERE IS NOT NULL」过滤形态必须已经不在谓词里。
	// 这是第一版的真凶；它一旦被改回来，本测试其它断言仍会全绿。
	if strings.Contains(stripSQLComments(pred), "rb.outbound_body") {
		t.Errorf("谓词里又出现了连接别名 rb. —— 旧的 LEFT JOIN 过滤形态已回归：\n%s", pred)
	}
}

func TestCompressionSessionsSQLHasNoBodiesJoin(t *testing.T) {
	where := `rl.ts >= $1 AND ` + compressionOutboundBodyPredicate + ` AND rl.gw_session_id IS NOT NULL`
	for name, sql := range map[string]string{
		"count": compressionSessionsCountSQL(where),
		"main":  compressionSessionsMainSQL(where, 4),
	} {
		// bodies 的**内容**必须只经 EXISTS 进来。任何 JOIN 形态都会
		// (a) 在镜像双生期虚增 COUNT(*)（本地实测 11,711 个双生 request_id），
		// (b) 把 v1 腿重新变成行基数的乘数。
		//
		// ⚠ 作用域必须排除 LATERAL 体：LATERAL 里**本来就该**有一条 v1 腿
		// （服务停写前历史）。第一版没排除它，于是这条判据对着自己刚写的
		// 正确实现报错——那说明判据的锚点选错了，不是实现错了。
		if got := countOccurrences(lateralBody(sql), "join request_logs_bodies"); got != 0 {
			t.Errorf("%s 语句在 LATERAL 之外还有 %d 处 bodies JOIN（期望 0）：\n%s\n"+
				"谓词已改成 EXISTS，JOIN 是残留 ⇒ 双生 request_id 会一对多、虚增 request_count。", name, got, sql)
		}
		if !strings.Contains(sql, compressionOutboundBodyPredicate) {
			t.Errorf("%s 语句没有内嵌 compressionOutboundBodyPredicate —— 过滤器与拼装脱钩了：\n%s", name, sql)
		}
	}
}

// ⚠ 位置判据一律作用在 stripSQLComments 之后的文本上（复用本包既有的
// 引号感知实现）：第二轮变异验证里，把源码的 `LIMIT $` 改成 `-- LIMIT $`
// 后拼出的 SQL **字面上仍含有** "LIMIT $4 OFFSET $5"，任何基于 strings.Index
// 的判据全绿——而 PostgreSQL 根本不执行它。**注释就是位置判据的伪装色。**
//
// lateralBody 抠掉 `LEFT JOIN LATERAL ( … ) latest ON true` 的体，只留下
// 「非 LATERAL 部分」。用于把 bodies JOIN 的反向断言限定在正确的作用域里。
func lateralBody(sql string) string {
	start := strings.Index(sql, "LEFT JOIN LATERAL (")
	if start < 0 {
		return sql
	}
	end := strings.Index(sql[start:], ") latest ON true")
	if end < 0 {
		return sql // 结构变了就整体交给判据报错，不要静默放过
	}
	return sql[:start] + sql[start+end:]
}

func TestCompressionSessionsPaginationPrecedesLateral(t *testing.T) {
	sql := stripSQLComments(compressionSessionsMainSQL(`rl.ts >= $1`, 4))

	// 锚点选在**子查询体**上，而不是「全文第一个 LIMIT」。
	//
	// ⚠ 第一版用的是 strings.Index(sql, "LIMIT $4 OFFSET $5")，第一轮变异
	// 验证就把它骗过去了：把源码里的 `LIMIT $` 改成 `-- LIMIT $` 只是把它
	// 变成一条 SQL 注释，字符串仍在、位置仍在 LATERAL 之前，于是判据全绿，
	// 而实际语义（LIMIT 有没有被执行）已经变了。**位置必须落在「真正会被
	// 执行的那段 SQL」里**，否则注释就是它的伪装色。
	pageStart := strings.Index(sql, "SELECT paged.*")
	lateralIdx := strings.Index(sql, "LEFT JOIN LATERAL")
	if pageStart < 0 || lateralIdx < 0 {
		t.Fatalf("mainSQL 结构变了（paged@%d, LATERAL@%d）——这条判据的前提不成立，"+
			"别默认分页仍在 LATERAL 之前：\n%s", pageStart, lateralIdx, sql)
	}
	pageBody := sql[pageStart:lateralIdx]
	limitIdx := strings.Index(pageBody, "LIMIT $4 OFFSET $5")
	if limitIdx < 0 {
		t.Errorf("分页子查询里没有 LIMIT/OFFSET 占位符：\n%s\n"+
			"⇒ 要么分页被删了，要么被注释掉了（本判据第一次就被 `-- LIMIT` 骗过）。", pageBody)
	}
	if !strings.Contains(pageBody, "ORDER BY paged.last_ts DESC") {
		t.Errorf("分页子查询里没有 ORDER BY —— LIMIT 无序，分页会返回任意 N 行：\n%s", pageBody)
	}
	// 外层（LATERAL 之后）不得再有 LIMIT：那里加 LIMIT 就是性能陷阱回归。
	outer := sql[lateralIdx:]
	if strings.Contains(outer, "LIMIT $") {
		t.Errorf("LATERAL 之后又出现了 LIMIT：\n%s\n"+
			"LATERAL 是 ON true 的一对一连接，会对全部分组会话求值才轮到 LIMIT —— "+
			"真库实测同窗 0.32s → 10.4s，已越过本端点 10s 超时。", outer)
	}
}

func TestCompressionSessionsLateralKeepsBothFamiliesWithoutTSEquality(t *testing.T) {
	lat := compressionLatestOrigMsgsLATERAL

	// v1 腿：保留 ts 等值（服务停写前历史，v1 侧 request_id/ts 全等）。
	if !strings.Contains(lat, "JOIN request_logs_bodies_with_current_month rb2") {
		t.Errorf("LATERAL 少了 v1 腿 —— 停写前的历史会话会丢掉 estimated_original_msgs：\n%s", lat)
	}
	if !strings.Contains(lat, "ON rb2.request_id = rl2.request_id AND rb2.ts = rl2.ts") {
		t.Errorf("v1 腿的 ts 等值条件被改动了（本地实测 35,506/35,506 全等，改掉会改变历史口径）：\n%s", lat)
	}
	// 会话腿：hot ∪ parent，**request_id 单键**。
	if !strings.Contains(lat, "FROM public.session_bodies_hot") ||
		!strings.Contains(lat, "FROM public.session_bodies") {
		t.Errorf("LATERAL 的会话腿不是 session_bodies_hot ∪ session_bodies 双腿形态：\n%s", lat)
	}
	// §9.231 列合同：会话族 bodies 源**不投影 ts**，等值 JOIN 是 99.85% 不命中
	// 的静默行数变化。任何人给会话腿补上 ts 等值都必须先撞这里。
	//
	// ⚠ 判据必须匹配**比较式**而不是某个具体别名拼法。第一版钉的是
	// `sb.ts = rl3.ts` 这一条字面量，变异验证立刻证明它是假绿：换个别名
	// 写成 `… AND session_turns.ts = sb.ts` 就完全绕过——而那正是要拦的东西。
	// ⇒ 用正则匹配 `sb.ts` 出现在比较运算的任何一侧。
	if m := regexp.MustCompile(`(?i)\bsb\.ts\s*(=|<>|!=|<=|>=|<|>)`).FindString(lat); m != "" {
		t.Errorf("LATERAL 的会话腿出现了 ts 比较（%q）——§9.231 实测两侧 ts 99.85%% 不等，"+
			"补上它会把解析期错误换成静默的行数变化：\n%s", m, lat)
	}
	if regexp.MustCompile(`(?i)\bts\s*(=|<>|!=|<=|>=|<|>)\s*[a-z_0-9]*\.?ts\b`).MatchString(
		regexp.MustCompile(`(?is)UNION ALL.*`).FindString(lat)) {
		t.Errorf("LATERAL 的会话腿分支里出现了 ts 与 ts 的比较（自等值形态）：\n%s", lat)
	}
	// 「取最新一轮」语义不得被换成聚合：MAX 会把停写前会话的返回值一并改掉。
	if strings.Contains(strings.ToUpper(lat), "MAX(") {
		t.Errorf("LATERAL 里出现了 MAX( —— 「最新一轮」语义被换成聚合，"+
			"停写前会话的 estimated_original_msgs 会与历史不一致：\n%s", lat)
	}
}
