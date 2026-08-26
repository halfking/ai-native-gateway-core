package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const SessionPreferenceTTL = 7 * 24 * time.Hour

type SessionPrefValue struct {
	CredentialID int    `json:"credential_id"`
	Model        string `json:"model,omitempty"`
}

type SessionPreference struct {
	redis *RedisClient
}

func NewSessionPreference(redisClient *RedisClient) *SessionPreference {
	return &SessionPreference{redis: redisClient}
}

func (sp *SessionPreference) redisKey(sessionID string) string {
	return fmt.Sprintf("session_pref:%s", sessionID)
}

func (sp *SessionPreference) Get(ctx context.Context, sessionID string) (*SessionPrefValue, bool) {
	if sp == nil || sp.redis == nil {
		return nil, false
	}
	data, err := sp.redis.client.Get(ctx, sp.redisKey(sessionID)).Result()
	if err == redis.Nil || err != nil {
		return nil, false
	}
	var val SessionPrefValue
	if err := json.Unmarshal([]byte(data), &val); err == nil {
		return &val, true
	}
	credID, err := strconv.Atoi(data)
	if err != nil {
		return nil, false
	}
	return &SessionPrefValue{CredentialID: credID}, true
}

func (sp *SessionPreference) GetModel(ctx context.Context, sessionID string) string {
	val, found := sp.Get(ctx, sessionID)
	if !found || val == nil {
		return ""
	}
	return val.Model
}

func (sp *SessionPreference) Set(ctx context.Context, sessionID string, credentialID int, model string) error {
	if sp == nil || sp.redis == nil {
		return nil
	}
	data, err := json.Marshal(SessionPrefValue{CredentialID: credentialID, Model: model})
	if err != nil {
		return fmt.Errorf("json marshal session pref failed: %w", err)
	}
	return sp.redis.client.Set(ctx, sp.redisKey(sessionID), data, SessionPreferenceTTL).Err()
}

func (sp *SessionPreference) Delete(ctx context.Context, sessionID string) error {
	if sp == nil || sp.redis == nil {
		return nil
	}
	return sp.redis.client.Del(ctx, sp.redisKey(sessionID)).Err()
}

func (sp *SessionPreference) ClearOnModelSwitch(ctx context.Context, sessionID string, oldModel, newModel string) error {
	return sp.Delete(ctx, sessionID)
}
