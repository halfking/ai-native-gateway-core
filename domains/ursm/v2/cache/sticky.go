package cache

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// StickyStore 是 sticky 路由的 Redis 权威 + 进程 LRU 镜像。
// 替代 domains/routing/sticky.go 的 map + 异步 DB 写(设计稿 Decision 4)。
//
// 不变量: 写路径 Redis 成功后才更新 LRU;读路径 LRU miss 回源 Redis 后回填。
// sticky 无 generation 概念(纯 credID 映射),因此 LRU 写不做单调比较,
// 但 TTL 由 Redis 单源管理,保证跨实例一致。
type StickyStore struct {
	rdb *redis.Client
	lru *LRU[string, int]
}

func NewStickyStore(rdb *redis.Client, capacity int, _ time.Duration) *StickyStore {
	return &StickyStore{rdb: rdb, lru: NewLRU[string, int](capacity)}
}

// Set 写 Redis(SET key credID EX ttl)并同步更新 LRU。
func (s *StickyStore) Set(ctx context.Context, credID int, rawKey string, ttl time.Duration) error {
	redisKey := StickyKey(levelOf(rawKey), rawKey)
	if err := s.rdb.Set(ctx, redisKey, strconv.Itoa(credID), ttl).Err(); err != nil {
		return err
	}
	s.lru.Put(redisKey, credID)
	return nil
}

// Get 先查 LRU,miss 回源 Redis 并回填。
// Redis miss/error 返回 (0,false): fail-open, 上层视作 sticky miss。
func (s *StickyStore) Get(ctx context.Context, rawKey string) (int, bool) {
	redisKey := StickyKey(levelOf(rawKey), rawKey)
	if v, ok := s.lru.Get(redisKey); ok {
		return v, true
	}
	val, err := s.rdb.Get(ctx, redisKey).Result()
	if err == redis.Nil {
		return 0, false
	}
	if err != nil {
		return 0, false // fail-open
	}
	credID, err := strconv.Atoi(val)
	if err != nil {
		return 0, false
	}
	s.lru.Put(redisKey, credID)
	return credID, true
}

// levelOf 从 rawKey 推断 sticky 级别(L1/L2/L3)用于 Redis key 前缀。
// 约定: caller 传入的 rawKey 已是 buildStickyKeys 产出的完整 key,
// 通过冒号分隔段数判定: 6 段=L1, 5 段=L2, 4 段=L3。
func levelOf(rawKey string) int {
	segs := 1
	for _, c := range rawKey {
		if c == ':' {
			segs++
		}
	}
	switch segs {
	case 6:
		return 1
	case 5:
		return 2
	default:
		return 3
	}
}
