package discovery

import (
	"testing"
)

func ptr(s string) *string { return &s }

func TestModelsEndpointURL(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		template *string
		want     string
	}{
		{
			name:     "xiaomi with /models template",
			baseURL:  "https://token-plan-cn.xiaomimimo.com/v1",
			template: ptr("/models"),
			want:     "https://token-plan-cn.xiaomimimo.com/v1/models",
		},
		{
			name:     "nil template (legacy fallback) - /v1 base",
			baseURL:  "https://token-plan-cn.xiaomimimo.com/v1",
			template: nil,
			want:     "https://token-plan-cn.xiaomimimo.com/v1/models",
		},
		{
			name:     "nil template (legacy fallback) - no /v1",
			baseURL:  "https://api.openai.com",
			template: nil,
			want:     "https://api.openai.com/v1/models",
		},
		{
			name:     "empty template (manifest-only supplier)",
			baseURL:  "https://example.com/v1",
			template: ptr(""),
			want:     "",
		},
		{
			name:     "full URL template",
			baseURL:  "https://example.com",
			template: ptr("https://custom.example.com/models"),
			want:     "https://custom.example.com/models",
		},
		{
			name:     "base with trailing slash + /models",
			baseURL:  "https://example.com/v1/",
			template: ptr("/models"),
			want:     "https://example.com/v1/models",
		},
		{
			name:     "template with leading space",
			baseURL:  "https://example.com/v1",
			template: ptr("  /models  "),
			want:     "https://example.com/v1/models",
		},
		{
			name:     "/v1/models template",
			baseURL:  "https://example.com",
			template: ptr("/v1/models"),
			want:     "https://example.com/v1/models",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := modelsEndpointURL(tt.baseURL, tt.template)
			if got != tt.want {
				t.Errorf("modelsEndpointURL(%q, %v) = %q, want %q",
					tt.baseURL, tt.template, got, tt.want)
			}
		})
	}
}
