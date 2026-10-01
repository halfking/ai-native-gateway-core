//go:build integration

// migration_715_integration_test.go — migration 715 真库验证（bba08b922
// 事件修复的数据库侧闭环）。
//
// 覆盖（单元测试只能钉 SQL 形状，真库才能钉行为）：
//
//	UP-1  389 旧约束库（现网形态）→ 应用 715：CHECK 放行 'pending'，
//	      部分唯一索引 WHERE 含 'pending'，既有 active/recovered 行不受影响；
//	UP-2  行为：'pending' 插入成功；同路由第二条 pending / active 命中
//	      唯一索引 23505；非法 state 命中 CHECK 23514；
//	UP-3  幂等：up 连续应用两次无错；
//	DOWN-1 pending 行存在时应用 down 必须失败（文件头告警的语义），
//	      且失败后 schema 保持新形态（单事务整体回滚）；
//	DOWN-2 清理 pending 行后 down 成功，旧 CHECK 拒绝 'pending'（23514），
//	      再重新应用 up 恢复新形态。
//
// 另有 TestMigration715FreshChainApplyMigrations：对一次性库直接跑真实
// db.Open（= ApplyMigrations 启动链），验证 ensureRouteIncidentPendingState
// 接线后全新安装路径在同一启动内从 389 旧约束升级到位——这正是 09-16 部署
// 验证发现缺失的那一环（无接线时首条 'pending' 写入即 23514）。
//
// 门控（沿用 migration_532 的 TEST_PG_URL 约定，指向一次性测试库）：
//
//	TEST_PG_URL=postgres://user:pass@localhost:5432/dbname?sslmode=disable \
//	go test -tags=integration ./sql/migrations/startup/ -run TestMigration715 -v -count=1
//
// 注意：FreshChain 会在目标库执行完整启动迁移链（数百对象），并可能需要
// 预置 schema_migrations 账本表（与生产引导一致）；务必指向可丢弃的库。
//
// FreshChain 的失败史（2026-10-02 定位，已修两层，剩一层）：
//
//  1. 原归因「01-schema 快照列漂移」是错的。实测 psql（ON_ERROR_STOP=0）
//     灌同一份快照零错误，父表有 application_id，分区正常挂载。
//
//  2. 它每次运行报**不同**的致命 SQLSTATE（42804 与 55000 各出现过一次），
//     这本身就说明不是某条 DDL 的确定性问题。真实成因有二：execTolerantSnapshot
//     在首个「非名单」SQLSTATE 处提前中止（与它注释声称复刻的 psql 语义相反，
//     已修）；以及本测试把全新安装快照灌在了**已经装满的门禁库**上——
//     引导报告 1705 条容忍错误、其中 42P07 占 1046 条，改用空库后归零。
//
//  3. 仍 FAIL，剩最后一层：本测试声称复刻生产全新安装，实际漏了中间那一步。
//     生产顺序是 00-prereqs + 01-schema 快照 → 安装器注册的 198 条启动迁移
//     （session_aggregate_outbox 由 630 建）→ 二进制启动链 db.Open。
//     本测试只做了 1 和 3，于是 db.Open 报 42P01
//     relation "public.session_aggregate_outbox" does not exist。
//
//     这一层没有在本轮补：注册清单在独立 Go module installer/ 的
//     dbinit.StartupFiles 里，根 module 的测试无法 import；而 sql/migrations/
//     startup/ 目录下有 793 个 .sql（含 .down.sql 与未注册的历史文件），
//     按文件名排序全量灌是错的。从测试里解析 installer 的 Go 源码来取清单
//     属于脆弱做法，不做。补法见 docs/audit/2026-10-02-round44-closure-migration-fixtures.md。
package startup

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/testdb"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

