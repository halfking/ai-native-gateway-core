package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// ─── W4.2 能力绑 Key：管理面写路径钉测 ─────────────────────────────
//
// 覆盖三个面：
//   ① POST /api/keys 创建时带 capabilities：合法清单落库、非法清单 400；
//   ② PUT /api/keys/:id/capabilities 整单替换：合法/非法/未知 key；
//   ③ 依赖闭包：session.forensics 单独授予必须 400（缺 session.audit）。
//
// 裁决语义（fail-closed）由 internal/capabilitygate 的测试钉；这里钉的是
// 管理面入口「坏清单进不来」——两层防御，口径一致。

func newCapabilitiesTestHandler(t *testing.T) (*Handler, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(mock.Close)
	return &Handler{keyCreateDB: mock, teamGuardDB: mock, keyCapsOverride: mock}, mock
}

func doCreateKeyWithCaps(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = SetAuthContext(req, &AuthContext{UserID: 7, TenantID: "tenant-a", Role: "tenant_admin"})
	rec := httptest.NewRecorder()
	h.createKey(rec, req)
	return rec
}

func TestCreateKey_CapabilitiesHappyPath(t *testing.T) {
	h, mock := newCapabilitiesTestHandler(t)

	expectKeyCreatePreamble(mock, "tenant-a", "caps-alias")
	mock.ExpectQuery(personalKeyInsertPattern).
		WithArgs(keyInsertArgs()...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(60))

	rec := doCreateKeyWithCaps(t, h,
		`{"key_alias":"caps-alias","capabilities":["session.audit","session.summary"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("合法能力清单必须建 key 成功: got %d body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("落库语句必须恰好是期望的那一条: %v", err)
	}
}

func TestCreateKey_UnknownCapabilityRejected(t *testing.T) {
	h, mock := newCapabilitiesTestHandler(t)

	rec := doCreateKeyWithCaps(t, h,
		`{"key_alias":"bogus","capabilities":["session.audit","bogus.cap"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知能力 ID 必须 400: got %d body=%s", rec.Code, rec.Body.String())
	}
	// 拒绝发生在任何 DB 查询之前（fail-fast）
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("拒绝路径不得触碰数据库: %v", err)
	}
}

func TestCreateKey_CapabilityDependencyClosureRejected(t *testing.T) {
	h, mock := newCapabilitiesTestHandler(t)

	// forensics 依赖 audit；只勾 forensics 是非法授权
	rec := doCreateKeyWithCaps(t, h,
		`{"key_alias":"closure","capabilities":["session.forensics"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("依赖闭包缺失必须 400: got %d body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("拒绝路径不得触碰数据库: %v", err)
	}
}

func TestCreateKey_CapabilitiesOmittedIsNoGrant(t *testing.T) {
	h, mock := newCapabilitiesTestHandler(t)

	expectKeyCreatePreamble(mock, "tenant-a", "no-caps")
	mock.ExpectQuery(personalKeyInsertPattern).
		WithArgs(keyInsertArgs()...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(61))

	rec := doCreateKeyWithCaps(t, h, `{"key_alias":"no-caps"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("不带 capabilities 的存量行为必须零变化: got %d", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("落库语句（capabilities COALESCE '[]'）必须照常执行: %v", err)
	}
}

func TestUpdateKeyCapabilities_ReplaceAndClear(t *testing.T) {
	h, mock := newCapabilitiesTestHandler(t)

	// 整单替换：forensics + audit（闭包完整）
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs(9).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(`UPDATE api_keys`).
		WithArgs(pgxmock.AnyArg(), 9).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPut, "/api/keys/9/capabilities",
		strings.NewReader(`{"capabilities":["session.audit","session.forensics"]}`))
	req = SetAuthContext(req, &AuthContext{UserID: 7, TenantID: "tenant-a", Role: "super_admin"})
	rec := httptest.NewRecorder()
	h.updateKeyCapabilities(rec, req, 9)
	if rec.Code != http.StatusOK {
		t.Fatalf("合法替换必须 200: got %d body=%s", rec.Code, rec.Body.String())
	}
	env := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应必须是 JSON: %v", err)
	}
	caps, _ := env["capabilities"].([]any)
	if len(caps) != 2 {
		t.Fatalf("响应必须回显新授权清单: %v", env)
	}

	// 清空：空数组 = 撤销全部（合法）
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs(9).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(`UPDATE api_keys`).
		WithArgs(pgxmock.AnyArg(), 9).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	req = httptest.NewRequest(http.MethodPut, "/api/keys/9/capabilities",
		strings.NewReader(`{"capabilities":[]}`))
	req = SetAuthContext(req, &AuthContext{UserID: 7, TenantID: "tenant-a", Role: "super_admin"})
	rec = httptest.NewRecorder()
	h.updateKeyCapabilities(rec, req, 9)
	if rec.Code != http.StatusOK {
		t.Fatalf("空清单（撤销全部）必须 200: got %d", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("两次更新语句必须照常执行: %v", err)
	}
}

func TestUpdateKeyCapabilities_InvalidListRejected(t *testing.T) {
	h, mock := newCapabilitiesTestHandler(t)

	req := httptest.NewRequest(http.MethodPut, "/api/keys/9/capabilities",
		strings.NewReader(`{"capabilities":["bogus.cap"]}`))
	req = SetAuthContext(req, &AuthContext{UserID: 7, TenantID: "tenant-a", Role: "super_admin"})
	rec := httptest.NewRecorder()
	h.updateKeyCapabilities(rec, req, 9)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法清单必须 400: got %d", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("拒绝路径不得触碰数据库: %v", err)
	}
}

func TestUpdateKeyCapabilities_UnknownKey404(t *testing.T) {
	h, mock := newCapabilitiesTestHandler(t)

	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs(999).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	req := httptest.NewRequest(http.MethodPut, "/api/keys/999/capabilities",
		strings.NewReader(`{"capabilities":["session.audit"]}`))
	req = SetAuthContext(req, &AuthContext{UserID: 7, TenantID: "tenant-a", Role: "super_admin"})
	rec := httptest.NewRecorder()
	h.updateKeyCapabilities(rec, req, 999)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知 key 必须 404: got %d", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("语句必须按预期执行: %v", err)
	}
}
