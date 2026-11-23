// Package verdictkv 提供历史守卫判决缓存的共享层后端（Redis 适配器）。
//
// 它单独成包是为了守住 sanitize 包的轻依赖：sanitize 只认一个三方法的接口
// （sanitize.VerdictKV），不认识 Redis；本包也不反向 import sanitize。装配在
// cmd/gateway 完成。
package verdictkv

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// KeyPrefix 是共享层键的前缀。带上产品名与用途，便于在共用的 Redis 实例里
// 用 SCAN 按前缀盘点/清理，也避免与其它键空间撞名。
const KeyPrefix = "llmgw:histmemo:"

// KV 是共享层后端的最小接口（Get / 带 TTL 的 Set / 内存水位）。
//
// ★ 2026-11-23（12h 变更审计 A）：它是 sanitize.VerdictKV 的结构化副本。
// sanitize 的**测试**文件 import 本包 ⇒ 本包不能 import sanitize（否则测试
// 二进制成环）⇒ 接口在这里另起一份（Go 接口按方法集匹配，赋值时自动满足
// sanitize.VerdictKV）。
type KV interface {
	// Get 返回键的值；键不存在时返回 ("", false, nil)。
	Get(ctx context.Context, key string) (val string, ok bool, err error)
	// Set 写入键值并设置过期。
	Set(ctx context.Context, key, val string, ttl time.Duration) error
	// UsedMemory 返回后端已用内存字节数（用于水位线保护）。
	UsedMemory(ctx context.Context) (int64, error)
}

// RedisKV 用 Redis 实现 sanitize.VerdictKV（结构化实现：本包不 import
// sanitize，避免环）。它只是个薄适配器，无状态。
type RedisKV struct {
	client redis.UniversalClient
	prefix string
}

// IsNilClient 报告 client 是否**不可用**：接口本身是 nil，或接口里装的是
// nil 指针（类型化 nil）。
//
// ★ 2026-11-23（12h 变更审计 A）：`client == nil` 只挡住「接口本身是 nil」。
// Redis 客户端是以**具体指针类型**（*redis.Client）被装进接口的 ⇒ 一个 nil 的
// *redis.Client 装进接口后，接口值 ≠ nil，但底下是空指针 ⇒ 两道 `== nil` 闸
// 都放行 ⇒ 首次请求 client.Get(...) 空指针 panic（实测链条：
// cmd/gateway/history_guard_memo_init.go 装配 ⇒ internal/verdictkv/redis.go
// 构造 ⇒ 守卫在 sessioncontext 的 worker 协程里调用）。
// ⇒ 判据必须是**双重**：接口为 nil **或**其底层指针为 nil。
func IsNilClient(client redis.UniversalClient) bool {
	if client == nil {
		return true
	}
	v := reflect.ValueOf(client)
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return v.IsNil()
	default:
		return false
	}
}

// errNoClient 是「后端没有可用客户端」的错误。
//
// ★ 2026-11-23（12h 变更审计 A）：共享层出错时，sanitize 侧按「未命中」
// 处理（结论不变）⇒ 用错误而不是 panic 表达「后端不可用」。
var errNoClient = errors.New("verdictkv: redis client is nil")

// NewRedis 用一个 Redis 客户端构造共享层后端。prefix 为空 ⇒ 用 KeyPrefix。
// client 不可用（nil 接口或类型化 nil）⇒ 返回**真 nil 接口**。
//
// ★ 2026-11-23（12h 变更审计 A）：返回类型从 *RedisKV 改成接口 KV ——
// 返回具体指针时，「失败路径返回 nil」落到调用方眼里会变成一个**类型化 nil**
// （`var kv sanitize.VerdictKV = NewRedis(nil, "")` 得到一个「不等于 nil」的
// 接口）⇒ 下游两道 `== nil` 闸都放行 ⇒ 首次请求空指针 panic。
// 返回接口后，失败路径的 nil 就是**真 nil 接口**，赋给任何接口字段仍是 nil。
func NewRedis(client redis.UniversalClient, prefix string) KV {
	if IsNilClient(client) {
		return nil
	}
	if prefix == "" {
		prefix = KeyPrefix
	}
	return &RedisKV{client: client, prefix: prefix}
}

// usableClient 返回后端客户端；不可用时返回 errNoClient 而不是让调用方踩
// 空指针。
//
// ★ 2026-11-23（12h 变更审计 A）：RedisKV 的字段虽未导出，但外部可以写
// `&verdictkv.RedisKV{}`（零值 ⇒ client 为 nil）⇒ 每个方法都要自检。
func (k *RedisKV) usableClient() (redis.UniversalClient, error) {
	if k == nil || IsNilClient(k.client) {
		return nil, errNoClient
	}
	return k.client, nil
}

func (k *RedisKV) Get(ctx context.Context, key string) (string, bool, error) {
	c, err := k.usableClient()
	if err != nil {
		return "", false, err
	}
	val, err := c.Get(ctx, k.prefix+key).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return val, true, nil
}

func (k *RedisKV) Set(ctx context.Context, key, val string, ttl time.Duration) error {
	c, err := k.usableClient()
	if err != nil {
		return err
	}
	return c.Set(ctx, k.prefix+key, val, ttl).Err()
}

func (k *RedisKV) UsedMemory(ctx context.Context) (int64, error) {
	c, err := k.usableClient()
	if err != nil {
		return 0, err
	}
	// 先只要 memory 段（响应小）；后端不认分段（如测试用的 miniredis）时回退到全量。
	info, err := c.Info(ctx, "memory").Result()
	if err != nil {
		if info, err = c.Info(ctx).Result(); err != nil {
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
