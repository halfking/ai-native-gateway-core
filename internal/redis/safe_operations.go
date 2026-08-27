// Package redis provides type-safe wrappers around Redis commands that can fail
// due to type mismatches or unexpected key states.
//
// Background: Redis commandstats show 60 HGETALL failed_calls due to type
// errors (attempting HGETALL on a non-hash key). These wrappers add defensive
// TYPE checks before performing operations, reducing silent failures and
// improving observability.
//
// Design: Fail-fast with typed errors rather than returning empty results that
// callers cannot distinguish from legitimate empty data structures.
package redis

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Common errors returned by safe operations.
var (
	// ErrWrongType indicates the key exists but is not the expected Redis type.
	// For example, attempting HGETALL on a string key.
	ErrWrongType = errors.New("redis: key exists but wrong type")

	// ErrKeyNotFound indicates the key does not exist. Callers can treat this
	// as an empty result if appropriate for their use case.
	ErrKeyNotFound = errors.New("redis: key not found")
)

// TypedError wraps a Redis operation error with the expected and actual types.
type TypedError struct {
	Key         string
	Expected    string // e.g., "hash", "set", "list"
	Actual      string // actual Redis TYPE result
	OriginalErr error
}

func (e *TypedError) Error() string {
	return fmt.Sprintf("redis type mismatch for key %q: expected %s, got %s: %v",
		e.Key, e.Expected, e.Actual, e.OriginalErr)
}

func (e *TypedError) Unwrap() error {
	return e.OriginalErr
}

// SafeHGetAll performs HGETALL with a TYPE check to ensure the key is a hash.
//
// Returns:
//   - Non-empty map + nil error: successful read of an existing hash
//   - Empty map + ErrKeyNotFound: key does not exist (safe to treat as empty)
//   - Empty map + ErrWrongType: key exists but is not a hash (caller should log/alert)
//   - Empty map + other error: Redis network/auth error
//
// Usage:
//
//	data, err := SafeHGetAll(ctx, rdb, "session:123")
//	if err != nil {
//	    if errors.Is(err, redis.ErrKeyNotFound) {
//	        // Key missing, treat as empty session
//	        return defaultSession()
//	    }
//	    if errors.Is(err, redis.ErrWrongType) {
//	        // Type corruption detected, log and fail open
//	        slog.Error("redis type mismatch", "key", "session:123", "error", err)
//	        return defaultSession()
//	    }
//	    return fmt.Errorf("redis error: %w", err)
//	}
//	// Use data...
func SafeHGetAll(ctx context.Context, client redis.Cmdable, key string) (map[string]string, error) {
	// Step 1: Check key type before attempting HGETALL
	keyType, err := client.Type(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TYPE check failed: %w", err)
	}

	// Step 2: Handle missing key (TYPE returns "none")
	if keyType == "none" {
		return nil, ErrKeyNotFound
	}

	// Step 3: Verify key is a hash
	if keyType != "hash" {
		return nil, &TypedError{
			Key:         key,
			Expected:    "hash",
			Actual:      keyType,
			OriginalErr: ErrWrongType,
		}
	}

	// Step 4: Perform HGETALL (should never fail now)
	data, err := client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("redis HGETALL failed after TYPE check: %w", err)
	}

	return data, nil
}

// SafeSMembers performs SMEMBERS with a TYPE check to ensure the key is a set.
//
// Returns the same error semantics as SafeHGetAll.
func SafeSMembers(ctx context.Context, client redis.Cmdable, key string) ([]string, error) {
	keyType, err := client.Type(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TYPE check failed: %w", err)
	}

	if keyType == "none" {
		return nil, ErrKeyNotFound
	}

	if keyType != "set" {
		return nil, &TypedError{
			Key:         key,
			Expected:    "set",
			Actual:      keyType,
			OriginalErr: ErrWrongType,
		}
	}

	members, err := client.SMembers(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("redis SMEMBERS failed after TYPE check: %w", err)
	}

	return members, nil
}

// SafeLRange performs LRANGE with a TYPE check to ensure the key is a list.
//
// Returns the same error semantics as SafeHGetAll.
func SafeLRange(ctx context.Context, client redis.Cmdable, key string, start, stop int64) ([]string, error) {
	keyType, err := client.Type(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TYPE check failed: %w", err)
	}

	if keyType == "none" {
		return nil, ErrKeyNotFound
	}

	if keyType != "list" {
		return nil, &TypedError{
			Key:         key,
			Expected:    "list",
			Actual:      keyType,
			OriginalErr: ErrWrongType,
		}
	}

	items, err := client.LRange(ctx, key, start, stop).Result()
	if err != nil {
		return nil, fmt.Errorf("redis LRANGE failed after TYPE check: %w", err)
	}

	return items, nil
}

// SafeMGet performs MGET with optional batch size limiting.
//
// If maxBatchSize > 0 and len(keys) > maxBatchSize, splits the request into
// multiple batches to avoid overwhelming Redis with a single large MGET.
//
// Returns values aligned with keys (value is nil for missing keys).
func SafeMGet(ctx context.Context, client redis.Cmdable, keys []string, maxBatchSize int) ([]interface{}, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	// No batching needed
	if maxBatchSize <= 0 || len(keys) <= maxBatchSize {
		return client.MGet(ctx, keys...).Result()
	}

	// Split into batches
	results := make([]interface{}, 0, len(keys))
	for i := 0; i < len(keys); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[i:end]
		vals, err := client.MGet(ctx, batch...).Result()
		if err != nil {
			return nil, fmt.Errorf("redis MGET batch [%d:%d] failed: %w", i, end, err)
		}
		results = append(results, vals...)
	}

	return results, nil
}
