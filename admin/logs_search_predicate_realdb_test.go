package admin

// logs_search_predicate_realdb_test.go —— G-5 全文检索腿的**真库**判据。
//
// # 这道门测什么
//
// 静态门（admin/logs_search_predicate_test.go）只能证明「谓词长什么样」。
// 本门证明**三件在真库里才有意义的事**：
//
//  1. **迁移 887 的 SQL 真能跑**（真字节 psql -f），并且建出来的索引
//     是 valid、且挂在父壳下（877 同款三段式的承重形态）。
//  2. **全文腿真的增加召回**：`search_text ILIKE '%deepseek chat%'` 命中 0 行，
//     而 `to_tsvector('simple', search_text) @@ plainto_tsquery('simple',
//     'deepseek chat')` 命中 1 行 ⇒ FTS 腿不是装饰。
//  3. **子串路径一条都没丢**：`'%eepseek%'` 是 `deepseekchat` 的**子串**
//     但不是它的**词元** ⇒ 纯 FTS 腿命中 0 行，而并联（OR）形态命中 2 行
//     ⇒ 抓住子串的正是那条 ILIKE 腿。
//
// ★ 第 3 条同时是「允许侧用例必须配前提断言」的形态：只断言「OR 形态命中
//   2 行」是不够的 —— 它可能全是 FTS 腿命中的。必须**同时**断言
//   「纯 FTS 形态命中 0 行」，才能证明这 2 行是 ILIKE 腿挣来的。
//
// ★ 第 1 条的顺序纪律：先跑**只有 FTS 腿**的查询看它命不命中，再建索引；
//   建完索引后同一查询的**计划**必须出现 FTS 索引（否则索引白建，
//   而 planner 不会为此报任何错 —— 判据纪律第 3 条的镜像形态）。
//
// 用法（opt-in，不设 DSN 就跳过）：
//
//	source /tmp/lgw-env.sh && cd <本 worktree>
//	go test ./admin/ -run 'TestG5SearchTextFTS' -count=1 -v
//
// DSN 取值顺序 TEST_ADMIN_500_DSN → LLM_GATEWAY_DATABASE_URL → DATABASE_URL。
// 只建后即删的一次性 database，**从不写业务库 public**。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestG5SearchTextFTSOnRealDB 是 G-5 的真库正题。
func TestG5SearchTextFTSOnRealDB(t *testing.T) {
	dsn := g5DSN(t)
	psqlBin := g5Psql(t)
	pool, ctx, isoDSN := g5ScratchDB(t, dsn)

	// ── 夹具：同形的分区父表 + hot 表（只有本门用到的列）────────────
	// ★ 夹具纪律：needle 必须**有选择性**。877 的实测记录里写明过
	//   「第一版夹具让每行都含 'deepseek'，planner 选 Seq Scan 是**正确**的
	//   （匹配 100% 的谓词用索引必然更慢），差点被读成「索引不可用」」。
	//   ⇒ 这里 4000 行里只有 2 行含目标词。
	if _, err := pool.Exec(ctx, `
		CREATE EXTENSION IF NOT EXISTS pg_trgm;
		CREATE TABLE public.session_turns (
		  id bigint NOT NULL,
		  session_id text NOT NULL,
		  turn_no integer NOT NULL,
		  tenant_id varchar(255) NOT NULL,
		  request_id text NOT NULL,
		  search_text text,
		  partition_date date DEFAULT CURRENT_DATE NOT NULL
		) PARTITION BY RANGE (partition_date);
		CREATE TABLE public.session_turns_2026_07 PARTITION OF public.session_turns
		  FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
		CREATE TABLE public.session_turns_2026_08 PARTITION OF public.session_turns
		  FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
		CREATE TABLE public.session_turns_hot (LIKE public.session_turns INCLUDING DEFAULTS);
		INSERT INTO public.session_turns_2026_07
		  SELECT g, 's'||g, 1, 'default', 'r'||g,
		         CASE WHEN g = 7 THEN 'using deepseek for chat completion' ELSE 'filler text '||g END,
		         DATE '2026-07-15'
		  FROM generate_series(1,2000) g;
		INSERT INTO public.session_turns_2026_08
		  SELECT g, 's'||g, 1, 'default', 'r8'||g,
		         CASE WHEN g = 3 THEN 'the deepseekchat model' ELSE 'other filler '||g END,
		         DATE '2026-08-15'
		  FROM generate_series(1,2000) g;
		ANALYZE public.session_turns;
	`); err != nil {
		t.Fatalf("建夹具失败: %v", err)
	}

	// ── 先量「只有 FTS 腿」与「只有 ILIKE 腿」的命中数（建索引之前）──
	ilikeDeepseekChat := g5Count(t, pool, ctx,
		`SELECT count(*) FROM public.session_turns WHERE search_text ILIKE '%deepseek chat%'`)
	ftsDeepseekChat := g5Count(t, pool, ctx,
		`SELECT count(*) FROM public.session_turns WHERE to_tsvector('simple', search_text) @@ plainto_tsquery('simple', 'deepseek chat')`)
	ilikeEepseek := g5Count(t, pool, ctx,
		`SELECT count(*) FROM public.session_turns WHERE search_text ILIKE '%eepseek%'`)
	ftsEepseek := g5Count(t, pool, ctx,
		`SELECT count(*) FROM public.session_turns WHERE to_tsvector('simple', search_text) @@ plainto_tsquery('simple', 'eepseek')`)

	// ★ 前提断言（缺一条，后面全是假绿）：
	//   ① 纯 ILIKE 抓不到 "deepseek chat"（那两个词在文本里**不连续**）
	//      ⇒ 后面「OR 形态命中 1 行」才证明得了是 FTS 腿挣来的；
	//   ② 纯 FTS 抓不到 "eepseek"（它是 deepseekchat 的子串、不是词元）
	//      ⇒ 后面「OR 形态命中 2 行」才证明得了是 ILIKE 腿挣来的。
	if ilikeDeepseekChat != 0 {
		t.Fatalf("夹具前提不成立：纯 ILIKE 的 '%%deepseek chat%%' 命中了 %d 行（应为 0）—— "+
			"两个词在夹具文本里必须**不连续**，否则 FTS 腿的召回无法被证明", ilikeDeepseekChat)
	}
	if ftsEepseek != 0 {
		t.Fatalf("夹具前提不成立：纯 FTS 的 'eepseek' 命中了 %d 行（应为 0）—— "+
			"'eepseek' 必须是 deepseekchat 的**子串**而不是词元，否则 ILIKE 腿无法被证明", ftsEepseek)
	}
	if ftsDeepseekChat != 1 {
		t.Fatalf("夹具前提不成立：纯 FTS 的 'deepseek chat' 命中 %d 行（应为 1）", ftsDeepseekChat)
	}
	if ilikeEepseek != 2 {
		t.Fatalf("夹具前提不成立：纯 ILIKE 的 '%%eepseek%%' 命中 %d 行（应为 2）", ilikeEepseek)
	}

	// ── 应用迁移 887 的**真字节**（psql -f，与部署通道同形态）────────
	// ★ 必须走 psql 的**元命令通道**：迁移里含 \gexec（生成每分区的
	//   CREATE INDEX CONCURRENTLY），pgx/lib-pq 直连 Exec 不认 \gexec。
	migPath := filepath.Join(repoRoot(t), "sql", "migrations", "startup", "887_session_turns_search_text_fts.sql")
	// ★ psql 必须指向**隔离库**（isoDSN），不是业务库。
	//   本机的 psql 是个 shim（~/kaixuan/pg18/bin/psql）：它把 DSN 的
	//   host:port 改写成容器内的 127.0.0.1:5432，**库名保持不变** ⇒
	//   用 isoDSN（库名=隔离库）时它进的仍是隔离库。
	if err := g5ApplyMigration(t, ctx, psqlBin, isoDSN, migPath); err != nil {
		t.Fatalf("应用迁移 887 失败: %v", err)
	}

	// 877 的 trigram 索引（生产上由 877 建）：**两条腿都要有**才能走 BitmapOr。
	if _, err := pool.Exec(ctx, `
		CREATE INDEX session_turns_2026_07_search_trgm ON public.session_turns_2026_07 USING gin (search_text gin_trgm_ops);
		CREATE INDEX session_turns_2026_08_search_trgm ON public.session_turns_2026_08 USING gin (search_text gin_trgm_ops);
		CREATE INDEX idx_session_turns_hot_search_trgm ON public.session_turns_hot USING gin (search_text gin_trgm_ops);
		ANALYZE public.session_turns;
	`); err != nil {
		t.Fatalf("建 trigram 夹具索引失败: %v", err)
	}

	// ── 断言 1：索引建成、valid、且挂在父壳下 ────────────────────────
	var valid bool
	var parentDef string
	if err := pool.QueryRow(ctx, `
		SELECT i.indisvalid, pg_get_indexdef(i.indexrelid)
		FROM pg_index i
		WHERE i.indexrelid = to_regclass('public.idx_session_turns_search_fts')
	`).Scan(&valid, &parentDef); err != nil {
		t.Fatalf("读父索引状态失败: %v", err)
	}
	if !valid {
		t.Errorf("idx_session_turns_search_fts indisvalid=false —— " +
			"半成品索引：对象在、名字在、但 planner 不用它（CONCURRENTLY 被掐断的形态）")
	}
	if !strings.Contains(parentDef, "to_tsvector('simple'::regconfig, search_text)") {
		t.Errorf("父索引的表达式不是 %s：\n%s", "to_tsvector('simple'::regconfig, search_text)", parentDef)
	}
	// 每个分区子索引都必须挂在父壳下（父壳只是壳，承重的是挂上去的子索引）。
	var attached, expected int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM pg_inherits WHERE inhparent = to_regclass('public.idx_session_turns_search_fts')),
		  (SELECT count(*) FROM pg_class c
		     WHERE c.oid IN (SELECT inhrelid FROM pg_inherits WHERE inhparent = 'public.session_turns'::regclass)
		       AND to_regclass(format('public.%I', c.relname || '_search_fts')) IS NOT NULL)
	`).Scan(&attached, &expected); err != nil {
		t.Fatalf("读 ATTACH 状态失败: %v", err)
	}
	if attached != expected {
		t.Errorf("父壳上挂了 %d 个分区子索引，实际有 %d 个 —— "+
			"没挂上的分区不会被父索引承重", attached, expected)
	}
	// hot 侧必须独立建（父表索引不级联到 hot 表）。
	if g5Count(t, pool, ctx,
		`SELECT count(*) FROM pg_class WHERE relname = 'idx_session_turns_hot_search_fts'`) != 1 {
		t.Errorf("hot 侧索引没建 —— 父表索引**不级联**到 hot 表（dual-write 架构），" +
			"漏掉这条腿，view 的第一条 UNION ALL 臂仍然顺序扫描")
	}

	// ── 断言 2：并联形态的命中集 = 两腿之和（且各自都不可少）──────────
	orDeepseekChat := g5Count(t, pool, ctx,
		`SELECT count(*) FROM public.session_turns WHERE (search_text ILIKE '%deepseek chat%' OR to_tsvector('simple', search_text) @@ plainto_tsquery('simple', 'deepseek chat'))`)
	if orDeepseekChat != 1 {
		t.Errorf("并联形态的 'deepseek chat' 命中 %d 行（应为 1）—— "+
			"纯 ILIKE 命中 0、纯 FTS 命中 1 ⇒ 这 1 行只能来自 FTS 腿", orDeepseekChat)
	}
	orEepseek := g5Count(t, pool, ctx,
		`SELECT count(*) FROM public.session_turns WHERE (search_text ILIKE '%eepseek%' OR to_tsvector('simple', search_text) @@ plainto_tsquery('simple', 'eepseek'))`)
	if orEepseek != 2 {
		t.Errorf("并联形态的 'eepseek' 命中 %d 行（应为 2）—— "+
			"纯 FTS 命中 0 ⇒ 这 2 行只能来自 ILIKE 腿（子串路径没被破坏）", orEepseek)
	}

	// ── 断言 3：计划里必须出现 FTS 索引（索引真的承重）──────────────
	// ★ 这条是「索引白建」的唯一可观测防线：表达式不同形时 planner
	//   **不报错**，只是不用它。
	plan := g5Explain(t, pool, ctx, `
		SELECT id FROM public.session_turns rl
		WHERE (rl.search_text ILIKE '%deepseek chat%'
		       OR to_tsvector('simple', rl.search_text) @@ plainto_tsquery('simple', 'deepseek chat'))`)
	if !strings.Contains(plan, "search_fts") {
		t.Errorf("计划里没有 FTS 索引 —— 表达式与索引不同形，planner 静默不用它：\n%s", plan)
	}
	if !strings.Contains(plan, "search_trgm") {
		t.Errorf("计划里没有 trigram 索引 —— 子串腿没被服务（并联退化成顺序扫描）：\n%s", plan)
	}

	// ── 断言 4（变异见证）：删掉 FTS 索引后计划必须不再用它 ──────────
	// 这一步是本门的**自证**：证明断言 3 真的在测「索引是否被用」，
	// 而不是「计划文本里恰好出现了某个词」。
	if _, err := pool.Exec(ctx, `DROP INDEX IF EXISTS public.idx_session_turns_search_fts`); err != nil {
		t.Fatalf("删父索引失败: %v", err)
	}
	plan2 := g5Explain(t, pool, ctx, `
		SELECT id FROM public.session_turns rl
		WHERE (rl.search_text ILIKE '%deepseek chat%'
		       OR to_tsvector('simple', rl.search_text) @@ plainto_tsquery('simple', 'deepseek chat'))`)
	if strings.Contains(plan2, "session_turns_2026_07_search_fts") {
		t.Errorf("删掉父索引后计划里仍有 FTS 索引 —— 断言 3 抓的不是「索引被用」：\n%s", plan2)
	}
}

// ── 本门的小工具 ──────────────────────────────────────────────────────

func g5DSN(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"TEST_ADMIN_500_DSN", "LLM_GATEWAY_DATABASE_URL", "DATABASE_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	t.Skip("set TEST_ADMIN_500_DSN (or `source /tmp/lgw-env.sh` and use LLM_GATEWAY_DATABASE_URL)")
	return ""
}

func g5Psql(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("psql"); err == nil {
		return p
	}
	if p, err := exec.LookPath("~/kaixuan/pg18/bin/psql"); err == nil {
		return p
	}
	t.Skip("psql unavailable; cannot replay migration bytes")
	return ""
}

// g5PsqlArgs 返回 psql 的连接参数（与迁移 887 要求的非事务通道一致：
// 迁移里含 \gexec，必须走 psql 的元命令通道，不能用 pgx 直连）。
func g5PsqlArgs(dsn string) []string {
	return []string{"-X", "-v", "ON_ERROR_STOP=1", dsn}
}

func g5ScratchDB(t *testing.T, dsn string) (*pgxpool.Pool, context.Context, string) {
	t.Helper()
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open root pool: %v", err)
	}
	name := fmt.Sprintf("lgw_g5_%d", time.Now().UnixNano())
	if _, err := root.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		root.Close()
		t.Fatalf("create database %s: %v", name, err)
	}
	idx := strings.LastIndex(dsn, "/")
	if idx < 0 {
		root.Close()
		t.Fatalf("cannot rewrite dsn %q", dsn)
	}
	rest := dsn[idx+1:]
	if q := strings.Index(rest, "?"); q >= 0 {
		rest = rest[q:]
	} else {
		rest = ""
	}
	isoDSN := dsn[:idx+1] + name + rest
	pool, err := pgxpool.New(ctx, isoDSN)
	if err != nil {
		root.Close()
		t.Fatalf("open isolated db pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := root.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		root.Close()
	})
	return pool, ctx, isoDSN
}

func g5ApplyMigration(t *testing.T, ctx context.Context, psqlBin, dsn, path string) error {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, psqlBin, g5PsqlArgs(dsn)...)
	cmd.Stdin = strings.NewReader(string(raw))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}

func g5Count(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, sql).Scan(&n); err != nil {
		t.Fatalf("count query failed (%s): %v", sql, err)
	}
	return n
}

func g5Explain(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string) string {
	t.Helper()
	rows, err := pool.Query(ctx, "EXPLAIN (COSTS OFF) "+sql)
	if err != nil {
		t.Fatalf("explain failed: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan explain: %v", err)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
