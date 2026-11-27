package admin

// 角色治理轮（2026-11-26）的判据测试。
//
// 背景（live 211 实测）：租户 chenb（非 default）下挂着 role=super_admin 的账号，
// 且 POST /api/users 对任意租户都接受 role=super_admin ⇒ 平台级身份落进业务租户，
// 租户边界失效。本文件把两条不变量钉住：
//
//	① 角色闭集 = {super_admin, tenant_admin, user}（普通用户此前根本建不出来）。
//	② super_admin 只允许落在 default 租户（建号与变更角色两条写路径同源）。
//
// 量具说明：判据不靠「期望值由被测对象生成」，而是直接断言**HTTP 状态码 + 错误文案**
// 与**实际落到 DB 的语句**（pgxmock 钉住 SQL 与参数）⇒ 绕过路径（少查/多查/写错列）
// 都会让本文件红。

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// ── 纯口径（唯一口径的本身）────────────────────────────────────────

func TestValidateAssignableRole_Matrix(t *testing.T) {
	cases := []struct {
		name     string
		role     string
		tenant   string
		wantErr  bool
		errParts string
	}{
		{name: "default 租户可以是超级管理员", role: RoleSuperAdmin, tenant: "default"},
		{name: "default 租户可以是租户管理员", role: RoleTenantAdmin, tenant: "default"},
		{name: "default 租户可以是普通用户", role: RoleUser, tenant: "default"},
		{name: "非 default 租户可以是租户管理员", role: RoleTenantAdmin, tenant: "chenb"},
		{name: "非 default 租户可以是普通用户", role: RoleUser, tenant: "chenb"},
		{name: "非 default 租户不得是超级管理员", role: RoleSuperAdmin, tenant: "chenb", wantErr: true, errParts: "only allowed in the default tenant"},
		{name: "闭集外的角色被拒", role: "wizard", tenant: "default", wantErr: true, errParts: "role must be one of"},
		{name: "空角色被拒（调用方须先落默认）", role: "", tenant: "default", wantErr: true, errParts: "role must be one of"},
		{name: "channel 角色不在管理端闭集内", role: "channel", tenant: "default", wantErr: true, errParts: "role must be one of"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAssignableRole(tc.role, tc.tenant)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际 nil（role=%s tenant=%s）", tc.role, tc.tenant)
				}
				if !strings.Contains(err.Error(), tc.errParts) {
					t.Fatalf("错误文案不含 %q，实际 %q", tc.errParts, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("期望通过，实际报错：%v", err)
			}
		})
	}
}

func TestRoleOptionsForTenant(t *testing.T) {
	def := roleOptionsForTenant("default")
	if len(def) != 3 || def[0] != RoleSuperAdmin || def[1] != RoleTenantAdmin || def[2] != RoleUser {
		t.Fatalf("default 租户的角色闭集不对：%v", def)
	}
	for _, tenant := range []string{"chenb", "hansi", "acme", "kevin"} {
		opts := roleOptionsForTenant(tenant)
		if len(opts) != 2 {
			t.Fatalf("非 default 租户 %s 的角色闭集应只有 2 档，实际 %v", tenant, opts)
		}
		for _, o := range opts {
			if o == RoleSuperAdmin {
				t.Fatalf("非 default 租户 %s 的角色闭集里出现了超级管理员：%v", tenant, opts)
			}
		}
	}
}

// ── 写路径：建号（POST /api/users）──────────────────────────────────

func newUsersHandler(t *testing.T) (*Handler, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	h := &Handler{usersDB: mock}
	return h, mock
}

func superAdminRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = SetAuthContext(req, &AuthContext{
		UserID: 1, TenantID: DefaultTenantCode, Username: "admin",
		Role: RoleSuperAdmin, IsJWT: true,
	})
	return req
}

