package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sessionforensics"
)

// TestSessionExportAPI_HandleExport_NoData 验证无数据时返回 404
// Phase 0 P0-1 (674058ad2): auth/tenant 校验先于 db 可用性检查 —— 无 auth
// 请求必须拿到 401 而不是泄漏 "数据库未配置" 的 503。
func TestSessionExportAPI_AuthPrecedesDBCheck(t *testing.T) {
	api := &SessionExportAPI{db: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-export?id=gw_x", nil)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 (auth precedes db check), got %d", w.Code)
	}
}

// TestSessionExport_Aliases 全字段别名 — 确保 admin web 前端解 JSON 不破坏。
//
// 验证 SessionMeta / ResumeBrief / ExportMessage / ExportAttachment 是
// sessionforensics 类型的别名（合并后保持兼容）。
func TestSessionExport_Aliases(t *testing.T) {
	meta := sessionforensics.SessionMeta{
		ID:         "gw_xxx",
		Title:      "Test",
		TenantID:   "default",
		ExportedAt: "2026-07-12T10:00:00Z",
	}
	if meta.ID != "gw_xxx" {
		t.Fatal("alias broken")
	}

	// JSON 序列化字段名匹配 admin web 前端期望
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"id":`, `"title":`, `"tenant_id":`, `"exported_at":`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("missing key %q in %s", key, b)
		}
	}
}

// TestSessionExport_DBRequired 各端点在通过 P0-1 auth 边界后 db=nil 返 503。
//
// Phase 0 P0-1 之后 auth 先于 db 检查，因此到达 503 需要注入 super_admin
// 上下文；真实生产环境 admin.NewSessionExportAPI 在 main.go 启动时统一注入。
func TestSessionExport_DBRequired(t *testing.T) {
	api := &SessionExportAPI{db: nil}
	cases := []struct {
		name, method, path, body string
		wantStatus               int
	}{
		{"export GET", http.MethodGet, "/api/admin/session-export?id=gw_x", "", http.StatusServiceUnavailable},
		{"import POST", http.MethodPost, "/api/admin/session-export/import", `{}`, http.StatusServiceUnavailable},
		{"fetch GET", http.MethodGet, "/api/admin/session-export/pack?id=x", "", http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body *strings.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			var req *http.Request
			if body != nil {
				req = httptest.NewRequest(tc.method, tc.path, body)
			} else {
				req = httptest.NewRequest(tc.method, tc.path, nil)
			}
			req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "acme"})
			w := httptest.NewRecorder()
			api.ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Errorf("expected %d, got %d body=%s", tc.wantStatus, w.Code, w.Body.String())
			}
		})
	}
}

// TestSessionExport_TenantEnforcement 验证 Phase 0 P0-1 跨租户授权边界：
//   - tenant_admin 不能用 ?tenant= 访问其他租户；
//   - tenant_admin 不带参数时强制使用 auth.TenantID；
//   - super_admin 可跨租户访问（但 db=nil，仍会在 503 之前的 auth 路径返回 200/4xx）。
func TestSessionExport_TenantEnforcement(t *testing.T) {
	cases := []struct {
		name        string
		role        string
		authTenant  string
		queryTenant string
		method      string
		path        string
		wantStatus  int
	}{
		// tenant_admin 跨租户（query 与 auth 不一致）→ 403
		{"tenant_admin export cross-tenant", "tenant_admin", "acme", "globex", "GET", "/api/admin/session-export?id=gw_x", http.StatusForbidden},
		{"tenant_admin import cross-tenant", "tenant_admin", "acme", "globex", "POST", "/api/admin/session-export/import", http.StatusForbidden},
		{"tenant_admin pack cross-tenant", "tenant_admin", "acme", "globex", "GET", "/api/admin/session-export/pack?id=p1", http.StatusForbidden},
		// tenant_admin 不带 query → 走到 db 检查（db=nil → 503，证明 auth 路径放行）
		{"tenant_admin export same-tenant", "tenant_admin", "acme", "", "GET", "/api/admin/session-export?id=gw_x", http.StatusServiceUnavailable},
		// super_admin 跨租户 → 走 db 检查（db=nil → 503，证明 auth 路径放行）
		{"super_admin export cross-tenant", "super_admin", "acme", "globex", "GET", "/api/admin/session-export?id=gw_x", http.StatusServiceUnavailable},
		// 无 auth context → 401
		{"no auth export", "", "", "any", "GET", "/api/admin/session-export?id=gw_x", http.StatusUnauthorized},
		{"no auth pack", "", "", "any", "GET", "/api/admin/session-export/pack?id=p1", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &SessionExportAPI{db: nil}
			url := tc.path
			if tc.queryTenant != "" {
				sep := "?"
				if strings.Contains(tc.path, "?") {
					sep = "&"
				}
				url = tc.path + sep + "tenant=" + tc.queryTenant
			}
			req := httptest.NewRequest(tc.method, url, nil)
			if tc.role != "" {
				req = SetAuthContext(req, &AuthContext{
					Role:     tc.role,
					TenantID: tc.authTenant,
				})
			}
			w := httptest.NewRecorder()
			api.ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Errorf("expected %d, got %d body=%s", tc.wantStatus, w.Code, w.Body.String())
			}
		})
	}
}
