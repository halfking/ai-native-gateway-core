package main

// dual_read_validator_pg_test.go — R80：dual_read_validator 的真库门。
//
// 为什么要这道门：本包既有的 4 个 dual_read_validator 测试**全部传 nil pool**
// （Constructor / NilPoolCompare / SummarizeSignature / NilPoolSummarize），
// 证明的是「nil pool 不得 panic」。而 CompareDetail / Summarize / driftBuckets
// 里数百行真实 SQL——含 mirrorDriftScopeSQL 这段跨两张基表、两个 NOT EXISTS
// 反连接、再套一层 drift_class CASE 的表达式——**从未被任何测试执行过**。
//
// 形态与 R76 挖出的那个 P0 完全同型：假件只验签名与空指针，真实 SQL 靠静态阅读。
// R79 用真库 PREPARE 证了 Compare 一段，剩下三段仍未验证；本门补齐。
//
// 判据是**跨查询不变量**，不是「不报错」：
//
//	I1  sum(ByWorkType[].Rows)      == V1RowsWithoutTurns
//	I2  sum(ByRequestStatus[].Rows)  == V1RowsWithoutTurns
//	I3  GenuineLossRows + InternalLoopbackRows + NonTerminalRows == V1RowsWithoutTurns
//	I4  GenuineLossRows <= V1Rows
//	I5  S4Ready 与 S4GateVoid 复算 s4GateVerdictOf 的完整契约
//	    （v1 写入中 ∧ 窗口扫到过东西 ∧ 无真漏写），且 void ⇒ ¬ready
//
// I1/I2 尤其关键：两个分桶查询与 V1RowsWithoutTurns 来自**同一段**
// mirrorDriftScopeSQL 的**三次独立执行**。任何一次被静默截断（行被 guard 吞掉、
// 超时后返回部分结果、分区漏扫）都会让聚合不相等——而「不报错」对此一无所知。
// 这正是本项目反复吃过的亏：会报错的缺陷会被发现，会返回部分结果且不报错的不会。
//
// I3 的三个分类值取自 db.MirrorDriftClassSQL（db/request_logs_view_schema.go:828）
// 的实际产出：internal_loopback / non_terminal / genuine_loss。I6 会钉住这一点，
// 分类 CASE 一改门就红，而不是让下一个人去猜新值。
//
// 变量名按仓内契约取 TEST_PG_DSN（见 47 号报告）：注入清单由
// sql/schema/integration_gate_test.go 的 TestGateInjectsEveryDBCredentialName
// 从仓内按后缀推导，自造 _PG_DSN 名字会被结构性排除在 CI 之外却仍显示 ok。
//
// 本门**只读**：不播种、不删数据、无残留，因此不需要事务回滚或前缀清理
// （与 R76 的 handoff 门不同——那门调用的方法不接受 tx，只能播种后按前缀删）。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/db"
)

func resolveDualReadDSN() string {
	if v := os.Getenv("TEST_PG_DSN"); v != "" {
		return v
	}
	return os.Getenv("LLM_GATEWAY_DUALREAD_PG_DSN")
}

