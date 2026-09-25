package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestEnsureUsageFactsOccurredAtIndex_RealDB — migration 749 ensure 的真库
// 回归（2026-09-26 R67 24h 审计轮）。749 与 727/728/729/744 同族：canonical
// .sql 走升级通道 psql 实跑，Go 侧 boot 收敛路径必须有真库覆盖。本测试钉住：
//
//  1. DROP 掉父索引与分区子索引（模拟尚未应用/中断残留），ensure 必须走
//     三段式重建（逐分区 CONCURRENTLY → ONLY 壳 → ATTACH），父索引与
//     DEFAULT 分区子索引都存在且 indisvalid=true；
//  2. 第二次 ensure 必须幂等快返回（父索引已 valid 的 boot 常态路径），
//     且 schema_migrations 盖到 749 号章。
//
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过（与 744/745 真库回归同门控）。
// CONCURRENTLY 无法在事务内执行，pool.Exec 的 autocommit 天然满足。
// 纪律：修复子代理禁连库——本测试只随协调者的 scratch 容器单点执行
// （db/db_744_ensure_realdb_test.go 同款结构）。
func TestEnsureUsageFactsOccurredAtIndex_RealDB(t *testing.T) {
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

	const parentIdx = "idx_usage_facts_occurred_at"
	if _, err := pool.Exec(ctx, "DROP INDEX IF EXISTS public."+parentIdx); err != nil {
		t.Fatalf("drop parent index: %v", err)
	}
	if _, err := pool.Exec(ctx, "DROP INDEX IF EXISTS public.usage_facts_default_occurred_at_idx"); err != nil {
		t.Fatalf("drop default-partition index: %v", err)
	}

	if err := d.ensureUsageFactsOccurredAtIndex(ctx); err != nil {
		t.Fatalf("ensure round 1 (three-stage rebuild path): %v", err)
	}
	for _, idx := range []struct{ name, table string }{
		{parentIdx, "usage_facts"},
		{"usage_facts_default_occurred_at_idx", "usage_facts_default"},
	} {
		var valid bool
		if err := pool.QueryRow(ctx, `
			SELECT i.indisvalid
			FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid
			JOIN pg_class t ON t.oid = i.indrelid
			WHERE c.relname = $1 AND t.relname = $2
			  AND t.relnamespace = 'public'::regnamespace
		`, idx.name, idx.table).Scan(&valid); err != nil {
			t.Fatalf("re-check %s on %s: %v", idx.name, idx.table, err)
		}
		if !valid {
			t.Fatalf("%s rebuilt but indisvalid=false", idx.name)
		}
	}

	// 幂等第二遍：父索引已 valid 时必须零 DDL 快返回。
	if err := d.ensureUsageFactsOccurredAtIndex(ctx); err != nil {
		t.Fatalf("ensure round 2 (idempotent path): %v", err)
	}

	var stamped int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.schema_migrations WHERE version = '749'
	`).Scan(&stamped); err != nil {
		t.Fatalf("check stamp: %v", err)
	}
	if stamped != 1 {
		t.Fatalf("schema_migrations missing 749 stamp (got %d)", stamped)
	}
}
