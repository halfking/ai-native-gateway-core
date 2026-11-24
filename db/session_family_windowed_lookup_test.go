package db

// session_family_windowed_lookup_test.go —— 2026-11-25 request-logs 超时根修的门。
//
// 根因（211 生产 EXPLAIN ANALYZE 实证）：月分区统计信息陈旧 ⇒ 冷分区估 3 行
// ⇒ details 的 ON 子句连接被规划成 Nested Loop + Materialize + Join Filter
// （单条语句 1 亿次比较、26~38s）。修法之一是把 details 腿改成「按连接键的单行
// 点查」（LEFT JOIN LATERAL … LIMIT 1），代价与估算无关。
//
// 本文件钉四件事：
//
//  1. 【形状】两臂的 details 腿都是关联点查——栅栏 `LIMIT 1` 在、连接键三条在、
//     分区键界在、点查的列 = 投影里以 d.<col> 出场的全部列（不多不少）。
//     少了栅栏，PG 会把简单子查询上拉回普通 LEFT JOIN（实测计划逐字回到老形状）。
//  2. 【同源】臂列表与整体源由同一对构造函数拼出（分页 top-K 与聚合读同一份臂）。
//  3. 【探测臂】按行点查形态与聚合形态的差别**只在**费用聚合怎么连；窗口 /
//     class 过滤 / 聚合表达式 / `GROUP BY request_id`（内连接语义的来源）逐字相同。
//  4. 【行集等价·真库】点查形态与改前的 ON 子句形态在真库上双向 EXCEPT 为空。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// lateralBodies 取出 sql 里每一段 `LEFT JOIN LATERAL (… LIMIT 1) d ON TRUE` 的
// 括号内文本。数量由调用方断言（两臂 ⇒ 2 段）。
func lateralBodies(t *testing.T, sql string) []string {
	t.Helper()
	const open, close = " LEFT JOIN LATERAL (", " LIMIT 1) d ON TRUE"
	var out []string
	rest := sql
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			break
		}
		j := strings.Index(rest[i:], close)
		if j < 0 {
			t.Fatalf("LEFT JOIN LATERAL 开启后没有找到闭合 %q：\n%s", close, rest[i:i+200])
		}
		out = append(out, rest[i+len(open):i+j])
		rest = rest[i+len(open)+j+len(close):]
	}
	return out
}

func TestSessionFamilyTurnsSourceWindowedSQL_DetailsLegIsCorrelatedKeyLookup(t *testing.T) {
	sql := SessionFamilyTurnsSourceWindowedSQL("$1", "$2")

	// ① 两臂各一段关联点查，且栅栏在（LIMIT 1 ⇒ 不被上拉回普通 LEFT JOIN）。
	bodies := lateralBodies(t, sql)
	if len(bodies) != 2 {
		t.Fatalf("业务两臂必须各有一段 details 关联点查，得到 %d 段：\n%s", len(bodies), sql)
	}
	// ② 不许残留 ON 子句形态的 details 连接（那正是会退化成 Join Filter 的形状）。
	if strings.Contains(sql, "LEFT JOIN public.session_turn_details") {
		t.Fatalf("details 腿不得再用 ON 子句等值连接（统计陈旧时会退化成 Nested Loop + Join Filter）：\n%s", sql)
	}
	wantRels := []string{"public.session_turn_details_hot d", "public.session_turn_details d"}
	for i, body := range bodies {
		// ③ 点查的列表 = 投影里以 d.<col> 出场的全部列（按名排序 ⇒ 文本确定）。
		//    不用 `SELECT *`：列集显式写出，details 加列时这里不会被悄悄带偏。
		if !strings.HasPrefix(body, "SELECT ") {
			t.Fatalf("臂 %d 的点查应以 SELECT 起头：%s", i, body[:min(len(body), 60)])
		}
		sel := body[len("SELECT "):strings.Index(body, " FROM ")]
		gotCols := strings.Split(sel, ", ")
		if sel != sessionTurnDetailsLookupColumns {
			t.Fatalf("臂 %d 的点查列与 sessionTurnDetailsLookupColumns 不一致：\n got %s", i, sel)
		}
		if len(gotCols) != len(detailsProjectionColumns) {
			t.Fatalf("臂 %d 点查列数 = %d，details 投影列数 = %d", i, len(gotCols), len(detailsProjectionColumns))
		}
		for _, c := range gotCols {
			if !strings.HasPrefix(c, "d.") || c == "d.*" {
				t.Fatalf("点查列必须是具名的 d.<col>，得到 %q", c)
			}
		}
		// ④ 读的是对应的 details 关系（热表臂读 _hot、父表臂读父表）。
		if !strings.Contains(body, wantRels[i]) {
			t.Fatalf("臂 %d 应读 %s：\n%s", i, wantRels[i], body)
		}
		// ⑤ 连接键三条 + 分区键界：键唯一点查 ⇒ 每行至多 1 行 details；界 ⇒ 规划期裁分区。
		for _, want := range []string{
			"WHERE d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date",
			"AND d.partition_date >= $1 AND d.partition_date <= $2",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("臂 %d 的点查缺少 %q：\n%s", i, want, body)
			}
		}
	}
	// ⑥ turns 侧分区键界仍在两臂上（R181 的裁剪前提，不许被这次改动碰掉）。
	if strings.Count(sql, " WHERE t.partition_date >= $1 AND t.partition_date <= $2)") != 2 {
		t.Fatalf("turns 两臂必须各自带分区键界并以右括号收尾：\n%s", sql)
	}
	if strings.Count(sql, "UNION ALL") != 1 {
		t.Fatalf("业务源应是两臂 UNION ALL，得到 %d 处", strings.Count(sql, "UNION ALL"))
	}
}

