package provider

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestLoadCandidatesDB_CrossTenantProviderSharing 验证跨租户供应商共享逻辑。
//
// 架构说明：
// - 供应商配置在 'default' 租户下（全局共享）
// - 所有租户的 API Key 可以访问 'default' 租户下的供应商
// - 租户模型策略是"黑名单"机制（默认全开，只配置禁用的模型）
//
// 测试场景：
// 1. default 租户的 API Key 访问 default 租户的供应商 ✅
// 2. hansi 租户的 API Key 访问 default 租户的供应商 ✅
// 3. hansi 租户的 API Key 访问 hansi 租户的私有供应商 ✅
// 4. hansi 租户的 API Key 不能访问 other 租户的私有供应商 ❌
//
// 修复问题：
// - 2026-07-08 P0: 修复租户隔离导致的"无可用路由"错误
// - 原因：loadCandidatesDB 只查询当前租户的供应商 (WHERE p.tenant_id = $2)
// - 修复：支持跨租户供应商共享 (WHERE p.tenant_id = $2 OR p.tenant_id = 'default')
func TestLoadCandidatesDB_CrossTenantProviderSharing(t *testing.T) {
	// 这是一个集成测试，需要真实的数据库连接
	// 在 CI 环境中可以跳过
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// 从环境变量读取数据库连接字符串
	dbURL := getTestDatabaseURL()
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("failed to connect to database: %v", err)
	}
	defer pool.Close()

	// 创建测试 Client
	client := NewClient()
	client.SetDB(pool, "test-secret-key", "test-credential-key")

	// 测试数据准备（使用事务回滚确保测试隔离）
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 1. 创建 default 租户的供应商
	var defaultProviderID int
	err = tx.QueryRow(ctx, `
		INSERT INTO providers (tenant_id, name, base_url, protocol)
		VALUES ('default', 'test-provider-default', 'https://api.example.com', 'openai')
		RETURNING id
	`).Scan(&defaultProviderID)
	if err != nil {
		t.Fatalf("failed to create default provider: %v", err)
	}

	// 2. 创建 hansi 租户的私有供应商
	var hansiProviderID int
	err = tx.QueryRow(ctx, `
		INSERT INTO providers (tenant_id, name, base_url, protocol)
		VALUES ('hansi', 'test-provider-hansi', 'https://api.hansi.com', 'openai')
		RETURNING id
	`).Scan(&hansiProviderID)
	if err != nil {
		t.Fatalf("failed to create hansi provider: %v", err)
	}

	// 3. 创建 other 租户的私有供应商
	var otherProviderID int
	err = tx.QueryRow(ctx, `
		INSERT INTO providers (tenant_id, name, base_url, protocol)
		VALUES ('other', 'test-provider-other', 'https://api.other.com', 'openai')
		RETURNING id
	`).Scan(&otherProviderID)
	if err != nil {
		t.Fatalf("failed to create other provider: %v", err)
	}

	// 4. 为每个供应商创建凭据和模型
	testModel := "gpt-4-test"
	for _, providerID := range []int{defaultProviderID, hansiProviderID, otherProviderID} {
		var credID int
		err = tx.QueryRow(ctx, `
			INSERT INTO credentials (provider_id, lifecycle_status, availability_state)
			VALUES ($1, 'active', 'ready')
			RETURNING id
		`, providerID).Scan(&credID)
		if err != nil {
			t.Fatalf("failed to create credential for provider %d: %v", providerID, err)
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO model_offers (credential_id, raw_model_name, routing_tier)
			VALUES ($1, $2, 1)
		`, credID, testModel)
		if err != nil {
			t.Fatalf("failed to create model offer for credential %d: %v", credID, err)
		}
	}

	// 提交测试数据
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("failed to commit test data: %v", err)
	}

	// 运行测试用例
	t.Run("default_tenant_access_default_provider", func(t *testing.T) {
		cands, err := client.loadCandidatesDB(ctx, testModel, "default")
		if err != nil {
			t.Fatalf("loadCandidatesDB failed: %v", err)
		}

		// 应该能看到 default 租户的供应商
		foundDefault := false
		for _, c := range cands {
			if c.ProviderID == defaultProviderID {
				foundDefault = true
				break
			}
		}
		if !foundDefault {
			t.Errorf("default tenant should see default provider, but didn't find it")
		}
	})

	t.Run("hansi_tenant_access_default_provider", func(t *testing.T) {
		cands, err := client.loadCandidatesDB(ctx, testModel, "hansi")
		if err != nil {
			t.Fatalf("loadCandidatesDB failed: %v", err)
		}

		// 应该能看到 default 租户的供应商（跨租户共享）
		foundDefault := false
		for _, c := range cands {
			if c.ProviderID == defaultProviderID {
				foundDefault = true
				break
			}
		}
		if !foundDefault {
			t.Errorf("hansi tenant should see default provider (cross-tenant sharing), but didn't find it")
		}
	})

	t.Run("hansi_tenant_access_hansi_provider", func(t *testing.T) {
		cands, err := client.loadCandidatesDB(ctx, testModel, "hansi")
		if err != nil {
			t.Fatalf("loadCandidatesDB failed: %v", err)
		}

		// 应该能看到 hansi 租户的私有供应商
		foundHansi := false
		for _, c := range cands {
			if c.ProviderID == hansiProviderID {
				foundHansi = true
				break
			}
		}
		if !foundHansi {
			t.Errorf("hansi tenant should see hansi private provider, but didn't find it")
		}
	})

	t.Run("hansi_tenant_cannot_access_other_provider", func(t *testing.T) {
		cands, err := client.loadCandidatesDB(ctx, testModel, "hansi")
		if err != nil {
			t.Fatalf("loadCandidatesDB failed: %v", err)
		}

		// 不应该看到 other 租户的私有供应商
		foundOther := false
		for _, c := range cands {
			if c.ProviderID == otherProviderID {
				foundOther = true
				break
			}
		}
		if foundOther {
			t.Errorf("hansi tenant should NOT see other tenant's private provider, but found it")
		}
	})

	// 清理测试数据
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = pool.Exec(cleanupCtx, `DELETE FROM model_offers WHERE raw_model_name = $1`, testModel)
	_, _ = pool.Exec(cleanupCtx, `DELETE FROM credentials WHERE provider_id IN ($1, $2, $3)`, 
		defaultProviderID, hansiProviderID, otherProviderID)
	_, _ = pool.Exec(cleanupCtx, `DELETE FROM providers WHERE id IN ($1, $2, $3)`, 
		defaultProviderID, hansiProviderID, otherProviderID)
}

// getTestDatabaseURL 从环境变量读取测试数据库连接字符串
func getTestDatabaseURL() string {
	return os.Getenv("TEST_DATABASE_URL")
}
