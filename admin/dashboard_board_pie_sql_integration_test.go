//go:build integration

package admin

// dashboard_board_pie_sql_integration_test.go
//
// 真库门：看板饼图主路径的 SQL 必须在**真实 PostgreSQL** 上、且以
// **预备语句**（prepared statement）的形式**可执行**。
//
// ── 为什么需要这道门（★ 一次真实生产事故）──────────────────────────────
//
// 2026-11-24 真源统一轮上线后，饼图主路径（读请求级日志）**每次调用都报错**：
//
//	ERROR: input of anonymous composite types is not implemented (SQLSTATE 0A000)
//	LOCATION: record_in, rowtypes.c:103
//
// 后果：载荷带 degraded ⇒ 页面静默退回预聚合维表 ⇒ 「切『含探测』时饼图不动」
// 的老症状原封不动地回来了，而**部署脚本全绿、单测全绿、psql 手测全绿**。
//
// 根因**不是数据，是协议**：初版用
//
//	CROSS JOIN LATERAL (VALUES …) AS d(dim_type, dim_key)
//
// 展开 7 个维度。生产走 pgx = **扩展协议 + 预备语句**；在通用计划
// （generic plan）下，PG 必须把 LATERAL 子查询里对外层的引用参数化，而外层是
// **视图** RTE（request_logs_with_current_month）⇒ 整行引用是**匿名 record 类型**
// ⇒ 生成 RECORD 类型的 Param ⇒ 执行期调用 record_in ⇒ 报错。
//
// ★★ 它**非确定性**：同一条 SQL 有时过、有时炸（取决于当次选通用/定制计划），
// ★★ 而 **psql 简单协议永远不会触发** ⇒ ★★ 用 psql 手测根本测不出来；
// ★★ pgxmock 只比对查询字符串 ⇒ ★★ 单测也测不出来。
//
// ⇒ 所以这道门必须同时具备三件事：① 真 PG；② **预备语句**；③ **强制通用计划**。
//
// ── ★★★ 这道门能抓什么、不能抓什么（★ 2026-11-24 实测订正）★★★ ────────
//
// ★★★ **能抓**：★
// ★★★   ① SQL 在**真实 PostgreSQL** 上根本执行不了（列名错、类型错、视图缺列、
// ★★★      语法错）——★ pgxmock 只比字符串，对这些**完全失明**。
// ★★★   ② 维度展开形态回退（形状断言，见下面那句「交给 PG 的语句里不得有 LATERAL」）。
// ★★★
// ★★★ **不能抓**：★ ★★ 「计划选择相关」的那类偶发失败。★★★
// ★★★   实测：★ 同一份 SQL、同一组参数、同一套会话设置，★ **时炸时不炸**
// ★★★   （生产上连续炸 5 次；随后 psql 连续 8 次全过）。★ ★★ 原因是 ★★
// ★★★   PG 为这条查询**选出来的计划**时好时坏（统计信息/并行度/成本估算在变）
// ★★★   ⇒ ★★ 坏的计划的特征就是「需要给视图外层生成 RECORD 类型 Param」。
// ★★★   ⇒ ★★ 所以：**执行型门对这一条没有确定性**，★ ★★ 别把「这次跑过了」
// ★★★   ★★ 当成「这条 SQL 没问题」（★ 这正是生产翻车时我自己的盲区）。
// ★★★
// ★★★ ⇒ **确定性守卫是形状门**（admin/dashboard_board_truth_source_test.go
// ★★★    门 ⑦）：★ 维度展开**不得**用相关 LATERAL 子查询 ⇒ ★★ 结构上排除
// ★★★    「视图外层整行引用 ⇒ RECORD Param」这条路 ⇒ ★★ 不依赖计划、不依赖时机。
//
// ── 跑法 ───────────────────────────────────────────────────────────────
//
//	TEST_DATABASE_URL='postgres://postgres@127.0.0.1:5432/llm_gateway' \
//	  go test -tags=integration ./admin/ -run TestBoardPieSQL
//
// 未设 TEST_DATABASE_URL 时 Skip —— 但 Skip **不构成**「SQL 能执行」的证据。
//
// ★ 只读：全程只有 SELECT / SET ⇒ 可以安全地指向生产库取证。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/maas"
)

