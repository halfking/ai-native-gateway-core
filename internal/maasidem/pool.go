package maasidem

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Pool 是 MaaS 四个领域服务（ledger / inventory / trading / commercial）
// 所需的**最小 DB 能力面**。
//
// ★ 为什么不是 *pgxpool.Pool：
//
// 这四个包的构造参数原是 `*pgxpool.Pool`（具体类型），于是**没有任何办法**
// 在不连真库的前提下测它们的 SQL 与作用域判定——而 D7（跨租户写）与
// D9（跨租户冲正）恰恰是**只体现在 SQL 与分支顺序上**的缺陷：
//
//   - 租户校验错误 ⇒ 请求被拒（且不产生任何写入）；
//   - 租户校验缺失 ⇒ 请求继续往下走 ⇒ 为别的租户落了行。
//
// 两者在「有没有真的拒绝」上截然不同，**必须在能观察 DB 交互的测试里钉死**。
// 把具体类型换成窄接口后，pgxmock 的池即可注入，单测能断言
// 「拒绝路径上没有任何写入」与「查询带上了 tenant_id 过滤」。
//
// 兼容性：`*pgxpool.Pool` 天然满足本接口（Query / QueryRow / Exec / Begin
// 签名一致），故所有既有调用方**无需改动**；构造函数签名由具体类型放宽为
// 接口，是**放宽**而非收紧。
type Pool interface {
	Queryer
	Execer

	// Query 与 QueryRow：领域服务读价格版本、读结算行等。
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row

	// Begin：领域服务的事务边界（D7 的 quote + quote_lines 双写原子性）。
	Begin(ctx context.Context) (pgx.Tx, error)
}
