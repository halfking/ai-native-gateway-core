package admin

import (
	"errors"
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

func TestTenantFromQueryOrContextPinsNonPrivilegedRoles(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/snapshot?tenant=other", nil)
	req.Header.Set("X-Tenant-ID", "header-other")
	req = SetAuthContext(req, &AuthContext{TenantID: "tenant-a", Role: "viewer", IsJWT: true})
	if got := tenantFromQueryOrContext(req); got != "tenant-a" {
		t.Fatalf("viewer tenant override must be ignored, got %q", got)
	}

	superReq := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/snapshot?tenant=other", nil)
	superReq = SetAuthContext(superReq, &AuthContext{TenantID: "tenant-a", Role: "super_admin", IsJWT: true})
	if got := tenantFromQueryOrContext(superReq); got != "other" {
		t.Fatalf("super admin explicit tenant selection must be retained, got %q", got)
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

func TestValidateCursor_CrossTenantRejected(t *testing.T) {
	key := []byte("test-key")
	p := cursorPayload{TenantID: "tenant-a", SessionID: "s1", TurnNo: 42, TS: time.Now()}
	encoded, err := encodeCursor(p, key)
	if err != nil {
		t.Fatal(err)
	}
	// 当前请求租户为 tenant-b，cursor 归属 tenant-a → 必须拒绝
	_, err = validateCursor(encoded, key, "tenant-b", "")
	if !errors.Is(err, errCursorMismatch) {
		t.Fatalf("expected errCursorMismatch for cross-tenant cursor, got: %v", err)
	}
}

func TestValidateCursor_CrossSessionRejected(t *testing.T) {
	key := []byte("test-key")
	p := cursorPayload{TenantID: "tenant-a", SessionID: "s1", TurnNo: 7, TS: time.Now()}
	encoded, err := encodeCursor(p, key)
	if err != nil {
		t.Fatal(err)
	}
	// 会话维度校验：cursor 归属 s1，请求 s2 → 必须拒绝
	_, err = validateCursor(encoded, key, "tenant-a", "s2")
	if !errors.Is(err, errCursorMismatch) {
		t.Fatalf("expected errCursorMismatch for cross-session cursor, got: %v", err)
	}
	// 会话维度跳过（sessionID==""）：仅校验租户 → 允许
	if _, err = validateCursor(encoded, key, "tenant-a", ""); err != nil {
		t.Fatalf("expected ok when session check skipped, got: %v", err)
	}
}

func TestValidateCursor_InvalidSignature(t *testing.T) {
	key := []byte("test-key")
	p := cursorPayload{TenantID: "tenant-a", SessionID: "s1", TurnNo: 1, TS: time.Now()}
	encoded, err := encodeCursor(p, key)
	if err != nil {
		t.Fatal(err)
	}
	// 错误 key 解码失败 → 应返回解码错误而非 mismatch
	_, err = validateCursor(encoded, []byte("wrong-key"), "tenant-a", "")
	if errors.Is(err, errCursorMismatch) {
		t.Fatal("expected decode error (bad signature), not cursor mismatch")
	}
	if err == nil {
		t.Fatal("expected error with wrong key")
	}
}
