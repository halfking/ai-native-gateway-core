package disguise

import (
	"testing"
	"time"
)

func TestPool_Headers(t *testing.T) {
	p := NewPool(0) // never rotate
	h := p.Headers()
	if h["User-Agent"] == "" {
		t.Error("User-Agent must be non-empty")
	}
	if h["Accept-Language"] == "" {
		t.Error("Accept-Language must be non-empty")
	}
	// Two consecutive calls should return strings from the same pool,
	// but possibly different ones.
	h2 := p.Headers()
	if h2["User-Agent"] == "" || h2["Accept-Language"] == "" {
		t.Error("Headers() must always return non-empty values")
	}
}

func TestPool_Stats(t *testing.T) {
	p := NewPool(time.Minute)
	stats := p.Stats()
	if enabled, _ := stats["enabled"].(bool); !enabled {
		t.Error("expected enabled=true")
	}
	if cnt, _ := stats["agent_count"].(int); cnt != len(defaultUserAgents) {
		t.Errorf("expected %d agents, got %d", len(defaultUserAgents), cnt)
	}
	if cnt, _ := stats["language_count"].(int); cnt != len(defaultAcceptLanguages) {
		t.Errorf("expected %d languages, got %d", len(defaultAcceptLanguages), cnt)
	}
}

func TestPool_NilSafe(t *testing.T) {
	var p *Pool
	h := p.Headers()
	if h == nil {
		t.Error("nil pool should return non-nil empty map")
	}
	if _, ok := h["User-Agent"]; !ok {
		t.Error("User-Agent key should be present (empty value is fine)")
	}
	stats := p.Stats()
	if enabled, _ := stats["enabled"].(bool); enabled {
		t.Error("nil pool should report enabled=false")
	}
	// MaybeRotate on nil should not panic.
	p.MaybeRotate()
}

func TestPool_DefaultsHaveVariety(t *testing.T) {
	// Sanity check: at least 30 UA strings and 20 language strings.
	if len(defaultUserAgents) < 30 {
		t.Errorf("expected >=30 UA strings, got %d", len(defaultUserAgents))
	}
	if len(defaultAcceptLanguages) < 20 {
		t.Errorf("expected >=20 language strings, got %d", len(defaultAcceptLanguages))
	}
	// Check that all UA strings contain a Mozilla/5.0 prefix.
	for i, ua := range defaultUserAgents {
		if len(ua) < 10 || ua[:10] != "Mozilla/5." {
			t.Errorf("UA[%d] does not start with Mozilla/5.: %q", i, ua)
		}
	}
}
