package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RouteNodeState tracks health state for (credentialID, model) dimension.
// State belongs to the route node itself, not to sessions using it.
//
// Design decision (2026-06-26): failure counting is per-node, not per-session.
// When a session switches credentials or models, the old node's state persists.
type RouteNodeState struct {
	CredentialID  int               `json:"credential_id"`
	Model         string            `json:"model"` // credential matched model name (cand.RawModel)
	SuccessCount  int64             `json:"success_count"`
	FailureCount  int64             `json:"failure_count"`
	SlideWindow   []RouteNodeRecord `json:"slide_window"` // 5-minute sliding window
	LastSuccessAt time.Time         `json:"last_success_at"`
	LastFailureAt time.Time         `json:"last_failure_at"`
	// Consecutive 3 failures → cooldown 5 minutes
	Disabled       bool      `json:"disabled"`
	DisabledUntil  time.Time `json:"disabled_until"`
	DisabledReason string    `json:"disabled_reason,omitempty"`
}

// RouteNodeRecord is one request record in the sliding window.
type RouteNodeRecord struct {
	RequestID string    `json:"request_id"`
	Success   bool      `json:"success"`
	ErrorKind string    `json:"error_kind,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

const (
	// RouteNodeWindowSeconds is the sliding window size for tracking request history.
	RouteNodeWindowSeconds = 300 // 5 minutes

	// RouteNodeFailStreakLimit is the consecutive failure threshold before disabling.
	RouteNodeFailStreakLimit = 3

	// RouteNodeDisabledCooldown is the cooldown period after disabling (seconds).
	RouteNodeDisabledCooldown = 300 // 5 minutes

	// RouteNodeStateTTLSeconds is the Redis TTL for RouteNodeState.
	RouteNodeStateTTLSeconds = 3600 // 1 hour
)

// IsUsable determines if the route node is currently usable.
//
// Rules:
// 1. If cooling down → not usable
// 2. If cooldown expired → auto-recover, reset failure count
// 3. If consecutive failure streak >= 3 → not usable
func (n *RouteNodeState) IsUsable(now time.Time) bool {
	// 1. Cooling down → not usable
	if n.Disabled && now.Before(n.DisabledUntil) {
		return false
	}
	// 2. Cooldown expired → auto-recover, reset failure count
	if n.Disabled && !now.Before(n.DisabledUntil) {
		n.Disabled = false
		n.FailureCount = 0
		n.SlideWindow = []RouteNodeRecord{} // Reset slide window
	}
	// 3. Sliding window tail consecutive 3 failures → not usable
	return n.ConsecutiveFailureStreak() < RouteNodeFailStreakLimit
}

// ConsecutiveFailureStreak counts consecutive failures from the end of the sliding window.
func (n *RouteNodeState) ConsecutiveFailureStreak() int {
	n.PruneOldRecords(time.Now(), time.Duration(RouteNodeWindowSeconds)*time.Second)
	streak := 0
	for i := len(n.SlideWindow) - 1; i >= 0; i-- {
		if !n.SlideWindow[i].Success {
			streak++
		} else {
			break
		}
	}
	return streak
}

// PruneOldRecords removes records older than the window duration.
func (n *RouteNodeState) PruneOldRecords(now time.Time, window time.Duration) {
	cutoff := now.Add(-window)
	i := 0
	for i < len(n.SlideWindow) && n.SlideWindow[i].Timestamp.Before(cutoff) {
		i++
	}
	if i > 0 {
		n.SlideWindow = n.SlideWindow[i:]
	}
}

// RecordSuccess records a successful request.
func (n *RouteNodeState) RecordSuccess(requestID string, now time.Time) {
	n.SuccessCount++
	n.LastSuccessAt = now
	n.SlideWindow = append(n.SlideWindow, RouteNodeRecord{
		RequestID: requestID,
		Success:   true,
		Timestamp: now,
	})
	n.PruneOldRecords(now, time.Duration(RouteNodeWindowSeconds)*time.Second)
}

// RecordFailure records a failed request and checks if disabling is needed.
func (n *RouteNodeState) RecordFailure(requestID, errorKind string, now time.Time) {
	n.FailureCount++
	n.LastFailureAt = now
	n.SlideWindow = append(n.SlideWindow, RouteNodeRecord{
		RequestID: requestID,
		Success:   false,
		ErrorKind: errorKind,
		Timestamp: now,
	})
	n.PruneOldRecords(now, time.Duration(RouteNodeWindowSeconds)*time.Second)

	// Check if we need to disable
	if n.ConsecutiveFailureStreak() >= RouteNodeFailStreakLimit && !n.Disabled {
		n.Disabled = true
		n.DisabledUntil = now.Add(time.Duration(RouteNodeDisabledCooldown) * time.Second)
		n.DisabledReason = fmt.Sprintf("consecutive %d failures", RouteNodeFailStreakLimit)
	}
}

// RouteNodeStore encapsulates Redis operations for RouteNodeState.
type RouteNodeStore struct {
	client *redis.Client
}

// NewRouteNodeStore creates a new RouteNodeStore.
func NewRouteNodeStore(client *redis.Client) *RouteNodeStore {
	return &RouteNodeStore{client: client}
}

// redisKey generates the Redis key for a route node.
// Format: route_node:<credID>:<model>
func (s *RouteNodeStore) redisKey(credentialID int, model string) string {
	return fmt.Sprintf("route_node:%d:%s", credentialID, model)
}

// readState reads RouteNodeState from a generic redis Cmdable (client or tx).
func readState(cmdable redis.Cmdable, ctx context.Context, key string, credentialID int, model string) (*RouteNodeState, error) {
	data, err := cmdable.Get(ctx, key).Result()
	if err == redis.Nil {
		return &RouteNodeState{
			CredentialID: credentialID,
			Model:        model,
			SlideWindow:  []RouteNodeRecord{},
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("redis get failed: %w", err)
	}

	var state RouteNodeState
	if err := json.Unmarshal([]byte(data), &state); err != nil {
		return nil, fmt.Errorf("json unmarshal failed: %w", err)
	}
	return &state, nil
}

// isTransactionConflict checks if the error indicates a concurrent modification
// that should be retried. In real Redis this is redis.TxFailedErr; miniredis
// returns proto.RedisError with the same text.
func isTransactionConflict(err error) bool {
	if err == nil {
		return false
	}
	if err == redis.TxFailedErr {
		return true
	}
	return strings.Contains(err.Error(), "transaction failed")
}

// Get retrieves the route node state from Redis.
func (s *RouteNodeStore) Get(ctx context.Context, credentialID int, model string) (*RouteNodeState, error) {
	return readState(s.client, ctx, s.redisKey(credentialID, model), credentialID, model)
}

// Set stores the route node state to Redis (direct write, no optimistic locking).
// Used by tests to inject specific state. Production paths use RecordSuccess/RecordFailure
// which employ WATCH/MULTI/EXEC for atomicity.
func (s *RouteNodeStore) Set(ctx context.Context, state *RouteNodeState) error {
	key := s.redisKey(state.CredentialID, state.Model)
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("json marshal failed: %w", err)
	}

	ttl := time.Duration(RouteNodeStateTTLSeconds) * time.Second
	if err := s.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("redis set failed: %w", err)
	}
	return nil
}

// updateState atomically reads, applies updateFn, and writes RouteNodeState
// using Redis WATCH/MULTI/EXEC optimistic locking. Prevents lost updates
// under concurrent requests.
//
// Retries up to 10 times with exponential backoff on transaction conflict.
func (s *RouteNodeStore) updateState(ctx context.Context, credentialID int, model string, updateFn func(*RouteNodeState)) error {
	key := s.redisKey(credentialID, model)
	ttl := time.Duration(RouteNodeStateTTLSeconds) * time.Second
	const maxRetries = 10

	var lastErr error
	for attempt := range maxRetries + 1 {
		err := s.client.Watch(ctx, func(tx *redis.Tx) error {
			state, err := readState(tx, ctx, key, credentialID, model)
			if err != nil {
				return err
			}
			state.CredentialID = credentialID
			state.Model = model
			if state.SlideWindow == nil {
				state.SlideWindow = []RouteNodeRecord{}
			}

			updateFn(state)

			data, marshalErr := json.Marshal(state)
			if marshalErr != nil {
				return fmt.Errorf("json marshal failed in watch: %w", marshalErr)
			}

			_, pipeErr := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, key, data, ttl)
				return nil
			})
			return pipeErr
		}, key)

		if err == nil {
			return nil
		}

		if isTransactionConflict(err) && attempt < maxRetries {
			lastErr = err
			// Exponential backoff: 1ms, 2ms, 4ms, 8ms, ...
			select {
			case <-time.After(time.Duration(1<<attempt) * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		return err
	}

	return fmt.Errorf("update state failed after %d retries: %w", maxRetries, lastErr)
}

// RecordSuccess records a successful request and updates Redis atomically.
// Uses WATCH/MULTI/EXEC to prevent lost updates under concurrent requests.
func (s *RouteNodeStore) RecordSuccess(ctx context.Context, credentialID int, model, requestID string) error {
	return s.updateState(ctx, credentialID, model, func(state *RouteNodeState) {
		state.RecordSuccess(requestID, time.Now())
	})
}

// RecordFailure records a failed request and updates Redis atomically.
// Uses WATCH/MULTI/EXEC to prevent lost updates under concurrent requests.
func (s *RouteNodeStore) RecordFailure(ctx context.Context, credentialID int, model, requestID, errorKind string) error {
	return s.updateState(ctx, credentialID, model, func(state *RouteNodeState) {
		state.RecordFailure(requestID, errorKind, time.Now())
	})
}
