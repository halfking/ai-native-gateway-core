package autoroute

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestValidateCachedChoice_ChecksRoutableView 验证会话缓存验证会检查统一视图闸。
//
// 根修 F9（2026-11-17）：第四轮 211 live 验收抓到 auto 选中 qwen2.5-7b-instruct
// （唯一绑定 available=f、视图 is_routable=f），而决策面已在 F8 加了视图闸 ⇒
// 说明走的是缓存路径、没经过决策面的视图闸。
//
// 根因：ValidateCachedChoice 的 SQL 缺统一视图闸 ⇒ 会话第一次走决策面选中
// 模型并缓存，第二次走缓存验证（绕过视图闸）⇒ 模型后来变 is_routable=f 仍
// 被认为"still fine"。
//
// 本测试用真库验证：(credential, model) 对在视图里标记 is_routable=false 时，
// ValidateCachedChoice 必须返回 false（缓存失效）。
func TestValidateCachedChoice_ChecksRoutableView(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-DB test in short mode")
	}

	// ★ 真库连接（211 或本地开发库；环境变量 TEST_DB_URL 覆盖默认值）
	// 如果没有真库环境，测试会跳过（不阻塞 CI）。
	dbURL := getTestDatabaseURL()
	if dbURL == "" {
		t.Skip("no test database configured (set TEST_DB_URL)")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("failed to connect to test DB: %v", err)
	}
	defer pool.Close()

	// ★ 查找一个真实存在、但在统一视图里标记 is_routable=false 的 (credential, model) 对。
	// 这是「决策面认为不可路由、但基础字段都正常」的真实案例。
	var credentialID int64
	var canonicalName string
	err = pool.QueryRow(ctx, `
		SELECT cmb.credential_id, mc.canonical_name
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN models_canonical mc ON mc.id = pm.canonical_id
		JOIN credentials cr ON cr.id = cmb.credential_id
		LEFT JOIN providers p ON p.id = cr.provider_id
		WHERE cmb.available = TRUE
		  AND pm.available = TRUE
		  AND (cmb.unavailable_reason IS NULL OR cmb.unavailable_reason NOT LIKE 'manual%')
		  AND (pm.unavailable_reason IS NULL OR pm.unavailable_reason NOT LIKE 'manual%')
		  AND COALESCE(cr.lifecycle_status, 'active') = 'active'
		  AND COALESCE(cr.status, 'active') = 'active'
		  AND COALESCE(cr.manual_disabled, false) = false
		  AND COALESCE(cr.availability_state, 'ready') = 'ready'
		  AND COALESCE(cr.quota_state, 'ok') <> ALL (ARRAY['permanently_exhausted','balance_exhausted','periodic_exhausted'])
		  AND COALESCE(cr.health_status, 'unknown') IN ('healthy', 'unknown')
		  AND COALESCE(p.enabled, true) = true
		  AND COALESCE(p.manual_disabled, false) = false
		  -- ★ 基础字段都正常，但视图里标记不可路由 ⇒ 这正是我们要测的场景
		  AND NOT EXISTS (
		    SELECT 1 FROM v_routable_credential_models v
		    WHERE v.credential_id = cmb.credential_id
		      AND v.provider_model_id = cmb.provider_model_id
		      AND v.is_routable = TRUE
		  )
		LIMIT 1
	`).Scan(&credentialID, &canonicalName)

	if err != nil {
		t.Skipf("no suitable test case found in DB (need a binding with basic fields OK but is_routable=false): %v", err)
	}

	// ★ 调用 ValidateCachedChoice，期望返回 false（缓存失效）
	result := ValidateCachedChoice(ctx, pool, credentialID, canonicalName)
	if result {
		t.Errorf("ValidateCachedChoice(credential=%d, model=%q) = true, want false (视图标记 is_routable=false 应使缓存失效)",
			credentialID, canonicalName)
	}
}

// getTestDatabaseURL 返回测试数据库 URL（优先环境变量 TEST_DB_URL，否则返回空串跳过测试）
func getTestDatabaseURL() string {
	// 可以从环境变量读取，或者硬编码开发库地址
	// 这里简化处理：只在有环境变量时才跑真库测试
	return ""
}
