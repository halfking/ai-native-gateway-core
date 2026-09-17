package settings

import (
	"encoding/json"
	"sync"
	"time"
)

const platformIntCacheTTL = 5 * time.Second

type cachedInt struct {
	mu      sync.Mutex
	value   int
	expires time.Time
}

var platformIntCache sync.Map

// platformValueCacheTTL bounds staleness of the opt-in cached platform
// readers (CachedPlatformBool/String/Float). Keep it short: the uncached
// GetPlatform* family is pinned by hot-reload tests, so cached variants are
// only for call sites that explicitly accept a ≤5s reload window.
const platformValueCacheTTL = 5 * time.Second

// cachedValue memoizes one resolved platform setting (the EffectiveValue
// result after the DB → env → default chain) behind a short TTL. The
// registry pointer guards against cross-contamination when tests (or
// re-init) swap settings.Global: entries populated by a previous registry
// are ignored rather than served.
type cachedValue struct {
	mu       sync.Mutex
	registry *Registry
	raw      jsonRawMessage
	source   string
	fetched  bool
	expires  time.Time
}

var platformValueCache sync.Map

// cachedEffectiveRaw resolves a platform-scoped setting through
// Global.EffectiveValue at most once per TTL window per key. ok=false means
// "not cached, resolve failed" — callers fall back exactly as they would on
// an uncached error.
func cachedEffectiveRaw(key string) (raw jsonRawMessage, source string, ok bool) {
	entryAny, _ := platformValueCache.LoadOrStore(key, &cachedValue{})
	entry := entryAny.(*cachedValue)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	now := time.Now()
	if entry.fetched && entry.registry == Global && now.Before(entry.expires) {
		return entry.raw, entry.source, true
	}
	raw, source, err := Global.EffectiveValue(ScopePlatform, key, "")
	if err != nil || raw == nil {
		// Do not cache failures: a transient DB error must not pin
		// fallback behavior for a full TTL window.
		return nil, "", false
	}
	entry.raw = raw
	entry.source = source
	entry.registry = Global
	entry.fetched = true
	entry.expires = now.Add(platformValueCacheTTL)
	return raw, source, true
}

// InvalidatePlatformValue removes one cached setting immediately after a
// successful DB write, so admin updates do not wait for the TTL.
func InvalidatePlatformValue(key string) {
	platformValueCache.Delete(key)
}

// CachedPlatformInt reads a platform integer without querying PostgreSQL on
// every request. The short TTL preserves hot-reload behavior for operators.
func CachedPlatformInt(key string, fallback int) int {
	entryAny, _ := platformIntCache.LoadOrStore(key, &cachedInt{})
	entry := entryAny.(*cachedInt)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	now := time.Now()
	if now.Before(entry.expires) {
		return entry.value
	}
	value := getPlatformInt(key, fallback)
	entry.value = value
	entry.expires = now.Add(platformIntCacheTTL)
	return value
}

// CachedPlatformBool is the opt-in cached variant of GetPlatformBool for
// call sites that accept a ≤5s hot-reload window (see helpers.go for why
// GetPlatformBool itself must stay uncached).
func CachedPlatformBool(key string, fallback bool) bool {
	if Global == nil {
		return fallback
	}
	raw, _, ok := cachedEffectiveRaw(key)
	if !ok || len(raw) == 0 {
		return fallback
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return fallback
	}
	return v
}

// CachedPlatformString is the opt-in cached variant of GetPlatformString.
func CachedPlatformString(key, fallback string) string {
	if Global == nil {
		return fallback
	}
	raw, _, ok := cachedEffectiveRaw(key)
	if !ok || len(raw) == 0 {
		return fallback
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return fallback
	}
	return v
}

// CachedPlatformFloat is the opt-in cached variant of GetPlatformFloat.
func CachedPlatformFloat(key string, fallback float64) float64 {
	if Global == nil {
		return fallback
	}
	raw, _, ok := cachedEffectiveRaw(key)
	if !ok || len(raw) == 0 {
		return fallback
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return fallback
	}
	return v
}

// InvalidatePlatformInt removes one cached setting immediately after a
// successful DB write, so admin updates do not wait for the TTL.
func InvalidatePlatformInt(key string) {
	platformIntCache.Delete(key)
	InvalidatePlatformValue(key)
}

// InvalidateSettingsCache clears all cached platform values when the
// settings backend is replaced during initialization or test setup.
func InvalidateSettingsCache() {
	platformIntCache = sync.Map{}
	platformValueCache = sync.Map{}
}
