package casdoor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RateLimiter implements token bucket rate limiting with Redis backend.
type RateLimiter struct {
	redis         *redis.Client
	keyPrefix     string
	mu            sync.RWMutex
	localBuckets  map[string]*TokenBucket // Fallback for when Redis is unavailable
}

// TokenBucket represents a token bucket for rate limiting.
type TokenBucket struct {
	Capacity       int       `json:"capacity"`        // Maximum tokens
	RefillRate     int       `json:"refill_rate"`     // Tokens per second
	Tokens         int       `json:"tokens"`          // Current tokens
	LastRefillTime time.Time `json:"last_refill_time"`
}

// RateLimitConfig holds rate limiter configuration.
type RateLimitConfig struct {
	RedisClient *redis.Client
	KeyPrefix   string // Default: "ratelimit:"
}

// NewRateLimiter creates a new rate limiter.
func NewRateLimiter(config *RateLimitConfig) *RateLimiter {
	if config == nil {
		config = &RateLimitConfig{}
	}

	if config.KeyPrefix == "" {
		config.KeyPrefix = "ratelimit:"
	}

	return &RateLimiter{
		redis:        config.RedisClient,
		keyPrefix:    config.KeyPrefix,
		localBuckets: make(map[string]*TokenBucket),
	}
}

// RateLimitRule defines rate limiting rules.
type RateLimitRule struct {
	Capacity   int           // Maximum requests
	RefillRate int           // Requests per second
	Window     time.Duration // Time window
}

// DefaultRules returns default rate limiting rules.
func DefaultRules() map[string]*RateLimitRule {
	return map[string]*RateLimitRule{
		"auth": {
			Capacity:   10,
			RefillRate: 2,
			Window:     time.Minute,
		},
		"api": {
			Capacity:   100,
			RefillRate: 20,
			Window:     time.Minute,
		},
		"admin": {
			Capacity:   50,
			RefillRate: 10,
			Window:     time.Minute,
		},
	}
}

// Allow checks if a request is allowed under rate limiting.
// Returns true if allowed, false if rate limited.
func (r *RateLimiter) Allow(ctx context.Context, key string, rule *RateLimitRule) (bool, error) {
	if rule == nil {
		return true, nil // No rule, allow by default
	}

	// Try Redis first
	if r.redis != nil {
		allowed, err := r.allowRedis(ctx, key, rule)
		if err == nil {
			return allowed, nil
		}
		// Fall through to local on Redis error
	}

	// Fallback to local token bucket
	return r.allowLocal(key, rule), nil
}

// AllowN checks if N requests are allowed under rate limiting.
func (r *RateLimiter) AllowN(ctx context.Context, key string, rule *RateLimitRule, n int) (bool, error) {
	if rule == nil || n <= 0 {
		return true, nil
	}

	// For simplicity, check n times
	// In production, this should be atomic
	for i := 0; i < n; i++ {
		allowed, err := r.Allow(ctx, key, rule)
		if err != nil {
			return false, err
		}
		if !allowed {
			return false, nil
		}
	}

	return true, nil
}

// GetRemaining returns the number of remaining requests.
func (r *RateLimiter) GetRemaining(ctx context.Context, key string, rule *RateLimitRule) (int, error) {
	if rule == nil {
		return rule.Capacity, nil
	}

	// Try Redis first
	if r.redis != nil {
		remaining, err := r.getRemainingRedis(ctx, key, rule)
		if err == nil {
			return remaining, nil
		}
	}

	// Fallback to local
	return r.getRemainingLocal(key, rule), nil
}

// Reset resets the rate limit for a key.
func (r *RateLimiter) Reset(ctx context.Context, key string) error {
	// Delete from Redis
	if r.redis != nil {
		fullKey := r.keyPrefix + key
		if err := r.redis.Del(ctx, fullKey).Err(); err != nil && err != redis.Nil {
			return fmt.Errorf("delete from redis: %w", err)
		}
	}

	// Delete from local
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.localBuckets, key)

	return nil
}

// allowRedis checks rate limit using Redis.
func (r *RateLimiter) allowRedis(ctx context.Context, key string, rule *RateLimitRule) (bool, error) {
	fullKey := r.keyPrefix + key
	now := time.Now().Unix()

	// Use Redis sliding window algorithm
	script := `
		local key = KEYS[1]
		local capacity = tonumber(ARGV[1])
		local window = tonumber(ARGV[2])
		local now = tonumber(ARGV[3])
		
		local trim_time = now - window
		redis.call('ZREMRANGEBYSCORE', key, 0, trim_time)
		
		local count = redis.call('ZCARD', key)
		
		if count < capacity then
			redis.call('ZADD', key, now, now)
			redis.call('EXPIRE', key, window)
			return 1
		else
			return 0
		end
	`

	result, err := r.redis.Eval(ctx, script, []string{fullKey},
		rule.Capacity, rule.Window.Seconds(), now).Int()
	if err != nil {
		return false, err
	}

	return result == 1, nil
}

