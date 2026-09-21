// sql_audit_realdb_test.go — 2026-09-21 252 PG SQL 日志审计复核轮的自审计
// 补充真库回归（F1/F2）。
// 字面量，PG17 解析成 interval+interval 必炸）与 F2（REFRESH 被角色级
// statement_timeout=30s 击杀）两缺陷 pgxmock 单测只能断言 SQL 字符串，
// 测不出 PG 语义；修复合入时也未通过 Go 驱动对真库执行过。本文件补齐：
// 必须连真 PG 才有意义，无 TEST_DATABASE_URL / TEST_DB_URL 时跳过。
package bg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// simpleProtocolPool 构造与生产一致的连接池（db/db.go 全局
// QueryExecModeSimpleProtocol——F1 的炸弹只在该模式下被内联引爆）。
// MaxConns 钳到 1：RESET 断言必须复用刷新用过的同一条连接，否则
// 多连接池可能抽到从未被 SET 污染的连接造成假阴性。
func simpleProtocolPool(t *testing.T) *pgxpool.Pool {
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
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestFeatureStatsHalfOpenWindow_RealDB 回归 F1：生产同文本的特征分布
// 聚合 SQL（含 $1::timestamptz cast）必须能在 SimpleProtocol 真库上解析
// 并执行。回归点：若有人把 cast 改回 `$1 + INTERVAL '1 day'`，本测试在
// PG17 上直接报 "invalid input syntax for type interval"（252 生产 R43
// 起每轮必炸的实锤形态）。事务内执行并回滚，零落库足迹。
func TestFeatureStatsHalfOpenWindow_RealDB(t *testing.T) {
	pool := simpleProtocolPool(t)
	ctx := context.Background()

	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.auto_route_selections') IS NOT NULL
		      AND to_regclass('public.feature_distribution_stats') IS NOT NULL`,
	).Scan(&exists); err != nil {
		t.Fatalf("probe tables: %v", err)
	}
	if !exists {
		t.Skip("auto_route_selections/feature_distribution_stats 不存在，跳过")
	}

	statDate := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 生产同一构造函数（featureDistributionQuery）产出的文本，两个参数
	// 均经 SimpleProtocol 内联——等价于生产日志里抓到的失败形态。
	if _, err := tx.Exec(ctx, featureDistributionQuery("detected_language"), statDate, "detected_language"); err != nil {
		t.Fatalf("feature distribution query failed under SimpleProtocol: %v", err)
	}
}

// TestMaterializedViewRefresher_TimeoutLiftAndReset_RealDB 回归 F2：
// refreshView 两条路径（advisory-lock / leader）都必须在固定连接上
// SET statement_timeout→REFRESH→RESET；归还连接池后同一条连接的语句
// 上限必须回到进入前基线。回归点：删掉 RESET → 池内唯一连接带 180s
// 残留被本测试抓获。MaxConns=1 保证断言精确。
func TestMaterializedViewRefresher_TimeoutLiftAndReset_RealDB(t *testing.T) {
	pool := simpleProtocolPool(t)
	ctx := context.Background()

	const probeView = "bg_mv_refresh_probe"
	// CONCURRENTLY 要求唯一索引。
	stmts := []string{
		`DROP MATERIALIZED VIEW IF EXISTS public.` + probeView,
		`CREATE MATERIALIZED VIEW public.` + probeView + ` AS SELECT 1 AS x`,
		`CREATE UNIQUE INDEX ON public.` + probeView + ` (x)`,
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DROP MATERIALIZED VIEW IF EXISTS public.`+probeView)
	})

	// 基线：进入刷新前的语句上限（真库可能是角色级 30s 或 0）。
	var baseline string
	if err := pool.QueryRow(ctx, "SHOW statement_timeout").Scan(&baseline); err != nil {
		t.Fatalf("read baseline statement_timeout: %v", err)
	}

	r := NewMaterializedViewRefresher(pool)
	if err := r.refreshView(ctx, probeView, true); err != nil {
		t.Fatalf("refreshView(advisory-lock path): %v", err)
	}
	if err := r.refreshView(ctx, probeView, false); err != nil {
		t.Fatalf("refreshView(leader path): %v", err)
	}

	// MaxConns=1 ⇒ 这里复用的必然是刷新用过的那条连接：RESET 缺失即暴露。
	var after string
	if err := pool.QueryRow(ctx, "SHOW statement_timeout").Scan(&after); err != nil {
		t.Fatalf("read statement_timeout after refresh: %v", err)
	}
	if after != baseline {
		t.Fatalf("statement_timeout after refresh = %q, want baseline %q（RESET 缺失或连接污染）",
			after, baseline)
	}
}
