// Package moduleexec - dbtx.go
// 定义数据库操作接口，支持 pgxmock 测试注入
package moduleexec

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX 数据库操作接口（兼容 *pgxpool.Pool 和 pgxmock.PgxPoolIface）
//
// 用于 Executor 的数据库操作，测试时可注入 mock 实现。
// 方法签名与 pgxpool.Pool 一致。
type DBTX interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
