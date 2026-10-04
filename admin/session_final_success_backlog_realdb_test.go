//go:build !integration

package admin

// session_final_success_backlog_realdb_test.go — 2026-10-05（审计 §9.203 / 决策 D32）。
//
// 这道门量的是一笔**欠账**：v1 的 final-success 认领有多少在 session 族还没有对应标记。
//
// # 为什么要有门
//
// 写侧已修（de9be4a8f，final_success_turn.go），所以**新请求**不再丢标记。
// 但已经躺在 session_turns 里的历史行，标记仍然是 NULL —— 而 `request_logs`
// 退役之后这份信息就**再也无法恢复**了。
//
// 而在此之前，这件事是**完全不可见**的：`session_turns.is_final_success`
// 全表 1,689,308 行里非空 0 行，而**没有任何门会报**。
// `db/session_family_column_availability_test.go` 确实把它列进了
// `GO EMPTY ON THE SESSION SIDE`，但那是**信息行不是断言**。
// ⇒ 这道门把它变成一条会变红的线。
//
// # 它会红，而且**这是对的**
//
// 本地（回填未执行）红；执行 `sql/scripts/backfill_final_success_marks.sql`
// 之后转绿。形态与 D30-a 那两道「故意保持红」的门同款：红的意义是
// 「这个库的形态/数据与应有状态不一致」。
//
// ⚠ 它**不是**回归门。有人在本地跑一次回填就会让它转绿，这是预期行为，
// 不是「被改绿了」。
//
// # 零样本必须指名 Skip
//
// 三种「看起来通过」的情形都不是通过：
//   - 没设 TEST_DATABASE_URL ⇒ Skip（指名）；
//   - v1 已被退役（request_logs 不存在）⇒ **Skip 并指名**「回填窗口已关闭」，
//     绝不是 Pass —— 那意味着没人再能修复这笔欠账，是最该报警的状态之一；
//   - v1 有 0 个 winner 且 v2 有 0 个标记 ⇒ 两者都是 0，判据无意义，指名 Skip。
//
// # 口径：残余缺口**不算**失败
//
// v1 有 winner 但 v2 没有对应 turn 的行（实测 38 行，2026_09）是**镜像按设计
// 排除**的结果（internal_loopback / 非终态占位行，hook.go:71/102），
// 不是欠账，也不是可修复的。把它算进判据会造出一个永远红的门。

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSessionFinalSuccessBacklogIsClosed(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping final-success backlog measurement")
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

	// 前提自证：两张族都得在。缺任一张时下面的「0 == 0」会恒真。
	var v1Face, v2Face bool
	if err := pool.QueryRow(ctx,
		"SELECT to_regclass('public.request_logs_2026_09') IS NOT NULL").Scan(&v1Face); err != nil {
		t.Fatalf("probe v1 face: %v", err)
	}
	if err := pool.QueryRow(ctx,
		"SELECT to_regclass('public.session_turns_2026_09') IS NOT NULL").Scan(&v2Face); err != nil {
		t.Fatalf("probe v2 face: %v", err)
	}
	if !v1Face {
		t.Skip("v1 月度分区不存在（request_logs 已退役？）—— **Skip 而非 Pass**：" +
			"回填窗口已关闭，未标记的历史 final-success 永久不可恢复。这不是通过。")
	}
	if !v2Face {
		t.Fatalf("public.session_turns_2026_09 不存在 —— 本门前提不成立，" +
			"「0 == 0」会恒绿。**不要**把恒绿当通过。")
	}

	// 分区对分区连接，避开 2.1M × 1.68M 的全表笛卡尔。
	// 与回填脚本同款口径：v1 的 winner 里，有多少在 v2 存在对应 turn。
	// 三项都按「有对应 turn / 无对应 turn / v2 已标记」取，两段分区相加。
	const pairTpl = `
		SELECT
		  (SELECT count(*) FROM ONLY request_logs_%[1]s v
		    WHERE v.is_final_success
		      AND EXISTS (SELECT 1 FROM ONLY session_turns_%[1]s t
		                   WHERE t.request_id = v.request_id))::bigint,
		  (SELECT count(*) FROM ONLY request_logs_%[1]s v
		    WHERE v.is_final_success
		      AND NOT EXISTS (SELECT 1 FROM ONLY session_turns_%[1]s t
		                       WHERE t.request_id = v.request_id))::bigint,
		  (SELECT count(*) FROM ONLY session_turns_%[1]s WHERE is_final_success IS TRUE)::bigint`

	var v1With, v1Without, v2Marked int64
	for _, part := range []string{"2026_09", "2026_10"} {
		var w, wo, m int64
		if err := pool.QueryRow(ctx, fmt.Sprintf(pairTpl, part)).Scan(&w, &wo, &m); err != nil {
			t.Fatalf("query %s: %v", part, err)
		}
		v1With += w
		v1Without += wo
		v2Marked += m
	}

	var hotMarked int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ONLY session_turns_hot WHERE is_final_success IS TRUE`).Scan(&hotMarked); err != nil {
		t.Fatalf("query hot: %v", err)
	}
	v2Marked += hotMarked

	t.Logf("v1 winner（有 v2 对应 turn）=%d；v1 winner（无 v2 turn，按设计排除）=%d；v2 已标记=%d",
		v1With, v1Without, v2Marked)

	// 零样本：两侧都是 0 时判据没有内容。必须指名，不能算通过。
	if v1With == 0 {
		t.Skipf("v1 在 2026_09/2026_10 没有任何有对应 turn 的 final-success winner"+
			"（v2 已标记 %d）—— **Skip 而非 Pass**：本门在这种情况下量不到任何东西，"+
			"空库与「已回填」在这里长得一模一样", v2Marked)
	}

	if v2Marked < v1With {
		t.Fatalf("session 族的 final-success 欠账 %d 行：v1 有 %d 个 winner 在 v2 有对应 turn，"+
			"但 v2 只有 %d 行带标记 ⇒ 会话时间线仍把这些会话的成功轮显示成普通 success"+
			"（另有 %d 个 v1 winner 按镜像排除规则本就无 v2 turn，不计入）。\n"+
			"修复：psql \"$DB_URL\" -f sql/scripts/backfill_final_success_marks.sql"+
			"（幂等，约 89s / 110k 行；必须赶在 request_logs 退役前跑）",
			v1With-v2Marked, v1With, v2Marked, v1Without)
	}
	if v2Marked > v1With {
		// 不是错：写侧（de9be4a8f）已经在给**新**认领打标，那部分不在 v1 的历史集合里。
		t.Logf("v2 标记 %d 略多于 v1 历史 winner %d —— 正常：写侧修复后新认领也会打标，"+
			"那部分不来自 v1 历史。", v2Marked, v1With)
	}
}
