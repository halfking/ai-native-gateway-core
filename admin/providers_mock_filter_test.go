package admin

import "testing"

// TestMockProviderHidden：listProviders 的 mock 供应商过滤谓词
//（验收：MockProbeHideInAdmin=true 时列表不含 mock- 前缀）。
func TestMockProviderHidden(t *testing.T) {
	cases := []struct {
		name   string
		env    string // LLM_GATEWAY_MOCK_PROBE_HIDE_IN_ADMIN
		code   string
		hidden bool
	}{
		{"default hides mock-fast", "", "mock-fast", true},
		{"default hides mock-slow", "", "mock-slow", true},
		{"default hides arbitrary mock prefix", "", "mock-anything", true},
		{"default keeps real providers", "", "default-openai", false},
		{"prefix must match with dash", "", "mockfast", false},
		{"explicit false shows mock", "false", "mock-fast", false},
		{"explicit 0 shows mock", "0", "mock-slow", false},
		{"explicit true hides", "true", "mock-fast", true},
	}
	for _, c := range cases {
		t.Setenv("LLM_GATEWAY_MOCK_PROBE_HIDE_IN_ADMIN", c.env)
		if got := mockProviderHidden(c.code); got != c.hidden {
			t.Errorf("%s: mockProviderHidden(%q) env=%q = %v, want %v",
				c.name, c.code, c.env, got, c.hidden)
		}
	}
}
