package bg

// 825 的真库回归（2026-10-04）
//
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过（与 db/db_750_ensure_realdb_test.go
// 同门控、同纪律：真实 boot 顺序里 canonical SQL 先于 Go ensure，所以本测试
// 跑的是 canonical 文件本身，不是它的副本）。
//
// ★ 本文件的存在理由是一条**静默且致命**的缺陷，只有真跑才暴露：
//   ensure 函数的二次短路原本按**分区名是否存在**判断。于是 up→down→up
//   往返时，同名的 `ursm_node_snapshot_min_YYYYMMDD` 仍挂在 _post825 下，
//   短路命中 → 新建的父表**一个分区都没有**；此后每次写入都报
//       ERROR: no partition of relation "ursm_node_snapshot_min" found for row
//   而迁移返回 RC=0、ensure 返回成功、日志无异常。跑 grep 门永远抓不到它。

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const migration825SQL = "../sql/migrations/manual/830_ursm_node_snapshot_min_partitioned.sql"

// dsnDatabaseName 从 DSN 里取出库名。pgx 两种写法都要吃：
// URL 形态（postgres://user:pw@host:5432/dbname?sslmode=disable）与
// 关键字形态（host=… dbname=…）。取不到时返回 "" —— 宁可让下面的门
// 拒绝，也不猜。
func dsnDatabaseName(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" && u.Path != "" {
		return strings.TrimPrefix(u.Path, "/")
	}
	// libpq 关键字形态的 dbname 可以带引号且引号内可含空格（dbname='my db'），
	// 所以引号分支必须整体吃掉引号内的字符，不能用 [^\s']+ —— 那会在空格处截断。
	if m := regexp.MustCompile(`(?:^|\s)dbname\s*=\s*(?:'([^']*)'|([^\s]+))`).FindStringSubmatch(dsn); m != nil {
		if m[1] != "" {
			return m[1]
		}
		return m[2]
	}
	return ""
}

// destructiveGate 是本文件唯一的准入门。返回 nil 表示放行。
//
// ⚠️ 2026-10-06 加固：此前本文件**只**按 env 变量是否存在放行，
// 于是有人拿 TEST_DATABASE_URL 指向 245 跑它时，dropAll825Objects 把 245 上的
// public.ursm_node_snapshot_min 连 CASCADE 删掉，而 schema_migrations 里
// 453/818 的 applied 记录原封不动 —— 账本从此声称「表结构已建立」而真实表不存在。
// 后果有两处，都不是显示问题：
//
//	① 245 每次 deploy 都被迁移 830 的前置守卫挡下（整次部署中止）；
//	② 运行期 ursm.v2 快照留存清理每轮报
//	   ERROR: relation "ursm_node_snapshot_min" does not exist (42P01)。
//
// 与同仓其它真库测试（db/db_750_ensure_realdb_test.go 等只判 env 变量）的区别：
// **只有本文件会 DROP 生产表**，所以只有它需要这道库名门。其它测试不改 schema。
func destructiveGate(dsn string) error {
	if os.Getenv("URSM_825_ALLOW_ANY_DB") == "1" {
		return nil
	}
	name := dsnDatabaseName(dsn)
	if name == "" {
		return fmt.Errorf("无法从 DSN 解析出库名（dsn=%s）。\n"+
			"本测试会 DROP TABLE ... CASCADE，只允许对可丢弃的测试库运行。\n"+
			"若你的 DSN 确实指向测试库，请显式设置 URSM_825_ALLOW_ANY_DB=1。", redactDSN(dsn))
	}
	if !strings.Contains(strings.ToLower(name), "test") {
		return fmt.Errorf("库名 %q 不含 \"test\"。\n"+
			"本测试会 DROP TABLE public.ursm_node_snapshot_min CASCADE 且不恢复。\n"+
			"2026-10-06 实测事故：仅凭 TEST_DATABASE_URL 就放行，导致 245 上的该表被删、\n"+
			"而 453/818 的台账未收口，随后每次部署与运行期留存清理都报错。\n"+
			"确需对非 _test 库运行时，显式设置 URSM_825_ALLOW_ANY_DB=1。", name)
	}
	return nil
}

