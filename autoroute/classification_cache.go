package autoroute

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// ClassificationCacheStats describes cache activity since creation or Reset.
type ClassificationCacheStats struct {
	Hits      uint64
	Misses    uint64
	Expired   uint64
	Evictions uint64
	Entries   int
}

type classificationCacheEntry struct {
	key       string
	value     *Classification
	expiresAt time.Time
	prev      *classificationCacheEntry
	next      *classificationCacheEntry
}

// ClassificationCache is a bounded, concurrent-safe TTL/LRU cache for
// classifier results. It is intentionally independent from SessionIntentCache:
// this cache keys request signals, not a session ID.
type ClassificationCache struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	items    map[string]*classificationCacheEntry
	head     *classificationCacheEntry
	tail     *classificationCacheEntry
	stats    ClassificationCacheStats
}

// NewClassificationCache creates a cache. Non-positive capacity is treated as
// disabled; non-positive TTL means entries never expire.
func NewClassificationCache(capacity int, ttl time.Duration) *ClassificationCache {
	if capacity < 0 {
		capacity = 0
	}
	return &ClassificationCache{
		capacity: capacity,
		ttl:      ttl,
		items:    make(map[string]*classificationCacheEntry),
	}
}

// Get returns a defensive copy of the cached classification.
func (c *ClassificationCache) Get(key string, now time.Time) (*Classification, bool) {
	if c == nil || key == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok {
		c.stats.Misses++
		return nil, false
	}
	if c.ttl > 0 && !now.Before(entry.expiresAt) {
		c.removeLocked(entry)
		c.stats.Expired++
		c.stats.Misses++
		return nil, false
	}
	c.moveToFrontLocked(entry)
	c.stats.Hits++
	return cloneClassification(entry.value), true
}

// Set stores a defensive copy of result. Empty keys/results are ignored.
func (c *ClassificationCache) Set(key string, result *Classification, now time.Time) {
	if c == nil || key == "" || result == nil || c.capacity == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.items[key]; ok {
		old.value = cloneClassification(result)
		old.expiresAt = c.expiry(now)
		c.moveToFrontLocked(old)
		return
	}
	entry := &classificationCacheEntry{key: key, value: cloneClassification(result), expiresAt: c.expiry(now)}
	c.items[key] = entry
	c.pushFrontLocked(entry)
	if len(c.items) > c.capacity {
		c.removeLocked(c.tail)
		c.stats.Evictions++
	}
}

func (c *ClassificationCache) expiry(now time.Time) time.Time {
	if c.ttl <= 0 {
		return time.Time{}
	}
	return now.Add(c.ttl)
}

// Stats returns a snapshot of cache activity.
func (c *ClassificationCache) Stats() ClassificationCacheStats {
	if c == nil {
		return ClassificationCacheStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	stats := c.stats
	stats.Entries = len(c.items)
	return stats
}

// Reset removes entries and counters. It is useful after classifier policy changes.
func (c *ClassificationCache) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*classificationCacheEntry)
	c.head, c.tail = nil, nil
	c.stats = ClassificationCacheStats{}
}

func (c *ClassificationCache) pushFrontLocked(entry *classificationCacheEntry) {
	entry.prev = nil
	entry.next = c.head
	if c.head != nil {
		c.head.prev = entry
	} else {
		c.tail = entry
	}
	c.head = entry
}

func (c *ClassificationCache) moveToFrontLocked(entry *classificationCacheEntry) {
	if c.head == entry {
		return
	}
	if entry.prev != nil {
		entry.prev.next = entry.next
	}
	if entry.next != nil {
		entry.next.prev = entry.prev
	} else {
		c.tail = entry.prev
	}
	c.pushFrontLocked(entry)
}

func (c *ClassificationCache) removeLocked(entry *classificationCacheEntry) {
	if entry == nil {
		return
	}
	if entry.prev != nil {
		entry.prev.next = entry.next
	} else {
		c.head = entry.next
	}
	if entry.next != nil {
		entry.next.prev = entry.prev
	} else {
		c.tail = entry.prev
	}
	delete(c.items, entry.key)
	entry.prev, entry.next = nil, nil
}

// ClassificationCacheVersion participates in the cache key so that a change
// to classification policy naturally invalidates previously cached results.
// Bump it whenever classifier logic (keywords, thresholds, weights) changes
// in a way that could alter results for identical signals.
const ClassificationCacheVersion = "v1"

// ClassificationCacheKey returns a stable key for request signals and policy version.
func ClassificationCacheKey(version string, signals ClassificationSignals) string {
	payload := fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%t\x00%t\x00%t\x00%s\x00%s\x00%s",
		version, signals.SystemPrompt, signals.MessageCount, signals.EstimatedTokens,
		signals.ToolCount, len(signals.LastUserPrompt), signals.HasImages, signals.HasCodeBlock,
		signals.HasToolResults, signals.Language, signals.ClientType, signals.LastUserPrompt,
	)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// CachedClassifier decorates any Classifier with an optional classification cache.
type CachedClassifier struct {
	base    Classifier
	cache   *ClassificationCache
	clock   func() time.Time
	version string
}

func NewCachedClassifier(base Classifier, cache *ClassificationCache, version string) *CachedClassifier {
	return &CachedClassifier{base: base, cache: cache, version: version, clock: time.Now}
}

// Compile-time guard: CachedClassifier must satisfy the Classifier interface
// so it can decorate any classifier handed to NewDecider.
var _ Classifier = (*CachedClassifier)(nil)

// Name implements Classifier; it delegates to the wrapped classifier so
// admin UI and Classification.Classifier stay consistent with the base.
func (c *CachedClassifier) Name() string {
	if c == nil || c.base == nil {
		return "cached"
	}
	return c.base.Name()
}

func (c *CachedClassifier) Classify(ctx context.Context, signals ClassificationSignals) (*Classification, error) {
	if c == nil || c.base == nil {
		return nil, fmt.Errorf("cached classifier: nil base classifier")
	}
	if c.cache == nil {
		return c.base.Classify(ctx, signals)
	}
	now := c.clock()
	key := ClassificationCacheKey(c.version, signals)
	lookupStarted := time.Now()
	if result, ok := c.cache.Get(key, now); ok {
		recordClassificationCacheMetric(true)
		recordClassificationMetrics(result, result.Classifier, "cache", time.Since(lookupStarted))
		return result, nil
	}
	recordClassificationCacheMetric(false)
	started := time.Now()
	result, err := c.base.Classify(ctx, signals)
	if err == nil && result != nil {
		c.cache.Set(key, result, c.clock())
		recordClassificationMetrics(result, result.Classifier, result.Classifier, time.Since(started))
	}
	return result, err
}

func cloneClassification(in *Classification) *Classification {
	if in == nil {
		return nil
	}
	out := *in
	out.Secondary = append([]TaskScore(nil), in.Secondary...)
	return &out
}
