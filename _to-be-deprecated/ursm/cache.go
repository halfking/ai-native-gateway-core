package ursm

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// LayerCache 泛型三层缓存（Memory → Redis → DB）
type LayerCache[T any] struct {
	// 存储层
	memCache    *sync.Map
	redisClient *redis.Client
	db          *pgxpool.Pool

	// TTL配置
	memTTL   time.Duration
	redisTTL time.Duration

	// DB查询函数（由具体类型注入）
	dbQueryFunc func(ctx context.Context, db *pgxpool.Pool, key string) (*T, error)

	// 可选：键前缀（用于Redis）
	redisPrefix string
}

// cacheEntry 内存缓存条目
type cacheEntry[T any] struct {
	value     *T
	expiresAt time.Time
}

// NewLayerCache 创建三层缓存
func NewLayerCache[T any](
	redisClient *redis.Client,
	db *pgxpool.Pool,
	memTTL time.Duration,
	redisTTL time.Duration,
	redisPrefix string,
	dbQueryFunc func(ctx context.Context, db *pgxpool.Pool, key string) (*T, error),
) *LayerCache[T] {
	return &LayerCache[T]{
		memCache:    &sync.Map{},
		redisClient: redisClient,
		db:          db,
		memTTL:      memTTL,
		redisTTL:    redisTTL,
		redisPrefix: redisPrefix,
		dbQueryFunc: dbQueryFunc,
	}
}

// Get 三层查询：mem → redis → db
func (c *LayerCache[T]) Get(ctx context.Context, key string) (*T, error) {
	// L1: 内存缓存
	if val, ok := c.getFromMem(key); ok {
		return val, nil
	}

	// L2: Redis缓存
	if val, err := c.getFromRedis(ctx, key); err == nil && val != nil {
		// 回写到L1
		c.setToMem(key, val)
		return val, nil
	}

	// L3: 数据库
	if c.dbQueryFunc != nil {
		val, err := c.getFromDB(ctx, key)
		if err != nil {
			// 数据库查询失败，返回错误
			if err == pgx.ErrNoRows {
				return nil, fmt.Errorf("not found: %w", err)
			}
			return nil, fmt.Errorf("db query failed: %w", err)
		}

		// 回写到L1和L2
		c.setToMem(key, val)
		c.setToRedis(ctx, key, val)
		return val, nil
	}

	return nil, fmt.Errorf("not found in cache and no db query func")
}

// Set 写入所有层
func (c *LayerCache[T]) Set(ctx context.Context, key string, value *T) error {
	// 写入L1
	c.setToMem(key, value)

	// 写入L2
	if err := c.setToRedis(ctx, key, value); err != nil {
		// Redis写入失败不阻塞，只记录日志
		// 可以添加日志记录
		_ = err
	}

	// 注意：DB写入由BatchWriter负责，这里不直接写
	return nil
}

// Invalidate 失效所有层
func (c *LayerCache[T]) Invalidate(ctx context.Context, key string) error {
	// 删除L1
	c.memCache.Delete(key)

	// 删除L2
	if c.redisClient != nil {
		redisKey := c.redisPrefix + ":" + key
		if err := c.redisClient.Del(ctx, redisKey).Err(); err != nil {
			// Redis删除失败不阻塞
			_ = err
		}
	}

	return nil
}

// getFromMem 从内存缓存读取
func (c *LayerCache[T]) getFromMem(key string) (*T, bool) {
	val, ok := c.memCache.Load(key)
	if !ok {
		return nil, false
	}

	entry := val.(*cacheEntry[T])
	if time.Now().After(entry.expiresAt) {
		// 过期，删除并返回miss
		c.memCache.Delete(key)
		return nil, false
	}

	return entry.value, true
}

// setToMem 写入内存缓存
func (c *LayerCache[T]) setToMem(key string, value *T) {
	entry := &cacheEntry[T]{
		value:     value,
		expiresAt: time.Now().Add(c.memTTL),
	}
	c.memCache.Store(key, entry)
}

// getFromRedis 从Redis读取
func (c *LayerCache[T]) getFromRedis(ctx context.Context, key string) (*T, error) {
	if c.redisClient == nil {
		return nil, fmt.Errorf("redis client not available")
	}

	redisKey := c.redisPrefix + ":" + key
	data, err := c.redisClient.Get(ctx, redisKey).Result()
	if err == redis.Nil {
		return nil, nil // 未找到
	}
	if err != nil {
		return nil, err
	}

	var value T
	if err := json.Unmarshal([]byte(data), &value); err != nil {
		return nil, fmt.Errorf("json unmarshal failed: %w", err)
	}

	return &value, nil
}

// setToRedis 写入Redis
func (c *LayerCache[T]) setToRedis(ctx context.Context, key string, value *T) error {
	if c.redisClient == nil {
		return nil // Redis不可用，直接返回
	}

	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("json marshal failed: %w", err)
	}

	redisKey := c.redisPrefix + ":" + key
	return c.redisClient.Set(ctx, redisKey, data, c.redisTTL).Err()
}

// getFromDB 从数据库读取
func (c *LayerCache[T]) getFromDB(ctx context.Context, key string) (*T, error) {
	if c.dbQueryFunc == nil {
		return nil, fmt.Errorf("no db query function")
	}

	return c.dbQueryFunc(ctx, c.db, key)
}

// Clear 清空内存缓存（用于测试或重置）
func (c *LayerCache[T]) Clear() {
	c.memCache = &sync.Map{}
}
