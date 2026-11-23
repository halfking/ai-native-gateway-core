// key_concurrent_gate.go — per-key 并发闸（2026-11-15 限流统一按 key 轮）。
//
// 背景：api_keys.rate_limit_concurrent 列 + admin 面 + tier 默认（verifier
// tierDefaults 第二列）自始存在，但数据面执法点是 executors 的
// KeyConcurrentLimit → credential/limiter.AcquireAll——TryAcquire 失败仅
// slog.Warn 放行（「配置了也只会告警，从不拒绝」）。本闸把该配置变成真执法：
// 进程内 per-key 计数器，满 → 429 标准信封（rate_limit_error /
// rate_limit_exceeded + X-RateLimit-* + Retry-After）。
//
// 豁免面与 RPM 链一致：无 key 身份（VerifiedKeyInfoFromContext=nil 且无
// verifier 回退：静态 key 旁路/内部回环探针/无验证模式）与 IsInternal key
// 不设闸。
//
// 2026-11-15 修正（211 生产反馈轮）：并发帽**只执法显式配置**——
// ConfiguredConcurrent（NULL 与 0 都不设帽）。原实现 NULL 落 tier 默认
// （EffectiveConcurrent，default 档=6）：存量 82 个 active key 的 NULL 列
// 在执法接入前从未代表「6 并发」，接入即全体被帽 = 行为突变，且闸在
// telemetry 之前拦 ⇒ 拒绝零可观测（无 request_logs 行、无日志、无指标）。
// 显式正值照常执法（帽来自 key 自己的配置——「限流按 key 配置」的语义）。
//
// 2026-11-15 executors 对齐（双重标准收口轮）：handler.go / messages.go /
// responses.go 的 KeyConcurrentLimit 四处调用点统一改用 ConfiguredConcurrent
// （explicit-only），与本闸语义对齐。EffectiveConcurrent 保留给 executors
// 软告警路径（从不拒绝，历史行为不变），但不再作为真执法依据。
// AST 钉测 TestKeyConcurrentLimitProducersUseConfigured 守住这一口径。
//
// 2026-11-15 三点审计轮（本轮）：
//
//	① key 标识唯一性——ID<=0 的身份**绝不落桶**：used[0] 会把所有无效身份
//	  聚成一个共享桶，不同 key 互相挤占 = 误拒。宁可 bypass（无唯一标记
//	  即无帽可执）+ WARN 可观测。生产 Verify 链的 ID 来自 ak.id 主键恒>0，
//	  此守卫是防御面（坏缓存/畸形构造）。
//	② 身份回退解析——embeddings/audio(STT/TTS)/images/videos/count_tokens
//	  这些面不写 sanitizeVerifiedKeyContextKey（各自直接 keyVerifier.Verify），
//	  闸包上去会因读不到身份而空转 ⇒ SetVerifier 装配回退：context 无身份
//	  时按 Bearer token 自行 Verify（cache-backed，开销一次 map 命中）。
//	  refine/analyze/summarize/mcp **不包闸**：其 LLM 步环回 chat 面携带
//	  同一把 key，chat 面闸已计一次——外层再包会把一次逻辑调用算成两个
//	  并发槽（与 authenticateNoRPM 的 RPM 不双计纪律同款）。
//	③ 分钟实时数据——minutePeak/minuteRejected/minuteLimit 三张按 key 累
//	  计表，SnapshotAndResetMinute 供 bg.KeyConcurrencyStatsWriter 每分钟
//	  UPSERT 进 api_key_concurrency_minute（迁移 965）：每个 key 的实时
//	  在飞/峰值/被拒数按分钟落库。
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
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // 与 sanitize_auth.go 同一鉴权面
)

// KeyIdentityVerifier 是并发闸的身份回退解析口（*authentication.KeyVerifier
// 天然满足）。仅当请求上下文没有 memo 身份时使用（见 serve）。
type KeyIdentityVerifier interface {
	Verify(ctx context.Context, rawKey string) (*authentication.KeyInfo, error)
}

