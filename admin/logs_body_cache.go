// Copyright 2026 kaixuan.ai
// In-memory LRU + TTL cache for `fetchRequestBodies` results.
//
// 2026-08-17 OPTIMIZATION: dashboard "实时请求流 → 点击请求" 用户经常
// "开 → 关 → 再开" 同一个 request_id 来回比对冷数据。第一次 fetch 走
// columnar 5s+（metadata + body），重复点击再 fetch 浪费 5s。把结果按
// request_id 缓存 5min，重复点击走 cache < 1ms。
//
// 设计要点：
//   - LRU 1024 entries ≈ 20MB（每条 body ~10KB JSONB）。超容量淘汰最久未用。
//   - TTL 5min：body 数据写入后很少改，5min 后 re-fetch（hot 命中便宜）。
//   - 只缓存 nil/200 结果；不缓存 transport 错误（超时、conn refused），
//     避免上游故障被永久错误缓存。
//   - sql.ErrNoRows（两端都没找到）也缓存 5min — 客户端重试同样结果，
//     避免每次都打 PG。
//   - 缓存值是 immutable snapshot（Go map 引用透明），不会因底层 row 被
//     重新写入而串味；TTL 是版本号。
package admin

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/cache"
)

// bodyFetchEntry 是单条缓存值的容器。requestBody/responseBody 已经是
// 解码后的 Go 值（map[string]any / string / nil），不存在可变引用。
type bodyFetchEntry struct {
	body     any // decoded request_body; nil 表示请求没有 body
	resp     any // decoded response_body
	storedAt time.Time
}

// bodyFetchCache 是 fetchRequestBodies 的请求级 LRU + TTL 缓存。
// 线程安全：底层 cache.LRU 自带 mutex，stats 用 atomic 无锁。
type bodyFetchCache struct {
	lru       *cache.LRU[string, bodyFetchEntry]
	ttl       time.Duration
	cap       int // 容量上限快照，仅供 stats 端点回显（避免前端硬编码）
	hits      atomic.Uint64
	misses    atomic.Uint64
	evictions atomic.Uint64
}

// newBodyFetchCache 构造缓存。cap < 1 panic（与 cache.LRU 一致）。
func newBodyFetchCache(cap int, ttl time.Duration) *bodyFetchCache {
	if ttl <= 0 {
		ttl = 5 * time.Minute // safety net for misconfig
	}
	return &bodyFetchCache{
		lru: cache.NewLRU[string, bodyFetchEntry](cap),
		ttl: ttl,
		cap: cap,
	}
}

// Get 返回缓存值。ok=false 表示未命中（包含 nil/已过期两种情况）。
// 命中时同步累加 hits atomic。
func (c *bodyFetchCache) Get(requestID string) (entry bodyFetchEntry, ok bool) {
	if c == nil {
		return bodyFetchEntry{}, false
	}
	e, ok := c.lru.Get(requestID)
	if !ok {
		c.misses.Add(1)
		return bodyFetchEntry{}, false
	}
	// TTL 检查（在 LRU 锁内已完成 Get，但 Get 不会自动过期）。
	// 用 time.Since 而非 Now() 以保持语义清晰。
	if time.Since(e.storedAt) > c.ttl {
		c.lru.Delete(requestID)
		c.misses.Add(1)
		return bodyFetchEntry{}, false
	}
	c.hits.Add(1)
	return e, true
}

// Put 写入缓存。err == nil 时存 (body, resp)；err == sql.ErrNoRows
// 时存 (nil, nil) 让"两端都没找到"也走缓存；其它 err 不缓存（rule 22
// 错误缓存防抖：transport-class error 必须能被重新尝试）。
//
// 返回 (evictedKey, evicted) 仅供 stats；调用方忽略。
func (c *bodyFetchCache) Put(requestID string, body, resp any) {
	if c == nil {
		return
	}
	// nil/nil 表示 ErrNoRows 路径 — 仍然缓存（ttl 内重复点击同样结果）。
	// 这里与其它 err 一样落到下方 entry 构造，不需要单独分支。
	_ = body
	entry := bodyFetchEntry{
		body:     body,
		resp:     resp,
		storedAt: time.Now(),
	}
	evictedKey, evicted := c.lru.Put(requestID, entry)
	if evicted {
		c.evictions.Add(1)
		_ = evictedKey // LRU 已删除;此处仅累加 stats
	}
}

// Stats 返回当前快照，用于 metrics endpoint 或日志。
func (c *bodyFetchCache) Stats() (size, hits, misses, evictions int) {
	if c == nil {
		return 0, 0, 0, 0
	}
	return c.lru.Len(), int(c.hits.Load()), int(c.misses.Load()), int(c.evictions.Load())
}

// handleBodyFetchCacheStats 是缓存的可观测端点 — GET /api/admin/logs/body-cache-stats。
// 返回当前 size / hits / misses / evictions / cap / hit_rate 快照。
// 鉴权：admin()（与 /api/admin/compression/stats、data-lifecycle/stats 同级的
// 诊断端点；计数器为进程级聚合、不含租户数据）。前端仅在 super admin 视图展示。
// ops 用于判断 cold path 是否被 cache 缓解。
//
// 200 OK 示例:
//
//	{"size": 142, "hits": 1023, "misses": 287, "evictions": 5, "hit_rate": 0.781, "cap": 1024}
//
// 503 when 缓存未初始化（h.bodyFetchCache == nil, 理论上 NewHandler 总会初始化）。
func (h *Handler) handleBodyFetchCacheStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.bodyFetchCache == nil {
		writeError(w, http.StatusServiceUnavailable, "body cache not initialized")
		return
	}
	size, hits, misses, evictions := h.bodyFetchCache.Stats()
	var hitRate float64
	total := hits + misses
	if total > 0 {
		hitRate = float64(hits) / float64(total)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"size":      size,
		"hits":      hits,
		"misses":    misses,
		"evictions": evictions,
		"hit_rate":  hitRate,
		"cap":       h.bodyFetchCache.cap,
	})
}
