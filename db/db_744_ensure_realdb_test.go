package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestEnsureSqlAuditPartialIndexes_RealDB — migration 744 ensure 的真库回归
// （2026-09-24 252 SQL 日志审计第六轮）。727/728/729 只有 canonical .sql +
// 当轮手工 psql 实跑，Go 侧 boot 收敛路径没有真库覆盖；本测试把它钉住：
//
//	1. DROP 掉 hot 侧索引（模拟尚未应用/中断残留），ensure 必须重建且
//	   indisvalid=true（INVALID/缺失都会被 buildConcurrently 处理）；
//	2. 第二次 ensure 必须幂等快返回（索引已 valid 时的 boot 路径）。
//
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过（与 bg 真库回归同门控）。
// CONCURRENTLY 无法在事务内执行，pool.Exec 的 autocommit 天然满足。
func TestEnsureSqlAuditPartialIndexes_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	defer pool.Close()
	d := &DB{pool: pool}

	const hotIdx = "idx_session_turns_hot_digest_null"
	if _, err := pool.Exec(ctx, "DROP INDEX IF EXISTS public."+hotIdx); err != nil {
		t.Fatalf("drop %s: %v", hotIdx, err)
	}

	if err := d.ensureSqlAuditPartialIndexes(ctx); err != nil {
		t.Fatalf("ensure round 1 (rebuild path): %v", err)
	}
	var valid bool
	if err := pool.QueryRow(ctx, `
		SELECT i.indisvalid
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		WHERE i.indrelid = 'public.session_turns_hot'::regclass
		  AND c.relname = $1
	`, hotIdx).Scan(&valid); err != nil {
		t.Fatalf("re-check %s: %v", hotIdx, err)
	}
	if !valid {
		t.Fatalf("%s rebuilt but indisvalid=false", hotIdx)
	}

	start := time.Now()
	if err := d.ensureSqlAuditPartialIndexes(ctx); err != nil {
		t.Fatalf("ensure round 2 (idempotent path): %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("second ensure took %s — the valid-index short-circuit is not skipping CONCURRENTLY builds", elapsed)
	}
}