// openDualReadPool 建一个**每条连接都设好 RLS 旁路**的池。
//
// 为什么必须用 AfterConnect 而不是执行一次 set_config：pgxpool 的连接会被复用，
// 一次 Exec 只作用于当时那条连接。FORCE RLS 的 request_logs（真库确认
// relforcerowsecurity=t）会在别的连接上静默返回 0 行——而 0 行恰好能通过下面
// 若干断言（I3/I5 在全零时恒成立），是最危险的失败形态。本机 DSN 用户是
// superuser 所以看不出差别，CI 的受限 DSN 用户会。
func openDualReadPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		// is_local=false：设置落到 session 级，随连接在池里留存。这样每条
		// 新建的连接都会自己带上，而不是依赖某一次 Exec 恰好落在它身上。
		_, err := c.Exec(ctx, `SELECT set_config('app.bypass_rls','true',false)`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// probeDualReadTables 在跑业务断言前先确认表存在：没迁移过的库应当干净跳过，
// 而不是报一堆语法错误让人以为是代码坏了。
func probeDualReadTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, tbl := range []string{
		"request_logs", "request_logs_hot",
		"session_turns", "session_turns_hot",
	} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT to_regclass('public.'||$1) IS NOT NULL`, tbl).Scan(&exists); err != nil {
			t.Fatalf("probe %s: %v", tbl, err)
		}
		if !exists {
			t.Skipf("public.%s missing — apply session storage migrations before running this gate", tbl)
		}
	}
}

// pickRealSession 找一个 V1 与 V2 两侧都有数据的真实会话。
// 找「两侧都有」而不是随便取一个：只有这种会话才让 Compare 的差值有意义，
// 否则 TokenDiff/CostDiff 恒为 0，门就退化成「不报错」。
func pickRealSession(t *testing.T, pool *pgxpool.Pool) (tenant, session string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// ts 边界 + 有界样本，两个都是必需的：
	//  1. 第一版没有 ts 边界，对 215 万行 request_logs 做 GROUP BY + 相关
	//     EXISTS，30s 超时，门只能 Skip——而 **Skip 在报告里和 PASS 长得
	//     一样**。同一段 EXISTS 在 Summarize 里对 24h 窗口只需 0.43s，
	//     证明 request_id 索引没问题，问题纯在分母。
	//  2. 第二版直接取「最新一条」的会话，拿到的是只有 1 行的会话——
	//     Compared==1 让断言被平凡满足，判别力归零。门若只能证明
	//     「1==1」，那它证明的是查询能返回，不是查询算得对。
	//     故在有界样本内按行数降序取最大会话。
	const sampleCap = 20000
	err := pool.QueryRow(ctx, `
		WITH recent AS (
			SELECT tenant_id::text AS tenant_id, gw_session_id, request_id
			FROM public.request_logs
			WHERE ts > now() - interval '24 hours'
			  AND gw_session_id IS NOT NULL AND gw_session_id <> ''
			LIMIT $1
		)
		SELECT r.tenant_id, r.gw_session_id
		FROM recent r
		WHERE EXISTS (
			SELECT 1 FROM public.session_turns st WHERE st.request_id = r.request_id
		)
		GROUP BY 1, 2
		ORDER BY count(*) DESC
		LIMIT 1`, sampleCap).Scan(&tenant, &session)
	if err != nil {
		if pgx.ErrNoRows == err {
			t.Skip("no session with both V1 and V2 rows in the last 24h — nothing to reconcile")
		}
		t.Skipf("session discovery failed (%v) — 无法在真库上对账", err)
	}
	return tenant, session
}

func TestDualReadValidator_Compare_RealDB(t *testing.T) {
	pool := openDualReadGate(t)
	tenant, session := pickRealSession(t, pool)

	// 会话太小会让下面所有断言平凡成立（1==1）。低于门槛就显式 Skip 并说清
	// 原因，而不是让一个没有判别力的 PASS 混进报告。
	requireSessionWithRows(t, pool, tenant, session, 5)

	v := NewDualReadValidator(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	d, err := v.Compare(ctx, tenant, session, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if d.SessionID != session {
		t.Errorf("SessionID = %q, want %q", d.SessionID, session)
	}

	// 语义断言：Compared 必须等于 V1 侧该会话的真实行数。**独立算一遍**
	// 而不是复述实现——复述实现的话，Compare 自己算错也会跟着「一致」。
	var wantCompared int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.request_logs
		WHERE tenant_id::text = $1 AND gw_session_id = $2`, tenant, session).Scan(&wantCompared); err != nil {
		t.Fatalf("independent count: %v", err)
	}
	if d.Compared != wantCompared {
		t.Errorf("Compared = %d, want %d (Compare 的 V1 计数与独立复算不一致)", d.Compared, wantCompared)
	}
	t.Logf("Compare(%s/%s): compared=%d token_diff=%d cost_diff=%.6f",
		tenant, session, d.Compared, d.TokenDiff, d.CostDiff)
}

