package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
)

func generateGwSessionID() string {
	return "gw_" + uuid.New().String()
}

func (sm *Manager) CreateV2(ctx context.Context, apiKeyID int, tenantID, deviceSeed, taskID string) (*Session, error) {
	if taskID == "" {
		taskID = "default"
	}

	sessionID := generateGwSessionID()
	sessionKey := generateSessionKey(apiKeyID, tenantID)

	now := time.Now()
	session := &Session{
		SessionID:  sessionID,
		SessionKey: sessionKey,
		APIKeyID:   apiKeyID,
		TenantID:   tenantID,
		TaskID:     taskID,
		Namespace:  "gw",
		Devices: []Device{
			{DeviceSeed: deviceSeed, FirstSeen: now, LastSeen: now},
		},
		CreatedAt:  now,
		LastActive: now,
		ExpiresAt:  now.Add(sm.ttl),
	}

	devicesJSON, _ := json.Marshal(session.Devices)
	cacheInfoJSON, _ := json.Marshal(session.ProviderCache)

	sessionKeyRedis := "session:key:" + sessionKey
	activeKeyRedis := fmt.Sprintf("session:apiKey:%d:active", apiKeyID)

	pipe := sm.redis.client.Pipeline()
	pipe.HSet(ctx, "session:"+sessionID, map[string]any{
		"api_key_id":          strconv.Itoa(apiKeyID),
		"tenant_id":           tenantID,
		"session_key":         sessionKey,
		"task_id":             taskID,
		"namespace":           "gw",
		"created_at":          now.Format(time.RFC3339),
		"last_active":         now.Format(time.RFC3339),
		"expires_at":          session.ExpiresAt.Format(time.RFC3339),
		"devices":             string(devicesJSON),
		"provider_cache_info": string(cacheInfoJSON),
	})
	// 2026-08-18: this Expire was missing, so every v2 session hash was
	// persistent. Sessions with no follow-up state write (the majority —
	// last_active == created_at) never hit the TTL re-arm done by
	// SetTitle/UpdateState/etc., accumulating ~90k dead keys in the shared
	// session Redis. The session:key index below DID expire, orphaning the
	// main hash forever.
	pipe.Expire(ctx, "session:"+sessionID, sm.ttl)
	pipe.Set(ctx, sessionKeyRedis, sessionID, sm.ttl)
	pipe.SAdd(ctx, activeKeyRedis, sessionID)
	pipe.Expire(ctx, activeKeyRedis, sm.ttl)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create session v2 in redis: %w", err)
	}

	return session, nil
}

// EnsureV2WithID registers a session record for a caller-supplied session id
// (e.g. a gw_ id honored verbatim from X-Gw-Session-Id). It is idempotent:
// when the id already resolves, the existing record is returned untouched
// (created=false). Without this registration a client that reuses the same
// id across a conversation hits ErrSessionNotFound on EVERY request — the
// session never becomes resolvable, so Touch/session-scoped state can never
// engage and turns cannot accumulate coherently.
func (sm *Manager) EnsureV2WithID(ctx context.Context, sessionID string, apiKeyID int, tenantID, deviceSeed, taskID string) (*Session, bool, error) {
	if sm == nil || sessionID == "" || apiKeyID <= 0 {
		return nil, false, fmt.Errorf("session: ensure requires session id and api key")
	}
	if existing, err := sm.Get(ctx, sessionID); err == nil && existing != nil {
		return existing, false, nil
	}
	if taskID == "" {
		taskID = "default"
	}

	sessionKey := generateSessionKey(apiKeyID, tenantID)
	now := time.Now()
	session := &Session{
		SessionID:  sessionID,
		SessionKey: sessionKey,
		APIKeyID:   apiKeyID,
		TenantID:   tenantID,
		TaskID:     taskID,
		Namespace:  "gw",
		Devices: []Device{
			{DeviceSeed: deviceSeed, FirstSeen: now, LastSeen: now},
		},
		CreatedAt:  now,
		LastActive: now,
		ExpiresAt:  now.Add(sm.ttl),
	}

	devicesJSON, _ := json.Marshal(session.Devices)
	cacheInfoJSON, _ := json.Marshal(session.ProviderCache)

	sessionKeyRedis := "session:key:" + sessionKey
	activeKeyRedis := fmt.Sprintf("session:apiKey:%d:active", apiKeyID)

	pipe := sm.redis.client.Pipeline()
	pipe.HSet(ctx, "session:"+sessionID, map[string]any{
		"api_key_id":          strconv.Itoa(apiKeyID),
		"tenant_id":           tenantID,
		"session_key":         sessionKey,
		"task_id":             taskID,
		"namespace":           "gw",
		"created_at":          now.Format(time.RFC3339),
		"last_active":         now.Format(time.RFC3339),
		"expires_at":          session.ExpiresAt.Format(time.RFC3339),
		"devices":             string(devicesJSON),
		"provider_cache_info": string(cacheInfoJSON),
	})
	pipe.Expire(ctx, "session:"+sessionID, sm.ttl)
	// NX: a concurrent first-request may have created the index already —
	// never let a later registration steal an existing session:key mapping.
	pipe.SetNX(ctx, sessionKeyRedis, sessionID, sm.ttl)
	pipe.SAdd(ctx, activeKeyRedis, sessionID)
	pipe.Expire(ctx, activeKeyRedis, sm.ttl)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("failed to ensure session v2 in redis: %w", err)
	}

	return session, true, nil
}
