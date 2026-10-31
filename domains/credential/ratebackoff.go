package credential

// ratebackoff.go — 429（限流）的**绑定级累计升级**退避器。
//
// # 这道改造补的是哪块缺口
//
// 2026-10-30 的 magpie 四域对比刷新轮（docs/research/2026-10-30-magpie四域对比刷新与优化方案.md
// §三 B5 / §五 A2，下轮主轴第一项）指出 magpie 对「持续限流」的税则契约
// （magpie 源码 internal/gateway/routing.go:1194 rateRest）：
//
//	rateRest：`1m << min(n-1, 10)` 封顶 30m；rateForget=30m 遗忘窗；
//	Retry-After 可超过退避值（vendor 明说了就信 vendor）。
//
// 拆成四条可断言的判据（§五 A2 的验收量具）：
//
//	① **单次 429 冷却 = 3min 不变**（本仓现值，writer.go coolingDuration）；
//	② **连续第 2 次 = 6min、第 3 次 = 12min、第 4 次 = 24min、第 5 次起 = 30min 封顶**
//	   （base 3min，shift 上限 4：3→6→12→24→30。用 min 封顶而非 2^4=48>30 的自然封顶，
//	   30min 是 magpie rateRest 的封顶值对齐）；
//	③ **遗忘窗后回 3min**（rateForget=30min：距上次 429 超过 30min ⇒ 计数归零；
//	   否则一个陈年的 429 会把绑定一直按在长冷却里）；
//	④ **Retry-After 信任上限 1h**（目标文本新增，收紧 coolingDuration 现有的 24h
//	   全局帽 —— 对 429 这一个 kind 而言，vendor 广告的等待超过 1h 几乎总是
//	   quota 类而不是 rate 类，超帽部分由 quota 路径（inferQuotaRecoverAt）自己管；
//	   升级退避值本身不受影响，仍以 30min 封顶）。
//
// # 红线（本轮不动的设计裁决）
//
// **429 不进断路器**（breaker.go:112-121，Wave 1 A4 2026-09-22 的定版）：
// 升级只作用于**绑定级冷却时长**（credential_model_bindings.unavailable_recover_at），
// 断路器对 KindRateLimit 照旧早退。**HALF_OPEN 探针撞 429 不加计数**
// ——「限流不是被验证的失败证据」，与 breaker.go:553 的探针语义同源；
// 本器由 writer.go 的 WriteOnError 驱动，而 probe 写路径（SetCredentialUnavailable
// / writeProtocolCredentialStateOnError 的 params.DispatchAttempt 分流）不经过它。
//
// # 载体（与 slowrest 同款的已知局限）
//
// 计数是**进程内**的（慢启动休息器 slowrest.go 同款）。多实例部署时第 2 次的
// 判定可能各实例各算一次 ⇒ 退避起点偏保守（少罚，不会多罚）。研究方案 §五 A2
// 的首选载体是 Redis（SETEX 30min），但 writer.go 现在没有 Redis 依赖
// （Writer 只持 DBQuerier），为「行为升级」单独引入跨域依赖的性价比不如
// 慢启动同款的进程内实现——已知局限与 slowrest ② 一致，先落地再观测。
//
// # 引用
//
//	domains/credential/writer.go —— WriteOnError KindRateLimit 接入点
//	domains/credential/slowrest.go —— 同款形态先例（模型粒度退避 + 遗忘窗）
//	domains/credential/breaker.go —— 429 不进断路器的红线（本器不触碰）

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// RateBackoffBase 是单次 429 的基准冷却（= coolingDuration 的现值）。
	// 判据①：单次 429 冷却不变。
	RateBackoffBase = 3 * time.Minute

	// rateBackoffMaxShift 是退避倍增的移位上限。
	// 3min<<4 = 48min > 30min 封顶 ⇒ 实际封顶由 rateBackoffMax 执行：
	// 3→6→12→24→30min（第 5 次起），与 magpie rateRest 的 30m 封顶对齐。
	rateBackoffMaxShift = 4

	// RateBackoffMax 是升级冷却的封顶值（magpie rateRest 顶格 30m）。
	RateBackoffMax = 30 * time.Minute

	// RateForget 是 429 计数的遗忘窗。
	// magpie: rateForget = 30min —— 距上次 429 超过 30min ⇒ 计数归零，
	// 恢复后第一时间回全速（判据③）。
	RateForget = 30 * time.Minute

	// MaxRateRetryAfter 是 429 这个 kind 对 Retry-After 的信任上限（判据④）。
	// 目标文本新增：超过 1h 的「限流等待」几乎总是 quota 类（window/monthly），
	// 那条路径有自己的恢复点推断（inferQuotaRecoverAt）；rate 路径信它 1h 封顶。
	// 注意：这是**收紧**——只影响 KindRateLimit，其他 kind 的 24h 全局帽不动。
	MaxRateRetryAfter = 1 * time.Hour
)

// RateRun 是一个 (凭据, 模型) 键上的 429 连败计数。
type RateRun struct {
	// n 是连续 429 次数（含本次）。
	n int
	// at 是最近一次 429 的时刻（用于遗忘窗）。
	at time.Time
}

