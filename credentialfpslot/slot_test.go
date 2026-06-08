package credentialfpslot

import (
	"context"
	"testing"
)

func TestEffectiveLimit(t *testing.T) {
	def := 5
	if EffectiveLimit(nil, def) == nil || *EffectiveLimit(nil, def) != 5 {
		t.Fatal("nil limit should default to 5")
	}
	zero := 0
	if EffectiveLimit(&zero, def) != nil {
		t.Fatal("0 should mean unlimited")
	}
	seven := 7
	if EffectiveLimit(&seven, def) == nil || *EffectiveLimit(&seven, def) != 7 {
		t.Fatal("explicit limit")
	}
}

func TestAcquireReleaseMemory(t *testing.T) {
	m := New(Config{DefaultLimit: 2, Enabled: true}, nil)
	ctx := context.Background()

	l1, ok := m.Acquire(ctx, 9, nil, "sess-a", "default")
	if !ok || l1 == nil {
		t.Fatal("expected lease 1")
	}
	l2, ok := m.Acquire(ctx, 9, nil, "sess-b", "default")
	if !ok || l2 == nil {
		t.Fatal("expected lease 2")
	}
	_, ok = m.Acquire(ctx, 9, nil, "sess-c", "default")
	if ok {
		t.Fatal("expected saturated")
	}
	m.Release(ctx, l1)
	l3, ok := m.Acquire(ctx, 9, nil, "sess-c", "default")
	if !ok || l3 == nil {
		t.Fatal("expected lease after release")
	}
	m.Release(ctx, l2)
	m.Release(ctx, l3)
}

func TestRoutingEligible(t *testing.T) {
	m := New(Config{DefaultLimit: 1, Enabled: true}, nil)
	ctx := context.Background()
	if !m.RoutingEligible(ctx, 3, nil, "new") {
		t.Fatal("should be eligible")
	}
	lease, _ := m.Acquire(ctx, 3, nil, "only", "default")
	if m.RoutingEligible(ctx, 3, nil, "other") {
		t.Fatal("should be saturated")
	}
	if !m.RoutingEligible(ctx, 3, nil, "only") {
		t.Fatal("pinned holder should stay eligible")
	}
	m.Release(ctx, lease)
}
