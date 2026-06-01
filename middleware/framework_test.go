package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBypassRuleExactPathDoesNotMatchPrefix(t *testing.T) {
	rule := &BypassRule{ExactPaths: []string{"/"}}
	req := httptest.NewRequest(http.MethodGet, "/admin/config/reload", nil)
	if rule.Match(req) {
		t.Fatal("exact path '/' should not match '/admin/config/reload'")
	}
}

func TestBypassRulePrefixStillMatchesNestedPath(t *testing.T) {
	rule := &BypassRule{PathPrefixes: []string{"/healthz"}}
	req := httptest.NewRequest(http.MethodGet, "/healthz/live", nil)
	if !rule.Match(req) {
		t.Fatal("path prefix should match nested health path")
	}
}
