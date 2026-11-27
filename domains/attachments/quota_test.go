package attachments

import (
	"os"
	"strings"
	"testing"
)

// TestQuotaExceededError_ErrorMessage tests error message formatting.
func TestQuotaExceededError_ErrorMessage(t *testing.T) {
	tests := []struct {
		name     string
		err      *QuotaExceededError
		contains []string
	}{
		{
			name: "storage quota",
			err: &QuotaExceededError{
				Type:      "storage",
				TenantID:  "tenant-123",
				Current:   900 * 1024,
				Limit:     1024 * 1024,
				Requested: 200 * 1024,
			},
			contains: []string{"storage", "tenant=tenant-123", "921600", "1048576", "204800"},
		},
		{
			name: "daily files quota",
			err: &QuotaExceededError{
				Type:     "daily_files",
				TenantID: "tenant-456",
				Current:  10,
				Limit:    10,
			},
			contains: []string{"daily file", "tenant=tenant-456", "10 files"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tt.err.Error()
			for _, substr := range tt.contains {
				if !contains(msg, substr) {
					t.Errorf("error message should contain %q, got: %s", substr, msg)
				}
			}
		})
	}
}

// TestIsQuotaExceeded tests quota error type detection.
func TestIsQuotaExceeded(t *testing.T) {
	quotaErr := &QuotaExceededError{
		Type:     "storage",
		TenantID: "test",
	}

	if !IsQuotaExceeded(quotaErr) {
		t.Error("IsQuotaExceeded should return true for QuotaExceededError")
	}

	if IsQuotaExceeded(nil) {
		t.Error("IsQuotaExceeded should return false for nil")
	}

	otherErr := &testError{}
	if IsQuotaExceeded(otherErr) {
		t.Error("IsQuotaExceeded should return false for other errors")
	}
}

// TestQuotaManager_NilDB tests backward compatibility when DB is nil.
func TestQuotaManager_NilDB(t *testing.T) {
	config := QuotaConfig{
		DefaultMaxBytes:       1024 * 1024,
		DefaultMaxFilesPerDay: 10,
	}

	qm := NewQuotaManager(nil, config) // nil DB

	// Should succeed without checking (backward compatible)
	err := qm.CheckAndReserve(nil, "tenant-a", 100*1024*1024)
	if err != nil {
		t.Errorf("nil DB should skip quota checks, got: %v", err)
	}

	err = qm.CommitUsage(nil, "tenant-a", 100*1024)
	if err != nil {
		t.Errorf("nil DB should skip quota commits, got: %v", err)
	}
}

// TestStorage_QuotaIntegrationArchitecture documents the integration architecture.
//
// Complete quota enforcement requires:
// 1. Storage.SetQuotaManager() / GetQuotaManager() methods (future work)
// 2. SaveBase64ImageWithTenant() method that accepts tenantID (future work)  
// 3. Extraction pipeline modified to pass tenantID through (future work)
// 4. Database migration for tenant_attachment_quotas table (deployment)
func TestStorage_QuotaIntegrationArchitecture(t *testing.T) {
	t.Skip("Architecture documentation - storage integration pending")
	
	// === Integration Architecture ===
	//
	// Current state:
	// - QuotaManager fully implemented (quota.go)
	// - Storage struct has quotaManager fields (storage.go)
	// - Helper methods (SetQuotaManager, GetQuotaManager) need to be added
	// - SaveBase64ImageWithTenant() wrapper needs to be implemented
	//
	// Integration steps:
	// 1. Add Storage.SetQuotaManager(qm *QuotaManager)
	// 2. Add Storage.GetQuotaManager() *QuotaManager  
	// 3. Add SaveBase64ImageWithTenant(requestID, dataURI, msgIdx, blockIdx, tenantID)
	// 4. Modify SaveBase64Image to call SaveBase64ImageWithTenant("") for backward compat
	// 5. Update extraction pipeline to pass tenantID from request context
}

// TestQuotaManager_PerTenantOverrides_Configuration tests override logic.
func TestQuotaManager_PerTenantOverrides_Configuration(t *testing.T) {
	config := QuotaConfig{
		DefaultMaxBytes:       1024 * 1024, // 1MB default
		DefaultMaxFilesPerDay: 10,
		PerTenantOverrides: map[string]TenantQuotaLimits{
			"premium": {
				MaxBytes:       100 * 1024 * 1024, // 100MB
				MaxFilesPerDay: 1000,
			},
		},
	}

	qm := NewQuotaManager(nil, config)

	// Verify override configuration is accessible
	limits := qm.getLimitsForTenant("premium")
	if limits.MaxBytes != 100*1024*1024 {
		t.Errorf("premium tenant should have 100MB limit, got: %d", limits.MaxBytes)
	}
	if limits.MaxFilesPerDay != 1000 {
		t.Errorf("premium tenant should have 1000 files/day limit, got: %d", limits.MaxFilesPerDay)
	}

	// Verify default is used for non-override tenants
	limits = qm.getLimitsForTenant("regular")
	if limits.MaxBytes != 1024*1024 {
		t.Errorf("regular tenant should have default 1MB limit, got: %d", limits.MaxBytes)
	}
	if limits.MaxFilesPerDay != 10 {
		t.Errorf("regular tenant should have default 10 files/day limit, got: %d", limits.MaxFilesPerDay)
	}
}

