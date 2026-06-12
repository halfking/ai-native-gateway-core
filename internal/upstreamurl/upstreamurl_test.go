package upstreamurl

import (
	"reflect"
	"testing"
)

func TestBuild(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ep   Endpoint
		want string
	}{
		// /v1 endpoints — base ends in /v1 stays /v1, base without version gets /v1 added.
		{"openai no /v1", "https://api.openai.com", EpChatCompletions, "https://api.openai.com/v1/chat/completions"},
		{"openai with /v1", "https://api.openai.com/v1", EpChatCompletions, "https://api.openai.com/v1/chat/completions"},

		// /v1 suffix on completion suffix: strip → re-add.
		{"openai chat completions suffix", "https://api.openai.com/v1/chat/completions", EpChatCompletions, "https://api.openai.com/v1/chat/completions"},
		{"anthropic messages suffix", "https://api.anthropic.com/v1/messages", EpMessages, "https://api.anthropic.com/v1/messages"},

		// Mid-path /v3, /v4 must be preserved, trailing /vN stripped.
		{"zhipu mid /v4", "https://open.bigmodel.cn/api/coding/paas/v4", EpChatCompletions, "https://open.bigmodel.cn/api/coding/paas/v1/chat/completions"},
		{"volcano mid /v3", "https://ark.cn-beijing.volces.com/api/v3", EpChatCompletions, "https://ark.cn-beijing.volces.com/api/v1/chat/completions"},
		{"volcano-coding mid /v3", "https://ark.cn-beijing.volces.com/api/coding/v3", EpChatCompletions, "https://ark.cn-beijing.volces.com/api/coding/v1/chat/completions"},

		// /v1beta is NOT a digits-only version → must be preserved.
		{"gemini /v1beta mid", "https://generativelanguage.googleapis.com/v1beta/openai", EpChatCompletions, "https://generativelanguage.googleapis.com/v1beta/openai/v1/chat/completions"},

		// v2-v9 trailing → strip and re-add /v1.
		{"v2 trailing", "https://api.example.com/v2", EpChatCompletions, "https://api.example.com/v1/chat/completions"},
		{"v9 trailing", "https://api.example.com/v9", EpChatCompletions, "https://api.example.com/v1/chat/completions"},

		// Trailing slash.
		{"trailing slash v1", "https://api.openai.com/v1/", EpChatCompletions, "https://api.openai.com/v1/chat/completions"},
		{"trailing slash no vN", "https://api.anthropic.com/", EpChatCompletions, "https://api.anthropic.com/v1/chat/completions"},

		// Local runtime templates.
		{"local ollama v1", "http://{host}:{port}/v1", EpChatCompletions, "http://{host}:{port}/v1/chat/completions"},

		// Each Endpoint type.
		{"models endpoint", "https://api.openai.com", EpModels, "https://api.openai.com/v1/models"},
		{"completions endpoint", "https://api.openai.com", EpCompletions, "https://api.openai.com/v1/completions"},
		{"messages endpoint", "https://api.anthropic.com", EpMessages, "https://api.anthropic.com/v1/messages"},
		{"responses endpoint", "https://api.openai.com", EpResponses, "https://api.openai.com/v1/responses"},

		// Empty base.
		{"empty", "", EpChatCompletions, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Build(tc.in, tc.ep); got != tc.want {
				t.Errorf("Build(%q, %q) = %q, want %q", tc.in, tc.ep, got, tc.want)
			}
		})
	}
}

func TestConvenienceShorthands(t *testing.T) {
	cases := []struct {
		name, in, want string
		fn             func(string) string
	}{
		{"ModelsURL openai", "https://api.openai.com", "https://api.openai.com/v1/models", ModelsURL},
		{"ModelsURL volcano v3", "https://ark.cn-beijing.volces.com/api/v3", "https://ark.cn-beijing.volces.com/api/v1/models", ModelsURL},
		{"ChatCompletionsURL openai", "https://api.openai.com", "https://api.openai.com/v1/chat/completions", ChatCompletionsURL},
		{"ChatCompletionsURL volcano v3", "https://ark.cn-beijing.volces.com/api/v3", "https://ark.cn-beijing.volces.com/api/v1/chat/completions", ChatCompletionsURL},
		{"MessagesURL anthropic", "https://api.anthropic.com", "https://api.anthropic.com/v1/messages", MessagesURL},
		{"ResponsesURL openai", "https://api.openai.com", "https://api.openai.com/v1/responses", ResponsesURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.fn(tc.in); got != tc.want {
				t.Errorf("%T(%q) = %q, want %q", tc.fn, tc.in, got, tc.want)
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
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ModelsURLCandidates(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
