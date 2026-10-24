package credential

// slowrest.go — 慢启动（首字节超时）的**模型粒度**休息器。
//
// # 这道改造补的是哪块缺口
//
// 2026-10-24 的 magpie 刷新轮（docs/research/2026-10-24-magpie-v0.1.1367-…md
// §6-P0-4）指出 magpie 对「首字节超时」的归责契约（magpie 源码
// `internal/gateway/routing.go:314-318` 注释 + `:1135-1173` 实现）：
//
//	failSlow: the member hadn't begun its reply when its group's FirstToken
//	ran out, nothing of it sent — the next is asked. **Once, nobody rests, as
//	a long prompt is slow anywhere; again and again on one account's model,
//	that model rests there (restSlow).**
//
// 拆成四条可断言的判据：
//
//	① **第一次放过** —— 长 prompt 哪儿都慢，第一次不该罚任何人；
//	② **反复发生才罚** —— 阈值 slowRests=2；
//	③ **罚在模型粒度** —— 休息键是「候选自己的模型」（restID = restKey + "/" + model），
//	   **不是账号**；
//	④ **退避递增** —— 第 2 次休息 1min、第 3 次 2min、第 4 次 4min…（magpie 的
//	   `fallbackCooldown << min(run.n-slowRests, 10)`）。
//
// ★ 另外 magpie 有一条 **slowForget=30min** 的遗忘窗：距上次慢启动超过 30min ⇒
//   计数归零（新一轮），否则一个**陈年的**慢启动会把模型一直按在休息里。
//
// # 本仓此前是什么样（为什么必须新增，而不是改现有开关）
//
// 首字节超时此前归 `errorsx.KindStreamTimeout` ⇒ `PenaltyOf` 判 **PenaltyBinding**
// ⇒ **第一次就**把 (凭据,模型) 绑定冷却 5 分钟（domains/credential/writer.go 的
// `coolingDuration`）。这与上面 ① 直接冲突：**第一次就罚**，且冷却时长固定、
// 不随次数递增、不区分"首字节"与"流中"。
//
// ⇒ 本轮把首字节超时**拆成独立 kind** `errorsx.KindFirstByteTimeout`
//   （静态判定 PenaltyNone ⇒ 第一次绝不罚），「反复才罚」由**本文件**承担。
//
// # 为什么静态表表达不了
//
// `PenaltyOf` 是**纯函数**（kind → 归责），而「第二次才罚」依赖**历史计数**
// ⇒ 静态表只能表达「不罚」这一半，另一半必须是有状态的。★ 这不是把逻辑
// 藏起来，而是把**两类归责**分开：静态的（按 kind）与**有状态的**（按历史）。
//
// # 与 magpie 的差异（写在这里，不要只写在注释里）
//
//	① magpie 的 provider 级 `NoFailRest()`（`provider.RestCap == RestCapOff`，
//	   internal/provider/concurrency.go:337）可以**按 provider** 整体关掉慢启动归责。
//	   本仓**没有 provider 级 rest cap 概念** ⇒ 提供**全局开关**
//	   `LLM_GATEWAY_SLOW_REST_DISABLE=1` 代替。⇒ 差异已知，未消除。
//	② magpie 的休息记在**路由层**（routing.slow + restingUntil）；
//	   本仓的休息记在**凭据层**（credential_model_bindings.unavailable_recover_at，
//	   经 writeModelLevelFailureOnly 落库）⇒ 跨实例一致（PG 是唯一事实源），
//   但**计数**是进程内的 ⇒ 多实例下第 2 次的判定可能各实例各算一次。
//   ⇒ 这是一条**已知局限**：多实例部署时退避起点可能偏保守（少罚，不会多罚）。
//
// # 引用
//
//	errorsx/restclasses.go —— slow 类的词表落法与差异说明
//	domains/credential/writer.go —— WriteOnError 的接入点

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// SlowRests 是「连续慢启动多少次才休息」的阈值。
	//
	// ★ magpie: `slowRests = 2`（internal/gateway/routing.go:198-199）
	//   —— 第 1 次放过、第 2 次开始休息。
	SlowRests = 2

	// SlowForget 是慢启动计数的遗忘窗。
	//
	// ★ magpie: `slowForget = 30 * time.Minute`（internal/gateway/routing.go:199）
	SlowForget = 30 * time.Minute

	// SlowFallbackCooldown 是首次休息的基准时长。
	//
	// ★ magpie: `fallbackCooldown = 1 * time.Minute`（internal/gateway/fallback.go:37）
	SlowFallbackCooldown = 1 * time.Minute

	// slowMaxShift 是退避倍增的移位上限。
	//
	// ★ magpie: `fallbackCooldown<<min(run.n-slowRests, 10)`
	//   —— 最多 2^10 倍（≈17 小时），防止计数无限增长把模型永久按死。
	slowMaxShift = 10

	// slowRestEnvName 是全局开关的环境变量名。
	//
	// ★ 本仓没有 magpie 的 provider 级 `RestCap==Off`（见文件头差异①），
	//   用这个开关代替：置 1 ⇒ 慢启动**永不休息**（等价 magpie 的 NoFailRest）。
	slowRestEnvName = "LLM_GATEWAY_SLOW_REST_DISABLE"
)

