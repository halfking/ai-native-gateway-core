package streaming

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisFormatCache implements FormatCache interface using Redis.
type RedisFormatCache struct {
	redis  *redis.Client
	ttl    time.Duration
	prefix string
}

// NewRedisFormatCache creates a new Redis-backed format cache.
func NewRedisFormatCache(client *redis.Client, ttl time.Duration) *RedisFormatCache {
	if ttl == 0 {
		ttl = 24 * time.Hour // Default 24 hours
	}
	return &RedisFormatCache{
		redis:  client,
		ttl:    ttl,
		prefix: "llmgw:format:session:",
	}
}

// Get retrieves the cached format information for a session.
func (c *RedisFormatCache) Get(ctx context.Context, sessionID string) (*CachedFormat, error) {
	if sessionID == "" {
		return nil, nil
	}

	key := c.prefix + sessionID
	data, err := c.redis.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil // Not found
	}
	if err != nil {
		return nil, err
	}

	var cached CachedFormat
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, err
	}

	// Update usage statistics (fire and forget)
	cached.UseCount++
	cached.LastUsed = time.Now()
	go func() {
		// Use background context with timeout
		bgCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		c.Set(bgCtx, sessionID, &cached)
	}()

	return &cached, nil
}

// Set stores the format information for a session.
func (c *RedisFormatCache) Set(ctx context.Context, sessionID string, format *CachedFormat) error {
	if sessionID == "" || format == nil {
		return nil
	}

	key := c.prefix + sessionID
	data, err := json.Marshal(format)
	if err != nil {
		return err
	}

	return c.redis.Set(ctx, key, data, c.ttl).Err()
}

// Delete removes the cached format information for a session.
// Useful for forcing a fresh detection.
func (c *RedisFormatCache) Delete(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}

	key := c.prefix + sessionID
	return c.redis.Del(ctx, key).Err()
}

// GetStats returns statistics about format usage across all sessions.
func (c *RedisFormatCache) GetStats(ctx context.Context) (map[string]int, error) {
	stats := make(map[string]int)
	
	// Scan all keys with the format prefix
	iter := c.redis.Scan(ctx, 0, c.prefix+"*", 1000).Iterator()
	
	for iter.Next(ctx) {
		data, err := c.redis.Get(ctx, iter.Val()).Bytes()
		if err != nil {
			continue // Skip errors
		}
		
		var cached CachedFormat
		if err := json.Unmarshal(data, &cached); err != nil {
			continue
		}
		
		stats[cached.PatternID] += cached.UseCount
	}
	
	if err := iter.Err(); err != nil {
		return nil, err
	}
	
	return stats, nil
}

// GetSessionCount returns the number of sessions with cached format information.
func (c *RedisFormatCache) GetSessionCount(ctx context.Context) (int64, error) {
	// Count keys matching the prefix
	var count int64
	iter := c.redis.Scan(ctx, 0, c.prefix+"*", 0).Iterator()
	
	for iter.Next(ctx) {
		count++
	}
	
	if err := iter.Err(); err != nil {
		return 0, err
	}
	
	return count, nil
}

// Clear removes all cached format information (for maintenance).
func (c *RedisFormatCache) Clear(ctx context.Context) error {
	// Find all keys
	var keys []string
	iter := c.redis.Scan(ctx, 0, c.prefix+"*", 1000).Iterator()
	
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	
	if err := iter.Err(); err != nil {
		return err
	}
	
	// Delete in batches
	if len(keys) > 0 {
		return c.redis.Del(ctx, keys...).Err()
	}
	
	return nil
}

// GetByPattern retrieves all sessions using a specific format pattern.
func (c *RedisFormatCache) GetByPattern(ctx context.Context, patternID string) ([]string, error) {
	var sessions []string
	iter := c.redis.Scan(ctx, 0, c.prefix+"*", 1000).Iterator()
	
	for iter.Next(ctx) {
		data, err := c.redis.Get(ctx, iter.Val()).Bytes()
		if err != nil {
			continue
		}
		
		var cached CachedFormat
		if err := json.Unmarshal(data, &cached); err != nil {
			continue
		}
		
		if cached.PatternID == patternID {
			// Extract session ID from key
			sessionID := iter.Val()[len(c.prefix):]
			sessions = append(sessions, sessionID)
		}
	}
	
	if err := iter.Err(); err != nil {
		return nil, err
	}
	
	return sessions, nil
}

// NullFormatCache is a no-op implementation for when Redis is not available.
type NullFormatCache struct{}

// NewNullFormatCache creates a no-op format cache.
func NewNullFormatCache() *NullFormatCache {
	return &NullFormatCache{}
}

// Get always returns nil (no cache).
func (c *NullFormatCache) Get(ctx context.Context, sessionID string) (*CachedFormat, error) {
	return nil, nil
}

// Set does nothing.
func (c *NullFormatCache) Set(ctx context.Context, sessionID string, format *CachedFormat) error {
	return nil
}

// Delete does nothing.
func (c *NullFormatCache) Delete(ctx context.Context, sessionID string) error {
	return nil
}