func TestSessionFamilyWindowedArms_AndSourceAreComposedFromTheSameText(t *testing.T) {
	arms := SessionFamilyTurnsWindowedArms("$7", "$8")
	if len(arms) != 2 {
		t.Fatalf("业务臂应为 2（热表、父表），得到 %d", len(arms))
	}
	for i, arm := range arms {
		if !strings.HasPrefix(arm, "(") || !strings.HasSuffix(arm, ")") {
			t.Fatalf("臂 %d 必须自带括号：%s", i, arm[:40])
		}
		if !strings.Contains(arm, "t.partition_date >= $7 AND t.partition_date <= $8") {
			t.Fatalf("臂 %d 必须带调用方指定的分区键界占位符 $7/$8", i)
		}
	}
	// 整体源 = 臂用 UNION ALL 接起来 ⇒ 聚合 / 分组路径与分页路径读的是同一份臂文本。
	if want := "(" + strings.Join(arms, " UNION ALL ") + ")"; SessionFamilyTurnsSourceWindowedSQL("$7", "$8") != want {
		t.Fatalf("SessionFamilyTurnsSourceWindowedSQL 必须由 SessionFamilyTurnsWindowedArms 拼出")
	}

	all := SessionFamilyTurnsAllWindowedArms("$1", "$2", "$3", "$4")
	if len(all) != 3 {
		t.Fatalf("包含探测应为 3 臂，得到 %d", len(all))
	}
	for i := 0; i < 2; i++ {
		if all[i] != SessionFamilyTurnsWindowedArms("$1", "$2")[i] {
			t.Fatalf("包含探测的臂 %d 与业务臂不同源", i)
		}
	}
	probe := all[2]
	// 分页用的探测臂 = 按行点查形态（费用聚合按 t.request_id 关联）。
	for _, want := range []string{
		"JOIN LATERAL (",
		"WHERE request_id = t.request_id",
		"AND occurred_at >= $3 AND occurred_at <= $4",
		"AND traffic_class <> 'business'",
		"GROUP BY request_id",
		") uf ON TRUE",
		"WHERE t.ts >= $3 AND t.ts <= $4",
	} {
		if !strings.Contains(probe, want) {
			t.Fatalf("分页探测臂缺少 %q：\n%s", want, probe[len(probe)-900:])
		}
	}
	// 聚合路径的整体源仍用「先聚合再 JOIN」的哈希连接形态。
	agg := SessionFamilyTurnsSourceWindowedAllSQL("$1", "$2", "$3", "$4")
	if !strings.Contains(agg, ") uf ON uf.request_id = t.request_id") {
		t.Fatalf("聚合路径的探测臂必须保持 JOIN 聚合子查询的形态")
	}
	if strings.Contains(agg, "JOIN LATERAL (") && strings.Contains(agg, "WHERE request_id = t.request_id") {
		t.Fatalf("聚合路径的探测臂不应混入按行点查形态")
	}
}

