package autoroute

import (
	"context"
	"sync"
	"testing"
	"time"
)

type countingClassifier struct {
	mu    sync.Mutex
	calls int
}

func (c *countingClassifier) Classify(_ context.Context, signals ClassificationSignals) (*Classification, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return &Classification{Primary: TaskCode, Confidence: 0.9, Signals: signals, Secondary: []TaskScore{{Task: TaskChat, Score: 0.1}}}, nil
}

func (c *countingClassifier) Name() string { return "counting" }

func TestClassificationCacheTTLAndLRU(t *testing.T) {
	now := time.Unix(100, 0)
	cache := NewClassificationCache(2, time.Second)
	result := &Classification{Primary: TaskCode, Secondary: []TaskScore{{Task: TaskChat, Score: 0.2}}}
	cache.Set("a", result, now)
	result.Secondary[0].Score = 0.8
	got, ok := cache.Get("a", now.Add(500*time.Millisecond))
	if !ok || got.Secondary[0].Score != 0.2 {
		t.Fatalf("cache did not isolate stored result: %#v, %v", got, ok)
	}
	cache.Set("b", result, now)
	cache.Set("c", result, now)
	if _, ok := cache.Get("a", now); ok {
		t.Fatal("expected least recently used entry to be evicted")
	}
	if _, ok := cache.Get("b", now.Add(2*time.Second)); ok {
		t.Fatal("expected entry to expire")
	}
	stats := cache.Stats()
	if stats.Evictions != 1 || stats.Expired != 1 || stats.Hits != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestClassificationCacheKeyIncludesVersionAndSignals(t *testing.T) {
	signals := ClassificationSignals{LastUserPrompt: "write code", ToolCount: 1}
	if ClassificationCacheKey("v1", signals) == ClassificationCacheKey("v2", signals) {
		t.Fatal("classifier version must affect cache key")
	}
	changed := signals
	changed.ToolCount = 2
	if ClassificationCacheKey("v1", signals) == ClassificationCacheKey("v1", changed) {
		t.Fatal("classification signals must affect cache key")
	}
}

func TestCachedClassifierAvoidsRepeatedClassification(t *testing.T) {
	base := &countingClassifier{}
	cache := NewClassificationCache(4, time.Hour)
	wrapped := NewCachedClassifier(base, cache, "v1")
	signals := ClassificationSignals{LastUserPrompt: "write code"}
	first, err := wrapped.Classify(context.Background(), signals)
	if err != nil {
		t.Fatal(err)
	}
	first.Secondary[0].Score = 0.99
	second, err := wrapped.Classify(context.Background(), signals)
	if err != nil {
		t.Fatal(err)
	}
	if base.calls != 1 {
		t.Fatalf("base classifier calls = %d, want 1", base.calls)
	}
	if second.Secondary[0].Score != 0.1 {
		t.Fatalf("cached result was not defensive copied: %+v", second.Secondary)
	}
}

func TestClassificationCacheConcurrentAccess(t *testing.T) {
	cache := NewClassificationCache(8, time.Minute)
	result := &Classification{Primary: TaskChat}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := string(rune('a' + i%4))
			cache.Set(key, result, time.Now())
			_, _ = cache.Get(key, time.Now())
		}(i)
	}
	wg.Wait()
	if cache.Stats().Entries > 8 {
		t.Fatal("cache exceeded configured capacity")
	}
}