func TestDualReadValidator_CompareDetail_RealDB(t *testing.T) {
	pool := openDualReadGate(t)
	tenant, session := pickRealSession(t, pool)

	v := NewDualReadValidator(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 这一段从未被任何测试执行过（R80 的主要目标）。
	requireSessionWithRows(t, pool, tenant, session, 5)
	det, err := v.CompareDetail(ctx, tenant, session, 50)
	if err != nil {
		t.Fatalf("CompareDetail: %v", err)
	}
	if det.SessionID != session {
		t.Errorf("SessionID = %q, want %q", det.SessionID, session)
	}
	// I7：只在一侧出现的 request_id 互斥且各自不超过该侧行数。
	// 这一条对「FULL JOIN 退化成 INNER JOIN」敏感——那种退化会让
	// OnlyInV1 与 OnlyInV2 同时变成 0，而「不报错」看不出来。
	if det.OnlyInV1Count != len(det.OnlyInV1) && !(det.OnlyInV1Count > 0 && len(det.OnlyInV1) == 0) {
		t.Errorf("OnlyInV1Count=%d 与 OnlyInV1 长度=%d 不自洽（允许采样截断：count>0 且列表为空）",
			det.OnlyInV1Count, len(det.OnlyInV1))
	}
	if det.OnlyInV2Count != len(det.OnlyInV2) && !(det.OnlyInV2Count > 0 && len(det.OnlyInV2) == 0) {
		t.Errorf("OnlyInV2Count=%d 与 OnlyInV2 长度=%d 不自洽（允许采样截断）",
			det.OnlyInV2Count, len(det.OnlyInV2))
	}
	// I8：采样上限真的被传下去了。要 10000 也全量返回 = sampleLimit 没生效。
	if got := len(det.DriftSamples); got > 50 {
		t.Errorf("DriftSamples 返回 %d 条而 sampleLimit=50 —— 采样上限未生效", got)
	}
	t.Logf("CompareDetail(%s/%s): v1=%d v2=%d onlyV1=%d onlyV2=%d samples=%d",
		tenant, session, det.V1Rows, det.V2Rows,
		det.OnlyInV1Count, det.OnlyInV2Count, len(det.DriftSamples))
}

func TestDualReadValidator_Summarize_RealDB(t *testing.T) {
	pool := openDualReadGate(t)

	v := NewDualReadValidator(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// 24h 窗口：够拿到非零数据，又不至于让四次全表聚合跑到超时。
	sum, err := v.Summarize(ctx, "", 24)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if sum == nil {
		t.Fatal("Summarize returned nil summary with no error")
	}
	// 钳位不改变有效值：24 传进去就该是 24。
	if sum.WindowHours != 24 {
		t.Errorf("WindowHours = %d, want 24", sum.WindowHours)
	}

	// I3：三类互斥且穷尽，分项之和必须等于总数。
	classSum := sum.GenuineLossRows + sum.InternalLoopbackRows + sum.NonTerminalRows
	if classSum != sum.V1RowsWithoutTurns {
		t.Errorf("I3 分类不自洽：genuine(%d)+internal(%d)+non_terminal(%d)=%d != V1RowsWithoutTurns(%d)",
			sum.GenuineLossRows, sum.InternalLoopbackRows, sum.NonTerminalRows,
			classSum, sum.V1RowsWithoutTurns)
	}
	// I4：漂移行是 V1 行的子集；反过来则说明分母查询被截断。
	if sum.GenuineLossRows > sum.V1Rows {
		t.Errorf("I4 GenuineLossRows(%d) > V1Rows(%d) —— 漂移多于总量，分母查询疑似被截断",
			sum.GenuineLossRows, sum.V1Rows)
	}
	// I5：独立复算 S4Ready。**判据已于 2026-10-02 收紧**。
	//
	// 原判据是 `S4Ready == (GenuineLossRows == 0)`。它是对的当且仅当 v1 还在
	// 写且窗口里扫到过东西——两个前提都不由这条不变式自己保证，而 S4 一停写
	// 两条同时失效，于是该字段恒真（真库实测：窗口内零 V1 行 ⇒ S4Ready=true）。
	// 换句话说，这道门原本把缺陷本身钉成了「不变式」。
	//
	// 现在复算完整契约：S4Ready 为真当且仅当（v1 写入中 ∧ 窗口扫到过东西 ∧
	// 无真漏写）。三者缺一都必须为假，并按 s4GateVerdictOf 的分类给出 void 理由。
	verdict := s4GateVerdictOf(s4GateInput{
		v1Rows:       sum.V1Rows,
		genuineLoss:  sum.GenuineLossRows,
		v1WritesOn:   currentV1WritesEnabled(),
		v1CoveragePP: sum.V1CoveragePP,
		windowHours:  sum.WindowHours,
	})
	if sum.S4Ready != verdict.Ready {
		t.Errorf("I5 S4Ready = %v, want %v (V1Rows=%d GenuineLossRows=%d v1WritesEnabled=%v)",
			sum.S4Ready, verdict.Ready, sum.V1Rows, sum.GenuineLossRows, sum.V1WritesEnabled)
	}
	if sum.S4GateVoid != verdict.Void {
		t.Errorf("I5b S4GateVoid = %v, want %v (reason=%q)", sum.S4GateVoid, verdict.Void, sum.S4GateVoidReason)
	}
	// I5c：把「void」与「ready」在产物层面互斥掉。这是本次修复的核心不变式，
	// 且与数据无关——不依赖窗口里恰好有没有漂移行，因此永远会被求值。
	if sum.S4GateVoid && sum.S4Ready {
		t.Errorf("I5c void 与 ready 同时为真（reason=%q）——这正是 2026-10-02 修掉的真空为绿",
			sum.S4GateVoidReason)
	}
	if sum.S4GateVoid != (sum.S4GateVoidReason != "") {
		t.Errorf("I5d S4GateVoid=%v 与 reason=%q 不自洽", sum.S4GateVoid, sum.S4GateVoidReason)
	}
	// I1 / I2：两个分桶查询各自跑了一遍同一段 scope SQL，行数之和必须与
	// V1RowsWithoutTurns 相等。任一次静默截断都会打破这条。
	if got := bucketRowSum(sum.ByWorkType); got != sum.V1RowsWithoutTurns {
		t.Errorf("I1 ByWorkType 行数之和 = %d, want V1RowsWithoutTurns = %d", got, sum.V1RowsWithoutTurns)
	}
	if got := bucketRowSum(sum.ByRequestStatus); got != sum.V1RowsWithoutTurns {
		t.Errorf("I2 ByRequestStatus 行数之和 = %d, want V1RowsWithoutTurns = %d", got, sum.V1RowsWithoutTurns)
	}
	// I6：桶内每一项的 class 必须是 MirrorDriftClassSQL 的三个已知产出之一。
	//
	// **这条断言本身是数据依赖的**——若窗口里没有 genuine_loss 的行，它根本
	// 不会被求值。第一版就栽在这里：把 db.MirrorDriftClassSQL 的
	// `ELSE 'genuine_loss'` 改成 `'genuine'` 做变异，本机 24h 窗口
	// GenuineLossRows=0，桶里只剩 internal_loopback/non_terminal，
	// 断言一次都没跑到 ⇒ **变异后仍绿**。数据无关的版本见
	// TestDriftClassExpression_RealDB，两者并存。
	for _, b := range append(append([]MirrorDriftBucket{}, sum.ByWorkType...), sum.ByRequestStatus...) {
		switch b.Class {
		case "internal_loopback", "non_terminal", "genuine_loss":
		default:
			t.Errorf("未知 drift_class %q（key=%s rows=%d）——分类 CASE 形态变了，本门需同步",
				b.Class, b.Key, b.Rows)
		}
	}
	t.Logf("Summarize(24h): v1=%d noTurns=%d genuine=%d internal=%d nonTerminal=%d S4Ready=%v",
		sum.V1Rows, sum.V1RowsWithoutTurns, sum.GenuineLossRows,
		sum.InternalLoopbackRows, sum.NonTerminalRows, sum.S4Ready)
}

func bucketRowSum(bs []MirrorDriftBucket) int64 {
	var n int64
	for _, b := range bs {
		n += b.Rows
	}
	return n
}

// openDualReadGate 是三个门共用的入口：无 DSN 干净跳过，有 DSN 则建池并探表。
func openDualReadGate(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := resolveDualReadDSN()
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set — offline mode")
	}
	pool := openDualReadPool(t, dsn)
	probeDualReadTables(t, pool)
	return pool
}

// requireSessionWithRows 保证被测会话够大。门槛取 5：Compare 的
// Compared==wantCompared 与 CompareDetail 的分桶求和都需要「不止一行」才有
// 判别力；单行会话会让任何算错的实现也通过。
func requireSessionWithRows(t *testing.T, pool *pgxpool.Pool, tenant, session string, min int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.request_logs
		WHERE tenant_id::text = $1 AND gw_session_id = $2`, tenant, session).Scan(&n); err != nil {
		t.Fatalf("count session rows: %v", err)
	}
	if n < min {
		t.Skipf("picked session %s has only %d V1 row(s) (<%d) —— 断言会被平凡满足，跳过以免假 PASS",
			session, n, min)
	}
	t.Logf("被测会话 %s：V1 %d 行", session, n)
}

// TestDualReadValidator_CompareVsDetailScope_RealDB 钉住一个**已知且刻意**的
// 口径差异：Compare 与 CompareDetail 对同一个会话读的不是同一批行。
//
// 事实（本机真库，24h 内某会话）：
//
//	Compare        V1Rows = 141   ← 只读 public.request_logs
//	CompareDetail  V1Rows = 152   ← 读 public.request_logs_hot ∪ public.request_logs
//	差                11   ← 全部来自仍在 hot 窗口的行
//
// request_logs_hot 与 request_logs 是**两张独立表**（真库确认 request_logs_hot
// 不是 request_logs 的分区，pg_inherits 命中 0），所以 CompareDetail 的
// UNION ALL 不是重复计数，而是真的补上了 hot 侧的行。
//
// 为什么把不一致钉成测试而不是直接修：dual_read_validator.go:189-191 的注释
// 写「Reads BASE tables (hot ∪ parent) on both sides for the same reason as
// CompareDetail」——**措辞暗示 Compare 也读基表，但它并不**。于是
// Compare.TokenDiff / CostDiff 会系统性低估漂移（漏掉整个 hot 窗口的行），
// 而 CompareDetail 是完整的。
//
// 改 Compare 的口径会改变对外端点的既有读数（客户端观感），属产品裁决
// （R79 已把「TokenDiff 不可解释」列为待裁决项）。本门的作用是：
// 一旦有人统一了口径、或反过来把差异扩大，本门会红，迫使那次改动被显式决定，
// 而不是无声发生。**这比"顺手改一致"更重要**——不一致本身不是缺陷，
// 不一致且无人知情才是。
func TestDualReadValidator_CompareVsDetailScope_RealDB(t *testing.T) {
	pool := openDualReadGate(t)
	tenant, session := pickRealSession(t, pool)
	requireSessionWithRows(t, pool, tenant, session, 5)

	v := NewDualReadValidator(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	d, err := v.Compare(ctx, tenant, session, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	det, err := v.CompareDetail(ctx, tenant, session, 50)
	if err != nil {
		t.Fatalf("CompareDetail: %v", err)
	}

	var parentOnly, hotOnly int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM public.request_logs
			 WHERE tenant_id::text=$1 AND gw_session_id=$2),
			(SELECT count(*) FROM public.request_logs_hot
			 WHERE tenant_id::text=$1 AND gw_session_id=$2)`,
		tenant, session).Scan(&parentOnly, &hotOnly); err != nil {
		t.Fatalf("scope counts: %v", err)
	}

	// Compare = 仅父表
	if d.Compared != parentOnly {
		t.Errorf("Compare.Compared = %d, want 父表口径 %d", d.Compared, parentOnly)
	}
	// CompareDetail = hot ∪ 父表
	if want := parentOnly + hotOnly; det.V1Rows != want {
		t.Errorf("CompareDetail.V1Rows = %d, want hot∪父表 %d (hot=%d)", det.V1Rows, want, hotOnly)
	}
	// 差异必须等于 hot 侧行数——这是把「口径差异」与「算错」区分开的那条线。
	if got := det.V1Rows - d.Compared; got != hotOnly {
		t.Errorf("口径差 = %d, want hot 侧行数 %d", got, hotOnly)
	}
	t.Logf("Compare(父表)=%d  CompareDetail(hot∪父表)=%d  hot=%d  → 差 %d 行被 Compare 漏掉",
		d.Compared, det.V1Rows, hotOnly, det.V1Rows-d.Compared)
}