func TestSessionFamilyProbeArmLookupSQL_DiffersFromSourceOnlyInHowCostsAreJoined(t *testing.T) {
	src := SessionFamilyProbeArmSourceSQL("$3", "$4")
	lookup := SessionFamilyProbeArmLookupSQL("$3", "$4")

	// 外层 118 列投影同一份（两形态只在内层不同）。
	head := func(s string) string { i := strings.Index(s, " FROM ("); return s[:i] }
	if head(src) != head(lookup) {
		t.Fatalf("两种探测臂的外层投影必须逐字相同")
	}
	if strings.Count(head(lookup), " AS ") != len(canonicalColumnOrderV2) {
		t.Fatalf("外层投影必须是 %d 列", len(canonicalColumnOrderV2))
	}
	// 聚合表达式、窗口、class 过滤逐字相同。
	for _, want := range []string{
		"SUM(cost_amount) AS probe_cost",
		"MAX(cost_currency) AS probe_currency",
		"SUM(credits_charged) AS probe_credits",
		"FROM public.usage_facts",
		"AND traffic_class <> 'business'",
		"GROUP BY request_id",
		"WHERE t.ts >= $3 AND t.ts <= $4",
	} {
		for name, s := range map[string]string{"source": src, "lookup": lookup} {
			if !strings.Contains(s, want) {
				t.Fatalf("%s 形态缺少 %q", name, want)
			}
		}
	}
	// ★ GROUP BY request_id 必须在点查形态里保留：去掉它以后「无 fact 的探测行」
	//   会从「零行（被内连接丢掉）」变成「一行全 NULL 费用」⇒ 行集变大、口径漂移。
	if strings.Count(lookup, "GROUP BY request_id") != 1 {
		t.Fatalf("点查形态必须恰好保留一处 GROUP BY request_id")
	}
	// 窗口参数复用同一对 $3/$4（ts 谓词与 occurred_at 界同窗）。
	if strings.Count(lookup, "occurred_at >= $3 AND occurred_at <= $4") != 1 {
		t.Fatalf("点查形态的 usage_facts 窗口必须绑 $3/$4")
	}
}

// legacyWindowedOnJoinSQL 重建**改前**的带界 ON 子句形态（R181 的
// SessionFamilyTurnsSourceWindowedSQL）：取不带界的原生两臂源，在每条 details
// 连接的 ON 子句里补上界、并在 turns 侧加上界。它是行集等价对拍的参照。
func legacyWindowedOnJoinSQL(pdFrom, pdTo string) string {
	plain := SessionFamilyTurnsSourceSQL() // "(SELECT … UNION ALL SELECT …)"
	joinTail := " AND d.partition_date = t.partition_date"
	perArm := strings.Split(plain[1:len(plain)-1], " UNION ALL ")
	if len(perArm) != 2 {
		panic("unexpected plain source shape")
	}
	for i, arm := range perArm {
		if strings.Count(arm, joinTail) != 1 {
			panic("unexpected details join shape in arm " + arm[:80])
		}
		perArm[i] = "(" + strings.Replace(arm, joinTail,
			joinTail+" AND d.partition_date >= "+pdFrom+" AND d.partition_date <= "+pdTo+
				" WHERE t.partition_date >= "+pdFrom+" AND t.partition_date <= "+pdTo, 1) + ")"
	}
	return strings.Join(perArm, " UNION ALL ")
}

// logsLookupRealDBPool 是真库对拍用的连接池；没配 TEST_PG_URL 时跳过。
//
// ⚠ 门禁纪律：跳过**不构成**证据（同 logs_single_scan_test.go 的先例）。
func logsLookupRealDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the details key lookup keeps the row set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Skipf("cannot parse TEST_PG_URL: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Skipf("cannot connect to TEST_PG_URL: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("cannot ping TEST_PG_URL: %v", err)
	}
	return pool
}

// exceptBothWays 返回 (new EXCEPT old) 与 (old EXCEPT new) 的行数。
func exceptBothWays(t *testing.T, pool *pgxpool.Pool, newSQL, oldSQL string, args ...any) (int, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var newerOnly, olderOnly int
	if err := pool.QueryRow(ctx,
		"SELECT (SELECT COUNT(*) FROM ("+newSQL+" EXCEPT "+oldSQL+")), (SELECT COUNT(*) FROM ("+oldSQL+" EXCEPT "+newSQL+"))",
		args...).Scan(&newerOnly, &olderOnly); err != nil {
		t.Fatalf("EXCEPT 对拍失败: %v", err)
	}
	return newerOnly, olderOnly
}

