package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionTurnsHandler_ListByCursor(t *testing.T) {
	h := NewSessionTurnsHandler(nil, nil)
	r := h.Routes()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/gw_test/turns?limit=10", nil)
	req.Header.Set("X-Test-Admin", "1")
	req.Header.Set("X-Tenant-ID", "default")
	req.Header.Set("X-User-ID", "tester")
	req.Header.Set("X-Role", "super_admin")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 with nil pool, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSessionTurnsHandler_RoutesIncludesSnapshot(t *testing.T) {
	h := NewSessionTurnsHandler(nil, nil)
	r := h.Routes()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/gw_test/snapshot", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCursorRoundTrip(t *testing.T) {
	p := cursorPayload{TenantID: "t", SessionID: "s", TurnNo: 42, TS: time.Now()}
	encoded, err := encodeCursor(p, []byte("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeCursor(encoded, []byte("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.TurnNo != 42 || decoded.SessionID != "s" {
		t.Fatalf("round-trip mismatch: %+v", decoded)
	}
	if _, err = decodeCursor(encoded, []byte("wrong")); err == nil {
		t.Fatal("expected error with wrong key")
	}
}
