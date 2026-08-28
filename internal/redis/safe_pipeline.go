package redis

import (
	"context"
	"errors"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
)

// PipelineHashResult is the result for one key in SafeHGetAllPipeline.
// ErrKeyNotFound means the key was absent at either validation or read time;
// all other errors are preserved so callers can distinguish corruption from a
// transient Redis failure.
type PipelineHashResult struct {
	Key    string
	Fields map[string]string
	Err    error
}

// SafeHGetAllPipeline validates keys in one TYPE pipeline, then reads only
// keys confirmed to be hashes in a second HGETALL pipeline. A single pipeline
// cannot conditionally skip HGETALL after TYPE, so the two phases are
// required to avoid WRONGTYPE commands for known non-hash keys.
func SafeHGetAllPipeline(ctx context.Context, pipe goredis.Pipeliner, keys []string) ([]PipelineHashResult, error) {
	if pipe == nil {
		return nil, fmt.Errorf("nil redis pipeline")
	}
	results := make([]PipelineHashResult, len(keys))
	for i, key := range keys {
		results[i].Key = key
	}
	if len(keys) == 0 {
		return results, nil
	}

	types := make([]*goredis.StatusCmd, len(keys))
	for i, key := range keys {
		types[i] = pipe.Type(ctx, key)
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, goredis.Nil) {
		return results, fmt.Errorf("redis TYPE pipeline failed: %w", err)
	}

	hashIndexes := make([]int, 0, len(keys))
	for i, cmd := range types {
		kind, err := cmd.Result()
		if err != nil {
			if errors.Is(err, goredis.Nil) {
				results[i].Err = ErrKeyNotFound
			} else {
				results[i].Err = fmt.Errorf("redis TYPE failed for key %q: %w", keys[i], err)
			}
			continue
		}
		switch kind {
		case "none":
			results[i].Err = ErrKeyNotFound
		case "hash":
			hashIndexes = append(hashIndexes, i)
		default:
			results[i].Err = &TypedError{
				Key: keys[i], Expected: "hash", Actual: kind,
				OriginalErr: ErrWrongType,
			}
		}
	}
	if len(hashIndexes) == 0 {
		return results, nil
	}

	reads := make([]*goredis.MapStringStringCmd, len(hashIndexes))
	for j, i := range hashIndexes {
		reads[j] = pipe.HGetAll(ctx, keys[i])
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, goredis.Nil) {
		return results, fmt.Errorf("redis HGETALL pipeline failed: %w", err)
	}
	for j, i := range hashIndexes {
		fields, err := reads[j].Result()
		if err == nil {
			results[i].Fields = fields
			continue
		}
		if errors.Is(err, goredis.Nil) {
			results[i].Err = ErrKeyNotFound
		} else if isWrongType(err) {
			results[i].Err = &TypedError{
				Key: keys[i], Expected: "hash", OriginalErr: err,
			}
		} else {
			results[i].Err = fmt.Errorf("redis HGETALL failed for key %q: %w", keys[i], err)
		}
	}
	return results, nil
}
