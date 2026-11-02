package admin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func unsetCookieDomainEnv(t *testing.T) {
	t.Helper()
	prev, had := os.LookupEnv("LLM_GATEWAY_COOKIE_DOMAIN")
	_ = os.Unsetenv("LLM_GATEWAY_COOKIE_DOMAIN")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("LLM_GATEWAY_COOKIE_DOMAIN", prev)
		} else {
			_ = os.Unsetenv("LLM_GATEWAY_COOKIE_DOMAIN")
		}
	})
}

func TestCookieDomain_EnvOverrideWins(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COOKIE_DOMAIN", ".example.com")
	req := httptest.NewRequest(http.MethodGet, "https://llm.itestu.cn/", nil)
	req.Host = "llm.itestu.cn"
	if got := cookieDomain(req); got != ".example.com" {
		t.Fatalf("got %q want .example.com", got)
	}
}

func TestCookieDomain_EmptyEnvDisablesHeuristic(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COOKIE_DOMAIN", "  ")
	req := httptest.NewRequest(http.MethodGet, "https://llm.itestu.cn/", nil)
	req.Host = "llm.itestu.cn"
	if got := cookieDomain(req); got != "" {
		t.Fatalf("blank override should disable, got %q", got)
	}
}

func TestCookieDomain_ItestuHeuristicWhenEnvAbsent(t *testing.T) {
	unsetCookieDomainEnv(t)
	cases := []struct {
		host string
		want string
	}{
		{"llm.itestu.cn", ".itestu.cn"},
		{"freellm.itestu.cn", ".itestu.cn"},
		{"LLM.ITESTU.CN", ".itestu.cn"},
		{"llm.itestu.cn:443", ".itestu.cn"},
		{"itestu.cn", ".itestu.cn"},
		{"localhost", ""},
		{"127.0.0.1:8781", ""},
		{"evil-itestu.cn", ""},
		{"notitestu.cn", ""},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "https://x/", nil)
		req.Host = tc.host
		if got := cookieDomain(req); got != tc.want {
			t.Fatalf("host=%q got %q want %q", tc.host, got, tc.want)
		}
	}
	if got := cookieDomain(nil); got != "" {
		t.Fatalf("nil request: got %q", got)
	}
}

// domainAttrOK accepts Go net/http's RFC6265 serialization which strips a
// leading dot from Cookie.Domain while still scoping to the eTLD+1.
func domainAttrOK(cDomain, raw, wantWithDot string) bool {
	wantBare := strings.TrimPrefix(wantWithDot, ".")
	if cDomain == wantWithDot || cDomain == wantBare {
		return true
	}
	return strings.Contains(raw, "Domain="+wantWithDot) || strings.Contains(raw, "Domain="+wantBare)
}

func TestSetSessionCookie_SetsDomainAttribute(t *testing.T) {
	unsetCookieDomainEnv(t)
	t.Setenv("LLM_GATEWAY_COOKIE_SECURE", "true")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://llm.itestu.cn/api/auth/token", nil)
	req.Host = "llm.itestu.cn"
	setSessionCookie(rec, req, "jwt-token", time.Now().Add(time.Hour))
	raw := rec.Header().Get("Set-Cookie")
	c := rec.Result().Cookies()[0]
	if !domainAttrOK(c.Domain, raw, ".itestu.cn") {
		t.Fatalf("Domain=%q raw=%s", c.Domain, raw)
	}
}

func TestClearSessionCookie_SetsDomainAttribute(t *testing.T) {
	unsetCookieDomainEnv(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://llm.itestu.cn/api/auth/logout", nil)
	req.Host = "llm.itestu.cn"
	clearSessionCookie(rec, req)
	raw := rec.Header().Get("Set-Cookie")
	c := rec.Result().Cookies()[0]
	if c.MaxAge >= 0 && !strings.Contains(raw, "Max-Age=0") {
		// Go may surface MaxAge=0 for deletion in Result().Cookies().
	}
	if c.MaxAge != -1 && c.MaxAge != 0 {
		t.Fatalf("clear MaxAge=%d raw=%s", c.MaxAge, raw)
	}
	if !domainAttrOK(c.Domain, raw, ".itestu.cn") {
		t.Fatalf("clear Domain=%q raw=%s", c.Domain, raw)
	}
}

func TestSetSessionCookieRemember_SetsDomain(t *testing.T) {
	unsetCookieDomainEnv(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://llm.itestu.cn/api/auth/token", nil)
	req.Host = "llm.itestu.cn"
	setSessionCookieRemember(rec, req, "jwt-token", time.Now().Add(24*time.Hour), false)
	raw := rec.Header().Get("Set-Cookie")
	c := rec.Result().Cookies()[0]
	if !domainAttrOK(c.Domain, raw, ".itestu.cn") {
		t.Fatalf("remember=false Domain=%q raw=%s", c.Domain, raw)
	}
}
