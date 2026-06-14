package autoroute

import (
	"testing"
)

func TestProfileWeights_AllProfilesSum(t *testing.T) {
	weights := DefaultProfileWeights()
	for _, p := range AllProfiles {
		w, ok := weights[p]
		if !ok {
			t.Fatalf("missing weights for %s", p)
		}
		sum := w.Sum()
		if sum < 50 {
			t.Fatalf("profile %s weights sum %.0f too low (likely misconfigured)", p, sum)
		}
		if sum > 200 {
			t.Fatalf("profile %s weights sum %.0f unreasonably high", p, sum)
		}
	}
}

func TestWeightsFor_FallbackToSmart(t *testing.T) {
	w := WeightsFor("unknown_profile")
	smart := WeightsFor(ProfileSmart)
	if w.Price != smart.Price {
		t.Fatalf("unknown profile should fall back to smart: got %+v want %+v", w, smart)
	}
}

func TestWeightsFor_CostFirst_HighPrice(t *testing.T) {
	c := WeightsFor(ProfileCostFirst)
	s := WeightsFor(ProfileSmart)
	if c.Price <= s.Price {
		t.Fatalf("cost-first should weight price higher than smart: cost=%v smart=%v", c.Price, s.Price)
	}
}

func TestWeightsFor_SpeedFirst_HighSpeed(t *testing.T) {
	s := WeightsFor(ProfileSpeedFirst)
	sm := WeightsFor(ProfileSmart)
	if s.Speed <= sm.Speed {
		t.Fatalf("speed-first should weight speed higher than smart: speed=%v smart=%v", s.Speed, sm.Speed)
	}
}

func TestAllProfiles_Complete(t *testing.T) {
	if len(AllProfiles) != 3 {
		t.Fatalf("expected 3 profiles, got %d", len(AllProfiles))
	}
}

func TestMemoryProfileStore_PutGet(t *testing.T) {
	store := NewMemoryProfileStore()
	store.now = func() (t2 time.Time) { return time.Unix(1_000_000, 0) }

	_ = store.Put(nil, 42, ProfileCostFirst, 30*time.Minute)

	got, ok := store.Get(nil, 42)
	if !ok || got != ProfileCostFirst {
		t.Fatalf("got (%q, %v), want (cost_first, true)", got, ok)
	}

	// Other key → miss
	if _, ok := store.Get(nil, 99); ok {
		t.Fatal("expected miss for unknown key")
	}
}

func TestMemoryProfileStore_Expiry(t *testing.T) {
	store := NewMemoryProfileStore()
	currentTime := time.Unix(1_000_000, 0)
	store.now = func() (t2 time.Time) { return currentTime }

	_ = store.Put(nil, 42, ProfileSpeedFirst, 1*time.Minute)

	// Advance clock past expiry
	currentTime = currentTime.Add(2 * time.Minute)

	_, ok := store.Get(nil, 42)
	if ok {
		t.Fatal("expected miss after TTL expiry")
	}
}

func TestMemoryProfileStore_Overwrite(t *testing.T) {
	store := NewMemoryProfileStore()
	store.now = func() (t2 time.Time) { return time.Unix(1_000_000, 0) }

	_ = store.Put(nil, 42, ProfileCostFirst, 30*time.Minute)
	_ = store.Put(nil, 42, ProfileSmart, 30*time.Minute)

	got, ok := store.Get(nil, 42)
	if !ok || got != ProfileSmart {
		t.Fatalf("got (%q, %v), want (smart, true) — overwrite failed", got, ok)
	}
}