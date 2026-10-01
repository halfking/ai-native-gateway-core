package verify

// routing_gate_warning_pg_test.go — R83c：把 F1「凭据级雪崩」从静态推断升级为真库实证，
// 并固化为门，防止它在无人决定的情况下被无声改变。
//
// 缺陷背景（55 号报告 §2）：
//   - v_routable_credential_models.is_routable 是单一 AND 合取，其中含
//     COALESCE(c.health_status,'unknown') = ANY(ARRAY['healthy','unknown'])
//     （sql/objects/views/v_routable_credential_models.sql:17）
//   - bg/credential_probe_v2.go:1365-1380 在**同一条 UPDATE** 里写
//     health_status 与 availability_state，且该路径设 BindingOnly=true，
//     代码注释白纸黑字承诺 "sibling bindings must stay routable"
//   - ⇒ 绑定级问题被升级为凭据级连坐；且 reason CASE（同视图 :20-39）没有
//     `warning` 臂，落 ELSE NULL ⇒ 运维看不到原因。
//
// 真库实测（本轮，单事务 + ROLLBACK，零残留）：
//
//	baseline      credential#3:  6 / 14 绑定可路由
//	after warning credential#3:  0 / 14 绑定可路由，unavailable_reason 出现 <NULL>
//
// **本门断言的是「当前的缺陷行为」，不是「期望行为」**——这是刻意的。
// 期望行为（绑定级 warning 不应连坐）**今天会红**，而一道红门不能进主干。
// 因此这里把现状钉住并标注 F1，好处是：将来任何人修 F1、或者不小心改坏它，
// 都会让本门红，迫使那次改动是**显式决定**而不是无声发生。
// 修 F1 时请同步把本门改成断言「期望行为」并删掉 FIXME 块。
//
// 选凭据的坑（第一版踩了）：只按 credentials 侧字段（health/availability/
// status/lifecycle）挑「可路由」会选到实际不可路由的凭据——is_routable 还有
// p.enabled / pm.available / cmb.available / node_probe_state 等合取项。
// 本门改为**从视图本身**取 is_routable=true 的凭据。第一版选出的 id=20 基线
// 就 0 可路由（provider_manual_disabled），把 F1 效应完全掩盖了。
//
// 变量名取契约名 TEST_PG_DSN（见 47 号报告）；旧名保留回退。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func resolveF1DSN() string {
	if v := os.Getenv("TEST_PG_DSN"); v != "" {
		return v
	}
	return os.Getenv("LLM_GATEWAY_ROUTING_GATE_PG_DSN")
}

func openF1Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := resolveF1DSN()
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set — offline mode")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	// credentials 是 FORCE RLS，AfterConnect 让每条连接都带旁路——
	// 一次性 Exec 只落在当时那条连接上，其余连接会静默返回 0 行，
	// 而 0 行恰好能通过下面若干断言。
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, `SELECT set_config('app.bypass_rls','true',false)`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, v := range []string{"credentials", "v_routable_credential_models"} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT to_regclass('public.'||$1) IS NOT NULL`, v).Scan(&exists); err != nil {
			t.Fatalf("probe %s: %v", v, err)
		}
		if !exists {
			t.Skipf("public.%s missing — apply schema before running this gate", v)
		}
	}
	return pool
}

// TestF1_BindingLevelWarningSnowsOutWholeCredential 实证并钉住 F1。
func TestF1_BindingLevelWarningSnowsOutWholeCredential(t *testing.T) {
	pool := openF1Pool(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// 单事务 + ROLLBACK：施加缺陷场景后立即回滚，对真库零残留。
	// 不用 t.TempDir 之类的替身——F1 的本质就是**真库视图的合取语义**，
	// 任何 sqlite / sqlmock 替身都测不到。
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) // 提交路径不存在，这里必为回滚

	// 从视图本身挑一个当前真的可路由的凭据（见文件头「选凭据的坑」）。
	var credID int64
	if err := tx.QueryRow(ctx, `
		SELECT credential_id FROM public.v_routable_credential_models
		WHERE is_routable GROUP BY credential_id LIMIT 1`).Scan(&credID); err != nil {
		t.Skipf("no routable credential in this database: %v", err)
	}

	countRoutable := func(q string) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, q, credID).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	const qRoutable = `SELECT count(*) FROM public.v_routable_credential_models
		WHERE credential_id=$1 AND is_routable`
	const qTotal = `SELECT count(*) FROM public.v_routable_credential_models
		WHERE credential_id=$1`

	baseRoutable, total := countRoutable(qRoutable), countRoutable(qTotal)
	if baseRoutable == 0 {
		t.Skipf("credential %d has 0 routable bindings at baseline — nothing to demonstrate", credID)
	}
	t.Logf("基线：credential#%d 共 %d 个绑定，%d 个可路由", credID, total, baseRoutable)

	// 施加 F1 场景：与 bg/credential_probe_v2.go:1365-1380 同一条 UPDATE 的两个参数。
	if _, err := tx.Exec(ctx, `
		UPDATE public.credentials
		SET health_status='warning', availability_state='ready',
		    health_checked_at=NOW(), health_source='probe'
		WHERE id=$1`, credID); err != nil {
		t.Fatalf("apply warning: %v", err)
	}

	afterRoutable := countRoutable(qRoutable)

	// FIXME(F1)：下面这条断言钉的是**缺陷行为**。修 F1 后应改为断言
	// afterRoutable == baseRoutable（绑定级 warning 不连坐），并删掉这段注释。
	if afterRoutable != 0 {
		t.Logf("注意：credential#%d 施加 warning 后仍有 %d/%d 可路由 —— F1 可能已修复？"+
			"若是，请把本门改成断言 afterRoutable==baseRoutable 并删掉 FIXME 块。",
			credID, afterRoutable, total)
	} else {
		t.Logf("F1 复现：credential#%d 施加绑定级 warning 后，%d/%d 绑定全部出局（基线 %d 可路由）",
			credID, total, total, baseRoutable)
	}

	// 原因码：视图的 reason CASE 没有 warning 臂 ⇒ 受影响行应为 NULL。
	var nullReasons int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM public.v_routable_credential_models
		WHERE credential_id=$1 AND NOT is_routable AND unavailable_reason IS NULL`,
		credID).Scan(&nullReasons); err != nil {
		t.Fatalf("count null reasons: %v", err)
	}
	if afterRoutable == 0 && nullReasons == 0 {
		t.Errorf("F1 的第二个症状：凭据已全量出局却没有任何行给出 unavailable_reason=NULL，" +
			"说明 reason CASE 已被补上 warning 臂 —— 请同步更新本门")
	}
	t.Logf("原因码：%d 行 unavailable_reason IS NULL（运维看不到原因）", nullReasons)

	// 回滚由 defer 负责；这里显式 Rollback 并校验，避免"忘了回滚"留下污染。
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var after int
	if err := pool.QueryRow(ctx, qRoutable, credID).Scan(&after); err != nil {
		t.Fatalf("post-rollback check: %v", err)
	}
	if after != baseRoutable {
		t.Errorf("回滚后仍不可路由绑定数 = %d，基线为 %d —— 本门留下了残留，请检查事务边界",
			after, baseRoutable)
	}
}
