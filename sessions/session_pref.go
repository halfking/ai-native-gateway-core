package sessions

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// SessionPreferenceTTL is the TTL for session credential preference.
// Per design spec: aligned with session TTL (7 days)
const SessionPreferenceTTL = 7 * 24 * time.Hour

// SessionPreference manages session-level credential preference.
// Lightweight mapping: which credential a session should prefer for routing.
//
// Design rationale (2026-06-26):
// - session_pref stores ONLY the credentialID (lightweight)
// - Used for routing priority: preferred credential goes first in candidate list
// - Cleared when session switches models (old credential may not support new model)
type SessionPreference struct {
	client *redis.Client
}

// NewSessionPreference creates a new SessionPreference manager.
func NewSessionPreference(client *redis.Client) *SessionPreference {
	return &SessionPreference{client: client}
}

// redisKey generates the Redis key for a session's credential preference.
// Format: session_pref:<sessionID>
func (sp *SessionPreference) redisKey(sessionID string) string {
	return fmt.Sprintf("session_pref:%s", sessionID)
}

// Get retrieves the preferred credential ID for a session.
// Returns (credentialID, true) if found, (0, false) otherwise.
func (sp *SessionPreference) Get(ctx context.Context, sessionID string) (int, bool) {
	key := sp.redisKey(sessionID)
	data, err := sp.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return 0, false
	}
	if err != nil {
		return 0, false
	}

	credID, err := strconv.Atoi(data)
	if err != nil {
		return 0, false
	}

	return credID, true
}

// Set stores the preferred credential ID for a session.
func (sp *SessionPreference) Set(ctx context.Context, sessionID string, credentialID int) error {
	key := sp.redisKey(sessionID)
	return sp.client.Set(ctx, key, credentialID, SessionPreferenceTTL).Err()
}

// Delete removes the preferred credential for a session.
// Used when:
// 1. Session switches models (old credential may not support new model)
// 2. Session explicitly wants to reset preference
func (sp *SessionPreference) Delete(ctx context.Context, sessionID string) error {
	key := sp.redisKey(sessionID)
	return sp.client.Del(ctx, key).Err()
}

// ClearOnModelSwitch deletes the session preference when model changes.
// Convenience method that combines Delete with logging.
func (sp *SessionPreference) ClearOnModelSwitch(ctx context.Context, sessionID string, oldModel, newModel string) error {
	if err := sp.Delete(ctx, sessionID); err != nil {
		return err
	}
	return nil
}