// KeyConcurrencySample 是一个分钟窗口内单个 key 的并发观测样本
// （bg.KeyConcurrencyStatsWriter 的写入单元）。
type KeyConcurrencySample struct {
	KeyID        int // api_keys.id（恒>0，见 serve 的唯一性守卫）
	InFlight     int // 采样时刻在飞请求数
	PeakInFlight int // 本窗口内在飞峰值
	Rejected     int // 本窗口内 429 拒绝次数
	Limit        int // 本窗口内生效的显式并发帽（0=无帽）
}

// KeyConcurrentGate 按调用方 API key 执法并发帽。
type KeyConcurrentGate struct {
	mu   sync.Mutex
	used map[int]int // keyID -> 在飞请求数（keyID 恒>0，见 serve 守卫）

	// 分钟观测面（③）：bg worker 每分钟 SnapshotAndResetMinute 取走并清零。
	minutePeak     map[int]int // keyID -> 本窗口在飞峰值
	minuteRejected map[int]int // keyID -> 本窗口被 429 次数
	minuteLimit    map[int]int // keyID -> 本窗口最后生效的显式帽

	// verifier 身份回退（②）：nil = 无回退（仅 context 身份，测试/旧装配）。
	verifier KeyIdentityVerifier
}

// NewKeyConcurrentGate 创建 per-key 并发闸。
func NewKeyConcurrentGate() *KeyConcurrentGate {
	return &KeyConcurrentGate{
		used:           make(map[int]int),
		minutePeak:     make(map[int]int),
		minuteRejected: make(map[int]int),
		minuteLimit:    make(map[int]int),
	}
}

