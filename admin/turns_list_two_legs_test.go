// turns_list_two_legs_test.go —— `/api/admin/turns` 列表读点的【两腿形态】门
// （2026-10-29 · R181 轮第 ⑦ 项）。
//
// # 这道门钉什么
//
// 列表读点此前读 `public.session_turns_with_current_month`。该视图的 hot 臂带
// 一个**相关反连接**（把已晋级到父表的同键行从 hot 臂剔掉）：
//
//	FROM session_turns_hot hot
//	 WHERE NOT (EXISTS (SELECT 1 FROM session_turns archived
//	                    WHERE archived.tenant_id::text = hot.tenant_id::text
//	                      AND archived.request_id = hot.request_id))
//
// ★ 相关子查询的**内侧拿不到外层谓词** ⇒ ⇒ 内侧 Append 遍历**全部**月分区，
// ★ 每个分区按 (tenant_id::text) 索引定位后**扫该租户的全部索引项**、
// ★ 再用 `Filter: request_id` 过滤 ⇒ ★「每 hot 行扫该租户全部索引项」。
//
// ⇒ 本轮把列表读点换成【两腿形态】（internal/sessionfamily/turns_reads.go 的
//
//	TurnsListArmsSQL）：hot 腿 + 父表腿，谓词推进两臂内部，且**反连接的内侧
//	带 partition_date 界** ⇒ 内侧裁到窗口所在的 1 个分区。
//
// # 三道门（缺一不可）
//
//	① **形状门**：渲染结果不得引用视图；必须两腿齐全；谓词必须落在臂内部；
//	  反连接的内侧必须带 partition_date 界（★ 这是本轮收益的来源 ⇒ 少了它
//	  就是「换了壳没换里子」）。
//	② **等价门（真库）**：在同一窗口下，**视图形态**与**两腿形态**读出的
//	  行集必须逐行相等（★ 同一 REPEATABLE READ 快照）。
//	③ **前提门（真库）**：等价性依赖「同一 (tenant_id, request_id) 不会跨
//	  partition_date 出现在父表」⇒ 这个前提必须**当场为真**；不真即报红
//	  （★ 前提破了就必须把内侧的界拿掉，而不是让差异静默发生）。
package admin

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// ── ① 形状门 ───────────────────────────────────────────────────────────────

// turnsListTwoLegsCheckSQL 校验两腿形态的渲染结果，返回问题描述列表（空 = 合格）。
func turnsListTwoLegsCheckSQL(s string) []string {
	var problems []string

	if strings.Contains(s, "session_turns_with_current_month") {
		problems = append(problems, "渲染结果引用了 session_turns_with_current_month 视图 —— "+
			"它的 hot 腿带反连接，且相关子查询的内侧拿不到外层谓词 ⇒ 内侧遍历全部月分区")
	}
	for _, leg := range []string{
		"FROM public.session_turns_hot hot",
		"FROM public.session_turns p",
	} {
		if !strings.Contains(s, leg) {
			problems = append(problems, "缺两腿之一："+leg)
		}
	}
	if !strings.Contains(s, "UNION ALL") {
		problems = append(problems, "缺 UNION ALL —— 两腿形态退化成单腿 ⇒ 漏读已晋级的轮次")
	}
	// 反连接的内侧必须带 partition_date 界（★ 本轮收益的来源）。
	for _, bound := range []string{
		"archived.partition_date >= $1",
		"archived.partition_date <= $2",
	} {
		if !strings.Contains(s, bound) {
			problems = append(problems, "反连接内侧缺分区界："+bound+
				" —— 少了它，内侧仍遍历全部月分区（每 hot 行扫该租户全部索引项）")
		}
	}
	// 租户比较必须逐字沿用视图的形态（★ 与视图同源 ⇒ 不引入新的口径）。
	if !strings.Contains(s, "archived.tenant_id::text = hot.tenant_id::text") {
		problems = append(problems, "反连接的租户比较必须逐字沿用视图的 ::text 形态")
	}
	// 谓词必须落在臂内部（★ 不是留在外面 ⇒ 否则 planner 仍可能把内侧展开成全分区）。
	for _, pred := range []string{"hot.ts >= $3", "p.ts >= $3"} {
		if !strings.Contains(s, pred) {
			problems = append(problems, "谓词未推进臂内部："+pred)
		}
	}
	return problems
}