// SlowRestDecision 是一次慢启动观察后的休息判定。
type SlowRestDecision struct {
	// Rest 表示这次是否应当休息（false = 放过）。
	Rest bool
	// Until 是休息到的时间点（Rest=true 时有效）。
	Until time.Time
	// Count 是当前的连续慢启动计数（含本次）。
	Count int
	// By 是休息原因标签："cooldown"（首次休息）或 "backoff"（已在退避）。
	By string
}

// slowRun 是一个 (凭据, 模型) 键上的慢启动计数。
type slowRun struct {
	// n 是连续慢启动次数。
	n int
	// at 是最近一次慢启动的时刻（用于遗忘窗）。
	at time.Time
	// until 是当前休息到的时间点（零值 = 没在休息）。
	//
	// ★ 休息状态**由本器自己持有**，不去查 DB：magpie 的 `resting` 判据
	//   （internal/gateway/routing.go:1141）问的是「这个键现在是不是正因慢启动
	//   而休息」⇒ 自己记最准，也省掉一次 DB 往返（这条路径在失败写库热路径上）。
	until time.Time
}

// SlowRestTracker 实现 magpie 的 restSlow。
//
// ★ 并发安全；零值不可用，请用 NewSlowRestTracker。
type SlowRestTracker struct {
	mu   sync.Mutex
	runs map[string]slowRun

	// now 可注入时钟（测试用；生产用 time.Now）。
	now func() time.Time

	// disabled 为真时 Observe 永远返回"放过"（全局开关）。
	disabled bool
}

// NewSlowRestTracker 构造一个慢启动休息器。
func NewSlowRestTracker() *SlowRestTracker {
	return &SlowRestTracker{
		runs:     make(map[string]slowRun),
		now:      time.Now,
		disabled: slowRestDisabledByEnv(),
	}
}

// slowRestDisabledByEnv 读全局开关。
//
// ★ 每次调用都读 env（不缓存）⇒ 与本仓其它 kill switch 的写法一致
//
//	（见 domains/hooks/compression/session_compressor.go 的同款做法），
//	便于测试用 t.Setenv 直接切换。
func slowRestDisabledByEnv() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(slowRestEnvName)))
	if v == "" {
		return false
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	// 非布尔值按"非空即开"处理（与本仓其它 DISABLE 开关同款语义）。
	return true
}

// SetClock 注入时钟（仅测试用）。
func (t *SlowRestTracker) SetClock(now func() time.Time) {
	if t == nil || now == nil {
		return
	}
	t.mu.Lock()
	t.now = now
	t.mu.Unlock()
}

// SetDisabled 打开/关闭慢启动归责（仅测试与运维热切换用）。
func (t *SlowRestTracker) SetDisabled(disabled bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.disabled = disabled
	t.mu.Unlock()
}

