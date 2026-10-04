//go:build !integration

package sessionv2mirror

// final_success_turn_realdb_test.go — 2026-10-05（审计 §9.203）。
//
// # 这道门量的是「标记函数本身」而不是「有没有人调用它」
//
// 接线由 final_success_turn_gate_test.go 断言；那道门是静态的，能证明调用
// 存在，证明不了 SQL 在真库上真的能落地。所以这里必须来真的：种行、跑标记、
// 读回、并用三个阴性对照证明**量具有牙**。
//
// # 为什么必须有阴性对照
//
// 「标记跑完，行是 TRUE」这句话，如果不加对照，可以由一个**什么都没做**的
// 实现满足：语句拼错、事务根本没提交、RLS 把行全滤掉、tenant 谓词写错——
// 这些都会让读回仍是 NULL，于是测试红；但反过来，**一个恒真的实现**（比如
// 把 WHERE 去掉、对全表 UPDATE）也会让阳性通过。区分这两者只能靠阴性对照：
// 未知 request_id 必须 0 行、错误 tenant_id 必须 0 行。少了它们，这道门
// 只能证明「标记在某些情况下发生过」，不能证明「标记只在该发生时发生」。
//
// # ⚠ 本地角色是超级用户，RLS 在这里**没有被考核**
//
// abandoned_turn.go 的表头已经实测：两张脸都 ENABLE RLS 且带一条 RESTRICTIVE
// owner_filter，而本机 `llm_gateway` 是 rolsuper=t 且 rolbypassrls=t ⇒ 本门
// 连接下 RLS 恒不生效，markTurnFinalSuccess 里那套 GUC 是否**真的**必要，
// 本门证明不了。tenant 谓词（阴性对照 B）仍然被考核，因为那是 WHERE 子句，
// 与角色无关。RLS 那一层要证明必须换非超级用户角色，属另一道门。
//
// # 不留痕
//
// 标记函数自带事务，无法被本门的外层事务回滚，所以种子行必须显式删除。
// 本门在前后各取一次基线计数并在最后复核——「清理过了」是断言，不是许诺。

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func finalSuccessProbeID(tag string) string {
	return fmt.Sprintf("probe-final-success-%s-%d", tag, time.Now().UnixNano())
}

func TestFinalSuccessMarkLandsOnRealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-database final-success pad proof")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("TEST_DATABASE_URL unreachable, skipping: %v", err)
	}

	// ── 前提自证 ────────────────────────────────────────────────────────────
	// 缺表时下面每一步都会以「0 行」通过，那是一条恒真的门。
	for _, tbl := range []string{"public.session_turns_hot", "public.session_turns"} {
		var ok bool
		if err := pool.QueryRow(ctx,
			"SELECT to_regclass($1) IS NOT NULL", tbl).Scan(&ok); err != nil {
			t.Fatalf("probe %s: %v", tbl, err)
		}
		if !ok {
			t.Fatalf("%s 不存在 —— 本门的前提不成立，它会在缺表时因「读回 0 行」而恒绿。"+
				"**不要**把恒绿当通过。", tbl)
		}
	}
	// 部分唯一索引是「重复授予」的唯一防线。没有它，mark_superseded 分支就
	// 永远不可达，本门也就无法区分「索引挡住了」与「索引不存在」。
	var idxCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_indexes
		 WHERE schemaname = 'public'
		   AND tablename IN ('session_turns_hot','session_turns')
		   AND indexdef ILIKE '%UNIQUE%'
		   AND indexdef ILIKE '%WHERE is_final_success%'
	`).Scan(&idxCount); err != nil {
		t.Fatalf("probe partial unique indexes: %v", err)
	}
	if idxCount < 2 {
		t.Fatalf("只找到 %d 个 is_final_success 部分唯一索引（hot + 母表各一），"+
			"少于预期的 2 个 —— session 族的唯一性防线不在，本门的重复授予对照不可判定", idxCount)
	}

	// 基线：只作参考记录。**不用它做清理判据**——网关在跑，总行数本来就在动。
	var baseTotal, baseMarked int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE is_final_success IS NOT NULL)
		  FROM session_turns_hot
	`).Scan(&baseTotal, &baseMarked); err != nil {
		t.Fatalf("baseline count: %v", err)
	}
	t.Logf("baseline session_turns_hot: total=%d marked=%d（仅供参考，不作判据）", baseTotal, baseMarked)

	const tenant = "probe-final-success-tenant"
	const session = "probe-final-success-session"
	hotID := finalSuccessProbeID("hot")
	// 父表腿（分区表）：直接往母表插，让 PG 自己按 partition_date 路由到当月
	// 分区，这样 markTurnFinalSuccess 的第二个 UPDATE 也有行可命中。路由失败
	// （当月分区还没建）时**指名 Skip 这一段**，不许算绿。
	partID := finalSuccessProbeID("part")
	// 阴性对照 B 的种子行。与上面两行同批登记进 cleanup —— 清理必须覆盖
	// **两张脸**（session_turns_hot 不是 session_turns 的分区，只删母表
	// 会把 hot 行留在库里；第一版就这么漏了一行，被门自己的残留复核抓到）。
	wrongTenantID := finalSuccessProbeID("wrongtenant")

	cleanup := func() {
		for _, id := range []string{hotID, partID, wrongTenantID} {
			if _, err := pool.Exec(ctx,
				`DELETE FROM session_turns WHERE request_id = $1 AND tenant_id = $2`,
				id, tenant); err != nil {
				t.Logf("cleanup session_turns %s: %v", id, err)
			}
			if _, err := pool.Exec(ctx,
				`DELETE FROM session_turns_hot WHERE request_id = $1 AND tenant_id = $2`,
				id, tenant); err != nil {
				t.Logf("cleanup session_turns_hot %s: %v", id, err)
			}
		}
	}
	// 清理复核只认**探针行是否清干净**，不认总数。原因：网关在跑，
	// session_turns_hot 的总行数本来就在变（实测 1621→1627），
	// 拿总数做判据会把并发的正常写入当成我留了痕迹 —— 那是判据红了
	// 先怀疑判据的又一次。
	defer func() {
		cleanup()
		var leftover int
		if err := pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM session_turns_hot WHERE request_id LIKE 'probe-final-success-%')
			     + (SELECT count(*) FROM session_turns  WHERE request_id LIKE 'probe-final-success-%')
		`).Scan(&leftover); err != nil {
			t.Logf("post-cleanup probe failed: %v", err)
			return
		}
		if leftover != 0 {
			t.Errorf("本门在库里留了 %d 行探针数据（request_id LIKE 'probe-final-success-%%'）", leftover)
		}
	}()

	// 种子行：只给 4 个无默认值的必填列，其余吃默认。
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_turns_hot (session_id, turn_no, tenant_id, request_id)
		VALUES ($1, 1, $2, $3)
	`, session, tenant, hotID); err != nil {
		t.Fatalf("seed session_turns_hot: %v", err)
	}
	partSeeded := false
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_turns (session_id, turn_no, tenant_id, request_id, partition_date)
		VALUES ($1, 1, $2, $3, CURRENT_DATE)
	`, session, tenant, partID); err != nil {
		t.Logf("SKIP 父表腿：往 session_turns 母表插入失败（%v）—— 当月分区可能尚未创建，"+
			"本门只证明了 hot 腿，**父表腿本次未被考核**", err)
	} else {
		partSeeded = true
	}
	t.Logf("seeded hot=%v partition=%v(seeded=%v)", hotID, partID, partSeeded)

	// 回读必须**两张脸都查**。session_turns_hot 不是 session_turns 的分区
	// （hot 是独立表，月度分区才是 session_turns 的子表），只查母表会把
	// hot 腿上真实落地的标记读成「没写」—— 量具读错对象，正是本轮反复
	// 出现的失败形态，所以回读写成两脸求和而不是二选一。
	readMarked := func(id string) bool {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM session_turns_hot
			         WHERE request_id = $1 AND tenant_id = $2 AND is_final_success IS TRUE)
			     + (SELECT count(*) FROM session_turns
			         WHERE request_id = $1 AND tenant_id = $2 AND is_final_success IS TRUE)
		`, id, tenant).Scan(&n); err != nil {
			t.Fatalf("read back %s: %v", id, err)
		}
		return n > 0
	}

	// ── 阳性：两张脸各自必须真的落地 ───────────────────────────────────────
	//
	// ⚠ 两张脸是**两个不同的 request_id**，不是一个逻辑行的两面。标记函数
	// 按 request_id 定位，一张行要么在 hot、要么已 promote 进月分区，
	// 不会同时在两处。pad 之所以扫两张脸，是为了覆盖「标记跑的时候这行已经
	// 被搬走了」—— 所以必须分别种、分别标、分别回读。给两张脸种不同 id 却
	// 只调用一次标记，然后要求两次回读都真，是把量具建模错了。
	before := testutil.ToFloat64(finalSuccessTurnOps.WithLabelValues("mark"))

	markTurnFinalSuccess(ctx, pool, hotID, tenant, session)
	if got := testutil.ToFloat64(finalSuccessTurnOps.WithLabelValues("mark")) - before; got != 1 {
		t.Fatalf("hot 腿的 mark 计数器没有 +1（实得 +%v）—— 标记没有成功落到 hot 行上", got)
	}
	if !readMarked(hotID) {
		t.Fatalf("标记跑完了，但 session_turns_hot 上 %s 的 is_final_success 仍非 TRUE —— "+
			"写侧仍然没写（这正是 §9.203 的原始症状）", hotID)
	}
	// 顺带一条阴性：标记 hotID 不得波及 partID（request_id 作用域是真的）。
	if partSeeded && readMarked(partID) {
		t.Fatalf("标记 %s 之后，未被标记的 %s 竟然也是 TRUE —— request_id 谓词没有真正参与过滤",
			hotID, partID)
	}
	if partSeeded {
		markTurnFinalSuccess(ctx, pool, partID, tenant, session)
		if !readMarked(partID) {
			t.Fatalf("标记跑完了，但月分区行 %s 的 is_final_success 仍非 TRUE —— 父表腿没生效", partID)
		}
	}
	t.Logf("阳性通过：hot 腿与父表腿各自落地（父表腿 seeded=%v）", partSeeded)

	// ── 幂等：再跑一次不得报错、不得改出第二行，且必须报 mark_noop ──────────
	// 第二版专门考核这一点。第一版把「0 行」一律报成 mark_no_row + WARN，
	// 于是幂等重跑会打出一条「v1 认领了但 turn 不在两张脸上」的**假警**——
	// 而那正是这条告警要抓的真故障。告警一旦会说假话就等于没有告警。
	noop := testutil.ToFloat64(finalSuccessTurnOps.WithLabelValues("mark_noop"))
	noRowBefore := testutil.ToFloat64(finalSuccessTurnOps.WithLabelValues("mark_no_row"))
	markTurnFinalSuccess(ctx, pool, hotID, tenant, session)
	if got := testutil.ToFloat64(finalSuccessTurnOps.WithLabelValues("mark_noop")) - noop; got != 1 {
		t.Errorf("对已标记的行重跑应产生 1 次 mark_noop，实得 +%v —— 幂等重跑被误报成异常", got)
	}
	if got := testutil.ToFloat64(finalSuccessTurnOps.WithLabelValues("mark_no_row")) - noRowBefore; got != 0 {
		t.Errorf("幂等重跑被记成了 mark_no_row（+%v）—— 「行不存在」与「已是 TRUE」被混为一谈，"+
			"这会让真异常告警在说假话", got)
	}
	if !readMarked(hotID) {
		t.Fatalf("第二次标记后 is_final_success 不再是 TRUE —— 幂等性被破坏")
	}

	// ── 阴性对照 A：未知 request_id 必须 0 行 ────────────────────────────────
	// 没有这一条，「什么都没做」和「正确地什么都没做」不可区分。
	noRow := testutil.ToFloat64(finalSuccessTurnOps.WithLabelValues("mark_no_row"))
	markTurnFinalSuccess(ctx, pool, finalSuccessProbeID("absent"), tenant, session)
	if got := testutil.ToFloat64(finalSuccessTurnOps.WithLabelValues("mark_no_row")) - noRow; got != 1 {
		t.Errorf("未知 request_id 应当产生 1 次 mark_no_row，实得 +%v —— "+
			"计数口径变了，或标记在无行时谎报成功", got)
	}

	// ── 阴性对照 B：request_id 对、tenant 错，必须 0 行 ──────────────────────
	// 这一条证明 tenant 谓词是真的在过滤，而不是恒真条件。
	//
	// ⚠ 回读必须**两张脸都查**（同 readMarked 的理由）。种在 hot、回读只查
	// 母表 session_turns 的写法会让这条对照**恒真**——无论 tenant 谓词写得多
	// 离谱，母表里都查不到那行，于是每次都「通过」。恒真的对照比没有对照更坏：
	// 它让人以为这一层被考核过了。
	// turn_no 必须另取：session_turns_hot 的唯一键是 (tenant_id, session_id,
	// turn_no)，沿用 turn_no=1 会撞 23505 —— 那是种数据撞约束，不是被测行为。
	// ⚠ 这行**必须用与 hotID 不同的 session_id**。第一版复用了同一个
	// session_id，于是标记它时会撞上 uq_session_turns_hot_final_success
	// （(tenant_id, session_id, partition_date) WHERE is_final_success）——
	// hotID 那一行在阳性段已经被标上了 ⇒ UPDATE 报 23505，函数走
	// mark_superseded 提前返回，**根本没执行到 tenant 谓词**。
	// 于是这条对照无论谓词写得多离谱都会「通过」。
	// ⇒ 它当时是一条**恒真的对照**，而恒真的对照比没有对照更坏：
	// 它让人以为跨租户这一层被考核过。变异实测确认：
	// 把谓词换成恒真表达式后本门仍然 PASS。
	wrongTenantSession := session + "-other"
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_turns_hot (session_id, turn_no, tenant_id, request_id)
		VALUES ($1, 1, $2, $3)
	`, wrongTenantSession, tenant, wrongTenantID); err != nil {
		t.Fatalf("seed wrong-tenant row: %v", err)
	}
	markTurnFinalSuccess(ctx, pool, wrongTenantID, tenant+"-NOT-THE-ROW", wrongTenantSession)
	var leaked int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM session_turns_hot
		         WHERE request_id = $1 AND tenant_id = $2 AND is_final_success IS TRUE)
		     + (SELECT count(*) FROM session_turns
		         WHERE request_id = $1 AND tenant_id = $2 AND is_final_success IS TRUE)
	`, wrongTenantID, tenant).Scan(&leaked); err != nil {
		t.Fatalf("read back wrong-tenant row: %v", err)
	}
	if leaked != 0 {
		t.Fatalf("用错误 tenant 标记时仍然命中了 %d 行 —— tenant 谓词没有真正参与过滤，"+
			"标记会跨租户写", leaked)
	}
	t.Log("阴性对照通过：未知 request_id 与错误 tenant 均 0 行")
}
