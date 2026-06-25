package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// SessionPreferenceTTL is the TTL for session credential preference.
// Per design spec: aligned with session TTL (7 days)
const SessionPreferenceTTL = 7 * 24 * time.Hour

// SessionPrefValue is the value stored in Redis for a session credential preference.
// Stored as JSON: {"credential_id": 42, "model": "gpt-4"}
//
// V3.1 (2026-06-26): model field added to support model switch detection.
// Backward compatible: old plain-string values are parsed on read.
type SessionPrefValue struct {
	CredentialID int    `json:"credential_id"`
	Model        string `json:"model,omitempty"`
}

// SessionPreference manages session-level credential preference.
// Maps (sessionID) → (credentialID, model) for routing priority.
//
// Design (V3.1, 2026-06-26):
// - Used for routing priority: preferred credential goes first in candidate list
// - Cleared when session switches models (old credential may not support new model)
// - Model field enables detectAndHandleModelSwitch without extra Redis round-trip
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

// Get retrieves the preferred credential + model for a session.
// Returns the value if found, nil otherwise.
//
// Backward compatible: reads old plain-string values (credential_id only).
func (sp *SessionPreference) Get(ctx context.Context, sessionID string) (*SessionPrefValue, bool) {
	key := sp.redisKey(sessionID)
	data, err := sp.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, false
	}
	if err != nil {
		return nil, false
	}

	// Try JSON first (V3.1+ format)
	var val SessionPrefValue
	if err := json.Unmarshal([]byte(data), &val); err == nil {
		return &val, true
	}

	// Fall back to plain string (legacy format)
	credID, err := strconv.Atoi(data)
	if err != nil {
		return nil, false
	}
	return &SessionPrefValue{CredentialID: credID}, true
}

// GetModel returns the model associated with a session preference.
// Returns empty string if no preference or model not set.
func (sp *SessionPreference) GetModel(ctx context.Context, sessionID string) string {
	val, found := sp.Get(ctx, sessionID)
	if !found {
		return ""
	}
	return val.Model
}

// Set stores the preferred credential ID and model for a session.
// model may be empty for backward compatibility.
func (sp *SessionPreference) Set(ctx context.Context, sessionID string, credentialID int, model string) error {
	key := sp.redisKey(sessionID)
	val := SessionPrefValue{CredentialID: credentialID, Model: model}
	data, err := json.Marshal(val)
	if err != nil {
		return fmt.Errorf("json marshal session pref failed: %w", err)
	}
	return sp.client.Set(ctx, key, data, SessionPreferenceTTL).Err()
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