// TestBoardPieSQLExecutesAsPreparedStatement 核饼图 SQL 在**预备语句 + 通用计划**下可执行。
func TestBoardPieSQLExecutesAsPreparedStatement(t *testing.T) {
	pool := boardProbePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// ★ 必须用同一条连接：plan_cache_mode 是会话级设置。
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("取连接失败：%v", err)
	}
	defer conn.Release()

	// ★★★ 强制通用计划：★★★ 生产踩的就是这条路径。
	// ★★★ 不强制的话，本门会**时绿时红**（取决于 PG 当次选了通用还是定制计划）
	// ★★★ ⇒ 那样的门等于没有门。
	if _, err := conn.Exec(ctx, "SET plan_cache_mode = force_generic_plan"); err != nil {
		t.Fatalf("设置 plan_cache_mode 失败：%v", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SET plan_cache_mode = auto")
	}()

	tr := boardTimeRange{
		Start: time.Now().UTC().Add(-7 * 24 * time.Hour),
		End:   time.Now().UTC(),
	}
	where, args := boardLogsWhereProbeScope(tr, "r", "", false)
	where += " AND r.request_status IN ('success','failure','rate_limited')"
	sql := boardPieFactsSQL(where, maas.RequestLogCreditsSQL("r", true))

	// ★★★ 确定性形状断言：★ 交给 PG 的语句里**不得**有相关 LATERAL 子查询。
	// ★★★ ★★ 这是本文件里**唯一确定性**的那条断言（执行类断言会因计划选择而时绿时红）。
	if strings.Contains(strings.ToUpper(sql), "LATERAL") {
		t.Fatalf("★ 交给 PG 的饼图 SQL 里出现了 LATERAL（相关子查询）⇒\n"+
			"★ ★★ 它在计划选择不当时会生成 RECORD 类型 Param ⇒ 报「匿名复合类型」(0A000)\n"+
			"★ ★★ ⇒ 饼图每次调用都降级。★ ★★ 修法：unnest(ARRAY[…]) + CASE d.dim_type。\n"+
			"★ SQL:\n%s", sql)
	}

	// ★★★ 用**重复 Query** 而不是手写 PREPARE：★★ pgx v5 的默认执行模式是
	// ★★★ QueryExecModeCacheStatement ⇒ 它会把语句**预备并按连接缓存** ⇒
	// ★★★ 同一连接上第 2 次起就在复用那条预备语句 ⇒ ★★★ 与生产**同一条路**。
	// ★★★ 连续执行 6 次：PG 的计划缓存前 5 次用定制计划，之后才可能转通用计划 ⇒
	// ★★★ 次数不够就**测不到**那条路径（这也是「时绿时红」的来源之一）。
	for i := 1; i <= 6; i++ {
		rows, err := conn.Query(ctx, sql, args...)
		if err != nil {
			if strings.Contains(err.Error(), "anonymous composite") {
				t.Fatalf("★ 饼图 SQL 以预备语句（第 %d 次）执行失败：%v\n"+
					"★ ★★ 「匿名复合类型」= 生产实测（2026-11-24，211）的那个错：\n"+
					"★ ★★ LATERAL 展开维度 + 视图外层 ⇒ 通用计划下生成 RECORD 类型 Param。\n"+
					"★ ★★ 后果：饼图每次调用都降级 ⇒ 切「含探测」时饼图不动。\n"+
					"★ SQL:\n%s", i, err, sql)
			}
			t.Fatalf("★ 饼图 SQL 以预备语句（第 %d 次）执行失败：%v\n★ SQL:\n%s", i, err, sql)
		}
		n := 0
		for rows.Next() {
			n++
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("★ 饼图 SQL 取数失败（第 %d 次）：%v", i, err)
		}
		rows.Close()
		if i == 1 && n == 0 {
			// ★ 空结果不算失败（库里可能确实没有数据），但要有交代。
			t.Logf("★ 饼图 SQL 执行成功，但窗口内没有数据行（生产库有空窗时属正常）。")
		}
	}
}

