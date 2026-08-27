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
	"strings"

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

	// wrongTypePrefix is the substring Redis uses for WRONGTYPE Operation
	// errors (e.g., "WRONGTYPE Operation against a key holding the wrong kind
	// of value"). Substring match is used because go-redis does not expose a
	// typed sentinel for it.
	wrongTypePrefix = "WRONGTYPE"
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

// checkType runs TYPE once and returns the typed sentinel if the type
// disagrees. Shared by SafeHGetAll / SafeSMembers / SafeLRange.
//
// Note on TOCTOU: between this call and the subsequent read, another client
// may DEL + re-SET the key under a different type. Callers must still
// inspect the read command error for WRONGTYPE — see the fallback path in
// SafeHGetAll et al.
func checkType(ctx context.Context, client redis.Cmdable, key, want string) (string, error) {
	got, err := client.Type(ctx, key).Result()
	if err != nil {
		return "", fmt.Errorf("redis TYPE check failed: %w", err)
	}
	if got == "none" {
		return "", ErrKeyNotFound
	}
	if got != want {
		return got, &TypedError{
			Key:         key,
			Expected:    want,
			Actual:      got,
			OriginalErr: ErrWrongType,
		}
	}
	return got, nil
}

// isWrongType returns true for the race-induced WRONGTYPE error returned
// when another client DELs+SETs the key under a different type between our
// TYPE check and the read command.
func isWrongType(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, redis.Nil) {
		return false
	}
	return strings.Contains(err.Error(), wrongTypePrefix)
}

// SafeHGetAll performs HGETALL with a TYPE check to ensure the key is a hash.
//
// Returns:
//   - Non-empty map + nil error: successful read of an existing hash
//   - Empty map + nil error: key is an existing empty hash (zero field)
//   - nil + ErrKeyNotFound: key does not exist
//   - nil + *TypedError (Unwrap → ErrWrongType): key exists but is not a hash
//   - nil + other error: Redis network/auth error, OR race-induced WRONGTYPE
//     (reclassified to *TypedError so callers see one consistent error type)
//
// Concurrency: a TYPE-checked key may be DEL'd+SET to a different type by
// another client between our Type() and HGetAll() calls. The HGetAll() error
// is reclassified via isWrongType → TypedError so callers don't need a
// separate handler. This is the only path that converts a network error
// into a typed sentinel.
func SafeHGetAll(ctx context.Context, client redis.Cmdable, key string) (map[string]string, error) {
	if client == nil {
		return nil, fmt.Errorf("nil redis client")
	}
	if _, err := checkType(ctx, client, key, "hash"); err != nil {
		return nil, err
	}

	data, err := client.HGetAll(ctx, key).Result()
	if err != nil {
		if isWrongType(err) {
			// TOCTOU: the key's type flipped after our TYPE check.
			// Re-read the actual type so the TypedError is informative.
			currentType, _ := client.Type(ctx, key).Result()
			return nil, &TypedError{
				Key:         key,
				Expected:    "hash",
				Actual:      currentType,
				OriginalErr: err,
			}
		}
		return nil, fmt.Errorf("redis HGETALL failed after TYPE check: %w", err)
	}

	return data, nil
}

// SafeSMembers performs SMEMBERS with a TYPE check to ensure the key is a set.
//
// Returns the same error semantics as SafeHGetAll.
func SafeSMembers(ctx context.Context, client redis.Cmdable, key string) ([]string, error) {
	if client == nil {
		return nil, fmt.Errorf("nil redis client")
	}
	if _, err := checkType(ctx, client, key, "set"); err != nil {
		return nil, err
	}
	members, err := client.SMembers(ctx, key).Result()
	if err != nil {
		if isWrongType(err) {
			return nil, &TypedError{
				Key:         key,
				Expected:    "set",
				OriginalErr: err,
			}
		}
		return nil, fmt.Errorf("redis SMEMBERS failed after TYPE check: %w", err)
	}
	return members, nil
}

// SafeLRange performs LRANGE with a TYPE check to ensure the key is a list.
//
// Returns the same error semantics as SafeHGetAll.
func SafeLRange(ctx context.Context, client redis.Cmdable, key string, start, stop int64) ([]string, error) {
	if client == nil {
		return nil, fmt.Errorf("nil redis client")
	}
	if _, err := checkType(ctx, client, key, "list"); err != nil {
		return nil, err
	}
	items, err := client.LRange(ctx, key, start, stop).Result()
	if err != nil {
		if isWrongType(err) {
			return nil, &TypedError{
				Key:         key,
				Expected:    "list",
				OriginalErr: err,
			}
		}
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
	if client == nil {
		return nil, fmt.Errorf("nil redis client")
	}
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
