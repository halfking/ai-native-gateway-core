package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// RedisNonceTTL is how long we keep nonces in Redis (5 minutes)
	RedisNonceTTL = 5 * time.Minute

	// RedisNoncePrefix is the key prefix for nonces in Redis
	RedisNoncePrefix = "nonce:"
)

// redisNonceStore manages nonce replay prevention using Redis
type redisNonceStore struct {
	client *redis.Client
}

func newRedisNonceStore(client *redis.Client) *redisNonceStore {
	return &redisNonceStore{
		client: client,
	}
}

// check returns true if nonce is new (not seen before), false if it's a replay
func (rns *redisNonceStore) check(ctx context.Context, nonce string) (bool, error) {
	key := RedisNoncePrefix + nonce

	// Use SETNX to atomically check and set the nonce
	// Returns 1 if the key was set (new nonce), 0 if key already exists (replay)
	result, err := rns.client.SetNX(ctx, key, "1", RedisNonceTTL).Result()
	if err != nil {
		return false, fmt.Errorf("redis SETNX failed: %w", err)
	}

	return result, nil
}