const fixtureDDL715 = `
-- 389 原始形态（ensureRouteIncidentSchema 镜像的旧 CHECK + 旧部分唯一索引）。
CREATE TABLE route_incidents (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           TEXT NOT NULL,
    endpoint_protocol   TEXT NOT NULL,
    model               TEXT NOT NULL,
    provider_id         BIGINT,
    credential_id       BIGINT,
    state               TEXT NOT NULL
        CHECK (state IN ('active', 'recovering', 'recovered')),
    failure_streak      INT  NOT NULL DEFAULT 0,
    recovery_streak     INT  NOT NULL DEFAULT 0,
    first_failure_at    TIMESTAMPTZ NOT NULL,
    last_failure_at     TIMESTAMPTZ,
    last_success_at     TIMESTAMPTZ,
    recovered_at        TIMESTAMPTZ,
    total_failures      BIGINT NOT NULL DEFAULT 0,
    total_successes     BIGINT NOT NULL DEFAULT 0,
    last_error_kind     TEXT,
    last_failure_stage  TEXT,
    resolution_source   TEXT,
    resolved_by_user    TEXT,
    resolved_reason     TEXT,
    version             BIGINT NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_route_incidents_active_route
    ON route_incidents (
        tenant_id, endpoint_protocol, model, COALESCE(provider_id, 0), COALESCE(credential_id, 0)
    )
    WHERE state IN ('active', 'recovering');
`

const fixtureCleanup715 = `DROP TABLE IF EXISTS route_incidents CASCADE;`

func setup715Fixture(t *testing.T, scriptConn *pgx.Conn) {
	t.Helper()
	execScript715(t, scriptConn, fixtureCleanup715)
	execScript715(t, scriptConn, fixtureDDL715)
	t.Cleanup(func() {
		//nolint:errcheck // best-effort cleanup
		_, _ = scriptConn.Exec(context.Background(), fixtureCleanup715)
	})

	// 既有现网形态行：一条 active（升级时已过阈值的历史事件）、一条 recovered。
	execScript715(t, scriptConn, `
		INSERT INTO route_incidents
		    (tenant_id, endpoint_protocol, model, provider_id, credential_id,
		     state, failure_streak, first_failure_at)
		VALUES
		    ('tenant-legacy', 'chat', 'gpt', 1, 1, 'active', 5, now()),
		    ('tenant-legacy', 'chat', 'claude', 1, 2, 'recovered', 3, now());
	`)
}

func execScript715(t *testing.T, conn *pgx.Conn, script string) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), script); err != nil {
		t.Fatalf("exec script: %v", err)
	}
}

func pgErrCode715(err error) (string, bool) {
	pgErr, ok := err.(*pgconn.PgError)
	if !ok {
		return "", false
	}
	return pgErr.Code, true
}

