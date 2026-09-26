package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
// 函数体为搬移后挂接（全步骤同事务），pool.Exec 的 autocommit 天然满足。
// 纪律：修复子代理禁连库——本测试只随协调者的 scratch 容器单点执行。
// 「DEFAULT 已有当日行」路径由 DefaultOverlap 同族测试钉住。
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

	// 安装/刷新 canonical 750 SQL（函数 + 当日/次日预建调用），与
	// DefaultOverlap 测试同款——真实 boot 顺序里 canonical 先于 Go ensure。
	sqlBytes, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup",
		"750_usage_facts_daily_partition.sql"))
	if err != nil {
		t.Fatalf("read canonical 750 sql: %v", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	if _, err := conn.Conn().PgConn().Exec(ctx, string(sqlBytes)).ReadAll(); err != nil {
		t.Fatalf("apply canonical 750 sql: %v", err)
	}
	conn.Release()

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

// TestEnsureUsageFactsDailyPartition_DefaultOverlap_RealDB 钉住 2026-09-26
// 当日修订审计 P1：目标日的行已经落进 DEFAULT 分区（存量库/跨午夜停机
// 场景）时，ensure 必须把它们搬进新建日分区再挂接，而不是让 PG 的
// default-partition 约束校验把 CREATE/ATTACH 炸掉——那会在 boot ensure
// 路径把网关打进 no-DB 模式。初版（ac007c2f8）的 scratch 四通道实证
// 未覆盖此路径。用 current_date+40 作探测日，避免与 boot ensure 的
// 当日/次日分区互相干扰；测试后清理探测分区。
func TestEnsureUsageFactsDailyPartition_DefaultOverlap_RealDB(t *testing.T) {
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

	// 安装/刷新 canonical 函数（simple protocol 承载多语句文件）。
	sqlBytes, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup",
		"750_usage_facts_daily_partition.sql"))
	if err != nil {
		t.Fatalf("read canonical 750 sql: %v", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Conn().PgConn().Exec(ctx, string(sqlBytes)).ReadAll(); err != nil {
		t.Fatalf("apply canonical 750 sql: %v", err)
	}

	// 1) 探测日 = current_date + 40：先插 3 行，全部路由进 DEFAULT。
	const probeRows = 3
	for i := 0; i < probeRows; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO usage_facts (event_id, request_id, status, occurred_at)
			VALUES ($1, $2, 'ok', (current_date + 40)::timestamptz + make_interval(secs => $3))
		`, fmt.Sprintf("overlap-probe-%d", i), fmt.Sprintf("req-overlap-%d", i), i); err != nil {
			t.Fatalf("seed probe row %d: %v", i, err)
		}
	}
	var inDefaultBefore int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM usage_facts_default
		WHERE occurred_at >= (current_date + 40)::timestamptz
		  AND occurred_at <  (current_date + 41)::timestamptz
	`).Scan(&inDefaultBefore); err != nil {
		t.Fatalf("count probe rows in DEFAULT (before): %v", err)
	}
	if inDefaultBefore != probeRows {
		t.Fatalf("precondition: probe rows in DEFAULT = %d, want %d", inDefaultBefore, probeRows)
	}

	// 2) ensure：必须搬移后挂接成功，而不是被 default 分区约束校验拒绝。
	if _, err := pool.Exec(ctx, `SELECT ensure_usage_facts_daily_partition(current_date + 40)`); err != nil {
		t.Fatalf("ensure with rows in DEFAULT must succeed via move-then-attach, got: %v", err)
	}

	// 3) 行已搬入新分区，DEFAULT 界内清零，父表视角总数守恒。
	var pname string
	if err := pool.QueryRow(ctx, `
		SELECT 'usage_facts_' || to_char(current_date + 40, 'YYYYMMDD')
	`).Scan(&pname); err != nil {
		t.Fatalf("derive probe partition name: %v", err)
	}
	var attached bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		  WHERE i.inhparent = 'public.usage_facts'::regclass AND c.relname = $1
		)`, pname).Scan(&attached); err != nil || !attached {
		t.Fatalf("probe partition not attached (err=%v attached=%v)", err, attached)
	}
	var moved int
	if err := pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT count(*) FROM %s`, pname)).Scan(&moved); err != nil {
		t.Fatalf("count moved rows: %v", err)
	}
	if moved != probeRows {
		t.Fatalf("moved rows = %d, want %d", moved, probeRows)
	}
	var inDefaultAfter int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM usage_facts_default
		WHERE occurred_at >= (current_date + 40)::timestamptz
		  AND occurred_at <  (current_date + 41)::timestamptz
	`).Scan(&inDefaultAfter); err != nil {
		t.Fatalf("count probe rows in DEFAULT (after): %v", err)
	}
	if inDefaultAfter != 0 {
		t.Fatalf("probe rows left in DEFAULT = %d, want 0", inDefaultAfter)
	}
	var viaParent int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM usage_facts WHERE event_id LIKE 'overlap-probe-%'
	`).Scan(&viaParent); err != nil || viaParent != probeRows {
		t.Fatalf("parent view count = %d (err=%v), want %d", viaParent, err, probeRows)
	}

	// 4) 幂等：同日再调一次不报错、行数不变。
	if _, err := pool.Exec(ctx, `SELECT ensure_usage_facts_daily_partition(current_date + 40)`); err != nil {
		t.Fatalf("ensure round 2 (idempotent): %v", err)
	}
	if err := pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT count(*) FROM %s`, pname)).Scan(&moved); err != nil || moved != probeRows {
		t.Fatalf("after round 2 moved rows = %d (err=%v), want %d", moved, err, probeRows)
	}

	// 5) 清理探测分区（DETACH 后 DROP，行随分区删掉，DEFAULT 不动）。
	if _, err := pool.Exec(ctx,
		fmt.Sprintf(`ALTER TABLE usage_facts DETACH PARTITION %s`, pname)); err != nil {
		t.Fatalf("cleanup detach: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, pname)); err != nil {
		t.Fatalf("cleanup drop: %v", err)
	}
}