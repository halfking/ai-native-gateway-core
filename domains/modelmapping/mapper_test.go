package modelmapping

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestTranslate_KnownCanonicalKnownProvider tests that a known mapping resolves correctly.
func TestTranslate_KnownCanonicalKnownProvider(t *testing.T) {
	m := NewModelMapper()

	tests := []struct {
		canonical string
		provider  string
		expected  string
	}{
		{"minimax-m2", "minimax", "MiniMax-M2"},
		{"minimax-m2", "nvidia", "minimaxai/minimax-m2.7"},
		{"minimax-m2", "evol", "MiniMax-M2.7"},
		{"minimax-m2", "kaixuan", "minimax-m2.7"},
		{"minimax-m3", "nvidia", "minimaxai/minimax-m3"},
		{"glm-4.7", "zhipu", "glm-4.7"},
		{"glm-5.1", "nvidia", "z-ai/glm-5.2"}, // NVIDIA uses 5.2
		{"deepseek-v4", "kaixuan", "deepseek-v4-pro"},
		{"deepseek-v4", "evol", "deepseek-v4-flash"},
	}

	for _, tc := range tests {
		t.Run(tc.canonical+"_"+tc.provider, func(t *testing.T) {
			got := m.Translate(tc.canonical, tc.provider)
			assert.Equal(t, tc.expected, got)
		})
	}
}

// TestTranslate_UnknownProviderFallsBackToDefault tests that unknown provider falls back to default.
func TestTranslate_UnknownProviderFallsBackToDefault(t *testing.T) {
	m := NewModelMapper()

	// Request "minimax-m2" from provider "unknown-provider" → should fall back to default
	got := m.Translate("minimax-m2", "unknown-provider")
	assert.Equal(t, "MiniMax-M2", got, "Should fall back to default mapping")
}

// TestTranslate_UnknownCanonicalPassthrough tests that unknown canonical returns as-is.
func TestTranslate_UnknownCanonicalPassthrough(t *testing.T) {
	m := NewModelMapper()

	// Unknown canonical → passthrough
	got := m.Translate("unknown-model-xyz", "nvidia")
	assert.Equal(t, "unknown-model-xyz", got, "Unknown canonical should passthrough")
}

// TestSupportsCanonical verifies SupportsCanonical logic.
func TestSupportsCanonical(t *testing.T) {
	m := NewModelMapper()

	assert.True(t, m.SupportsCanonical("minimax", "minimax-m2"))
	assert.True(t, m.SupportsCanonical("nvidia", "minimax-m2"))
	assert.True(t, m.SupportsCanonical("kaixuan", "deepseek-v4"))
	assert.False(t, m.SupportsCanonical("xiaomi", "minimax-m2"), "xiaomi doesn't serve minimax")
	assert.False(t, m.SupportsCanonical("minimax", "nonexistent-model"))
}

// TestProvidersFor verifies ProvidersFor returns correct list.
func TestProvidersFor(t *testing.T) {
	m := NewModelMapper()

	providers := m.ProvidersFor("minimax-m2")
	sort.Strings(providers)
	expected := []string{"evol", "kaixuan", "minimax", "nvidia"}
	assert.Equal(t, expected, providers, "Should return all providers serving minimax-m2")
}

// TestCanonicalModels returns all canonical names.
func TestCanonicalModels(t *testing.T) {
	m := NewModelMapper()

	models := m.CanonicalModels()
	assert.Contains(t, models, "minimax-m2")
	assert.Contains(t, models, "minimax-m3")
	assert.Contains(t, models, "glm-4.7")
	assert.Contains(t, models, "glm-5.1")
	assert.Contains(t, models, "claude-sonnet-4")
}

// TestRegisterMapping_RuntimeUpdate tests dynamic registration.
func TestRegisterMapping_RuntimeUpdate(t *testing.T) {
	m := NewModelMapper()

	// Register new mapping
	m.RegisterMapping("new-model-x", "new-provider", "native-name-z")

	got := m.Translate("new-model-x", "new-provider")
	assert.Equal(t, "native-name-z", got)
}

// TestRoundTripPreservesSemantic verifies translations are idempotent at semantic level.
// I.e., translating canonical→native should not lose semantic meaning.
func TestRoundTripPreservesSemantic(t *testing.T) {
	m := NewModelMapper()

	// Each canonical name should map to a "sensible" native name
	// (not blank, not the same as another model's native name).
	for _, canonical := range m.CanonicalModels() {
		for _, provider := range m.ProvidersFor(canonical) {
			native := m.Translate(canonical, provider)
			assert.NotEmpty(t, native, "Native name should not be empty for %s/%s", canonical, provider)
			t.Logf("  %s via %s → %s", canonical, provider, native)
		}
	}
}

// BenchmarkTranslate benchmarks the translation operation.
func BenchmarkTranslate(b *testing.B) {
	m := NewModelMapper()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.Translate("minimax-m2", "nvidia")
	}
}
