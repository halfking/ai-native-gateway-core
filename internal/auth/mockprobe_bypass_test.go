package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func bearerReq(method, path, token string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

// TestIsMockProbeClient 覆盖命中/未命中矩阵（大小写、空白、非 Bearer、
// 错误 token、缺头、nil 请求）。
func TestIsMockProbeClient(t *testing.T) {
	cases := []struct {
		name  string
		setup func() *http.Request
		want  bool
	}{
		{"exact token", func() *http.Request { return bearerReq("POST", "/mock/v1/chat/completions/fast", MockProbeClientToken) }, true},
		{"case-insensitive scheme", func() *http.Request {
			r := httptest.NewRequest("POST", "/mock/", nil)
			r.Header.Set("Authorization", "bearer "+MockProbeClientToken)
			return r
		}, true},
		{"padded token", func() *http.Request {
			r := httptest.NewRequest("POST", "/mock/", nil)
			r.Header.Set("Authorization", "Bearer  "+MockProbeClientToken+" ")
			return r
		}, true},
		{"wrong token", func() *http.Request { return bearerReq("POST", "/mock/", "sk-real-key") }, false},
		{"no header", func() *http.Request { return httptest.NewRequest("POST", "/mock/", nil) }, false},
		{"raw token without bearer", func() *http.Request {
			r := httptest.NewRequest("POST", "/mock/", nil)
			r.Header.Set("Authorization", MockProbeClientToken)
			return r
		}, false},
		{"basic auth", func() *http.Request {
			r := httptest.NewRequest("POST", "/mock/", nil)
			r.Header.Set("Authorization", "Basic dXNlcjpwdw==")
			return r
		}, false},
		{"nil request", func() *http.Request { return nil }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsMockProbeClient(c.setup()); got != c.want {
				t.Fatalf("IsMockProbeClient = %v, want %v", got, c.want)
			}
		})
	}
}

// TestEnforceMockProbeScope 白名单语义：两个 mock 供应商放行，其余
// （含前缀伪造）拒绝。
func TestEnforceMockProbeScope(t *testing.T) {
	for _, ok := range []string{"mock-fast", "mock-slow"} {
		if !EnforceMockProbeScope(ok) {
			t.Errorf("EnforceMockProbeScope(%q) should be true", ok)
		}
	}
	for _, bad := range []string{"", "mock-fastx", "openai", "mock-", "Mock-Fast"} {
		if EnforceMockProbeScope(bad) {
			t.Errorf("EnforceMockProbeScope(%q) should be false", bad)
		}
	}
}

// TestMockEndpointGuard 端点守卫：POST+正确 token+白名单 supplier → 200；
// GET → 405；缺/错 token → 401；越权 supplier → 403。
func TestMockEndpointGuard(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mock-ok"))
	})
	h := MockEndpoint("mock-fast", upstream)

	t.Run("allowed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearerReq("POST", "/mock/v1/chat/completions/fast", MockProbeClientToken))
		if rec.Code != http.StatusOK || rec.Body.String() != "mock-ok" {
			t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
		}
	})
	t.Run("method not allowed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearerReq("GET", "/mock/v1/chat/completions/fast", MockProbeClientToken))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET should 405, got %d", rec.Code)
		}
	})
	t.Run("unauthorized", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearerReq("POST", "/mock/v1/chat/completions/fast", ""))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("no token should 401, got %d", rec.Code)
		}
	})
	t.Run("forbidden supplier", func(t *testing.T) {
		bad := MockEndpoint("mock-evil", upstream)
		rec := httptest.NewRecorder()
		bad(rec, bearerReq("POST", "/mock/v1/chat/completions/evil", MockProbeClientToken))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("out-of-scope supplier should 403, got %d", rec.Code)
		}
	})
}

// TestMockProbeBypass 装配语义：enabled+mock 请求绕过 API-key 门；
// 非 mock 路径/mock 请求但 disabled/无 token 请求仍走原鉴权（401）。
func TestMockProbeBypass(t *testing.T) {
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mux"))
	})
	apiGate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("gate"))
	})

	t.Run("bypass when enabled", func(t *testing.T) {
		h := MockProbeBypass(true, apiGate, mux)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearerReq("POST", "/mock/v1/chat/completions/fast", MockProbeClientToken))
		if rec.Code != http.StatusOK || rec.Body.String() != "mux" {
			t.Fatalf("should bypass to mux: code=%d body=%q", rec.Code, rec.Body.String())
		}
	})
	t.Run("no bypass when disabled", func(t *testing.T) {
		h := MockProbeBypass(false, apiGate, mux)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearerReq("POST", "/mock/v1/chat/completions/fast", MockProbeClientToken))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("disabled should hit api gate: got %d", rec.Code)
		}
	})
	t.Run("non-mock path goes through gate", func(t *testing.T) {
		h := MockProbeBypass(true, apiGate, mux)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearerReq("POST", "/v1/chat/completions", MockProbeClientToken))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("non-mock path should hit gate: got %d", rec.Code)
		}
	})
	t.Run("mock path without token hits gate", func(t *testing.T) {
		h := MockProbeBypass(true, apiGate, mux)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearerReq("POST", "/mock/v1/chat/completions/fast", "sk-other"))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong token should hit gate: got %d", rec.Code)
		}
	})
}