// Disabled 报告慢启动归责当前是否被关闭。
func (t *SlowRestTracker) Disabled() bool {
	if t == nil {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.disabled
}

// Observe 记录一次慢启动，返回休息判定。
//
// key 必须是**模型粒度**的休息键：本仓用 "<credential_id>/<model>"
// （对齐 magpie 的 restID = restKey + "/" + model，internal/gateway/fallback.go:119-124）。
//
// ★ 「这个键当前是否正因慢启动而休息」由本器自己判定（slowRun.until），
//
//	不问调用方、也不查 DB —— magpie 的 `resting` 判据
//	（internal/gateway/routing.go:1141）问的正是这件事。
//
// ★ magpie 的计数条件是
//
//	`if !resting || prev.Why != failSlow || run.n == 0 { run.n++ }`
//	（internal/gateway/routing.go:1141-1143）：
//	**正在因慢启动而休息时不再加数** ⇒ 一个请求自己的重试不会把计数推高；
//	而"因别的原因而休息"照常加数（`prev.Why != failSlow`）。
//	本仓只知道自己发的休息 ⇒ "别的原因"在本仓等价于"不在慢启动休息中" ⇒ 照常加数，
//	与 magpie 一致。
func (t *SlowRestTracker) Observe(key string) SlowRestDecision {
	if t == nil {
		return SlowRestDecision{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.disabled {
		return SlowRestDecision{}
	}
	now := t.now()
	run := t.runs[key]
	// 遗忘窗：距上次慢启动超过 SlowForget ⇒ 计数归零（新一轮）。
	if now.Sub(run.at) >= SlowForget {
		run.n = 0
		run.until = time.Time{}
	}
	// 正在因慢启动而休息 ⇒ 不加数（magpie: !resting 才加）。
	restingForSlow := !run.until.IsZero() && now.Before(run.until)
	if !restingForSlow || run.n == 0 {
		run.n++
	}
	run.at = now
	t.runs[key] = run
	t.pruneLocked(now)

	if run.n < SlowRests {
		// ★ 第一次（以及任何未达阈值的次数）⇒ **放过**，不罚任何人。
		return SlowRestDecision{Count: run.n}
	}
	shift := run.n - SlowRests
	if shift > slowMaxShift {
		shift = slowMaxShift
	}
	d := SlowFallbackCooldown << shift
	by := "cooldown"
	if run.n > SlowRests {
		by = "backoff"
	}
	until := now.Add(d)
	run.until = until
	t.runs[key] = run
	return SlowRestDecision{Rest: true, Until: until, Count: run.n, By: by}
}

// pruneLocked 清掉过期条目（遗忘窗之外的键）。
//
// ★ magpie 侧没有这一步（routed.slow 只增不减）；本仓加它是因为本仓的
//
//	键空间是 **(凭据, 模型)** 组合，比 magpie 的订阅账号粒度细 ⇒ 长跑进程
//	会积累大量过期键。清理门槛固定（每次 Observe 至多扫 64 个键），
//	避免在高频路径上做全量扫描。
func (t *SlowRestTracker) pruneLocked(now time.Time) {
	if len(t.runs) < 1024 {
		return
	}
	scanned := 0
	for k, r := range t.runs {
		if now.Sub(r.at) >= SlowForget {
			delete(t.runs, k)
		}
		scanned++
		if scanned >= 64 {
			return
		}
	}
}

// Count 返回某个键当前的慢启动计数（测试与观测用）。
func (t *SlowRestTracker) Count(key string) int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.runs[key].n
}

// defaultSlowRest 是进程级的默认休息器。
var defaultSlowRest = NewSlowRestTracker()

// DefaultSlowRest 返回进程级的默认慢启动休息器。
func DefaultSlowRest() *SlowRestTracker { return defaultSlowRest }

// SlowRestKey 构造模型粒度的休息键 "<credential_id>/<model>"。
//
// ★ 与 magpie 的 restID（restKey + "/" + model）同构：休息**永远**落在模型上，
//
//	**不落在账号上** ⇒ 同一账号的其它模型不受影响。
func SlowRestKey(credentialID int, model string) string {
	return strconv.Itoa(credentialID) + "/" + strings.TrimSpace(model)
}
