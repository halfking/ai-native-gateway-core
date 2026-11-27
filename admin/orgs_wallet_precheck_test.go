// orgs_wallet_precheck_test.go —— updateOrgWallet 存在性预检判据
// （2026-11-28 12h 审计轮 F-1）。
//
// 历史：修复前 org 不存在时 ensure-wallet INSERT（tenant_id 子查询 NULL ⇒
// NOT NULL/FK 违约）以 500「ensure org wallet failed」漏出；更早版本把
// orgWalletSelect 别名 42P01 误报成 404。预检后不存在 org 稳定 404，且
// 预检必须发生在任何写操作之前（部分写副作用面）。
package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
)

var errTestOrgsWalletBoom = errors.New("boom")

// TestUpdateOrgWalletMissingOrgReturns404BeforeAnyWrite：不存在 org ⇒
// 404，且之后不得再有任何 DB 交互（预检即短路）。
func TestUpdateOrgWalletMissingOrgReturns404BeforeAnyWrite(t *testing.T) {
	h, mock, _ := newOrgsTestFixture(t)

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM organizations").
		WithArgs(404).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	req := superAdminReq(http.MethodPatch, "/api/admin/orgs/404/wallet", `{"credit_limit_credits":100}`)
	rec := httptest.NewRecorder()
	h.updateOrgWallet(rec, req, 404)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing org must 404 before any write: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations (write happened after precheck?): %v", err)
	}
}

// TestUpdateOrgWalletPrecheckQueryErrorIs500：预检查询本身失败 ⇒ 500
// （真存储故障不允许伪装成 404）。
func TestUpdateOrgWalletPrecheckQueryErrorIs500(t *testing.T) {
	h, mock, _ := newOrgsTestFixture(t)

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM organizations").
		WithArgs(9).
		WillReturnError(errTestOrgsWalletBoom)

	req := superAdminReq(http.MethodPatch, "/api/admin/orgs/9/wallet", `{"credit_limit_credits":100}`)
	rec := httptest.NewRecorder()
	h.updateOrgWallet(rec, req, 9)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("precheck query failure must 500: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// TestUpdateOrgWalletExistingOrgProceedsToEnsureWallet：存在 org ⇒ 预检通过
// 后进入 ensure-wallet（INSERT 分派），证明预检没有拦住正常路径。
func TestUpdateOrgWalletExistingOrgProceedsToEnsureWallet(t *testing.T) {
	h, mock, _ := newOrgsTestFixture(t)

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM organizations").
		WithArgs(7).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	// ensure org wallet（ON CONFLICT DO NOTHING）——3 参 (id, balance, policy)
	mock.ExpectExec("INSERT INTO org_credit_wallets").
		WithArgs(7, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	req := superAdminReq(http.MethodPatch, "/api/admin/orgs/7/wallet", `{}`)
	rec := httptest.NewRecorder()
	h.updateOrgWallet(rec, req, 7)

	if rec.Code == http.StatusNotFound {
		t.Fatalf("existing org must pass precheck: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
