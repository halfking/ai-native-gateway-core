package admin

import (
	"net/http/httptest"
	"testing"
)

func TestHandleLogoutClearsCookieWithRequestSecurity(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COOKIE_SECURE", "")

	req := httptest.NewRequest("POST", "http://127.0.0.1:8782/api/auth/logout", nil)
	response := httptest.NewRecorder()
	(&Handler{}).handleLogout(response, req)

	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one session cookie deletion, got %d", len(cookies))
	}
	if cookies[0].Secure {
		t.Fatal("logout cookie security must match an HTTP loopback session cookie")
	}
	if cookies[0].Name != CookieName || cookies[0].Path != "/" || cookies[0].MaxAge != -1 {
		t.Fatalf("unexpected session cookie deletion attributes: %#v", cookies[0])
	}
}
