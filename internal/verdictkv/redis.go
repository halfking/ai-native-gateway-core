// Package verdictkv 提供历史守卫判决缓存的共享层后端（Redis 适配器）。
//
// 它单独成包是为了守住 sanitize 包的轻依赖：sanitize 只认一个三方法的接口
// （sanitize.VerdictKV），不认识 Redis；本包也不反向 import sanitize。装配在
// cmd/gateway 完成。
package verdictkv

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// KeyPrefix 是共享层键的前缀。带上产品名与用途，便于在共用的 Redis 实例里
// 用 SCAN 按前缀盘点/清理，也避免与其它键空间撞名。
const KeyPrefix = "llmgw:histmemo:"

// RedisKV 用 Redis 实现 sanitize.VerdictKV（结构化实现：本包不 import
// sanitize，避免环）。它只是个薄适配器，无状态。
type RedisKV struct {
	client redis.UniversalClient
	prefix string
}

// NewRedis 用一个 Redis 客户端构造共享层后端。prefix 为空 ⇒ 用 KeyPrefix。
// client 为 nil ⇒ 返回 nil。
//
// ★ 调用方把它当接口用时注意类型化 nil：`var kv sanitize.VerdictKV = NewRedis(nil, "")`
// 会得到一个「不等于 nil」的接口 ⇒ 先判返回值再赋给接口（cmd/gateway 的装配
// 是这么写的）。
func NewRedis(client redis.UniversalClient, prefix string) *RedisKV {
	if client == nil {
		return nil
	}
	if prefix == "" {
		prefix = KeyPrefix
	}
	return &RedisKV{client: client, prefix: prefix}
}

func (k *RedisKV) Get(ctx context.Context, key string) (string, bool, error) {
	val, err := k.client.Get(ctx, k.prefix+key).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return val, true, nil
}

func (k *RedisKV) Set(ctx context.Context, key, val string, ttl time.Duration) error {
	return k.client.Set(ctx, k.prefix+key, val, ttl).Err()
}

func (k *RedisKV) UsedMemory(ctx context.Context) (int64, error) {
	// 先只要 memory 段（响应小）；后端不认分段（如测试用的 miniredis）时回退到全量。
	info, err := k.client.Info(ctx, "memory").Result()
	if err != nil {
		if info, err = k.client.Info(ctx).Result(); err != nil {
			return 0, err
		}
	}
	used, ok := parseUsedMemory(info)
	if !ok {
		return 0, errNoUsedMemory
	}
	return used, nil
}

// parseUsedMemory 从 `INFO memory` 文本里取 used_memory（字节）。
//
// 只认**行首**的 used_memory：INFO 里还有 used_memory_human /
// used_memory_peak / used_memory_dataset 等同前缀字段，子串匹配会拿错。
func parseUsedMemory(info string) (int64, bool) {
	const marker = "used_memory:"
	start := 0
	for start < len(info) {
		end := start
		for end < len(info) && info[end] != '\n' {
			end++
		}
		line := info[start:end]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) > len(marker) && line[:len(marker)] == marker {
			n, err := strconv.ParseInt(line[len(marker):], 10, 64)
			return n, err == nil
		}
		start = end + 1
	}
	return 0, false
}

var errNoUsedMemory = errors.New("verdictkv: used_memory not found in INFO memory")
