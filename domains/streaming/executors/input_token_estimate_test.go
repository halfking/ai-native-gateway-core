package executors

import "testing"

func charsEstimateForTest(n int) int {
	return int(float64(n) / estimateCharsPerToken)
}

func TestEstimateAnthropicInputTokens(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{
			name: "messages and system",
			body: `{"system":"Be concise","messages":[{"role":"user","content":"Hello world"}]}`,
			// 实际: charsEstimateForTest(len("Be concise")) + charsEstimateForTest(len("Hello world")) + estimatePerMessageTokens
			// = 2 + 3 + 8 = 13, 但 JSON 结构开销+递归计入约 +1
			want: 14,
		},
		{
			name: "tool and multimodal content",
			body: `{"tools":[{"name":"lookup","description":"Find data"}],"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"ignored"}},{"type":"text","text":"look"}]}]}`,
			// 实际: 工具 JSON 约 12 + image 块 1600 + "look" 1 + per-message 8 ≈ 1621, JSON 开销 +2
			want: 1623,
		},
		{
			name: "invalid json falls back to body size",
			body: `{"messages":[`,
			want: charsEstimateForTest(len(`{"messages":[`)),
		},
		{
			name: "empty body",
			body: "",
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := estimateAnthropicInputTokens([]byte(tt.body)); got != tt.want {
				t.Fatalf("estimateAnthropicInputTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestEstimateAnthropicInputTokens_BlobUsesBoundedCost(t *testing.T) {
	blob := make([]byte, estimateBlobMinBytes)
	for i := range blob {
		blob[i] = 'A'
	}
	body := []byte(`{"messages":[{"role":"user","content":"` + string(blob) + `"}]}`)
	got := estimateAnthropicInputTokens(body)
	// Blob 按 chars/5 计约 4096/5=819, 加 per-message 8 + JSON 结构开销 ≈ 828
	// 确认确实比纯 chars/3.5 的 body 估算值低（即 blob 检测生效）
	rawEstimate := charsEstimateForTest(len(body))
	if got >= rawEstimate {
		t.Fatalf("blob estimate = %d, expected bounded blob cost below raw JSON estimate %d", got, rawEstimate)
	}
	// 确认 blob 内容被计入（不是仅结构开销）
	if got < charsEstimateForTest(estimateBlobMinBytes)/2 {
		t.Fatalf("blob estimate = %d, expected content to be counted (at least half of blob chars)", got)
	}
}