// allowLocal checks rate limit using local token bucket.
func (r *RateLimiter) allowLocal(key string, rule *RateLimitRule) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	bucket, exists := r.localBuckets[key]
	if !exists {
		bucket = &TokenBucket{
			Capacity:       rule.Capacity,
			RefillRate:     rule.RefillRate,
			Tokens:         rule.Capacity,
			LastRefillTime: time.Now(),
		}
		r.localBuckets[key] = bucket
	}

	// Refill tokens
	now := time.Now()
	elapsed := now.Sub(bucket.LastRefillTime).Seconds()
	tokensToAdd := int(elapsed * float64(bucket.RefillRate))
	
	if tokensToAdd > 0 {
		bucket.Tokens += tokensToAdd
		if bucket.Tokens > bucket.Capacity {
			bucket.Tokens = bucket.Capacity
		}
		bucket.LastRefillTime = now
	}

	// Check if we have tokens
	if bucket.Tokens > 0 {
		bucket.Tokens--
		return true
	}

	return false
}

// getRemainingRedis gets remaining requests from Redis.
func (r *RateLimiter) getRemainingRedis(ctx context.Context, key string, rule *RateLimitRule) (int, error) {
	fullKey := r.keyPrefix + key
	now := time.Now().Unix()
	trimTime := now - int64(rule.Window.Seconds())

	// Remove old entries
	if err := r.redis.ZRemRangeByScore(ctx, fullKey, "0", fmt.Sprintf("%d", trimTime)).Err(); err != nil {
		return 0, err
	}

	// Count remaining
	count, err := r.redis.ZCard(ctx, fullKey).Result()
	if err != nil {
		return 0, err
	}

	remaining := rule.Capacity - int(count)
	if remaining < 0 {
		remaining = 0
	}

	return remaining, nil
}

// getRemainingLocal gets remaining requests from local bucket.
func (r *RateLimiter) getRemainingLocal(key string, rule *RateLimitRule) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	bucket, exists := r.localBuckets[key]
	if !exists {
		return rule.Capacity
	}

	// Refill tokens (read-only calculation)
	now := time.Now()
	elapsed := now.Sub(bucket.LastRefillTime).Seconds()
	tokensToAdd := int(elapsed * float64(bucket.RefillRate))
	
	tokens := bucket.Tokens + tokensToAdd
	if tokens > bucket.Capacity {
		tokens = bucket.Capacity
	}

	return tokens
}

// CleanupExpiredBuckets removes expired local buckets.
func (r *RateLimiter) CleanupExpiredBuckets(ctx context.Context, maxAge time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	count := 0
	now := time.Now()

	for key, bucket := range r.localBuckets {
		if now.Sub(bucket.LastRefillTime) > maxAge {
			delete(r.localBuckets, key)
			count++
		}
	}

	return count
}

// RateLimitStats returns rate limiting statistics.
type RateLimitStats struct {
	TotalBuckets  int `json:"total_buckets"`
	ActiveBuckets int `json:"active_buckets"`
}

// GetStats returns rate limiting statistics.
func (r *RateLimiter) GetStats(ctx context.Context) *RateLimitStats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stats := &RateLimitStats{
		TotalBuckets: len(r.localBuckets),
	}

	now := time.Now()
	for _, bucket := range r.localBuckets {
		if now.Sub(bucket.LastRefillTime) < time.Minute {
			stats.ActiveBuckets++
		}
	}

	return stats
}

// RateLimitMiddleware provides rate limiting for different operations.
type RateLimitMiddleware struct {
	limiter *RateLimiter
	rules   map[string]*RateLimitRule
}

// NewRateLimitMiddleware creates a new rate limit middleware.
func NewRateLimitMiddleware(limiter *RateLimiter, rules map[string]*RateLimitRule) *RateLimitMiddleware {
	if rules == nil {
		rules = DefaultRules()
	}

	return &RateLimitMiddleware{
		limiter: limiter,
		rules:   rules,
	}
}

// CheckAuth checks rate limit for authentication requests.
func (m *RateLimitMiddleware) CheckAuth(ctx context.Context, identifier string) (bool, error) {
	rule := m.rules["auth"]
	return m.limiter.Allow(ctx, "auth:"+identifier, rule)
}

// CheckAPI checks rate limit for API requests.
func (m *RateLimitMiddleware) CheckAPI(ctx context.Context, identifier string) (bool, error) {
	rule := m.rules["api"]
	return m.limiter.Allow(ctx, "api:"+identifier, rule)
}

// CheckAdmin checks rate limit for admin requests.
func (m *RateLimitMiddleware) CheckAdmin(ctx context.Context, identifier string) (bool, error) {
	rule := m.rules["admin"]
	return m.limiter.Allow(ctx, "admin:"+identifier, rule)
}
