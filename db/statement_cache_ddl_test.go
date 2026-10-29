package db

// statement_cache_ddl_test.go —— 语句缓存的【DDL 门】的**独立文件**
// （2026-10-29 · R181 轮第 ⑧ 项）。
//
// ★★ 为什么必须单独一个文件：★★
//
//	★ 仓内有一道隔离门（db_package_isolation_gate_test.go）：★ db 包的测试文件
//	★ 若含**破坏性 DDL**（DROP TABLE 等）⇒ ★ 它必须走**一次性空库** helper
//	★ （scratchPool）★ ⇒ 否则它会直连共享 DSN 指向的那个库并把它洗成玩具形态。
//	★ ⇒ ⇒ 破坏性 DDL 与「读共享库做测量」必须**分居两个文件**：
//	★ 本文件只用一次性库；statement_cache_test.go 只读共享库、不含破坏性 DDL。
//
// ★ 本门测什么（★ 两条真实形态，★ 逐条实测，不是推断）：★★
//
//	① **DROP + 同名重建** ⇒ 缓存的语句**照常可用**（★ PG 的计划缓存按依赖 OID
//	   失效 ⇒ 同名重建后 OID 变了 ⇒ PG 自动重新规划 ⇒ **不报错**）。
//	   ★ 这是运行期最常见的 DDL（分区增删）⇒ ⇒ 缓存对它天然安全。
//	② **RENAME** ⇒ 这才是 2026-07-15 的事故形态（★ 解析树里仍写着旧名 ⇒
//	   42P01）⇒ ★ 门断言的是**连接没有被毒化**：★ 出错之后同一条连接上别的
//	   语句照常可用 ⇒ ⇒ 「前提被破」的代价 = 一次请求失败，**不是**整条连接报废。

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// TestStatementCache_DDLDoesNotPoisonTheCache 是**DDL 门**：两条真实形态。
//
// ★ 形态一（DROP + 同名重建）：★ 预编译语句**照常可用**——★ PG 的计划缓存按
// ★ **依赖（OID）**失效，★ 同名重建后 OID 变了 ⇒ PG 自动重新规划 ⇒ **不报错**。
// ★ ⇒ 这是运行期最常见的 DDL（分区增删）⇒ ⇒ 缓存对它天然安全。
//
// ★ 形态二（RENAME）：★ 这才是 2026-07-15 的事故形态——★ 解析树里仍写着旧名
// ★ ⇒ 报 42P01。★ 门断言的是**连接没有被毒化**：★ 出错之后同一条连接上
// ★ 别的语句照常可用，且失败语句在下一次会**重新准备**（★ pgx 在任何错误上
// ★ 失效缓存）⇒ ⇒ 「前提被破」的代价 = 一次请求失败，**不是**整条连接报废。
func TestStatementCache_DDLDoesNotPoisonTheCache(t *testing.T) {
	// ★ 一次性空库（★ 破坏性 DDL 只发生在它上面）★ —— 再用**生产同一条**
	// ★ 构造路径（newPoolConfig）起池 ⇒ 测的就是生产的执行模式。
	scratch := scratchPool(t)
	// ★ 显式钉住开关（★ R182 轮 D7）：★ 否则环境里若已设 "false"（★ 杀开关）
	// ★ ⇒ 「生产形态」也退化成 SimpleProtocol ⇒ ⇒ 这道门测不到缓存路径。
	t.Setenv("LLM_GATEWAY_DB_STATEMENT_CACHE", "true")
	cfg, err := newPoolConfig(scratchDSN(scratch))
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	ctx := context.Background()

	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()

	const table = "stmt_cache_ddl_probe"
	_, err = conn.Exec(ctx, "DROP TABLE IF EXISTS "+table+"_new")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "DROP TABLE IF EXISTS "+table)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "CREATE TABLE "+table+" (id int)")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+table+"_new")
		_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+table)
	})

	// ★★★ R182 轮修正（★★ 这道门原本【没有牙齿】★★）：★★
	// ★ 原写法是 SQL 层的 `PREPARE … AS` / `EXECUTE …` ⇒ ⇒ 那是 **PostgreSQL
	// ★ 服务端**的预备语句，与 pgx 的**客户端语句缓存**（`conn.statementCache`）
	// ★ **完全是两码事** ⇒ ⇒ 而且这些语句**一个参数都没有** ⇒ ⇒ pgx 在
	// ★ `vendor/github.com/jackc/pgx/v5/conn.go:515-518` 处会**强制切到
	// ★ simple protocol** ⇒ ⇒ **根本走不到 `QueryExecModeCacheStatement`
	// ★ 那条分支** ⇒ ⇒ 「缓存的语句照常可用」「坏语句不得毒化连接」
	// ★ 这两条断言**恒真** ⇒ ⇒ 即便 pgx 压根没有缓存也照样过。
	// ★ ⇒ 改用**带参数**的语句 ⇒ ⇒ pgx 真正 prepare + 真正进缓存。
	const probe = "SELECT count(*) FROM " + table + " WHERE id = $1"

	var n int
	require.NoError(t, conn.QueryRow(ctx, probe, 1).Scan(&n), "第一次执行必须成功")
	require.Zero(t, n)

	// ── 形态一：DROP + 同名重建 ⇒ 必须照常可用（PG 按依赖 OID 失效并重规划）。
	_, err = conn.Exec(ctx, "DROP TABLE "+table)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "CREATE TABLE "+table+" (id int)")
	require.NoError(t, err)
	require.NoError(t, conn.QueryRow(ctx, probe, 1).Scan(&n),
		"★ DROP + 同名重建之后，缓存的语句必须照常可用（PG 按依赖失效并重规划）")
	require.Zero(t, n)

	// ── 形态二：RENAME ⇒ 必然 42P01（★ 前提：解析树里仍是旧名）。
	_, err = conn.Exec(ctx, "ALTER TABLE "+table+" RENAME TO "+table+"_new")
	require.NoError(t, err)
	err = conn.QueryRow(ctx, probe, 1).Scan(&n)
	require.Error(t, err, "★ 改名之后，引用旧名的缓存语句必须报 42P01（前提成立）")

	// ★ 关键断言一：★ 连接**没有被毒化**——★ 同一条连接上别的语句照常可用。
	// ★ ★ 用**带参数**的语句 ⇒ ⇒ 它也真的走缓存路径（而不是被 simple protocol 绕开）。
	var other int
	require.NoError(t, conn.QueryRow(ctx, "SELECT $1::int", 1).Scan(&other),
		"★ 一条坏语句不得毒化整条连接（★ 这是重开缓存的安全依据）")
	require.Equal(t, 1, other)

	// ★ 关键断言二（★ 这条才是【缓存真的参与】的可证伪证据 ★）：★★
	// ★ 把名字改回来 ⇒ ⇒ 同一段 SQL（仍写旧名，此时旧名已恢复）必须
	// ★ **重新可用** ⇒ ⇒ 而这只在【pgx 在出错时把缓存条目失效了】时成立
	// ★ （`conn.go:520-529` 的 defer：`if err != nil { sc.Invalidate(sql) }`）
	// ★ ⇒ ⇒ 若 pgx 不失效，这条语句会一直拿着**已死的解析树**⇒ 永远失败。
	_, err = conn.Exec(ctx, "ALTER TABLE "+table+"_new RENAME TO "+table)
	require.NoError(t, err)
	require.NoError(t, conn.QueryRow(ctx, probe, 1).Scan(&n),
		"★ 出错之后，同一条 SQL 必须能【重新准备并成功】⇒ ⇒ 这证明 pgx 在出错时失效了缓存条目")
	require.Zero(t, n)
}