// 非 default 租户建超级管理员 ⇒ 400，且**不落库**。
func TestCreateUser_NonDefaultTenantRejectsSuperAdmin(t *testing.T) {
	h, mock := newUsersHandler(t)
	defer mock.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT status FROM tenants WHERE code = $1`)).
		WithArgs("chenb").
		WillReturnRows(mock.NewRows([]string{"status"}).AddRow("active"))
	// ★ 没有任何 ExpectExec ⇒ 若代码真的写了库，pgxmock 会因「未预期的查询」而红。

	req := superAdminRequest(http.MethodPost, "/api/users",
		`{"username":"chenb2","password":"ValidPass123","tenant_id":"chenb","role":"super_admin"}`)
	rec := httptest.NewRecorder()

	h.createUser(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "super_admin role is only allowed in the default tenant") {
		t.Fatalf("错误文案不对：%s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock 期望未满足（说明多查了库）：%v", err)
	}
}

// 非 default 租户建「普通用户」⇒ 201（此前被闭集挡在门外，建不出来）。
func TestCreateUser_NonDefaultTenantAllowsRegularUser(t *testing.T) {
	h, mock := newUsersHandler(t)
	defer mock.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT status FROM tenants WHERE code = $1`)).
		WithArgs("chenb").
		WillReturnRows(mock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO users`)).
		WithArgs("chenb", "chenb2", pgxmock.AnyArg(), "", "", RoleUser, []string{RoleUser}).
		WillReturnRows(mock.NewRows([]string{
			"id", "tenant_id", "username", "display_name", "email", "role", "roles",
			"enabled", "must_change_password", "created_at",
		}).AddRow(99, "chenb", "chenb2", "", "", RoleUser, []string{RoleUser}, true, true, time.Now()))

	req := superAdminRequest(http.MethodPost, "/api/users",
		`{"username":"chenb2","password":"ValidPass123","tenant_id":"chenb","role":"user"}`)
	rec := httptest.NewRecorder()

	h.createUser(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("期望 201，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"role":"user"`) {
		t.Fatalf("响应里没有落回普通用户角色：%s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock 期望未满足：%v", err)
	}
}

// 闭集外的角色 ⇒ 400，且不落库。
func TestCreateUser_RejectsRoleOutsideClosedSet(t *testing.T) {
	h, mock := newUsersHandler(t)
	defer mock.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT status FROM tenants WHERE code = $1`)).
		WithArgs(DefaultTenantCode).
		WillReturnRows(mock.NewRows([]string{"status"}).AddRow("active"))

	req := superAdminRequest(http.MethodPost, "/api/users",
		`{"username":"wiz","password":"ValidPass123","tenant_id":"default","role":"wizard"}`)
	rec := httptest.NewRecorder()

	h.createUser(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "role must be one of") {
		t.Fatalf("错误文案不对：%s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock 期望未满足：%v", err)
	}
}

// ── 写路径：变更角色（PUT /api/users/:id）──────────────────────────

// 非 default 租户的用户不得被提升为超级管理员 ⇒ 400，且不写库。
func TestUpdateUser_NonDefaultTenantRejectsSuperAdmin(t *testing.T) {
	h, mock := newUsersHandler(t)
	defer mock.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT tenant_id, role FROM users WHERE id = $1`)).
		WithArgs(49).
		WillReturnRows(mock.NewRows([]string{"tenant_id", "role"}).AddRow("chenb", RoleTenantAdmin))

	req := superAdminRequest(http.MethodPut, "/api/users/49", `{"role":"super_admin"}`)
	rec := httptest.NewRecorder()

	h.updateUser(rec, req, 49)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "super_admin role is only allowed in the default tenant") {
		t.Fatalf("错误文案不对：%s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock 期望未满足（说明多写了库）：%v", err)
	}
}

// ★ live 上「租户 chenb 下那个 super_admin 降级为租户管理员」这条真实路径必须能走通。
func TestUpdateUser_DemotesSuperAdminInNonDefaultTenant(t *testing.T) {
	h, mock := newUsersHandler(t)
	defer mock.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT tenant_id, role FROM users WHERE id = $1`)).
		WithArgs(49).
		WillReturnRows(mock.NewRows([]string{"tenant_id", "role"}).AddRow("chenb", RoleSuperAdmin))
	// 降级超级管理员时要先确认「还有别的超级管理员」⇒ 这里有 4 个。
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT count(*) FROM users WHERE role = $1 AND enabled AND id <> $2`)).
		WithArgs(RoleSuperAdmin, 49).
		WillReturnRows(mock.NewRows([]string{"count"}).AddRow(4))
	mock.ExpectQuery(regexp.QuoteMeta(`UPDATE users SET role = $1, updated_at = now() WHERE id = $2`)).
		WithArgs(RoleTenantAdmin, 49).
		WillReturnRows(mock.NewRows([]string{
			"id", "tenant_id", "username", "display_name", "email", "role",
			"enabled", "must_change_password", "last_login_at", "created_at",
		}).AddRow(49, "chenb", "chenb", "", "", RoleTenantAdmin, true, false, nil, time.Now()))

	req := superAdminRequest(http.MethodPut, "/api/users/49", `{"role":"tenant_admin"}`)
	rec := httptest.NewRecorder()

	h.updateUser(rec, req, 49)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"role":"tenant_admin"`) {
		t.Fatalf("响应角色不对：%s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock 期望未满足：%v", err)
	}
}

// 最后一个超级管理员不能被降级 ⇒ 409（否则管理台唯一入口被关掉）。
func TestUpdateUser_CannotDemoteLastSuperAdmin(t *testing.T) {
	h, mock := newUsersHandler(t)
	defer mock.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT tenant_id, role FROM users WHERE id = $1`)).
		WithArgs(41).
		WillReturnRows(mock.NewRows([]string{"tenant_id", "role"}).AddRow("default", RoleSuperAdmin))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT count(*) FROM users WHERE role = $1 AND enabled AND id <> $2`)).
		WithArgs(RoleSuperAdmin, 41).
		WillReturnRows(mock.NewRows([]string{"count"}).AddRow(0))

	req := superAdminRequest(http.MethodPut, "/api/users/41", `{"role":"tenant_admin"}`)
	rec := httptest.NewRecorder()

	h.updateUser(rec, req, 41)

	if rec.Code != http.StatusConflict {
		t.Fatalf("期望 409，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cannot demote the last super_admin") {
		t.Fatalf("错误文案不对：%s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock 期望未满足（说明写了库）：%v", err)
	}
}

// tenant_admin 永远不能授予超级管理员 ⇒ 403。
func TestUpdateUser_TenantAdminCannotGrantSuperAdmin(t *testing.T) {
	h, mock := newUsersHandler(t)
	defer mock.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT tenant_id, role FROM users WHERE id = $1`)).
		WithArgs(50).
		WillReturnRows(mock.NewRows([]string{"tenant_id", "role"}).AddRow("chenb", RoleUser))

	req := httptest.NewRequest(http.MethodPut, "/api/users/50", strings.NewReader(`{"role":"super_admin"}`))
	req = SetAuthContext(req, &AuthContext{
		UserID: 2, TenantID: "chenb", Username: "chenb",
		Role: RoleTenantAdmin, IsJWT: true,
	})
	rec := httptest.NewRecorder()

	h.updateUser(rec, req, 50)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("期望 403，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tenant_admin cannot set super_admin role") {
		t.Fatalf("错误文案不对：%s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock 期望未满足：%v", err)
	}
}

