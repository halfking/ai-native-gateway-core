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

// TestJunkSeedGuardSQL_Behavior（R50 行为级钉桩，-tags integration 运行）：
// R48/R49 的守卫谓词全是 SQL 字符串钉桩，两臂全死（stdName dash 形 vs 左侧
// 下划线折叠、截断臂 '%-' 永不匹配）却零测试暴露——本用例对真 PG 验证
// 修正后谓词的真实命中行为。
func TestJunkSeedGuardSQL_Behavior(t *testing.T) {
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
	// testcontainers 首端口映射后 PG 仍需数秒进入 ready；对齐 db 包
	// waitForLedgerEnsurePool 的重试定式。
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

	// 最小表形：只含守卫谓词触碰的列。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE models_canonical (
			id bigserial PRIMARY KEY,
			canonical_name text NOT NULL UNIQUE,
			status text NOT NULL DEFAULT 'active'
		)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	seed := []string{"claude-opus-5", "glm-5.3"}
	for _, name := range seed {
		if _, err := pool.Exec(ctx,
			`INSERT INTO models_canonical (canonical_name) VALUES ($1)`, name); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	cases := []struct {
		stdName     string
		wantBlocker string // "" = no blocker (seeding allowed)
	}{
		{"opus-5", "claude-opus-5"},        // 截断形 → 截断臂必须命中
		{"claude-opus-5", "claude-opus-5"}, // 精确等值 → 相等臂命中
		{"totally-new-model", ""},          // 无既有名 → 放行回种
		// 契约边界（守卫不做、也不应做）：
		// - dot/dash 互换（"glm5.3" vs "glm-5.3"）：NormalizeRouteKey 文档
		//   明示不转换 dot↔dash，属不同 route key；
		// - 连续分隔符（"claude--opus-5"）：NormalizeRouteKey 输出已折叠
		//   运行，调用方不变量保证 stdName 不含 "--"。
		// 这两类输入若出现在调用方即违反 stdName=NormalizeRouteKey(model)
		// 前置条件；admin/models.go 的 createModel 查重另有 run-collapse。
		{"glm5.3", ""},
		{"claude--opus-5", ""},
	}
	for _, tc := range cases {
		var blocker string
		err := pool.QueryRow(ctx, modelname.JunkSeedGuardSQL, tc.stdName).Scan(&blocker)
		if tc.wantBlocker == "" {
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Errorf("stdName %q: expected no blocker, got err=%v blocker=%q", tc.stdName, err, blocker)
			}
			continue
		}
		if err != nil {
			t.Errorf("stdName %q: guard query failed: %v", tc.stdName, err)
			continue
		}
		if blocker != tc.wantBlocker {
			t.Errorf("stdName %q: blocker = %q, want %q", tc.stdName, blocker, tc.wantBlocker)
		}
	}
}
