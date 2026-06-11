package urlutil

import "testing"

func TestModelsURL(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		// Trailing /v1 must be stripped, then /v1/models appended.
		{"xiaomi /v1", "https://token-plan-cn.xiaomimimo.com/v1", "https://token-plan-cn.xiaomimimo.com/v1/models"},
		{"minimax /v1", "https://api.minimaxi.com/v1", "https://api.minimaxi.com/v1/models"},
		{"openai /v1", "https://api.openai.com/v1", "https://api.openai.com/v1/models"},
		{"nvidia /v1", "https://integrate.api.nvidia.com/v1", "https://integrate.api.nvidia.com/v1/models"},
		{"vapeur /v1", "https://api.vapeur.ai/v1", "https://api.vapeur.ai/v1/models"},
		{"evol /v1 with proxy prefix", "https://mg-new.evolai.cn/openclaw-proxy/v1", "https://mg-new.evolai.cn/openclaw-proxy/v1/models"},

		// Mid-path /v3, /v4 must be preserved, trailing /v3 stripped, then /v1/models appended.
		{"zhipu mid /v4", "https://open.bigmodel.cn/api/coding/paas/v4", "https://open.bigmodel.cn/api/coding/paas/v1/models"},
		{"volcano mid /v3", "https://ark.cn-beijing.volces.com/api/v3", "https://ark.cn-beijing.volces.com/api/v1/models"},
		{"volcano-coding mid /v3", "https://ark.cn-beijing.volces.com/api/coding/v3", "https://ark.cn-beijing.volces.com/api/coding/v1/models"},

		// /v1beta is NOT a digits-only version → must be preserved as part of the path.
		{"gemini /v1beta mid", "https://generativelanguage.googleapis.com/v1beta/openai", "https://generativelanguage.googleapis.com/v1beta/openai/v1/models"},

		// No trailing version: just append /v1/models.
		{"anthropic no /vN", "https://api.anthropic.com", "https://api.anthropic.com/v1/models"},
		{"azure placeholder no /vN", "https://{r}.openai.azure.com/openai/deployments/{d}", "https://{r}.openai.azure.com/openai/deployments/{d}/v1/models"},

		// Trailing slash.
		{"trailing slash", "https://api.openai.com/v1/", "https://api.openai.com/v1/models"},
		{"no version trailing slash", "https://api.anthropic.com/", "https://api.anthropic.com/v1/models"},

		// Completion suffixes → clean → append /v1/models.
		{"chat completions suffix", "https://api.openai.com/v1/chat/completions", "https://api.openai.com/v1/models"},
		{"completions suffix", "https://api.openai.com/v1/completions", "https://api.openai.com/v1/models"},
		{"responses suffix", "https://api.openai.com/v1/responses", "https://api.openai.com/v1/models"},
		{"messages suffix", "https://api.anthropic.com/v1/messages", "https://api.anthropic.com/v1/models"},

		// Local runtime templates.
		{"local ollama /v1", "http://{host}:{port}/v1", "http://{host}:{port}/v1/models"},
		{"local ollama no /v1", "http://{host}:{port}", "http://{host}:{port}/v1/models"},

		// v2-v9.
		{"v2 trailing", "https://api.example.com/v2", "https://api.example.com/v1/models"},
		{"v9 trailing", "https://api.example.com/v9", "https://api.example.com/v1/models"},

		// Empty.
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModelsURL(tc.in); got != tc.want {
				t.Errorf("ModelsURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestChatCompletionsURL(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"xiaomi /v1", "https://token-plan-cn.xiaomimimo.com/v1", "https://token-plan-cn.xiaomimimo.com/v1/chat/completions"},
		{"openai no /vN", "https://api.openai.com", "https://api.openai.com/v1/chat/completions"},
		{"volcano mid /v3", "https://ark.cn-beijing.volces.com/api/v3", "https://ark.cn-beijing.volces.com/api/v1/chat/completions"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ChatCompletionsURL(tc.in); got != tc.want {
				t.Errorf("ChatCompletionsURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestModelsURLCandidates(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "openai no /v1 suffix",
			in:   "https://api.openai.com",
			want: []string{
				"https://api.openai.com/models",
				"https://api.openai.com/v1/models",
			},
		},
		{
			// For base ending in /v1, Python's candidates list has 3 entries
			// but two of them collapse to the same URL (root/v1/models ==
			// normalized/v1/models when normalized ends in /v1). The third
			// is normalized/v1/models which actually becomes /v1/v1/models
			// (will 404 in practice but mirrors Python's probe list).
			name: "xiaomi ends in /v1",
			in:   "https://token-plan-cn.xiaomimimo.com/v1",
			want: []string{
				"https://token-plan-cn.xiaomimimo.com/v1/models", // root+v1/models
				"https://token-plan-cn.xiaomimimo.com/v1/models", // normalized/models
				"https://token-plan-cn.xiaomimimo.com/v1/v1/models", // normalized+v1/models
			},
		},
		{
			name: "zhipu mid /v4",
			in:   "https://open.bigmodel.cn/api/coding/paas/v4",
			want: []string{
				"https://open.bigmodel.cn/api/coding/paas/v4/models",
				"https://open.bigmodel.cn/api/coding/paas/v4/v1/models",
			},
		},
		{
			name: "empty",
			in:   "",
			want: nil,
		},
		{
			name: "trailing slash",
			in:   "https://api.openai.com/",
			want: []string{
				"https://api.openai.com/models",
				"https://api.openai.com/v1/models",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ModelsURLCandidates(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("ModelsURLCandidates(%q) len=%d, want %d (%v)", tc.in, len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("ModelsURLCandidates(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}
