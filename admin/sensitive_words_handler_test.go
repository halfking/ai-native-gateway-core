package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/security/sensitive"
)

type reloadablePatternDetector struct {
	reloadErr error
	called    bool
}

func (d *reloadablePatternDetector) ReloadFromFile() error {
	d.called = true
	return d.reloadErr
}

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
	missingPath := filepath.Join(t.TempDir(), "missing-sensitive_words.json")
	if err := engine.BuildFromFile(missingPath); err == nil {
		t.Fatal("BuildFromFile should fail")
	}

	h := NewSensitiveWordsHandler(engine)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sensitive-words/reload", nil)
	res := httptest.NewRecorder()
	h.handleReload(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("reload status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
	if strings.Contains(res.Body.String(), missingPath) {
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

func TestSensitiveWordsHandlerReloadsPatternDetector(t *testing.T) {
	engine := sensitive.NewSensitiveWordEngine()
	path := filepath.Join(t.TempDir(), "sensitive_words.json")
	if err := os.WriteFile(path, []byte(`{"categories":{}}`), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}
	if err := engine.BuildFromFile(path); err != nil {
		t.Fatalf("BuildFromFile failed: %v", err)
	}
	patternDetector := &reloadablePatternDetector{reloadErr: errors.New("invalid yaml")}
	h := NewSensitiveWordsHandler(engine)
	h.SetPatternDetector(patternDetector)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/sensitive-words/reload", nil)
	res := httptest.NewRecorder()
	h.handleReload(res, req)

	if !patternDetector.called {
		t.Fatal("pattern detector was not reloaded")
	}
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("reload status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
}