// splitSQLStatements 按顶层分号切分 SQL，正确跳过单引号字符串、双引号
// 标识符、行/块注释与 $tag$ dollar-quote 函数体。快照含大量 plpgsql
// 函数，朴素 strings.Split 不可用。
func splitSQLStatements(script string) []string {
	var stmts []string
	var b strings.Builder
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			stmts = append(stmts, s)
		}
		b.Reset()
	}
	i, n := 0, len(script)
	for i < n {
		c := script[i]
		switch {
		case c == '\'' || c == '"':
			quote := c
			b.WriteByte(c)
			i++
			for i < n {
				b.WriteByte(script[i])
				if script[i] == quote { // '' / "" 转义 = 连续两个同引号
					if i+1 < n && script[i+1] == quote {
						i++
						b.WriteByte(script[i])
					} else {
						i++
						break
					}
				}
				i++
			}
		case c == '$' && i+1 < n: // $tag$ dollar-quote（含 $$）
			j := i + 1
			for j < n && (isDollarTagChar(script[j])) {
				j++
			}
			if j < n && script[j] == '$' {
				tag := script[i : j+1]
				b.WriteString(tag)
				i = j + 1
				end := strings.Index(script[i:], tag)
				if end < 0 {
					b.WriteString(script[i:])
					i = n
				} else {
					b.WriteString(script[i : i+end+len(tag)])
					i += end + len(tag)
				}
			} else {
				b.WriteByte(c)
				i++
			}
		case c == '-' && i+1 < n && script[i+1] == '-':
			for i < n && script[i] != '\n' {
				b.WriteByte(script[i])
				i++
			}
		case c == '/' && i+1 < n && script[i+1] == '*':
			depth := 1
			b.WriteString("/*")
			i += 2
			for i < n && depth > 0 {
				if i+1 < n && script[i] == '/' && script[i+1] == '*' {
					depth++
					b.WriteString("/*")
					i += 2
				} else if i+1 < n && script[i] == '*' && script[i+1] == '/' {
					depth--
					b.WriteString("*/")
					i += 2
				} else {
					b.WriteByte(script[i])
					i++
				}
			}
		case c == ';':
			b.WriteByte(c)
			flush()
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	flush()
	return stmts
}

func isDollarTagChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// execTolerantSnapshot 复刻 init-local-db 的 psql（无 ON_ERROR_STOP）语义：
// 快照对裸库不是顺序安全的（视图/函数先于所引用的表出现），错误容忍跳过。
// 分块批量执行控制跨隧道往返；块内出错（隐式事务整体中止）时降级为块内
// 逐条执行。
//
// 关键：降级路径**不因任何服务端 SQL 错误中止**。这是 2026-10-02 的行为修正。
// 旧实现在遇到第一个「非容忍 SQLSTATE」时直接 return，与它自己注释里声称的
// psql 语义相反（psql 报错后继续执行后续语句）。后果不只是提前退出：容错地
// 跳过一条 CREATE 会留下半成品对象，使后面某条语句以一个**不在名单里**的
// SQLSTATE 失败，于是失败点取决于哪条语句先被跳过——
//
//	第 1 次运行：statement #2297 → 55000 cannot attach index ... as a partition of index
//	第 2 次运行：42804 table "request_logs_2026_07" contains column "application_id"
//	            not found in parent "request_logs"
//
// 同一份输入、两个不同的致命错误，说明这不是某条 DDL 的确定性问题，而是重放
// 机制在制造损坏的中间态。实测 psql（ON_ERROR_STOP=0）灌同一份 01-schema.sql
// 零错误，所以正确做法是让服务端错误全部容忍、让后续断言去判成败。
//
// 只有**非服务端**错误（连接断开、协议层失败）才中止——那种情况重放无意义。
// 被容忍的错误按 SQLSTATE 汇总后 t.Logf 出来，不静默。
func execTolerantSnapshot(t *testing.T, ctx context.Context, conn *pgx.Conn, script string) error {
	const chunkSize = 64 << 10
	stmts := splitSQLStatements(script)
	tolerated := map[string]int{}
	for start := 0; start < len(stmts); {
		end := start
		size := 0
		for end < len(stmts) && (size == 0 || size+len(stmts[end]) <= chunkSize) {
			size += len(stmts[end]) + 1
			end++
		}
		chunk := strings.Join(stmts[start:end], "\n")
		if _, err := conn.Exec(ctx, chunk); err != nil {
			// 出错块逐条降级；服务端可能停留在 aborted 事务，先复位。
			_, _ = conn.Exec(ctx, "ROLLBACK")
			for _, s := range stmts[start:end] {
				if _, err := conn.Exec(ctx, s); err != nil {
					if _, ok := pgErrCode715(err); !ok {
						// Not a server-reported error: the connection or the
						// protocol is gone, and replaying cannot help.
						return fmt.Errorf("snapshot execution failed outside statement replay: %w", err)
					}
					tolerated[pgErrCodeMust715(err)]++
					_, _ = conn.Exec(ctx, "ROLLBACK")
				}
			}
		}
		start = end
	}
	if len(tolerated) > 0 {
		codes := make([]string, 0, len(tolerated))
		for c := range tolerated {
			codes = append(codes, c)
		}
		sort.Strings(codes)
		parts := make([]string, 0, len(codes))
		total := 0
		for _, c := range codes {
			parts = append(parts, fmt.Sprintf("%s x%d", c, tolerated[c]))
			total += tolerated[c]
		}
		// Surfaced, not swallowed: a bootstrap that needed this much tolerance
		// is worth a reader seeing, and the caller still has to satisfy every
		// assertion below before the test passes.
		t.Logf("execTolerantSnapshot: tolerated %d statement errors while replaying chunks: %s",
			total, strings.Join(parts, ", "))
	}
	return nil
}

// pgErrCodeMust715 is pgErrCode715 for a caller that has already established
// the error IS a server error.
func pgErrCodeMust715(err error) string {
	code, _ := pgErrCode715(err)
	return code
}

func TestMigration715PendingStateLifecycle(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set; migration 715 integration test requires a live PostgreSQL (convention: tests/integration gating)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

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

	up, err := os.ReadFile("715_route_incidents_pending_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("715_route_incidents_pending_state.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	setup715Fixture(t, scriptConn)

	// UP-1 + UP-3：应用并重复应用（幂等）。
	execScript715(t, scriptConn, string(up))
	execScript715(t, scriptConn, string(up))

	var checkDef string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname = 'route_incidents_state_check'
		  AND conrelid = 'route_incidents'::regclass`).Scan(&checkDef); err != nil {
		t.Fatalf("read constraint: %v", err)
	}
	for _, state := range []string{"pending", "active", "recovering", "recovered"} {
		if !strings.Contains(checkDef, "'"+state+"'") {
			t.Errorf("UP-1: CHECK def missing %q: %s", state, checkDef)
		}
	}
	var indexPred string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(pg_get_expr(i.indpred, i.indrelid), '')
		FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'uq_route_incidents_active_route'`).Scan(&indexPred); err != nil {
		t.Fatalf("read index predicate: %v", err)
	}
	if !strings.Contains(indexPred, "pending") {
		t.Errorf("UP-1: unique index predicate must admit 'pending', got: %s", indexPred)
	}

	// 既有行不受升级影响。
	var legacy int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM route_incidents
		WHERE tenant_id = 'tenant-legacy' AND state IN ('active', 'recovered')`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy != 2 {
		t.Errorf("UP-1: legacy rows disturbed: got %d, want 2", legacy)
	}

	// UP-2：'pending' 可写入（修复的核心诉求——阈值前不可见的事件必须能落库）。
	if _, err := pool.Exec(ctx, `
		INSERT INTO route_incidents
		    (tenant_id, endpoint_protocol, model, provider_id, credential_id,
		     state, failure_streak, first_failure_at)
		VALUES ('tenant-a', 'chat', 'gpt', 1, 10, 'pending', 1, now())`); err != nil {
		t.Fatalf("UP-2: pending insert rejected: %v", err)
	}
	// 同路由第二条 pending → 唯一索引 23505。
	if _, err := pool.Exec(ctx, `
		INSERT INTO route_incidents
		    (tenant_id, endpoint_protocol, model, provider_id, credential_id,
		     state, failure_streak, first_failure_at)
		VALUES ('tenant-a', 'chat', 'gpt', 1, 10, 'pending', 2, now())`); err == nil {
		t.Fatal("UP-2: duplicate same-route pending must violate uq_route_incidents_active_route")
	} else if code, ok := pgErrCode715(err); !ok || code != "23505" {
		t.Fatalf("UP-2: want 23505, got %v", err)
	}
	// 同路由 active 同样互斥（索引 WHERE 的 active 分支）。
	if _, err := pool.Exec(ctx, `
		INSERT INTO route_incidents
		    (tenant_id, endpoint_protocol, model, provider_id, credential_id,
		     state, failure_streak, first_failure_at)
		VALUES ('tenant-a', 'chat', 'gpt', 1, 10, 'active', 3, now())`); err == nil {
		t.Fatal("UP-2: same-route active must violate the partial unique index while pending exists")
	} else if code, ok := pgErrCode715(err); !ok || code != "23505" {
		t.Fatalf("UP-2: want 23505, got %v", err)
	}
	// 非法 state → CHECK 23514。
	if _, err := pool.Exec(ctx, `
		INSERT INTO route_incidents
		    (tenant_id, endpoint_protocol, model, provider_id, credential_id,
		     state, failure_streak, first_failure_at)
		VALUES ('tenant-a', 'chat', 'gpt', 1, 11, 'bogus', 1, now())`); err == nil {
		t.Fatal("UP-2: bogus state must violate route_incidents_state_check")
	} else if code, ok := pgErrCode715(err); !ok || code != "23514" {
		t.Fatalf("UP-2: want 23514, got %v", err)
	}

	// DOWN-1：pending 行存在时 down 必须失败，且 schema 保持新形态。
	// 批内首错（23514）之后的语句以 25P02 报告——两者都算"down 被拒"。
	if _, err := scriptConn.Exec(ctx, string(down)); err == nil {
		t.Fatal("DOWN-1: down migration must fail while pending rows exist (documented hazard)")
	} else if code, ok := pgErrCode715(err); ok && code != "23514" && code != "25P02" {
		t.Fatalf("DOWN-1: want 23514/25P02, got %v", err)
	}
	// pgx 首错即返回，down 脚本内的 COMMIT 没送达——服务端事务停留在
	// aborted-open 状态，显式 ROLLBACK 复位（不在事务中时仅为 no-op 警告）。
	if _, err := scriptConn.Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatalf("DOWN-1: rollback recovery failed: %v", err)
	}
	var stillNew bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
			WHERE c.relname = 'uq_route_incidents_active_route'
			  AND pg_get_expr(i.indpred, i.indrelid) LIKE '%pending%'
		)`).Scan(&stillNew); err != nil {
		t.Fatal(err)
	}
	if !stillNew {
		t.Fatal("DOWN-1: failed down must leave the new (pending-aware) schema in place")
	}

	// DOWN-2：清理 pending 行后 down 成功；旧 CHECK 拒绝新 pending。
	if _, err := pool.Exec(ctx, `DELETE FROM route_incidents WHERE tenant_id = 'tenant-a'`); err != nil {
		t.Fatal(err)
	}
	execScript715(t, scriptConn, string(down))
	if _, err := pool.Exec(ctx, `
		INSERT INTO route_incidents
		    (tenant_id, endpoint_protocol, model, provider_id, credential_id,
		     state, failure_streak, first_failure_at)
		VALUES ('tenant-b', 'chat', 'gpt', 1, 12, 'pending', 1, now())`); err == nil {
		t.Fatal("DOWN-2: post-down old CHECK must reject 'pending'")
	} else if code, ok := pgErrCode715(err); !ok || code != "23514" {
		t.Fatalf("DOWN-2: want 23514, got %v", err)
	}
	// 旧索引在 down 后仍保护 active/recovering 互斥。
	execScript715(t, scriptConn, `
		INSERT INTO route_incidents
		    (tenant_id, endpoint_protocol, model, provider_id, credential_id,
		     state, failure_streak, first_failure_at)
		VALUES ('tenant-c', 'chat', 'gpt', 1, 13, 'active', 9, now())`)
	if _, err := pool.Exec(ctx, `
		INSERT INTO route_incidents
		    (tenant_id, endpoint_protocol, model, provider_id, credential_id,
		     state, failure_streak, first_failure_at)
		VALUES ('tenant-c', 'chat', 'gpt', 1, 13, 'recovering', 9, now())`); err == nil {
		t.Fatal("DOWN-2: old index must still enforce same-route active/recovering exclusivity")
	} else if code, ok := pgErrCode715(err); !ok || code != "23505" {
		t.Fatalf("DOWN-2: want 23505, got %v", err)
	}

	// 重新应用 up（down→up 循环收口）。
	execScript715(t, scriptConn, string(up))
}

