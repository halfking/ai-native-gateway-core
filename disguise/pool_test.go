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

	// HeadersForSlot on nil must also be safe.
	h2 := p.HeadersForSlot(3)
	if h2 == nil {
		t.Error("nil pool HeadersForSlot should return non-nil empty map")
	}
	if _, ok := h2["User-Agent"]; !ok {
		t.Error("HeadersForSlot should include User-Agent key")
	}
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

// TestPool_HeadersForSlot_Deterministic pins the core "stable after
// connection" contract: same slot → same UA on every call.
func TestPool_HeadersForSlot_Deterministic(t *testing.T) {
	p := NewPool(0) // never rotate, so the only variability is the slot index
	for _, slot := range []int{0, 3, 7, 42, 100} {
		slot := slot
		t.Run("", func(t *testing.T) {
			first := p.HeadersForSlot(slot)
			for i := 0; i < 100; i++ {
				next := p.HeadersForSlot(slot)
				if next["User-Agent"] != first["User-Agent"] {
					t.Fatalf("slot=%d: UA drifted at iter %d: first=%q got=%q",
						slot, i, first["User-Agent"], next["User-Agent"])
				}
				if next["Accept-Language"] != first["Accept-Language"] {
					t.Fatalf("slot=%d: Accept-Language drifted at iter %d: first=%q got=%q",
						slot, i, first["Accept-Language"], next["Accept-Language"])
				}
			}
		})
	}
}

// TestPool_HeadersForSlot_DifferentSlotsDifferentUAs: distinct slot
// indices should generally map to distinct UAs (the disguise goal:
// different virtual devices, each with their own stable UA).
func TestPool_HeadersForSlot_DifferentSlotsDifferentUAs(t *testing.T) {
	p := NewPool(0)
	seen := make(map[string]bool)
	for slot := 0; slot < 20; slot++ {
		ua := p.HeadersForSlot(slot)["User-Agent"]
		seen[ua] = true
	}
	// With 50+ UAs in the pool and 20 slots, we expect to see at least
	// min(20, len(agents)) distinct UAs, but allow collision. The
	// "at least 2" floor catches outright broken implementations.
	if len(seen) < 2 {
		t.Fatalf("expected at least 2 distinct UAs across 20 slots, got %d", len(seen))
	}
}

// TestPool_HeadersForSlot_NegativeFallsBackToRandom: slot < 0 (no
// acquired slot, i.e. stateless request) must NOT return a deterministic
// value — it falls back to random so the pool still gets exercised.
func TestPool_HeadersForSlot_NegativeFallsBackToRandom(t *testing.T) {
	p := NewPool(0)
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		ua := p.HeadersForSlot(-1)["User-Agent"]
		seen[ua] = true
	}
	// Random across 50+ UAs: expect at least a handful of distinct values
	// in 100 draws.
	if len(seen) < 5 {
		t.Fatalf("expected random fallback (slot<0) to vary; got only %d distinct UAs in 100 draws", len(seen))
	}
}

// TestPool_HeadersForSlot_Wraparound covers slot indices larger than
// the pool size: the modulo should wrap without panicking.
func TestPool_HeadersForSlot_Wraparound(t *testing.T) {
	p := NewPool(0)
	// len(defaultUserAgents) is the cap. Indices beyond must wrap.
	bigSlot := len(defaultUserAgents) + 3
	h := p.HeadersForSlot(bigSlot)
	if h["User-Agent"] == "" {
		t.Fatalf("expected non-empty UA for wraparound slot %d", bigSlot)
	}
	// It should equal the UA at slot 3.
	want := p.HeadersForSlot(3)["User-Agent"]
	if h["User-Agent"] != want {
		t.Fatalf("wraparound mismatch: slot %d should equal slot 3, got %q vs %q",
			bigSlot, h["User-Agent"], want)
	}
}

// TestDefaultPool_RotationIntervalIs30Min: the package-level default
// pool must use 30 min (not the old 5 min) so the slot-bound UA stays
// stable across normal-session durations.
func TestDefaultPool_RotationIntervalIs30Min(t *testing.T) {
	if DefaultPool.rotateEvery != 30*time.Minute {
		t.Fatalf("DefaultPool rotation interval changed: got %v, want 30m",
			DefaultPool.rotateEvery)
	}
}
