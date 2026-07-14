package resolve

import (
	"context"
	"testing"
	"time"
)

func TestPassthrough_NoDB(t *testing.T) {
	r := NewResolver("", 0)
	defer r.Stop()
	res := r.Resolve(context.Background(), "gpt-4o", "")
	if res.ResolutionPath != "direct" {
		t.Fatalf("expected direct, got %s", res.ResolutionPath)
	}
	if len(res.RawModels) != 1 || res.RawModels[0] != "gpt-4o" {
		t.Fatalf("expected [gpt-4o], got %v", res.RawModels)
	}
	if res.CanonicalID != nil {
		t.Fatal("expected nil canonical_id")
	}
}

func TestResolve_CacheHit(t *testing.T) {
	r := NewResolver("", 60*time.Second)
	defer r.Stop()
	r.cache["gpt-4o"] = cacheEntry{
		resolved: &Resolution{
			ClientModel:    "gpt-4o",
			RawModels:      []string{"gpt-4o"},
			ResolutionPath: "direct",
		},
		expiration: time.Now().Add(time.Hour),
	}
	for i := 0; i < 10; i++ {
		res := r.Resolve(context.Background(), "gpt-4o", "")
		if res.ResolutionPath != "direct" {
			t.Fatalf("call %d: expected direct, got %s", i, res.ResolutionPath)
		}
	}
	if r.CachedCount() != 1 {
		t.Fatalf("expected 1 cached entry, got %d", r.CachedCount())
	}
}

func TestResolve_CacheExpiry(t *testing.T) {
	r := NewResolver("", 50*time.Millisecond)
	defer r.Stop()
	r.cache["test-model"] = cacheEntry{
		resolved:   passthrough("test-model"),
		expiration: time.Now().Add(50 * time.Millisecond),
	}
	res1 := r.Resolve(context.Background(), "test-model", "")
	if res1.ResolutionPath != "direct" {
		t.Fatal("first call should be direct")
	}
	time.Sleep(80 * time.Millisecond)
	res2 := r.Resolve(context.Background(), "test-model", "")
	if res2.ResolutionPath != "direct" {
		t.Fatal("second call should be direct after expiry")
	}
}

func TestResolve_ProfileDifferentiation(t *testing.T) {
	r := NewResolver("", 60*time.Second)
	defer r.Stop()
	r.cache["model-x|cursor"] = cacheEntry{
		resolved:   passthrough("model-x"),
		expiration: time.Now().Add(time.Hour),
	}
	r.cache["model-x|copilot"] = cacheEntry{
		resolved:   passthrough("model-x"),
		expiration: time.Now().Add(time.Hour),
	}
	r.Resolve(context.Background(), "model-x", "cursor")
	r.Resolve(context.Background(), "model-x", "copilot")
	r.Resolve(context.Background(), "model-x", "cursor")
	if r.CachedCount() != 2 {
		t.Fatalf("expected 2 cache entries, got %d", r.CachedCount())
	}
}

func TestEvictExpired(t *testing.T) {
	r := NewResolver("", 0)
	defer r.Stop()
	r.cache["a"] = cacheEntry{
		resolved:   passthrough("a"),
		expiration: time.Now().Add(-1 * time.Hour),
	}
	r.cache["b"] = cacheEntry{
		resolved:   passthrough("b"),
		expiration: time.Now().Add(1 * time.Hour),
	}
	r.EvictExpired()
	if r.CachedCount() != 1 {
		t.Fatalf("expected 1 after eviction, got %d", r.CachedCount())
	}
}

func TestCacheKey(t *testing.T) {
	if cacheKey("GPT-4O", "") != "gpt-4o" {
		t.Fatalf("unexpected key: %s", cacheKey("GPT-4O", ""))
	}
	if cacheKey("GPT-4O-2024-08-06", "") != "gpt-4o" {
		t.Fatalf("unexpected normalized key: %s", cacheKey("GPT-4O-2024-08-06", ""))
	}
	if cacheKey("GPT-4O", "Cursor") != "gpt-4o|cursor" {
		t.Fatalf("unexpected key: %s", cacheKey("GPT-4O", "Cursor"))
	}
}

func TestLowerUnique(t *testing.T) {
	result := lowerUnique([]string{"GPT-4o", "gpt-4o", "GPT-4O-2024-08-06", ""})
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d: %v", len(result), result)
	}
	if result[0] != "gpt-4o" || result[1] != "gpt-4o-2024-08-06" {
		t.Fatalf("unexpected values: %v", result)
	}
}
