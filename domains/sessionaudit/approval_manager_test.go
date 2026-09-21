package sessionaudit

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

// TestNewApprovalManager_Defaults timeout <= 0 → 15m。
func TestNewApprovalManager_Defaults(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	m := NewApprovalManager(mock, 0)
	if m.timeout != 15*time.Minute {
		t.Errorf("default timeout = %v, want 15m", m.timeout)
	}
}

func TestNewApprovalManager_NonZeroTimeout(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	m := NewApprovalManager(mock, 30*time.Second)
	if m.timeout != 30*time.Second {
		t.Errorf("timeout = %v, want 30s", m.timeout)
	}
}

// TestCreate_Success 验证 INSERT 路径并检查参数化绑定 + SET LOCAL tenant。
func TestCreate_Success(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectExec(`SELECT set_config\('app\.current_tenant', \$1, true\)`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	mock.ExpectExec(`INSERT INTO approval_queue`).
		WithArgs(
			pgxmock.AnyArg(), // id (uuid)
			"sess-1",
			"tenant-1",
			"req-1",
			pgxmock.AnyArg(), // detect_result jsonb
			pgxmock.AnyArg(), // snapshot jsonb
			ApprovalPending,
			pgxmock.AnyArg(), // created_at
			pgxmock.AnyArg(), // expires_at
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	m := NewApprovalManager(mock, 15*time.Minute)
	id, err := m.Create(context.Background(), &ApprovalRequest{
		SessionID: "sess-1",
		TenantID:  "tenant-1",
		RequestID: "req-1",
		DetectResult: &DetectResult{
			Score:    9,
			Decision: DecisionNeedApproval,
		},
		Snapshot: &RequestSnapshot{
			SessionID: "sess-1",
			TenantID:  "tenant-1",
			RequestID: "req-1",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty approval id")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet mock expectations: %v", err)
	}
}

// TestCreate_RejectsEmptyTenantID 应用层空 tenant_id 校验,不应触达 DB。
func TestCreate_RejectsEmptyTenantID(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	m := NewApprovalManager(mock, 15*time.Minute)
	if _, err := m.Create(context.Background(), &ApprovalRequest{
		SessionID: "s",
		RequestID: "r",
	}); err == nil {
		t.Error("expected error for empty tenant_id")
	}
}

// TestCreate_RejectsNilRequest nil request 必须报错。
func TestCreate_RejectsNilRequest(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	m := NewApprovalManager(mock, 15*time.Minute)
	if _, err := m.Create(context.Background(), nil); err == nil {
		t.Error("expected error for nil request")
	}
}

// TestGet_NotFound 没找到返回 ErrNotFound。
// 走 GetForTenant("", super_admin) → SET LOCAL role,再 SELECT。
func TestGet_NotFound(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{AccessMode: pgx.ReadOnly})
	mock.ExpectExec(`SELECT set_config\('app\.current_role', 'super_admin', true\)`).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	mock.ExpectQuery(`SELECT id, session_id, tenant_id`).
		WithArgs("missing").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	m := NewApprovalManager(mock, 15*time.Minute)
	_, err := m.GetForTenant(context.Background(), "missing", "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestGetForTenant_TenantMismatch 应用层横向越权防护。
// 即使事务内 SET LOCAL 设了一个 tenant,但 SELECT 出来的行 tenant 不同
// → 应用层返回 ErrTenantMismatch。
func TestGetForTenant_TenantMismatch(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{AccessMode: pgx.ReadOnly})
	mock.ExpectExec(`SELECT set_config\('app\.current_tenant', \$1, true\)`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	rows := pgxmock.NewRows([]string{
		"id", "session_id", "tenant_id", "request_id",
		"detect_result", "snapshot",
		"status", "approved_by", "approved_at", "reason",
		"created_at", "expires_at",
	}).AddRow(
		"appr-1", "sess-1", "OTHER-TENANT", "req-1",
		[]byte(`{"score":9}`), []byte(`{"session_id":"sess-1"}`),
		"pending", nil, nil, nil,
		time.Now(), time.Now().Add(time.Hour),
	)
	mock.ExpectQuery(`SELECT id, session_id, tenant_id`).
		WithArgs("appr-1").
		WillReturnRows(rows)
	mock.ExpectRollback()

	m := NewApprovalManager(mock, 15*time.Minute)
	_, err := m.GetForTenant(context.Background(), "appr-1", "MY-TENANT")
	if !errors.Is(err, ErrTenantMismatch) {
		t.Errorf("expected ErrTenantMismatch, got %v", err)
	}
}

// TestGetForTenant_OK 正常路径：tenant 一致,解析 JSON,返回记录。
func TestGetForTenant_OK(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{AccessMode: pgx.ReadOnly})
	mock.ExpectExec(`SELECT set_config\('app\.current_tenant', \$1, true\)`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	rows := pgxmock.NewRows([]string{
		"id", "session_id", "tenant_id", "request_id",
		"detect_result", "snapshot",
		"status", "approved_by", "approved_at", "reason",
		"created_at", "expires_at",
	}).AddRow(
		"appr-1", "sess-1", "tenant-A", "req-1",
		[]byte(`{"score":9,"decision":"need_approval"}`),
		[]byte(`{"session_id":"sess-1","tenant_id":"tenant-A","request_id":"req-1","created_at":"2026-06-27T00:00:00Z"}`),
		"pending", nil, nil, nil,
		time.Now(), time.Now().Add(time.Hour),
	)
	mock.ExpectQuery(`SELECT id, session_id, tenant_id`).
		WithArgs("appr-1").
		WillReturnRows(rows)
	mock.ExpectCommit()

	m := NewApprovalManager(mock, 15*time.Minute)
	rec, err := m.GetForTenant(context.Background(), "appr-1", "tenant-A")
	if err != nil {
		t.Fatalf("GetForTenant: %v", err)
	}
	if rec.ID != "appr-1" {
		t.Errorf("ID=%s", rec.ID)
	}
	if rec.TenantID != "tenant-A" {
		t.Errorf("TenantID=%s", rec.TenantID)
	}
	if rec.DetectResult == nil || rec.DetectResult.Score != 9 {
		t.Errorf("DetectResult parse failed: %+v", rec.DetectResult)
	}
}

// TestGetForTenant_RequiresTenantID 应用层不允许空 tenant 进入 GetForTenant。
// 但本测试已被取消——super_admin 现在传空 tenant 是合法调用。
// 改为校验不带过滤的 fallback Get() 已废弃。
func TestGetForTenant_SuperAdminBypass(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{AccessMode: pgx.ReadOnly})
	mock.ExpectExec(`SELECT set_config\('app\.current_role', 'super_admin', true\)`).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	rows := pgxmock.NewRows([]string{
		"id", "session_id", "tenant_id", "request_id",
		"detect_result", "snapshot",
		"status", "approved_by", "approved_at", "reason",
		"created_at", "expires_at",
	}).AddRow(
		"appr-1", "sess-1", "any-tenant", "req-1",
		[]byte(`{"score":9}`), []byte(`{"session_id":"sess-1"}`),
		"pending", nil, nil, nil,
		time.Now(), time.Now().Add(time.Hour),
	)
	mock.ExpectQuery(`SELECT id, session_id, tenant_id`).
		WithArgs("appr-1").
		WillReturnRows(rows)
	mock.ExpectCommit()

	m := NewApprovalManager(mock, 15*time.Minute)
	rec, err := m.GetForTenant(context.Background(), "appr-1", "") // super_admin
	if err != nil {
		t.Fatalf("GetForTenant: %v", err)
	}
	if rec.TenantID != "any-tenant" {
		t.Errorf("TenantID=%s", rec.TenantID)
	}
}

// TestList_DefaultsLimit 当 limit<=0 时回退到 50。
func TestList_DefaultsLimit(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{AccessMode: pgx.ReadOnly})
	mock.ExpectExec(`SELECT set_config\('app\.current_tenant', \$1, true\)`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	rows := pgxmock.NewRows([]string{
		"id", "session_id", "tenant_id", "request_id",
		"detect_result", "snapshot",
		"status", "approved_by", "approved_at", "reason",
		"created_at", "expires_at",
	})
	mock.ExpectQuery(`SELECT id, session_id, tenant_id`).
		WithArgs("tenant-A", ApprovalPending, 50, 0).
		WillReturnRows(rows)
	mock.ExpectCommit()

	m := NewApprovalManager(mock, 15*time.Minute)
	recs, err := m.List(context.Background(), &ApprovalFilter{
		TenantID: "tenant-A",
		Status:   ApprovalPending,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 records, got %d", len(recs))
	}
}

// TestList_ClampLimit 超 200 → 回退到 50。
func TestList_ClampLimit(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectBeginTx(pgx.TxOptions{AccessMode: pgx.ReadOnly})
	mock.ExpectExec(`SELECT set_config\('app\.current_tenant', \$1, true\)`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	rows := pgxmock.NewRows([]string{
		"id", "session_id", "tenant_id", "request_id",
		"detect_result", "snapshot",
		"status", "approved_by", "approved_at", "reason",
		"created_at", "expires_at",
	})
	mock.ExpectQuery(`SELECT id, session_id, tenant_id`).
		WithArgs("tenant-A", 50, 0).
		WillReturnRows(rows)
	mock.ExpectCommit()

	m := NewApprovalManager(mock, 15*time.Minute)
	_, err := m.List(context.Background(), &ApprovalFilter{
		TenantID: "tenant-A",
		Limit:    9999,
		Offset:   -5,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
}

// TestList_SuperAdminBypass super_admin 跨租户列表：空 TenantID → SET LOCAL role。
func TestList_SuperAdminBypass(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{AccessMode: pgx.ReadOnly})
	mock.ExpectExec(`SELECT set_config\('app\.current_role', 'super_admin', true\)`).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	rows := pgxmock.NewRows([]string{
		"id", "session_id", "tenant_id", "request_id",
		"detect_result", "snapshot",
		"status", "approved_by", "approved_at", "reason",
		"created_at", "expires_at",
	})
	// super_admin 不带 tenant → SQL 不带 AND tenant_id,args=[status, limit, offset]
	mock.ExpectQuery(`SELECT id, session_id, tenant_id`).
		WithArgs(ApprovalPending, 50, 0).
		WillReturnRows(rows)
	mock.ExpectCommit()

	m := NewApprovalManager(mock, 15*time.Minute)
	_, err := m.List(context.Background(), &ApprovalFilter{
		TenantID: "",
		Status:   ApprovalPending,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
}

// TestApprove_OK 完整事务：SELECT FOR UPDATE → 比对 tenant → UPDATE → Commit。
func TestApprove_OK(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectExec(`SELECT set_config\('app\.current_tenant', \$1, true\)`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	rows := pgxmock.NewRows([]string{"tenant_id", "status"}).
		AddRow("tenant-A", "pending")
	mock.ExpectQuery(`SELECT tenant_id, status FROM approval_queue WHERE id = \$1 FOR UPDATE`).
		WithArgs("appr-1").
		WillReturnRows(rows)
	mock.ExpectExec(`UPDATE approval_queue`).
		WithArgs(ApprovalApproved, "admin@kx", pgxmock.AnyArg(), "ok", "appr-1", ApprovalPending).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	m := NewApprovalManager(mock, 15*time.Minute)
	if err := m.Approve(context.Background(), "appr-1", "tenant-A", "admin@kx", "ok"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
}

// TestApprove_SuperAdminBypass super_admin 跨租户审批：空 callerTenantID → SET LOCAL role。
func TestApprove_SuperAdminBypass(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectExec(`SELECT set_config\('app\.current_role', 'super_admin', true\)`).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	rows := pgxmock.NewRows([]string{"tenant_id", "status"}).
		AddRow("any-tenant", "pending")
	mock.ExpectQuery(`SELECT tenant_id, status FROM approval_queue WHERE id = \$1 FOR UPDATE`).
		WithArgs("appr-1").
		WillReturnRows(rows)
	mock.ExpectExec(`UPDATE approval_queue`).
		WithArgs(ApprovalApproved, "super", pgxmock.AnyArg(), "ok", "appr-1", ApprovalPending).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	m := NewApprovalManager(mock, 15*time.Minute)
	if err := m.Approve(context.Background(), "appr-1", "", "super", "ok"); err != nil {
		t.Fatalf("Approve (super): %v", err)
	}
}

// TestApprove_TenantMismatch SELECT 出 tenant != caller → ErrTenantMismatch,
// 且 UPDATE 不应发生。
func TestApprove_TenantMismatch(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectExec(`SELECT set_config\('app\.current_tenant', \$1, true\)`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	rows := pgxmock.NewRows([]string{"tenant_id", "status"}).
		AddRow("OTHER", "pending")
	mock.ExpectQuery(`SELECT tenant_id, status FROM approval_queue WHERE id = \$1 FOR UPDATE`).
		WithArgs("appr-1").
		WillReturnRows(rows)
	mock.ExpectRollback()

	m := NewApprovalManager(mock, 15*time.Minute)
	err := m.Approve(context.Background(), "appr-1", "MY", "admin@kx", "ok")
	if !errors.Is(err, ErrTenantMismatch) {
		t.Errorf("expected ErrTenantMismatch, got %v", err)
	}
}

// TestApprove_AlreadyDecided 行已非 pending → ErrAlreadyDecided。
func TestApprove_AlreadyDecided(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectExec(`SELECT set_config\('app\.current_tenant', \$1, true\)`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	rows := pgxmock.NewRows([]string{"tenant_id", "status"}).
		AddRow("tenant-A", "approved") // 已经审批过
	mock.ExpectQuery(`SELECT tenant_id, status FROM approval_queue WHERE id = \$1 FOR UPDATE`).
		WithArgs("appr-1").
		WillReturnRows(rows)
	mock.ExpectRollback()

	m := NewApprovalManager(mock, 15*time.Minute)
	err := m.Approve(context.Background(), "appr-1", "tenant-A", "admin@kx", "ok")
	if !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("expected ErrAlreadyDecided, got %v", err)
	}
}

// TestReject_OK 与 Approve 同流程,只是 status target 不同。
func TestReject_OK(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectExec(`SELECT set_config\('app\.current_tenant', \$1, true\)`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	rows := pgxmock.NewRows([]string{"tenant_id", "status"}).
		AddRow("tenant-A", "pending")
	mock.ExpectQuery(`SELECT tenant_id, status FROM approval_queue WHERE id = \$1 FOR UPDATE`).
		WithArgs("appr-1").
		WillReturnRows(rows)
	mock.ExpectExec(`UPDATE approval_queue`).
		WithArgs(ApprovalRejected, "admin@kx", pgxmock.AnyArg(), "violates policy", "appr-1", ApprovalPending).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	m := NewApprovalManager(mock, 15*time.Minute)
	if err := m.Reject(context.Background(), "appr-1", "tenant-A", "admin@kx", "violates policy"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
}

// TestApprove_NotFound SELECT 无结果 → ErrNotFound。
func TestApprove_NotFound(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBeginTx(pgx.TxOptions{})
	mock.ExpectExec(`SELECT set_config\('app\.current_tenant', \$1, true\)`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	mock.ExpectQuery(`SELECT tenant_id, status FROM approval_queue WHERE id = \$1 FOR UPDATE`).
		WithArgs("missing").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	m := NewApprovalManager(mock, 15*time.Minute)
	err := m.Approve(context.Background(), "missing", "tenant-A", "admin@kx", "ok")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestMarkTimeout 后台 worker 路径：批量把过期的 pending 改为 timeout。
//
// RLS Phase 2 (R41 P1-6): wrapped in own tx with super_admin role GUC
// (approval_queue policy's role 分支即满足), 规避 autocommit GUC 不生效
// 导致非 default 租户 pending 永不被超时终结。
//
// R49 修订：FOR UPDATE SKIP LOCKED + LIMIT 分批，批返回值 < batchSize 即止。
func TestMarkTimeout(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config\('app\.current_role', 'super_admin', true\)`).
		WillReturnResult(pgxmock.NewResult("SELECT", 0))
	mock.ExpectExec(`UPDATE approval_queue q`).
		WithArgs(ApprovalTimeout, ApprovalPending, approvalTimeoutBatchSize).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))
	mock.ExpectCommit()

	m := NewApprovalManager(mock, 15*time.Minute)
	n, err := m.MarkTimeout(context.Background())
	if err != nil {
		t.Fatalf("MarkTimeout: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 updated, got %d", n)
	}
}

// TestMarkTimeout_MultiBatch（R49 审计 P1 钉桩）：积压超过单批上限时循环
// 分批推进，每批独立事务提交——首批打满 batchSize 触发下一批，末批不满
// 即收敛，总量为两批之和。
func TestMarkTimeout_MultiBatch(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	expectBatch := func(rows int64) {
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT set_config\('app\.current_role', 'super_admin', true\)`).
			WillReturnResult(pgxmock.NewResult("SELECT", 0))
		mock.ExpectExec(`UPDATE approval_queue q`).
			WithArgs(ApprovalTimeout, ApprovalPending, approvalTimeoutBatchSize).
			WillReturnResult(pgxmock.NewResult("UPDATE", rows))
		mock.ExpectCommit()
	}
	expectBatch(int64(approvalTimeoutBatchSize)) // 首批打满 → 触发下一批
	expectBatch(2)                               // 末批不满 → 收敛

	m := NewApprovalManager(mock, 15*time.Minute)
	n, err := m.MarkTimeout(context.Background())
	if err != nil {
		t.Fatalf("MarkTimeout: %v", err)
	}
	if n != int(approvalTimeoutBatchSize)+2 {
		t.Errorf("expected %d updated across batches, got %d", approvalTimeoutBatchSize+2, n)
	}
}

// TestDecide_EmptyApprovalID 参数校验,不应触达 DB。
func TestDecide_EmptyApprovalID(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	m := NewApprovalManager(mock, 15*time.Minute)
	if err := m.Approve(context.Background(), "", "t", "u", "r"); err == nil {
		t.Error("expected error for empty id")
	}
	if err := m.Reject(context.Background(), "", "t", "u", "r"); err == nil {
		t.Error("expected error for empty id")
	}
}

// TestMarkTimeout_RejectBranchAuditFields（R50 审计 P3 钉桩）：timeout-reject
// 分支必须与 approve 分支同样写 approved_by/approved_at——否则"谁在何时终结
// 此审批"的审计查询对该分支无法回答（仅 reason 内嵌秒数可反推）。
func TestMarkTimeout_RejectBranchAuditFields(t *testing.T) {
	b, err := os.ReadFile("approval_manager.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if got := strings.Count(src, "approved_by = 'system:timeout'"); got != 2 {
		t.Errorf("approval_manager.go has %d 'system:timeout' approved_by writes, want 2 (approve + reject branches)", got)
	}
}
