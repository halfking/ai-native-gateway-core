package db

// db_session_dim_session_key_index_test.go —— 会话级统计的「会话宇宙」连接
// 索引契约门（2026-11-23 统计口径轮 D5 根修）。
//
// # 这道门守什么
//
// 「会话级统计读 session_dim.session_key」这件事**不会红**：没有索引时查询
// 照样返回结果，只是慢到 30 秒超时 ⇒ 页面上表现为「该档位失败」而不是
// 「数据不对」。这种退化不会被任何功能门抓到（功能面全绿），所以需要一道
// **文本钉子**把四件事钉死：索引在、连接键对、并发建（不阻塞写）、已挂进
// ensure 链（否则新环境永远缺）。
//
// # 为什么索引列必须是 (tenant_id, session_key) 且顺序如此
//
// 会话宇宙的 JOIN 键（domains/analytics/session_probe_scope.go 的
// SessionUniverseJoin）是
//
//	LEFT JOIN session_dim sd ON sd.session_key = ss.session_key
//	                          AND sd.tenant_id  = ss.tenant_id
//
// ⇒ 两个列都是等值条件。tenant_id 前置是为了与本表既有索引族（全部
// tenant_id 前缀）一致，也保证租户维度过滤仍然可用。索引缺任一列 ⇒ 回退成
// 「拿 tenant_id 索引取回整个租户再过滤」⇒ 就是本轮修掉的那个平方级放大
// （211 实测：121,788 ms ⇒ 194.9 ms）。
//
// # 门本身的口径
//
// 本门是**文本钉子**，它证明不了「索引真的让查询变快」——那件事由真库回归
// （TestEnsureSessionDimSessionKeyIndex_RealDB）与生产 EXPLAIN 对拍背书。
// 它抓的是唯一一种回退：有人把索引删了/改键了/挪出 ensure 链/改成阻塞式建。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sessionDimSessionKeyIndexName = "idx_session_dim_session_key"

