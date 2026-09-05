//go:build integration

// migration_532_integration_test.go — v4 T7 migration 532 真库验证。
//
// 覆盖测试方案 G10 的库级断言（单元测试只能钉 SQL 形状）：
//
//	UT-FS-01  并发/连续 claim 下同 gw_session_id 至多一条 is_final_success=TRUE
//	          （部分唯一索引 23505 兜底 + NOT EXISTS claim 语义）；
//	UT-FS-02  5 次重发回归（migration 054 场景）：同会话 5 行 success，仅 1 行
//	          拿到 final-success 标记，其余保持 FALSE（不回改历史）；
//	另验：空/NULL gw_session_id 不受约束、promoted 分区侧 claim 互斥、
//	      ensure_request_logs_partition 为新分区补索引、视图暴露新列、幂等重放。
//
// 门控（沿用 tests/integration 的 TEST_PG_URL 约定，指向一次性测试库）。
// 2026-08-18 复核：初版注释称 testcontainers 在当前 vendor 集合下无法编译；
// 实测 repair_529_integration_test.go 的 testcontainers 用法可编译
// （go vet -tags=integration ./sql/migrations/startup/ 通过），如需容器化
// fixture 可参照该先例；本测试维持 TEST_PG_URL 约定不变。
//
//	TEST_PG_URL=postgres://user:pass@localhost:5432/dbname?sslmode=disable \
//	go test -tags=integration ./sql/migrations/startup/ -run TestMigration532 -v -count=1
//
// 测试在 TEST_PG_URL 指向的库（请指向一次性测试库，勿指向生产/共享库）的
// public schema 中搭建最小 fixture（hot 表 + 分区母表 + 一个分区 + 视图）——
// migration 532 的分区索引 DO 循环按 public 限定过滤 pg_inherits，fixture 必
// 须与生产同名同 schema 才能验证真实路径；结束时逐对象 DROP IF EXISTS 清理。
package startup

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// claimSQLFinalSuccess 与 telemetry/client.go claimSessionFinalSuccess 的语句
// 保持一致（如改动那边，请同步这里）。
const claimSQLFinalSuccess = `
	UPDATE request_logs_hot
	   SET is_final_success = TRUE
	 WHERE request_id = $1
	   AND success = TRUE
	   AND request_status = 'success'
	   AND COALESCE(gw_session_id, '') <> ''
	   AND NOT EXISTS (
	        SELECT 1
	          FROM request_logs_hot other
	         WHERE other.gw_session_id = request_logs_hot.gw_session_id
	           AND other.is_final_success
	           AND other.request_id <> request_logs_hot.request_id
	   )
	   AND NOT EXISTS (
	        SELECT 1
	          FROM request_logs promoted
	         WHERE promoted.gw_session_id = request_logs_hot.gw_session_id
	           AND promoted.is_final_success
	   )
`

const fixtureCleanup532 = `
DROP VIEW IF EXISTS request_logs_with_current_month;
DROP TABLE IF EXISTS request_logs_2099_01;
DROP TABLE IF EXISTS request_logs_2099_03;
DROP TABLE IF EXISTS request_logs CASCADE;
DROP TABLE IF EXISTS request_logs_hot;
DROP FUNCTION IF EXISTS ensure_request_logs_partition(timestamp with time zone);
`

const fixtureDDL532 = `
-- ensure_request_logs_partition（migration 532 重建版）会在新分区上建
-- search_text/client_model 的 gin_trgm_ops 索引，fixture 需要这两列与 pg_trgm。
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE TABLE request_logs_hot (
    id bigserial PRIMARY KEY,
    request_id text NOT NULL,
    ts timestamptz NOT NULL,
    tenant_id text NOT NULL,
    gw_session_id text,
    success boolean NOT NULL,
    request_status text,
    error_kind text,
    search_text text,
    client_model text
);
CREATE TABLE request_logs (
    id bigint,
    request_id text NOT NULL,
    ts timestamptz NOT NULL,
    tenant_id text NOT NULL,
    gw_session_id text,
    success boolean NOT NULL,
    request_status text,
    error_kind text,
    search_text text,
    client_model text
) PARTITION BY RANGE (ts);
CREATE TABLE request_logs_2099_01 PARTITION OF request_logs
    FOR VALUES FROM ('2099-01-01') TO ('2099-02-01');
CREATE VIEW request_logs_with_current_month AS
SELECT id, request_id, ts, tenant_id, gw_session_id, success, request_status, error_kind FROM request_logs_hot
UNION ALL
SELECT id, request_id, ts, tenant_id, gw_session_id, success, request_status, error_kind FROM request_logs;
`

