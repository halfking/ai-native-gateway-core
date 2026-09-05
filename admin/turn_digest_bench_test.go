package admin

import (
	"encoding/json"
	"strconv"
	"testing"
)

func benchmarkDigestPayload(text string) (any, any) {
	request := map[string]any{"messages": []any{map[string]any{"role": "user", "content": text}}}
	response := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": text}}}}
	return request, response
}

func BenchmarkBuildTurnDigestTextShapes(b *testing.B) {
	cases := map[string]string{
		"short":                "请总结这个请求",
		"long_zh":              "这是一个需要观察摘要生成性能的中文长文本。关键结论是保持确定性、UTF-8 安全，并且不要把工具参数暴露为默认用户文本。结果需要在管理页面稳定呈现。",
		"long_en":              "This long request exercises deterministic digest generation. The important result is that summaries remain UTF-8 safe, stable across calls, and do not expose arbitrary tool arguments in the default operator view.",
		"no_sentence_boundary": "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 repeated content without sentence boundaries to exercise the safe prefix and suffix fallback path ",
	}
	for name, text := range cases {
		request, response := benchmarkDigestPayload(text)
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = buildTurnDigest(request, response, map[string]any{"prompt_tokens": 120, "completion_tokens": 80, "latency_ms": 240}, nil)
			}
		})
	}
}

func BenchmarkBuildTurnDigestBatch(b *testing.B) {
	for _, n := range []int{1, 10, 50, 200} {
		requestMessages := make([]any, 0, n)
		responseMessages := make([]any, 0, n)
		for i := 0; i < n; i++ {
			text := "turn " + strconv.Itoa(i) + ": the result is stable and should remain visible to an operator."
			requestMessages = append(requestMessages, map[string]any{"role": "user", "content": text})
			responseMessages = append(responseMessages, map[string]any{"role": "assistant", "content": text})
		}
		request := map[string]any{"messages": requestMessages}
		response := map[string]any{"messages": responseMessages}
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = buildTurnDigest(request, response, map[string]any{"prompt_tokens": n * 10, "completion_tokens": n * 8}, nil)
			}
		})
	}
}

func BenchmarkTurnDigestJSONMarshal(b *testing.B) {
	request, response := benchmarkDigestPayload("This is a representative request that is long enough to exercise deterministic summary output and response serialization.")
	digest := buildTurnDigest(request, response, map[string]any{"prompt_tokens": 120, "completion_tokens": 80, "cost_usd": 0.01, "latency_ms": 240}, nil)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(digest)
	}
}
