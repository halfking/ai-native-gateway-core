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
