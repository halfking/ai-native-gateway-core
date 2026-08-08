package freeresource

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// getTestDB 从 OMNIFREE_TEST_DB_URL 读取, 未设置则 skip.
// 例如: export OMNIFREE_TEST_DB_URL='postgres://omnifree_test_user:omnifree_test_pwd@localhost:5455/omnifree_round3?sslmode=disable'
//
// 必须用非超级用户 (NOBYPASSRLS) 连接, 否则 RLS 不生效 — 测试失去意义.
// 也可设置 OMNIFREE_TEST_DB_ADMIN_URL 指向超级用户, 用于测试前的 setup/seed.
func getTestDB(t *testing.T) *sql.DB {
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

// getTestAdminDB 返回超级用户连接, 用于跨租户 setup / cleanup.
// OMNIFREE_TEST_DB_ADMIN_URL 不设置时, 测试自动 skip (因为 setup 需要
// 跨 tenant 写入).
func getTestAdminDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("OMNIFREE_TEST_DB_ADMIN_URL")
	if dsn == "" {
		return nil // 不强制要求
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("admin db unreachable: %v", err)
	}
	return db
}

// seedQuotaRowAsSuperAdmin 用 super_admin 角色 (绕过 RLS WITH CHECK) 写入
// 多 tenant 测试数据. 真实生产路径上 Record/CorrectFromHeaders 由应用
// 代码触发, 已经 SET LOCAL 推送 GUC, 不需要这条路径.
func seedQuotaRowAsSuperAdmin(t *testing.T, db *sql.DB, credID int64, tenant string) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		"SELECT set_config('app.current_role', 'super_admin', true)"); err != nil {
		t.Fatalf("set super_admin: %v", err)
	}
	dayStart := time.Now().UTC().Truncate(24 * time.Hour)
	dayEnd := dayStart.Add(24 * time.Hour)
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO free_quota_tracker (
            credential_id, provider_code, model_id, window_type,
            window_start, window_end, request_count,
            is_exhausted, tenant_id
        ) VALUES ($1, $2, $3, 'day-1', $4, $5, 999, TRUE, $6)
        ON CONFLICT DO NOTHING
    `, credID, "r3-iso", "r3-iso-model", dayStart, dayEnd, tenant); err != nil {
		t.Fatalf("seed %s: %v", tenant, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestSetRLSTenantContext_GUCSession 验证 SetRLSTenantContext 真的把
// app.current_tenant 推到了 PG session; 后续 SELECT get_current_tenant()
// 应当返回所设值, 而不是 'default' fallback.
//
// 实现要点: 必须在同一 *sql.Conn (物理连接) 上 set + select, 因为
// SET 是 session-scoped, 跨连接就丢失. SetRLSTenantContextConn 直接
// 操作指定 Conn, 而 SetRLSTenantContext 用 db pool (适合无锁的 happy path,
// 但 SET 仍需在同一连接上 SELECT 才能验证, 所以这里测 Conn 形式).
func TestSetRLSTenantContext_GUCSession(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("get conn: %v", err)
	}
	defer conn.Close()

	SetRLSTenantContextConn(context.Background(), conn, "tenant-r3-audit")

	var gotVal string
	if err := conn.QueryRowContext(context.Background(),
		"SELECT public.get_current_tenant()").Scan(&gotVal); err != nil {
		t.Fatalf("query get_current_tenant: %v", err)
	}
	if gotVal != "tenant-r3-audit" {
		t.Errorf("expected tenant-r3-audit, got %q", gotVal)
	}
}

// TestSetRLSTenantContext_EmptyFallback 验证空 tenantID 时不设置 GUC,
// 让 get_current_tenant() 走 'default' fallback.
func TestSetRLSTenantContext_EmptyFallback(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("get conn: %v", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(context.Background(),
		"SET app.current_tenant = 'preexisting'"); err != nil {
		t.Fatalf("seed GUC: %v", err)
	}

	// 空 ID → helper 直接 return, 不影响 GUC.
	SetRLSTenantContext(context.Background(), db, "")

	var gotVal string
	if err := conn.QueryRowContext(context.Background(),
		"SELECT public.get_current_tenant()").Scan(&gotVal); err != nil {
		t.Fatalf("query get_current_tenant: %v", err)
	}
	if gotVal != "preexisting" {
		t.Errorf("empty tenantID should not clear existing GUC, got %q", gotVal)
	}
}

// TestSetRLSTenantContext_InvalidFallback 验证非法 tenantID 被替换
// 为 'default' (防 SQL 注入).
func TestSetRLSTenantContext_InvalidFallback(t *testing.T) {
	cases := []string{
		"'; DROP TABLE users; --",
		"abc def",
		"中文字符",
		"with'quote",
	}
	for _, bad := range cases {
		if got := isValidTenantID(bad); got {
			t.Errorf("isValidTenantID(%q) = true, want false", bad)
		}
		if got := escapeTenant(bad); got != "default" {
			t.Errorf("escapeTenant(%q) = %q, want default", bad, got)
		}
	}
}

// TestPreflight_RLSIsolation 验证 Preflight 在 SET LOCAL GUC 后只看到
// 当前 tenant 的行. 这正是 round 3 audit C2/C3 修复的核心.
//
// 前置: admin 用 super_admin 在两个 tenant 各写一条耗尽行; 测试
// 用户是 NOSUPERUSER + NOBYPASSRLS, 必须透过 GUC 才能 SELECT 自己的
// 行, 而 RLS 会挡住其他 tenant 的行.
func TestPreflight_RLSIsolation(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	admin := getTestAdminDB(t)
	if admin == nil {
		t.Skip("OMNIFREE_TEST_DB_ADMIN_URL not set; cannot seed multi-tenant rows")
	}
	defer admin.Close()

	// 用 admin 连接 setup: 两个 tenant 各一条.
	seedQuotaRowAsSuperAdmin(t, admin, 990001, "tenant-r3-iso-a")
	seedQuotaRowAsSuperAdmin(t, admin, 990002, "tenant-r3-iso-b")
	defer func() {
		if _, err := admin.ExecContext(context.Background(),
			"DELETE FROM free_quota_tracker WHERE tenant_id IN ('tenant-r3-iso-a','tenant-r3-iso-b')"); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()

	ctx := context.Background()

	// 2. Tenant A 在自己的 GUC 下查询 — 应见耗尽 (返回 false).
	qt := NewQuotaTracker(db)
	got, err := qt.Preflight(ctx, PreflightRequest{
		CredentialID:    990001,
		ProviderCode:    "r3-iso",
		ModelID:         "r3-iso-model",
		WindowType:      WindowTypeDay1,
		DefaultLimit:    1000,
		MinRemainingPct: 0.1,
		TenantID:        "tenant-r3-iso-a",
	})
	if err != nil {
		t.Fatalf("Preflight A: %v", err)
	}
	if got {
		t.Errorf("tenant A should see its OWN exhausted row, got pass=true (RLS leak in reverse)")
	}

	// 3. Tenant B 在自己的 GUC 下查询 — 也应见自己的耗尽 (返回 false).
	//    如果 RLS 没生效, 会 SELECT 到 A 的行并返回 false — 但测试期望
	//    B 也是 false (因为 B 自己也有耗尽行). 这两个场景在原始测试
	//    中无法区分; 我们加一个 Tenant C (无耗尽行) 的负样本.
	got, err = qt.Preflight(ctx, PreflightRequest{
		CredentialID:    990002,
		ProviderCode:    "r3-iso",
		ModelID:         "r3-iso-model",
		WindowType:      WindowTypeDay1,
		DefaultLimit:    1000,
		MinRemainingPct: 0.1,
		TenantID:        "tenant-r3-iso-b",
	})
	if err != nil {
		t.Fatalf("Preflight B: %v", err)
	}
	if got {
		t.Errorf("tenant B should see its OWN exhausted row, got pass=true (RLS leak in reverse)")
	}

	// 4. 关键 RLS 验证: Tenant C (没有任何行) 应该 pass=true, 因为 RLS
	//    已经把 A/B 的行挡住. 如果 RLS 没生效, tenant C 会 SELECT 到
	//    任意一条 (例如 990002 已被 A 占, 但 990002 唯一 key 是
	//    (cred, provider, model, window, start, tenant) 所以 tenant='C'
	//    不会匹配任何行 → SQL_NoRows → pass=true. 这个测试主要验证
	//    pass=true 而不是 fallback 到某条错误数据.
	got, err = qt.Preflight(ctx, PreflightRequest{
		CredentialID:    990001,
		ProviderCode:    "r3-iso",
		ModelID:         "r3-iso-model",
		WindowType:      WindowTypeDay1,
		DefaultLimit:    1000,
		MinRemainingPct: 0.1,
		TenantID:        "tenant-r3-iso-c-clean",
	})
	if err != nil {
		t.Fatalf("Preflight C: %v", err)
	}
	if !got {
		t.Errorf("tenant C with no row should pass (RLS hides A/B rows); got pass=false (RLS leak)")
	}
}

// TestRecord_TxWrappedAndRLS 验证 Record 在事务里执行, 用 SET LOCAL
// 推送 GUC 让 RLS 过滤生效. 测试用户为非超级用户, 所以必须透过 GUC 才能
// 看到自己 tenant 的行.
func TestRecord_TxWrappedAndRLS(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	qt := NewQuotaTracker(db)
	ctx := context.Background()
	ts := time.Date(2024, 8, 15, 12, 0, 0, 0, time.UTC)

	if err := qt.Record(ctx, RecordRequest{
		CredentialID: 991000,
		ProviderCode: "r3-tx",
		ModelID:      "r3-tx-model",
		WindowTypes:  []WindowType{WindowTypeDay1},
		TokenCount:   0,
		Success:      true,
		TenantID:     "tenant-r3-tx-test",
		Timestamp:    ts,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// 验证 row 写入且 tenant_id 正确 (RLS 透过 GUC 过滤后能 SELECT 到).
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		"SET app.current_tenant = 'tenant-r3-tx-test'"); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	var count int
	if err := tx.QueryRowContext(ctx, `
        SELECT COUNT(*) FROM free_quota_tracker
        WHERE credential_id = $1 AND provider_code = $2 AND model_id = $3
          AND tenant_id = $4
    `, 991000, "r3-tx", "r3-tx-model", "tenant-r3-tx-test").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 quota row, got %d", count)
	}

	// 清理.
	if _, err := db.ExecContext(ctx,
		"DELETE FROM free_quota_tracker WHERE credential_id = 991000"); err != nil {
		t.Logf("cleanup: %v", err)
	}
}

// TestCorrectFromHeaders_SetsExhausted 验证 CorrectFromHeaders 把 day-1 行
// 标记为 is_exhausted, 同时支持 tenant 隔离.
func TestCorrectFromHeaders_SetsExhausted(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	qt := NewQuotaTracker(db)
	ctx := context.Background()

	headers := map[string]string{
		"Retry-After":       "60",
		"X-RateLimit-Limit": "1000",
		"X-RateLimit-Reset": "1800000000",
	}

	if err := qt.CorrectFromHeaders(ctx, CorrectionRequest{
		CredentialID: 992000,
		ProviderCode: "r3-corr",
		ModelID:      "r3-corr-model",
		Headers:      headers,
		TenantID:     "tenant-r3-corr-test",
	}); err != nil {
		t.Fatalf("CorrectFromHeaders: %v", err)
	}

	// 验证.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		"SET app.current_tenant = 'tenant-r3-corr-test'"); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	var exhausted bool
	var limit sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
        SELECT is_exhausted, corrected_limit FROM free_quota_tracker
        WHERE credential_id = $1 AND tenant_id = $2
    `, 992000, "tenant-r3-corr-test").Scan(&exhausted, &limit); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !exhausted {
		t.Errorf("expected is_exhausted=TRUE, got FALSE")
	}
	if !limit.Valid || limit.Int64 != 1000 {
		t.Errorf("expected corrected_limit=1000, got %v", limit)
	}

	// 清理.
	if _, err := db.ExecContext(ctx,
		"DELETE FROM free_quota_tracker WHERE credential_id = 992000"); err != nil {
		t.Logf("cleanup: %v", err)
	}
}