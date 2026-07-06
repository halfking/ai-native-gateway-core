// Package analysis — shared DB abstraction for session analytics engines.
//
// 所有分析引擎（tagger/summarizer/clusterer/optimizer）依赖此最小接口，
// 便于在 pgxpool.Pool 与测试桩之间解耦。
package analysis

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB 是分析引擎所需的最小数据库接口（*pgxpool.Pool 天然实现）。
type DB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// poolDB adapts *pgxpool.Pool to DB.
type poolDB struct{ p *pgxpool.Pool }

// NewPoolDB wraps a *pgxpool.Pool to satisfy DB.
func NewPoolDB(p *pgxpool.Pool) DB { return poolDB{p: p} }

func (d poolDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return d.p.QueryRow(ctx, sql, args...)
}
func (d poolDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return d.p.Query(ctx, sql, args...)
}
func (d poolDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return d.p.Exec(ctx, sql, args...)
}

// isNoRows reports whether err is pgx.ErrNoRows.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