// ★ 2026-11-28 审计轮回归：只改 roles 数组（不改 role）也必须先查目标用户的
//   租户与当前主角色（修复前 super_admin 只改 roles 时 targetRole 恒空 ⇒
//   归一化没有主角色可前置、成员校验没有租户可判）。
func TestUpdateUser_RolesOnlyPerformsTargetLookup(t *testing.T) {
	h, mock := newUsersHandler(t)
	defer mock.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT tenant_id, role FROM users WHERE id = $1`)).
		WithArgs(77).
		WillReturnRows(mock.NewRows([]string{"tenant_id", "role"}).AddRow("default", RoleUser))
	mock.ExpectQuery(regexp.QuoteMeta(`UPDATE users SET roles = $1, updated_at = now() WHERE id = $2 RETURNING`)).
		WithArgs([]string{RoleUser, RoleTenantAdmin}, 77).
		WillReturnRows(mock.NewRows([]string{
			"id", "tenant_id", "username", "display_name", "email", "role",
			"enabled", "must_change_password", "last_login_at", "created_at",
		}).AddRow(77, "default", "u77", "", "", RoleUser, true, false, nil, time.Now()))

	req := superAdminRequest(http.MethodPut, "/api/users/77", `{"roles":["tenant_admin"]}`)
	rec := httptest.NewRecorder()

	h.updateUser(rec, req, 77)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock 期望未满足：%v", err)
	}
}

// ★ 2026-11-28 审计轮回归：同请求改 role + roles 时归一化必须用**新**主角色
//   前置（修复前用旧主角色 ⇒ roles[0]=旧主角色、旧角色残留数组，违反
//   Roles[0]=主角色 契约）。当前主角色 user，请求 role=tenant_admin +
//   roles=["user","tenant_admin"] ⇒ 期望库中 roles=["tenant_admin","user"]。
func TestUpdateUser_RoleAndRolesUsesNewPrimaryFirst(t *testing.T) {
	h, mock := newUsersHandler(t)
	defer mock.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT tenant_id, role FROM users WHERE id = $1`)).
		WithArgs(88).
		WillReturnRows(mock.NewRows([]string{"tenant_id", "role"}).AddRow("default", RoleUser))
	mock.ExpectQuery(regexp.QuoteMeta(`UPDATE users SET role = $1, roles = $2, updated_at = now() WHERE id = $3 RETURNING`)).
		WithArgs(RoleTenantAdmin, []string{RoleTenantAdmin, RoleUser}, 88).
		WillReturnRows(mock.NewRows([]string{
			"id", "tenant_id", "username", "display_name", "email", "role",
			"enabled", "must_change_password", "last_login_at", "created_at",
		}).AddRow(88, "default", "u88", "", "", RoleTenantAdmin, true, false, nil, time.Now()))

	req := superAdminRequest(http.MethodPut, "/api/users/88", `{"role":"tenant_admin","roles":["user","tenant_admin"]}`)
	rec := httptest.NewRecorder()

	h.updateUser(rec, req, 88)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock 期望未满足：%v", err)
	}
}