// RateBackoffDecision 是一次 429 观察后的冷却判定。
type RateBackoffDecision struct {
	// Duration 是本次的绑定冷却时长。
	Duration time.Duration
	// Count 是当前的连续 429 计数（含本次）。
	Count int
	// Escalated 标记本次是否升级（false = 首次，= 旧固定 3min 路径）。
	Escalated bool
}

// RateBackoffTracker 实现 magpie 的 rateRest（多实例版的本仓落地形态）。
//
// ★ 并发安全；零值不可用，请用 NewRateBackoffTracker。
type RateBackoffTracker struct {
	mu   sync.Mutex
	runs map[string]RateRun

	// now 可注入时钟（测试用；生产用 time.Now）。
	now func() time.Time
}

// NewRateBackoffTracker 构造一个 429 累计升级退避器。
func NewRateBackoffTracker() *RateBackoffTracker {
	return &RateBackoffTracker{
		runs: make(map[string]RateRun),
		now:  time.Now,
	}
}

// SetClock 注入时钟（仅测试用）。
func (t *RateBackoffTracker) SetClock(now func() time.Time) {
	if t == nil || now == nil {
		return
	}
	t.mu.Lock()
	t.now = now
	t.mu.Unlock()
}

// Observe 记录一次 429，返回本次的绑定冷却时长。
//
// key 必须是**绑定粒度**的键：本仓用 RateBackoffKey(credentialID, model)
// （与 slowrest.SlowRestKey 同构——冷却落在 (凭据,模型) 绑定上，不动账号）。
//
// 与 slowrest 的差异：429 **第一次就罚**（现契约 = 3min 冷却，判据①），
// 升级发生在「连续再犯」——这与慢启动的「第一次放过」是两类归责
// （429 是上游明确说了"你被限流"的失败证据；首字节超时可能是自己 prompt 长）。
func (t *RateBackoffTracker) Observe(key string) RateBackoffDecision {
	if t == nil {
		return RateBackoffDecision{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	run := t.runs[key]
	// 遗忘窗：距上次 429 超过 RateForget ⇒ 计数归零（判据③）。
	if !run.at.IsZero() && now.Sub(run.at) >= RateForget {
		run.n = 0
	}
	run.n++
	run.at = now
	t.runs[key] = run
	t.pruneLocked(now)

	d := RateBackoffBase
	escalated := false
	if run.n > 1 {
		shift := run.n - 1
		if shift > rateBackoffMaxShift {
			shift = rateBackoffMaxShift
		}
		d = RateBackoffBase << shift
		if d > RateBackoffMax {
			d = RateBackoffMax
		}
		escalated = true
	}
	return RateBackoffDecision{Duration: d, Count: run.n, Escalated: escalated}
}

// pruneLocked 清掉过期条目（遗忘窗之外的键）。
//
// 与 slowrest.pruneLocked 同款门槛：键空间是 (凭据, 模型) 组合，
// 长跑进程会积累过期键；每次 Observe 至多扫 64 个，避免全量扫描。
func (t *RateBackoffTracker) pruneLocked(now time.Time) {
	if len(t.runs) < 1024 {
		return
	}
	scanned := 0
	for k, r := range t.runs {
		if now.Sub(r.at) >= RateForget {
			delete(t.runs, k)
		}
		scanned++
		if scanned >= 64 {
			return
		}
	}
}

// Count 返回某个键当前的 429 计数（测试与观测用）。
func (t *RateBackoffTracker) Count(key string) int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.runs[key].n
}

// defaultRateBackoff 是进程级的默认退避器。
var defaultRateBackoff = NewRateBackoffTracker()

// DefaultRateBackoff 返回进程级的默认 429 退避器。
func DefaultRateBackoff() *RateBackoffTracker { return defaultRateBackoff }

// RateBackoffKey 构造绑定粒度的退避键 "<credential_id>/<model>"。
//
// 与 slowrest.SlowRestKey 同构：升级**永远**落在 (凭据,模型) 绑定上，
// 同一凭据的其它模型不受影响。
func RateBackoffKey(credentialID int, model string) string {
	return strconv.Itoa(credentialID) + "/" + strings.TrimSpace(model)
}

// rateLimitedCoolingDuration 计算一次 429 的绑定冷却时长。
//
// 来源链（判据④ + coolingDuration 的既有语义）：
//  1. Retry-After（vendor 明说了就信 vendor）——但 429 只信到 1h
//     （MaxRateRetryAfter；超过的部分几乎总是 quota 类，由 quota 路径管）。
//     这一条**独立于计数**：即使第 5 次连败，vendor 说等 20s 就等 20s。
//  2. 无 Retry-After ⇒ 累计升级：3min→6min→12min→24min→30min 封顶。
//
// coolingDuration 的 24h 全局帽对其它 kind 不变（函数不动）。
func rateLimitedCoolingDuration(retryAfter time.Duration, t *RateBackoffTracker, key string) time.Duration {
	if retryAfter > 0 {
		if retryAfter > MaxRateRetryAfter {
			return MaxRateRetryAfter
		}
		return retryAfter
	}
	if t == nil {
		return RateBackoffBase
	}
	return t.Observe(key).Duration
}
