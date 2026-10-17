package redis

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	goredis "github.com/redis/go-redis/v9"
)

// SafeHGetAllBatch is the **chunked** entry point over SafeHGetAllPipeline.
//
// ── Why this exists (2026-10-18 §10.110.54/55) ──
//
// SafeHGetAllPipeline already exists and is used by
// domains/ursm/v2/store/{pipeline,probe_evidence}.go. The gap was never a
// missing bulk primitive — it was that the URSM v2 persist writer still calls
// the per-key SafeHGetAll, which is **two serial round trips** (TYPE then
// HGETALL). A ~1,800-key snapshot therefore costs ~3,600-3,800 round trips
// inside one 30s budget:
//
//	30,000 ms ÷ 3,800 ≈ 7.9 ms/round trip  ← the cliff
//
// Measured round-trip latency was 8.8ms (from 154) / 26.1ms (from 245), i.e.
// **past the cliff**, so Collect ate the whole budget and the single-transaction
// Flush was left with a few hundred ms — losing all ~1,800 rows per tick.
//
// SafeHGetAllPipeline removes the round trips but takes an unbounded key list:
// two Exec calls carrying 2N commands, whose HGETALL reply holds every hash in
// memory at once. At ~1,800 node keys that is a large burst. This wrapper adds
// the missing piece — **bounded chunks** — so callers get a bounded reply size
// *and* a bounded round-trip count (2 per chunk).
//
// It is deliberately a thin wrapper: the per-key semantics come from
// SafeHGetAllPipeline, and there is exactly one implementation of them.
// DefaultHashBatchSize is a measured operating point, not a guess.
//
// Read-only A/B against the production Redis (db14, ~1,800 node keys, three
// runs per size, from both gateways) gave:
//
//	batch    245 best/median    154 best/median
//	  128      600 /  665 ms      314 /  346 ms
//	  256      362 /  391 ms      222 /  385 ms
//	  512      254 /  315 ms      318 /  331 ms
//	 1024      285 /  395 ms      282 /  323 ms
//	 2048      279 /  385 ms      372 /  423 ms
//	 4096      415 /  425 ms      405 /  519 ms
//
// ⚠️ The "best" column disagrees between hosts (512 wins on 245, 256 on 154),
// so a single-host best-case run would have "proven" either value. The
// **median** agrees: 512 beats 256 on both. The margin is only ~20%, though —
// batch size is not a sensitive knob here; the whole curve sits ~100x below
// the per-key path, which took 20.8s (245) / 26.3s (154) for the same 1,800
// keys against a 30s budget that also has to cover the write.
const DefaultHashBatchSize = 512

// batchClient is the only capability needed: SafeHGetAllPipeline takes a
// pipeline, and a fresh one is created per chunk.
type batchClient interface {
	Pipeline() goredis.Pipeliner
}

// SafeHGetAllBatch reads several hashes, `maxBatchSize` keys per pipeline.
//
// Returned entries mirror SafeHGetAll(ctx, client, key) per key:
//
//	TYPE=none      → key absent from the map (SafeHGetAll: ErrKeyNotFound)
//	TYPE=hash      → its fields
//	TYPE=other     → *TypedError
//	read failure   → the error SafeHGetAllPipeline reports for that key
//
// Fail-fast is preserved: the first offending key aborts the call with a
// non-nil error, matching SafeHGetAll's contract — callers decide whether to
// skip missing keys by membership, and corruption still takes the whole cycle.
func SafeHGetAllBatch(ctx context.Context, client batchClient, keys []string, maxBatchSize int) (map[string]map[string]string, error) {
	if client == nil {
		return nil, fmt.Errorf("nil redis client")
	}
	if len(keys) == 0 {
		return map[string]map[string]string{}, nil
	}
	if maxBatchSize <= 0 {
		maxBatchSize = DefaultHashBatchSize
	}

	out := make(map[string]map[string]string, len(keys))
	for start := 0; start < len(keys); start += maxBatchSize {
		end := start + maxBatchSize
		if end > len(keys) {
			end = len(keys)
		}
		chunk := keys[start:end]
		results, err := SafeHGetAllPipeline(ctx, client.Pipeline(), chunk)
		if err != nil {
			return nil, err
		}
		for _, r := range results {
			if r.Err != nil {
				if errors.Is(r.Err, ErrKeyNotFound) {
					continue // absent keys are skipped, as callers do today
				}
				return nil, r.Err
			}
			out[r.Key] = r.Fields
		}
	}
	return out, nil
}

// BatchSizeFromEnv reads a positive batch size from the environment.
// Returns ok=false when unset, unparseable or non-positive, so callers keep
// their default rather than silently degrading to an unbounded batch.
func BatchSizeFromEnv(key string) (int, bool) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
