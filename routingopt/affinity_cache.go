// affinity_cache.go — P2.2 Track A: routing_user_affinity 的 Redis 读缓存。
//
// 包装 UserAffinityDAO.GetByUserID：
//   - Redis hash 缓存 task_type_distribution 与 preferred_providers
//     （key routingopt:affinity:<userID>，TTL 1h）
//   - miss 回源 DB 并回填 Redis；DB 也 miss（ErrNoRows）→ 负缓存 30s，
//     窗口内直接返回"无数据"，不重复回源
//   - 构造时 redis.Client 为 nil → 直查 DB 降级（行为等同无缓存，不 panic）
//
// 错误纪律：任何 Redis 故障只 slog 降级为 DB 直查，绝不阻塞路由热路径；
// 与 feedback_batch.go 同一原则（routing beats data）。
//
// 计数（P2.2 Track C）：
//   - 正命中 → RecordCacheHit()；空读回源 → RecordCacheMiss()
//   - Redis 故障降级直查 DB → 计为 Miss（确实回了源）
//   - 负缓存命中不计数（既没走 DB 也不是正命中，避免 hit rate 失真）
//   - redis 为 nil（无缓存语义）不计数
//
// 并发安全：go-redis 客户端本身并发安全；本结构体全部字段构造期定型
// （不可变），无本地可变状态，因此无需额外锁，-race 下并发 Get 无竞态。
//
// 缓存一致性选择：UpdateUserAffinity（enhancer.go）DB upsert 成功后
// 调 Invalidate 删除对应 Redis key（del-on-write）。相比"写穿更新
// 缓存"，删 key 让下次读自然回填最新 DB 状态，实现最简单且不存在
// 写缓存失败的窗口；代价是写后第一次读多一次 DB 往返（可接受）。
package routingopt

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// affinityKeyPrefix 是亲和力缓存的 Redis key 前缀；完整 key 为
// routingopt:affinity:<userID>。
const affinityKeyPrefix = "routingopt:affinity:"

// hash field 名（单 key 多 field，一次 HGetAll 全取）。
const (
	affinityFieldTTD = "task_type_distribution" // map[string]int JSON
	affinityFieldPP  = "preferred_providers"    // map[string]float64 JSON
	affinityFieldNeg = "negative"               // "1" = 负缓存标记
)

// TTL 默认值（设计文档：正缓存 1h，负缓存 30s）。测试可经
// newAffinityCache 注入更小值。
const (
	defaultAffinityTTL    = time.Hour
	defaultAffinityNegTTL = 30 * time.Second
)

// affinitySource 抽象 DB 回源，测试可 stub（*UserAffinityDAO 天然满足）。
type affinitySource interface {
	GetByUserID(ctx context.Context, userID string) (*UserAffinity, error)
}

// AffinityCache 是 UserAffinityDAO.GetByUserID 的 Redis 读缓存。
// 零值不可用；经 NewAffinityCache / newAffinityCache 构造。
type AffinityCache struct {
	src    affinitySource // DB 回源；nil → 一律返回"无数据"（不 panic）
	rdb    *redis.Client  // nil → 直查 DB 降级（等同无缓存）
	ttl    time.Duration  // 正缓存 TTL（默认 1h）
	negTTL time.Duration  // 负缓存 TTL（默认 30s）
}

// NewAffinityCache 构造生产用亲和力缓存。rdb 为 nil 时退化为 DB 直查
// （行为等同无缓存）；pool 为 nil 时所有 Get 返回"无数据"（不 panic，
// 对齐错误纪律）。TTL 取文档默认值。
func NewAffinityCache(pool *pgxpool.Pool, rdb *redis.Client) *AffinityCache {
	// pool 为 nil 时保持 src 为 nil（Get 一律返回"无数据"）——DAO 持有
	// nil pool 会在 QueryRow 处 panic，这里提前短路对齐错误纪律。
	var src affinitySource
	if pool != nil {
		src = NewUserAffinityDAO(pool)
	}
	return newAffinityCache(src, rdb, defaultAffinityTTL, defaultAffinityNegTTL)
}

// newAffinityCache 是可注入 TTL / 回源实现的内部构造器（测试用）。
func newAffinityCache(src affinitySource, rdb *redis.Client, ttl, negTTL time.Duration) *AffinityCache {
	if ttl <= 0 {
		ttl = defaultAffinityTTL
	}
	if negTTL <= 0 {
		negTTL = defaultAffinityNegTTL
	}
	return &AffinityCache{src: src, rdb: rdb, ttl: ttl, negTTL: negTTL}
}