// redactDSN 只给日志看：保留 host 与库名，抹掉口令与整个 query。
func redactDSN(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
		if u.User != nil {
			u.User = url.User("***")
		}
		u.RawQuery = ""
		return u.String()
	}
	return regexp.MustCompile(`password\s*=\s*\S+`).ReplaceAllString(dsn, "password=***")
}

func connect825(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过 825 真库回归")
	}
	if err := destructiveGate(dsn); err != nil {
		t.Skipf("拒绝执行：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// dropAll825Objects 清掉 825 可能留下的全部对象，让每个用例都从干净前置态开始。
// ★ 必须连 _post825 / _legacy 一起清：它们各自占着 PK 约束名，只清主表的话
//
//	下一次 CREATE ... PRIMARY KEY (同名) 会撞名 —— 这是本轮实跑踩过的坑。
func dropAll825Objects(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS public.ursm_node_snapshot_min CASCADE`)
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS public.ursm_node_snapshot_min_post825 CASCADE`)
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS public.ursm_node_snapshot_min_legacy CASCADE`)
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS public.ursm_node_snapshot_min_legacy_keep CASCADE`)
	_, _ = pool.Exec(ctx, `DROP FUNCTION IF EXISTS public.ensure_ursm_node_snapshot_min_daily_partition(DATE) CASCADE`)
}