func TestSessionDimSessionKeyIndexEnsureIsWired(t *testing.T) {
	src := readGuardedFile(t, "db.go")

	// ① ensure 函数存在，且 DDL 打在这张表的这对列上。
	i := strings.Index(src, "func (d *DB) ensureSessionDimSessionKeyIndex")
	if i < 0 {
		t.Fatal("db/db.go 里没有 ensureSessionDimSessionKeyIndex —— 连接索引不再被自愈创建")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	for _, frag := range []string{
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS " + sessionDimSessionKeyIndexName,
		"ON public.session_dim (tenant_id, session_key)",
		// INVALID 残留清理：被中断的 CONCURRENTLY 构建会留下 INVALID 索引，
		// 而 IF NOT EXISTS 会永远跳过它 ⇒ 必须先 DROP 再建。
		"DROP INDEX CONCURRENTLY IF EXISTS public.",
		"errors.Is(err, pgx.ErrNoRows)",
	} {
		if !strings.Contains(body, frag) {
			t.Errorf("ensureSessionDimSessionKeyIndex 缺 %q", frag)
		}
	}

	// ② 已挂进 ensure 链（applyMigrationsOnce）⇒ 新环境/存量库每次启动都自愈。
	chain := ensureChainCalls(t)
	call := strings.Index(chain, "ensureSessionDimSessionKeyIndex(migCtx)")
	if call < 0 {
		t.Fatal("ensure 链里没有 ensureSessionDimSessionKeyIndex(migCtx) —— 索引不会被创建")
	}
	// ③ 失败语义：非致命（记日志继续启动）⇒ 索引建不出来不该把网关拉下线。
	after := chain[call:]
	if j := strings.Index(after, "\n\t}"); j >= 0 {
		after = after[:j]
	}
	if !strings.Contains(after, "slog.Error") {
		t.Error("ensure 调用点没有 slog.Error —— 失败会被吞掉（既不响也不留痕）")
	}
	if strings.Contains(after, "return err") {
		t.Error("ensure 调用点把错误直接 return 了 —— 一个 dashboard 统计索引会让整个网关起不来")
	}

	// ④ 索引列 ⊇ 会话宇宙的连接键集合（两列等值 ⇒ 两列都要进索引）。
	universe := readGuardedFile(t, filepath.Join("..", "domains", "analytics", "session_probe_scope.go"))
	for _, frag := range []string{
		".session_key = %[2]s.session_key",
		".tenant_id = %[2]s.tenant_id",
	} {
		if !strings.Contains(universe, frag) {
			t.Fatalf("会话宇宙连接键变了：%s 里没有 %q —— 本门断言的索引列需要重新推导", "session_probe_scope.go", frag)
		}
	}
	if !strings.Contains(body, "(tenant_id, session_key)") {
		t.Error("索引列不是 (tenant_id, session_key) —— 与会话宇宙连接键不匹配")
	}
}

// TestEnsureSessionDimSessionKeyIndex_RealDB —— ensure 的真库回归
// （2026-11-23 统计口径轮）。
//
//  1. DROP 掉索引（模拟尚未创建），ensure 必须建出来且 indisvalid=true；
//  2. 第二次 ensure 必须幂等快返回（索引已 valid 时的 boot 路径）；
//  3. 建出来的索引必须真能服务连接键查询（EXPLAIN 走 Index Cond 命中 1 行，
//     而不是回退成「取回全租户再过滤」）。
//
// 纪律（R78 轮改造）：本门自带**一次性空库**（db/scratch_db_test.go）——
// 破坏性 DDL 只发生在一次性库上，绝不直连 DSN 指向的形态库。
func TestEnsureSessionDimSessionKeyIndex_RealDB(t *testing.T) {
	pool := scratchPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	d := &DB{pool: pool}

	// 测试用的最小 session_dim 形态（生产表由迁移 350 建，这里只求连接键形态）。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.session_dim (
			gw_session_id text PRIMARY KEY,
			session_key    text,
			tenant_id     text
		)`); err != nil {
		t.Fatalf("prepare session_dim: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP INDEX IF EXISTS public.`+sessionDimSessionKeyIndexName); err != nil {
		t.Fatalf("drop %s: %v", sessionDimSessionKeyIndexName, err)
	}

	if err := d.ensureSessionDimSessionKeyIndex(ctx); err != nil {
		t.Fatalf("ensure round 1 (build path): %v", err)
	}
	var valid bool
	if err := pool.QueryRow(ctx, `
		SELECT i.indisvalid
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		WHERE i.indrelid = 'public.session_dim'::regclass
		  AND c.relname = $1
	`, sessionDimSessionKeyIndexName).Scan(&valid); err != nil {
		t.Fatalf("re-check %s: %v", sessionDimSessionKeyIndexName, err)
	}
	if !valid {
		t.Fatalf("%s rebuilt but indisvalid=false", sessionDimSessionKeyIndexName)
	}

	// 幂等：第二次 ensure 不报错（索引已 valid ⇒ 直接返回）。
	if err := d.ensureSessionDimSessionKeyIndex(ctx); err != nil {
		t.Fatalf("ensure round 2 (idempotent path): %v", err)
	}

	// 连接键查询必须走索引（命中 1 行），而不是回退成全租户扫描。
	var plan string
	if err := pool.QueryRow(ctx, `
		EXPLAIN (COSTS OFF)
		SELECT sd.gw_session_id
		FROM public.session_dim sd
		WHERE sd.tenant_id = 'tenant-demo' AND sd.session_key = 'session-demo'
	`).Scan(&plan); err != nil {
		t.Fatalf("explain: %v", err)
	}
	if !strings.Contains(plan, sessionDimSessionKeyIndexName) {
		t.Errorf("连接键查询没有走 %s：\n%s", sessionDimSessionKeyIndexName, plan)
	}
}
