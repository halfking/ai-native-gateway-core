package settings

import (
	"sync"
	"sync/atomic"
	"time"
)

const platformIntCacheTTL = 5 * time.Second

type cachedInt struct {
	value   atomic.Value
	expires atomic.Value
}

var platformIntCache sync.Map

// CachedPlatformInt reads a platform integer without querying PostgreSQL on
// every request. The short TTL preserves hot-reload behavior for operators.
func CachedPlatformInt(key string, fallback int) int {
	entryAny, _ := platformIntCache.LoadOrStore(key, &cachedInt{})
	entry := entryAny.(*cachedInt)
	now := time.Now()
	if expires, ok := entry.expires.Load().(time.Time); ok && now.Before(expires) {
		if value, ok := entry.value.Load().(int); ok {
			return value
		}
	}
	value := getPlatformInt(key, fallback)
	entry.value.Store(value)
	entry.expires.Store(now.Add(platformIntCacheTTL))
	return value
}