func TestMigration532FinalSuccessUniqueness(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set; migration 532 integration test requires a live PostgreSQL (convention: tests/integration gating)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// 简单协议连接：migration 文件含 BEGIN/COMMIT 与 DO $$ 块的多语句脚本。
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_PG_URL: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	scriptConn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Skipf("TEST_PG_URL unreachable: %v", err)
	}
	defer func() { _ = scriptConn.Close(ctx) }()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()

	execScript := func(script string) {
		t.Helper()
		if _, err := scriptConn.Exec(ctx, script); err != nil {
			t.Fatalf("exec script: %v", err)
		}
	}

	// fixture + 清理（见 fixtureCleanup532 注释：TEST_PG_URL 必须指向一次性库）。
	execScript(fixtureCleanup532)
	t.Cleanup(func() {
		//nolint:errcheck // best-effort cleanup
		_, _ = scriptConn.Exec(context.Background(), fixtureCleanup532)
	})
	execScript(fixtureDDL532)

	migration, err := os.ReadFile("532_request_logs_final_success.sql")
	if err != nil {
		t.Fatal(err)
	}
	execScript(string(migration))

	// 幂等：重复应用必须无错。
	execScript(string(migration))

	// fixture 在 public 下，identity 包装保持调用点不变。
	inSchema := func(sql string) string { return sql }

	// 1) 列与索引落位。
	for _, check := range []struct {
		name  string
		query string
	}{
		{"hot column", `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='request_logs_hot' AND column_name='is_final_success')`},
		{"parent column", `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='request_logs' AND column_name='is_final_success')`},
		{"partition column cascade", `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='request_logs_2099_01' AND column_name='is_final_success')`},
		{"view column", `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='request_logs_with_current_month' AND column_name='is_final_success')`},
		{"hot unique index", `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname='uq_request_logs_hot_final_success_session')`},
		{"partition unique index", `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname='uq_request_logs_2099_01_final_success_session')`},
	} {
		var got bool
		if err := pool.QueryRow(ctx, inSchema(check.query)).Scan(&got); err != nil {
			t.Fatalf("%s: %v", check.name, err)
		}
		if !got {
			t.Errorf("%s: expected true, got false", check.name)
		}
	}

	// 2) UT-FS-02：5 次重发（migration 054 场景）——同会话 5 行 success，
	// 逐行跑 claim，最终恰 1 行 TRUE，5 行 success 全部保留。
	for i := 0; i < 5; i++ {
		reqID := fmt.Sprintf("req_054_%d", i)
		if _, err := pool.Exec(ctx, inSchema(`
			INSERT INTO request_logs_hot (request_id, ts, tenant_id, gw_session_id, success, request_status)
			VALUES ($1, now() + ($2 || ' seconds')::interval, 'default', 'sess-054', TRUE, 'success')`),
			reqID, fmt.Sprintf("%d", i)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, inSchema(claimSQLFinalSuccess), reqID); err != nil {
			t.Fatalf("claim #%d: %v", i, err)
		}
	}
	var finalCount, successRows int
	if err := pool.QueryRow(ctx, inSchema(
		`SELECT count(*) FROM request_logs_hot WHERE gw_session_id='sess-054' AND is_final_success`)).Scan(&finalCount); err != nil {
		t.Fatal(err)
	}
	if finalCount != 1 {
		t.Fatalf("UT-FS-02: final success rows = %d, want 1", finalCount)
	}
	if err := pool.QueryRow(ctx, inSchema(
		`SELECT count(*) FROM request_logs_hot WHERE gw_session_id='sess-054' AND success`)).Scan(&successRows); err != nil {
		t.Fatal(err)
	}
	if successRows != 5 {
		t.Fatalf("history rewritten: success rows = %d, want 5 (rows must be preserved)", successRows)
	}

	// 3) UT-FS-01（索引兜底）：绕过 NOT EXISTS 直接批量双标记必须被 23505 拒绝。
	_, err = pool.Exec(ctx, inSchema(
		`UPDATE request_logs_hot SET is_final_success = TRUE WHERE gw_session_id = 'sess-054'`))
	if err == nil {
		t.Fatal("expected 23505 when force-marking multiple rows, got nil")
	} else {
		pgErr, ok := err.(*pgconn.PgError)
		if !ok || pgErr.Code != "23505" {
			t.Fatalf("expected 23505, got %v", err)
		}
	}

	// 4) 空/NULL gw_session_id 不受约束（两行都可为 TRUE）。
	nullSession := any(nil)
	emptySession := any("")
	for _, row := range []struct {
		id   string
		sess any
	}{{"req_null_sess", nullSession}, {"req_empty_sess", emptySession}} {
		if _, err := pool.Exec(ctx, inSchema(`
			INSERT INTO request_logs_hot (request_id, ts, tenant_id, gw_session_id, success, request_status, is_final_success)
			VALUES ($1, now(), 'default', $2, TRUE, 'success', TRUE)`), row.id, row.sess); err != nil {
			t.Fatalf("%s: %v (empty-session rows must bypass the partial unique index)", row.id, err)
		}
	}

	// 5) promoted 分区侧互斥：分区里已持有 claim 的会话，hot 侧 claim 必须 no-op。
	if _, err := pool.Exec(ctx, inSchema(`
		INSERT INTO request_logs (id, request_id, ts, tenant_id, gw_session_id, success, request_status, is_final_success)
		VALUES (9001, 'req_promoted_1', '2099-01-05T00:00:00Z', 'default', 'sess-cross', TRUE, 'success', TRUE)`)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, inSchema(`
		INSERT INTO request_logs_hot (request_id, ts, tenant_id, gw_session_id, success, request_status)
		VALUES ('req_hot_cross', now(), 'default', 'sess-cross', TRUE, 'success')`)); err != nil {
		t.Fatal(err)
	}
	tag, err := pool.Exec(ctx, inSchema(claimSQLFinalSuccess), "req_hot_cross")
	if err != nil {
		t.Fatalf("cross-window claim exec: %v", err)
	}
	if tag.RowsAffected() != 0 {
		t.Fatal("cross-window claim must be a no-op when the promoted side already holds the claim")
	}

	// 6) 失败行不 claim（语句自守卫）。
	if _, err := pool.Exec(ctx, inSchema(`
		INSERT INTO request_logs_hot (request_id, ts, tenant_id, gw_session_id, success, request_status, error_kind)
		VALUES ('req_fail_guard', now(), 'default', 'sess-fail', FALSE, 'failure', 'upstream_error')`)); err != nil {
		t.Fatal(err)
	}
	tag, err = pool.Exec(ctx, inSchema(claimSQLFinalSuccess), "req_fail_guard")
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 0 {
		t.Fatal("failure rows must never claim")
	}

	// 7) ensure_request_logs_partition 为新分区补唯一索引。
	if _, err := pool.Exec(ctx, inSchema(
		`SELECT ensure_request_logs_partition('2099-03-15'::timestamptz)`)); err != nil {
		t.Fatal(err)
	}
	var newPartIdx bool
	if err := pool.QueryRow(ctx, inSchema(
		`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname='uq_request_logs_2099_03_final_success_session')`)).Scan(&newPartIdx); err != nil {
		t.Fatal(err)
	}
	if !newPartIdx {
		t.Fatal("ensure_request_logs_partition did not create the final-success index on the new partition")
	}
}
