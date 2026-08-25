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

// SetLevel 显式指定 level(1/2/3)写入, 避免 levelOf 的冒号段数启发式
// (profile/model 含冒号时 levelOf 会误判)。调用方已知 level 时应优先用此方法。
func (s *StickyStore) SetLevel(ctx context.Context, level int, credID int, rawKey string, ttl time.Duration) error {
	redisKey := StickyKey(level, rawKey)
	if err := s.rdb.Set(ctx, redisKey, strconv.Itoa(credID), ttl).Err(); err != nil {
		return err
	}
	s.lru.Put(redisKey, credID)
	return nil
}

// GetLevel 显式指定 level 读取。
func (s *StickyStore) GetLevel(ctx context.Context, level int, rawKey string) (int, bool) {
	redisKey := StickyKey(level, rawKey)
	if v, ok := s.lru.Get(redisKey); ok {
		return v, true
	}
	val, err := s.rdb.Get(ctx, redisKey).Result()
	if err == redis.Nil || err != nil {
		return 0, false
	}
	credID, err := strconv.Atoi(val)
	if err != nil {
		return 0, false
	}
	s.lru.Put(redisKey, credID)
	return credID, true
}

func (s *StickyStore) DeleteLevelIfCredential(ctx context.Context, level int, rawKey string, credID int) error {
	redisKey := StickyKey(level, rawKey)
	const deleteIfMatches = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`
	if err := s.rdb.Eval(ctx, deleteIfMatches, []string{redisKey}, strconv.Itoa(credID)).Err(); err != nil {
		return err
	}
	s.lru.DeleteIf(redisKey, func(v int) bool { return v == credID })
	return nil
}

// ClearForCredential scans the entire sticky namespace (L1/L2/L3) and
// deletes every entry whose value equals credID. Returns the number of
// entries removed. Used by hot-reload hooks when admin changes a
// credential's priority / weight / concurrency.
//
// 2026-08-26 hot-reload hook: we must clear stale pins so new sessions can
// re-enter load balancing after the operator explicitly changes a credential's
// routing configuration.
//
// Implementation notes:
//   - Uses SCAN with COUNT 200 (not KEYS) to avoid blocking Redis at scale.
//   - Compares each value against credID via GET → strconv.Atoi; deletes only
//     on match (EVAL ... DEL atomic check).
//   - Batches DELs in pipelines of 200 to limit round-trips.
//   - Synchronously invalidates the in-process LRU mirror.
func (s *StickyStore) ClearForCredential(ctx context.Context, credID int) (int, error) {
	if credID <= 0 {
		return 0, nil
	}
	credIDStr := strconv.Itoa(credID)
	cleared := 0

	// 1. Clear LRU mirror up-front (cheap; based on local state).
	s.lru.DeleteIfValue(credID)

	// 2. SCAN the namespace, batch GET, then pipeline DEL for matches.
	const scanBatch = 200
	var cursor uint64
	for {
		keys, next, err := s.rdb.Scan(ctx, cursor, "ursm:v2:sticky:L*", int64(scanBatch)).Result()
		if err != nil {
			return cleared, err
		}
		if len(keys) > 0 {
			// GET all in one round-trip.
			gets, err := s.rdb.MGet(ctx, keys...).Result()
			if err != nil {
				return cleared, err
			}
			toDel := make([]string, 0, len(keys))
			for i, v := range gets {
				if v == nil {
					continue
				}
				str, ok := v.(string)
				if !ok || str != credIDStr {
					continue
				}
				toDel = append(toDel, keys[i])
			}
			if len(toDel) > 0 {
				if err := s.rdb.Del(ctx, toDel...).Err(); err != nil {
					return cleared, err
				}
				cleared += len(toDel)
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return cleared, nil
}

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
