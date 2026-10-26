package streaming

import (
	"testing"
)

// 2026-10-27 缓存监测轮：非流式响应体的 TTL 写入分档提取
// （extractCacheWriteTTLSplit，只进监测面）。
func TestExtractCacheWriteTTLSplit_VendorShapes(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		short, lng int
		ok         bool
	}{
		{
			name:  "anthropic nested cache_creation",
			body:  `{"usage":{"input_tokens":100,"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}}}`,
			short: 100, lng: 200, ok: true,
		},
		{
			name:  "openai cache_write_tokens_details",
			body:  `{"usage":{"input_tokens":100,"cache_write_tokens":192,"cache_write_tokens_details":{"ttl_30m_tokens":64,"ttl_12h_tokens":128}}}`,
			short: 64, lng: 128, ok: true,
		},
		{
			name: "absent split stays not-ok",
			body: `{"usage":{"input_tokens":100,"output_tokens":5}}`,
			ok:   false,
		},
		{
			name: "empty body",
			body: ``,
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			short, lng, ok := extractCacheWriteTTLSplit([]byte(tt.body))
			if short != tt.short || lng != tt.lng || ok != tt.ok {
				t.Fatalf("extractCacheWriteTTLSplit(%s) = (%d,%d,%v), want (%d,%d,%v)",
					tt.body, short, lng, ok, tt.short, tt.lng, tt.ok)
			}
		})
	}
}
