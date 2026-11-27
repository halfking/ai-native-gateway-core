package admin

// 三级配额制 admin 面判据（2026-11-27）：
//
//   ① handleOrgs 路由分发：model-policies 子资源接线存在（org 级策略有
//      管理面入口 —— gate-with-call-site 纪律）；
//   ② org 级策略创建：落库形状钉死（mode/canonical/created_by 逐参）＋
//      写后 Checker 缓存失效被调用；
//   ③ user 钱包 PATCH：三态 limit（保持/设 NULL/设值）落库形状；
//   ④ mode 校验：allow/deny 之外 400，不落库；
//   ⑤ user 级策略路由的 super_admin 守卫。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
)

// stubModelPolicyChecker 钉「写路径调用了 Invalidate*」。
type stubModelPolicyChecker struct {
	invalidatedTenants []string
	invalidatedOrgs    []int64
	invalidatedUsers   []int64
}

func (s *stubModelPolicyChecker) Invalidate(tenantID string) {
	s.invalidatedTenants = append(s.invalidatedTenants, tenantID)
}
func (s *stubModelPolicyChecker) InvalidateOrg(orgID int64) {
	s.invalidatedOrgs = append(s.invalidatedOrgs, orgID)
}
func (s *stubModelPolicyChecker) InvalidateUser(userID int64) {
	s.invalidatedUsers = append(s.invalidatedUsers, userID)
}

func newOrgsTestFixture(t *testing.T) (*Handler, pgxmock.PgxPoolIface, *stubModelPolicyChecker) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	h := &Handler{}
	h.orgPolicyDB = mock
	stub := &stubModelPolicyChecker{}
	h.modelPolicy = stub
	return h, mock, stub
}

// nowPtr returns a time.Time (not a string) for mock timestamp columns —
// pgx scan into *time.Time rejects string values.
func nowPtr() time.Time { return time.Unix(0, 0).UTC() }

func superAdminReq(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	return req.WithContext(context.WithValue(req.Context(), authContextKey{}, &AuthContext{Username: "tester", Role: "super_admin"}))
}

// TestOrgModelPolicyCreatePersistsRowAndInvalidates：
// POST org 级策略：落库整行（mode 逐参）+ InvalidateOrg 被调。
func TestOrgModelPolicyCreatePersistsRowAndInvalidates(t *testing.T) {
	h, mock, stub := newOrgsTestFixture(t)

	// subjectExists → organizations EXISTS
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM organizations").
		WithArgs(7).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	// BEGIN（set_config + INSERT 走同一事务）
	mock.ExpectBegin()
	// set_config（审计 actor GUC；带 $1 参 ⇒ WithArgs 钉 actor）
	mock.ExpectQuery("SELECT set_config\\('app.current_admin'").
		WithArgs("tester").
		WillReturnRows(pgxmock.NewRows([]string{"set_config"}).AddRow("tester"))
	// INSERT 整行
	mock.ExpectQuery("INSERT INTO org_model_policies \\(org_id, mode, canonical_name, reason, created_by\\)").
		WithArgs(7, "allow", "glm-5.1", "whitelist pilot", "tester").
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "mode", "canonical_name", "reason", "created_by", "deleted_at", "deleted_by", "created_at", "updated_at"}).
			AddRow(11, 7, "allow", "glm-5.1", "whitelist pilot", "tester", nil, nil, nowPtr(), nowPtr()))
	// COMMIT
	mock.ExpectCommit()

	req := superAdminReq(http.MethodPost, "/api/admin/orgs/7/model-policies", `{"canonical_name": "glm-5.1", "mode": "allow", "reason": "whitelist pilot"}`)
	w := httptest.NewRecorder()

	h.createSubjectModelPolicy(w, req, "org", 7)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s, want 201", w.Code, w.Body.String())
	}
	if len(stub.invalidatedOrgs) != 1 || stub.invalidatedOrgs[0] != 7 {
		t.Fatalf("InvalidateOrg must be called once with org 7, got %v", stub.invalidatedOrgs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL shape mismatch: %v", err)
	}
}

// TestOrgModelPolicyCreateRejectsInvalidMode：mode 白名单外 400、不落库。
func TestOrgModelPolicyCreateRejectsInvalidMode(t *testing.T) {
	h, mock, stub := newOrgsTestFixture(t)

	req := superAdminReq(http.MethodPost, "/api/admin/orgs/7/model-policies", `{"canonical_name": "glm-5.1", "mode": "permissive"}`)
	w := httptest.NewRecorder()

	h.createSubjectModelPolicy(w, req, "org", 7)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for invalid mode", w.Code)
	}
	// mock 上没有任何 expectation ⇒ ExpectationsWereMet 恒 nil ⇒ 真判据是
	// 「没有期望也没有调用」（空 expectation + 400 即零 SQL 的机器判据）。
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("no SQL expectation should exist or be consumed for an invalid mode: %v", err)
	}
	if len(stub.invalidatedOrgs) != 0 {
		t.Error("cache must not be invalidated when the create is rejected")
	}
}