// makePreMigrationTable 建出迁移前的形态：一个普通的非分区表，且 PK 约束名是
// canonical 的 `ursm_node_snapshot_min_pkey`（825 的 RENAME CONSTRAINT 那一步
// 就是为它准备的）。
//
// 列不需要与生产一致：825 不读旧表结构，只 RENAME。但 PK 四列必须与 825
// 新建的 PK 同名，才能覆盖"约束名被旧表占着"这条路径。
func makePreMigrationTable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		CREATE TABLE public.ursm_node_snapshot_min (
		    snapshot_ts timestamp with time zone NOT NULL,
		    tenant_id   text NOT NULL,
		    credential_id integer NOT NULL,
		    raw_model_name text NOT NULL,
		    CONSTRAINT ursm_node_snapshot_min_pkey
		        PRIMARY KEY (snapshot_ts, tenant_id, credential_id, raw_model_name)
		);
		INSERT INTO public.ursm_node_snapshot_min
		    SELECT ts, 't1', 1, 'm1' FROM generate_series(now() - interval '2 days', now(), interval '1 hour') ts;
	`)
	if err != nil {
		t.Fatalf("build pre-migration table: %v", err)
	}
}

func run825(t *testing.T, pool *pgxpool.Pool) error {
	t.Helper()
	b, err := os.ReadFile(migration825SQL)
	if err != nil {
		t.Fatalf("read %s: %v", migration825SQL, err)
	}
	conn, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	_, err = conn.Conn().PgConn().Exec(context.Background(), string(b)).ReadAll()
	return err
}

// Test825MigrationRealDB — 主路径：跑通 + 验收关键形态。
func Test825MigrationRealDB(t *testing.T) {
	pool := connect825(t)
	dropAll825Objects(t, pool)
	t.Cleanup(func() { dropAll825Objects(t, pool) })
	makePreMigrationTable(t, pool)

	if err := run825(t, pool); err != nil {
		t.Fatalf("apply 825: %v", err)
	}
	ctx := context.Background()

	t.Run("旧表完整保留", func(t *testing.T) {
		var n int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.ursm_node_snapshot_min_legacy`).Scan(&n); err != nil {
			t.Fatalf("count legacy: %v", err)
		}
		if n != 49 { // 2 天 × 每小时一条 + 端点 = 49
			t.Fatalf("legacy rows = %d, want 49 (历史必须一行不少)", n)
		}
	})

	t.Run("新父表是分区父表且有分区", func(t *testing.T) {
		var relkind string
		var parts int
		// ★ 断言必须连分区数一起查：只查 relkind='p' 会漏掉
		//   「是分区父表但零分区」这个真正的故障形态（见文件头）。
		err := pool.QueryRow(ctx, `
			SELECT c.relkind::text,
			       (SELECT count(*) FROM pg_inherits i WHERE i.inhparent = c.oid)
			  FROM pg_class c WHERE c.oid = 'public.ursm_node_snapshot_min'::regclass
		`).Scan(&relkind, &parts)
		if err != nil {
			t.Fatalf("read parent: %v", err)
		}
		if relkind != "p" {
			t.Fatalf("relkind = %q, want 'p'", relkind)
		}
		if parts != 3 {
			t.Fatalf("partitions = %d, want 3 — a partitioned parent with zero partitions "+
				"makes EVERY write fail with no partition of relation found", parts)
		}
	})

	t.Run("没有 DEFAULT 分区", func(t *testing.T) {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
			 WHERE i.inhparent = 'public.ursm_node_snapshot_min'::regclass
			   AND pg_get_expr(c.relpartbound, c.oid) = 'DEFAULT'
		`).Scan(&n); err != nil {
			t.Fatalf("count default: %v", err)
		}
		if n != 0 {
			t.Fatalf("DEFAULT partitions = %d, want 0", n)
		}
	})

	t.Run("分区边界是 +08 不是 Z", func(t *testing.T) {
		rows, err := pool.Query(ctx, `
			SELECT pg_get_expr(c.relpartbound, c.oid) FROM pg_class c
			 WHERE c.relname LIKE 'ursm_node_snapshot_min\_2%' AND c.relpartbound IS NOT NULL
		`)
		if err != nil {
			t.Fatalf("query bounds: %v", err)
		}
		defer rows.Close()
		var seen int
		for rows.Next() {
			var b string
			if err := rows.Scan(&b); err != nil {
				t.Fatal(err)
			}
			seen++
			// 边界错位 8h ⇒ DROP 留存会提前删掉最多 8 小时的存活数据。
			if !strings.Contains(b, "+08") {
				t.Fatalf("partition bound %q is not pinned to Asia/Shanghai (+08)", b)
			}
		}
		if seen != 3 {
			t.Fatalf("bounded partitions = %d, want 3", seen)
		}
	})

	t.Run("56 列且无 snapshot_ts 单列索引", func(t *testing.T) {
		var cols int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.columns
			 WHERE table_schema='public' AND table_name='ursm_node_snapshot_min'
		`).Scan(&cols); err != nil {
			t.Fatal(err)
		}
		if cols != 56 {
			t.Fatalf("columns = %d, want 56", cols)
		}
		var idx int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_indexes
			 WHERE tablename='ursm_node_snapshot_min' AND indexdef LIKE '%(snapshot_ts)%'
		`).Scan(&idx); err != nil {
			t.Fatal(err)
		}
		if idx != 0 {
			t.Fatalf("ts-only indexes = %d, want 0 (设计稿 §3.6 刻意不建)", idx)
		}
	})

	t.Run("writer 的 ON CONFLICT 仍可执行", func(t *testing.T) {
		// 硬约束 1：PK 没了这就是全量写入瘫痪。
		for i := 0; i < 2; i++ {
			if _, err := pool.Exec(ctx, `
				INSERT INTO public.ursm_node_snapshot_min
				  (snapshot_ts, recovery_epoch, provider_id, credential_id,
				   raw_model_name, canonical_name, available, health_status, payload,
				   updated_at_ms)
				VALUES (date_trunc('minute', now()), 1, 1, 999, 'probe', 'probe',
				        true, 'healthy', '{}'::jsonb, 1)
				ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name) DO NOTHING
			`); err != nil {
				t.Fatalf("writer-shaped INSERT #%d: %v", i+1, err)
			}
		}
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM public.ursm_node_snapshot_min WHERE credential_id = 999`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("probe rows = %d, want 1 (第二次应被 ON CONFLICT 吃掉)", n)
		}
	})

	t.Run("ensure 幂等", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			if _, err := pool.Exec(ctx,
				`SELECT public.ensure_ursm_node_snapshot_min_daily_partition((now() AT TIME ZONE 'Asia/Shanghai')::date)`,
			); err != nil {
				t.Fatalf("ensure #%d: %v", i+1, err)
			}
		}
		var parts int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM pg_inherits WHERE inhparent='public.ursm_node_snapshot_min'::regclass`,
		).Scan(&parts); err != nil {
			t.Fatal(err)
		}
		if parts != 3 {
			t.Fatalf("partitions after 3 ensure calls = %d, want 3", parts)
		}
	})

	t.Run("重放被守卫挡住", func(t *testing.T) {
		if err := run825(t, pool); err == nil {
			t.Fatal("replaying 825 succeeded — the fail-closed guard is not working")
		}
	})
}

// Test825EnsureRejectsForeignPartitionSquatting — ★ 本文件的核心用例。
//
// 构造 up→down→up 的前置形态：同名分区已存在，但挂在**别的**父表下
// （真实来源是 830.down 保留的 _post825）。要求 ensure **明确报错**，
// 而不是静默返回 —— 静默返回的后果是新父表零分区、所有写入失败，
// 而迁移报成功。
func Test825EnsureRejectsForeignPartitionSquatting(t *testing.T) {
	pool := connect825(t)
	dropAll825Objects(t, pool)
	t.Cleanup(func() { dropAll825Objects(t, pool) })
	makePreMigrationTable(t, pool)

	// 跑通一次 825 拿到真函数。
	if err := run825(t, pool); err != nil {
		t.Fatalf("apply 825: %v", err)
	}
	ctx := context.Background()

	// 把父表与它的分区整体挪到 _post825 名下（等价于 830.down 的第 1 步）。
	if _, err := pool.Exec(ctx,
		`ALTER TABLE public.ursm_node_snapshot_min RENAME TO ursm_node_snapshot_min_post825`); err != nil {
		t.Fatalf("rename parent: %v", err)
	}
	// _legacy 先挪到 _legacy_keep 腾出名字：下面要建的"空分区父表"必须
	// 占用 canonical 表名，否则测的就不是"同名分区挂在别的父表下"这件事了。
	if _, err := pool.Exec(ctx,
		`ALTER TABLE public.ursm_node_snapshot_min_legacy RENAME TO ursm_node_snapshot_min_legacy_keep`); err != nil {
		t.Fatalf("move legacy aside: %v", err)
	}

	// 建一个空的分区父表（模拟 up 建好但一个分区都没有的那一刻）。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE public.ursm_node_snapshot_min (
		    snapshot_ts timestamp with time zone NOT NULL,
		    recovery_epoch bigint NOT NULL,
		    provider_id integer NOT NULL,
		    credential_id integer NOT NULL,
		    raw_model_name text NOT NULL,
		    canonical_name text,
		    tenant_id text DEFAULT ''::text NOT NULL,
		    available boolean NOT NULL,
		    health_status text,
		    fail_streak integer,
		    cool_until timestamp with time zone,
		    sr_1m real, sr_5m real, sr_30m real,
		    samples_1m integer, samples_5m integer, samples_30m integer,
		    lat_p50_ms integer, lat_p95_ms integer,
		    score real,
		    price_in_per_1m numeric, price_out_per_1m numeric,
		    billing_mode text, trust_level real,
		    baseurl_latency_ms integer,
		    conc_used integer, conc_limit integer,
		    fp_used integer, fp_limit integer,
		    source_priority integer, generation bigint, payload jsonb,
		    updated_at_ms bigint, last_probe_at_ms bigint,
		    last_probe_latency_ms bigint, last_attempt_ms bigint,
		    last_ok_ms bigint, last_request_at_ms bigint,
		    last_request_error_at_ms bigint, manual_at_ms bigint,
		    cool_until_ms bigint, event_seq bigint,
		    disabled boolean, last_direct_ok boolean, manual_hold boolean,
		    success_count integer, failure_count integer, disable_count integer,
		    lat_ewma_ms real, empty_response_rate_1m real, empty_response_rate_30m real,
		    last_err text, manual_reason text, manual_actor text,
		    disabled_reason text, cool_reason text
		) PARTITION BY RANGE (snapshot_ts);
	`); err != nil {
		t.Fatalf("create fresh parent: %v", err)
	}

	// ★ 关键断言：ensure 必须报错。
	_, err := pool.Exec(ctx,
		`SELECT public.ensure_ursm_node_snapshot_min_daily_partition((now() AT TIME ZONE 'Asia/Shanghai')::date)`)
	if err == nil {
		t.Fatal("ensure silently succeeded while a same-named partition is attached to ANOTHER parent — " +
			"the new parent would end up with zero partitions and every write would fail " +
			"with no partition of relation found, while the migration reports success")
	}

	// 且报错后父表仍然零分区这件事必须**可见**给排查者（而不是被悄悄留下）。
	var parts int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_inherits WHERE inhparent='public.ursm_node_snapshot_min'::regclass`,
	).Scan(&parts); err != nil {
		t.Fatal(err)
	}
	if parts != 0 {
		t.Fatalf("fresh parent has %d partitions; expected 0 so the caller can see nothing was created", parts)
	}
}

// Test825UpDownRoundTripRealDB — ★ 往返回归。
//
// 本轮本地 PG 17.11 实跑在**往返**路径上抓到三个缺陷，每一个都只在
// 「up 过一次、down 过一次、再上」时才现形，单跑一次 up 永远看不见：
//
//  1. RENAME 时约束**不跟着表改名**，`_legacy` 仍占着
//     `ursm_node_snapshot_min_pkey` ⇒ 新父表建同名 PK 报 already exists；
//  2. down 若不把旧 PK 约束名换回 canonical，重新 up 时第 1 步的
//     RENAME CONSTRAINT 报 does not exist ⇒ **回滚后再也上不了迁移**；
//  3. down 保留的 `_post825` 仍占着 canonical PK 名 ⇒ 同上。
//
// 「只能退不能进的回滚是假回滚」。本测试把整条往返钉死。
func Test825UpDownRoundTripRealDB(t *testing.T) {
	pool := connect825(t)
	dropAll825Objects(t, pool)
	t.Cleanup(func() { dropAll825Objects(t, pool) })
	ctx := context.Background()
	downPath := filepath.Join("..", "sql", "migrations", "startup",
		"830_ursm_node_snapshot_min_partitioned.down.sql")

	runDown := func() {
		t.Helper()
		b, err := os.ReadFile(downPath)
		if err != nil {
			t.Fatalf("read down sql: %v", err)
		}
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer conn.Release()
		if _, err := conn.Conn().PgConn().Exec(ctx, string(b)).ReadAll(); err != nil {
			t.Fatalf("apply 825 down: %v", err)
		}
	}
	pkName := func(table string) string {
		var n string
		if err := pool.QueryRow(ctx, `
			SELECT coalesce((SELECT conname FROM pg_constraint
			                  WHERE conrelid = $1::regclass AND contype = 'p' LIMIT 1), '')
		`, table).Scan(&n); err != nil {
			t.Fatalf("read pk of %s: %v", table, err)
		}
		return n
	}

	makePreMigrationTable(t, pool)
	if err := run825(t, pool); err != nil {
		t.Fatalf("up #1: %v", err)
	}

	t.Run("down 回到非分区表且历史一行不少", func(t *testing.T) {
		runDown()
		var relkind string
		var rows int
		if err := pool.QueryRow(ctx, `
			SELECT c.relkind::text, (SELECT count(*) FROM public.ursm_node_snapshot_min)
			  FROM pg_class c WHERE c.oid = 'public.ursm_node_snapshot_min'::regclass
		`).Scan(&relkind, &rows); err != nil {
			t.Fatal(err)
		}
		if relkind != "r" {
			t.Fatalf("relkind after down = %q, want 'r'", relkind)
		}
		if rows != 49 {
			t.Fatalf("rows after down = %d, want 49", rows)
		}
		if got := pkName("public.ursm_node_snapshot_min"); got != "ursm_node_snapshot_min_pkey" {
			t.Fatalf("pk constraint name after down = %q, want the canonical name — "+
				"otherwise re-applying 825 fails on the RENAME CONSTRAINT step", got)
		}
	})

	t.Run("_post825 仍在时重新 up 必须大声报错", func(t *testing.T) {
		if err := run825(t, pool); err == nil {
			t.Fatal("re-applying 825 with _post825 present succeeded — it should refuse, " +
				"because the same-named partitions under _post825 would be silently skipped " +
				"and the new parent would end up with zero partitions")
		}
	})

	t.Run("清掉 _post825 后能重新 up", func(t *testing.T) {
		// down 之后 `_post825` 还占着 canonical PK 名，up 之前必须先处理它。
		if _, err := pool.Exec(ctx, `DROP TABLE public.ursm_node_snapshot_min_post825 CASCADE`); err != nil {
			t.Fatalf("drop _post825: %v", err)
		}
		if err := run825(t, pool); err != nil {
			t.Fatalf("up #2 after clearing _post825: %v", err)
		}
		var parts int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM pg_inherits WHERE inhparent='public.ursm_node_snapshot_min'::regclass`,
		).Scan(&parts); err != nil {
			t.Fatal(err)
		}
		if parts != 3 {
			t.Fatalf("partitions after re-up = %d, want 3", parts)
		}
	})
}
