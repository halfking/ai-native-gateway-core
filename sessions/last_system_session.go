package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// LastSystemSessionEntry tracks the last system-assigned session for a client.
// Used to enable 5-minute session reuse for clients that don't provide session IDs.
//
// Design rationale (2026-06-26): Many client SDKs don't send stable session IDs.
// Gateway assigns "gw_<uuid>" but that creates session fragmentation. To maintain
// session continuity (for prompt cache), reuse the last system-assigned session
// within a 5-minute window.
type LastSystemSessionEntry struct {
	SessionID      string    `json:"session_id"`
	LastAssignedAt time.Time `json:"last_assigned_at"`
	DeviceSeed     string    `json:"device_seed,omitempty"`
	TaskID         string    `json:"task_id,omitempty"`
}

const (
	// LastSystemSessionTTL is the reuse window for system-assigned sessions.
	// Per design spec: "5分钟内同 client 无 id 请求复用 last_system_session"
	LastSystemSessionTTL = 5 * time.Minute
)

// LastSystemSessionIndex manages the last system-assigned session per client.
type LastSystemSessionIndex struct {
	client *redis.Client
}

// NewLastSystemSessionIndex creates a new index.
func NewLastSystemSessionIndex(client *redis.Client) *LastSystemSessionIndex {
	return &LastSystemSessionIndex{client: client}
}

// redisKey generates the Redis key for a client's last system session.
// Format: client:<apiKeyID>:last_system_session
func (idx *LastSystemSessionIndex) redisKey(apiKeyID int) string {
	return fmt.Sprintf("client:%d:last_system_session", apiKeyID)
}

// Get retrieves the last system-assigned session for a client.
// Returns (entry, true) if found and not expired, (nil, false) otherwise.
func (idx *LastSystemSessionIndex) Get(ctx context.Context, apiKeyID int) (*LastSystemSessionEntry, bool) {
	key := idx.redisKey(apiKeyID)
	data, err := idx.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, false
	}
	if err != nil {
		// Log error but treat as miss (graceful degradation)
		return nil, false
	}

	var entry LastSystemSessionEntry
	if err := json.Unmarshal([]byte(data), &entry); err != nil {
		return nil, false
	}

	// Check if entry is still within the 5-minute window
	if time.Since(entry.LastAssignedAt) > LastSystemSessionTTL {
		return nil, false
	}

	return &entry, true
}

// Set stores the last system-assigned session for a client.
func (idx *LastSystemSessionIndex) Set(ctx context.Context, apiKeyID int, entry *LastSystemSessionEntry) error {
	key := idx.redisKey(apiKeyID)
	entry.LastAssignedAt = time.Now()

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("json marshal failed: %w", err)
	}

	if err := idx.client.Set(ctx, key, data, LastSystemSessionTTL).Err(); err != nil {
		return fmt.Errorf("redis set failed: %w", err)
	}

	return nil
}

// Delete removes the last system session entry for a client.
// Used when we want to force a new session on the next request.
func (idx *LastSystemSessionIndex) Delete(ctx context.Context, apiKeyID int) error {
	key := idx.redisKey(apiKeyID)
	return idx.client.Del(ctx, key).Err()
}

// Touch updates the LastAssignedAt timestamp without changing the session ID.
// Used when reusing an existing system session to extend the 5-minute window.
func (idx *LastSystemSessionIndex) Touch(ctx context.Context, apiKeyID int) error {
	entry, found := idx.Get(ctx, apiKeyID)
	if !found {
		return fmt.Errorf("no entry to touch")
	}
	return idx.Set(ctx, apiKeyID, entry)
}
