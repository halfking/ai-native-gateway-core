package admin

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/providers/mock"
)

// TestMockProviderHidden：listProviders 的 mock 供应商过滤谓词（audit P3
// 收敛后的语义：MockProbeHideInAdmin=true 时仅隐藏探测通道自带的
// mock-fast / mock-slow；历史种子（mock-openai / mock-anthropic）与运维
// 自建的 mock-x 保持可见）。
func TestMockProviderHidden(t *testing.T) {
	cases := []struct {
		name   string
		env    string // LLM_GATEWAY_MOCK_PROBE_HIDE_IN_ADMIN
		code   string
		hidden bool
	}{
		{"default hides mock-fast", "", mock.CodeFast, true},
		{"default hides mock-slow", "", mock.CodeSlow, true},
		{"self-built mock-x stays visible", "", "mock-x", false},
		{"legacy seed mock-openai stays visible", "", "mock-openai", false},
		{"legacy seed mock-anthropic stays visible", "", "mock-anthropic", false},
		{"default keeps real providers", "", "default-openai", false},
		{"prefix must match with dash", "", "mockfast", false},
		{"explicit false shows mock", "false", mock.CodeFast, false},
		{"explicit 0 shows mock", "0", mock.CodeSlow, false},
		{"explicit true hides", "true", mock.CodeFast, true},
	}
	for _, c := range cases {
		t.Setenv("LLM_GATEWAY_MOCK_PROBE_HIDE_IN_ADMIN", c.env)
		if got := mockProviderHidden(c.code); got != c.hidden {
			t.Errorf("%s: mockProviderHidden(%q) env=%q = %v, want %v",
				c.name, c.code, c.env, got, c.hidden)
		}
	}
}

// TestMockProviderFilterClause：SQL 侧 WHERE 谓词与隐藏集合一致性
//（audit P3：必须是精确 NOT IN 集合，不得回退为前缀 NOT LIKE）。
func TestMockProviderFilterClause(t *testing.T) {
	clause := mockProbeFilterClause()
	if !strings.Contains(clause, "NOT IN") {
		t.Errorf("clause must be an exact NOT IN set, got %q", clause)
	}
	if strings.Contains(clause, "LIKE") {
		t.Errorf("prefix LIKE filter must not come back (audit P3), got %q", clause)
	}
	for _, code := range []string{mock.CodeFast, mock.CodeSlow} {
		if !strings.Contains(clause, "'"+code+"'") {
			t.Errorf("clause %q must cover probe supplier %q", clause, code)
		}
	}
	// 历史种子与自建 mock-x 不得出现在隐藏集合里。
	for _, keep := range []string{"mock-openai", "mock-anthropic", "mock-x"} {
		if strings.Contains(clause, "'"+keep+"'") {
			t.Errorf("clause %q must not hide non-probe code %q", clause, keep)
		}
	}
}
