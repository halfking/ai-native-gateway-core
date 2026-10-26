// auto_index_refresher_rollup_realdb_test.go — 2026-10-20 退役轮：
// rollupCredentialModelIndexSQL 的**真库可执行性**门。
//
// ★ 这道门存在的理由（缺陷本体）：
//
//	890（v1 族真删）把 half-1 的 v1 腿整条删掉、换成会话腿。会话腿内层投影
//	是 8 个**表达式**，而外层沿用 v1 腿的列名引用它们
//	（rl.credential_id / rl.model_key / rl.mo_canonical_id / rl.mo_billing_mode /
//	  rl.mo_price_in / rl.mo_price_out / rl.success / rl.latency_ms）。
//	未补别名 ⇒ PG 把表达式列命名成 "?column?" ⇒ 外层 42703
//	（column rl.credential_id does not exist）⇒ **整条 rollup 失败** ⇒
//	autoroute 每 5s 刷新报错、凭据健康/压力指标停更（活库日志实测每 5s 一条）。
//
// ★ 为什么不能用「纯文本形状门」兜住：既有门
//
//	（auto_index_refresher_s3_wave3_test.go）只数 "FROM session_turns_hot t"
//	的出现次数、查 "FROM _request_logs_hot" 是否消失、查 alias 链子串——
//	这些**都不涉及列解析** ⇒ 少一个别名它照样绿。
//	⇒ 只能靠「真库执行」把这类错误钉住。
//
// 零足迹：整条语句在显式事务里跑，末尾 ROLLBACK（不落任何行）。
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过。
package bg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func rollupRealDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Skipf("parse dsn: %v", err)
	}
	// 生产全量 SimpleProtocol（db/db.go）——参数内联语义只有该模式可证。
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestRollupCredentialModelIndex_ExecutesOnRealDB 把整条 rollup 语句丢给
// 真库解析+执行：任何列解析错（42703）、关系不存在（42P01）、语法错都直接
// 变成本门红。断言「不报错」+「产出至少一行」。
func TestRollupCredentialModelIndex_ExecutesOnRealDB(t *testing.T) {
	pool := rollupRealDBPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 先确认会话族在位（v1 退役后的承重腿）；不在 ⇒ 跳过而不是误报红。
	var ok bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.session_turns_hot') IS NOT NULL
		    AND to_regclass('public.credential_model_index_hot') IS NOT NULL`,
	).Scan(&ok); err != nil {
		t.Skipf("probe session family: %v", err)
	}
	if !ok {
		t.Skip("会话族/credential_model_index_hot 不在位，跳过（需要装过 890 之后的库）")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// 显式回滚：整条 rollup 是写语句（INSERT ... ON CONFLICT），必须零足迹。
	defer func() { _ = tx.Rollback(ctx) }()

	// ⚠ 桶必须取一个**不可能被 dedup 命中**的值，否则「产出 0 行」是合法结果、
	// 断言会假红（2026-10-20 实测踩到过）：
	//
	// rollup 的收尾有一层 dedup：当 (credential_id, raw_model) 在**上一个
	// bucket** 的指标（success_rate / p95 / 三个 score）与本次完全相同时，
	// 本次这一行被跳过（这是 2026-07-13 那次「97.1% 的插入是纯重复」的
	// 优化）。⇒ 若上一次 bucket 就在眼前（运行中的实例每 5 分钟写一次），
	// 且指标没变，INSERT 合法地产出 0 行。
	//
	// 取一年前的桶 ⇒ dedup 的比较窗口（[bucket-7d, bucket)）里没有任何行
	// ⇒ 必然走插入路径 ⇒ 「产出 ≥ 1 行」才真正说明「指标源在供数」。
	// 该桶不在任何真实 bucket 序列里，写进去的行随事务 ROLLBACK 消失 ⇒
	// 零足迹；也不会被 promote 搬走（没有提交）。
	bucket := time.Now().UTC().Add(-365 * 24 * time.Hour).Truncate(time.Minute)

	tag, err := tx.Exec(ctx, rollupCredentialModelIndexSQL, bucket)
	if err != nil {
		t.Fatalf("rollup 语句在真库执行失败：%v\n（这一条就是退役改写引入的列解析/关系错误；"+
			"语句本体：\n%s", err, rollupCredentialModelIndexSQL)
	}
	if tag.RowsAffected() == 0 {
		t.Fatalf("rollup 执行成功但产出 0 行：会话腿与基线腿都空 ⇒ 指标源断供")
	}
}
