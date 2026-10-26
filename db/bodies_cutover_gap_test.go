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
//	    问：「session_bodies 能不能替代 _request_logs_bodies？」（数据）
//
// 流程门永远绿不了（26 个文件未评估），而**数据门是可能变绿的**——
// 当回填补齐之后。所以它们不是重复：前者决定「能不能开始切」，
// 后者决定「切了会不会丢东西」。
//
// # 为什么这道门是**故意红**的，以及它红在哪个数上
//
// ⚠ 下面这组数字来自 §9.228 的**第一个总体**（_request_logs 口径），
// 已被 §9.229/D33 **撤回**（acc0511b0 的提交信息与 6d5b9196f 的就地更正）：
// 总体应按被影响 SQL 的 FROM 选（session_turns 族）。数字留在这里只作
// 历史测量记录，不再作为本门论据；现行判据用 bodiesGap.SessionTurns
// 作总体（见下方 struct 注释）。
//
// 实测（本地库，2026-10-05，808,556 个带会话头的 v1 turn）：
//
//	in session_bodies            770,209  (95.26%)
//	in _request_logs_bodies       742,472  (91.81%)
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
	// HasV1Body 是其中在 _request_logs_bodies 里有正文的。
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
// ⚠⚠⚠ **R69 轮：这条 SQL 已退役，且它引用的 v1 族关系已不存在** ——
// `_request_logs_bodies` 随迁移 885（真删表）+ 890（退族）被删除
// ⇒ **现在跑它必然 42P01** ⇒ 它**不能再被用来量任何东西**。
// 保留它只作**历史测量记录**（§9.228/§9.229 的口径与撤回过程）。
// ⇒ 若将来要重新量「会话 bodies 覆盖度」，**必须重写口径**，不能复用这条。
//
// ⚠ **口径钉死在这里，不要在调用点改写**：
//   - 总体 = `_request_logs` 里 `request_id IS NOT NULL` 且有会话头的 turn。
//     这与 bodies 的实际用法一致：export/compare/summarizer 都是在
//     会话族的 turn 列表上按 request_id 去 join bodies。
//   - 两侧都按 **DISTINCT request_id** 计，避免 bodies 表里有重复行时
//     把命中数放大（放大会让 would_be_lost 变成负数或 0，而那是假的干净）。
const measureBodiesGapSQL = `
WITH st AS (SELECT request_id, session_id FROM session_turns),
     sb AS (SELECT DISTINCT request_id FROM session_bodies),
     rb AS (SELECT DISTINCT request_id FROM _request_logs_bodies)
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
  SELECT request_id, gw_session_id, is_auto_request FROM _request_logs
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
     rb AS (SELECT DISTINCT request_id FROM _request_logs_bodies)
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
     rb AS (SELECT DISTINCT request_id FROM _request_logs_bodies)
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

// TestBodiesCutoverV1ArmStaysRetired 是 bodies 切换的**现行**守卫。
//
// # R69 轮改瞄（2026-10-21）：这道门换过一次题目
//
// 原门 `TestBodiesCutoverGapBlocksCutover` 量的是
// 「**session_bodies 能不能替代 `_request_logs_bodies`**」——
// 即切换前先看数据缺口（`would_be_lost` 必须为 0）。
//
// ★ **那个题目已经不存在了**：v1 族随迁移 885（真删表）+ 890（退族）被退役，
// `_request_logs_bodies` **这个关系在真库里已经不存在** ⇒
// 原门的三个测量 SQL（`measureBodiesGapSQL` / `lossByDaySQL` /
// `measureBodiesSessionTailSQL`）与 `v1OnlyTurnsSQL` **全部 42P01**
// ⇒ ⇒ 这道门**恒红**，而那个红**与产品无关**：它守的那个东西已经被删了。
//
// ⇒ **同一族的问题在退役后换了形态**：
//
//	切换前：会不会丢正文？        ⇒ 量 `would_be_lost`
//	切换后：会不会**又切回去**？  ⇒ 断言 **v1 关系不存在**
//
// ⇒ 后者才是今天**唯一**还有意义的不变量：
// 一旦有人把 `_request_logs_bodies` / `request_logs_bodies_with_current_month`
// 重新建起来（某个迁移回滚、或带外 DDL），读面就会**重新分裂成两族**，
// 而 `admin/request_logs_bodies_retirement_gate_test.go`（流程门）
// 只问「谁读过、评估过没有」，**不问它还在不在**。
//
// ⇒ 所以本门现在钉两件事：
//
//	① v1 bodies 的两个关系名**必须都不存在**（退役完成）；
//	② 会话 bodies 源**必须可执行**（真跑一次 JOIN）⇒ 切过去取得到正文。
//
// ⚠ **② 允许 0 行**：空库上 0 行是**正常**的（不是「量具坏了」）——
// 本门量的是「源可不可执行」，不是「有没有数据」。
// （★ 这与 `db/session_bodies_source_test.go` 那道门分工不同：
// 那道门量**列名与类型**，本门量**关系退役状态与源可达性**。）
//
// # 判据量（切换前那道门的历史读数，仅作背景，不是判据）
//
// 实测（本地库，2026-10-05，808,556 个带会话头的 v1 turn）：
//
//	in session_bodies            770,209  (95.26%)
//	in _request_logs_bodies       742,472  (91.81%)
//	v1 有、session 没有（会丢）    29,691
//	两边都没有                    8,656
func TestBodiesCutoverV1ArmStaysRetired(t *testing.T) {
	pool := newBodiesCutoverPool(t)
	ctx := context.Background()

	// ① v1 bodies 的关系必须不存在（退役完成 ⇒ 缺口结构上不可能存在）。
	for _, rel := range []string{
		"_request_logs_bodies",
		"request_logs_bodies_with_current_month",
	} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT to_regclass('public.`+rel+`') IS NOT NULL`).Scan(&exists); err != nil {
			t.Errorf("探测 v1 bodies 关系 %q 是否存在：%v", rel, err)
			continue
		}
		if exists {
			t.Errorf("★ v1 bodies 关系 public.%s **又存在了** —— "+
				"v1 族已随迁移 885/890 退役（真删表）；它若被重新建起，"+
				"读面会重新分裂成两族（v1 / 会话），而这条正是那个切换的守卫。", rel)
		}
	}

	// ② 会话 bodies 源必须**可执行**（真跑一次 JOIN）。
	// ⚠ 0 行是允许的：这里量的是「源可达」，不是「有没有数据」。
	var legs, bodies int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::bigint,
		       count(rb.request_id)::bigint
		FROM public.session_turns st
		LEFT JOIN `+SessionFamilyBodiesSourceSQL()+` rb ON rb.request_id = st.request_id
	`).Scan(&legs, &bodies); err != nil {
		t.Fatalf("★ 会话 bodies 源**跑不起来**（%v）—— "+
			"它是导出/对比 API 的正文来源；源不可执行 ⇒ 那些 API 的正文全是空的。", err)
	}
	t.Logf("会话 bodies 源可执行：session_turns %d 行 ⇒ 正文命中 %d 行（0 行合法：空库）", legs, bodies)
	t.Logf("【结论】bodies 切换已完成且**不可静默回退**：v1 bodies 关系不存在，" +
		"会话 bodies 源可达 ⇒ 「切换会不会丢正文」在今天**结构上**恒为「不会」。")
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
//
// ★ **R69 轮说明**：上面那道门已经换成「v1 臂是否退役」的守卫
// （`TestBodiesCutoverV1ArmStaysRetired`），**不再**调用这个纯函数。
// ⇒ 本门保留的价值变成：**把这个判定逻辑（量到 0 条 = 阻塞，而不是「无缺口」）**
// 钉成一个**可离线回归**的契约 —— 那个坑（把「没量到」读成「没问题」）
// 与 v1 族无关，是**口径**上的坑，未来任何新的缺口测量都会再遇到。
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
