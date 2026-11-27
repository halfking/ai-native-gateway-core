package maas

// 三级配额制（租户→组织→用户）组织/用户级预检判据（2026-11-27）。
//
// 数据库判据走既有 TEST_DATABASE_URL 通道（同 balance_precheck_test.go）；
// **调用点判据（gate-with-call-site 纪律）不依赖数据库**：四面入口必须真实
// 调用 PreCheckOrgAndUserForOwner / tieredBalanceGate——team_budget_gate
// 「写好零调用点至今」的教训 ⇒ 调用点缺失判据当场红。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestOrgUser 建组织 + 用户 + 对应钱包，返回 (orgID, userID)。
func setupTestOrgUser(t *testing.T, pool *pgxpool.Pool, orgBalance, orgLimit int64, orgPolicy string, userBalance, userLimit int64, userPolicy string) (int, int) {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var tenantID string
	err := pool.QueryRow(ctx, `
		INSERT INTO tenants (code, name) VALUES ($1, $1)
		ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name
		RETURNING code
	`, fmt.Sprintf("tiered-test-%d", suffix)).Scan(&tenantID)
	require.NoError(t, err)

	var orgID int
	err = pool.QueryRow(ctx, `
		INSERT INTO organizations (tenant_id, code, name)
		VALUES ($1, $2, $2) RETURNING id
	`, tenantID, fmt.Sprintf("org-%d", suffix)).Scan(&orgID)
	require.NoError(t, err)

	var userID int
	err = pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, username, password_hash, org_id)
		VALUES ($1, $2, 'x', $3) RETURNING id
	`, tenantID, fmt.Sprintf("tiered-user-%d", suffix), orgID).Scan(&userID)
	require.NoError(t, err)

	if orgBalance != 0 || orgLimit != 0 || orgPolicy != "" {
		_, err = pool.Exec(ctx, `
			INSERT INTO org_credit_wallets (org_id, tenant_id, balance_credits, credit_limit_credits, credit_policy)
			VALUES ($1, $2, $3, NULLIF($4, 0), $5)
		`, orgID, tenantID, orgBalance, orgLimit, orgPolicy)
		require.NoError(t, err)
	}
	if userBalance != 0 || userLimit != 0 || userPolicy != "" {
		_, err = pool.Exec(ctx, `
			INSERT INTO user_credit_wallets (user_id, tenant_id, balance_credits, credit_limit_credits, credit_policy)
			VALUES ($1, $2, $3, NULLIF($4, 0), $5)
		`, userID, tenantID, userBalance, userLimit, userPolicy)
		require.NoError(t, err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_credit_wallets WHERE user_id = $1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM org_credit_wallets WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE code = $1`, tenantID)
	})
	return orgID, userID
}

// TestPreCheckWalletBalance_OrgBlockOverLimit：org 余额负、超信用额度 ⇒ block。
func TestPreCheckWalletBalance_OrgBlockOverLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}
	pool := setupTestDB(t)
	svc := &Service{pool: pool}
	orgID, _ := setupTestOrgUser(t, pool, -2000, 1000, "warn", 0, 0, "")

	d, err := svc.PreCheckWalletBalance(context.Background(), PrecheckScopeOrg, orgID, 100)
	require.NoError(t, err)
	assert.False(t, d.Allow, "org over credit limit must block")
	assert.Equal(t, "block", d.Policy)
}

// TestPreCheckWalletBalance_UserBlockPolicy：user 余额负但在额度内、policy=block ⇒ block。
func TestPreCheckWalletBalance_UserBlockPolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}
	pool := setupTestDB(t)
	svc := &Service{pool: pool}
	_, userID := setupTestOrgUser(t, pool, 0, 0, "", -500, 1000, "block")

	d, err := svc.PreCheckWalletBalance(context.Background(), PrecheckScopeUser, userID, 100)
	require.NoError(t, err)
	assert.False(t, d.Allow, "user negative balance with policy=block must block even within credit line")
	assert.Equal(t, "block", d.Policy)
}

// TestPreCheckWalletBalance_NullLimitUnlimited：credit_limit NULL ⇒ 不限制（负债也放行 warn）。
func TestPreCheckWalletBalance_NullLimitUnlimited(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}
	pool := setupTestDB(t)
	svc := &Service{pool: pool}
	_, userID := setupTestOrgUser(t, pool, 0, 0, "", -99999, 0, "warn")

	d, err := svc.PreCheckWalletBalance(context.Background(), PrecheckScopeUser, userID, 100)
	require.NoError(t, err)
	assert.True(t, d.Allow, "NULL credit_limit = unlimited ⇒ negative balance allowed in warn mode")
	assert.Equal(t, "warn", d.Policy)
}

// TestPreCheckWalletBalance_NoWalletRowSkips：无钱包行 = 该层不限 ⇒ 放行 skipped/warn。
func TestPreCheckWalletBalance_NoWalletRowSkips(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}
	pool := setupTestDB(t)
	svc := &Service{pool: pool}
	orgID, _ := setupTestOrgUser(t, pool, 0, 0, "", 0, 0, "")

	d, err := svc.PreCheckWalletBalance(context.Background(), PrecheckScopeOrg, orgID, 100)
	require.NoError(t, err)
	assert.True(t, d.Allow, "no wallet row = no org-level limit ⇒ allow")
}

