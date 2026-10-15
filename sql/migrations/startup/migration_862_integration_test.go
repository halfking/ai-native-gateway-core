//go:build integration

// migration_862_integration_test.go —— r1030 真库判据：862 的守卫腿与
// COMMENT 腿必须同守卫。
//
// 静态判据（migration_862_test.go）只能钉「顶格 COMMENT」这一形态，钉不住
// 「注释所指向的索引在这一环境里到底建没建成」——而那才是真缺陷的现场。
// 2026-10-14 r1030 用隔离的 PG18 实例逐字复现了它：
//
//	public.session_turns (id, ts) 存在、public._request_logs 不存在
//	  → 建腿 DO 块正确地 NOTICE 跳过
//	  → 脚本末尾无守卫的 COMMENT ON INDEX idx_request_logs_ts
//	  → ERROR: relation "idx_request_logs_ts" does not exist，psql exit=3
//
// 影响面按 dbinit 的真实 apply 通道判定（installer/internal/dbinit/runner.go
// applySQL = `psql -v ON_ERROR_STOP=1`，只按退出码记账）：该迁移永远不被记为
// applied，每次 boot 重放、每次同样失败；而 CREATE INDEX IF NOT EXISTS 早已
// 命中，索引其实一直在场。测试因此照运行器口径要求「整文件零错误」。
//
// 门控（沿用 migration_532/715 的 TEST_PG_URL 约定）：
//
//	TEST_PG_URL=postgres://user:pass@localhost:5432/dbname?sslmode=disable \
//	go test -tags=integration ./sql/migrations/startup -run TestMigration862 -v -count=1
//
// 本文件自己建一次性库（internal/testdb.Create），不碰 TEST_PG_URL 指向的库。
package startup

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kaixuan/llm-gateway-go/internal/testdb"
)

// fixture862OnlySessionTurns 建出**只有 session_turns 族**的形态：
// 真实的 RANGE 分区父表 + 两个既存月度子分区，且**没有** _request_logs 族。
// 这正是 860 的守卫分支（「not found (skip)」）所描述的环境。
const fixture862OnlySessionTurns = `
CREATE TABLE public.session_turns (id bigserial, ts timestamptz NOT NULL) PARTITION BY RANGE (ts);
CREATE TABLE public.session_turns_2026_09 PARTITION OF public.session_turns
	FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE public.session_turns_2026_10 PARTITION OF public.session_turns
	FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
`

// fixture862BothFamilies 在上一形态上补齐 860 之后的 request_logs 族。
const fixture862BothFamilies = `
CREATE TABLE public._request_logs (id bigserial, ts timestamptz NOT NULL) PARTITION BY RANGE (ts);
CREATE TABLE public._request_logs_2026_10 PARTITION OF public._request_logs
	FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
`

func TestMigration862AbsentRequestLogsFamilyStillApplies(t *testing.T) {
	conn := connectScratch862(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	exec862(t, ctx, conn, fixture862OnlySessionTurns)

	// 整文件必须零错误（applySQL 的记账口径）。
	up := readMigration862(t)
	exec862(t, ctx, conn, up)
	// 幂等：重放一次（IF NOT EXISTS 语义）。
	exec862(t, ctx, conn, up)

	// 缺席族的场景下，承重的那条索引仍必须在场、且带 862 注释。
	assertRegclass862(t, ctx, conn, "public.idx_session_turns_ts", true)
	assertHas862Comment(t, ctx, conn, "public.idx_session_turns_ts")
	// 分区父表级联：既存月度分区拿到子索引（862 的全部前提）。
	assertRegclass862(t, ctx, conn, "public.session_turns_2026_09_ts_idx", true)
	assertRegclass862(t, ctx, conn, "public.session_turns_2026_10_ts_idx", true)
	// 缺席族那条**不该**凭空出现。
	assertRegclass862(t, ctx, conn, "public.idx_request_logs_ts", false)
}

func TestMigration862BothFamiliesBuildsAndCommentsBothLegs(t *testing.T) {
	conn := connectScratch862(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	exec862(t, ctx, conn, fixture862OnlySessionTurns+fixture862BothFamilies)

	up := readMigration862(t)
	exec862(t, ctx, conn, up)
	exec862(t, ctx, conn, up)

	assertRegclass862(t, ctx, conn, "public.idx_session_turns_ts", true)
	assertRegclass862(t, ctx, conn, "public.idx_request_logs_ts", true)
	assertHas862Comment(t, ctx, conn, "public.idx_session_turns_ts")
	assertHas862Comment(t, ctx, conn, "public.idx_request_logs_ts")
	assertRegclass862(t, ctx, conn, "public._request_logs_2026_10_ts_idx", true)

	// down 对称：两条父级索引一起消失。
	exec862(t, ctx, conn, readMigration862Down(t))
	assertRegclass862(t, ctx, conn, "public.idx_session_turns_ts", false)
	assertRegclass862(t, ctx, conn, "public.idx_request_logs_ts", false)
}

func connectScratch862(t *testing.T) *pgx.Conn {
	t.Helper()

	base := os.Getenv("TEST_PG_URL")
	if base == "" {
		t.Skip("TEST_PG_URL not set; migration 862 integration test requires a live PostgreSQL " +
			"(convention: //go:build integration + TEST_PG_URL, same as 532/715)")
	}
	scratch := testdb.Create(t, base)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg, err := pgx.ParseConfig(scratch)
	if err != nil {
		t.Fatalf("parse scratch DSN: %v", err)
	}
	// 简单协议：整份迁移文件按一个批次送进去，与 psql 的 ON_ERROR_STOP=1
	// 同样「任一句出错即整批失败」——这正是 applySQL 的记账口径。
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect scratch DB: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func readMigration862(t *testing.T) string {
	t.Helper()
	return repoFile(t, "sql/migrations/startup/"+idx862File)
}

func readMigration862Down(t *testing.T) string {
	t.Helper()
	return repoFile(t, "sql/migrations/startup/862_partition_family_leading_ts_idx.down.sql")
}

// exec862 把整段脚本作为一个批次执行；任何错误都直接判失败并附上 SQLSTATE，
// 以便一眼区分「脚本语法/DDL 错」与「连不上」。
func exec862(t *testing.T, ctx context.Context, conn *pgx.Conn, script string) {
	t.Helper()
	if _, err := conn.Exec(ctx, script); err != nil {
		t.Fatalf("apply 862 script failed: %v", err)
	}
}

func assertRegclass862(t *testing.T, ctx context.Context, conn *pgx.Conn, name string, want bool) {
	t.Helper()
	var got bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", name).Scan(&got); err != nil {
		t.Fatalf("probe %s: %v", name, err)
	}
	if got != want {
		t.Errorf("relation %s exists = %v, want %v", name, got, want)
	}
}

func assertHas862Comment(t *testing.T, ctx context.Context, conn *pgx.Conn, name string) {
	t.Helper()
	var comment *string
	if err := conn.QueryRow(ctx,
		"SELECT obj_description(to_regclass($1)::oid)", name).Scan(&comment); err != nil {
		t.Fatalf("read comment of %s: %v", name, err)
	}
	if comment == nil {
		t.Errorf("index %s has no COMMENT at all —— 守卫块里的 COMMENT 没有真正执行", name)
		return
	}
	if !strings.Contains(*comment, "862") {
		t.Errorf("index %s comment = %q, want it to mention 862 —— 守卫块里的 COMMENT 没有真正执行", name, *comment)
	}
}
