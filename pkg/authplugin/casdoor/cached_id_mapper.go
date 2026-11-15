package casdoor

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// CachedIDMapper wraps IDMapper with Redis caching for performance.
// It caches ID mapping lookups to reduce database queries.
type CachedIDMapper struct {
	mapper *IDMapper
	cache  *redis.Client
	ttl    time.Duration
}

// NewCachedIDMapper creates a new cached ID mapper.
func NewCachedIDMapper(mapper *IDMapper, redisClient *redis.Client, ttl time.Duration) *CachedIDMapper {
	if ttl == 0 {
		ttl = 1 * time.Hour // Default TTL
	}
	return &CachedIDMapper{
		mapper: mapper,
		cache:  redisClient,
		ttl:    ttl,
	}
}

// GetByInternalID retrieves external ID by internal ID with caching.
func (m *CachedIDMapper) GetByInternalID(ctx context.Context, internalID int) (provider, externalID, tenantID string, err error) {
	// Try cache first
	cacheKey := fmt.Sprintf("id_map:internal:%d", internalID)
	cached, err := m.cache.HGetAll(ctx, cacheKey).Result()
	if err == nil && len(cached) > 0 {
		// Cache hit
		return cached["provider"], cached["external_id"], cached["tenant_id"], nil
	}

	// Cache miss - fetch from database
	provider, externalID, tenantID, err = m.mapper.GetByInternalID(ctx, internalID)
	if err != nil {
		return "", "", "", err
	}

	// Cache the result
	m.cacheMapping(ctx, cacheKey, map[string]interface{}{
		"provider":    provider,
		"external_id": externalID,
		"tenant_id":   tenantID,
	})

	return provider, externalID, tenantID, nil
}

// GetByExternalID retrieves internal ID by external ID with caching.
func (m *CachedIDMapper) GetByExternalID(ctx context.Context, provider, externalID, tenantID string) (int, error) {
	// Try cache first
	cacheKey := fmt.Sprintf("id_map:external:%s:%s:%s", provider, externalID, tenantID)
	cached, err := m.cache.Get(ctx, cacheKey).Int()
	if err == nil {
		// Cache hit
		return cached, nil
	}

	// Cache miss - fetch from database
	internalID, err := m.mapper.GetByExternalID(ctx, provider, externalID, tenantID)
	if err != nil {
		return 0, err
	}

	// Cache the result
	m.cache.Set(ctx, cacheKey, internalID, m.ttl)

	// Also cache reverse mapping
	reverseCacheKey := fmt.Sprintf("id_map:internal:%d", internalID)
	m.cacheMapping(ctx, reverseCacheKey, map[string]interface{}{
		"provider":    provider,
		"external_id": externalID,
		"tenant_id":   tenantID,
	})

	return internalID, nil
}

// GetByUsername retrieves internal ID and external ID by username with caching.
func (m *CachedIDMapper) GetByUsername(ctx context.Context, provider, username, tenantID string) (internalID int, externalID string, err error) {
	// Try cache first
	cacheKey := fmt.Sprintf("id_map:username:%s:%s:%s", provider, username, tenantID)
	cached, err := m.cache.HGetAll(ctx, cacheKey).Result()
	if err == nil && len(cached) > 0 {
		// Cache hit
		internalIDInt, _ := strconv.Atoi(cached["internal_id"])
		return internalIDInt, cached["external_id"], nil
	}

	// Cache miss - fetch from database
	internalID, externalID, err = m.mapper.GetByUsername(ctx, provider, username, tenantID)
	if err != nil {
		return 0, "", err
	}

	// Cache the result
	m.cacheMapping(ctx, cacheKey, map[string]interface{}{
		"internal_id": internalID,
		"external_id": externalID,
	})

	return internalID, externalID, nil
}

// MapOrCreate creates or retrieves ID mapping with cache invalidation.
func (m *CachedIDMapper) MapOrCreate(
	ctx context.Context,
	provider, externalID, tenantID, username, displayName, email string,
) (int, error) {
	// Create or get mapping
	internalID, err := m.mapper.MapOrCreate(ctx, provider, externalID, tenantID, username, displayName, email)
	if err != nil {
		return 0, err
	}

	// Invalidate related cache entries to ensure consistency
	// We invalidate rather than set because MapOrCreate might update existing records
	m.invalidateMapping(ctx, provider, externalID, tenantID, username, internalID)

	return internalID, nil
}