func TestTurnsListTwoLegs_Shape(t *testing.T) {
	now := time.Now().UTC()
	req := httptest.NewRequest("GET", "/api/admin/turns?tenant=t1&model=m1&provider=p1&status_code=200", nil)
	arms, _, argIdx := buildTurnsListArms(req, "t1", now.Add(-24*time.Hour), now, time.Time{}, "", 0)
	require.NotEmpty(t, arms)
	require.Positive(t, argIdx)

	problems := turnsListTwoLegsCheckSQL(arms)
	require.Empty(t, problems, "两腿形态的渲染不合格：%v", problems)

	// ★ 阳性对照：把形态改回「引用视图」，检测器必须判红（★ 否则这道门是恒真的）。
	bad := "FROM public.session_turns_with_current_month t WHERE 1=1"
	require.NotEmpty(t, turnsListTwoLegsCheckSQL(bad),
		"形状门必须能认出「引用视图」的坏形态（否则它恒绿）")
}

// TestTurnsListTwoLegs_PredicateIsInsideArms 钉住「谓词推进两臂内部」。
//
// 为什么单独一道：形状门看的是「有没有这些片段」，而这一道看的是
// **两条腿各自的谓词内容**（★ 两腿的列前缀不同 ⇒ 别名写错会 42703 或
// 悄悄退化成全表扫）。
func TestTurnsListTwoLegs_PredicateIsInsideArms(t *testing.T) {
	now := time.Now().UTC()
	req := httptest.NewRequest("GET", "/api/admin/turns?tenant=t1&model=m1", nil)
	arms, _, _ := buildTurnsListArms(req, "t1", now.Add(-24*time.Hour), now, time.Time{}, "", 0)

	hotIdx := strings.Index(arms, "FROM public.session_turns_hot hot")
	parentIdx := strings.Index(arms, "FROM public.session_turns p")
	require.Positive(t, hotIdx)
	require.Positive(t, parentIdx)

	hotLeg := arms[hotIdx:parentIdx]
	parentLeg := arms[parentIdx:]

	require.Contains(t, hotLeg, "hot.tenant_id = $5", "hot 腿必须带租户谓词")
	require.Contains(t, hotLeg, "hot.model = $6", "hot 腿必须带模型谓词")
	require.Contains(t, parentLeg, "p.tenant_id = $5", "父表腿必须带租户谓词")
	require.Contains(t, parentLeg, "p.model = $6", "父表腿必须带模型谓词")
	// ★ 两腿的参数编号必须**一致**（★ 共用同一批 $N ⇒ 少写一个就会错位）。
	require.Contains(t, hotLeg, "$6")
	require.Contains(t, parentLeg, "$6")
}

// ── ②③ 真库门 ─────────────────────────────────────────────────────────────

