package upstreamurl

import (
	"strings"
	"testing"
)

func FuzzBuild(f *testing.F) {
	seeds := []struct {
		base string
		ep   string
	}{
		{"", "chat_completions"},
		{"https://api.openai.com", "chat_completions"},
		{"https://api.openai.com/v1", "chat_completions"},
		{"https://ark.cn-beijing.volces.com/api/v3", "chat_completions"},
		{"https://open.bigmodel.cn/api/coding/paas/v4", "messages"},
		{"http://localhost:11434", "ollama_chat"},
		{"http://localhost:11434/v1", "ollama_chat"},
		{"https://api.openai.com/v1/chat/completions", "chat_completions"},
		{"https://api.anthropic.com/v1/messages", "messages"},
	}
	for _, s := range seeds {
		f.Add(s.base, s.ep)
	}

	f.Fuzz(func(t *testing.T, base string, epStr string) {
		ep := Endpoint(epStr)
		got := Build(base, ep)

		if base == "" && got != "" {
			t.Fatalf("Build(%q, %q) = %q; want empty for empty base", base, epStr, got)
		}

		if got2 := Build(got, ep); got2 != got {
			t.Fatalf("Build not idempotent: Build(%q,%q)=%q; second pass=%q", base, epStr, got, got2)
		}

		if ep == EpOllamaChat && base != "" && !strings.HasSuffix(got, "/api/chat") {
			t.Fatalf("OllamaChat Build(%q) = %q; want suffix /api/chat", base, got)
		}
	})
}

func BenchmarkBuild(b *testing.B) {
	cases := []struct {
		name string
		base string
		ep   Endpoint
	}{
		{"openai_bare", "https://api.openai.com", EpChatCompletions},
		{"openai_v1", "https://api.openai.com/v1", EpChatCompletions},
		{"volcengine_v3", "https://ark.cn-beijing.volces.com/api/v3", EpChatCompletions},
		{"zhipu_v4_deep", "https://open.bigmodel.cn/api/coding/paas/v4", EpChatCompletions},
		{"ollama_strip_v1", "http://localhost:11434/v1", EpOllamaChat},
		{"empty_base", "", EpChatCompletions},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = Build(c.base, c.ep)
			}
		})
	}
}
