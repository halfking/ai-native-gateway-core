// key_concurrent_gate.go — per-key 并发闸（2026-11-15 限流统一按 key 轮）。
//
// 背景：api_keys.rate_limit_concurrent 列 + admin 面 + tier 默认（verifier
// tierDefaults 第二列）自始存在，但数据面执法点是 executors 的
// KeyConcurrentLimit → credential/limiter.AcquireAll——TryAcquire 失败仅
// slog.Warn 放行（「配置了也只会告警，从不拒绝」）。本闸把该配置变成真执法：
// 进程内 per-key 计数器，满 → 429 标准信封（rate_limit_error /
// rate_limit_exceeded + X-RateLimit-* + Retry-After）。
//
// 豁免面与 RPM 链一致：无 key 身份（VerifiedKeyInfoFromContext=nil：静态 key
// 旁路/内部回环探针/无验证模式）与 IsInternal key 不设闸；NULL 列取 tier
// 默认（EffectiveConcurrent），0 = 显式无限。
//
// 口径边界：进程内计数（不跨实例聚合）——与 RPM 滑窗的进程内语义同档；
// 跨实例精确并发归 Redis 限流器（登记为后续面）。全局资源兜底是
// LargeIngress 的并发槽（ingress_capacity_exceeded），两者维度不同：本闸
// 按 key 配置执法，槽门保护进程自身——executors 侧的软告警保留为纵深观察。
//
// 429 头语义：Retry-After: 1（并发槽随请求结束即时回落）；Remaining 为当前
// 剩余额度（可为 0）。
package streaming

import (
	"net/http"
	"sync"
)

// KeyConcurrentGate 按调用方 API key 执法并发帽。
type KeyConcurrentGate struct {
	mu   sync.Mutex
	next http.Handler
	used map[int]int // keyID -> 在飞请求数
}

// NewKeyConcurrentGate 创建 per-key 并发闸。
func NewKeyConcurrentGate() *KeyConcurrentGate {
	return &KeyConcurrentGate{used: make(map[int]int)}
}

// Wrap 包装 handler。
func (g *KeyConcurrentGate) Wrap(next http.Handler) http.Handler {
	g.next = next
	return g
}

// ServeHTTP 实现 http.Handler。
func (g *KeyConcurrentGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	keyInfo := VerifiedKeyInfoFromContext(r.Context())
	if keyInfo == nil || keyInfo.IsInternal {
		g.next.ServeHTTP(w, r)
		return
	}
	limit := keyInfo.EffectiveConcurrent()
	if limit <= 0 {
		g.next.ServeHTTP(w, r)
		return
	}

	g.mu.Lock()
	if g.used[keyInfo.ID] >= limit {
		used := g.used[keyInfo.ID]
		g.mu.Unlock()
		// Blocked=true：走 writeRateLimitHeaders 的标准拒绝头路径
		//（Retry-After / X-RateLimit-*）。并发槽随请求结束即时回落 ⇒
		// ResetSec=1。
		writeRateLimitHeaders(w, rateLimitOutcome{Blocked: true, Limit: limit, Remaining: 0, ResetSec: 1, Reason: "key_concurrent_limit"})
		writeErrorJSONWithKindProto(protocolOfRequest(r), w, http.StatusTooManyRequests, requestIDOf(r),
			"Key concurrent request limit reached", "rate_limit_error", "rate_limit_exceeded", "rate_limit_exceeded",
			map[string]any{"stage": "key_concurrency", "limit": limit, "in_flight": used})
		return
	}
	g.used[keyInfo.ID]++
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		g.used[keyInfo.ID]--
		if g.used[keyInfo.ID] <= 0 {
			delete(g.used, keyInfo.ID)
		}
		g.mu.Unlock()
	}()

	g.next.ServeHTTP(w, r)
}

// requestIDOf 提取请求 ID（无则退回占位）——错误信封字段。
func requestIDOf(r *http.Request) string {
	if v := r.Header.Get("X-Request-Id"); v != "" {
		return v
	}
	return "req-concurrent-limit"
}