// TestOrgModelPolicyRouteDispatch：model-policies 子资源从 handleOrgs 分发
// 到共享判定体（gate-with-call-site：无此分支则 org 策略无管理入口）。
func TestOrgModelPolicyRouteDispatch(t *testing.T) {
	h, mock, _ := newOrgsTestFixture(t)

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM organizations").
		WithArgs(7).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("SELECT id, org_id, mode, canonical_name").
		WithArgs(7).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "mode", "canonical_name", "reason", "created_by", "deleted_at", "deleted_by", "created_at", "updated_at"}).
			AddRow(1, 7, "deny", "minimax-m3", "bad model", "tester", nil, nil, nowPtr(), nowPtr()))

	req := superAdminReq(http.MethodGet, "/api/admin/orgs/7/model-policies", "")
	req.URL.Path = "/api/admin/orgs/7/model-policies"
	w := httptest.NewRecorder()

	h.handleOrgs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200 (dispatch must reach org policy list)", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not json: %v", err)
	}
	if resp["count"].(float64) != 1 {
		t.Fatalf("count = %v, want 1", resp["count"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL shape mismatch: %v", err)
	}
}

// TestUserWalletPatchSetsValueLimit：user 钱包 PATCH 设值 limit 落库形状。
func TestUserWalletPatchSetsValueLimit(t *testing.T) {
	h, mock, _ := newOrgsTestFixture(t)

	fifty := int64(50)
	// ensure row（全 3 参：id + COALESCE($2,0) + COALESCE($3,'warn')；本请求
	// balance/policy 缺省 ⇒ $2/$3 为 nil 值）
	mock.ExpectExec("INSERT INTO user_credit_wallets").
		WithArgs(42, (*int64)(nil), (*string)(nil)).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	// limit set value (JSON number → concrete 50)
	mock.ExpectExec("UPDATE user_credit_wallets SET credit_limit_credits = \\$2").
		WithArgs(42, fifty).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	// read back
	mock.ExpectQuery("SELECT COALESCE\\(balance_credits, 0\\)").
		WithArgs(42).
		WillReturnRows(pgxmock.NewRows([]string{"balance", "limit", "policy"}).AddRow(int64(0), &fifty, "warn"))

	req := superAdminReq(http.MethodPatch, "/api/users/42/wallet", `{"credit_limit_credits": 50}`)
	w := httptest.NewRecorder()

	h.updateUserWallet(w, req, 42)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL shape mismatch: %v", err)
	}
}

// TestUserWalletPatchTriStateLimitNull：limit 设 NULL（JSON null）落库形状。
func TestUserWalletPatchTriStateLimitNull(t *testing.T) {
	h, mock, _ := newOrgsTestFixture(t)

	// ensure row（no balance/policy change ⇒ driver 仍传全 3 参（$2/$3 为 nil 值））
	mock.ExpectExec("INSERT INTO user_credit_wallets").
		WithArgs(42, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	// limit set NULL（"unlimited" 哨兵 ⇒ SET credit_limit_credits = NULL）
	mock.ExpectExec("UPDATE user_credit_wallets SET credit_limit_credits = NULL").
		WithArgs(42).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	mock.ExpectQuery("SELECT COALESCE\\(balance_credits, 0\\)").
		WithArgs(42).
		WillReturnRows(pgxmock.NewRows([]string{"balance", "limit", "policy"}).AddRow(int64(0), nil, "warn"))

	req := superAdminReq(http.MethodPatch, "/api/users/42/wallet", `{"credit_limit_credits": "unlimited"}`)
	w := httptest.NewRecorder()

	h.updateUserWallet(w, req, 42)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", w.Code, w.Body.String())
	}
	// 响应 wallet.credit_limit_credits 必须是 null（三态「设 NULL」落库后的读回）。
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not json: %v", err)
	}
	if wallet, ok := resp["wallet"].(map[string]any); !ok {
		t.Fatalf("response missing wallet: %v", resp)
	} else if v, exists := wallet["credit_limit_credits"]; !exists || v != nil {
		t.Fatalf("credit_limit_credits = %v (exists=%v), want null", v, exists)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL shape mismatch: %v", err)
	}
}

// TestUserModelPolicyRouteGuard：非 super_admin 触碰 user 级策略 403。
// 走 handleUsers 真入口：tenant_admin（带租户作用域）通过 requireUserManager
// 后命中 model-policies 分支守卫 ⇒ 403 且零 SQL。
func TestUserModelPolicyRouteGuard(t *testing.T) {
	h, mock, _ := newOrgsTestFixture(t)

	req := httptest.NewRequest(http.MethodPost, "/api/users/42/model-policies", strings.NewReader(`{"canonical_name":"x"}`))
	req = req.WithContext(context.WithValue(req.Context(), authContextKey{}, &AuthContext{
		Username: "tenantadmin", Role: "tenant_admin", TenantID: "acme",
	}))
	req.URL.Path = "/api/users/42/model-policies"
	w := httptest.NewRecorder()

	h.handleUsers(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403 (model policy management is super_admin only)", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("no SQL should run when the role guard rejects: %v", err)
	}
}

var _ = pgx.ErrNoRows // keep import if helpers change
