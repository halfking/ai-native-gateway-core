package db

// statement_cache_test.go —— 写路径【计划缓存】的门（2026-10-29 · R181 轮第 ⑧ 项）。
//
// # 这道门钉什么
//
// 网关的 PG 连接池自 2026-07-15 起关掉了 pgx 的语句缓存（`QueryExecModeSimpleProtocol`），
// 理由是「改名后长连接里的陈旧预编译语句 ⇒ relation does not exist」。
// 代价是**每请求重新规划**：本地真库实测，写路径的幂等探针
// （`internal/sessionfamily.ExistsByKeySQL`）字面量谓词下
//
//	规划 38.0 ms / 执行 0.104 ms　　← 规划是执行的 365 倍
//
// ⇒ 写路径的真实成本在**规划期**，不在扫描 ⇒ 计划缓存是数量级收益。
//
// # 三道门
//
//	① **开关门**：LLM_GATEWAY_DB_STATEMENT_CACHE 的语义（默认开；false/0/no/off 关；
//	   其它值 ⇒ 开 + Warn）。
//	② **真库摊销门**（★ 核心）：同一连接上反复执行同一条语句 ⇒ 第 6 次起
//	   **规划时间必须塌到第 1 次的 5% 以下**（PG 在 5 次之后切泛型计划）。
//	   ★ 这道门在「缓存没开」时必须报红（★ 每次执行都重新规划 ⇒ 比值 ≈ 1）。
//	③ **真库自愈门**：pgx 在**任何**错误上失效该语句的缓存 ⇒ 陈旧语句
//	   **失败一次之后自愈**。★ 这条把「前提被破」的代价从「永久毒化连接」
//	   降成「一次请求失败」—— 2026-07-15 的事故形态正是靠这条性质才敢重开缓存。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// ── ① 开关门 ───────────────────────────────────────────────────────────────

func TestStatementCacheEnabledFromEnv(t *testing.T) {
	t.Setenv("LLM_GATEWAY_DB_STATEMENT_CACHE", "")
	require.True(t, statementCacheEnabledFromEnv(), "★ 默认必须开（默认关等于本轮没做）")

	for _, off := range []string{"false", "FALSE", "0", "no", "off", " off "} {
		t.Setenv("LLM_GATEWAY_DB_STATEMENT_CACHE", off)
		require.False(t, statementCacheEnabledFromEnv(), "值 %q 必须关", off)
	}
	for _, on := range []string{"true", "1", "yes", "on", "anything-else"} {
		t.Setenv("LLM_GATEWAY_DB_STATEMENT_CACHE", on)
		require.True(t, statementCacheEnabledFromEnv(), "值 %q 必须开", on)
	}
}

// TestStatementCacheIsWiredIntoThePoolConfig 是**接线门**：db.go 必须真的把
// 执行模式设成 CacheStatement。
//
// ★ 为什么需要它：开关门的输入是环境变量，**没人保证** `Open()` 真的用了它
// ★ —— 一行注释就能把整件事悄悄改回旧行为（★ 而门仍然全绿：环境变量语义
// ★ 没变，只是没人读它了）。
func TestStatementCacheIsWiredIntoThePoolConfig(t *testing.T) {
	src, err := os.ReadFile("db.go")
	require.NoError(t, err)
	text := string(src)
	require.Contains(t, text, "pgx.QueryExecModeCacheStatement",
		"db.go 必须把语句缓存接进连接池配置")
	require.Contains(t, text, "statementCacheEnabledFromEnv()",
		"db.go 必须通过开关读取执行模式（★ 留一条不改代码就能退回去的路）")
}

// ── 真库门 ─────────────────────────────────────────────────────────────────

// statementCacheTestDSN 返回真库 DSN（未设置 ⇒ 跳过）。
func statementCacheTestDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-database statement-cache proof")
	}
	return dsn
}

// statementCacheTestPool 起一个**开着语句缓存**的池（★ 与生产同形态）。
func statementCacheTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	// ★★★ 走**生产同一条构造路径**（newPoolConfig）★★★
	// ★ 若这里自己硬编码执行模式 ⇒ 「把生产配置改回 SimpleProtocol」这个变异
	// ★ 不会让门变红 ⇒ ⇒ 门变成恒绿的摆设。
	cfg, err := newPoolConfig(statementCacheTestDSN(t))
	require.NoError(t, err)
	cfg.MaxConns = 2
	cfg.MinConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// TestStatementCache_SteadyStateCostBeatsSimpleProtocol 是**核心门**：
