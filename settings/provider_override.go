package settings

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ProviderSettingsResolver resolves settings with provider-level override support.
// Resolution order: provider_settings > platform settings > default
type ProviderSettingsResolver struct {
	db          *pgxpool.Pool
	registry    *Registry
	cache       sync.Map // map[cacheKey]cacheEntry
	cacheMu     sync.Mutex
	cacheTTL    time.Duration
	cacheHitLog bool
	stopCleanup chan struct{} // P1-8 fix: signal to stop cleanup goroutine
	cleanupDone chan struct{}
	closeOnce   sync.Once
}

type cacheKey struct {
	providerID int
	key        string
}

type cacheEntry struct {
	value      interface{}
	hasValue   bool
	expireTime time.Time
}

// NewProviderSettingsResolver creates a new resolver with 5-minute cache TTL.
// P1-8 fix (2026-08-28): Starts a background goroutine to evict expired entries
// every 10 minutes, preventing unbounded sync.Map growth.
func NewProviderSettingsResolver(db *pgxpool.Pool, registry *Registry) *ProviderSettingsResolver {
	r := &ProviderSettingsResolver{
		db:          db,
		registry:    registry,
		cacheTTL:    5 * time.Minute,
		cacheHitLog: false, // set to true for debugging
		stopCleanup: make(chan struct{}),
		cleanupDone: make(chan struct{}),
	}
	if db == nil || registry == nil {
		close(r.cleanupDone)
	} else {
		go r.cleanupExpiredEntries()
	}
	return r
}

// Get retrieves a setting value with provider-level override support.
// Returns (value, true) if found, (nil, false) if not found or disabled.
func (r *ProviderSettingsResolver) Get(ctx context.Context, providerID int, key string) (interface{}, bool) {
	if r.db == nil || r.registry == nil {
		return nil, false
	}

	// Check cache first
	k := cacheKey{providerID: providerID, key: key}
	r.cacheMu.Lock()
	if cached, ok := r.cache.Load(k); ok {
		entry, valid := cached.(cacheEntry)
		if valid && time.Now().Before(entry.expireTime) {
			r.cacheMu.Unlock()
			if r.cacheHitLog {
				slog.Debug("provider_settings cache hit", "provider_id", providerID, "key", key)
			}
			return entry.value, entry.hasValue
		}
		// Expired, remove while holding the cache lock.
		r.cache.Delete(k)
	}
	r.cacheMu.Unlock()

	// Query from database
	value, hasValue := r.queryDB(ctx, providerID, key)

	// Cache the result (even if not found, to avoid repeated queries).
	r.cacheMu.Lock()
	r.cache.Store(k, cacheEntry{
		value:      value,
		hasValue:   hasValue,
		expireTime: time.Now().Add(r.cacheTTL),
	})
	r.cacheMu.Unlock()

	return value, hasValue
}

// GetString is a convenience method for string-typed settings.
func (r *ProviderSettingsResolver) GetString(ctx context.Context, providerID int, key string) (string, bool) {
	val, ok := r.Get(ctx, providerID, key)
	if !ok {
		return "", false
	}
	s, ok := val.(string)
	return s, ok
}

// GetBool is a convenience method for boolean-typed settings.
func (r *ProviderSettingsResolver) GetBool(ctx context.Context, providerID int, key string) (bool, bool) {
	val, ok := r.Get(ctx, providerID, key)
	if !ok {
		return false, false
	}
	b, ok := val.(bool)
	return b, ok
}

// GetInt64 is a convenience method for int64-typed settings.
func (r *ProviderSettingsResolver) GetInt64(ctx context.Context, providerID int, key string) (int64, bool) {
	val, ok := r.Get(ctx, providerID, key)
	if !ok {
		return 0, false
	}
	// Handle both int64 and float64 (JSON numbers)
	switch v := val.(type) {
	case int64:
		return v, true
	case float64:
		return int64(v), true
	case int:
		return int64(v), true
	default:
		return 0, false
	}
}

// queryDB queries the provider_settings table and falls back to platform/default.
func (r *ProviderSettingsResolver) queryDB(ctx context.Context, providerID int, key string) (interface{}, bool) {
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var settingValueJSON []byte
	var enabled bool

	// Query provider-specific override
	err := r.db.QueryRow(queryCtx, `
		SELECT setting_value, enabled
		FROM provider_settings
		WHERE provider_id = $1 AND setting_key = $2
	`, providerID, key).Scan(&settingValueJSON, &enabled)

	if err == nil && enabled {
		// Provider override found and enabled
		var value interface{}
		if err := json.Unmarshal(settingValueJSON, &value); err != nil {
			slog.Warn("provider_settings invalid JSON",
				"provider_id", providerID,
				"key", key,
				"error", err)
			return nil, false
		}
		slog.Debug("provider_settings override applied",
			"provider_id", providerID,
			"key", key,
			"value", value)
		return value, true
	}

	// No provider override or error, fallback to platform/default
	if r.registry != nil {
		platformValue, source, err := r.registry.EffectiveValue(ScopePlatform, key, "")
		if err == nil && len(platformValue) > 0 {
			var value interface{}
			if err := json.Unmarshal(platformValue, &value); err == nil {
				slog.Debug("provider_settings fallback to platform",
					"provider_id", providerID,
					"key", key,
					"source", source)
				return value, true
			}
		}
	}

	return nil, false
}

// ClearCache clears the entire settings cache. Useful for testing or forced refresh.
func (r *ProviderSettingsResolver) ClearCache() {
	r.cache.Range(func(key, value interface{}) bool {
		r.cache.Delete(key)
		return true
	})
	slog.Info("provider_settings cache cleared")
}

// ClearProviderCache clears cache entries for a specific provider.
func (r *ProviderSettingsResolver) ClearProviderCache(providerID int) {
	count := 0
	r.cache.Range(func(key, value interface{}) bool {
		k := key.(cacheKey)
		if k.providerID == providerID {
			r.cache.Delete(key)
			count++
		}
		return true
	})
	if count > 0 {
		slog.Info("provider_settings cache cleared for provider",
			"provider_id", providerID,
			"entries_cleared", count)
	}
}

// cleanupExpiredEntries runs in a background goroutine and periodically removes
// expired cache entries to prevent unbounded sync.Map growth (P1-8 fix).
func (r *ProviderSettingsResolver) cleanupExpiredEntries() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			now := time.Now()
			count := 0
			r.cacheMu.Lock()
			r.cache.Range(func(key, value interface{}) bool {
				entry, ok := value.(cacheEntry)
				if ok && now.After(entry.expireTime) {
					r.cache.Delete(key)
					count++
				}
				return true
			})
			r.cacheMu.Unlock()
			if count > 0 {
				slog.Debug("provider_settings: evicted expired cache entries", "count", count)
			}
		case <-r.stopCleanup:
			close(r.cleanupDone)
			return
		}
	}
}

// Close stops the background cleanup goroutine. It is safe to call repeatedly.
func (r *ProviderSettingsResolver) Close() {
	if r == nil || r.stopCleanup == nil || r.cleanupDone == nil {
		return
	}
	r.closeOnce.Do(func() {
		close(r.stopCleanup)
	})
	<-r.cleanupDone
}
