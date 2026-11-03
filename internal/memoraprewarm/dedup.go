// dedup.go —— 「每会话至多一次」的执行器（P1-2③ W3，ADR §5）。
//
// ADR §5 的风险条写死了一条口径：
//
//	「Redis 写放大：预热每会话至多一次（首请求），读走既有 session hash
//	 —— 无新增热路径成本。」
//
// ⇒ 预热的**触发**必须是**每会话一次**，而不是「每请求一次」。
// ⇒ 本文件是这条口径的执行器：Wiring.Apply 每请求都会到，但它先问本表
//
//	「这个会话预热过没有？」⇒ 答「有」就直接跳过 ⇒ 请求 N+1..N+k 不再
//	重复召回、不再重复旁路写、不再起 goroutine。
//
// ★ 为什么表必须**有界**：
//
//	会话数无界 ⇒ 无界 map ⇒ 内存泄漏（这正是本条契约要避免的写放大的
//	另一面：省下了 Redis 写，却赔进去一份常驻堆）。
//	⇒ 两条约束：① 条目按 TTL 过期（会话 TTL 同量级）；
//	② 容量硬上限（超出按最旧淘汰到水位线）⇒ 内存**恒有界**。
//
// ★ 并发口径：同一会话的并发请求可能同时问本表 ⇒ 必须**恰好一个**赢家
//
//	（⇒ 抢占在锁内完成 ⇒ 其余请求看到「已预热」直接跳过）。
package memoraprewarm

import (
	"sort"
	"sync"
	"time"
)

// 去重表的两个参数。
const (
	// DefaultDedupMaxSessions 是去重表容量上限（条目数）。
	// 取 1w：按「单会话平均存活 24h」估算，常驻条目数 = 24h 内活跃会话数；
	// 1w 条 × ~100B ≈ 1MB 量级 ⇒ 常驻内存可忽略，而超限淘汰的代价
	// （偶发一次 O(n) 扫描）远小于「放过一次重复预热」的代价。
	DefaultDedupMaxSessions = 10000
	// DefaultDedupTTL 是「本会话已预热」标记的存活时长。
	// 与会话 TTL（DefaultSessionTTL）同量级：会话本身活着的期间不重复
	// 预热；会话真死之后标记过期，允许该 session id 被复用时重新预热。
	DefaultDedupTTL = DefaultSessionTTL
)

// sessionDedup 记录「本会话已发起过预热」。
type sessionDedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
	max  int
}

// newSessionDedup 构造。ttl/max 非正 ⇒ 取默认值。
func newSessionDedup(ttl time.Duration, max int) *sessionDedup {
	if ttl <= 0 {
		ttl = DefaultDedupTTL
	}
	if max <= 0 {
		max = DefaultDedupMaxSessions
	}
	return &sessionDedup{seen: make(map[string]time.Time), ttl: ttl, max: max}
}

// claim 抢占本会话的预热权。
//
// 语义：**首次返回 true**（本次调用应当发起预热）；TTL 内的重复调用返回
// false（⇒ 跳过）。同一会话的并发调用中**恰好一个**返回 true。
//
// nil 接收者 ⇒ 恒 true（不装配去重表 ⇒ 退化成旧行为；装配面保证非 nil）。
func (d *sessionDedup) claim(sessionID string, now time.Time) bool {
	if d == nil || sessionID == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if at, ok := d.seen[sessionID]; ok && now.Sub(at) < d.ttl {
		return false // TTL 内已预热 ⇒ 跳过（ADR §5：每会话至多一次）
	}
	d.seen[sessionID] = now
	if len(d.seen) > d.max {
		d.evictLocked(now)
	}
	return true
}

// evictLocked 把表压回水位线：先删过期条目；仍超限则按时间从旧到新淘汰。
//
// 调用方必须已持 d.mu。
func (d *sessionDedup) evictLocked(now time.Time) {
	for k, at := range d.seen {
		if now.Sub(at) >= d.ttl {
			delete(d.seen, k)
		}
	}
	if len(d.seen) <= d.max {
		return
	}
	type entry struct {
		key string
		at  time.Time
	}
	items := make([]entry, 0, len(d.seen))
	for k, at := range d.seen {
		items = append(items, entry{key: k, at: at})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].at.Before(items[j].at) })
	// 淘汰到 75% 水位 ⇒ 避免每插入一条都触发一次全表扫描。
	target := d.max * 3 / 4
	for i := 0; i < len(items) && len(d.seen) > target; i++ {
		delete(d.seen, items[i].key)
	}
}

// size 返回当前条目数（供测试断言内存有界）。
func (d *sessionDedup) size() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}
