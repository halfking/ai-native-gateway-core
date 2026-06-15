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
		{
			name:     "azure-openai empty template with base URL containing /openai",
			baseURL:  "https://test.openai.azure.com/openai",
			template: ptr(""),
			want:     "",
		},
		{
			name:     "ollama localhost no template",
			baseURL:  "http://localhost:11434",
			template: nil,
			want:     "http://localhost:11434/v1/models",
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

// ── mergeManifestModels — merge known-but-unlisted models from catalog ─────

func TestMergeManifestModels_AppendsUnlistedGlm52(t *testing.T) {
	// zhipu ships glm-5.2 before /models lists it; the catalog manifest
	// registers it so auto-discovery still surfaces it.
	live := []string{"glm-4.6", "glm-5", "glm-5.1"}
	manifest := ptr(`{"models":[{"id":"glm-5.1"},{"id":"glm-5.2"}]}`)
	got := mergeManifestModels(live, manifest)
	want := []string{"glm-4.6", "glm-5", "glm-5.1", "glm-5.2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestMergeManifestModels_NilManifestReturnsLive(t *testing.T) {
	live := []string{"a", "b"}
	if got := mergeManifestModels(live, nil); len(got) != 2 || got[0] != "a" {
		t.Fatalf("got %v, want live unchanged", got)
	}
}

func TestMergeManifestModels_MalformedJSONReturnsLive(t *testing.T) {
	live := []string{"a"}
	if got := mergeManifestModels(live, ptr("{not json")); len(got) != 1 || got[0] != "a" {
		t.Fatalf("got %v, want live unchanged on malformed manifest", got)
	}
}

func TestMergeManifestModels_CaseInsensitiveDedup(t *testing.T) {
	live := []string{"GPT-4o"}
	got := mergeManifestModels(live, ptr(`{"models":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`))
	if len(got) != 2 || got[0] != "GPT-4o" || got[1] != "gpt-4o-mini" {
		t.Fatalf("got %v, want [GPT-4o gpt-4o-mini]", got)
	}
}
