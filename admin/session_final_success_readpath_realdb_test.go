//go:build !integration

package admin

// session_final_success_readpath_realdb_test.go — 2026-10-04（审计 §9.192）。
//
// 这道门**只证明读路是好的**：生产 SQL `db.SessionFamilyTurnsForSessionSQL()`
// 真的会把 `is_final_success=TRUE` 的行带出来。
//
// # 为什么需要一道「只证明读路」的门
//
// 真库实测（7 天窗口）：
//
//	session_turns ∪ session_turns_hot：31,274 轮，is_final_success 非空 **0**
//	request_logs：                 73,264 行，is_final_success 非空 **9,614**
//
// 而 `admin/session_online.go:446` 的 `querySessionTimeline` 直读 session 族
// 原生表并取 `COALESCE(rl.is_final_success, FALSE)` ⇒ 该值**恒为 FALSE**。
//
// 看到 0，第一反应会是「投影坏了 / 读路坏了」。**必须先排除这个解释**，
// 否则修错方向（会去改 710 投影，而投影是忠实的）。所以这里反向构造：
// 在**一次性事务**里种一行 `is_final_success=TRUE`，用**生产 SQL** 读回来，
// 断言它是 TRUE；再种一行 FALSE，断言读回 FALSE。
//
// ⇒ 阳性对照为绿 ⇒ 「0」不是读路缺陷造成的，**缺陷在写侧**
// （`claimSessionFinalSuccess` 只 UPDATE `request_logs_hot`，session 族无等价写方）。
// 这一条把「读路坏了」与「写路没写」分开，是本轮唯一不可省略的判别动作。
//
// # 为什么不直接修
//
// 给 `session_turns` 补写 `is_final_success` 是**改生产行为**（写侧 + 认领语义 +
// 迁移回填），属主决定（决策表 D28）。本轮只刻画 + 钉住读路，不改行为。
//
// # 零行不是绿
//
// 事务回滚 ⇒ 库里不留痕。本门**不读任何既有数据**，所以它在空库上照样有意义；
// 「生产写侧有没有写」是另一道门（s4_session_family_unservable_realdb_test.go），
// 那道门在零行时必须指名 Skip，不许算绿。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

func TestSessionTimelineFinalSuccessReadPathIsSound(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-database read-path proof")
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

	// 前提自证：表必须真的在。缺表时下面每一步都会以「0 行」通过，
	// 那是一条**恒真**的门——所以先把前提钉死。
	var hasTurns bool
	if err := pool.QueryRow(ctx,
		"SELECT to_regclass('public.session_turns') IS NOT NULL").Scan(&hasTurns); err != nil {
		t.Fatalf("probe session_turns: %v", err)
	}
	if !hasTurns {
		t.Fatalf("public.session_turns 不存在 —— 本门的前提不成立，" +
			"它在缺表时会因「读回 0 行」而恒绿。**不要**把恒绿当通过。")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// 整道门跑在一个事务里，末尾 ROLLBACK ⇒ 结构上不可能污染共享库。
	// （这正是 §9.186 踩过的坑：清理不吞错误、绿着留下残行。）
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			// ErrTxClosed = 事务已被结束；本门从不 COMMIT，所以它不是失败。
			// 其余错误必须报出来——吞掉回滚错误正是「绿着污染共享库」的成因。
			t.Errorf("ROLLBACK 失败: %v —— 本门不依赖提交，但**必须**确认没有行留在库里", err)
		}
	}()

	marker := fmt.Sprintf("__readpath_probe_%d", time.Now().UTC().UnixNano())
	sessionID := marker + "_sess"

	// 两条：一条标记 TRUE、一条 FALSE。**两个方向都要**——
	// 只测 TRUE 的话，「投影把该列写死成 FALSE」也会绿。
	for i, want := range []bool{true, false} {
		rid := fmt.Sprintf("%s_r%d", marker, i)
		_, err := tx.Exec(ctx, `
			INSERT INTO public.session_turns
				(session_id, turn_no, tenant_id, request_id, ts,
				 submit_mode, source_kind, quality, partition_date, attempt_no, tools,
				 is_final_success)
			VALUES ($1, $2, $3, $4, now(), 'full', 'live', 'verified', CURRENT_DATE, 0, '[]'::jsonb, $5)`,
			sessionID, i, "__readpath_tenant__", rid, want)
		if err != nil {
			t.Fatalf("seed turn (is_final_success=%v): %v", want, err)
		}
	}

	// 走**生产 SQL**，一字不改。db.SessionFamilyTurnsForSessionSQL() 是
	// admin/session_online.go:445 用的同一个函数。
	// session 谓词**已经在 $1 绑定**（下推进两条腿，外层不得再加 session_id/gw_session_id
	// 谓词——那是 db/request_logs_view_schema.go 的契约）。我第一版在外层写了
	// `WHERE rl.session_id = $1`，报 42703：投影里没有这一列。
	query := `
		SELECT rl.request_id, COALESCE(rl.is_final_success, FALSE)
		FROM ` + dbpkg.SessionFamilyTurnsForSessionSQL() + ` rl
		WHERE 1 = 1
		ORDER BY rl.request_id ASC`
	rows, err := tx.Query(ctx, query, sessionID)
	if err != nil {
		t.Fatalf("生产 SQL 查询失败: %v", err)
	}
	got := map[string]bool{}
	for rows.Next() {
		var rid string
		var fs bool
		if err := rows.Scan(&rid, &fs); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		got[rid] = fs
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("rows: %v", err)
	}
	rows.Close()

	if len(got) != 2 {
		t.Fatalf("生产 SQL 读回 %d 行（应为 2：一条 TRUE 一条 FALSE），实际 %v —— "+
			"本门的对照不成立，**不是**「读路坏」的证据，先查 fixture 与投影",
			len(got), got)
	}
	for i, want := range []bool{true, false} {
		rid := fmt.Sprintf("%s_r%d", marker, i)
		g, ok := got[rid]
		if !ok {
			t.Errorf("生产 SQL 没读回种进去的行 %s（读回集合 %v）", rid, got)
			continue
		}
		if g != want {
			t.Errorf("种的是 is_final_success=%v，生产 SQL 读回 %v —— "+
				"**读路本身有缺陷**（此时 §9.192 的结论要改：问题在投影而不在写侧）", want, g)
		}
	}
}
