//go:build integration

// migration_r76_attach_slot_integration_test.go — R76 轮真库门：
// **复现原缺陷形态**（PG 自动命名的成员已占住槽位）并断言三份迁移**零报错 + 零孤儿**。
//
// 为什么需要真库腿：静态门只能证明「守卫的写法对了」，证明不了
// 「在这个形态下迁移真的会跑过」⇒ 真库腿把缺陷形态摆出来，跑真迁移，看结果。
//
// 门控（与同包既有门同一约定）：
//
//	TEST_PG_URL=postgres://user:pass@localhost:5432/<一次性库>?sslmode=disable \
//	go test -tags=integration ./sql/migrations/startup/ -run TestMigrationR76 -count=1 -v
//
// ⚠ TEST_PG_URL 必须指向**可丢弃**的一次性库：本门会建表、建索引、跑整份迁移。
// ⚠ 库名必须匹配 bench|test|tmp|scratch|pg_ 之一（同包其它门的既有约定）。
package startup

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestMigrationR76AttachSlotRealDB —— 真库门：复现**原缺陷形态**并断言零孤儿 + 零报错。
//
// 夹具刻意先用一个**普通 CREATE INDEX**（不带 ON ONLY）在父表上建索引：
// ⇒ PG 会自动给每个分区建匹配索引并**自动命名**（`<part>_<column>_idx`）
// ⇒ 这正是生产 252 的真实形态，也是缺陷的触发前提。
//
// 断言：
//
//	P1 三份迁移全部 exit=0（补丁前 877/887/888 在此形态下报 42P01 槽位冲突）；
//	P2 补丁后**零孤儿**（没有「同义但不同名、且未挂父」的重复索引）；
//	P3 每个分区在父索引下**确实有成员**（能力没有因为跳过 ATTACH 而丢）。
func TestMigrationR76AttachSlotRealDB(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	dsn, terminate := r76RealDB(ctx, t)
	defer terminate()

	conn, err := r76Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("连不上目标库：%v", err)
	}
	defer conn.Close(ctx)

	// ★ pg_trgm 是 trigram 索引的前提（877/887 的定义依赖 gin_trgm_ops）。
	//   外部库未必装了这个扩展 ⇒ 门自己幂等补上（测试库，允许）。
	if _, err := conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pg_trgm"); err != nil {
		t.Fatalf("建 pg_trgm 扩展失败：%v", err)
	}

	// ── 夹具：会话族父表 + 两个分区 + hot 表 ────────────────────────────
	// ⚠ 用真名（与迁移里的 regclass 字面量一致）⇒ 否则迁移会因找不到关系而失败，
	//   而那与本门要守的槽位冲突无关。
	fixture := []string{
		`DROP VIEW IF EXISTS session_turns_with_current_month`,
		`DROP TABLE IF EXISTS session_turns_hot CASCADE`,
		`DROP TABLE IF EXISTS session_turns CASCADE`,
		`CREATE TABLE session_turns (id bigint, search_text text, tenant_id text,
			session_id text, turn_no int, request_id text, partition_date date)
			PARTITION BY RANGE (partition_date)`,
		`CREATE TABLE session_turns_2026_09 PARTITION OF session_turns
			FOR VALUES FROM ('2026-09-01') TO ('2026-10-01')`,
		`CREATE TABLE session_turns_2026_10 PARTITION OF session_turns
			FOR VALUES FROM ('2026-10-01') TO ('2026-11-01')`,
		`CREATE TABLE session_turns_hot (id bigint, search_text text)`,
		// ★ 888 的两个父索引族（session_bodies / sessions）也要在夹具里，
		//   否则 888 会因「relation does not exist」失败 —— 那与本门要守的
		//   槽位冲突无关，却会让门测不到东西（假红）。
		`DROP TABLE IF EXISTS session_bodies_hot CASCADE`,
		`DROP TABLE IF EXISTS session_bodies CASCADE`,
		`DROP TABLE IF EXISTS sessions CASCADE`,
		`CREATE TABLE session_bodies (id bigint, tenant_id text, session_id text,
			turn_no int, request_id text, partition_date date)
			PARTITION BY RANGE (partition_date)`,
		`CREATE TABLE session_bodies_2026_10 PARTITION OF session_bodies
			FOR VALUES FROM ('2026-10-01') TO ('2026-11-01')`,
		`CREATE TABLE session_bodies_hot (id bigint, tenant_id text, session_id text,
			turn_no int, request_id text)`,
		`CREATE TABLE sessions (id bigint, tenant_id text, session_id text,
			partition_date date) PARTITION BY RANGE (partition_date)`,
		`CREATE TABLE sessions_2026_10 PARTITION OF sessions
			FOR VALUES FROM ('2026-10-01') TO ('2026-11-01')`,
	}
	for _, stmt := range fixture {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("夹具失败 (%s): %v", r76FirstLine(stmt), err)
		}
	}

	// ★ 缺陷触发前提：普通 CREATE INDEX（不带 ON ONLY）⇒ PG 自动给分区建成员并自动命名。
	//   生产 252 上父索引的 6 个成员就是这样来的（实测全是 `<part>_search_text_idx`）。
	if _, err := conn.Exec(ctx,
		`CREATE INDEX idx_session_turns_search_trgm ON public.session_turns
			USING gin (search_text gin_trgm_ops)`); err != nil {
		t.Fatalf("建父索引失败：%v", err)
	}

	// ★ 三份迁移逐份应用。补丁前 877/887/888 在此形态下报
	//   「cannot attach index … as a partition of index …」。
	for _, name := range r76Migrations {
		runR76Migration(t, ctx, conn, name)
	}

	// P2：零孤儿（未挂父的同族名索引）。
	const orphanQuery = `
		SELECT count(*) FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		WHERE (c.relname LIKE '%\_search\_trgm' OR c.relname LIKE '%\_search\_fts')
		  AND c.relname NOT IN ('idx_session_turns_search_trgm',
		                        'idx_session_turns_search_fts',
		                        'idx_session_turns_hot_search_trgm',
		                        'idx_session_turns_hot_search_fts')
		  AND NOT EXISTS (SELECT 1 FROM pg_inherits h WHERE h.inhrelid = c.oid)`
	var orphans int
	if err := conn.QueryRow(ctx, orphanQuery).Scan(&orphans); err != nil {
		t.Fatalf("查孤儿失败：%v", err)
	}
	if orphans != 0 {
		t.Errorf("孤儿索引 %d 个（未挂父的同族名重复索引）\n"+
			"⇒ 第 1 段在「该分区已有成员」时仍在建重复索引 ⇒ 写放大翻倍且永远挂不上。", orphans)
	}

	// P3：每个分区在父索引下确实有成员（跳过 ATTACH 不等于丢能力）。
	const memberQuery = `
		SELECT c.relname, count(*) FROM pg_inherits h
		JOIN pg_class p ON p.oid = h.inhparent
		JOIN pg_index mi ON mi.indexrelid = h.inhrelid
		JOIN pg_class c ON c.oid = mi.indrelid
		WHERE p.relname = 'idx_session_turns_search_trgm'
		  AND c.relname LIKE 'session_turns%'
		GROUP BY c.relname ORDER BY c.relname`
	rows, err := conn.Query(ctx, memberQuery)
	if err != nil {
		t.Fatalf("查成员失败：%v", err)
	}
	defer rows.Close()
	members := 0
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatalf("扫描成员失败：%v", err)
		}
		if n != 1 {
			t.Errorf("%s 在父索引下有 %d 个成员（应为 1）", name, n)
		}
		members++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历成员失败：%v", err)
	}
	if members < 2 {
		t.Errorf("父索引下只有 %d 个分区有成员（夹具有 2 个月分区）⇒ 能力丢了", members)
	}
}

