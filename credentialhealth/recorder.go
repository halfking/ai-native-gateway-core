// Package credentialhealth implements intelligent credential availability tracking
// with sliding window call history, continuous failure detection, and concurrency auto-tuning.
package credentialhealth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// CallEntry represents a single LLM call result in the sliding window.
type CallEntry struct {
	RequestID string `json:"rid"`
	Timestamp int64  `json:"ts"`  // unix milliseconds
	Success   bool   `json:"ok"`
	LatencyMs int    `json:"lat"` // milliseconds
	ErrorKind string `json:"err,omitempty"` // empty if success
}

// Recorder writes call history to Redis LIST (sliding window).
type Recorder struct {
	client    *redis.Client
	windowTTL time.Duration // default 2h
	maxSize   int           // default 100
}

// NewRecorder creates a sliding window recorder.
func NewRecorder(client *redis.Client, windowTTL time.Duration, maxSize int) *Recorder {
	if windowTTL == 0 {
		windowTTL = 2 * time.Hour
	}
	if maxSize == 0 {
		maxSize = 100
	}
	return &Recorder{
		client:    client,
		windowTTL: windowTTL,
		maxSize:   maxSize,
	}
}

// Enabled returns true if Redis is available.
func (r *Recorder) Enabled() bool {
	return r != nil && r.client != nil
}

// Append adds a call entry to the sliding window (async safe, non-blocking).
func (r *Recorder) Append(ctx context.Context, credentialID int, model string, entry CallEntry) error {
	if !r.Enabled() {
		return nil // silently skip if Redis unavailable
	}

	key := r.redisKey(credentialID, model)
	jsonBytes, err := json.Marshal(entry)
	if err != nil {
		slog.Warn("failed to marshal call entry",
			"credential_id", credentialID,
			"model", model,
			"error", err)
		return err
	}

	// Use pipeline for atomicity: LPUSH + LTRIM + EXPIRE
	pipe := r.client.Pipeline()
	pipe.LPush(ctx, key, jsonBytes)
	pipe.LTrim(ctx, key, 0, int64(r.maxSize-1)) // keep most recent N
	pipe.Expire(ctx, key, r.windowTTL)
	_, err = pipe.Exec(ctx)

	if err != nil {
		slog.Warn("failed to append call history",
			"credential_id", credentialID,
			"model", model,
			"error", err)
		return err
	}

	slog.Debug("call history recorded",
		"credential_id", credentialID,
		"model", model,
		"success", entry.Success,
		"latency_ms", entry.LatencyMs)

	return nil
}

// GetRecent retrieves recent call entries within the time window.
// Returns entries sorted newest-first (same order as Redis LIST).
func (r *Recorder) GetRecent(ctx context.Context, credentialID int, model string, since time.Time) ([]CallEntry, error) {
	if !r.Enabled() {
		return nil, nil
	}

	key := r.redisKey(credentialID, model)
	
	// Read all entries from LIST (0 to maxSize-1)
	results, err := r.client.LRange(ctx, key, 0, int64(r.maxSize-1)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("redis lrange failed: %w", err)
	}

	sinceMilli := since.UnixMilli()
	entries := make([]CallEntry, 0, len(results))

	for _, jsonStr := range results {
		var e CallEntry
		if err := json.Unmarshal([]byte(jsonStr), &e); err != nil {
			slog.Warn("failed to unmarshal call entry",
				"credential_id", credentialID,
				"model", model,
				"json", jsonStr,
				"error", err)
			continue
		}

		// Filter by time window
		if e.Timestamp >= sinceMilli {
			entries = append(entries, e)
		}
	}

	return entries, nil
}

// redisKey constructs the Redis key for a credential-model pair.
func (r *Recorder) redisKey(credentialID int, model string) string {
	return fmt.Sprintf("llmgw:callhist:%d:%s", credentialID, model)
}

// Stats returns aggregated stats for the recent window (for debugging).
type Stats struct {
	Total       int
	Success     int
	Failed      int
	FailureRate float64
	ErrorKinds  map[string]int
}

// ComputeStats aggregates entries into stats.
func ComputeStats(entries []CallEntry) Stats {
	stats := Stats{
		Total:      len(entries),
		ErrorKinds: make(map[string]int),
	}

	for _, e := range entries {
		if e.Success {
			stats.Success++
		} else {
			stats.Failed++
			if e.ErrorKind != "" {
				stats.ErrorKinds[e.ErrorKind]++
			}
		}
	}

	if stats.Total > 0 {
		stats.FailureRate = float64(stats.Failed) / float64(stats.Total)
	}

	return stats
}