// TestQuotaManager_IntegrationGuide provides integration test guidance.
//
// Complete quota enforcement testing requires a real PostgreSQL database
// with the tenant_attachment_quotas table. This test documents the approach.
func TestQuotaManager_IntegrationGuide(t *testing.T) {
	t.Skip("Integration test guide - run manually with real database")

	// === Integration Test Steps ===
	//
	// 1. Database Setup:
	//    CREATE TABLE tenant_attachment_quotas (
	//        tenant_id TEXT PRIMARY KEY,
	//        current_bytes BIGINT DEFAULT 0,
	//        files_today INTEGER DEFAULT 0,
	//        last_reset_date TEXT,
	//        updated_at TIMESTAMPTZ DEFAULT NOW()
	//    );
	//
	// 2. Test Scenarios:
	//
	//    a) Storage quota enforcement:
	//       - Insert: tenant-a with current_bytes=900KB, limit=1MB
	//       - Attempt to save 200KB attachment
	//       - Expected: QuotaExceededError with Type="storage"
	//
	//    b) Daily file count enforcement:
	//       - Insert: tenant-b with files_today=10, limit=10
	//       - Attempt to save new attachment
	//       - Expected: QuotaExceededError with Type="daily_files"
	//
	//    c) Daily reset:
	//       - Insert: tenant-c with files_today=10, last_reset_date=yesterday
	//       - Attempt to save new attachment
	//       - Expected: Success (counter reset)
	//
	//    d) Quota commit:
	//       - Start with tenant-d at 0 bytes
	//       - Save 100KB attachment
	//       - Verify: current_bytes=100KB, files_today=1
	//
	//    e) Deduplication:
	//       - Save same attachment twice
	//       - Verify: quota only updated once (dedup doesn't count)
	//
	// 3. Performance verification:
	//    - Query with EXPLAIN ANALYZE
	//    - Verify ON CONFLICT DO UPDATE handles concurrency
	//    - Test under concurrent load (multiple goroutines)
}

// ─── Helper types and functions ───────────────────────────────────────

type testError struct{}

func (e *testError) Error() string { return "test error" }

func contains(s, substr string) bool {
	if len(s) == 0 || len(substr) == 0 {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestQuotaManager_AtomicReservationSQLShape 钉住原子预留 SQL 形态
// （2026-11-27 12h 审计轮 P2-F）。
//
// 为什么是读源钉测而非 mock：配额防超卖的关键是「检查＋预留必须在同一条
// 条件 UPSERT 里」，任何把 WHERE 拆掉、或把 reserved 累加搬出 UPDATE 的
// 改动都会静默复活 check-then-act 竞态。钉四点：
//  1. CheckAndReserve 的 INSERT 带 ON CONFLICT + WHERE（条件失败=拒绝）
//  2. WHERE 同时约束容量（current+reserved+new<=max）与日文件数
//  3. CommitUsage 转销预留（GREATEST 防负）
//  4. ReleaseReservation 存在（失败路径释放）
func TestQuotaManager_AtomicReservationSQLShape(t *testing.T) {
	src, err := os.ReadFile("quota.go")
	if err != nil {
		t.Fatalf("read quota.go: %v", err)
	}
	s := string(src)

	// 1) 预留是条件 UPSERT：ON CONFLICT ... DO UPDATE ... WHERE 缺一不可
	if !strings.Contains(s, "ON CONFLICT (tenant_id) DO UPDATE SET") {
		t.Error("CheckAndReserve 必须用 ON CONFLICT (tenant_id) DO UPDATE 原子预留")
	}
	if !strings.Contains(s, "reserved_bytes = q.reserved_bytes + $3") {
		t.Error("预留必须在 UPDATE 分支原子累加 reserved_bytes（写回数据库，不能只在内存判断）")
	}

	// 2) WHERE 同时约束容量与日文件数（拆掉任一即超卖/超条）
	if !strings.Contains(s, "q.current_bytes + q.reserved_bytes + $3 <= $4") {
		t.Error("预留 WHERE 必须含容量约束 current+reserved+new <= max")
	}
	if !strings.Contains(s, "reserved_files_today + 1") {
		t.Error("预留 WHERE 必须含日文件数约束（reserved_files_today 参与）")
	}

	// 3) CommitUsage 转销预留（GREATEST 防重复 Commit 打穿负数）
	if !strings.Contains(s, "GREATEST(tenant_attachment_quotas.reserved_bytes - $2, 0)") {
		t.Error("CommitUsage 必须转销 reserved_bytes（GREATEST 防负数）")
	}

	// 4) 失败路径有释放
	if !strings.Contains(s, "func (qm *QuotaManager) ReleaseReservation") {
		t.Error("必须存在 ReleaseReservation（保存失败/去重命中时释放预留）")
	}
}

// TestQuotaManager_ReservedColumnsInMigration 钉住 633 迁移带 reserved 两列。
func TestQuotaManager_ReservedColumnsInMigration(t *testing.T) {
	src, err := os.ReadFile("../../sql/migrations/startup/633_tenant_attachment_quotas.sql")
	if err != nil {
		t.Fatalf("read 633 migration: %v", err)
	}
	s := string(src)
	for _, col := range []string{"reserved_bytes", "reserved_files_today"} {
		if !strings.Contains(s, "ADD COLUMN IF NOT EXISTS "+col) {
			t.Errorf("633 迁移必须幂等补列 %s（存量库兼容）", col)
		}
	}
}
