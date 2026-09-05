package autoroute

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	ursmcache "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/cache"
)

// 2026-07-04 V18: task-drift detection threshold.
// After this many cache hits, force reclassification to detect task drift
// (e.g., user starts with chat, then switches to code review with tools).
const intentCacheDriftThreshold = 50

// IntentRedisStore 是 domains/ursm/v2/cache.IntentStore 的最小接口,
// autoroute 只依赖接口(与 routing.StickyRedisStore 同一模式)。
// autoroute → ursm/v2/cache 是同模块兄弟包, 无循环依赖
// (ursm/v2/cache 不 import autoroute, 仅注释引用)。
type IntentRedisStore interface {
	Set(ctx context.Context, sessionID string, in ursmcache.Intent, ttl time.Duration) error
	Get(ctx context.Context, sessionID string) (ursmcache.Intent, bool)
	Delete(ctx context.Context, sessionID string) error
}

// toCacheIntent 将内存态 CachedIntent 转为 Redis 持久化态 ursmcache.Intent。
// 自定义 string 类型 (TaskType/Profile) 显式 cast 为 string;
// ClassifiedAt/ExpiresAt 不入库 (Intent 用 LastSeen 自动盖戳)。
func toCacheIntent(in CachedIntent) ursmcache.Intent {
	return ursmcache.Intent{
		TaskType:     string(in.TaskType),
		WorkType:     in.WorkType,
		ChosenModel:  in.ChosenModel,
		CredentialID: in.CredentialID,
		Profile:      string(in.Profile),
		Confidence:   in.Confidence,
		Classifier:   in.Classifier,
		HitCount:     in.HitCount,
	}
}

// fromCacheIntent 反向转换: Redis 拉回的 Intent 还原为 CachedIntent。
// ClassifiedAt/ExpiresAt 留零值, 由回填内存时由 Put/调用方补戳。
func fromCacheIntent(ci ursmcache.Intent) CachedIntent {
	return CachedIntent{
		TaskType:     TaskType(ci.TaskType),
		WorkType:     ci.WorkType,
		ChosenModel:  ci.ChosenModel,
		CredentialID: ci.CredentialID,
		Profile:      Profile(ci.Profile),
		Confidence:   ci.Confidence,
		Classifier:   ci.Classifier,
		HitCount:     ci.HitCount,
	}
}

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
	WorkType     string
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
	mu         sync.RWMutex
	entries    map[string]CachedIntent
	ttl        time.Duration
	now        func() time.Time // injectable for tests
	redisStore IntentRedisStore // URSM v2 过渡: nil 时退化为纯内存
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

// SetRedisStore 注入 IntentRedisStore(URSM v2 过渡)。
// Put 双写内存+Redis; Get miss 后回源 Redis 并回填。nil 时退化为纯内存(旧行为)。
func (c *SessionIntentCache) SetRedisStore(store IntentRedisStore) {
	if c == nil {
		return
	}
	c.redisStore = store
}

// Get returns the cached intent for sessionID, or (zero, false) if
// not found or expired. Expired entries are lazily deleted.
//
// URSM v2 过渡: 内存 miss/expired 后, 若注入了 Redis store, 回源 Redis
// 并回填内存(读锁已释放, 回填走写锁, 不自死锁)。Redis 错误 fail-open(返回 miss)。
func (c *SessionIntentCache) Get(sessionID string) (CachedIntent, bool) {
	if c == nil || sessionID == "" {
		return CachedIntent{}, false
	}
	c.mu.RLock()
	intent, ok := c.entries[sessionID]
	c.mu.RUnlock()
	if !ok {
		return c.redisFallback(sessionID)
	}
	if c.now().After(intent.ExpiresAt) {
		c.mu.Lock()
		delete(c.entries, sessionID)
		c.mu.Unlock()
		return c.redisFallback(sessionID)
	}
	return intent, true
}

// redisFallback 在内存 miss 后回源 Redis。命中则转回 CachedIntent 并回填内存,
// 不命中或 Redis 错误则返回 (zero,false)。无锁期间发起 Redis 调用。
func (c *SessionIntentCache) redisFallback(sessionID string) (CachedIntent, bool) {
	if c.redisStore == nil {
		return CachedIntent{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	ci, ok := c.redisStore.Get(ctx, sessionID)
	cancel()
	if !ok {
		return CachedIntent{}, false
	}
	intent := fromCacheIntent(ci)
	// 回填内存, 补上 ClassifiedAt/ExpiresAt(否则下次 Get 会因零值 ExpiresAt 立即过期)
	now := c.now()
	intent.ClassifiedAt = now
	intent.ExpiresAt = now.Add(c.ttl)
	c.mu.Lock()
	c.entries[sessionID] = intent
	c.mu.Unlock()
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

	// Redis 双写(URSM v2 过渡): fail-open, 错误仅忽略不影响内存。
	// 无锁期间发起 Redis 调用。
	if c.redisStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_ = c.redisStore.Set(ctx, sessionID, toCacheIntent(intent), c.ttl) // fail-open
	}
}

// IncrementHit atomically increments the HitCount of the cached intent for
// sessionID and returns the updated intent. It performs the read-modify-write
// under a single write lock so concurrent requests on the same session each
// observe a distinct count (the prior Get→HitCount++→Put pattern read the same
// count and let the last Put win, undercounting hits and firing the drift
// threshold late).
//
// 2026-07-27 concurrency fix. Returns (zero, false) when the entry is absent
// or expired. On a hit it also refreshes the expiry like Put, so the cached
// intent stays live for the session's duration.
func (c *SessionIntentCache) IncrementHit(sessionID string) (CachedIntent, bool) {
	if c == nil || sessionID == "" {
		return CachedIntent{}, false
	}
	c.mu.Lock()
	intent, ok := c.entries[sessionID]
	if !ok || c.now().After(intent.ExpiresAt) {
		if ok {
			delete(c.entries, sessionID)
		}
		c.mu.Unlock()
		intent, ok = c.redisFallback(sessionID)
		if !ok {
			return CachedIntent{}, false
		}
		c.mu.Lock()
	}
	now := c.now()
	intent.HitCount++
	intent.ExpiresAt = now.Add(c.ttl)
	c.entries[sessionID] = intent
	c.mu.Unlock()

	if c.redisStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_ = c.redisStore.Set(ctx, sessionID, toCacheIntent(intent), c.ttl)
	}
	return intent, true
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
	if c.redisStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_ = c.redisStore.Delete(ctx, sessionID)
	}
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
