package bg

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeModelKey(t *testing.T) {
	cases := map[string]string{
		"GPT-5.4":       "gpt-5.4",
		" glm-5.2 ":     "glm-5.2",
		"Claude-Sonnet": "claude-sonnet",
		"":              "",
		"   ":           "",
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

	assert.True(t, m.IsFeaturedModel("gpt-5.4", ""))        // static hit (raw)
	assert.True(t, m.IsFeaturedModel("GPT-5.4", ""))        // case-insensitive
	assert.True(t, m.IsFeaturedModel("claude-sonnet", ""))  // static normalized
	assert.True(t, m.IsFeaturedModel("glm-5.2", ""))        // usage hit
	assert.False(t, m.IsFeaturedModel("deepseek-chat", "")) // neither
	assert.False(t, m.IsFeaturedModel("", ""))              // empty

	// Canonical fallback: raw not in set but canonical is static.
	m2 := newTierWithSet([]string{"claude-sonnet-5"}, nil)
	assert.True(t, m2.IsFeaturedModel("anthropic/claude-sonnet-5", "claude-sonnet-5"))
	assert.False(t, m2.IsFeaturedModel("anthropic/claude-sonnet-5", ""))
}

func TestIsFeaturedModel_NilSafe(t *testing.T) {
	var m *ModelTier
	assert.False(t, m.IsFeaturedModel("gpt-5.4", "")) // nil receiver

	m2 := NewModelTier(nil, ModelTierConfig{})         // no snapshot stored
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

// TestModelTier_SQLReferencesActualColumns (audit fix #12) is a frozen-golden
// guard: the SQL inside ModelTier.refresh must reference the real schema column
// names. It does not execute the query (that would need a DB) but it asserts
// the literal strings so a regression like "model" (a nonexistent column) is
// caught at unit-test time instead of at the 10min refresh.
func TestModelTier_SQLReferencesActualColumns(t *testing.T) {
	// We can't import the function body directly; instead we assert the source
	// text via string-grep on the SQL fragments we know are inside refresh().
	// The test fails if the developer accidentally references a missing column.
	mustContain := []string{
		"COALESCE(rl.outbound_model, rl.client_model)", // real model-name column
		"FROM request_logs_hot rl",                     // table exists
		"WHERE rl.success",                             // column exists
		"WHERE tenant_id = $1",                         // tenant scope (audit #3)
		"GROUP BY raw_model",                           // dedup (audit #11)
	}
	src := sourceFromFile(t, "model_tier.go")
	for _, fragment := range mustContain {
		assert.True(t, strings.Contains(src, fragment),
			"model_tier.go must contain %q (audit guard)", fragment)
	}
	// Guard: must NOT reference the bare `model` column from request_logs_hot.
	// Use a tight pattern: a SELECT/GROUP BY/ORDER BY column reference, not just
	// any substring (avoid false-positive matches like `raw_model`, `client_model`,
	// or `outbound_model`). `\bmodel\b` matches a bare token `model` only when
	// it's not preceded by `raw_` or any column prefix.
	notAllowed := regexp.MustCompile(`(?im)(?:select|where|group\s+by|order\s+by)\s+(?:\w+\.)?model\b`)
	assert.False(t, notAllowed.MatchString(src),
		"request_logs_hot has no `model` column; use COALESCE(outbound_model,client_model)")

	// Tier-aware priority must return the featured default when the global
	// tier recognises the model. We restore the previous global state on exit.
	prevTier := globalModelTier.Load()
	t.Cleanup(func() { globalModelTier.Store(prevTier) })
	SetGlobalModelTier(newTierWithSet([]string{"gpt-5.4"}, nil))
	assert.Equal(t, int16(80), FeaturedQueuePriority("gpt-5.4", 60),
		"featured priority must default to 80 (>= integrity 70)")
}

// sourceFromFile reads the file (relative to this package's source root) and
// returns its content. Kept tiny to avoid pulling in go/ast for a single
// string guard.
func sourceFromFile(t *testing.T, relPath string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	data, err := os.ReadFile(filepath.Join(dir, relPath))
	if err != nil {
		// Fallback for callers that run from a different cwd.
		data, err = os.ReadFile(relPath)
		if err != nil {
			t.Fatalf("read %s: %v", relPath, err)
		}
	}
	// Trim large trailing binary noise (none expected, but cheap insurance).
	if len(data) > 2<<20 {
		data = data[:2<<20]
	}
	return string(bytes.TrimSpace(data))
}
