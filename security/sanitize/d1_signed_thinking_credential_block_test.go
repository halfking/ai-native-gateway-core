package sanitize

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ★ D1（2026-11-19 审计轮）签名 thinking 分级阻断钉测。
//
// 契约：
//
//	· 强凭据形态（厂商密钥 / JWT / 长不透明令牌）⇒ **拒整请求(400)**
//	  —— `signature` 字段完全由客户端控制，客户端可伪造带签名的 thinking
//	  块把真凭据夹带出网；对不验签的上游等于无条件脱敏旁路。
//	· 散文级弱命中 ⇒ 仍原样放行（保持 r1058 的「不砖化会话」结论）。
//	· 判据取**窄**：模型散文里的 commit hash / UUID 不是凭据，误判即砖化。

// TestStrongCredentialCandidate 钉住强凭据判据的边界。
func TestStrongCredentialCandidate(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		// ── 强凭据：厂商前缀 ──
		{"anthropic key", "sk-ant-api03-x7Kq2mW9eLbT4vRrN8zA1cD5fGhJ6uM", true},
		{"anthropic 短样例", "sk-ant-api03-XYZ", true},
		{"openai proj key", "sk-proj-abc123DEF456ghi789JKL012", true},
		{"github token", "ghp_16C7e42F292c6912E7710c838347Ae178B4a", true},
		{"gitlab token", "glpat-ABCdef123456789", true},
		{"slack token", "xoxb-123456789012-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx", true},
		{"google api key", "AIzaSyD-1234567890abcdefghijklmnopqrstu", true},
		{"aws access key", "AKIAIOSFODNN7EXAMPLE", true},
		{"裸 sk- 长且含数字", "sk-abcdefghij1234567890", true},
		// ── 强凭据：JWT / 长不透明令牌 ──
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", true},
		{"长不透明令牌", "aB3xY9zQ1wE7rT5yU2iO4pL6kJ8hG0fD2sA4qW6eR8tY0uI2oP4", true},
		// ── 散文级：不得误判（误判＝会话砖化） ──
		{"r1058 事故形态", "token", false},
		{"r1058 弱命中形态", "MySecretPass123", false},
		{"普通英文词", "authentication", false},
		{"短 sk- 词", "sk-test", false},
		{"40 位 commit hash", "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", false},
		{"uuid", "550e8400-e29b-41d4-a716-446655440000", false},
		{"空串", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := strongCredentialCandidate(tc.value); got != tc.want {
				t.Fatalf("strongCredentialCandidate(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestD1SignedThinkingCredentialRejected 端到端：带签名的 thinking 块里
// 夹带厂商密钥 ⇒ 整请求被拒（400），下游 handler **不得**被调用，
// 且请求体不得出网。
func TestD1SignedThinkingCredentialRejected(t *testing.T) {
	configured, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSanitizer(configured)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewSanitizeInputMiddleware(s, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	body := `{"model":"claude-sonnet-5-5","stream":true,"messages":[` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"上次用的 key 是 sk-ant-api03-x7Kq2mW9eLbT4vRrN8zA1cD5fGhJ6uM","signature":"sig_forged_by_client"}]}]}`

	var nextCalled bool
	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("X-Session-Id", "gw_d1_credential_test")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("带凭据的签名 thinking 必须被拒（400），got status=%d body=%s", rec.Code, rec.Body.String())
	}
	if nextCalled {
		t.Fatal("请求被拒时下游 handler 不得被调用（凭据不得出网）")
	}
	if strings.Contains(rec.Body.String(), "sk-ant-api03") {
		t.Fatalf("错误响应不得回显凭据：%s", rec.Body.String())
	}
}

// TestD1SignedThinkingProseStillPasses 负控：散文级弱命中仍放行，
// body 逐字未动（保持 r1058 的「不砖化会话」结论）。
//
// 这一条是 r1058 事故的回归门：一旦它转红，就意味着又一轮会话砖化债。
func TestD1SignedThinkingProseStillPasses(t *testing.T) {
	configured, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSanitizer(configured)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewSanitizeInputMiddleware(s, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	body := `{"model":"claude-sonnet-5-5","stream":true,"messages":[` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"line 38: Bearer token 检查；password=MySecretPass123","signature":"sig_abc123"}]}]}`

	var nextCalled bool
	var nextBody string
	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		b, _ := io.ReadAll(r.Body)
		nextBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("X-Session-Id", "gw_d1_prose_test")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("散文级弱命中不得拒（否则重演 r1058 会话砖化），got status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !nextCalled {
		t.Fatal("下游 handler 必须被调用")
	}
	if nextBody != body {
		t.Fatalf("body 被改写：\n got: %s\nwant: %s", nextBody, body)
	}
}
