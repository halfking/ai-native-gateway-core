package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/kaixuan/llm-gateway-go/security/sensitive"
	"github.com/kaixuan/llm-gateway-go/settings"
)

func sanitizeAPIConfig() sanitizerules.Config {
	return sanitizerules.Config{Rules: []sanitizerules.Rule{{ID: "secret", Name: "Secret", Type: "secret", Kind: "pattern", Pattern: `secret=(\w+)`, Group: 1, Scopes: []string{"input"}, Action: "mask", Enabled: true}}}
}
func sanitizeAPIHandler(t *testing.T) (*Handler, *sanitizerules.MemoryStore) {
	t.Helper()
	s := sanitizerules.NewMemoryStore()
	m, err := sanitizerules.NewManager(s, sanitizeAPIConfig(), sanitizerules.Options{InstanceID: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := &Handler{secret: "sanitize-api-secret"}
	h.SetSanitizeRuleManager(m)
	return h, s
}
func sanitizeAPIRequest(t *testing.T, h *Handler, method, path, role, tenant, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if role != "" {
		r = SetAuthContext(r, &AuthContext{UserID: 42, Username: "operator", Role: role, TenantID: tenant})
	}
	w := httptest.NewRecorder()
	h.handleSanitizeRules(w, r)
	return w
}
func TestSanitizeRuleRoutesAlwaysRegisteredAndAuthenticated(t *testing.T) {
	h := &Handler{secret: "sanitize-api-secret"}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	token, _, err := SignToken(1, "default", "root", "super_admin", h.secret, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/admin/sanitize-rules", "/api/admin/sanitize-rules/validate", "/api/admin/sanitize-rules/publish", "/api/admin/sanitize-rules/rollback", "/api/admin/sanitize-rules/versions", "/api/admin/sanitize-rules/events", "/api/admin/sanitize-rules/events/1/review", "/api/admin/sanitize-rules/instances"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Errorf("anonymous %s: %d %s", path, w.Code, w.Body.String())
		}
		r = httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 503 {
			t.Errorf("nil manager %s: %d %s", path, w.Code, w.Body.String())
		}
	}
}
func TestSanitizeRuleAuthorizationMatrix(t *testing.T) {
	h, _ := sanitizeAPIHandler(t)
	for _, role := range []string{"tenant_admin", "user", "unknown"} {
		for _, path := range []string{"", "/validate", "/publish", "/rollback", "/versions", "/instances"} {
			w := sanitizeAPIRequest(t, h, "GET", "/api/admin/sanitize-rules"+path, role, "a", "")
			if w.Code != 403 {
				t.Errorf("%s %s=%d", role, path, w.Code)
			}
		}
	}
	for _, role := range []string{"super_admin", "admin_key"} {
		w := sanitizeAPIRequest(t, h, "GET", "/api/admin/sanitize-rules", role, "a", "")
		if w.Code != 200 {
			t.Fatal(role, w.Code, w.Body.String())
		}
	}
	for _, role := range []string{"user", "unknown"} {
		w := sanitizeAPIRequest(t, h, "GET", "/api/admin/sanitize-rules/events", role, "a", "")
		if w.Code != 403 {
			t.Fatal(role, w.Code)
		}
	}
	w := sanitizeAPIRequest(t, h, "GET", "/api/admin/sanitize-rules/events", "tenant_admin", "", "")
	if w.Code != 403 {
		t.Fatal("missing authenticated tenant", w.Code)
	}
}
func TestSanitizeRulePublishValidateRollbackResponses(t *testing.T) {
	h, s := sanitizeAPIHandler(t)
	cfg := sanitizeAPIConfig()
	raw, _ := json.Marshal(cfg)
	w := sanitizeAPIRequest(t, h, "GET", "/api/admin/sanitize-rules", "super_admin", "default", "")
	var current struct {
		Version int64                  `json:"version"`
		Config  sanitizerules.Config   `json:"config"`
		Digest  string                 `json:"config_digest"`
		Status  sanitizerules.Instance `json:"status"`
	}
	if json.Unmarshal(w.Body.Bytes(), &current) != nil || current.Version != 1 || current.Digest == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = sanitizeAPIRequest(t, h, "POST", "/api/admin/sanitize-rules/validate", "super_admin", "default", string(raw))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"valid":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	body := `{"base_version":1,"config":` + string(raw) + `,"comment":"publish"}`
	w = sanitizeAPIRequest(t, h, "POST", "/api/admin/sanitize-rules/publish", "super_admin", "default", body)
	var v sanitizerules.Version
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.ID != 2 || v.Actor != "operator" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = sanitizeAPIRequest(t, h, "POST", "/api/admin/sanitize-rules/publish", "super_admin", "default", body)
	if w.Code != 409 {
		t.Fatal("missing conflict", w.Code, w.Body.String())
	}
	w = sanitizeAPIRequest(t, h, "POST", "/api/admin/sanitize-rules/rollback", "super_admin", "default", `{"base_version":2,"target_version":1,"comment":"restore"}`)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.ID != 3 || v.RollbackOf != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = sanitizeAPIRequest(t, h, "GET", "/api/admin/sanitize-rules/versions", "super_admin", "default", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"versions"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = sanitizeAPIRequest(t, h, "GET", "/api/admin/sanitize-rules/instances", "super_admin", "default", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"target_version":3`) {
		t.Fatal(w.Code, w.Body.String())
	}
	head, _ := s.Current(context.Background())
	if head.ID != 3 {
		t.Fatal(head)
	}
}
func TestSanitizeRuleStrictJSONRejectsWithoutPublishing(t *testing.T) {
	h, s := sanitizeAPIHandler(t)
	raw, _ := json.Marshal(sanitizeAPIConfig())
	valid := `{"base_version":1,"config":` + string(raw) + `}`
	for _, body := range []string{valid + ` {}`, strings.Replace(valid, `"base_version":1`, `"base_version":1,"actor":"forged"`, 1), strings.Replace(valid, `"name":"Secret"`, `"name":"Secret","script":"shell"`, 1), `null`, strings.Repeat(" ", 2<<20) + valid, `{"config":` + string(raw) + `}`, strings.Replace(valid, `"base_version":1`, `"base_version":-1`, 1)} {
		w := sanitizeAPIRequest(t, h, "POST", "/api/admin/sanitize-rules/publish", "super_admin", "default", body)
		if w.Code != 400 {
			t.Errorf("invalid JSON accepted: %d %s", w.Code, w.Body.String())
		}
	}
	v, _ := s.Current(context.Background())
	if v.ID != 1 {
		t.Fatal("invalid body mutated head", v.ID)
	}
}
