// approval_guc_integration_test.go — 真库 GUC 回归（252 PG SQL 审计轮 F1）。
//
// 背景：setSuperAdminGUC 曾写 `SET LOCAL app.current_role=...`，current_role
// 是 PG 保留字 → 生产环境每次调用都 syntax error，且 pgxmock 单测只能断言
// SQL 字符串、测不出 PG 语法语义——252 生产日志实证该路径 R41 起全部失败。
// 本测试必须连真 PG 才有意义：无 TEST_DATABASE_URL / TEST_DB_URL 时跳过。
package sessionaudit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func realDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库 GUC 回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestSuperAdminGUC_BeginTenantTx_RealDB 验证 super_admin（空租户）路径：
// 事务内 GUC 必须成功设置且 current_setting 读回 'super_admin'。
// 回归点：若 GUC 语句被改回 SET LOCAL app.current_role=...，本测试在真 PG
// 上直接 syntax error（pgxmock 测不出）。
func TestSuperAdminGUC_BeginTenantTx_RealDB(t *testing.T) {
	pool := realDBPool(t)
	ctx := context.Background()

	tx, err := beginTenantTx(ctx, pool, "", true)
	if err != nil {
		t.Fatalf("beginTenantTx(super_admin): %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var role string
	if err := tx.QueryRow(ctx, "SELECT current_setting('app.current_role')").Scan(&role); err != nil {
		t.Fatalf("read back current_role: %v", err)
	}
	if role != "super_admin" {
		t.Fatalf("app.current_role = %q, want super_admin", role)
	}
}

// TestTenantGUC_BeginTenantTx_RealDB 验证租户路径：set_config 参数绑定设置
// app.current_tenant，读回等于调用方租户（含带单引号的恶意输入，验证不再
// 依赖手工转义）。
func TestTenantGUC_BeginTenantTx_RealDB(t *testing.T) {
	pool := realDBPool(t)
	ctx := context.Background()

	const tenant = "tenant-a'"
	tx, err := beginTenantTx(ctx, pool, tenant, true)
	if err != nil {
		t.Fatalf("beginTenantTx(tenant): %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var got string
	if err := tx.QueryRow(ctx, "SELECT current_setting('app.current_tenant')").Scan(&got); err != nil {
		t.Fatalf("read back current_tenant: %v", err)
	}
	if got != tenant {
		t.Fatalf("app.current_tenant = %q, want %q（单引号须原样保留）", got, tenant)
	}
}
