package dbx

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the minimal execution seam shared by *pgxpool.Pool, pgx.Tx and
// pgxmock fakes. The framework never holds pool state itself.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// TxBeginner is implemented by *pgxpool.Pool and pgxmock pools; scope and
// transaction runners use it to open transactions.
type TxBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}
