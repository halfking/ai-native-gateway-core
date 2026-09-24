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
