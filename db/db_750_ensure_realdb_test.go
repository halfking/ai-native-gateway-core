package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestEnsureUsageFactsDailyPartition_RealDB — migration 750 ensure 的真库
// 回归（2026-09-26 R68 24h 审计轮）。750 与 749 同族：canonical .sql 走
// 升级通道 psql 实跑，Go 侧 boot 收敛路径必须有真库覆盖。本测试钉住：
//
//  1. 首次 ensure 必须成功创建今日 + 次日具体 RANGE 分区，DEFAULT 分区
//     保留作历史 catch-all；
//  2. 第二次 ensure 必须幂等快返回，分区计数与首次一致；
//  3. schema_migrations 盖到 750 号章。
//
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过（与 749 真库回归同门控）。
// CREATE TABLE PARTITION OF 不需事务（IF NOT EXISTS 幂等），pool.Exec 的
// autocommit 天然满足。纪律：修复子代理禁连库——本测试只随协调者的
// scratch 容器单点执行。
func TestEnsureUsageFactsDailyPartition_RealDB(t *testing.T) {
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

	// Round 1：第一次 ensure 必须创建今日+次日分区，DEFAULT 仍挂。
	if err := d.ensureUsageFactsDailyPartition(ctx); err != nil {
		t.Fatalf("ensure round 1: %v", err)
	}
	partsAfterRound1, err := countUsageFactsPartitions(ctx, pool)
	if err != nil {
		t.Fatalf("count partitions round 1: %v", err)
	}
	if !hasDailyPartition(ctx, pool, "today") {
		t.Fatalf("today partition missing after round 1")
	}
	if !hasDailyPartition(ctx, pool, "tomorrow") {
		t.Fatalf("tomorrow partition missing after round 1")
	}
	if !defaultPartitionAttached(ctx, pool) {
		t.Fatalf("DEFAULT partition should remain attached (historical catch-all)")
	}

	// Round 2：幂等，分区数与 round 1 一致。
	if err := d.ensureUsageFactsDailyPartition(ctx); err != nil {
		t.Fatalf("ensure round 2 (idempotent): %v", err)
	}
	partsAfterRound2, err := countUsageFactsPartitions(ctx, pool)
	if err != nil {
		t.Fatalf("count partitions round 2: %v", err)
	}
	if partsAfterRound1 != partsAfterRound2 {
		t.Fatalf("partition count drift: round1=%d round2=%d (ensure should be idempotent)",
			partsAfterRound1, partsAfterRound2)
	}

	// schema_migrations 盖到 750 号章。
	var stamped int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.schema_migrations WHERE version = '750'
	`).Scan(&stamped); err != nil {
		t.Fatalf("check stamp: %v", err)
	}
	if stamped != 1 {
		t.Fatalf("schema_migrations missing 750 stamp (got %d)", stamped)
	}
}

func countUsageFactsPartitions(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_inherits WHERE inhparent = 'public.usage_facts'::regclass
	`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func hasDailyPartition(ctx context.Context, pool *pgxpool.Pool, day string) bool {
	var todayDate string
	switch day {
	case "today":
		todayDate = "current_date"
	case "tomorrow":
		todayDate = "current_date + 1"
	default:
		return false
	}
	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM pg_class c
		  JOIN pg_inherits i ON i.inhrelid = c.oid
		  WHERE i.inhparent = 'public.usage_facts'::regclass
		    AND c.relname = 'usage_facts_' || to_char(`+todayDate+`, 'YYYYMMDD')
		)
	`).Scan(&exists); err != nil {
		return false
	}
	return exists
}

func defaultPartitionAttached(ctx context.Context, pool *pgxpool.Pool) bool {
	var attached bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM pg_inherits i
		  JOIN pg_class c ON c.oid = i.inhrelid
		  WHERE i.inhparent = 'public.usage_facts'::regclass
		    AND c.relname = 'usage_facts_default'
		)
	`).Scan(&attached); err != nil {
		return false
	}
	return attached
}