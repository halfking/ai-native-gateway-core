//go:build !integration

package db

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	// SessionTurns 是参与测量的会话族 turn 数。**这是正确的总体**（§9.229）：
	// 导出/对比 API 的 FROM 是 SessionFamilyTurnsForSessionSQL()，
	// 一个没有 session_turns 行的 turn 从来就不在它们的结果集里。
	SessionTurns int64
	// HasV1Body 是其中在 request_logs_bodies 里有正文的。
	HasV1Body int64
	// HasSessionBody 是其中在 session_bodies 里有正文的。
	HasSessionBody int64
	// WouldBeLost 是**切换会真的丢正文**的 turn 数：有 v1 正文、但没有 session_bodies 行。
	// 切换后这些 turn 仍在导出结果里，但正文变成 `{}`。
	// **这是唯一的判据。**
	WouldBeLost int64
	// WouldGain 不作为判据（方向与切换无关），只作信息。
	WouldGain int64
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
	if g.SessionTurns == 0 {
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
WITH st AS (SELECT request_id, session_id FROM session_turns),
     sb AS (SELECT DISTINCT request_id FROM session_bodies),
     rb AS (SELECT DISTINCT request_id FROM request_logs_bodies)
SELECT
  count(*)::bigint AS session_turns,
  count(*) FILTER (WHERE rb.request_id IS NOT NULL)::bigint AS has_v1_body,
  count(*) FILTER (WHERE sb.request_id IS NOT NULL)::bigint AS has_session_body,
  count(*) FILTER (WHERE rb.request_id IS NOT NULL AND sb.request_id IS NULL)::bigint AS would_be_lost,
  count(*) FILTER (WHERE rb.request_id IS NULL AND sb.request_id IS NOT NULL)::bigint AS would_gain
FROM st
LEFT JOIN sb ON sb.request_id = st.request_id
LEFT JOIN rb ON rb.request_id = st.request_id
`

// v1OnlyTurnsSQL 量的是**另一个量**，名字必须与判据分开：
// 「v1 有会话头的 turn 里，有多少根本没有对应的 session_turns 行」。
//
// ⚠ **它不是切换损失。** 这些 turn 早已不在导出/对比 API 的结果集里——
// 那两个 API 的 FROM 是 `SessionFamilyTurnsForSessionSQL()`，即会话族；
// 一个没有 session_turns 行的 turn **从来就没被它们 JOIN 到过**。
// 切不切 bodies 都看不见它。
//
// 第一版（§9.228.4）把这个量当成「切换会丢的正文」，报出 29,741 ——
// 见 §9.229 的撤回。那 29,691 绝大多数是 `is_auto_request = t` 的探针/自动流量，
// 按设计就不进会话族（§9.215 记过同一类排除）。
const v1OnlyTurnsSQL = `
WITH v1 AS (
  SELECT request_id, gw_session_id, is_auto_request FROM request_logs
  WHERE request_id IS NOT NULL AND gw_session_id IS NOT NULL AND gw_session_id <> ''
), st AS (SELECT DISTINCT request_id FROM session_turns)
SELECT
  count(*)::bigint AS v1_session_turns,
  count(*) FILTER (WHERE st.request_id IS NULL)::bigint AS v1_only,
  count(*) FILTER (WHERE st.request_id IS NULL AND v1.is_auto_request)::bigint AS v1_only_auto
FROM v1 LEFT JOIN st ON st.request_id = v1.request_id
`

// lossByDaySQL 把损失按天摊开。
//
// 理由：生产实测（2026-10-05）**全部 1,113 条损失都落在 2026-09-30 一天**
// ——即 `session_bodies` 覆盖起点之前的那一天；10-01 起为零。
// 只给总数的话，读者无法区分「系统性缺口」与「一个回填边界日」，
// 而两者的处置完全不同（前者要查写入路径，后者补一天即可）。
const lossByDaySQL = `
WITH st AS (SELECT request_id, ts FROM session_turns),
     sb AS (SELECT DISTINCT request_id FROM session_bodies),
     rb AS (SELECT DISTINCT request_id FROM request_logs_bodies)
SELECT st.ts::date AS day, count(*)::bigint AS would_lose
FROM st
JOIN rb ON rb.request_id = st.request_id
LEFT JOIN sb ON sb.request_id = st.request_id
WHERE sb.request_id IS NULL
GROUP BY 1 ORDER BY 1
`

// measureBodiesSessionTailSQL 给出**会话级**的分布。
//
// ⚠ 为什么必须看分布而不是均值：一个「1.59% 的会话会丢东西」的结论
// 若不给上界，读者无法判断这 1.59% 是不是「每个会话少一条正文」
// （可接受）还是「有一个会话少了 1,365 条」（不可接受）。实测是后者。
const measureBodiesSessionTailSQL = `
WITH st AS (SELECT request_id, session_id FROM session_turns),
     sb AS (SELECT DISTINCT request_id FROM session_bodies),
     rb AS (SELECT DISTINCT request_id FROM request_logs_bodies)
SELECT
  count(*)::bigint AS sessions,
  count(*) FILTER (WHERE would_lose > 0)::bigint AS sessions_that_lose,
  COALESCE(max(would_lose), 0)::bigint AS worst_lose
FROM (
  SELECT st.session_id,
         count(*) FILTER (WHERE rb.request_id IS NOT NULL AND sb.request_id IS NULL) AS would_lose
  FROM st
  LEFT JOIN sb ON sb.request_id = st.request_id
  LEFT JOIN rb ON rb.request_id = st.request_id
  GROUP BY st.session_id
) t
`

// TestBodiesCutoverGapBlocksCutover 是数据门（**故意红**，真库）。
//
// 与 admin 侧那道 bodies 门同一个 package 家族但**不同问题**：
// 那道问「谁读过、评估过没有」，这道问「切了会不会丢正文」。
func TestBodiesCutoverGapBlocksCutover(t *testing.T) {
	pool := newBodiesCutoverPool(t)
	ctx := context.Background()

	// 判据量：总体 = session_turns。
	var g bodiesGap
	if err := pool.QueryRow(ctx, measureBodiesGapSQL).Scan(
		&g.SessionTurns, &g.HasV1Body, &g.HasSessionBody, &g.WouldBeLost, &g.WouldGain,
	); err != nil {
		t.Fatalf("measure bodies gap: %v", err)
	}
	t.Logf("★ 判据量（总体=session_turns，%d 行）：有 v1 正文 %d（%.2f%%）、"+
		"有 session_bodies %d（%.2f%%）⇒ **切换会丢 %d 条**（%.4f%%）",
		g.SessionTurns, g.HasV1Body, bodiesPct(g.HasV1Body, g.SessionTurns),
		g.HasSessionBody, bodiesPct(g.HasSessionBody, g.SessionTurns),
		g.WouldBeLost, bodiesPct(g.WouldBeLost, g.SessionTurns))

	// 损失按天摊开：区分「系统性缺口」与「一个回填边界日」。
	rows, err := pool.Query(ctx, lossByDaySQL)
	if err != nil {
		t.Fatalf("loss by day: %v", err)
	}
	defer rows.Close()
	var days []string
	for rows.Next() {
		var d string
		var n int64
		if err := rows.Scan(&d, &n); err != nil {
			t.Fatalf("scan loss by day: %v", err)
		}
		days = append(days, fmt.Sprintf("%s=%d", d, n))
	}
	t.Logf("损失按天：%s", strings.Join(days, " "))

	// 会话级尾部。
	var sessions, sessionsThatLose, worstLose int64
	if err := pool.QueryRow(ctx, measureBodiesSessionTailSQL).Scan(
		&sessions, &sessionsThatLose, &worstLose,
	); err != nil {
		t.Fatalf("measure bodies session tail: %v", err)
	}
	t.Logf("会话级：%d 个会话中 %d 个（%.2f%%）会丢至少一条正文；单会话最多丢 %d",
		sessions, sessionsThatLose, bodiesPct(sessionsThatLose, sessions), worstLose)

	// ⚠ **另一个量，且不是损失**：v1 有会话头但没有 session_turns 行的 turn。
	// 它们**早已不在**导出/对比 API 的结果集里（那两个 API 的 FROM 是会话族），
	// 所以切不切 bodies 都看不见。把它算进损失是 §9.228 的口径错误（§9.229 撤回）。
	var v1Turns, v1Only, v1OnlyAuto int64
	if err := pool.QueryRow(ctx, v1OnlyTurnsSQL).Scan(&v1Turns, &v1Only, &v1OnlyAuto); err != nil {
		t.Fatalf("measure v1-only turns: %v", err)
	}
	t.Logf("（**不是切换损失**）v1 带会话头 %d 条，其中 %d 条没有 session_turns 行；"+
		"其中 %d 条是 is_auto_request=t（%.1f%%）—— 这些按设计不进会话族（§9.215）。"+
		"它们的去向是「退役 v1 后失去探针/自动流量的可审计性」，那是**另一个属主决定**。",
		v1Turns, v1Only, v1OnlyAuto, bodiesPct(v1OnlyAuto, maxInt64(v1Only, 1)))

	blocked, why := BodiesBlocksCutover(g)
	if blocked {
		t.Errorf("bodies 切换被数据缺口阻塞：%s\n\n"+
			"判据量：session_turns %d 条里有 %d 条「有 v1 正文、但没有 session_bodies 行」"+
			"（%.4f%%）。切换后这些 turn **仍在**导出/对比 API 的结果里，正文变成 `{}`，"+
			"而 API 不报错。\n"+
			"按天看：%s\n"+
			"（全部落在同一天 ⇒ 是**回填边界**而不是写入路径缺陷；跨多天 ⇒ 要查写入路径。）\n"+
			"会话级：%d/%d 个会话（%.2f%%）会至少丢一条，单会话最多丢 %d。\n\n"+
			"处置顺序（**不要先改读方**）：\n"+
			"  1. 先补齐缺口：go run ./cmd/tools/backfill_session_bodies"+
			"（--use-hot 可先跑热分区；逐天跑可只补边界那一天）\n"+
			"  2. 本门转绿（would_be_lost = 0）后再谈改 bodies 腿\n"+
			"  3. 列名不同：v1 是 request_body/response_body，会话侧是\n"+
			"     request_delta/response_delta ⇒ 需要显式列映射；"+
			"db 包至今**没有** bodies 源的 SQL helper\n"+
			"  4. 全程需灰度开关：失效形态是 COALESCE(…,'{}') ⇒ 导出照常成功、正文为空",
			why, g.SessionTurns, g.WouldBeLost, bodiesPct(g.WouldBeLost, g.SessionTurns),
			strings.Join(days, " "),
			sessionsThatLose, sessions, bodiesPct(sessionsThatLose, sessions), worstLose)
		return
	}
	t.Logf("【预期红已解除】would_be_lost = 0，bodies 切换在数据层面可以开始评估。")
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

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
		{"有缺口：阻塞", bodiesGap{SessionTurns: 100, HasV1Body: 90, HasSessionBody: 87, WouldBeLost: 3}, true},
		{"无缺口：不阻塞", bodiesGap{SessionTurns: 100, HasV1Body: 100, HasSessionBody: 100}, false},
		// ★ 真实生产场景：**session 侧覆盖比 v1 侧更高**（v1 已被 DROP 掉更早的分区），
		// 仍然要阻塞。只看 HasSessionBody >= HasV1Body 的实现会在这里放行 ——
		// 而生产实测正是 23,145 > 24,191 的反面：HasV1Body 24,191 > HasSessionBody 23,145。
		{"v1 侧覆盖更高但仍有丢：仍阻塞", bodiesGap{SessionTurns: 100, HasV1Body: 96, HasSessionBody: 95, WouldBeLost: 1}, true},
		// 总体里大部分 turn 根本没有 v1 正文（v1 早被轮转掉）时，
		// **不能**因此放行：那与「有缺口」是同一种门红。
		{"绝大多数 turn 无 v1 正文但有缺口：仍阻塞", bodiesGap{SessionTurns: 1000, HasV1Body: 24, HasSessionBody: 23, WouldBeLost: 1}, true},
		// 总体里大部分 turn 两边都没有：不是缺口。
		{"绝大多数 turn 两边都无正文：不阻塞", bodiesGap{SessionTurns: 1000, HasV1Body: 24, HasSessionBody: 24, WouldGain: 0}, false},
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