// TestPreCheckOrgAndUserForOwner_OrgBlocksFirst：两级都 block 时返回第一级（org）。
func TestPreCheckOrgAndUserForOwner_OrgBlocksFirst(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}
	pool := setupTestDB(t)
	svc := &Service{pool: pool}
	suffix := time.Now().UnixNano()

	var tenantID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO tenants (code, name) VALUES ($1, $1)
		ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name
		RETURNING code
	`, fmt.Sprintf("tiered-both-%d", suffix)).Scan(&tenantID)
	require.NoError(t, err)
	var orgID, userID int
	err = pool.QueryRow(context.Background(), `
		INSERT INTO organizations (tenant_id, code, name) VALUES ($1, $2, $2) RETURNING id
	`, tenantID, fmt.Sprintf("org-both-%d", suffix)).Scan(&orgID)
	require.NoError(t, err)
	err = pool.QueryRow(context.Background(), `
		INSERT INTO users (tenant_id, username, password_hash, org_id)
		VALUES ($1, $2, 'x', $3) RETURNING id
	`, tenantID, fmt.Sprintf("u-both-%d", suffix), orgID).Scan(&userID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `
		INSERT INTO org_credit_wallets (org_id, tenant_id, balance_credits, credit_limit_credits, credit_policy)
		VALUES ($1, $2, -5000, 100, 'warn')`, orgID, tenantID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `
		INSERT INTO user_credit_wallets (user_id, tenant_id, balance_credits, credit_limit_credits, credit_policy)
		VALUES ($1, $2, -500, 100, 'block')`, userID, tenantID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_credit_wallets WHERE user_id=$1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM org_credit_wallets WHERE org_id=$1`, orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE code=$1`, tenantID)
	})

	blocked, err := svc.PreCheckOrgAndUserForOwner(context.Background(), fmt.Sprintf("u-both-%d", suffix), 100)
	require.NoError(t, err)
	require.NotNil(t, blocked, "org over-limit must block")
	assert.False(t, blocked.Allow)
	// 第一级（org）命中：org 余额 -5000 / 限 100 ⇒ over credit limit 语义。
	assert.Contains(t, blocked.Reason, "over credit limit")
}

// TestPreCheckOrgAndUserForOwner_UserWithoutOrgSkips：owner 无组织 ⇒ 两层跳过。
func TestPreCheckOrgAndUserForOwner_UserWithoutOrgSkips(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}
	pool := setupTestDB(t)
	svc := &Service{pool: pool}
	suffix := time.Now().UnixNano()
	var tenantID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO tenants (code, name) VALUES ($1, $1)
		ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name RETURNING code
	`, fmt.Sprintf("tiered-noorg-%d", suffix)).Scan(&tenantID)
	require.NoError(t, err)
	var userID int
	err = pool.QueryRow(context.Background(), `
		INSERT INTO users (tenant_id, username, password_hash) VALUES ($1, $2, 'x') RETURNING id
	`, tenantID, fmt.Sprintf("u-noorg-%d", suffix)).Scan(&userID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE code=$1`, tenantID)
	})

	blocked, err := svc.PreCheckOrgAndUserForOwner(context.Background(), fmt.Sprintf("u-noorg-%d", suffix), 100)
	require.NoError(t, err)
	assert.Nil(t, blocked, "user without org and without user wallet ⇒ no block")
}

// TestPreCheckOrgAndUserForOwner_UnknownOwnerSkips：owner 不在 users 表 ⇒ fail-open。
func TestPreCheckOrgAndUserForOwner_UnknownOwnerSkips(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database test in short mode")
	}
	pool := setupTestDB(t)
	svc := &Service{pool: pool}
	blocked, err := svc.PreCheckOrgAndUserForOwner(context.Background(), "nonexistent-owner-xyz", 100)
	require.NoError(t, err)
	assert.Nil(t, blocked)
}

// ─────────────────────────────────────────────────────────────
// 调用点判据（不依赖数据库）：四面入口必须真实调用 tiered gate。
// team_budget_gate 教训：写了判定体但零调用点 ⇒ 门静默失效。
// ─────────────────────────────────────────────────────────────

// TestTieredBalanceGateHasCallSites：四个数据面入口文件必须各自调用
// PreCheckOrgAndUserForOwner（chat/messages/responses 经 maasSvc，
// audio 经 tieredBalanceGate）。调用点缺失 ⇒ 本判据当场红。
func TestTieredBalanceGateHasCallSites(t *testing.T) {
	expectations := []struct {
		file    string
		mustHit []string
	}{
		{"handler.go", []string{"PreCheckOrgAndUserForOwner"}},              // chat 面
		{"messages.go", []string{"PreCheckOrgAndUserForOwner"}},             // /v1/messages
		{"responses.go", []string{"PreCheckOrgAndUserForOwner"}},            // /v1/responses
		{"audio_service.go", []string{"tieredBalanceGate", "PreCheckOrgAndUserForOwner"}}, // 音频面
	}
	for _, e := range expectations {
		src, err := os.ReadFile("../domains/streaming/" + e.file)
		require.NoError(t, err, "read streaming surface source")
		for _, hit := range e.mustHit {
			assert.True(t, strings.Contains(string(src), hit),
				"%s must reference %s (tiered gate call site missing ⇒ gate silently dead)",
				e.file, hit)
		}
	}
}

// TestAudioSetMaasWiresTieredGate：SetMaas 对满足 audioTieredBalanceGate 的
// concrete Service 必须接入 tieredBalanceGate；对不满足的桩必须保持 nil。
// （实现在 domains/streaming/tiered_gate_wiring_test.go —— AudioService 在那边。）
