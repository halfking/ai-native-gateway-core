// bg/probe_round_log_dedup.go —— 探测轮「协议还是节点」判决日志的**同因冷却去重**（R-76-3，2026-11-14）
//
// # 为什么要有这个文件
//
// ★ 现象（211 真机 1h 窗实测，a 槽 2.5.9-bf7f18f5）：`node probe failed` WARN
// ★ 389 条/h（429 族 208 + 503 族 108 为大头）。探测梯本就有节奏
// ★ （5s→30s→2m→5m→15m，持续失败 1h 复检），梯子上**每一次**失败轮都打
// ★ 一条同文案 WARN：对同一 (provider, model, rootCause) 组合，判决在配置
// ★ 或上游状态被改变前不会变，逐轮重复没有信息增量。
//
// ★ 去重的**口径边界**：
//   · 计数不受影响：emitProbe 的每轮计数在调用方独立发生
//     （logProbeRoundRootCause 只管日志，见其头注「Counting does NOT happen
//     here」）⇒ 压日志不丢任何指标。
//   · 只压**重复判决**：首次失败（或根因/状态码族变化）立刻放行——运维
//     仍能在第一现场看到「协议还是节点」。
//   · 键 = providerID + outboundModel + rootCause：同供应商同模型同根因
//     才算重复；换凭据/换模型/换根因（如 node→protocol）都会立刻放行。
//
// ★ 与 provider/candidate_diag_dedup.go 同构但独立实现：两处键语义、冷却
// ★ 时长、并发形态都不同，抽公共件只会把两个口径焊死在一起。
package bg

import (
	"strconv"
	"sync"
	"time"
)

// probeRoundLogCooldown 是同一 (provider, model, rootCause) 判决日志的冷却窗。
// 30 分钟：探测梯的稳态复检间隔（持续失败 1h、普通梯 ≤15m）都落在窗内
// ⇒ 梯子上的重复轮被合并；窗口过半放行节奏仍保证每个小时每组合至多
// 2 条——比逐轮洪水低两个数量级，又不会让持续失败整小时静默。
const probeRoundLogCooldown = 30 * time.Minute

// probeRoundLogEntry 是单个键的冷却状态。
type probeRoundLogEntry struct {
	lastLog    time.Time
	suppressed int
}

// probeRoundLogGate 是探测轮判决日志的去重闸。
//
// ★ 并发安全：探测队列 worker 多 goroutine 共用一个实例。
type probeRoundLogGate struct {
	mu       sync.Mutex
	entries  map[string]*probeRoundLogEntry
	cooldown time.Duration
}

func newProbeRoundLogGate(cooldown time.Duration) *probeRoundLogGate {
	return &probeRoundLogGate{
		entries:  make(map[string]*probeRoundLogEntry),
		cooldown: cooldown,
	}
}

// allow 报告「这条判决日志现在是否放行」，语义与 provider 侧同构：
// allowed=true 时返回的 suppressed 是冷却窗内被压掉的条数（调用方附进
// args 后由本方法清零）；allowed=false 表示窗口内重复，调用方跳过 slog。
func (g *probeRoundLogGate) allow(providerID int, outboundModel string, cause ProbeRootCause, now time.Time) (bool, int) {
	if g == nil {
		return true, 0
	}
	key := string(cause) + "|" + outboundModel + "|" + strconv.Itoa(providerID)
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.entries[key]
	if !ok {
		g.entries[key] = &probeRoundLogEntry{lastLog: now}
		return true, 0
	}
	if now.Sub(e.lastLog) < g.cooldown {
		e.suppressed++
		return false, 0
	}
	suppressed := e.suppressed
	e.lastLog = now
	e.suppressed = 0
	return true, suppressed
}

// probeRoundLogGateInstance 是探测路径共用的闸（进程级单例）。
var probeRoundLogGateInstance = newProbeRoundLogGate(probeRoundLogCooldown)
