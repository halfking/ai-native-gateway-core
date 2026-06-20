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
	cacheTTL    time.Duration
	cacheHitLog bool
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
func NewProviderSettingsResolver(db *pgxpool.Pool, registry *Registry) *ProviderSettingsResolver {
	return &ProviderSettingsResolver{
		db:          db,
		registry:    registry,
		cacheTTL:    5 * time.Minute,
		cacheHitLog: false, // set to true for debugging
	}
}

// Get retrieves a setting value with provider-level override support.
// Returns (value, true) if found, (nil, false) if not found or disabled.
func (r *ProviderSettingsResolver) Get(ctx context.Context, providerID int, key string) (interface{}, bool) {
	if r.db == nil || r.registry == nil {
		return nil, false
	}

	// Check cache first
	k := cacheKey{providerID: providerID, key: key}
	if cached, ok := r.cache.Load(k); ok {
		entry := cached.(cacheEntry)
		if time.Now().Before(entry.expireTime) {
			if r.cacheHitLog {
				slog.Debug("provider_settings cache hit", "provider_id", providerID, "key", key)
			}
			return entry.value, entry.hasValue
		}
		// Expired, remove from cache
		r.cache.Delete(k)
	}

	// Query from database
	value, hasValue := r.queryDB(ctx, providerID, key)

	// Cache the result (even if not found, to avoid repeated queries)
	r.cache.Store(k, cacheEntry{
		value:      value,
		hasValue:   hasValue,
		expireTime: time.Now().Add(r.cacheTTL),
	})

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
