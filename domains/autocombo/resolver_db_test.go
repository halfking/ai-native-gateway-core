package autocombo

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"
	_ "github.com/lib/pq"
)

// getTestDB 复用 freeresource 包相同的 helper 模式, 但这是 autocombo 包,
// 我们不能跨包访问 freeresource 私有 helper. 这里直接定义本地版本.
func getTestDBForAutocombo(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("OMNIFREE_TEST_DB_URL")
	if dsn == "" {
		t.Skip("OMNIFREE_TEST_DB_URL not set; skipping live-DB integration test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("test db unreachable: %v", err)
	}
	return db
}

func getTestAdminDBForAutocombo(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("OMNIFREE_TEST_DB_ADMIN_URL")
	if dsn == "" {
		return nil
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	return db
}

// TestResolver_QueryDB_Live 验证 round 4 L3 Resolver.queryDB SQL 路径在
// live PG 上正确工作. 覆盖 sql: pq.Array + tenant fallback + 参数化绑定.
func TestResolver_QueryDB_Live(t *testing.T) {
	db := getTestDBForAutocombo(t)
	defer db.Close()
	admin := getTestAdminDBForAutocombo(t)
	if admin == nil {
		t.Skip("OMNIFREE_TEST_DB_ADMIN_URL not set; cannot seed")
	}
	defer admin.Close()

	ctx := context.Background()

	// Setup: 用 super_admin 写入一条 tenant-r3-resolver-test 的模板.
	tx, err := admin.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		"SELECT set_config('app.current_role', 'super_admin', true)"); err != nil {
		t.Fatalf("set super_admin: %v", err)
	}
	weights := ScoringWeights{HealthScore: 0.5, LatencyP95: 0.5}
	wJSON, _ := json.Marshal(weights)
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO auto_combo_templates (
            combo_name, display_name, variant, tier_filter, tos_filter,
            provider_allowlist, provider_denylist, model_pattern,
            scoring_weights_json, max_candidates, exploration_rate,
            enabled, tenant_id
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, '', $8, 50, 0.05, TRUE, $9)
        ON CONFLICT (combo_name, tenant_id) DO UPDATE
        SET scoring_weights_json = EXCLUDED.scoring_weights_json
    `, "auto/r3-test", "R3 Test Combo", "cheap",
		pq.Array([]string{"free"}),
		pq.Array([]string{"ok"}),
		pq.Array([]string{}),
		pq.Array([]string{}),
		wJSON,
		"tenant-r3-resolver-test"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	defer func() {
		if _, err := admin.ExecContext(ctx,
			"DELETE FROM auto_combo_templates WHERE tenant_id = 'tenant-r3-resolver-test'"); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()

	// 验证: Resolver.Resolve 应该命中 DB row, 返回 spec.
	r := NewResolver(db)
	spec, err := r.Resolve(ctx, "auto/r3-test", "tenant-r3-resolver-test")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if spec == nil {
		t.Fatal("expected non-nil spec from DB row")
	}
	if spec.Variant != VariantCheap {
		t.Errorf("expected variant cheap, got %s", spec.Variant)
	}
	if spec.MaxCandidates != 50 {
		t.Errorf("expected MaxCandidates=50, got %d", spec.MaxCandidates)
	}
}

// TestResolver_Resolve_DBNotFound_FallsBackToBuiltin 验证当 DB 查不到时,
// Resolver 回退到 builtinMap (auto/free 等).
func TestResolver_Resolve_DBNotFound_FallsBackToBuiltin(t *testing.T) {
	db := getTestDBForAutocombo(t)
	defer db.Close()

	r := NewResolver(db)
	spec, err := r.Resolve(context.Background(), "auto/free", "tenant-r3-resolver-builtin")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if spec == nil {
		t.Fatal("expected non-nil spec from builtin")
	}
	if spec.Variant != VariantCheap {
		t.Errorf("expected builtin variant cheap, got %s", spec.Variant)
	}
}

// TestResolver_Resolve_TenantIsolation 验证不同 tenant 看到不同 DB row.
// Tenant A 看 spec=VariantCoding, Tenant B 看 spec=VariantFast.
func TestResolver_Resolve_TenantIsolation(t *testing.T) {
	db := getTestDBForAutocombo(t)
	defer db.Close()
	admin := getTestAdminDBForAutocombo(t)
	if admin == nil {
		t.Skip("OMNIFREE_TEST_DB_ADMIN_URL not set; cannot seed")
	}
	defer admin.Close()

	ctx := context.Background()
	tx, err := admin.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		"SELECT set_config('app.current_role', 'super_admin', true)"); err != nil {
		t.Fatalf("set super_admin: %v", err)
	}
	weightsJSON, _ := json.Marshal(ScoringWeights{HealthScore: 0.5, LatencyP95: 0.5})
	for _, tenant := range []string{"tenant-r3-resolver-iso-a", "tenant-r3-resolver-iso-b"} {
		variant := "coding"
		if tenant == "tenant-r3-resolver-iso-b" {
			variant = "fast"
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO auto_combo_templates (
                combo_name, display_name, variant, tier_filter, tos_filter,
                provider_allowlist, provider_denylist, model_pattern,
                scoring_weights_json, max_candidates, exploration_rate,
                enabled, tenant_id
            ) VALUES ($1, $2, $3, $4, $5, $6, $7, '', $8, 50, 0.05, TRUE, $9)
            ON CONFLICT (combo_name, tenant_id) DO UPDATE SET variant = EXCLUDED.variant
        `, "auto/r3-iso-test", "R3 ISO Test", variant,
			pq.Array([]string{"free"}),
			pq.Array([]string{"ok"}),
			pq.Array([]string{}), pq.Array([]string{}), weightsJSON, tenant); err != nil {
			t.Fatalf("seed %s: %v", tenant, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	defer func() {
		admin.ExecContext(ctx,
			"DELETE FROM auto_combo_templates WHERE tenant_id IN ('tenant-r3-resolver-iso-a','tenant-r3-resolver-iso-b')")
	}()

	r := NewResolver(db)

	// Tenant A 应见 coding.
	specA, err := r.Resolve(ctx, "auto/r3-iso-test", "tenant-r3-resolver-iso-a")
	if err != nil {
		t.Fatalf("Resolve A: %v", err)
	}
	if specA == nil || specA.Variant != VariantCoding {
		t.Errorf("expected tenant A coding variant, got %v", specA)
	}

	// Tenant B 应见 fast.
	specB, err := r.Resolve(ctx, "auto/r3-iso-test", "tenant-r3-resolver-iso-b")
	if err != nil {
		t.Fatalf("Resolve B: %v", err)
	}
	if specB == nil || specB.Variant != VariantFast {
		t.Errorf("expected tenant B fast variant, got %v", specB)
	}

	// 其它 tenant 应回退到 builtin (DB row 不存在, builtin 也没这个组合 → error).
	specC, err := r.Resolve(ctx, "auto/r3-iso-test", "tenant-r3-resolver-other")
	if err == nil {
		t.Errorf("expected error for unknown tenant+combo, got spec=%v", specC)
	}
	if specC != nil {
		t.Errorf("expected nil spec on error, got %v", specC)
	}
}
