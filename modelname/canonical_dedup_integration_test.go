//go:build integration

package modelname_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

// TestDedupCanonicalNameSQL_Behavior（R50 F19 行为级钉桩，-tags integration
// 运行）：admin createModel 查重谓词在真 PG 上的命中语义。与守卫谓词
// 刻意不同的两处在此钉死：run-collapse 臂命中连续分隔符变体；不带
// status 过滤（disabled 拼写同样占用名字空间）。
func TestDedupCanonicalNameSQL_Behavior(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("db_test"),
		postgres.WithUsername("db_test"),
		postgres.WithPassword("db_test"),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	defer func() {
		termCtx, termCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer termCancel()
		_ = container.Terminate(termCtx)
	}()

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	var pool *pgxpool.Pool
	for attempt := 0; attempt < 30; attempt++ {
		pool, err = pgxpool.New(ctx, dsn)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				break
			}
			pool.Close()
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `
		CREATE TABLE models_canonical (
			id bigserial PRIMARY KEY,
			canonical_name text NOT NULL UNIQUE,
			status text NOT NULL DEFAULT 'active'
		)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	type seedRow struct {
		name   string
		status string
	}
	for _, s := range []seedRow{
		{"glm-5.3", "active"},
		{"glm-4.7", "disabled"},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO models_canonical (canonical_name, status) VALUES ($1, $2)`, s.name, s.status); err != nil {
			t.Fatalf("seed %s: %v", s.name, err)
		}
	}

	cases := []struct {
		candidate   string
		wantBlocker string // "" = allow create
	}{
		{"glm-5.3", "glm-5.3"},      // 精确等值（lower 臂）
		{"GLM-5.3", "glm-5.3"},      // 大小写折叠
		{"glm--5.3", "glm-5.3"},     // 连续分隔符 → run-collapse 臂命中（R50 修复）
		{"glm_5_3", "glm-5.3"},      // 分隔符互换折叠
		{"glm5.3", ""},              // 契约边界：无分隔符 ≠ 有分隔符（fold 后 glm5_3 ≠ glm_5_3，与 NormalizeRouteKey 不转 dot↔dash 口径一致）
		{"glm-4-7", "glm-4.7"},      // disabled 行同样占用名字空间（无 status 过滤）
		{"totally-new-model", ""},   // 放行
		{"totally--new--model", ""}, // 放行（无既有名）
	}
	for _, tc := range cases {
		var blocker string
		err := pool.QueryRow(ctx, modelname.DedupCanonicalNameSQL, tc.candidate).Scan(&blocker)
		if tc.wantBlocker == "" {
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Errorf("candidate %q: expected allow, got err=%v blocker=%q", tc.candidate, err, blocker)
			}
			continue
		}
		if err != nil {
			t.Errorf("candidate %q: dedup query failed: %v", tc.candidate, err)
			continue
		}
		if blocker != tc.wantBlocker {
			t.Errorf("candidate %q: blocker = %q, want %q", tc.candidate, blocker, tc.wantBlocker)
		}
	}
}
