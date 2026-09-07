package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func generateGwSessionID() string {
	return "gw_" + uuid.New().String()
}

func (sm *Manager) CreateV2(ctx context.Context, apiKeyID int, tenantID, deviceSeed, taskID string) (*Session, error) {
	// 2026-09-08 audit: same defensive contract as EnsureV2WithID/BindAPIKey —
	// nil-check the redis client at entry so a future caller wiring the
	// manager without redis panics here as an error, not in a goroutine.
	if sm == nil || sm.redis == nil || sm.redis.client == nil {
		return nil, fmt.Errorf("session: create requires redis client")
	}
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
	// 2026-09-07 audit: mirror BindAPIKey's guard — the caller invokes this
	// from a goroutine, and a nil redis client panicking there would take
	// down the whole process, not just this registration.
	if sm == nil || sm.redis == nil || sm.redis.client == nil || sessionID == "" || apiKeyID <= 0 {
		return nil, false, fmt.Errorf("session: ensure requires redis client, session id and api key")
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

	// 2026-09-08 audit: the previous pipeline HSet-unconditionally wrote
	// session:<id> between the Get check and the write (check-then-act).
	// Two different api keys concurrently first-using the same legacy id
	// both passed the Get→NotFound gate, and the later pipeline stole the
	// hash — the first key then hit a permanent (TTL-lived) 403 on every
	// follow-up request. The script makes exists-check + write atomic:
	// first writer wins, the loser falls through to the re-Get below and
	// keeps the pre-registration "honored, unresolvable" behavior instead
	// of acquiring someone else's session.
	ensureScript := redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  return 0
end
redis.call('HSET', KEYS[1], unpack(ARGV, 1, #ARGV - 2))
redis.call('EXPIRE', KEYS[1], tonumber(ARGV[#ARGV - 1]))
redis.call('SET', KEYS[2], ARGV[#ARGV], 'NX', 'EX', tonumber(ARGV[#ARGV - 1]))
redis.call('SADD', KEYS[3], ARGV[#ARGV])
redis.call('EXPIRE', KEYS[3], tonumber(ARGV[#ARGV - 1]))
return 1
`)
	fields := []any{
		"api_key_id", strconv.Itoa(apiKeyID),
		"tenant_id", tenantID,
		"session_key", sessionKey,
		"task_id", taskID,
		"namespace", "gw",
		"created_at", now.Format(time.RFC3339),
		"last_active", now.Format(time.RFC3339),
		"expires_at", session.ExpiresAt.Format(time.RFC3339),
		"devices", string(devicesJSON),
		"provider_cache_info", string(cacheInfoJSON),
	}
	args := append(fields, int(sm.ttl/time.Second), sessionID)
	created, err := ensureScript.Run(ctx, sm.redis.client,
		[]string{"session:" + sessionID, sessionKeyRedis, activeKeyRedis}, args...).Int()
	if err != nil {
		return nil, false, fmt.Errorf("failed to ensure session v2 in redis: %w", err)
	}
	if created == 0 {
		// Race lost: the id was registered concurrently. Re-read so the
		// caller gets the real owner's record (created=false).
		existing, gerr := sm.Get(ctx, sessionID)
		if gerr != nil {
			return nil, false, gerr
		}
		return existing, false, nil
	}

	return session, true, nil
}
