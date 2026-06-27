package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleChangePassword_RequiresJWT(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPut, "/api/auth/change-password", strings.NewReader(`{"old_password":"OldPass123","new_password":"NewPass123"}`))
	rec := httptest.NewRecorder()

	h.handleChangePassword(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "jwt authentication required") {
		t.Fatalf("expected jwt auth error, got %s", rec.Body.String())
	}
}

func TestHandleChangePassword_RejectsShortPasswordBeforeDB(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPut, "/api/auth/change-password", strings.NewReader(`{"old_password":"OldPass123","new_password":"short"}`))
	req = SetAuthContext(req, &AuthContext{UserID: 7, TenantID: "tenant-a", Username: "alice", Role: "tenant_admin", IsJWT: true})
	rec := httptest.NewRecorder()

	h.handleChangePassword(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "new password must be at least 8 characters") {
		t.Fatalf("expected short password error, got %s", rec.Body.String())
	}
}

func TestHandleChangePassword_RejectsInvalidJSON(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPut, "/api/auth/change-password", strings.NewReader(`{"old_password":`))
	req = SetAuthContext(req, &AuthContext{UserID: 7, TenantID: "tenant-a", Username: "alice", Role: "tenant_admin", IsJWT: true})
	rec := httptest.NewRecorder()

	h.handleChangePassword(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid request body") {
		t.Fatalf("expected invalid request body error, got %s", rec.Body.String())
	}
}

func TestHandleUsersPasswordRoute_RejectsInvalidUserID(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPut, "/api/users/not-a-number/password", strings.NewReader(`{"password":"ValidPass123"}`))
	rec := httptest.NewRecorder()

	h.handleUsers(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid user id") {
		t.Fatalf("expected invalid user id error, got %s", rec.Body.String())
	}
}

func TestHandleUsersPasswordRoute_RejectsNonPUT(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/api/users/12/password", strings.NewReader(`{"password":"ValidPass123"}`))
	rec := httptest.NewRecorder()

	h.handleUsers(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "method not allowed") {
		t.Fatalf("expected method not allowed error, got %s", rec.Body.String())
	}
}

func TestResetUserPassword_RejectsShortPasswordBeforeDB(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPut, "/api/users/12/password", strings.NewReader(`{"password":"short"}`))
	rec := httptest.NewRecorder()

	h.resetUserPassword(rec, req, 12)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "password must be at least 8 characters") {
		t.Fatalf("expected short password error, got %s", rec.Body.String())
	}
}

func TestResetUserPassword_RejectsWeakPasswordBeforeDB(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPut, "/api/users/12/password", strings.NewReader(`{"password":"alllowercase123"}`))
	rec := httptest.NewRecorder()

	h.resetUserPassword(rec, req, 12)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "uppercase") {
		t.Fatalf("expected complexity error, got %s", rec.Body.String())
	}
}

func TestResetUserPassword_RejectsInvalidJSON(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPut, "/api/users/12/password", strings.NewReader(`{"password":`))
	rec := httptest.NewRecorder()

	h.resetUserPassword(rec, req, 12)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid request body") {
		t.Fatalf("expected invalid request body error, got %s", rec.Body.String())
	}
}