// runR76Migration 把一份迁移文件应用到连接上。
//
// ⚠ 迁移里含 `\gexec`（psql 元命令），pgx 不认它。
//
//	本门用与同包既有门（migration_877_integration_test.go）相同的办法：
//	按**整行**切出 `\gexec`，把生成器 SELECT 跑掉取回 DDL，逐条执行，再执行其余部分。
//	⚠ 切分必须按整行：`-- dbinit:no-transaction` 那一行与文件头注释里都出现过
//	  "gexec" 这个词，裸子串切会把注释劈开。
func runR76Migration(t *testing.T, ctx context.Context, conn *pgx.Conn, name string) {
	t.Helper()
	raw := readMigrationContract(t, name)
	const gexecLine = "\n\\gexec\n"
	segments := strings.Split(raw, gexecLine)
	if len(segments) < 2 {
		t.Fatalf("%s：应当至少有一个独占一行的 \\gexec 元命令", name)
	}
	for i, seg := range segments {
		gen := strings.LastIndex(seg, "SELECT format(\n  'CREATE INDEX CONCURRENTLY")
		if gen < 0 {
			// ⚠ 必须**逐语句**执行，不能整段一次 Exec：
			//   pgx 的一次 Exec（多语句、无参数）走扩展协议会被 PG 当成
			//   一个**隐式事务块** ⇒ 888 的 `DROP INDEX CONCURRENTLY`
			//   直接报 25001 "cannot run inside a transaction block"。
			//   复用同包既有的 splitSQLStatements（migration_715_integration_test.go）——
			//   它正确跳过单引号/双引号/注释/$tag$ dollar-quote。
			for _, stmt := range splitSQLStatements(seg) {
				if _, err := conn.Exec(ctx, stmt); err != nil {
					t.Fatalf("%s：第 %d 段语句失败: %v\n语句: %s", name, i, err, r76FirstLine(stmt))
				}
			}
			continue
		}
		prefix, generator := seg[:gen], strings.TrimSpace(strings.TrimSuffix(
			strings.TrimSpace(seg[gen:]), ";"))
		for _, stmt := range splitSQLStatements(prefix) {
			if _, err := conn.Exec(ctx, stmt); err != nil {
				t.Fatalf("%s：第 %d 段前置语句失败: %v\n语句: %s", name, i, err, r76FirstLine(stmt))
			}
		}
		rows, err := conn.Query(ctx, generator)
		if err != nil {
			t.Fatalf("%s：第 %d 段生成器 SELECT 失败: %v", name, i, err)
		}
		var ddls []string
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				rows.Close()
				t.Fatalf("%s：第 %d 段扫描生成的 DDL 失败: %v", name, i, err)
			}
			ddls = append(ddls, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("%s：第 %d 段生成器遍历失败: %v", name, i, err)
		}
		for _, d := range ddls {
			// ⚠ CREATE INDEX CONCURRENTLY 不能在事务块内跑；pgx 的 Exec 不带事务
			//   ⇒ 每条自动提交，满足该约束。
			if _, err := conn.Exec(ctx, d); err != nil {
				t.Fatalf("%s：生成的 DDL 失败 (%s): %v", name, r76FirstLine(d), err)
			}
		}
	}
}

