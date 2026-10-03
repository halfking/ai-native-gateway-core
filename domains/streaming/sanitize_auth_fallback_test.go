package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Audit round 237 — cover prepareSanitizeRequest, which until this round had
// ZERO test references and no sanitize_auth_test.go at all.
//
// It is called from all three entry points (handler.go:1901, messages.go:78,
// responses.go:111) and is the only place that decides whether the sanitizer
// may run and, if so, under which tenant namespace.
//
// What is tested here is the DECISION, which is observable from outside the
// package. The tenant string it binds into the context is deliberately NOT
// asserted: the accessor (security/sanitize/input_protocols.go:24
// authenticatedTenant) is unexported, and inventing a mirror of it in this
// test would assert against a copy rather than the real thing. Round 237 pins
// the consequence of that string on the sanitize side instead — see
// security/sanitize/map_key_normalisation_test.go, which can reach the
// unexported sanitizeMapKey and therefore measures the real buckets.
func TestPrepareSanitizeRequest_StaticKeyGate(t *testing.T) {
	const staticKey = "sk-static-237"

	cases := []struct {
		name        string
		handler     *ChatHandler
		bearer      string
		wantAllowed bool
		why         string
	}{
		{
			name:        "无 verifier 且无静态密钥 = 本地模式，放行",
			handler:     &ChatHandler{},
			bearer:      "",
			wantAllowed: true,
			why:         "sanitize_auth.go:28-38 注释：local no-verifier mode has one fixed tenant",
		},
		{
			name:        "无 verifier + 正确的静态密钥 = 放行",
			handler:     &ChatHandler{staticDataPlaneKey: staticKey},
			bearer:      staticKey,
			wantAllowed: true,
			why:         "sanitize_auth.go:31-36",
		},
		{
			name:        "无 verifier + 错误的静态密钥 = 必须拒绝",
			handler:     &ChatHandler{staticDataPlaneKey: staticKey},
			bearer:      "sk-attacker-supplied",
			wantAllowed: false,
			why: "sanitize_auth.go:30 的原话：Reject before the sanitizer can persist " +
				"attacker-controlled mappings —— 这是本函数唯一的安全属性",
		},
		{
			name:        "无 verifier + 缺少 Bearer 头 = 必须拒绝",
			handler:     &ChatHandler{staticDataPlaneKey: staticKey},
			bearer:      "",
			wantAllowed: false,
			why:         "sanitize_auth.go:32-33 的 ConstantTimeCompare 对空串同样为假",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if tc.bearer != "" {
				r.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			got, allowed := tc.handler.prepareSanitizeRequest(r.WithContext(context.Background()))
			if allowed != tc.wantAllowed {
				t.Fatalf("allowed = %v, want %v（%s）", allowed, tc.wantAllowed, tc.why)
			}
			if !allowed && got == nil {
				t.Fatal("拒绝路径仍应返回原 request，不能返回 nil")
			}
		})
	}
}