// TestTurnsListTwoLegs_NoCrossPartitionDuplicateKeys 是**前提门**。
//
// ★ 前提：同一个 (tenant_id, request_id) 不会跨 partition_date 出现在父表。
// ★ 若它不成立 ⇒ 反连接内侧的分区界会改变结果集（窗口内的 hot 行本该被
// ★ 剔掉，却因为同键行落在窗口外的分区而留下来）。
// ★ 三条依据说明正常路径不产生这种行：晋级逐字保 partition_date；写路径的
// ★ 幂等探针 ExistsByKeySQL 不带分区界；父表唯一键含 partition_date。
// ★ ⇒ 这个前提**当场可测**，所以必须测，而不是写在注释里。
func TestTurnsListTwoLegs_NoCrossPartitionDuplicateKeys(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	var dup int
	err := pool.QueryRow(ctx, `
		SELECT count(*)::int FROM (
			SELECT tenant_id, request_id
			FROM public.session_turns
			GROUP BY tenant_id, request_id
			HAVING count(DISTINCT partition_date) > 1
		) d`).Scan(&dup)
	require.NoError(t, err)
	require.Zero(t, dup,
		"父表里存在跨 partition_date 的同键行 ⇒ 反连接内侧的分区界会改变结果集；"+
			"此时必须去掉内侧的界（而不是让差异静默发生）")
}

// TestTurnsListTwoLegs_RealDB_Equivalence 是**等价门**。
//
// ★ 在同一个 REPEATABLE READ 快照内，把「视图形态」与「两腿形态」读出的
// ★ 行集逐行比较 ⇒ 必须完全相等。
// ★ 两侧都必须有数据（行数守卫）⇒ 否则显式 SKIP 并写明理由（★ 纪律：
// ★ 量具必须能自证「空集」不是「判定通过」）。
func TestTurnsListTwoLegs_RealDB_Equivalence(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	// 窗口：取库里有数据的一段时间（★ 两侧必须都有行 ⇒ 否则读数是空的）。
	var minTS, maxTS time.Time
	err = tx.QueryRow(ctx, `
		SELECT min(ts), max(ts) FROM public.session_turns`).Scan(&minTS, &maxTS)
	if err != nil || minTS.IsZero() {
		t.Skipf("真库里没有 session_turns 数据（min=%v err=%v）⇒ 等价性无样本，显式 SKIP", minTS, err)
	}
	from := minTS.Add(-time.Hour)
	to := maxTS.Add(time.Hour)
	t.Logf("等价门窗口：[%s, %s)", from, to)

	req := httptest.NewRequest("GET", "/api/admin/turns", nil)
	arms, args, argIdx := buildTurnsListArms(req, "", from, to, time.Time{}, "", 0)

	const viewForm = `
		SELECT t.session_id, t.turn_no, t.ts
		FROM public.session_turns_with_current_month t
		WHERE t.ts >= $3 AND t.ts <= $4
		  AND t.partition_date >= $1 AND t.partition_date <= $2
		ORDER BY t.ts DESC, t.session_id DESC, t.turn_no DESC`
	const twoLegsForm = `
		SELECT t.session_id, t.turn_no, t.ts
		FROM %s t
		ORDER BY t.ts DESC, t.session_id DESC, t.turn_no DESC`

	viewSQL := viewForm
	twoLegsSQL := fmt.Sprintf(twoLegsForm, arms)

	read := func(sql string) []string {
		rows, err := tx.Query(ctx, sql, args...)
		require.NoError(t, err, "查询失败：%s", sql)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var sessionID string
			var turnNo int
			var ts time.Time
			require.NoError(t, rows.Scan(&sessionID, &turnNo, &ts))
			out = append(out, fmt.Sprintf("%s|%d|%s", sessionID, turnNo, ts.UTC().Format(time.RFC3339Nano)))
		}
		require.NoError(t, rows.Err())
		return out
	}

	viewRows := read(viewSQL)
	twoLegsRows := read(twoLegsSQL)
	require.Positive(t, argIdx)
	require.NotEmpty(t, viewRows, "视图形态读不到行 ⇒ 等价性无样本，显式失败（不要把空集当通过）")
	require.NotEmpty(t, twoLegsRows, "两腿形态读不到行 ⇒ 等价性无样本，显式失败")
	require.Equal(t, viewRows, twoLegsRows,
		"两腿形态与视图形态读出的行集不相等（★ 前提门 TestTurnsListTwoLegs_NoCrossPartitionDuplicateKeys 必须先过）")
}
