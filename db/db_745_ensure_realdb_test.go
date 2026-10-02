package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestEnsureReportSnapshots_RealDB — migration 745 ensure 的真库回归
// （2026-09-24 对账报表设计切片）。745 原文件死放 migrations/ 顶层、无任何
// 投递通道（审计确认的死文件 + 五点同步全缺），本测试把 boot 收敛路径钉住：
//
//	1. DROP 掉表（模拟尚未应用/回滚残留），ensure 必须重建，且 UNIQUE 约束
//	   report_snapshots_scope_key_date_raw_model_key 四列在位（粒度契约：
//	   scope×scope_key×report_date×raw_model_name —— 无模型维度的旧三键
//	   形状装不下 provider×model×day，见 745 迁移头注记②）、raw_model_name
//	   为 NOT NULL DEFAULT '' 哨兵列；
//	2. 第二次 ensure 必须幂等快返回（information_schema 存在性短路的
//	   boot 常态路径），不重复执行任何 DDL。
//
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过（与 744 真库回归同门控）。
// 纪律：修复子代理禁连库——本测试只随协调者的 scratch 容器单点执行
//（db/db_744_ensure_realdb_test.go 同款结构，只写不跑）。
func TestEnsureReportSnapshots_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	defer pool.Close()
	d := &DB{pool: pool}

	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS public.report_snapshots"); err != nil {
		t.Fatalf("drop report_snapshots: %v", err)
	}

	if err := d.ensureReportSnapshots(ctx); err != nil {
		t.Fatalf("ensure round 1 (create path): %v", err)
	}

	var uniqueDef string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(c.oid)::text
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		WHERE t.relname = 'report_snapshots' AND c.contype = 'u'
	`).Scan(&uniqueDef); err != nil {
		t.Fatalf("re-check unique constraint: %v", err)
	}
	// 列名集合断言：把约束定义里的括号/逗号归一成空白后逐词比对，
	// (scope, scope_key, report_date, raw_model_name) 四列缺一即红。
	replaced := strings.NewReplacer("(", " ", ")", " ", ",", " ").Replace(uniqueDef)
	words := map[string]bool{}
	for _, w := range strings.Fields(replaced) {
		words[strings.ToLower(w)] = true
	}
	for _, col := range []string{"scope", "scope_key", "report_date", "raw_model_name"} {
		if !words[col] {
			t.Fatalf("UNIQUE constraint lost column %q (got %q) — 745 granularity contract broken", col, uniqueDef)
		}
	}

	var rawModelNullable bool
	if err := pool.QueryRow(ctx, `
		SELECT is_nullable = 'YES'
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'report_snapshots'
		  AND column_name = 'raw_model_name'
	`).Scan(&rawModelNullable); err != nil {
		t.Fatalf("re-check raw_model_name column: %v", err)
	}
	if rawModelNullable {
		t.Fatal("raw_model_name must be NOT NULL DEFAULT '' (non-model-dimension sentinel)")
	}

	start := time.Now()
	if err := d.ensureReportSnapshots(ctx); err != nil {
		t.Fatalf("ensure round 2 (idempotent path): %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("second ensure took %s — the information_schema short-circuit is not skipping DDL", elapsed)
	}
}

// TestEnsureReportSnapshots746Upgrade_RealDB — 746 存量库升级路径回归
//（2026-09-25 对账报表落地轮）：手工建出 745 旧形状（tenant_id BIGINT、
// 无 credits/latency 三列），ensure 必须原位升级到 746 最终形状：
//
//	1. tenant_id 数据类型 = text（对齐 usage_facts 文本租户键）；
//	2. credits_charged / latency_p50_ms / latency_p95_ms 三列在位且
//	   NOT NULL DEFAULT 0；
//	3. 二次 ensure 幂等（columnsAllPresent 短路，不再跑 ALTER）。
//
// 门控与 TestEnsureReportSnapshots_RealDB 相同（TEST_DATABASE_URL /
// TEST_DB_URL，scratch 库单点执行）。
func TestEnsureReportSnapshots746Upgrade_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	defer pool.Close()
	d := &DB{pool: pool}

	// 重建 745 旧形状：bigint tenant_id、无 746 三列。
	if _, err := pool.Exec(ctx, `
		DROP TABLE IF EXISTS public.report_snapshots;
		CREATE TABLE public.report_snapshots (
		    id                  BIGSERIAL PRIMARY KEY,
		    scope               TEXT NOT NULL,
		    scope_key           TEXT NOT NULL,
		    report_date         DATE NOT NULL,
		    raw_model_name      TEXT NOT NULL DEFAULT '',
		    granularity         TEXT NOT NULL DEFAULT 'day',
		    request_count       BIGINT NOT NULL DEFAULT 0,
		    success_count       BIGINT NOT NULL DEFAULT 0,
		    error_count         BIGINT NOT NULL DEFAULT 0,
		    input_tokens        BIGINT NOT NULL DEFAULT 0,
		    output_tokens       BIGINT NOT NULL DEFAULT 0,
		    cache_read_tokens   BIGINT NOT NULL DEFAULT 0,
		    cache_write_tokens  BIGINT NOT NULL DEFAULT 0,
		    error_kind_breakdown JSONB NOT NULL DEFAULT '{}'::jsonb,
		    cache_hit_ratio     NUMERIC(6,4),
		    estimated_cost_cents BIGINT NOT NULL DEFAULT 0,
		    currency            TEXT NOT NULL DEFAULT 'USD',
		    price_snapshot      JSONB NOT NULL DEFAULT '{}'::jsonb,
		    provider_id         BIGINT,
		    canonical_id        BIGINT,
		    tenant_id           BIGINT,
		    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
		    CONSTRAINT report_snapshots_scope_key_date_raw_model_key
		        UNIQUE (scope, scope_key, report_date, raw_model_name)
		);
	`); err != nil {
		t.Fatalf("recreate 745-shape table: %v", err)
	}

	if err := d.ensureReportSnapshots(ctx); err != nil {
		t.Fatalf("ensure upgrade path: %v", err)
	}

	type colDef struct {
		dataType     string
		isNullable   string
		columnDefaul string
	}
	cols := map[string]colDef{}
	rows, err := pool.Query(ctx, `
		SELECT column_name, data_type, is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'report_snapshots'
	`)
	if err != nil {
		t.Fatalf("inspect columns: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, dataType, isNullable, def string
		if err := rows.Scan(&name, &dataType, &isNullable, &def); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		cols[name] = colDef{dataType: dataType, isNullable: isNullable, columnDefaul: def}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}

	if got := cols["tenant_id"].dataType; got != "text" {
		t.Errorf("tenant_id type = %q, want text (746 alignment with usage_facts)", got)
	}
	for _, c := range []string{"credits_charged", "latency_p50_ms", "latency_p95_ms"} {
		def, ok := cols[c]
		if !ok {
			t.Errorf("column %q missing after 746 upgrade", c)
			continue
		}
		if def.dataType != "bigint" || def.isNullable != "NO" || def.columnDefaul != "0" {
			t.Errorf("column %q = %+v, want bigint NOT NULL DEFAULT 0", c, def)
		}
	}

	start := time.Now()
	if err := d.ensureReportSnapshots(ctx); err != nil {
		t.Fatalf("ensure round 2 (idempotent path): %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("second ensure took %s — columnsAllPresent guard is not skipping the 746 ALTER", elapsed)
	}
}
