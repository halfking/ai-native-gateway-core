package autoroute

import (
	"testing"
)

// TestResolve_FourLevelPriority verifies the documented 4-level resolution
// order: tenant+profile > tenant+generic > platform+profile > platform+generic.
// See docs/拆分/22-Auto智能路由与任务识别.md §22.6.
func TestResolve_FourLevelPriority(t *testing.T) {
	s := &DefaultRoutingStore{}
	tenantA := int64(42)
	tenantB := int64(99)
	tenantAPtr := new(int64)
	*tenantAPtr = tenantA

	// Seed the snapshot directly via the package-internal field.
	snap := &defaultRoutingSnapshot{
		byTask: map[string][]DefaultRouting{
			"code": {
				// platform generic (lowest)
				{ID: 1, TaskType: "code", Profile: "", Tier: RoutingPrimary, CanonicalModel: "platform-generic", Priority: 50},
				// platform profile=smart
				{ID: 2, TaskType: "code", Profile: "smart", Tier: RoutingPrimary, CanonicalModel: "platform-smart", Priority: 50},
				// tenant 42 generic
				{ID: 3, TaskType: "code", Profile: "", Tier: RoutingPrimary, CanonicalModel: "tenant-generic", TenantID: tenantAPtr, Priority: 50},
				// tenant 42 profile=smart (highest)
				{ID: 4, TaskType: "code", Profile: "smart", Tier: RoutingPrimary, CanonicalModel: "tenant-smart", TenantID: tenantAPtr, Priority: 50},
			},
		},
	}
	s.snapshot.Store(snap)

	// tenant 42 + smart → must hit tenant-smart (level 1)
	res, ok := s.Resolve("code", "smart", tenantA)
	if !ok || res.CanonicalModel != "tenant-smart" {
		t.Fatalf("level1: got model=%q ok=%v, want tenant-smart", res.CanonicalModel, ok)
	}
	if res.ScopeLevel != "tenant_profile" {
		t.Fatalf("level1 scope: got %q, want tenant_profile", res.ScopeLevel)
	}

	// tenant 42 + profile=cost_first (no profile-level tenant row) → tenant-generic (level 2)
	res, ok = s.Resolve("code", "cost_first", tenantA)
	if !ok || res.CanonicalModel != "tenant-generic" {
		t.Fatalf("level2: got model=%q ok=%v, want tenant-generic", res.CanonicalModel, ok)
	}
	if res.ScopeLevel != "tenant_generic" {
		t.Fatalf("level2 scope: got %q, want tenant_generic", res.ScopeLevel)
	}

	// tenant 99 (no tenant rows) + smart → platform-smart (level 3)
	res, ok = s.Resolve("code", "smart", tenantB)
	if !ok || res.CanonicalModel != "platform-smart" {
		t.Fatalf("level3: got model=%q ok=%v, want platform-smart", res.CanonicalModel, ok)
	}
	if res.ScopeLevel != "platform_profile" {
		t.Fatalf("level3 scope: got %q, want platform_profile", res.ScopeLevel)
	}

	// tenant 99 + cost_first → platform-generic (level 4)
	res, ok = s.Resolve("code", "cost_first", tenantB)
	if !ok || res.CanonicalModel != "platform-generic" {
		t.Fatalf("level4: got model=%q ok=%v, want platform-generic", res.CanonicalModel, ok)
	}
	if res.ScopeLevel != "platform_generic" {
		t.Fatalf("level4 scope: got %q, want platform_generic", res.ScopeLevel)
	}

	// tenantID <= 0 → only platform rows eligible; +smart → platform-smart
	res, ok = s.Resolve("code", "smart", 0)
	if !ok || res.CanonicalModel != "platform-smart" {
		t.Fatalf("no-tenant: got model=%q ok=%v, want platform-smart", res.CanonicalModel, ok)
	}
}

// TestResolve_NoMatchReturnsFalse: unknown task type → ok=false.
func TestResolve_NoMatchReturnsFalse(t *testing.T) {
	s := &DefaultRoutingStore{}
	s.snapshot.Store(&defaultRoutingSnapshot{
		byTask: map[string][]DefaultRouting{
			"code": {{TaskType: "code", Profile: "", CanonicalModel: "x"}},
		},
	})
	if _, ok := s.Resolve("vision", "smart", 1); ok {
		t.Fatal("want ok=false for unknown task")
	}
}

// TestResolve_PriorityDescWithinLevel: same level, higher priority wins.
func TestResolve_PriorityDescWithinLevel(t *testing.T) {
	s := &DefaultRoutingStore{}
	s.snapshot.Store(&defaultRoutingSnapshot{
		byTask: map[string][]DefaultRouting{
			"chat": {
				{TaskType: "chat", Profile: "", CanonicalModel: "low", Priority: 10},
				{TaskType: "chat", Profile: "", CanonicalModel: "high", Priority: 90},
				{TaskType: "chat", Profile: "", CanonicalModel: "mid", Priority: 50},
			},
		},
	})
	res, ok := s.Resolve("chat", "smart", 0)
	if !ok || res.CanonicalModel != "high" {
		t.Fatalf("priority: got %q ok=%v, want high", res.CanonicalModel, ok)
	}
}

// TestResolve_NeverLoadedReturnsEmpty: zero-value store never returns a hit.
func TestResolve_NeverLoadedReturnsEmpty(t *testing.T) {
	s := &DefaultRoutingStore{}
	if _, ok := s.Resolve("code", "smart", 1); ok {
		t.Fatal("never-loaded store must not resolve")
	}
}
