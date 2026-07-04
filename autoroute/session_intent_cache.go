package autoroute

import (
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// 2026-07-04 V18: task-drift detection threshold.
// After this many cache hits, force reclassification to detect task drift
// (e.g., user starts with chat, then switches to code review with tools).
const intentCacheDriftThreshold = 50

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
	// 2026-07-04 V18 fix: task-drift detection. Count requests served
	// from this cached intent. After N hits (default 50), force
	// reclassification to catch drift (chat → code, or tool adoption).
	HitCount int
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

// NewRedisSessionIntentCache is a compatibility constructor for call sites
// that want a Redis-backed cache. The current implementation still uses the
// in-process cache semantics; Redis is accepted so startup wiring compiles
// while the distributed cache implementation lands separately.
func NewRedisSessionIntentCache(_ *redis.Client, ttl time.Duration) *SessionIntentCache {
	return NewSessionIntentCache(ttl)
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

// inferTaskFromSignals heuristically infers the task type from classification signals.
// This is a lightweight heuristic used only for drift detection, not for actual routing.
// It returns "" if signals are too weak to infer a task type, which means "no drift detection"
// and shouldReclassify will only rely on hard signal checks.
func inferTaskFromSignals(sigs ClassificationSignals) TaskType {
	// Hard signals take precedence
	if sigs.HasImages {
		return TaskVision
	}
	if sigs.EstimatedTokens > 50_000 {
		return TaskLongContext
	}
	if sigs.ToolCount >= 3 && sigs.HasToolResults {
		return TaskAgent
	}
	// Soft heuristics (only when tool count is present but below threshold)
	if sigs.ToolCount > 0 {
		return TaskAgent // Lighter agent threshold for drift detection
	}
	// Return empty string when no strong signal present - this disables drift detection
	// for pure text prompts, avoiding false positives
	return ""
}

// shouldReclassify checks if the current request signals conflict with
// the cached task type. Returns true when the request has fundamentally
// changed nature (e.g. user switched from chat to code, or added images).
//
// Only "hard override" signals trigger reclassification:
//   - HasImages → vision (regardless of cached type)
//   - EstimatedTokens > 50k → long_context
//   - ToolCount >= 3 + HasToolResults → agent
//   - HitCount >= intentCacheDriftThreshold → forced refresh (V18)
//   - Soft task drift detection (V21) → DetectSessionDrift
//
// Soft signals (keyword changes within the same task type) do NOT
// trigger reclassification — the session keeps its intent.
func shouldReclassify(cached TaskType, sigs ClassificationSignals, hitCount int) bool {
	// 2026-07-04 V18 fix: task-drift detection. After N hits on the same
	// cached intent, force reclassification to catch user behavior drift
	// (e.g., chat → code, or tool adoption). This prevents a session from
	// being permanently locked to a stale task type for its entire 10min TTL.
	if hitCount >= intentCacheDriftThreshold {
		return true
	}

	// 2026-07-05 V21 fix: Call DetectSessionDrift to catch soft task type changes
	// (e.g., chat → code review without hard signals). This fixes the V18 incomplete
	// fix where only hitCount threshold was used.
	//
	// Only call DetectSessionDrift when inferTaskFromSignals returns a non-empty task.
	// Empty string means signals are too weak to infer task type, so we skip drift detection
	// to avoid false positives on pure text prompts.
	inferredTask := inferTaskFromSignals(sigs)
	if inferredTask != "" && DetectSessionDrift(cached, inferredTask) {
		return true
	}

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
