package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/security/sensitive"
)

func TestSensitiveWordsHandlerMatch(t *testing.T) {
	engine := sensitive.NewSensitiveWordEngine()
	if err := engine.Build(&sensitive.SensitiveWordConfig{Categories: map[string]sensitive.CategoryConf{
		"test": {Name: "Test", Words: []string{"secret"}},
	}}); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	mux := http.NewServeMux()
	NewSensitiveWordsHandler(engine).RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return next
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sensitive-words/match", strings.NewReader(`{"text":"secret"}`))
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)

	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"matched":true`) {
		t.Fatalf("match response = %d %s", res.Code, res.Body.String())
	}
}

func TestSensitiveWordsHandlerReloadErrorDoesNotLeakPath(t *testing.T) {
	engine := sensitive.NewSensitiveWordEngine()
	if err := engine.BuildFromFile("/path/that/does/not/exist/sensitive_words.json"); err == nil {
		t.Fatal("BuildFromFile should fail")
	}

	h := NewSensitiveWordsHandler(engine)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sensitive-words/reload", nil)
	res := httptest.NewRecorder()
	h.handleReload(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("reload status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
	if strings.Contains(res.Body.String(), "/path/that/does/not/exist") {
		t.Fatalf("reload response leaked filesystem path: %s", res.Body.String())
	}
}

func TestSensitiveWordsHandlerRejectsWrongMethod(t *testing.T) {
	engine := sensitive.NewSensitiveWordEngine()
	h := NewSensitiveWordsHandler(engine)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sensitive-words/match", nil)
	res := httptest.NewRecorder()
	h.handleMatch(res, req)

	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("match method status = %d, want %d", res.Code, http.StatusMethodNotAllowed)
	}
}