// TestSessionFamilyTurnsSourceWindowedSQL_RowSetEqualsLegacyOnJoin 是点查形态的
// **行集等价**对拍：与改前的 ON 子句形态在真库上双向 EXCEPT 必须为空
// （EXCEPT 比较全部 118 列，NULL 安全）。
//
// 覆盖两个窗口：整表（界放宽到覆盖全部月份分区）与「最近有数据的一天」
// （同一语句里两条臂都裁到单个分区时的形态）。
func TestSessionFamilyTurnsSourceWindowedSQL_RowSetEqualsLegacyOnJoin(t *testing.T) {
	pool := logsLookupRealDBPool(t)

	const wideFrom, wideTo = "'2000-01-01'", "'2999-12-31'"
	newWide := SessionFamilyTurnsSourceWindowedSQL(wideFrom, wideTo)
	oldWide := "(" + legacyWindowedOnJoinSQL(wideFrom, wideTo) + ")"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var total int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+newWide).Scan(&total); err != nil {
		t.Fatalf("count new source: %v", err)
	}
	if total == 0 {
		t.Skip("真库里会话族没有行——空集对空集不构成证据")
	}
	newerOnly, olderOnly := exceptBothWays(t, pool, newWide, oldWide)
	if newerOnly != 0 || olderOnly != 0 {
		t.Fatalf("整表窗口：点查形态与 ON 子句形态行集不等价（new-only=%d, old-only=%d, total=%d）",
			newerOnly, olderOnly, total)
	}

	// 最近有数据的一天：取 session_turns 的最大 ts 所在的（UTC）日历日。
	var maxTS time.Time
	if err := pool.QueryRow(ctx, "SELECT max(ts) FROM public.session_turns").Scan(&maxTS); err != nil || maxTS.IsZero() {
		t.Skipf("session_turns 无数据（err=%v），跳过单日窗口对拍", err)
	}
	// 月份界放宽到首尾两个月分区之间（覆盖 max_ts 所在分区）；ts 界由调用方的
	// WHERE 负责，这里只对拍源本身在「单分区」形态下的行集。
	dayFrom := "'" + maxTS.UTC().AddDate(0, 0, -1).Format("2006-01-02") + "'"
	dayTo := "'" + maxTS.UTC().Format("2006-01-02") + "'"
	newDay := SessionFamilyTurnsSourceWindowedSQL(dayFrom, dayTo)
	oldDay := "(" + legacyWindowedOnJoinSQL(dayFrom, dayTo) + ")"
	var dayRows int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+newDay).Scan(&dayRows); err != nil {
		t.Fatalf("count day source: %v", err)
	}
	if dayRows == 0 {
		t.Skip("最近一天窗口内无行——空集对空集不构成证据")
	}
	newerOnly, olderOnly = exceptBothWays(t, pool, newDay, oldDay)
	if newerOnly != 0 || olderOnly != 0 {
		t.Fatalf("单日窗口：点查形态与 ON 子句形态行集不等价（new-only=%d, old-only=%d, rows=%d）",
			newerOnly, olderOnly, dayRows)
	}
}

// TestSessionFamilyProbeArmLookupSQL_RowSetEqualsSource 对拍探测臂的两种形态。
// 真库没有探测数据时空集对空集 ⇒ 显式跳过（不计为通过）；有数据的环境
// （211 生产只读）另由运维验证脚本对拍。
func TestSessionFamilyProbeArmLookupSQL_RowSetEqualsSource(t *testing.T) {
	pool := logsLookupRealDBPool(t)
	const from, to = "'2000-01-01'", "'2999-12-31'"
	lookup := SessionFamilyProbeArmLookupSQL(from, to)
	src := SessionFamilyProbeArmSourceSQL(from, to)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var total int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+src).Scan(&total); err != nil {
		t.Fatalf("count probe arm: %v", err)
	}
	if total == 0 {
		t.Skip("真库里没有带 fact 的探测行——空集对空集不构成证据")
	}
	newerOnly, olderOnly := exceptBothWays(t, pool, lookup, src)
	if newerOnly != 0 || olderOnly != 0 {
		t.Fatalf("探测臂点查形态与聚合形态行集不等价（lookup-only=%d, source-only=%d, total=%d）",
			newerOnly, olderOnly, total)
	}
}
