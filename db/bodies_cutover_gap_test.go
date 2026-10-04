//go:build !integration

package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newBodiesCutoverPool 按 db 包既有门控开一个真库连接。
//
// ⚠ **没有 TEST_DATABASE_URL 时必须 t.Skip 而不是当 0 处理。**
// 少了这一行，这道门在没设环境变量的 CI 上会「测到 0 条 v1 turn」
// ⇒ would_be_lost = 0 ⇒ **绿**，而它一次都没量过东西。
// 「量具没跑」与「量到 0」在退出码上不可区分——这是本项目反复踩的那类。
func newBodiesCutoverPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— " +
			"⚠ 这里必须 skip 而不是当 0：否则这道门会在没连库的机器上「量到 0」而变绿。")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// bodies 切换的**数据级**前置条件（审计 §9.228）。
//
// # 它问的问题与 admin 侧那道 bodies 门不同
//
//	admin/request_logs_bodies_retirement_gate_test.go
//	    问：「每个读 v1 bodies 腿的文件，是否都被逐点评估并登记了？」（流程）
//	本文件
//	    问：「session_bodies 能不能替代 request_logs_bodies？」（数据）
//
// 流程门永远绿不了（26 个文件未评估），而**数据门是可能变绿的**——
// 当回填补齐之后。所以它们不是重复：前者决定「能不能开始切」，
// 后者决定「切了会不会丢东西」。
//
// # 为什么这道门是**故意红**的，以及它红在哪个数上
//
// 实测（本地库，2026-10-05，808,556 个带会话头的 v1 turn）：
//
//	in session_bodies            770,209  (95.26%)
//	in request_logs_bodies       742,472  (91.81%)
//	v1 有、session 没有（会丢）    29,691
//	两边都没有                    8,656
//
// ⚠ **两张表不是包含关系，是部分不相交。**
// 「session_bodies 覆盖 95%」这句话会让人以为切过去只丢 5%——
// 实际是 29,691 个**具体的消息**会变空，同时另外 27,737 个会开始可用。
// 这不是「略有损失」，是**换了一批消息**。
//
// 尾部比平均值更能说明问题（会话级，742,767 个会话）：
//
//	会丢至少一条正文的会话   11,783  (1.59%)
//	单会话最多丢              1,365  ⚠
//	单会话最多「得救」           22
//
// ⇒ 一次切换的**收益上界是 22 条正文，损失上界是 1,365 条**。
// 在这个分布下做无灰度的直接切换，风险与收益完全不成比例。
//
// # 所以本门要什么
//
// 判据只有一条：**`v1 有而 session 没有` 必须为 0**。
// 那是「切过去会真的丢东西」的精确集合；其它数（命中率、命中率百分比）
// 都是它的推论，不作为判据——
// **一个会让人以为「只丢 5%」的百分比，不该被写成门。**

// bodiesGap 是 bodies 两表之间的差异测量。
type bodiesGap struct {
	// V1Turns 是参与测量的 v1 turn 数（带会话头、有 request_id）。
	V1Turns int64
	// InSession 是 session_bodies 命中的 turn 数。
	InSession int64
	// InV1 是 request_logs_bodies 命中的 turn 数。
	InV1 int64
	// WouldBeLost 是**切换会真的丢正文**的 turn 数：v1 有、session 没有。
	// 这是唯一的判据。
	WouldBeLost int64
	// EmptyInBoth 是两边都没有的 turn 数。切换不丢任何东西（现状也一样空）。
	EmptyInBoth int64
}

// BodiesBlocksCutover 返回「bodies 切换是否被数据缺口阻塞」，以及阻塞原因。
//
// 抽成纯函数是为了能做双向对照：只看「gap==0 时返回 false」的话，
// 一个恒返回 false 的函数也能让门变绿。
func BodiesBlocksCutover(g bodiesGap) (blocked bool, why string) {
	// ⚠ **顺序是承重的，而且这条是变异验证逼出来的。**
	//
	// 第一版只有 `WouldBeLost > 0` 一条判据。于是「总体被改窄成 0 条」
	// （口径写错、库是空的、request_id 被过滤掉）会得到
	// WouldBeLost = 0 ⇒ **判为不阻塞 ⇒ 门变绿**。
	//
	// 而这道门是**真库门**，最容易被改坏的就是 SQL 里的总体口径。
	// 「一条 v1 turn 都没量到」和「量过了，没有缺口」在退出码上完全一样，
	// 差别只在有没有人记得看那行日志。
	//
	// ⇒ 量到 0 条本身就是阻塞条件。**先判它，再判缺口。**
	if g.V1Turns == 0 {
		return true, "没有量到任何 v1 turn —— 这不是「无缺口」，是口径写错或库是空的"
	}
	if g.WouldBeLost > 0 {
		return true, "v1 有而 session_bodies 没有的正文尚未补齐"
	}
	return false, ""
}

