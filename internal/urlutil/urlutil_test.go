package urlutil

import "testing"

func TestBuildModelsURL(t *testing.T) {
	cases := []struct {
		name, base, tmpl, want string
	}{
		{"empty template skips", "https://api.openai.com/v1", "", ""},
		{"azure manifest-only", "https://{r}.openai.azure.com/openai/deployments/{d}", "", ""},
		{"minimax /models", "https://api.minimaxi.com/v1", "/models", "https://api.minimaxi.com/v1/models"},
		{"xiaomi /models", "https://token-plan-cn.xiaomimimo.com/v1", "/models", "https://token-plan-cn.xiaomimimo.com/v1/models"},
		{"zhipu paas /models", "https://open.bigmodel.cn/api/coding/paas/v4", "/models", "https://open.bigmodel.cn/api/coding/paas/v4/models"},
		{"anthropic /v1/models", "https://api.anthropic.com", "/v1/models", "https://api.anthropic.com/v1/models"},
		{"volcano /models (base has /v3)", "https://ark.cn-beijing.volces.com/api/v3", "/models", "https://ark.cn-beijing.volces.com/api/v3/models"},
		{"volcano coding /models (base has /v3)", "https://ark.cn-beijing.volces.com/api/coding/v3", "/models", "https://ark.cn-beijing.volces.com/api/coding/v3/models"},
		{"absolute url template", "https://x.openai.com/v1", "https://y.com/models", "https://y.com/models"},
		{"trailing slash base", "https://api.openai.com/v1/", "/models", "https://api.openai.com/v1/models"},
		{"ollama local runtime", "http://{host}:{port}/v1", "/models", "http://{host}:{port}/v1/models"},
		{"scnet nested", "https://api.scnet.cn/api/llm/v1", "/models", "https://api.scnet.cn/api/llm/v1/models"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BuildModelsURL(tc.base, tc.tmpl); got != tc.want {
				t.Errorf("BuildModelsURL(%q, %q) = %q, want %q", tc.base, tc.tmpl, got, tc.want)
			}
		})
	}
}

func TestCleanBaseURL(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"trailing slash only", "https://api.openai.com/v1/", "https://api.openai.com/v1"},
		{"chat completions suffix", "https://api.openai.com/v1/chat/completions", "https://api.openai.com"},
		{"completions suffix", "https://api.openai.com/v1/completions", "https://api.openai.com"},
		{"responses suffix", "https://api.openai.com/v1/responses", "https://api.openai.com"},
		{"messages suffix", "https://api.anthropic.com/v1/messages", "https://api.anthropic.com"},
		{"no suffix", "https://api.openai.com/v1", "https://api.openai.com/v1"},
		{"no version no change", "https://api.anthropic.com", "https://api.anthropic.com"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanBaseURL(tc.in); got != tc.want {
				t.Errorf("CleanBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