// InvalidateByInternalID invalidates cache for a given internal ID.
// This should be called after updating user data in the database.
func (m *CachedIDMapper) InvalidateByInternalID(ctx context.Context, internalID int) error {
	// Get mapping details to invalidate all related keys
	provider, externalID, tenantID, err := m.mapper.GetByInternalID(ctx, internalID)
	if err != nil {
		return err
	}

	// We need username to invalidate all keys, but we don't have a GetMapping method
	// So we'll invalidate what we can and rely on TTL for username cache
	m.cache.Del(ctx,
		fmt.Sprintf("id_map:internal:%d", internalID),
		fmt.Sprintf("id_map:external:%s:%s:%s", provider, externalID, tenantID),
	)

	return nil
}

// Delete deletes ID mapping with cache invalidation.
func (m *CachedIDMapper) Delete(ctx context.Context, internalID int) error {
	// Get mapping details before deletion for cache invalidation
	provider, externalID, tenantID, err := m.mapper.GetByInternalID(ctx, internalID)
	if err != nil {
		return err
	}

	// Delete from database
	err = m.mapper.Delete(ctx, internalID)
	if err != nil {
		return err
	}

	// Invalidate cache (we don't have username, but invalidate what we can)
	m.cache.Del(ctx,
		fmt.Sprintf("id_map:internal:%d", internalID),
		fmt.Sprintf("id_map:external:%s:%s:%s", provider, externalID, tenantID),
	)

	return nil
}

// ListByProvider lists mappings by provider (not cached - used for admin/batch operations).
func (m *CachedIDMapper) ListByProvider(ctx context.Context, provider, tenantID string, limit int) ([]Mapping, error) {
	// List operations are not cached as they're typically used for admin/batch operations
	return m.mapper.ListByProvider(ctx, provider, tenantID, limit)
}

// cacheMapping stores a hash map in cache.
func (m *CachedIDMapper) cacheMapping(ctx context.Context, key string, fields map[string]interface{}) {
	// Use HMSet for hash storage
	_, err := m.cache.HMSet(ctx, key, fields).Result()
	if err != nil {
		// Log error but don't fail the operation
		// Cache failures should not break the application
		return
	}
	m.cache.Expire(ctx, key, m.ttl)
}

// invalidateMapping removes all cache entries related to a mapping.
func (m *CachedIDMapper) invalidateMapping(ctx context.Context, provider, externalID, tenantID, username string, internalID int) {
	// Invalidate all possible cache keys for this mapping
	keys := []string{
		fmt.Sprintf("id_map:internal:%d", internalID),
		fmt.Sprintf("id_map:external:%s:%s:%s", provider, externalID, tenantID),
		fmt.Sprintf("id_map:username:%s:%s:%s", provider, username, tenantID),
	}

	for _, key := range keys {
		m.cache.Del(ctx, key)
	}
}

// ClearCache clears all ID mapping cache entries.
func (m *CachedIDMapper) ClearCache(ctx context.Context) error {
	// Use SCAN to find all id_map:* keys
	iter := m.cache.Scan(ctx, 0, "id_map:*", 0).Iterator()
	keys := []string{}
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return fmt.Errorf("failed to scan cache keys: %w", err)
	}

	// Delete in batches
	if len(keys) > 0 {
		if err := m.cache.Del(ctx, keys...).Err(); err != nil {
			return fmt.Errorf("failed to delete cache keys: %w", err)
		}
	}

	return nil
}

// GetCacheStats returns cache statistics for monitoring.
func (m *CachedIDMapper) GetCacheStats(ctx context.Context) (map[string]interface{}, error) {
	// Count total cached mappings
	iter := m.cache.Scan(ctx, 0, "id_map:*", 0).Iterator()
	count := 0
	for iter.Next(ctx) {
		count++
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan cache keys: %w", err)
	}

	// Get Redis info
	info, err := m.cache.Info(ctx, "stats").Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get redis info: %w", err)
	}

	return map[string]interface{}{
		"cached_mappings": count,
		"ttl_seconds":     int(m.ttl.Seconds()),
		"redis_info":      info,
	}, nil
}
