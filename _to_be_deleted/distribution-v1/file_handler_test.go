package distribution

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestFileHandlerServeRequiresTicket(t *testing.T) {
	dir := t.TempDir()
	h := NewFileHandler(dir, NewTicketSigner([]byte("test-secret"), 0), nil)
	e := echo.New()
	h.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodGet, "/llm-gateway-go/v1.0.0/pkg.tar.gz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestFileHandlerServeWithValidTicket(t *testing.T) {
	dir := t.TempDir()
	version := "v2.4.6"
	name := artifactFileName(version, "linux", "amd64")
	artifactPath := filepath.Join(dir, version, name)
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("offline-package")
	if err := os.WriteFile(artifactPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	signer := NewTicketSigner([]byte("test-secret"), 0)
	token, _, err := signer.Issue("req-1", version, "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}

	h := NewFileHandler(dir, signer, nil)
	e := echo.New()
	h.RegisterRoutes(e)

	req := httptest.NewRequest(http.MethodGet, "/llm-gateway-go/"+version+"/"+name+"?ticket="+token, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Download-Request-ID") != "req-1" {
		t.Fatalf("missing request id header")
	}
}
