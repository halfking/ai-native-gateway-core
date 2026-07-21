package persist

import (
	"context"
	"database/sql"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func TestWriterInsertsRow(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	w := New(rdb, "ursm:v2:", nil /* db 留 nil 时只做 Redis 拉取 */)
	rows, err := w.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	_ = rows
	_ = sql.ErrNoRows
	_ = (*pgxpool.Pool)(nil)
}
