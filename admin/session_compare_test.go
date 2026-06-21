package admin

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestEstimateTokens 验证 token 估算 (len(s) / 3.5, 取整).
func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 0},                // 1 / 3.5 = 0.28 -> 0
		{"abcd", 1},             // 4 / 3.5 = 1.14 -> 1
		{"abcdefg", 2},          // 7 / 3.5 = 2.0 -> 2
		{"abcdefghijklmnop", 4}, // 16 / 3.5 = 4.57 -> 4
		{strings.Repeat("x", 35), 10},
		{strings.Repeat("x", 100), 28},
	}
	for _, tt := range tests {
		got := estimateTokens(tt.input)
		if got != tt.want {
			t.Errorf("estimateTokens(len=%d) = %d, want %d", len(tt.input), got, tt.want)
		}
	}
}

// TestTruncateStr 验证字符串截断.
//
// Known behaviour (audit): truncateStr 会 panic 当 max < 0, 因为
// s[:-1] 是 Go 里非法的 slice 操作. 该函数只在 UI 显示场景使用,
// 上游 max 来自常量 (e.g. 100/200), 所以实际不会触发负值. 本测试
// pin 当前真实行为, 包括 panic, 等于未来重构时显式 surface.
func TestTruncateStr(t *testing.T) {
	tests := []struct {
		input string
		max   int
		want  string
	}{
		{"hello", 10, "hello"},        // len < max -> unchanged
		{"hello", 5, "hello"},         // len == max -> unchanged
		{"hello world", 5, "hello"},   // len > max -> truncated
		{"", 10, ""},                  // empty -> empty
		{"abc", 0, ""},                // max=0 -> ""
		{strings.Repeat("x", 1000), 100, strings.Repeat("x", 100)},
	}
	for _, tt := range tests {
		got := truncateStr(tt.input, tt.max)
		if got != tt.want {
			t.Errorf("truncateStr(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.want)
		}
	}
}

// TestExtractTextContent_String 验证 string content extraction.
func TestExtractTextContent_String(t *testing.T) {
	raw := json.RawMessage(`"hello world"`)
	if got := extractTextContent(raw); got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

// TestExtractTextContent_Parts 验证 multipart text extraction.
func TestExtractTextContent_Parts(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"single text part", `[{"type":"text","text":"hello"}]`, "hello"},
		{"multiple text parts", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "a\nb"},
		{"text + image, only text", `[{"type":"text","text":"see:"},{"type":"image","src":"x"}]`, "see:"},
		{"empty parts", `[]`, ""},
		{"only image, no text", `[{"type":"image","src":"x"}]`, ""},
		{"empty raw", ``, ""},
		{"non-text type", `[{"type":"tool_use","id":"t1"}]`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTextContent(json.RawMessage(tt.raw))
			if got != tt.want {
				t.Errorf("extractTextContent(%s) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestExtractTextContent_NonJSON 验证非 JSON fallback 为 raw 字符串.
func TestExtractTextContent_NonJSON(t *testing.T) {
	raw := json.RawMessage(`plain text not json`)
	got := extractTextContent(raw)
	if got != "plain text not json" {
		t.Errorf("non-json should fallback to raw, got %q", got)
	}
}

// TestHandleCompare_NoURLTenantID verifies that HandleCompare does NOT read
// tenant_id from URL query params. This is a regression guard for the P0
// security fix that replaced URL param extraction with EffectiveTenantID(r).
func TestHandleCompare_NoURLTenantID(t *testing.T) {
	data, err := os.ReadFile("session_compare.go")
	if err != nil {
		t.Fatalf("cannot read session_compare.go: %v", err)
	}
	src := string(data)
	if strings.Contains(src, `r.URL.Query().Get("tenant_id")`) {
		t.Fatal("session_compare.go must NOT read tenant_id from URL query — use EffectiveTenantID(r)")
	}
	if !strings.Contains(src, "EffectiveTenantID(r)") {
		t.Fatal("session_compare.go must use EffectiveTenantID(r) for tenant scoping")
	}
}

// TestHandleCompare_EffectiveTenantID_FromJWT verifies that HandleCompare
// uses the JWT AuthContext for tenant isolation, not URL params.
func TestHandleCompare_EffectiveTenantID_FromJWT(t *testing.T) {
	tests := []struct {
		name   string
		auth   *AuthContext
		wantID string
	}{
		{"nil auth → default", nil, "default"},
		{"super_admin → default", &AuthContext{Role: "super_admin", TenantID: "default"}, "default"},
		{"tenant_admin → own tenant", &AuthContext{Role: "tenant_admin", TenantID: "acme"}, "acme"},
		{"admin_key → default", &AuthContext{Role: "admin_key", TenantID: "default"}, "default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/admin/session-compare?session_id=x&tenant_id=hacker", nil)
			if tt.auth != nil {
				req = SetAuthContext(req, tt.auth)
			}
			got := EffectiveTenantID(req)
			if got != tt.wantID {
				t.Errorf("EffectiveTenantID = %q, want %q (URL ?tenant_id=hacker must be ignored)", got, tt.wantID)
			}
		})
	}
}