package settings

import (
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

// InvalidatePlatformInt removes one cached setting immediately after a
// successful DB write, so admin updates do not wait for the TTL.
func InvalidatePlatformInt(key string) {
	platformIntCache.Delete(key)
}

// InvalidateSettingsCache clears all cached platform integers when the
// settings backend is replaced during initialization or test setup.
func InvalidateSettingsCache() {
	platformIntCache = sync.Map{}
}
