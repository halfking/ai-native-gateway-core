package resolve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestPassthrough_NoEndpoint(t *testing.T) {
	r := NewResolver("", 0)
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

func TestResolve_Canonical(t *testing.T) {
	cid := 42
	cname := "gpt-4o"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("model") != "gpt-4o" {
			t.Errorf("unexpected model param: %s", r.URL.Query().Get("model"))
		}
		json.NewEncoder(w).Encode(Resolution{
			ClientModel:    "gpt-4o",
			CanonicalID:    &cid,
			CanonicalName:  &cname,
			RawModels:      []string{"gpt-4o", "gpt-4o-2024-08-06"},
			ResolutionPath: "canonical",
		})
	}))
	defer srv.Close()

	r := NewResolver(srv.URL, 30*time.Second)
	res := r.Resolve(context.Background(), "gpt-4o", "")
	if res.ResolutionPath != "canonical" {
		t.Fatalf("expected canonical, got %s", res.ResolutionPath)
	}
	if res.CanonicalID == nil || *res.CanonicalID != 42 {
		t.Fatalf("expected canonical_id=42, got %v", res.CanonicalID)
	}
	if len(res.RawModels) != 2 {
		t.Fatalf("expected 2 raw models, got %d", len(res.RawModels))
	}
}

func TestResolve_CacheHit(t *testing.T) {
	calls := 0
	cid := 1
	cname := "claude-3.5"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(Resolution{
			ClientModel:    "claude-3.5",
			CanonicalID:    &cid,
			CanonicalName:  &cname,
			RawModels:      []string{"claude-3.5", "claude-3-5-sonnet"},
			ResolutionPath: "canonical",
		})
	}))
	defer srv.Close()

	r := NewResolver(srv.URL, 60*time.Second)

	for i := 0; i < 10; i++ {
		res := r.Resolve(context.Background(), "claude-3.5", "")
		if res.ResolutionPath != "canonical" {
			t.Fatalf("call %d: expected canonical, got %s", i, res.ResolutionPath)
		}
	}
	if calls != 1 {
		t.Fatalf("expected 1 upstream call, got %d", calls)
	}
	if r.CachedCount() != 1 {
		t.Fatalf("expected 1 cached entry, got %d", r.CachedCount())
	}
}

func TestResolve_CacheExpiry(t *testing.T) {
	calls := 0
	cid := 1
	cname := "test"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(Resolution{
			ClientModel:    "test-model",
			CanonicalID:    &cid,
			CanonicalName:  &cname,
			RawModels:      []string{"test-model"},
			ResolutionPath: "canonical",
		})
	}))
	defer srv.Close()

	r := NewResolver(srv.URL, 50*time.Millisecond)

	res1 := r.Resolve(context.Background(), "test-model", "")
	if res1.ResolutionPath != "canonical" {
		t.Fatal("first call should be canonical")
	}

	time.Sleep(80 * time.Millisecond)

	res2 := r.Resolve(context.Background(), "test-model", "")
	if res2.ResolutionPath != "canonical" {
		t.Fatal("second call should be canonical")
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls after expiry, got %d", calls)
	}
}

func TestResolve_UpstreamError_Passthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := NewResolver(srv.URL, 30*time.Second)
	res := r.Resolve(context.Background(), "unknown-model", "")
	if res.ResolutionPath != "direct" {
		t.Fatalf("expected direct on error, got %s", res.ResolutionPath)
	}
}

func TestResolve_ProfileDifferentiation(t *testing.T) {
	cid := 1
	cname := "model-x"
	seen := map[string]bool{}
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Query().Get("profile")] = true
		mu.Unlock()
		json.NewEncoder(w).Encode(Resolution{
			ClientModel:    "model-x",
			CanonicalID:    &cid,
			CanonicalName:  &cname,
			RawModels:      []string{"model-x"},
			ResolutionPath: "canonical",
		})
	}))
	defer srv.Close()

	r := NewResolver(srv.URL, 60*time.Second)
	r.Resolve(context.Background(), "model-x", "cursor")
	r.Resolve(context.Background(), "model-x", "copilot")
	r.Resolve(context.Background(), "model-x", "cursor")

	if len(seen) != 2 {
		t.Fatalf("expected 2 unique profiles, got %d: %v", len(seen), seen)
	}
	if r.CachedCount() != 2 {
		t.Fatalf("expected 2 cache entries (model-x|cursor, model-x|copilot), got %d", r.CachedCount())
	}
}

func TestEvictExpired(t *testing.T) {
	r := NewResolver("", 0)
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

func TestResolve_AliasPath(t *testing.T) {
	cid := 10
	cname := "glm-4.7"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Resolution{
			ClientModel:    "glm-4-7-251222",
			CanonicalID:    &cid,
			CanonicalName:  &cname,
			RawModels:      []string{"glm-4-7-251222", "glm-4.7"},
			ResolutionPath: "alias",
		})
	}))
	defer srv.Close()

	r := NewResolver(srv.URL, 30*time.Second)
	res := r.Resolve(context.Background(), "glm-4-7-251222", "")
	if res.ResolutionPath != "alias" {
		t.Fatalf("expected alias, got %s", res.ResolutionPath)
	}
	if *res.CanonicalID != 10 {
		t.Fatalf("expected canonical_id=10, got %d", *res.CanonicalID)
	}
}
