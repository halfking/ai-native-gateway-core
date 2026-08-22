package freeresource

import (
	"context"
	"testing"
)

// TestPoolDedupTotals_Empty 验证空 catalog 返回 0, 不 panic.
func TestPoolDedupTotals_Empty(t *testing.T) {
	totals := &PoolDedupTotals{ByPool: map[string]int64{}}
	if totals.PoolHeadlineMonthlyTokens != 0 {
		t.Errorf("expected 0 monthly tokens, got %d", totals.PoolHeadlineMonthlyTokens)
	}
	if totals.PoolCount != 0 {
		t.Errorf("expected 0 pools, got %d", totals.PoolCount)
	}
	if got := totals.SortedPoolKeys(); len(got) != 0 {
		t.Errorf("expected no pool keys, got %v", got)
	}
}

// TestSortedPoolKeys 验证返回顺序.
func TestSortedPoolKeys(t *testing.T) {
	totals := &PoolDedupTotals{ByPool: map[string]int64{
		"zeta-pool":  100,
		"alpha-pool": 50,
		"mu-pool":    200,
	}}
	want := []string{"alpha-pool", "mu-pool", "zeta-pool"}
	got := totals.SortedPoolKeys()
	if len(got) != len(want) {
		t.Fatalf("expected %d keys, got %d", len(want), len(got))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("at %d: want %q, got %q", i, w, got[i])
		}
	}
}

// TestComputePoolDedupTotals_Live 真实 DB 集成测试: 验证 pool 去重逻辑.
// 需要 OMNIFREE_TEST_DB_URL (非超级用户) 与 OMNIFREE_TEST_DB_ADMIN_URL (超级用户).
func TestComputePoolDedupTotals_Live(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()
	admin := getTestAdminDB(t)
	if admin == nil {
		t.Skip("OMNIFREE_TEST_DB_ADMIN_URL not set; skipping live test")
	}
	defer admin.Close()

	ctx := context.Background()

	// Setup: 用 super_admin 插入两个共享 pool + 一个独立行.
	tx, err := admin.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		"SELECT set_config('app.current_role', 'super_admin', true)"); err != nil {
		t.Fatalf("set super_admin: %v", err)
	}
	seed := []struct {
		provider, model, pool, freeType string
		monthly                         int64
	}{
		{"test-r3", "r3-m1", "r3-shared-pool", "recurring-daily", 100},
		{"test-r3", "r3-m2", "r3-shared-pool", "recurring-daily", 200},
		{"test-r3", "r3-m3", "r3-shared-pool", "recurring-daily", 50}, // 应被 MAX 覆盖
		{"test-r3", "r3-m4", "", "recurring-monthly", 1000},           // 独立行
	}
	for _, s := range seed {
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO free_resource_catalog
                (provider_code, model_id, display_name, free_type,
                 monthly_tokens, pool_key, tos_verdict, enabled, tenant_id)
            VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), 'ok', TRUE, 'tenant-r3-pool-test')
            ON CONFLICT DO NOTHING
        `, s.provider, s.model, s.model, s.freeType, s.monthly, s.pool); err != nil {
			t.Fatalf("seed %s: %v", s.model, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	defer func() {
		if _, err := admin.ExecContext(ctx,
			"DELETE FROM free_resource_catalog WHERE tenant_id = 'tenant-r3-pool-test'"); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()

	totals, err := ComputePoolDedupTotals(ctx, db, "tenant-r3-pool-test")
	if err != nil {
		t.Fatalf("ComputePoolDedupTotals: %v", err)
	}

	// 期望:
	//   r3-shared-pool -> MAX(100, 200, 50) = 200
	//   __standalone__ -> 1000
	//   PoolHeadline = 200 + 1000 = 1200
	if totals.PoolCount != 2 {
		t.Errorf("expected 2 pools, got %d", totals.PoolCount)
	}
	if totals.ModelCount != 4 {
		t.Errorf("expected 4 models, got %d", totals.ModelCount)
	}
	if totals.PoolHeadlineMonthlyTokens != 1200 {
		t.Errorf("expected headline 1200, got %d", totals.PoolHeadlineMonthlyTokens)
	}
	if totals.ByPool["r3-shared-pool"] != 200 {
		t.Errorf("expected shared pool MAX=200, got %d", totals.ByPool["r3-shared-pool"])
	}
	if totals.ByPool["__standalone__"] != 1000 {
		t.Errorf("expected standalone=1000, got %d", totals.ByPool["__standalone__"])
	}
}
