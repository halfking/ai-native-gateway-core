package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"golang.org/x/crypto/bcrypt"
)

// TestHandleLogin_DirectDB_Success tests the legacy direct DB authentication path
// This verifies backward compatibility when registry is nil
func TestHandleLogin_DirectDB_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock pool: %v", err)
	}
	defer mock.Close()

	testPassword := "Test123!@#"
	passwordHash, _ := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.DefaultCost)

	// Mock user query
	mock.ExpectQuery(`SELECT id, tenant_id, username, password_hash`).
		WithArgs("testuser").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "tenant_id", "username", "password_hash", "display_name",
			"email", "role", "roles", "enabled", "must_change_password",
		}).AddRow(
			1, "default", "testuser", string(passwordHash), "Test User",
			"test@example.com", "user", []string{"user"}, true, false,
		))

	// Mock last_login_at update
	mock.ExpectExec(`UPDATE users SET last_login_at`).
		WithArgs(1).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Create handler WITHOUT registry (nil) - tests legacy path
	h := &Handler{
		loginDB:       mock,
		secret:   "test-secret",
		registry: nil, // Explicitly nil to test fallback
	}

	// Create login request
	reqBody := map[string]interface{}{
		"username": "testuser",
		"password": testPassword,
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.RemoteAddr = "192.168.1.1:12345"
	rec := httptest.NewRecorder()

	// Execute
	h.handleLogin(rec, req)

	// Assert
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if _, ok := resp["access_token"]; !ok {
		t.Error("response missing access_token")
	}
	if resp["token_type"] != "Bearer" {
		t.Errorf("expected token_type Bearer, got %v", resp["token_type"])
	}

	user, ok := resp["user"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing user object")
	}
	if user["username"] != "testuser" {
		t.Errorf("expected username testuser, got %v", user["username"])
	}

	// Verify all expectations met
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %v", err)
	}
}

// TestHandleLogin_DirectDB_InvalidCredentials tests error handling
func TestHandleLogin_DirectDB_InvalidCredentials(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock pool: %v", err)
	}
	defer mock.Close()

	passwordHash, _ := bcrypt.GenerateFromPassword([]byte("correctpassword"), bcrypt.DefaultCost)

	// Mock user query - user exists
	mock.ExpectQuery(`SELECT id, tenant_id, username, password_hash`).
		WithArgs("testuser").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "tenant_id", "username", "password_hash", "display_name",
			"email", "role", "roles", "enabled", "must_change_password",
		}).AddRow(
			1, "default", "testuser", string(passwordHash), "Test User",
			"test@example.com", "user", []string{"user"}, true, false,
		))

	h := &Handler{
		loginDB:       mock,
		secret:   "test-secret",
		registry: nil,
	}

	// Create login request with wrong password
	reqBody := map[string]interface{}{
		"username": "testuser",
		"password": "wrongpassword",
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.RemoteAddr = "192.168.1.2:12345"
	rec := httptest.NewRecorder()

	// Execute
	h.handleLogin(rec, req)

	// Assert
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %v", err)
	}
}

// TestHandleLogin_DirectDB_UserDisabled tests disabled user handling
func TestHandleLogin_DirectDB_UserDisabled(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock pool: %v", err)
	}
	defer mock.Close()

	passwordHash, _ := bcrypt.GenerateFromPassword([]byte("Test123!@#"), bcrypt.DefaultCost)

	// Mock user query - user disabled
	mock.ExpectQuery(`SELECT id, tenant_id, username, password_hash`).
		WithArgs("disableduser").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "tenant_id", "username", "password_hash", "display_name",
			"email", "role", "roles", "enabled", "must_change_password",
		}).AddRow(
			2, "default", "disableduser", string(passwordHash), "Disabled User",
			"disabled@example.com", "user", []string{"user"}, false, false, // enabled=false
		))

	h := &Handler{
		loginDB:       mock,
		secret:   "test-secret",
		registry: nil,
	}

	reqBody := map[string]interface{}{
		"username": "disableduser",
		"password": "Test123!@#",
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.RemoteAddr = "192.168.1.3:12345"
	rec := httptest.NewRecorder()

	// Execute
	h.handleLogin(rec, req)

	// Assert
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d: %s", rec.Code, rec.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %v", err)
	}
}

// TestHandleLogin_MethodNotAllowed tests HTTP method validation
func TestHandleLogin_MethodNotAllowed(t *testing.T) {
	h := &Handler{
		db:       nil,
		secret:   "test-secret",
		registry: nil,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	rec := httptest.NewRecorder()

	h.handleLogin(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", rec.Code)
	}
}

// TestHandleLogin_DatabaseNotConfigured tests nil DB handling
func TestHandleLogin_DatabaseNotConfigured(t *testing.T) {
	h := &Handler{
		db:       nil,
		secret:   "test-secret",
		registry: nil,
	}

	reqBody := map[string]interface{}{
		"username": "test",
		"password": "test",
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.handleLogin(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rec.Code)
	}
}

// TestHandleLogin_InvalidRequestBody tests JSON parsing error handling
func TestHandleLogin_InvalidRequestBody(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	h := &Handler{
		loginDB:       mock,
		secret:   "test-secret",
		registry: nil,
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader([]byte("invalid json")))
	req.RemoteAddr = "192.168.1.4:12345"
	rec := httptest.NewRecorder()

	h.handleLogin(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

