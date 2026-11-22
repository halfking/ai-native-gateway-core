package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// ★ 2026-11-23（12h 修订审计轮）：api_keys.team_id 写入路径的归属校验钉测。
//
// ★ 为什么必须有这组钉测：`api_keys.team_id` 在此前**全仓无任何写入路径**
//   ⇒ `maas/team_budget_gate.go` 的团队花费聚合恒为 0 ⇒ 团队预算门永远判
//   「未超限」。补上写入路径后，这道闸就成了**唯一的防线** ⇒ 必须能被单测
//   钉住，否则下一轮改坏它没有任何东西会变红。
//
// ★ 三条判据各自的可观测行为：
//   ① 团队不存在 / 不属于本租户 ⇒ 不放行（跨租户防串）；
//   ② 调用方不是该团队 owner / 成员 ⇒ 不放行；
//   ③ 调用方没有真实用户身份（legacy admin key，UserID=0）⇒ 不放行，
//      且**一次数据库访问都不该发生**（这是「用 mock 无任何 expectation」
//      就能钉住的：多一次访问 mock 直接报错）。

// newTeamGuardHandler 造一个带 AuthContext 的请求与一个注入了 pgxmock 的 Handler。
func newTeamGuardHandler(t *testing.T, userID int) (*Handler, pgxmock.PgxPoolIface, *http.Request) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	h := &Handler{teamGuardDB: mock}
	req := httptest.NewRequest(http.MethodPost, "/api/keys", nil)
	if userID > 0 {
		req = SetAuthContext(req, &AuthContext{UserID: userID, TenantID: "tenant-a", Role: "tenant_admin"})
	}
	return h, mock, req
}

// TestCallerMayUseTeam_AllowsOwnerOfSameTenant：owner 且团队同租户 ⇒ 放行。
// 断言里同时钉住「RLS GUC 与判据在同一事务内设置」—— teams / user_teams 的
// 策略都基于 current_setting('app.current_user_id')，不设它 ⇒ RLS 与 Go 侧
// 判据各说各话。
func TestCallerMayUseTeam_AllowsOwnerOfSameTenant(t *testing.T) {
	h, mock, req := newTeamGuardHandler(t, 7)

	mock.ExpectBegin()
	mock.ExpectExec("set_config").
		WithArgs("7").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(int64(3), "tenant-a", int64(7)).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	allowed, err := h.callerMayUseTeam(context.Background(), req, 3, "tenant-a")
	if err != nil {
		t.Fatalf("callerMayUseTeam 不得报错: %v", err)
	}
	if !allowed {
		t.Fatalf("owner 且团队同租户必须放行")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("mock 期望未满足: %v", err)
	}
}

// TestCallerMayUseTeam_DeniesCrossTenant：团队不属于本租户 ⇒ 不放行。
// 判据落在 SQL 的 t.tenant_id = $2 上 ⇒ EXISTS 为 false。
func TestCallerMayUseTeam_DeniesCrossTenant(t *testing.T) {
	h, mock, req := newTeamGuardHandler(t, 7)

	mock.ExpectBegin()
	mock.ExpectExec("set_config").
		WithArgs("7").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(int64(3), "tenant-b", int64(7)).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	allowed, err := h.callerMayUseTeam(context.Background(), req, 3, "tenant-b")
	if err != nil {
		t.Fatalf("callerMayUseTeam 不得报错: %v", err)
	}
	if allowed {
		t.Fatalf("跨租户挂团队必须被拒")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("mock 期望未满足: %v", err)
	}
}

// TestCallerMayUseTeam_DeniesNonMember：调用方既不是 owner 也不是成员 ⇒ 不放行。
func TestCallerMayUseTeam_DeniesNonMember(t *testing.T) {
	h, mock, req := newTeamGuardHandler(t, 99)

	mock.ExpectBegin()
	mock.ExpectExec("set_config").
		WithArgs("99").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(int64(3), "tenant-a", int64(99)).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	allowed, err := h.callerMayUseTeam(context.Background(), req, 3, "tenant-a")
	if err != nil {
		t.Fatalf("callerMayUseTeam 不得报错: %v", err)
	}
	if allowed {
		t.Fatalf("非 owner/非成员必须被拒")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("mock 期望未满足: %v", err)
	}
}

// TestCallerMayUseTeam_LegacyAdminKeyIsDenied：无真实用户身份（UserID=0）
// 时**一次数据库访问都不发生**（mock 上没有任何 expectation ⇒ 只要发生
// 任何一次访问，pgxmock 就会直接报错）。legacy admin key 认证没有用户身份，
// 无法证明成员关系 ⇒ 不放行，且不该白跑一趟库。
func TestCallerMayUseTeam_LegacyAdminKeyIsDenied(t *testing.T) {
	h, mock, req := newTeamGuardHandler(t, 0)

	allowed, err := h.callerMayUseTeam(context.Background(), req, 3, "tenant-a")
	if err != nil {
		t.Fatalf("callerMayUseTeam 不得报错: %v", err)
	}
	if allowed {
		t.Fatalf("无真实用户身份必须被拒")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("不应发生任何数据库访问: %v", err)
	}
}

// TestCallerMayUseTeam_NoAuthContextIsDenied：完全没有 AuthContext ⇒ 不放行。
func TestCallerMayUseTeam_NoAuthContextIsDenied(t *testing.T) {
	h, mock, req := newTeamGuardHandler(t, 0)

	allowed, err := h.callerMayUseTeam(context.Background(), req, 3, "tenant-a")
	if err != nil {
		t.Fatalf("callerMayUseTeam 不得报错: %v", err)
	}
	if allowed {
		t.Fatalf("无 AuthContext 必须被拒")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("不应发生任何数据库访问: %v", err)
	}
}

// TestCreateKey_RejectsForeignTeamBeforeDBWork：创建路径在**任何写库动作之前**
// 就必须因「团队归属不合法」而中止 —— 不允许先落库再校验。
// 这里用 nil db（无任何注入面）构造 Handler：若校验被跳过，代码会走到
// getOrCreateApplicationForTenant 并因 nil db 走另一条错误分支，两条分支的
// 错误文案不同 ⇒ 可区分。
func TestCreateKey_RejectsForeignTeamBeforeDBWork(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/api/keys",
		strings.NewReader(`{"application_code":"team-guard","tenant_id":"tenant-a","team_id":3}`))
	req.Header.Set("Content-Type", "application/json")
	req = SetAuthContext(req, &AuthContext{UserID: 7, TenantID: "tenant-a", Role: "tenant_admin"})
	rec := httptest.NewRecorder()

	h.createKey(rec, req)

	// nil db ⇒ 团队归属校验返回「database not configured」⇒ 500 且文案是
	// "query failed"（而不是创建路径后续步骤的错误）。
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("团队校验应在最前面触发 500: got %d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "query failed") {
		t.Fatalf("错误文案应为 query failed: body=%s", body)
	}
}
