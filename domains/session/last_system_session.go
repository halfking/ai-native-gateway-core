package session

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type LastSystemSessionEntry struct {
	SessionID      string    `json:"session_id"`
	LastAssignedAt time.Time `json:"last_assigned_at"`
	DeviceSeed     string    `json:"device_seed,omitempty"`
	TaskID         string    `json:"task_id,omitempty"`
}

const LastSystemSessionTTL = 5 * time.Minute

type LastSystemSessionIndex struct {
	redis *RedisClient
}

func NewLastSystemSessionIndex(redisClient *RedisClient) *LastSystemSessionIndex {
	return &LastSystemSessionIndex{redis: redisClient}
}

func (idx *LastSystemSessionIndex) redisKey(apiKeyID int) string {
	return fmt.Sprintf("client:%d:last_system_session", apiKeyID)
}

func (idx *LastSystemSessionIndex) Get(ctx context.Context, apiKeyID int) (*LastSystemSessionEntry, bool) {
	if idx == nil || idx.redis == nil {
		return nil, false
	}
	data, err := idx.redis.client.Get(ctx, idx.redisKey(apiKeyID)).Result()
	if err == redis.Nil || err != nil {
		return nil, false
	}
	var entry LastSystemSessionEntry
	if err := json.Unmarshal([]byte(data), &entry); err != nil {
		return nil, false
	}
	if time.Since(entry.LastAssignedAt) > LastSystemSessionTTL {
		return nil, false
	}
	return &entry, true
}

func (idx *LastSystemSessionIndex) Set(ctx context.Context, apiKeyID int, entry *LastSystemSessionEntry) error {
	if idx == nil || idx.redis == nil || entry == nil {
		return nil
	}
	entry.LastAssignedAt = time.Now()
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("json marshal failed: %w", err)
	}
	return idx.redis.client.Set(ctx, idx.redisKey(apiKeyID), data, LastSystemSessionTTL).Err()
}

func (idx *LastSystemSessionIndex) Delete(ctx context.Context, apiKeyID int) error {
	if idx == nil || idx.redis == nil {
		return nil
	}
	return idx.redis.client.Del(ctx, idx.redisKey(apiKeyID)).Err()
}

func (idx *LastSystemSessionIndex) Touch(ctx context.Context, apiKeyID int) error {
	entry, found := idx.Get(ctx, apiKeyID)
	if !found {
		return fmt.Errorf("no entry to touch")
	}
	return idx.Set(ctx, apiKeyID, entry)
}
