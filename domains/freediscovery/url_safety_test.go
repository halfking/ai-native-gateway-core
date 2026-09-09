package freediscovery

import (
	"strings"
	"testing"
)

func TestIsValidBaseURL(t *testing.T) {
	good := []string{
		"https://api.groq.com/openai/v1",
		"http://localhost:8080/v1",
		"https://generativelanguage.googleapis.com/v1beta",
		"https://api.example.com:443/v1",
	}
	bad := []struct {
		in   string
		want string // 期望错误信息片段; 空表示通过
	}{
		{"", "required"},
		{"ftp://api.x.com", "must use http"},
		{"//api.x.com/v1", "must use http"},     // 无 scheme
		{"javascript:alert(1)", "must use http"}, // 不允许 scheme
		{"https://user:pass@api.x.com/v1", "userinfo"},
		{"https://api.x.com/v1#frag", "fragment"},
		{"https:// api.x.com/v1", "invalid character"},
		{"https://127.0.0.1/v1", "loopback"},
		{"https://10.0.0.1/v1", "private"},
		{"https://169.254.169.254/latest", "link-local"},
		{"https://192.168.1.1:8080/v1", "private"},
		{"http://[::1]/v1", "loopback"},
		{"http://[fc00::1]/v1", "private"},
		{"https://evil@api.x.com/v1", "userinfo"},
		{"https://api.x.com/" + strings.Repeat("a", 2100), "exceeds"},
		{"https://\x00bad\x01/", "control"},
	}
	for _, u := range good {
		if msg := isValidBaseURL(u); msg != "" {
			t.Errorf("good base %q rejected: %s", u, msg)
		}
	}
	for _, tc := range bad {
		msg := isValidBaseURL(tc.in)
		if msg == "" {
			t.Errorf("bad base %q accepted", tc.in)
			continue
		}
		if !strings.Contains(strings.ToLower(msg), strings.ToLower(tc.want)) {
			t.Errorf("bad base %q error %q missing %q", tc.in, msg, tc.want)
		}
	}
}

func TestIsValidModelsEndpoint(t *testing.T) {
	good := []string{"", "/models", "/v1/models", "/api/v3/models?type=free"}
	bad := []struct {
		in   string
		want string
	}{
		{"models", "relative"},   // 缺前导 /
		{"//api.x.com/models", "scheme-relative"},
		{"https://api.x.com/models", "relative path"},
		{"http://api.x.com/models", "relative path"},
		{"/models/" + strings.Repeat("a", 600), "exceeds"},
		{"/models\x00bad", "control"},
	}
	for _, e := range good {
		if msg := isValidModelsEndpoint(e); msg != "" {
			t.Errorf("good endpoint %q rejected: %s", e, msg)
		}
	}
	for _, tc := range bad {
		msg := isValidModelsEndpoint(tc.in)
		if msg == "" {
			t.Errorf("bad endpoint %q accepted", tc.in)
			continue
		}
		if !strings.Contains(strings.ToLower(msg), strings.ToLower(tc.want)) {
			t.Errorf("bad endpoint %q error %q missing %q", tc.in, msg, tc.want)
		}
	}
}

func TestJoinBaseAndEndpoint_SSRFDefense(t *testing.T) {
	cases := []struct {
		base, ep, want string
	}{
		{"https://api.groq.com/openai/v1", "/models", "https://api.groq.com/openai/v1/models"},
		{"https://api.groq.com/openai/v1/", "/models", "https://api.groq.com/openai/v1/models"},
		{"https://api.groq.com/openai/v1", "models", "https://api.groq.com/openai/v1/models"},
		{"https://api.groq.com/openai/v1", "", "https://api.groq.com/openai/v1"},
		{"https://api.groq.com/openai/v1", "/models?type=free", "https://api.groq.com/openai/v1/models?type=free"},
	}
	for _, c := range cases {
		got, err := joinBaseAndEndpoint(c.base, c.ep)
		if err != nil {
			t.Errorf("joinBaseAndEndpoint(%q,%q): %v", c.base, c.ep, err)
			continue
		}
		if got != c.want {
			t.Errorf("joinBaseAndEndpoint(%q,%q) = %q, want %q", c.base, c.ep, got, c.want)
		}
	}
	// 拒绝绝对 endpoint
	if _, err := joinBaseAndEndpoint("https://x.com/v1", "//evil.com/models"); err == nil {
		t.Error("scheme-relative endpoint must be rejected")
	}
}

func TestTrimmedDisplayName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"  Groq  ", "Groq"},
		{"", ""},
		{strings.Repeat("中", 250), strings.Repeat("中", 200)},
	}
	for _, c := range cases {
		got := TrimmedDisplayName(c.in)
		if got != c.want {
			t.Errorf("TrimmedDisplayName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