// Get 返回用户亲和度，语义对齐 DAO：
//   - 找到   → (affinity, nil)
//   - 无数据 → (nil, nil)   （正缓存不含该 key 且 DB 也 miss / 负缓存命中）
//   - DB 故障 → (nil, err)  （Redis 故障不返回错误，只降级）
//
// (nil, nil) 与 (nil, err) 的区分让调用方（enhancer）维持"错误=首次
// 请求、nil=确认无数据"的原有语义。
func (c *AffinityCache) Get(ctx context.Context, userID string) (*UserAffinity, error) {
	if c == nil || c.src == nil || userID == "" {
		return nil, nil
	}
	if c.rdb == nil {
		// 降级路径：无 Redis，行为等同无缓存（不计数——没有缓存语义）。
		return c.getFromDB(ctx, userID)
	}

	key := affinityKeyPrefix + userID
	fields, err := c.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		// Redis 故障：slog 降级 DB 直查，绝不阻塞路由。计为 Miss
		//（本次确实回源了）。
		slog.DebugContext(ctx, "routingopt: affinity cache read failed, falling back to DB",
			"err", err, "user_id", userID)
		RecordCacheMiss()
		return c.getFromDB(ctx, userID)
	}
	if len(fields) == 0 {
		// 真正的 miss：回源 DB 并回填。
		RecordCacheMiss()
		return c.getFromDBAndFill(ctx, userID, key)
	}
	if _, ok := fields[affinityFieldTTD]; ok {
		// 正命中。
		RecordCacheHit()
		aff, derr := decodeAffinity(fields)
		if derr != nil {
			// 脏数据（字段版本混用等）：删 key 回源重填，按 miss 处理。
			slog.WarnContext(ctx, "routingopt: affinity cache entry undecodable, refetching",
				"err", derr, "user_id", userID)
			c.delKey(ctx, key)
			RecordCacheMiss()
			return c.getFromDBAndFill(ctx, userID, key)
		}
		return aff, nil
	}
	if _, ok := fields[affinityFieldNeg]; ok {
		// 负缓存命中：30s 内已确认 DB 无数据，直接返回，不回源、不计数。
		return nil, nil
	}
	// 只有未知 field 的畸形条目：按 miss 处理（回源会覆盖整个 key 语义）。
	RecordCacheMiss()
	return c.getFromDBAndFill(ctx, userID, key)
}

// Invalidate 删除该用户的缓存条目（正/负缓存一并）。写穿/失效用：
// UpdateUserAffinity 在 DB upsert 成功后调用，保持缓存一致性。
// 失败只 slog，绝不上抛——最坏情况是缓存多活一个 TTL。
func (c *AffinityCache) Invalidate(userID string) {
	if c == nil || c.rdb == nil || userID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.delKey(ctx, affinityKeyPrefix+userID)
}

// delKey 删除缓存 key，失败仅降级日志（调用方均无法利用该错误）。
func (c *AffinityCache) delKey(ctx context.Context, key string) {
	if err := c.rdb.Del(ctx, key).Err(); err != nil {
		slog.DebugContext(ctx, "routingopt: affinity cache invalidation failed",
			"err", err, "key", key)
	}
}

// getFromDB 直查 DB（无缓存语义）：无数据返回 (nil, nil) 以外的错误
// 原样上抛，ErrNoRows 归一为 (nil, nil)。
func (c *AffinityCache) getFromDB(ctx context.Context, userID string) (*UserAffinity, error) {
	aff, err := c.src.GetByUserID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return aff, err
}

// getFromDBAndFill 回源 DB 并按结果回填 Redis（正缓存或负缓存）。
func (c *AffinityCache) getFromDBAndFill(ctx context.Context, userID, key string) (*UserAffinity, error) {
	aff, err := c.src.GetByUserID(ctx, userID)
	switch {
	case err == nil && aff != nil:
		c.fillPositive(ctx, key, aff)
		return aff, nil
	case errors.Is(err, pgx.ErrNoRows):
		c.fillNegative(ctx, key)
		return nil, nil
	default:
		// DB 故障：不回填（避免把故障窗口固化为缓存内容），错误上抛。
		return nil, err
	}
}

// fillPositive 写入正缓存：两个 hash field + 正 TTL。HDel 清掉可能残留
// 的负缓存标记（同一 key 先负后正的场景）。失败仅日志——下次读自动重填。
func (c *AffinityCache) fillPositive(ctx context.Context, key string, aff *UserAffinity) {
	ttd, err1 := json.Marshal(aff.TaskTypeDistribution)
	pp, err2 := json.Marshal(aff.PreferredProviders)
	if err1 != nil || err2 != nil {
		slog.WarnContext(ctx, "routingopt: affinity cache marshal failed (skipped fill)",
			"err_ttd", err1, "err_pp", err2)
		return
	}
	pipe := c.rdb.TxPipeline()
	pipe.HDel(ctx, key, affinityFieldNeg)
	pipe.HSet(ctx, key, map[string]interface{}{
		affinityFieldTTD: ttd,
		affinityFieldPP:  pp,
	})
	pipe.Expire(ctx, key, c.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		slog.DebugContext(ctx, "routingopt: affinity cache fill failed",
			"err", err, "key", key)
	}
}

// fillNegative 写入负缓存标记（negTTL 窗口内不再回源 DB）。
func (c *AffinityCache) fillNegative(ctx context.Context, key string) {
	pipe := c.rdb.TxPipeline()
	pipe.HSet(ctx, key, affinityFieldNeg, "1")
	pipe.Expire(ctx, key, c.negTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		slog.DebugContext(ctx, "routingopt: affinity negative cache fill failed",
			"err", err, "key", key)
	}
}

// decodeAffinity 从 HGetAll 结果还原 UserAffinity（只填两个缓存字段）。
func decodeAffinity(fields map[string]string) (*UserAffinity, error) {
	aff := &UserAffinity{}
	if raw, ok := fields[affinityFieldTTD]; ok {
		if err := json.Unmarshal([]byte(raw), &aff.TaskTypeDistribution); err != nil {
			return nil, err
		}
	}
	if raw, ok := fields[affinityFieldPP]; ok {
		if err := json.Unmarshal([]byte(raw), &aff.PreferredProviders); err != nil {
			return nil, err
		}
	}
	return aff, nil
}
