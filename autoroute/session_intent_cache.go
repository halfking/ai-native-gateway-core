package autoroute

import (
	"sync"
	"time"
)

// CachedIntent stores the auto-route decision for a session so that
// subsequent requests in the same session skip classification + scoring.
//
// Key = session_id (from X-Gw-Session-Id header).
// TTL = 10 minutes by default (configurable via Decider.IntentCacheTTL).
//
// The cache is process-local (in-memory). In multi-instance deployments
// (184 k3s + 71 docker), each instance maintains its own cache — this
// is acceptable because the sticky credential layer (routing/sticky.go)
// already handles cross-instance credential stickiness via DB.
type CachedIntent struct {
	TaskType     TaskType
	ChosenModel  string
	CredentialID int64
	Profile      Profile
	Confidence   float64
	Classifier   string
	ClassifiedAt time.Time
	ExpiresAt    time.Time
}

// SessionIntentCache is a thread-safe in-memory cache of per-session
// auto-route decisions.
//
// Usage:
//
//	cache := NewSessionIntentCache(10 * time.Minute)
//	if intent, ok := cache.Get(sessionID); ok {
//	    if !shouldReclassify(intent.TaskType, sigs) {
//	        return intent // cache hit, skip classification
//	    }
//	}
//	// ... classify + score ...
//	cache.Put(sessionID, intent)
type SessionIntentCache struct {
	mu      sync.RWMutex
	entries map[string]CachedIntent
	ttl     time.Duration
	now     func() time.Time // injectable for tests
}

// NewSessionIntentCache constructs a cache with the given TTL.
// Default TTL = 10 minutes.
func NewSessionIntentCache(ttl time.Duration) *SessionIntentCache {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &SessionIntentCache{
		entries: make(map[string]CachedIntent),
		ttl:     ttl,
		now:     time.Now,
	}
}

// Get returns the cached intent for sessionID, or (zero, false) if
// not found or expired. Expired entries are lazily deleted.
func (c *SessionIntentCache) Get(sessionID string) (CachedIntent, bool) {
	if c == nil || sessionID == "" {
		return CachedIntent{}, false
	}
	c.mu.RLock()
	intent, ok := c.entries[sessionID]
	c.mu.RUnlock()
	if !ok {
		return CachedIntent{}, false
	}
	if c.now().After(intent.ExpiresAt) {
		c.mu.Lock()
		delete(c.entries, sessionID)
		c.mu.Unlock()
		return CachedIntent{}, false
	}
	return intent, true
}

// Put stores the intent for sessionID with the configured TTL.
// No-op if sessionID is empty.
func (c *SessionIntentCache) Put(sessionID string, intent CachedIntent) {
	if c == nil || sessionID == "" {
		return
	}
	now := c.now()
	intent.ClassifiedAt = now
	intent.ExpiresAt = now.Add(c.ttl)
	c.mu.Lock()
	c.entries[sessionID] = intent
	c.mu.Unlock()
}

// Invalidate removes the cached intent for sessionID. Called when a
// decision fails or when the client explicitly requests reclassification.
func (c *SessionIntentCache) Invalidate(sessionID string) {
	if c == nil || sessionID == "" {
		return
	}
	c.mu.Lock()
	delete(c.entries, sessionID)
	c.mu.Unlock()
}

// Len returns the number of cached entries (for admin metrics).
func (c *SessionIntentCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// shouldReclassify checks if the current request signals conflict with
// the cached task type. Returns true when the request has fundamentally
// changed nature (e.g. user switched from chat to code, or added images).
//
// Only "hard override" signals trigger reclassification:
//   - HasImages → vision (regardless of cached type)
//   - EstimatedTokens > 50k → long_context
//   - ToolCount >= 3 + HasToolResults → agent
//
// Soft signals (keyword changes within the same task type) do NOT
// trigger reclassification — the session keeps its intent.
func shouldReclassify(cached TaskType, sigs ClassificationSignals) bool {
	// Vision override: images present but cached wasn't vision
	if sigs.HasImages && cached != TaskVision {
		return true
	}
	// Long context override
	if sigs.EstimatedTokens > 50_000 && cached != TaskLongContext {
		return true
	}
	// Agent override: tools appeared (>= 3 + has tool results)
	if sigs.ToolCount >= 3 && sigs.HasToolResults && cached != TaskAgent {
		return true
	}
	return false
}