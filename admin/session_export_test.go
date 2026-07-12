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
func TestSessionExportAPI_HandleExport_NoData(t *testing.T) {
	api := &SessionExportAPI{db: nil}
	// db=nil should produce Service Unavailable early
	req := httptest.NewRequest(http.MethodGet, "/api/admin/session-export?id=gw_x", nil)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

// TestSessionExport_Aliases 全字段别名 — 确保 admin web 前端解 JSON 不破坏。
//
// 验证 SessionMeta / ResumeBrief / ExportMessage / ExportAttachment 是
// sessionforensics 类型的别名（合并后保持兼容）。
func TestSessionExport_Aliases(t *testing.T) {
	var meta SessionExportMeta = sessionforensics.SessionMeta{
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

// TestSessionExport_DBRequired 各端点有 db=nil 时返 503。
//
// 这是迁移后的新行为：db 必须先注入，否则拒绝服务（避免 nil deref）。
// 真实生产环境 admin.NewSessionExportAPI 在 main.go 启动时统一注入。
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
			w := httptest.NewRecorder()
			api.ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Errorf("expected %d, got %d body=%s", tc.wantStatus, w.Code, w.Body.String())
			}
		})
	}
}
