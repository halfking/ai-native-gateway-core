package bg

import "testing"

func TestResponseTokenCountSupportsProviderUsageShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"total", `{"usage":{"total_tokens":12}}`, 12},
		{"openai", `{"usage":{"prompt_tokens":7,"completion_tokens":5}}`, 12},
		{"modern", `{"usage":{"input_tokens":8,"output_tokens":3}}`, 11},
		{"missing", `{"choices":[]}`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := responseTokenCount([]byte(tt.body)); got != tt.want {
				t.Fatalf("responseTokenCount() = %d, want %d", got, tt.want)
			}
		})
	}
}