// r76FirstLine 取一段 SQL 的首行（报错信息用，避免刷屏）。
func r76FirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// r76DisposableDBName 判断库名是否像一次性库（与同包 waDisposableDBName 同一口径，
// 但那个住在带 build tag 的文件里，本文件无 tag ⇒ 不能引用它）。
//
// ⚠ 本门会建表、建索引、跑整份迁移 ⇒ 绝不能落在不可丢弃的库上。
func r76DisposableDBName(dsn string) bool {
	l := strings.ToLower(dsn)
	for _, p := range []string{"bench", "test", "tmp", "scratch", "pg_"} {
		if strings.Contains(l, p) {
			return true
		}
	}
	return false
}

// r76RealDB 给出真库 DSN 与清理函数。
//
// ★★ **为什么必须有 testcontainers 这条腿**（2026-10-21 R76 轮自查发现）：
//
//	CI 的 integration job（.github/workflows/integration-testcontainers-ci.yml）
//	只设 `TESTCONTAINERS_*`，**不设 `TEST_PG_URL`** ⇒ 任何以 `TEST_PG_URL` 为唯一
//	入口的真库门在 CI 里都会**静默 SKIP** ⇒ ⇒ **落进「从未被执行过」那一类**
//	（同款问题：R74 轮实测 `installer/` 的形态门从未被跑过）。
//	⇒ 本门按仓内既有范式（taskprofile/e2e_loop_integration_test.go 同款）：
//	**有 TEST_PG_URL 就用它，没有就自己起一个 postgres 容器** ⇒ CI 里真跑。
//
// ⚠ 用外部 DSN 时仍要求库名像一次性库（**绝不能落在业务库上**）：
//
//	本门会建表、建索引、跑整份迁移。
func r76RealDB(ctx context.Context, t *testing.T) (string, func()) {
	t.Helper()
	if dsn := os.Getenv("TEST_PG_URL"); dsn != "" {
		if !r76DisposableDBName(dsn) {
			t.Skipf("TEST_PG_URL 的库名不匹配 bench|test|tmp|scratch|pg_ ⇒ " +
				"拒绝在可能不可丢弃的库上跑本门（本门会建表、建索引、跑整份迁移）")
		}
		return dsn, func() {}
	}
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("r76_attach_slot"),
		postgres.WithUsername("r76_attach_slot"),
		postgres.WithPassword("r76_attach_slot"),
	)
	if err != nil {
		t.Fatalf("起 postgres 容器失败：%v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("取容器连接串失败：%v", err)
	}
	return dsn, func() { _ = container.Terminate(ctx) }
}

// r76Connect 连库并带**就绪重试**。
//
// ★ 为什么需要重试：testcontainers 的容器「已启动」≠「PG 已就绪」——
//
//	PG 初始化期会**主动 reset 掉早到的连接**（实测报
//	"connection reset by peer"）⇒ 一次 Connect 失败就 Fatalf 会让这道门
//	**在 CI 里随机红** ⇒ ⇒ 必须重试到就绪。
//	（仓内同款范式见 taskprofile/e2e_loop_integration_test.go 的 waitFor。）
func r76Connect(ctx context.Context, dsn string) (*pgx.Conn, error) {
	var lastErr error
	for i := 0; i < 30; i++ {
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, lastErr
}