// TestMigration715FreshChainApplyMigrations 对一次性库跑真实 db.Open：
// 启动链必须把 389 旧约束就地升级为 715 形态（全新安装路径）。
func TestMigration715FreshChainApplyMigrations(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set; fresh-chain test requires a disposable PostgreSQL")
	}
	// A scratch database, NOT the one TEST_PG_URL names. "Fresh install" is the
	// entire premise of this test: it replays 00-prereqs + 01-schema and then
	// the whole startup chain, and asserts the 389 -> 715 upgrade happened. Run
	// against the gate database (installer shape, 435 relations) the snapshot
	// lands on top of an already-migrated schema instead, and the test measures
	// the collision rather than the install: measured 2026-10-02, the bootstrap
	// reported 1705 tolerated errors of which 42P07 "already exists" was 1046,
	// and db.Open then failed with 42703 on provider_id. The 42804 and 55000
	// this test used to fail with were the same collision wearing different
	// SQLSTATEs on different runs.
	dsn = testdb.Create(t, dsn)

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_PG_URL: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	// 生产全新安装 = schema 快照引导（init-local-db：00-prereqs → 01-schema，
	// 快照是 715 之前的 389 旧形态）+ 二进制启动链。这里复刻同一路径：
	// 裸 db.Open 在空库上不可行——ensureRequestLogSchema 只 ALTER 不 CREATE。
	boot, err := pgx.ConnectConfig(context.Background(), cfg)
	if err != nil {
		t.Skipf("TEST_PG_URL unreachable: %v", err)
	}
	bootCtx, cancelBoot := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancelBoot()
	// 扩展最好已由引导安装；角色无权时逐条跳过（一次性实例通常预装）。
	for _, ext := range []string{
		"btree_gist WITH SCHEMA public", "pg_trgm WITH SCHEMA public",
		"pgcrypto WITH SCHEMA public", "plpgsql WITH SCHEMA pg_catalog",
		"pg_stat_statements WITH SCHEMA public", "pgstattuple WITH SCHEMA public",
		"citus WITH SCHEMA pg_catalog", "citus_columnar WITH SCHEMA pg_catalog",
		"vector WITH SCHEMA public",
	} {
		_, _ = boot.Exec(bootCtx,
			"CREATE EXTENSION IF NOT EXISTS "+ext) //nolint:errcheck // best-effort
	}
	for _, snapshot := range []string{"../../schema/00-prereqs.sql", "../../schema/01-schema.sql"} {
		snapSQL, err := os.ReadFile(snapshot)
		if err != nil {
			t.Fatalf("read bootstrap %s: %v", snapshot, err)
		}
		if err := execTolerantSnapshot(t, bootCtx, boot, string(snapSQL)); err != nil {
			t.Fatalf("apply bootstrap %s: %v", snapshot, err)
		}
	}
	t.Cleanup(func() { _ = boot.Close(context.Background()) })

	// 生产引导在启动链之前创建迁移账本（apply-db-revision-sequence 同款），
	// ensure 链内的 stamp（701/704/715）依赖它。
	_, _ = boot.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS public.schema_migrations (
			version      text PRIMARY KEY,
			description  text,
			applied_at   timestamp with time zone DEFAULT now()
		)`)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	gw, err := dbpkg.Open(ctx, dsn)
	if err != nil {
		// The original skip text here claimed a "pre-existing schema snapshot
		// drift (request_logs_hot column types vs parent, 42804 on view
		// rebuild)". That cause is DISPROVEN by measurement, 2026-10-02:
		//
		//   * sql/schema/01-schema.sql applied to an empty database with psql
		//     and ON_ERROR_STOP=0 completes with zero errors;
		//   * afterwards public.request_logs HAS application_id, and
		//     request_logs_2026_07 IS attached (pg_inherits = 1), with 74
		//     monthly partitions for 2026;
		//   * no ALTER/ADD/DROP COLUMN touches request_logs between its
		//     CREATE (line 7205) and the ATTACH (line 19528).
		//
		// The observed 42804 therefore does not come from the snapshot. It is
		// produced by execTolerantSnapshot's chunked replay: a failing 64 KiB
		// chunk is rolled back and re-run statement by statement, and a
		// partition ATTACH can be reached in that replay while the parent is
		// momentarily short a column. 42804 is not in
		// isIgnorableSnapshotError, so the helper returns it as fatal — even
		// though the database it left behind is complete and correct (verified:
		// 154 columns, partition attached). The failure site is also not this
		// one; the same 42804 aborts earlier, inside execTolerantSnapshot at
		// the bootstrap call, so the guard below never sees it.
		//
		// 42804 is deliberately NOT added to isIgnorableSnapshotError to turn
		// this green: that list also governs the column-type drift this test
		// is meant to catch, and widening it would hide the real defect
		// instead of the test harness's own noise. Fixing the replay needs its
		// own change.
		msg := err.Error()
		if strings.Contains(msg, "rebuild request_logs base wrapper view") &&
			strings.Contains(msg, "42804") {
			t.Skipf("fresh-install chain broken at the request_logs view rebuild: %v", err)
		}
		t.Fatalf("db.Open (full startup chain) failed: %v", err)
	}
	defer gw.Close()

	pool := gw.Pool()
	var checkDef string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname = 'route_incidents_state_check'
		  AND conrelid = 'route_incidents'::regclass`).Scan(&checkDef); err != nil {
		t.Fatalf("fresh chain: route_incidents constraint missing: %v", err)
	}
	if !strings.Contains(checkDef, "'pending'") {
		t.Fatalf("fresh chain: constraint was not upgraded to 715 form: %s", checkDef)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO route_incidents
		    (tenant_id, endpoint_protocol, model, provider_id, credential_id,
		     state, failure_streak, first_failure_at)
		VALUES ('tenant-fresh', 'chat', 'gpt', 1, 1, 'pending', 1, now())`); err != nil {
		t.Fatalf("fresh chain: 'pending' write rejected after full startup chain: %v", err)
	}
	var stamped bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM public.schema_migrations WHERE version = '715')`).Scan(&stamped); err != nil {
		t.Fatal(err)
	}
	if !stamped {
		t.Fatal("fresh chain: migration 715 ledger stamp missing")
	}
}
