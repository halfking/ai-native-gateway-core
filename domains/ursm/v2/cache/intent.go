package cache

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// Intent 是 session intent 的序列化载荷,字段对齐
// autoroute.CachedIntent(设计稿 Decision 5)。
type Intent struct {
	TaskType     string  `json:"task"`
	WorkType     string  `json:"work_type,omitempty"`
	ChosenModel  string  `json:"model"`
	CredentialID int64   `json:"cred_id"`
	Profile      string  `json:"profile"`
	Confidence   float64 `json:"confidence"`
	Classifier   string  `json:"classifier"`
	HitCount     int     `json:"hit_count"`
	LastSeen     int64   `json:"last_seen"`
}

// IntentStore 是 session intent 的 Redis 权威 + 进程 LRU 镜像。
// 替代 autoroute/session_intent_cache.go 的纯内存缓存(设计稿 Decision 5)。
//
// 不变量: 写路径 Redis 成功后才更新 LRU;读路径 LRU miss 回源 Redis 后回填。
// fail-open: Redis miss/error 返回 (zero,false), 上层视作 intent miss。
type IntentStore struct {
	rdb     *redis.Client
	lru     *LRU[string, Intent]
	softTTL time.Duration // 保留字段, 当前未用于软过期(intent 无 gen 概念)
}

func NewIntentStore(rdb *redis.Client, capacity int, softTTL time.Duration) *IntentStore {
	return &IntentStore{rdb: rdb, lru: NewLRU[string, Intent](capacity), softTTL: softTTL}
}

// Set 写 Redis(SET key JSON EX ttl)并同步更新 LRU。LastSeen 自动盖戳。
func (s *IntentStore) Set(ctx context.Context, sessionID string, in Intent, ttl time.Duration) error {
	in.LastSeen = time.Now().Unix()
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	key := IntentKey(sessionID)
	if err := s.rdb.Set(ctx, key, data, ttl).Err(); err != nil {
		return err
	}
	s.lru.Put(key, in)
	return nil
}

func (s *IntentStore) Delete(ctx context.Context, sessionID string) error {
	key := IntentKey(sessionID)
	if err := s.rdb.Del(ctx, key).Err(); err != nil {
		return err
	}
	s.lru.Delete(key)
	return nil
}
func (s *IntentStore) Get(ctx context.Context, sessionID string) (Intent, bool) {
	key := IntentKey(sessionID)
	if v, ok := s.lru.Get(key); ok {
		return v, true
	}
	data, err := s.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil || err != nil {
		return Intent{}, false
	}
	var in Intent
	if err := json.Unmarshal(data, &in); err != nil {
		return Intent{}, false
	}
	s.lru.Put(key, in)
	return in, true
}