// measureBodiesGapSQL 是上面那个测量的 SQL。
//
// ⚠ **口径钉死在这里，不要在调用点改写**：
//   - 总体 = `request_logs` 里 `request_id IS NOT NULL` 且有会话头的 turn。
//     这与 bodies 的实际用法一致：export/compare/summarizer 都是在
//     会话族的 turn 列表上按 request_id 去 join bodies。
//   - 两侧都按 **DISTINCT request_id** 计，避免 bodies 表里有重复行时
//     把命中数放大（放大会让 would_be_lost 变成负数或 0，而那是假的干净）。
const measureBodiesGapSQL = `
WITH v1 AS (
  SELECT request_id, gw_session_id FROM request_logs
  WHERE request_id IS NOT NULL AND gw_session_id IS NOT NULL AND gw_session_id <> ''
), sb AS (SELECT DISTINCT request_id FROM session_bodies),
     rb AS (SELECT DISTINCT request_id FROM request_logs_bodies)
SELECT
  count(*)::bigint AS v1_turns,
  count(*) FILTER (WHERE sb.request_id IS NOT NULL)::bigint AS in_session,
  count(*) FILTER (WHERE rb.request_id IS NOT NULL)::bigint AS in_v1,
  count(*) FILTER (WHERE rb.request_id IS NOT NULL AND sb.request_id IS NULL)::bigint AS would_be_lost,
  count(*) FILTER (WHERE rb.request_id IS NULL AND sb.request_id IS NULL)::bigint AS empty_in_both
FROM v1
LEFT JOIN sb ON sb.request_id = v1.request_id
LEFT JOIN rb ON rb.request_id = v1.request_id
`

// measureBodiesSessionTailSQL 给出**会话级**的分布。
//
// ⚠ 为什么必须看分布而不是均值：一个「1.59% 的会话会丢东西」的结论
// 若不给上界，读者无法判断这 1.59% 是不是「每个会话少一条正文」
// （可接受）还是「有一个会话少了 1,365 条」（不可接受）。实测是后者。
const measureBodiesSessionTailSQL = `
WITH v1 AS (
  SELECT request_id, gw_session_id FROM request_logs
  WHERE request_id IS NOT NULL AND gw_session_id IS NOT NULL AND gw_session_id <> ''
), sb AS (SELECT DISTINCT request_id FROM session_bodies),
     rb AS (SELECT DISTINCT request_id FROM request_logs_bodies)
SELECT
  count(*)::bigint AS sessions,
  count(*) FILTER (WHERE would_lose > 0)::bigint AS sessions_that_lose,
  COALESCE(max(would_lose), 0)::bigint AS worst_lose,
  COALESCE(max(would_gain), 0)::bigint AS worst_gain
FROM (
  SELECT v1.gw_session_id,
         count(*) FILTER (WHERE rb.request_id IS NOT NULL AND sb.request_id IS NULL) AS would_lose,
         count(*) FILTER (WHERE sb.request_id IS NOT NULL AND rb.request_id IS NULL) AS would_gain
  FROM v1
  LEFT JOIN sb ON sb.request_id = v1.request_id
  LEFT JOIN rb ON rb.request_id = v1.request_id
  GROUP BY v1.gw_session_id
) t
`

