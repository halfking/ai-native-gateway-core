package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrSessionExpired  = errors.New("session expired")
	ErrInvalidSession  = errors.New("invalid session")
)

type Device struct {
	DeviceSeed string    `json:"device_seed"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
}

type CacheInfo struct {
	OpenAICheckpoint   string `json:"openai_checkpoint,omitempty"`
	AnthropicCacheToken string `json:"anthropic_cache_token,omitempty"`
}

type Session struct {
	SessionID      string    `json:"session_id"`
	SessionKey     string    `json:"session_key"`
	APIKeyID       int       `json:"api_key_id"`
	TenantID       string    `json:"tenant_id"`
	Devices        []Device  `json:"devices"`
	ProviderCache  CacheInfo `json:"provider_cache_info"`
	CreatedAt      time.Time `json:"created_at"`
	LastActive     time.Time `json:"last_active"`
	ExpiresAt      time.Time `json:"expires_at"`
}

func (s *Session) GetAPIKeyID() int {
	return s.APIKeyID
}

func (s *Session) GetTenantID() string {
	return s.TenantID
}

func (s *Session) GetSessionKey() string {
	return s.SessionKey
}

type RedisClient struct {
	client *redis.Client
}

func NewRedisClient(addr, password string, db int) *RedisClient {
	return &RedisClient{
		client: redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: password,
			DB:       db,
		}),
	}
}

func (r *RedisClient) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

func (r *RedisClient) HSet(ctx context.Context, key string, fields map[string]any) error {
	return r.client.HSet(ctx, key, fields).Err()
}

func (r *RedisClient) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return r.client.HGetAll(ctx, key).Result()
}

func (r *RedisClient) Get(ctx context.Context, key string) (string, error) {
	return r.client.Get(ctx, key).Result()
}

func (r *RedisClient) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}

func (r *RedisClient) SAdd(ctx context.Context, key string, members ...any) error {
	return r.client.SAdd(ctx, key, members...).Err()
}

func (r *RedisClient) SRem(ctx context.Context, key string, members ...any) error {
	return r.client.SRem(ctx, key, members...).Err()
}

func (r *RedisClient) Del(ctx context.Context, keys ...string) error {
	return r.client.Del(ctx, keys...).Err()
}

func (r *RedisClient) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return r.client.Expire(ctx, key, ttl).Err()
}

type Manager struct {
	redis *RedisClient
	ttl   time.Duration
}

func NewManager(redisClient *RedisClient, ttl time.Duration) *Manager {
	if ttl == 0 {
		ttl = 7 * 24 * time.Hour
	}
	return &Manager{redis: redisClient, ttl: ttl}
}

func generateSessionKey(apiKeyID int, tenantID string) string {
	raw := fmt.Sprintf("%d:%s:%s:%d", apiKeyID, tenantID, time.Now().UnixNano(), uuid.New().String())
	hash := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(hash[:])[:32]
}

func (sm *Manager) Create(ctx context.Context, apiKeyID int, tenantID string, deviceSeed string) (*Session, error) {
	sessionID := uuid.New().String()
	sessionKey := generateSessionKey(apiKeyID, tenantID)

	now := time.Now()
	session := &Session{
		SessionID:  sessionID,
		SessionKey: sessionKey,
		APIKeyID:   apiKeyID,
		TenantID:   tenantID,
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
		"created_at":          now.Format(time.RFC3339),
		"last_active":         now.Format(time.RFC3339),
		"expires_at":          session.ExpiresAt.Format(time.RFC3339),
		"devices":             string(devicesJSON),
		"provider_cache_info": string(cacheInfoJSON),
	})
	pipe.Set(ctx, sessionKeyRedis, sessionID, sm.ttl)
	pipe.SAdd(ctx, activeKeyRedis, sessionID)
	pipe.Expire(ctx, activeKeyRedis, sm.ttl)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create session in redis: %w", err)
	}

	return session, nil
}

func (sm *Manager) Get(ctx context.Context, sessionID string) (*Session, error) {
	data, err := sm.redis.HGetAll(ctx, "session:"+sessionID)
	if err != nil || len(data) == 0 {
		return nil, ErrSessionNotFound
	}

	apiKeyID, _ := strconv.Atoi(data["api_key_id"])
	devices := []Device{}
	if data["devices"] != "" {
		json.Unmarshal([]byte(data["devices"]), &devices)
	}
	cacheInfo := CacheInfo{}
	if data["provider_cache_info"] != "" {
		json.Unmarshal([]byte(data["provider_cache_info"]), &cacheInfo)
	}

	createdAt, _ := parseTime(data["created_at"])
	lastActive, _ := parseTime(data["last_active"])
	expiresAt, _ := parseTime(data["expires_at"])

	if time.Now().After(expiresAt) {
		return nil, ErrSessionExpired
	}

	return &Session{
		SessionID:     sessionID,
		SessionKey:    data["session_key"],
		APIKeyID:      apiKeyID,
		TenantID:      data["tenant_id"],
		Devices:       devices,
		ProviderCache: cacheInfo,
		CreatedAt:     createdAt,
		LastActive:    lastActive,
		ExpiresAt:     expiresAt,
	}, nil
}

func (sm *Manager) Delete(ctx context.Context, sessionID string) error {
	data, err := sm.redis.HGetAll(ctx, "session:"+sessionID)
	if err != nil || len(data) == 0 {
		return ErrSessionNotFound
	}

	apiKeyID, _ := strconv.Atoi(data["api_key_id"])
	sessionKey := data["session_key"]

	pipe := sm.redis.client.Pipeline()
	pipe.Del(ctx, "session:"+sessionID)
	pipe.Del(ctx, "session:key:"+sessionKey)
	pipe.SRem(ctx, fmt.Sprintf("session:apiKey:%d:active", apiKeyID), sessionID)
	_, err = pipe.Exec(ctx)

	return err
}

func (sm *Manager) Migrate(ctx context.Context, sessionID string, newDeviceSeed string) (*Session, error) {
	session, err := sm.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	deviceExists := false
	for i, d := range session.Devices {
		if d.DeviceSeed == newDeviceSeed {
			deviceExists = true
			session.Devices[i].LastSeen = time.Now()
			break
		}
	}

	if !deviceExists {
		session.Devices = append(session.Devices, Device{
			DeviceSeed: newDeviceSeed,
			FirstSeen:  time.Now(),
			LastSeen:   time.Now(),
		})
	}
	session.LastActive = time.Now()

	devicesJSON, _ := json.Marshal(session.Devices)
	sm.redis.HSet(ctx, "session:"+sessionID, map[string]any{
		"devices":      string(devicesJSON),
		"last_active": session.LastActive.Format(time.RFC3339),
	})

	return session, nil
}

func (sm *Manager) Touch(ctx context.Context, sessionID string) error {
	return sm.redis.HSet(ctx, "session:"+sessionID, map[string]any{
		"last_active": time.Now().Format(time.RFC3339),
	})
}

func (sm *Manager) UpdateCacheInfo(ctx context.Context, sessionID string, cacheInfo CacheInfo) error {
	cacheInfoJSON, _ := json.Marshal(cacheInfo)
	return sm.redis.HSet(ctx, "session:"+sessionID, map[string]any{
		"provider_cache_info": string(cacheInfoJSON),
	})
}

func (sm *Manager) GetBySessionKey(ctx context.Context, sessionKey string) (*Session, error) {
	sessionID, err := sm.redis.Get(ctx, "session:key:"+sessionKey)
	if err != nil {
		return nil, ErrSessionNotFound
	}
	return sm.Get(ctx, sessionID)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, s)
}