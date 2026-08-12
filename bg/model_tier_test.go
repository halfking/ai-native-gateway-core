package bg

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeModelKey(t *testing.T) {
	cases := map[string]string{
		"GPT-5.4":      "gpt-5.4",
		" glm-5.2 ":    "glm-5.2",
		"Claude-Sonnet": "claude-sonnet",
		"":             "",
		"   ":          "",
	}
	for in, want := range cases {
		assert.Equal(t, want, normalizeModelKey(in), "input=%q", in)
	}
}

// newTierWithSet builds a ModelTier and publishes an immutable featuredSet
// directly (no DB), so IsFeaturedModel can be tested without Postgres.
func newTierWithSet(static, usage []string) *ModelTier {
	m := NewModelTier(nil, ModelTierConfig{RefreshInterval: time.Minute})
	fs := &featuredSet{static: map[string]struct{}{}, usage: map[string]struct{}{}}
	for _, s := range static {
		if k := normalizeModelKey(s); k != "" {
			fs.static[k] = struct{}{}
		}
	}
	for _, s := range usage {
		if k := normalizeModelKey(s); k != "" {
			fs.usage[k] = struct{}{}
		}
	}
	m.cur.Store(fs)
	return m
}

func TestIsFeaturedModel_StaticAndUsage(t *testing.T) {
	m := newTierWithSet([]string{"gpt-5.4", "Claude-Sonnet"}, []string{"glm-5.2"})

	assert.True(t, m.IsFeaturedModel("gpt-5.4", ""))          // static hit (raw)
	assert.True(t, m.IsFeaturedModel("GPT-5.4", ""))          // case-insensitive
	assert.True(t, m.IsFeaturedModel("claude-sonnet", ""))    // static normalized
	assert.True(t, m.IsFeaturedModel("glm-5.2", ""))          // usage hit
	assert.False(t, m.IsFeaturedModel("deepseek-chat", ""))   // neither
	assert.False(t, m.IsFeaturedModel("", ""))                // empty

	// Canonical fallback: raw not in set but canonical is static.
	m2 := newTierWithSet([]string{"claude-sonnet-5"}, nil)
	assert.True(t, m2.IsFeaturedModel("anthropic/claude-sonnet-5", "claude-sonnet-5"))
	assert.False(t, m2.IsFeaturedModel("anthropic/claude-sonnet-5", ""))
}

func TestIsFeaturedModel_NilSafe(t *testing.T) {
	var m *ModelTier
	assert.False(t, m.IsFeaturedModel("gpt-5.4", "")) // nil receiver

	m2 := NewModelTier(nil, ModelTierConfig{}) // no snapshot stored
	assert.False(t, m2.IsFeaturedModel("gpt-5.4", "")) // empty snapshot
}

// TestGlobalIsFeaturedModel_AndPriority verifies the package-level accessor +
// FeaturedQueuePriority honor the global tier, and restore state afterward.
func TestGlobalIsFeaturedModel_AndPriority(t *testing.T) {
	prev := globalModelTier.Load()
	t.Cleanup(func() { globalModelTier.Store(prev) })

	SetGlobalModelTier(newTierWithSet([]string{"gpt-5.4"}, nil))
	assert.True(t, globalIsFeaturedModel("gpt-5.4", ""))
	assert.False(t, globalIsFeaturedModel("other-model", ""))

	// Featured ⇒ setting-driven priority (default 80 when settings Global nil).
	assert.Equal(t, int16(80), FeaturedQueuePriority("gpt-5.4", 60))
	// Non-featured ⇒ fallback.
	assert.Equal(t, int16(60), FeaturedQueuePriority("other-model", 60))
}

// TestModelTier_StopWithoutStart is a regression guard: Stop must be safe to
// call on a tier that was never started (done channel still open).
func TestModelTier_StopWithoutStart(t *testing.T) {
	m := NewModelTier(nil, ModelTierConfig{})
	done := make(chan struct{})
	go func() { m.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked on a never-started tier")
	}
}