// TestBodiesCutoverGapBlocksCutover 是数据门（**故意红**，真库）。
//
// 与 admin 侧那道 bodies 门同一个 package 家族但**不同问题**：
// 那道问「谁读过、评估过没有」，这道问「切了会不会丢正文」。
func TestBodiesCutoverGapBlocksCutover(t *testing.T) {
	pool := newBodiesCutoverPool(t)
	ctx := context.Background()

	var g bodiesGap
	if err := pool.QueryRow(ctx, measureBodiesGapSQL).Scan(
		&g.V1Turns, &g.InSession, &g.InV1, &g.WouldBeLost, &g.EmptyInBoth,
	); err != nil {
		t.Fatalf("measure bodies gap: %v", err)
	}

	var sessions, sessionsThatLose, worstLose, worstGain int64
	if err := pool.QueryRow(ctx, measureBodiesSessionTailSQL).Scan(
		&sessions, &sessionsThatLose, &worstLose, &worstGain,
	); err != nil {
		t.Fatalf("measure bodies session tail: %v", err)
	}

	t.Logf("v1 turns=%d  in session_bodies=%d (%.2f%%)  in request_logs_bodies=%d (%.2f%%)",
		g.V1Turns, g.InSession, bodiesPct(g.InSession, g.V1Turns), g.InV1, bodiesPct(g.InV1, g.V1Turns))
	t.Logf("⚠ 两表**部分不相交**：v1 有而 session 没有 = %d；两边都没有 = %d", g.WouldBeLost, g.EmptyInBoth)
	t.Logf("会话级：%d 个会话中 %d 个（%.2f%%）会丢至少一条正文；单会话最多丢 %d，最多得救 %d",
		sessions, sessionsThatLose, bodiesPct(sessionsThatLose, sessions), worstLose, worstGain)

	blocked, why := BodiesBlocksCutover(g)
	if blocked {
		t.Errorf("bodies 切换被数据缺口阻塞：%s（%d 条 v1 有而 session_bodies 没有的正文）\n\n"+
			"为什么这不是一个百分比问题：session_bodies 命中 %.2f%%、"+
			"request_logs_bodies 命中 %.2f%%，**看起来**只差几个百分点；"+
			"但两表是**部分不相交**的 —— 切过去会具体地丢 %d 条正文，"+
			"同时另有 %d 条从「空」变「有」。\n"+
			"会话级尾部更说明问题：%d 个会话（%.2f%%）会至少丢一条，"+
			"而单会话最多丢 **%d** 条、最多只「得救」**%d** 条。\n"+
			"⇒ 一次切换的收益上界（%d）远小于损失上界（%d），"+
			"在这个分布下无灰度直切风险与收益完全不成比例。\n\n"+
			"处置顺序（**不要先改读方**）：\n"+
			"  1. 补齐这 %d 条缺口：go run ./cmd/tools/backfill_session_bodies（--use-hot 可先跑热分区）\n"+
			"  2. 本门转绿（would_be_lost = 0）后再谈把 admin/session_export.go、\n"+
			"     admin/session_compare.go 的 bodies 腿改指 session_bodies\n"+
			"  3. 注意列名不同：v1 是 request_body/response_body，"+
			"session 侧是 request_delta/response_delta ⇒ **不是机械替换**，"+
			"需要显式列映射；db 包至今**没有** bodies 源的 SQL helper。\n\n"+
			"⚠ 与 admin/request_logs_bodies_retirement_gate_test.go 不是重复：\n"+
			"那道问「谁读过、评估过没有」（26 个文件，流程），本道问「切了会不会丢」（数据）。\n"+
			"流程门永远不会先变绿，数据门才可能在回填后变绿。",
			why, g.WouldBeLost,
			bodiesPct(g.InSession, g.V1Turns), bodiesPct(g.InV1, g.V1Turns),
			g.WouldBeLost, g.InSession-g.InV1,
			sessionsThatLose, bodiesPct(sessionsThatLose, sessions), worstLose, worstGain,
			worstGain, worstLose, g.WouldBeLost)
		return
	}
	t.Logf("【预期红已解除】would_be_lost = 0，bodies 切换在数据层面可以开始评估。")
}

// bodiesPct 算百分比，**保留分母为 0 的情况**。
//
// ⚠ 命名不叫 pct：db 包里已经有一个 pct 了（别的文件），同名会让本文件编译失败。
// 这不是风格问题——**同名函数在 Go 里必须恰好一个**，而"换个名字"是唯一解法。
// 复用别人的 pct 反而更糟：它的分母语义未必是「分母为 0 返回 0」。
func bodiesPct(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

// TestBodiesBlocksCutover_PureFunction 是纯函数的双向对照。
//
// ⚠ 没有对照组的话，一个恒返回 false 的 BodiesBlocksCutover 也能让上面那道门变绿。
func TestBodiesBlocksCutover_PureFunction(t *testing.T) {
	for _, tc := range []struct {
		name      string
		gap       bodiesGap
		wantBlock bool
	}{
		{"有缺口：阻塞", bodiesGap{V1Turns: 100, InSession: 95, InV1: 90, WouldBeLost: 3}, true},
		{"无缺口：不阻塞", bodiesGap{V1Turns: 100, InSession: 100, InV1: 100}, false},
		// 反向对照：**session 覆盖比 v1 高但仍要阻塞**。
		// 只看 InSession >= InV1 的实现会在这里放行 ——
		// 而那正是本门存在的场景（770,209 > 742,472，却仍丢 29,691）。
		{"session 覆盖更高但仍有丢：仍阻塞", bodiesGap{V1Turns: 100, InSession: 96, InV1: 92, WouldBeLost: 1}, true},
		// 两边都空不算缺口：现状也是空的，切换不丢任何东西。
		{"两边都空：不阻塞", bodiesGap{V1Turns: 100, InSession: 91, InV1: 91, EmptyInBoth: 9}, false},
		// ⚠ 这条是变异验证逼出来的：量到 0 条**必须**阻塞。
		// 少了它，SQL 里总体口径被改窄（`WHERE false`）会让这道门变绿，
		// 而它一次都没量过东西 —— 真库门最容易被改坏的就是总体口径。
		{"总体为 0 条：阻塞（量具没量到，不是无缺口）", bodiesGap{}, true},
	} {
		got, _ := BodiesBlocksCutover(tc.gap)
		if got != tc.wantBlock {
			t.Errorf("%s: BodiesBlocksCutover(%+v) = %v，期望 %v",
				tc.name, tc.gap, got, tc.wantBlock)
		}
	}
}