// TestDriftClassExpression_RealDB 是 I6 的**数据无关**版本：直接对
// db.MirrorDriftClassSQL 求值，用三行合成输入各命中一个 WHEN/ELSE。
//
// 为什么必须与 I6 并存：分类断言若只在真实窗口上跑，就继承了「窗口里恰好没有
// 这一类」的失效模式——门看起来在守着分类，实际上一行都没守。本门不依赖任何
// 业务数据，恒定求值三个分支，因此分类表达式的任何改名/改形都会红。
//
// 输入列与 db/request_logs_view_schema.go:828 的 CASE 所引用的列一一对应。
func TestDriftClassExpression_RealDB(t *testing.T) {
	openDualReadGate(t) // 只为确认 DSN/连通性；本测试不读业务表
	pool := openDualReadPool(t, resolveDualReadDSN())

	cases := []struct {
		name string
		row  string
		want string
	}{
		{"internal_loopback_by_request_type",
			`(true, 'title_gen', '', 'sometask', true, 'success', '')`, "internal_loopback"},
		{"internal_loopback_by_empty_task_type",
			`(true, 'chat', '', '', true, 'success', '')`, "internal_loopback"},
		{"non_terminal",
			`(false, 'chat', '', 'task', false, 'in_progress', '')`, "non_terminal"},
		{"genuine_loss_terminal_failure",
			`(false, 'chat', '', 'task', false, 'failure', 'transient')`, "genuine_loss"},
		{"genuine_loss_success",
			`(false, 'chat', '', 'task', true, 'success', '')`, "genuine_loss"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, tc := range cases {
		var got string
		q := `SELECT (` + db.MirrorDriftClassSQL + `)
		      FROM (VALUES ` + tc.row + `)
		      -- 别名必须叫 rl：MirrorDriftClassSQL 内部用 rl. 前缀引用列
		      AS rl(is_auto_request, request_type, origin_actor, task_type, success, request_status, error_kind)`
		if err := pool.QueryRow(ctx, q).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: class = %q, want %q", tc.name, got, tc.want)
		}
	}
	t.Logf("分类表达式 %d 个合成输入全部命中预期分类", len(cases))
}
