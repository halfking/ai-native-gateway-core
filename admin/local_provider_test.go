package admin

import (
	"strings"
	"testing"
)

// 2026-09-07 本地托管供应商（migration 671）辅助逻辑测试。

func TestIsLocalKind(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"local", true},
		{"Local", true},
		{" LOCAL ", true},
		{"cloud", false},
		{"", false},
		{"local-provider", false},
	}
	for _, c := range cases {
		if got := isLocalKind(c.in); got != c.want {
			t.Errorf("isLocalKind(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestValidateLocalBaseURL(t *testing.T) {
	valid := []string{
		"http://127.0.0.1:11434/v1",
		"http://localhost:8080/v1",
		"http://192.168.31.28:8080/v1",
		"http://10.0.0.5:8000/v1",
		"http://172.16.0.9:8000/v1",
		"http://172.31.255.255:8000/v1",
		"http://host.docker.internal:8080/v1",
		"https://127.0.0.1:8080/v1",
		"http://[::1]:8080/v1",        // IPv6 回环
		"http://[fd00::1]:8080/v1",    // IPv6 ULA
		"http://127.8.8.8:11434/v1",   // 回环段非 .0.0.1 字面值
	}
	for _, u := range valid {
		if msg := validateLocalBaseURL(u); msg != "" {
			t.Errorf("validateLocalBaseURL(%q) rejected: %s", u, msg)
		}
	}

	invalid := []string{
		"",
		"not-a-url",
		"ftp://127.0.0.1:8080",
		"https://api.openai.com/v1",    // 公网端点不允许
		"http://8.136.114.245:8080/v1", // 公网 IP 不允许
		"http://172.32.0.1:8000/v1",    // 172.32 超出私网段
		// 2026-09-07 审计：字符串前缀匹配放行伪私网主机名（SSRF 面），必须拒绝。
		"http://10.evil.com:8080/v1",
		"http://192.168.attacker.tld:8080/v1",
		"http://172.16.evil.com:8000/v1",
		"http://metadata.google.internal/v1", // 云 metadata 端点
		"http://169.254.169.254/latest",      // 云 metadata IP（链路本地不放行）
		"http://0.0.0.0:8000/v1",             // 未指定地址
	}
	for _, u := range invalid {
		if msg := validateLocalBaseURL(u); msg == "" {
			t.Errorf("validateLocalBaseURL(%q) should be rejected", u)
		}
	}
}

func TestLocalNoKeyPlaceholder(t *testing.T) {
	// 占位密钥必须非空且无空白 —— 加密链路与 Bearer 头拼装都假定单 token。
	if strings.TrimSpace(localNoKeyPlaceholder) == "" {
		t.Fatal("placeholder must not be blank")
	}
	if strings.ContainsAny(localNoKeyPlaceholder, " \t\n") {
		t.Fatal("placeholder must not contain whitespace")
	}
	if localNoKeyPlaceholder != "local-no-key" {
		t.Fatalf("placeholder changed unexpectedly: %q", localNoKeyPlaceholder)
	}
}

func TestLocalCredentialsImmutableMessage(t *testing.T) {
	if errLocalCredentialImmutable == "" {
		t.Fatal("immutable message must not be empty")
	}
	if !strings.Contains(errLocalCredentialImmutable, "cannot") {
		t.Fatal("immutable message should explain the restriction")
	}
}

func TestDefaultLocalBaseURLForCode(t *testing.T) {
	cases := map[string]string{
		"ollama":   "http://127.0.0.1:11434/v1",
		"mlx":      "http://127.0.0.1:8080/v1",
		"llamacpp": "http://127.0.0.1:8082/v1",
		"lmstudio": "http://127.0.0.1:1234/v1",
		"vllm":     "http://127.0.0.1:8000/v1",
		"unknown":  "",
	}
	for code, want := range cases {
		if got := defaultLocalBaseURLForCode(code); got != want {
			t.Errorf("defaultLocalBaseURLForCode(%q) = %q, want %q", code, got, want)
		}
	}
}