// SetVerifier 装配身份回退解析器。必须在 ServeHTTP 之前调用（main.go 装配
// 期一次）；nil 为合法值（清空回退，测试用）。
func (g *KeyConcurrentGate) SetVerifier(v KeyIdentityVerifier) {
	g.mu.Lock()
	g.verifier = v
	g.mu.Unlock()
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

// resolveIdentity 取调用方 key 身份：先读请求上下文 memo（chat/messages/
// responses 面由 prepareSanitizeRequest 写入），缺失时走 verifier 回退
// （embeddings/audio/images 等面不写 memo——闸需要自行解析才能不空转）。
// 回退 Verify 失败 = 无身份（无效 key 由各 handler 自己的鉴权拒绝，
// 闸不越权报错）。
func (g *KeyConcurrentGate) resolveIdentity(r *http.Request) *authentication.KeyInfo {
	if info := VerifiedKeyInfoFromContext(r.Context()); info != nil {
		return info
	}
	g.mu.Lock()
	verifier := g.verifier
	g.mu.Unlock()
	if verifier == nil {
		return nil
	}
	rawKey := extractBearerToken(r)
	if rawKey == "" {
		return nil
	}
	info, err := verifier.Verify(r.Context(), rawKey)
	if err != nil || info == nil {
		return nil
	}
	return info
}

func (g *KeyConcurrentGate) serve(w http.ResponseWriter, r *http.Request, next http.Handler) {
	keyInfo := g.resolveIdentity(r)
	if keyInfo == nil || keyInfo.IsInternal {
		next.ServeHTTP(w, r)
		return
	}
	// ① 唯一性守卫（三点审计轮）：ID<=0 的身份绝不落桶——used[0] 会把所有
	// 无效身份聚成共享桶互相挤占。bypass + WARN（生产 Verify 链 ID 恒>0，
	// 命中此分支即为装配/数据异常，需要可观测）。
	if keyInfo.ID <= 0 {
		slog.Warn("key_concurrent_gate: non-positive key id, bypassing",
			"api_key_id", keyInfo.ID,
			"path", r.URL.Path)
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
	g.minuteLimit[keyInfo.ID] = limit
	if g.used[keyInfo.ID] >= limit {
		used := g.used[keyInfo.ID]
		g.minuteRejected[keyInfo.ID]++
		g.mu.Unlock()
		observeModalityIdentity(r, keyInfo)
		markModalityRateLimited(r, "key_concurrent_limit")
		// 拒绝可观测（本轮补齐）：WARN + 指标（reason=key_concurrent_limit，
		// 与 RPM 族的 llm_gateway_rate_limit_rejections_total 同家族）。
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
		if r.Context().Value(modalityIngressKey{}) == nil {
			RecordGatewayAdmissionRejection(r, http.StatusTooManyRequests, "key_concurrent_limit", keyInfo)
		}
		return
	}
	g.used[keyInfo.ID]++
	if g.used[keyInfo.ID] > g.minutePeak[keyInfo.ID] {
		g.minutePeak[keyInfo.ID] = g.used[keyInfo.ID]
	}
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

// SnapshotAndResetMinute 返回刚过去的观测窗口内每个有活动的 key 的并发
// 样本，并重置窗口累计量（peak/rejected/limit）。in_flight 是采样时刻的
// 实时在飞数（不重置——它是即时值不是窗口累计值）。
//
// 无活动的 key（窗口内既无在飞也无被拒）不产生样本：零行本身即信息，
// 表保持极小。调用方 = bg.KeyConcurrencyStatsWriter（每 60s 一次）。
func (g *KeyConcurrentGate) SnapshotAndResetMinute() []KeyConcurrencySample {
	g.mu.Lock()
	defer g.mu.Unlock()
	seen := make(map[int]struct{}, len(g.used)+len(g.minutePeak)+len(g.minuteRejected))
	for id := range g.used {
		seen[id] = struct{}{}
	}
	for id := range g.minutePeak {
		seen[id] = struct{}{}
	}
	for id := range g.minuteRejected {
		seen[id] = struct{}{}
	}
	samples := make([]KeyConcurrencySample, 0, len(seen))
	for id := range seen {
		samples = append(samples, KeyConcurrencySample{
			KeyID:        id,
			InFlight:     g.used[id],
			PeakInFlight: g.minutePeak[id],
			Rejected:     g.minuteRejected[id],
			Limit:        g.minuteLimit[id],
		})
	}
	// 重置前保存旧 limit（播种需要）
	oldLimit := g.minuteLimit
	g.minutePeak = make(map[int]int)
	g.minuteRejected = make(map[int]int)
	g.minuteLimit = make(map[int]int)
	// 窗口播种：仍持有的在飞请求是下一窗口峰值的下界（长请求跨窗时
	// peak ≥ 在飞数，否则跨窗窗口会出现 peak=0 而 in_flight=1 的自相矛盾
	// 读数）；limit 同理保留仍生效的帽（否则跨窗样本 limit=0 而 peak>0
	// 自相矛盾——有峰值意味着有帽在执法）。
	for id, inFlight := range g.used {
		if inFlight > 0 {
			g.minutePeak[id] = inFlight
			// 播种 limit：从重置前的旧值继承（上一窗口采到的帽在本窗口
			// 开始时刻仍然生效——同一 key 的帽不会中途变化）。
			if limit := oldLimit[id]; limit > 0 {
				g.minuteLimit[id] = limit
			}
		}
	}
	return samples
}

// minuteSampleInterval 是 bg worker 的采样间隔基准（worker 侧 env 可调，
// 这里仅作为闸文档的语义锚点：窗口≈1 分钟，minute_ts 取采样时刻的墙钟分钟）。
const minuteSampleInterval = time.Minute

// requestIDOf 提取请求 ID（无则退回占位）——错误信封字段。
func requestIDOf(r *http.Request) string {
	if v := r.Header.Get("X-Request-Id"); v != "" {
		return v
	}
	return "req-concurrent-limit"
}
