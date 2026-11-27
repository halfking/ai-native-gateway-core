// applications_model_policy_test.go —— application（Key）级模型 allowlist
// 管理面的落库形状 + InvalidateApplication 调用点判据（2026-11-28 L144 轮）。
//
// team_budget_gate 教训：写面与缓存失效必须钉「调用点存在」——PATCH 后
// 不调 InvalidateApplication ⇒ 判据红（失效静默失效、生效等 60s TTL）。
package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
)

// TestApplicationAllowedModelsPatchPersistsAndInvalidates：PATCH 非空名单
// ⇒ UPDATE 落库 + InvalidateApplication 被调 + 响应回显规范化后的名单。
func TestApplicationAllowedModelsPatchPersistsAndInvalidates(t *testing.T) {
	h, mock, stub := newOrgsTestFixture(t)

	// 目标行存在性
	mock.ExpectQuery("SELECT EXISTS \\(SELECT 1 FROM applications").
		WithArgs(int64(7)).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	// UPDATE 落库（payload 是规范化 JSON 数组——pgxmock 对 jsonb 参数用 AnyArg）
	mock.ExpectExec("UPDATE applications SET allowed_models_json").
		WithArgs(int64(7), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))

	req := superAdminReq(http.MethodPatch, "/api/admin/applications/7/allowed-models",
		`{"allowed_models":["GLM-5.1"," glm-5.1 ","kimi-k3"]}`)
	rec := httptest.NewRecorder()
	h.handleApplicationAllowedModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH must succeed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(stub.invalidatedApplications) != 1 || stub.invalidatedApplications[0] != 7 {
		t.Fatalf("PATCH must invalidate application 7: got %v", stub.invalidatedApplications)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
	// 响应回显规范化（trim/去重/小写）：两个去重后条目。
	want := `{"application_id":7,"allowed_models":["glm-5.1","kimi-k3"]}`
	if got := rec.Body.String(); got != want+"\n" && got != want {
		t.Errorf("response body mismatch:\n got %s\nwant %s", got, want)
	}
}

// TestApplicationAllowedModelsPatchEmptyClearsToNull：空数组 = 清除 =
// 落 NULL（未启用），仍调 InvalidateApplication。
func TestApplicationAllowedModelsPatchEmptyClearsToNull(t *testing.T) {
	h, mock, stub := newOrgsTestFixture(t)

	mock.ExpectQuery("SELECT EXISTS \\(SELECT 1 FROM applications").
		WithArgs(int64(3)).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("UPDATE applications SET allowed_models_json").
		WithArgs(int64(3), nil).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))

	req := superAdminReq(http.MethodPatch, "/api/admin/applications/3/allowed-models", `{"allowed_models":[]}`)
	rec := httptest.NewRecorder()
	h.handleApplicationAllowedModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH empty must succeed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(stub.invalidatedApplications) != 1 {
		t.Fatalf("clear must still invalidate: got %v", stub.invalidatedApplications)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// TestApplicationAllowedModelsPatchMissingApp404：目标行不存在 ⇒ 404，
// 不 UPDATE、不失效。
func TestApplicationAllowedModelsPatchMissingApp404(t *testing.T) {
	h, mock, stub := newOrgsTestFixture(t)

	mock.ExpectQuery("SELECT EXISTS \\(SELECT 1 FROM applications").
		WithArgs(int64(999)).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	req := superAdminReq(http.MethodPatch, "/api/admin/applications/999/allowed-models",
		`{"allowed_models":["glm-5.1"]}`)
	rec := httptest.NewRecorder()
	h.handleApplicationAllowedModels(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing application must 404: status=%d", rec.Code)
	}
	if len(stub.invalidatedApplications) != 0 {
		t.Fatalf("404 path must not invalidate: got %v", stub.invalidatedApplications)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// TestApplicationAllowedModelsTenantAdmin403：tenant_admin 不得写
// application 级 allowlist（super_admin 守卫，org 面同款）。
func TestApplicationAllowedModelsTenantAdmin403(t *testing.T) {
	h, mock, _ := newOrgsTestFixture(t)

	req := superAdminReq(http.MethodPatch, "/api/admin/applications/7/allowed-models", `{"allowed_models":["glm-5.1"]}`)
	req = req.WithContext(context.WithValue(req.Context(), authContextKey{}, &AuthContext{
		Username: "tenantadmin", Role: "tenant_admin", TenantID: "acme",
	}))
	rec := httptest.NewRecorder()
	h.handleApplicationAllowedModels(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("tenant_admin must be 403: status=%d", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("403 path must not touch the db: %v", err)
	}
}

// TestApplicationAllowedModelsGetReturnsList：GET 读回 JSONB 数组原文。
func TestApplicationAllowedModelsGetReturnsList(t *testing.T) {
	h, mock, _ := newOrgsTestFixture(t)

	mock.ExpectQuery("SELECT allowed_models_json FROM applications").
		WithArgs(int64(7)).
		WillReturnRows(pgxmock.NewRows([]string{"allowed_models_json"}).
			AddRow([]byte(`["glm-5.1","kimi-k3"]`)))

	req := superAdminReq(http.MethodGet, "/api/admin/applications/7/allowed-models", "")
	rec := httptest.NewRecorder()
	h.handleApplicationAllowedModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET must succeed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	want := `{"application_id":7,"allowed_models":["glm-5.1","kimi-k3"]}`
	if got := rec.Body.String(); got != want+"\n" && got != want {
		t.Errorf("response body mismatch:\n got %s\nwant %s", got, want)
	}
}
