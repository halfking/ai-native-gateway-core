package routing

import (
	"context"
	"encoding/json"
	"fmt"
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

// Get retrieves the route node state from Redis.
func (s *RouteNodeStore) Get(ctx context.Context, credentialID int, model string) (*RouteNodeState, error) {
	key := s.redisKey(credentialID, model)
	data, err := s.client.Get(ctx, key).Result()
	if err == redis.Nil {
		// Not found, return empty state
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

// Set stores the route node state to Redis.
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

// RecordSuccess records a successful request and updates Redis.
func (s *RouteNodeStore) RecordSuccess(ctx context.Context, credentialID int, model, requestID string) error {
	state, err := s.Get(ctx, credentialID, model)
	if err != nil {
		return err
	}
	state.RecordSuccess(requestID, time.Now())
	return s.Set(ctx, state)
}

// RecordFailure records a failed request and updates Redis.
func (s *RouteNodeStore) RecordFailure(ctx context.Context, credentialID int, model, requestID, errorKind string) error {
	state, err := s.Get(ctx, credentialID, model)
	if err != nil {
		return err
	}
	state.RecordFailure(requestID, errorKind, time.Now())
	return s.Set(ctx, state)
}
