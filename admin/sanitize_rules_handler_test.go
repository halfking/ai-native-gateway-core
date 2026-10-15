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
func TestSanitizeRuleEventsAreTenantScopedAndReviewLinked(t *testing.T) {
	h, s := sanitizeAPIHandler(t)
	ctx := context.Background()
	for _, tenant := range []string{"a", "b"} {
		if err := s.RecordEvent(ctx, sanitizerules.Event{TenantID: tenant, RuleID: "secret", Version: 1, Phase: "input", Action: "mask"}, 100); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := s.ListEvents(ctx, sanitizerules.EventFilter{})
	var a, b sanitizerules.Event
	for _, e := range all {
		if e.TenantID == "a" {
			a = e
		} else {
			b = e
		}
	}
	w := sanitizeAPIRequest(t, h, "GET", "/api/admin/sanitize-rules/events?tenant_id=b&limit=20", "tenant_admin", "a", "")
	var rows struct {
		Events []sanitizerules.Event `json:"events"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows.Events) != 1 || rows.Events[0].TenantID != "a" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = sanitizeAPIRequest(t, h, "POST", "/api/admin/sanitize-rules/events/"+strconv.FormatInt(b.ID, 10)+"/review?tenant_id=b", "tenant_admin", "a", `{"status":"false_positive","comment":"cross tenant"}`)
	if w.Code != 404 {
		t.Fatal("cross tenant review", w.Code, w.Body.String())
	}
	w = sanitizeAPIRequest(t, h, "POST", "/api/admin/sanitize-rules/events/"+strconv.FormatInt(a.ID, 10)+"/review", "tenant_admin", "a", `{"status":"false_positive","comment":"scoped fix"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	raw, _ := json.Marshal(sanitizeAPIConfig())
	w = sanitizeAPIRequest(t, h, "POST", "/api/admin/sanitize-rules/publish", "super_admin", "default", `{"base_version":1,"config":`+string(raw)+`,"review_event_ids":[`+strconv.FormatInt(a.ID, 10)+`]}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	events, _ := s.ListEvents(ctx, sanitizerules.EventFilter{TenantID: "a"})
	if events[0].ResolvedVersion != 2 || events[0].Reviewer != "operator" {
		t.Fatal(events)
	}
}

type sanitizeAPIFailingStore struct{ sanitizerules.Store }

func (s sanitizeAPIFailingStore) ListVersions(context.Context, int) ([]sanitizerules.Version, error) {
	return nil, errors.New("postgres password=private-secret")
}
func TestSanitizeRuleErrorsAndMethodsAreSafe(t *testing.T) {
	h, s := sanitizeAPIHandler(t)
	m, _ := sanitizerules.NewManager(sanitizeAPIFailingStore{s}, sanitizeAPIConfig(), sanitizerules.Options{InstanceID: "error"})
	h.SetSanitizeRuleManager(m)
	w := sanitizeAPIRequest(t, h, "GET", "/api/admin/sanitize-rules/versions", "super_admin", "default", "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "private-secret") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = sanitizeAPIRequest(t, h, "PUT", "/api/admin/sanitize-rules", "super_admin", "default", "")
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
	w = sanitizeAPIRequest(t, h, "GET", "/api/admin/sanitize-rules/unknown", "super_admin", "default", "")
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestSanitizeManagedReloadRejectsBeforeSensitiveWordsChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "words.json")
	_ = os.WriteFile(path, []byte(`{"categories":{"a":{"name":"Original","words":["original"]}}}`), 0600)
	engine := sensitive.NewSensitiveWordEngine()
	if err := engine.BuildFromFile(path); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(path, []byte(`{"categories":{"b":{"name":"Changed","words":["changed"]}}}`), 0600)
	detector := sanitize.NewPatternDetector()
	m, _ := sanitizerules.NewManager(sanitizerules.NewMemoryStore(), sanitizeAPIConfig(), sanitizerules.Options{InstanceID: "reload"})
	detector.SetRuleManager(m)
	h := NewSensitiveWordsHandler(engine)
	h.SetPatternDetector(detector)
	w := httptest.NewRecorder()
	h.handleReload(w, httptest.NewRequest("POST", "/api/admin/sensitive-words/reload", nil))
	if w.Code != 409 || len(engine.Match("original")) != 1 || len(engine.Match("changed")) != 0 {
		t.Fatal("reload partially changed sensitive words", w.Code, w.Body.String())
	}
}
func TestSanitizeRuleValidationExplainsFieldWithoutEchoingSecret(t *testing.T) {
	h, _ := sanitizeAPIHandler(t)
	cfg := sanitizeAPIConfig()
	cfg.Rules[0].Pattern = "(private-secret"
	body, _ := json.Marshal(cfg)
	w := sanitizeAPIRequest(t, h, "POST", "/api/admin/sanitize-rules/validate", "super_admin", "default", string(body))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "rules[0].pattern") || strings.Contains(w.Body.String(), "private-secret") {
		t.Fatal("validation lacks safe field diagnostic", w.Code, w.Body.String())
	}
}
func TestSanitizeRuleJWTAuthorizationUsesAuthenticatedTenant(t *testing.T) {
	h, s := sanitizeAPIHandler(t)
	_ = s.RecordEvent(context.Background(), sanitizerules.Event{TenantID: "a", RuleID: "secret", Version: 1, Phase: "input", Action: "mask"}, 100)
	_ = s.RecordEvent(context.Background(), sanitizerules.Event{TenantID: "b", RuleID: "secret", Version: 1, Phase: "input", Action: "mask"}, 100)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	for _, tc := range []struct {
		role, path string
		code       int
	}{{"tenant_admin", "/api/admin/sanitize-rules", 403}, {"tenant_admin", "/api/admin/sanitize-rules/events?tenant_id=b", 200}, {"user", "/api/admin/sanitize-rules/events", 403}, {"super_admin", "/api/admin/sanitize-rules", 200}} {
		token, _, err := SignToken(42, "a", "operator", tc.role, h.secret, false)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d %s", tc.role, tc.path, w.Code, w.Body.String())
		}
		if tc.code == 200 && tc.role == "tenant_admin" && strings.Contains(w.Body.String(), `"tenant_id":"b"`) {
			t.Fatal("JWT tenant escaped")
		}
	}
}
func TestSanitizeRuleReviewInputAndMissingLinkFailures(t *testing.T) {
	h, s := sanitizeAPIHandler(t)
	ctx := context.Background()
	_ = s.RecordEvent(ctx, sanitizerules.Event{TenantID: "a", RuleID: "secret", Version: 1, Phase: "input", Action: "mask"}, 100)
	events, _ := s.ListEvents(ctx, sanitizerules.EventFilter{TenantID: "a"})
	id := strconv.FormatInt(events[0].ID, 10)
	for _, tc := range []struct {
		path, body string
		code       int
	}{{"/events/" + id + "/review", `{"status":"allow_forever"}`, 400}, {"/events/" + id + "/review", `{"status":"false_positive","tenant_id":"b"}`, 400}, {"/events/-1/review", `{"status":"ignored"}`, 400}, {"/events/999/review", `{"status":"ignored"}`, 404}, {"/events/1/extra/review", `{"status":"ignored"}`, 404}} {
		w := sanitizeAPIRequest(t, h, "POST", "/api/admin/sanitize-rules"+tc.path, "tenant_admin", "a", tc.body)
		if w.Code != tc.code {
			t.Errorf("review %s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	raw, _ := json.Marshal(sanitizeAPIConfig())
	for _, tc := range []struct {
		ids  string
		code int
	}{{`[` + id + `]`, 400}, {`[999]`, 404}, {`[1,1]`, 400}, {`[-1]`, 400}} {
		w := sanitizeAPIRequest(t, h, "POST", "/api/admin/sanitize-rules/publish", "super_admin", "default", `{"base_version":1,"config":`+string(raw)+`,"review_event_ids":`+tc.ids+`}`)
		if w.Code != tc.code {
			t.Errorf("ids=%s: %d %s", tc.ids, w.Code, w.Body.String())
		}
	}
	head, _ := s.Current(ctx)
	if head.ID != 1 {
		t.Fatal("rejected publication changed head")
	}
}

func TestSanitizeRulesModuleExposesParametersAndPage(t *testing.T) {
	old := settings.Global
	t.Cleanup(func() { settings.Global = old })
	registry := settings.NewRegistry()
	for _, spec := range settings.SanitizeRulesSpecs() {
		if err := registry.RegisterSpec(spec); err != nil {
			t.Fatal(err)
		}
	}
	settings.Global = registry
	h := &Handler{}
	w := httptest.NewRecorder()
	h.handleModulesGet(w, httptest.NewRequest("GET", "/api/admin/modules/output_compliance", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body struct {
		Module ModuleWithStatus `json:"module"`
		Config map[string]any   `json:"config"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{settings.KeySanitizeRulesRefreshIntervalSeconds, settings.KeySanitizeRulesAuditEnabled, settings.KeySanitizeRulesEventLimit} {
		if _, ok := body.Config[key]; !ok {
			t.Errorf("module parameter missing %s", key)
		}
	}
	if body.Module.DocsURL != "/admin/sanitize-rules" {
		t.Fatal("rule page entry missing", body.Module.DocsURL)
	}
}