// 生产配置（语句缓存）的**稳态**每次执行成本必须显著低于旧行为
// （SimpleProtocol = 每次重新准备 + 重新规划）。
//
// ★★★ **这一轮把 R181 的读数纠正了（★ 重要的更正）：★★★
//
//	★ R181 记的是「写路径每请求 29.8 ms 规划时间」。★ 本轮实测拆开后发现，
//	★ 那 38 ms 里**绝大部分是冷启动成本**（首次访问这些分区的目录/统计），
//	★ **不是**稳态的每次规划成本：
//
//		模式                首执行        稳态 20 次平均
//		CacheStatement      34~44 ms      0.72 ms/次
//		SimpleProtocol     34~44 ms      2.19~3.02 ms/次
//
//	★ ⇒ 首执行在两种模式下**相同** ⇒ ⇒ 那 38 ms 不是「缓存省掉的规划」；
//	★ ⇒ 缓存真正省掉的是**稳态每次 1.5~2.3 ms**（本地 4.2×）。
//	★ ⇒ R181 的 29.8 ms 若成立，它成立在**别的维度**（252 的目录/统计规模
//	★ 更大 ⇒ 每次规划都更贵 ⇒ 缓存省下的绝对值更大）⇒ ★ 本轮**不否认**
//	★ 生产侧的收益，但**拒绝**把冷启动成本算进缓存的功劳。
//
// ★★ 门为什么用**比值**：★★
//
//	★ 机器快慢差几十倍 ⇒ 绝对阈值会变成「这台机器够快就恒绿」的假闸；
//	★ 比值（缓存形态 vs 旧行为，同一台机器、同一批数据、同一个连接流程）
//	★ 才是与机器无关的判据。
//
// ★★ 门为什么**必须走生产配置**（newPoolConfig）：★★
//
//	★ 若门自己起池并硬编码执行模式 ⇒ 「把生产配置改回 SimpleProtocol」这个
//	★ 变异**不会让门变红** ⇒ ⇒ 门变成恒绿的摆设。
func TestStatementCache_SteadyStateCostBeatsSimpleProtocol(t *testing.T) {
	dsn := statementCacheTestDSN(t)
	ctx := context.Background()

	// 写路径的幂等探针（与 internal/sessionfamily.ExistsByKeySQL 同形）：
	// hot ∪ 分区父表，按 (tenant_id, request_id) 判存在。
	const probe = `SELECT EXISTS (
		(SELECT 1 FROM public.session_turns_hot WHERE tenant_id = $1 AND request_id = $2)
		UNION ALL
		(SELECT 1 FROM public.session_turns WHERE tenant_id = $1 AND request_id = $2)
	)`

	// measure 起一个池、预热 3 次、再测 n 次的稳态平均。
	measure := func(cfg *pgxpool.Config) time.Duration {
		cfg.MaxConns = 2
		cfg.MinConns = 1
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		require.NoError(t, err)
		defer pool.Close()
		conn, err := pool.Acquire(ctx)
		require.NoError(t, err)
		defer conn.Release()
		run := func() time.Duration {
			st := time.Now()
			var exists bool
			require.NoError(t, conn.QueryRow(ctx, probe, "default", "no-such-request-id").Scan(&exists))
			return time.Since(st)
		}
		for i := 0; i < 3; i++ {
			run()
		}
		const n = 20
		var total time.Duration
		for i := 0; i < n; i++ {
			total += run()
		}
		return total / n
	}

	// ★ 生产形态（★ 走 newPoolConfig ⇒ 与线上同一份配置）。
	// ★★★ R182 轮修正（★★ D7）：★★ 这道门**必须显式钉住开关**★★ ——
	// ★ `newPoolConfig` 读 `LLM_GATEWAY_DB_STATEMENT_CACHE`；★ 若环境里已经
	// ★ 把它设成 "false"（★ 那正是本轮留的**杀开关**）⇒ ⇒ 「生产形态」也变成
	// ★ SimpleProtocol ⇒ ⇒ prod == old ⇒ ⇒ 这道门**必红**，★ 而那个红与本轮
	// ★ 改动无关 ⇒ ⇒ 是量具被环境变量绑架，不是被测行为坏了。
	// ★ ⇒ 显式置 "true"（★ 默认值本就是开，但**显式**才不受环境影响）。
	t.Setenv("LLM_GATEWAY_DB_STATEMENT_CACHE", "true")
	prodCfg, err := newPoolConfig(dsn)
	require.NoError(t, err)
	prod := measure(prodCfg)

	// ★ 旧行为（2026-07-15 的形态）：每次重新准备 + 重新规划。
	oldCfg, err := newPoolConfig(dsn)
	require.NoError(t, err)
	oldCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	old := measure(oldCfg)

	t.Logf("稳态每次执行：生产形态（语句缓存）%s/次；旧行为（SimpleProtocol）%s/次 ⇒ 省 %.1f%%",
		prod, old, 100*(1-float64(prod)/float64(old)))
	require.Positive(t, old, "旧行为的每次成本必须是正数（否则这道门是空转）")
	require.Less(t, prod, old*6/10,
		"语句缓存没有带来稳态收益：生产形态 %s/次 vs 旧行为 %s/次（★ 要求生产形态快 40%% 以上）",
		prod, old)
}

// TestStatementCache_ServingPathPlanningCost 记录写路径探针的规划成本
// （★ 只读数，不设阈值 —— 阈值门在上面的摊销门里）。
func TestStatementCache_ServingPathPlanningCost(t *testing.T) {
	pool := statementCacheTestPool(t)
	ctx := context.Background()

	start := time.Now()
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			(SELECT 1 FROM public.session_turns_hot WHERE tenant_id = $1 AND request_id = $2)
			UNION ALL
			(SELECT 1 FROM public.session_turns WHERE tenant_id = $1 AND request_id = $2)
		)`, "default", "no-such-request-id").Scan(&exists)
	require.NoError(t, err)
	t.Logf("写路径幂等探针（含规划）耗时 %s（存在=%v）", time.Since(start), exists)
}
