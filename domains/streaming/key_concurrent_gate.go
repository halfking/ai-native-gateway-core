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
// 旁路/内部回环探针/无验证模式）与 IsInternal key 不设闸。
//
// 2026-11-15 修正（211 生产反馈轮）：并发帽**只执法显式配置**——
// ConfiguredConcurrent（NULL 与 0 都不设帽）。原实现 NULL 落 tier 默认
// （EffectiveConcurrent，default 档=6）：存量 82 个 active key 的 NULL 列
// 在执法接入前从未代表「6 并发」，接入即全体被帽 = 行为突变，且闸在
// telemetry 之前拦 ⇒ 拒绝零可观测（无 request_logs 行、无日志、无指标）。
// 显式正值照常执法（帽来自 key 自己的配置——「限流按 key 配置」的语义）。
//
// 口径边界：进程内计数（不跨实例聚合）——与 RPM 滑窗的进程内语义同档；
// 跨实例精确并发归 Redis 限流器（登记为后续面）。全局资源兜底是
// LargeIngress 的并发槽（ingress_capacity_exceeded），两者维度不同：本闸
// 按 key 配置执法，槽门保护进程自身——executors 侧的软告警保留为纵深观察。
//
// 429 头语义：Retry-After: 1（并发槽随请求结束即时回落）；Remaining 为当前
// 剩余额度（可为 0）。拒绝可观测：WARN 日志（key_concurrent_gate_rejected）
// + llm_gateway_rate_limit_rejections_total{reason=key_concurrent_limit}。
package streaming

import (
	"log/slog"
	"net/http"
	"sync"
)

// KeyConcurrentGate 按调用方 API key 执法并发帽。
type KeyConcurrentGate struct {
	mu   sync.Mutex
	used map[int]int // keyID -> 在飞请求数
}

// NewKeyConcurrentGate 创建 per-key 并发闸。
func NewKeyConcurrentGate() *KeyConcurrentGate {
	return &KeyConcurrentGate{used: make(map[int]int)}
}

// Wrap 包装 handler。每个调用返回独立闭包捕获各自的 next——**不得**把
// next 存进实例字段：2026-11-15 S09 下钻轮三方 A/B 定案（39b4d8e5a=chat
// 形状 ✓ / 79237f4d4 与后续=responses 形状 ✗），单实例对 chat/messages/
// responses 三个路由各调一次 Wrap 时，g.next 被最后一次调用覆盖 ⇒
// /v1/chat/completions 实际路由到 responsesHandler（chat 面全量反转为
// responses 形状——共享实例多处 Wrap=后者遮蔽前者，D-S4 双路由遮蔽同族）。
// 钉测：TestKeyConcurrentGate_WrapPerRoute。
func (g *KeyConcurrentGate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.serve(w, r, next)
	})
}

func (g *KeyConcurrentGate) serve(w http.ResponseWriter, r *http.Request, next http.Handler) {
	keyInfo := VerifiedKeyInfoFromContext(r.Context())
	if keyInfo == nil || keyInfo.IsInternal {
		next.ServeHTTP(w, r)
		return
	}
	// explicit-only（2026-11-15 修正）：NULL/0 = 不设帽；显式正值才执法。
	limit := keyInfo.ConfiguredConcurrent()
	if limit <= 0 {
		next.ServeHTTP(w, r)
		return
	}

	g.mu.Lock()
	if g.used[keyInfo.ID] >= limit {
		used := g.used[keyInfo.ID]
		g.mu.Unlock()
		// 拒绝可观测（本轮补齐）：WARN + 指标（reason=key_concurrent_limit，
		// 与 RPM 族的 llm_gateway_rate_limit_rejections_total 同家族）——
		// 闸在 telemetry 之前拦，不写 request_logs，这两条是唯一痕迹。
		outcome := rateLimitOutcome{Blocked: true, Limit: limit, Remaining: 0, ResetSec: 1, Reason: "key_concurrent_limit"}
		recordGatewayRateLimitRejection(outcome)
		slog.Warn("key_concurrent_gate_rejected",
			"api_key_id", keyInfo.ID,
			"path", r.URL.Path,
			"limit", limit,
			"in_flight", used)
		// Blocked=true：走 writeRateLimitHeaders 的标准拒绝头路径
		//（Retry-After / X-RateLimit-*）。并发槽随请求结束即时回落 ⇒
		// ResetSec=1。
		writeRateLimitHeaders(w, outcome)
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

	next.ServeHTTP(w, r)
}

// requestIDOf 提取请求 ID（无则退回占位）——错误信封字段。
func requestIDOf(r *http.Request) string {
	if v := r.Header.Get("X-Request-Id"); v != "" {
		return v
	}
	return "req-concurrent-limit"
}