// TestBoardPieSQLNoAnonymousCompositeParam 额外钉一条：**不得**依赖会生成
// RECORD 类型 Param 的形状（即相关子查询 / LATERAL）。
//
// ★ 这条门与门 ⑦（单测层）互为犄角：★ 门 ⑦ 查「SQL 文本里有没有 LATERAL」，
// ★ 这条查「执行期有没有踩到 record_in」——★ ★★ 两者都能被变异臂证伪。
func TestBoardPieSQLNoAnonymousCompositeParam(t *testing.T) {
	pool := boardProbePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("取连接失败：%v", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SET plan_cache_mode = force_generic_plan"); err != nil {
		t.Fatalf("设置 plan_cache_mode 失败：%v", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SET plan_cache_mode = auto")
	}()

	tr := boardTimeRange{
		Start: time.Now().UTC().Add(-7 * 24 * time.Hour),
		End:   time.Now().UTC(),
	}
	where, args := boardLogsWhereProbeScope(tr, "r", "", false)
	where += " AND r.request_status IN ('success','failure','rate_limited')"
	sql := boardPieFactsSQL(where, maas.RequestLogCreditsSQL("r", true))

	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		if strings.Contains(err.Error(), "anonymous composite") {
			t.Fatalf("★ 饼图 SQL 触发了「匿名复合类型」错误 ⇒\n"+
				"★ ★★ 生产实测（2026-11-24，211）：★ 这会让饼图每次调用都降级，\n"+
				"★ ★★ 而单测/psql 都测不出来（见本文件头注）。\n"+
				"★ ★★ 修法：维度展开改成 unnest(ARRAY[…]) + CASE d.dim_type。\n"+
				"★ SQL:\n%s", sql)
		}
		t.Fatalf("★ 饼图 SQL 执行失败：%v", err)
	}
	rows.Close()
}

// TestBoardPieSQLExecutesAsExplicitPrepare 走**显式 PREPARE（带参数类型）**这条路。
//
// ★★ 为什么必须多走这一条：★★
// ★★ pgx 的默认执行模式（QueryExecModeCacheStatement）走的是「Parse 不带参数类型
// ★★ ⇒ PG 自己推断 ⇒ 前几次走定制计划」⇒ ★★ LATERAL 形态在这条路上**不炸**
// ★★ （本门第一次变异自证就实测到了：7 维 LATERAL 在 pgx 路径下全部 PASS）。
// ★★ 而「PREPARE stmt(timestamptz, timestamptz) AS …」这条路上，PG 从第一刻就用
// ★★ **通用计划** ⇒ LATERAL 形态**必炸**（实测：0A000 匿名复合类型）。
// ★★ ⇒ 只测 pgx 路径的门**没有牙**；两条都测才既贴生产又能证伪。
func TestBoardPieSQLExecutesAsExplicitPrepare(t *testing.T) {
	pool := boardProbePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("取连接失败：%v", err)
	}
	defer conn.Release()

	tr := boardTimeRange{
		Start: time.Now().UTC().Add(-7 * 24 * time.Hour),
		End:   time.Now().UTC(),
	}
	where, args := boardLogsWhereProbeScope(tr, "r", "", false)
	where += " AND r.request_status IN ('success','failure','rate_limited')"
	sql := boardPieFactsSQL(where, maas.RequestLogCreditsSQL("r", true))

	// ★ 显式声明参数类型 ⇒ PG 直接走通用计划（正是暴露 LATERAL 缺陷的那条路）。
	if _, err := conn.Exec(ctx,
		"PREPARE board_pie_explicit(timestamptz, timestamptz) AS "+sql); err != nil {
		t.Fatalf("★ 饼图 SQL 的 PREPARE 失败：%v\n★ SQL:\n%s", err, sql)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "DEALLOCATE board_pie_explicit")
	}()

	// ★ EXECUTE 是**工具命令**：它的参数必须是**常量表达式**，不能是查询参数
	// ★ （pgx 传 $1/$2 会得到 "mismatched param and argument count"）
	// ★ ⇒ 这里把窗口起止内联成字面量。
	_ = args
	execSQL := "EXECUTE board_pie_explicit('" + tr.Start.UTC().Format(time.RFC3339) +
		"'::timestamptz, '" + tr.End.UTC().Format(time.RFC3339) + "'::timestamptz)"
	for i := 1; i <= 3; i++ {
		if _, err := conn.Exec(ctx, execSQL); err != nil {
			if strings.Contains(err.Error(), "anonymous composite") {
				t.Fatalf("★ 饼图 SQL 在通用计划下触发「匿名复合类型」错误（第 %d 次）：%v\n"+
					"★ ★★ 这正是 2026-11-24 生产事故的那个错（0A000）。\n"+
					"★ ★★ 后果：饼图每次调用都降级 ⇒ 切「含探测」时饼图不动。\n"+
					"★ ★★ 修法：维度展开用 unnest(ARRAY[…]) + CASE d.dim_type（无相关子查询）。\n"+
					"★ SQL:\n%s", i, err, sql)
			}
			t.Fatalf("★ 饼图 SQL 的 EXECUTE 失败（第 %d 次）：%v\n★ SQL:\n%s", i, err, sql)
		}
	}
}
