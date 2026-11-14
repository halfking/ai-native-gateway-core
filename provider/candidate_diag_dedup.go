// provider/candidate_diag_dedup.go —— db_empty 归因日志的**同键冷却去重**（R-76-3，2026-11-14）
//
// # 为什么要有这个文件
//
// ★ 现象（211 真机 1h 窗实测，a 槽 2.5.9-bf7f18f5）：`[candidate_diag] db_empty`
// ★ WARN 427 条/h——同一批不可用模型（gemini-2.5-flash 75 / minimax-text-01 74 /
// ★ sensenova 52 / kimi-k3 32 …）**每个请求都打一条**，内容逐字节相同：
// ★ 归因结论在 Owner 处置前不会变，逐请求重复只是把可执行信号淹没成洪水。
//
// ★ 去重的**口径边界**（为什么这里只动日志、不动其它）：
//   · 指标照旧每事件 +1：`llmgw_routing_candidate_diagnostics_total` 的口径
//     是「事件发生数」，与日志合并无关 ⇒ 去重只省日志，不丢任何计数。
//   · 只对 `db_empty` 生效：其余 candidate_diag 事件（db_unavailable /
//     enrich_empty / db_query_retry …）要么罕见、要么是状态变化信号，
//     逐条保留。
//   · 冷却窗内**不丢首条**：每个 (model, tenant) 组合的首条 WARN 原样打
//     （含完整归因 args），重复条合并为「下一条放行时附 suppressed=N」——
//     运维仍能在洪水里看到第一次告警与被压掉的量级。
//
// ★ 键的选择：`event + model + tenant_id`。同一模型不同租户的归因结论
// ★ 可能不同（候选 SQL 限定 p.tenant_id）⇒ 按租户分键，不合并。
package provider

import (
	"sync"
	"time"
)

// candidateDiagLogCooldown 是同一 (model, tenant) 的 db_empty 日志冷却窗。
// 5 分钟：Owner 处置周期以分钟计，窗口内重复打屏无信息增量；窗口外放行
// 一条以确认归因未漂移（模型登记/状态闸被改时会立即以新内容出现）。
const candidateDiagLogCooldown = 5 * time.Minute

// candidateDiagLogEntry 是单个键的冷却状态。
type candidateDiagLogEntry struct {
	lastLog    time.Time
	suppressed int
}

// candidateDiagLogGate 是 db_empty 日志的去重闸。
//
// ★ 并发安全：请求路径多 goroutine 共用一个实例。
// ★ 有界：键空间 = 不可用模型 × 租户，运行期自然收敛；即便失控上限也
//   是线性小对象（时间戳+计数），不设 LRU（无证据支撑额外复杂度）。
type candidateDiagLogGate struct {
	mu      sync.Mutex
	entries map[string]*candidateDiagLogEntry
	cooldown time.Duration
}

func newCandidateDiagLogGate(cooldown time.Duration) *candidateDiagLogGate {
	return &candidateDiagLogGate{
		entries:  make(map[string]*candidateDiagLogEntry),
		cooldown: cooldown,
	}
}

// allow 报告「这条日志现在是否放行」。
//
// 返回 (allowed, suppressed)：
//   - allowed=true  ⇒ 放行（首条或冷却窗已过）；suppressed=冷却窗内被压掉的条数，
//     调用方应把它附进日志 args，然后本方法已将该计数清零。
//   - allowed=false ⇒ 冷却窗内重复，调用方跳过 slog（指标由调用方先行记录）。
func (g *candidateDiagLogGate) allow(event, model, tenant string, now time.Time) (bool, int) {
	if g == nil {
		return true, 0
	}
	key := event + "|" + model + "|" + tenant
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.entries[key]
	if !ok {
		g.entries[key] = &candidateDiagLogEntry{lastLog: now}
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

// candidateDiagLogGateInstance 是请求路径共用的闸（进程级单例）。
var candidateDiagLogGateInstance = newCandidateDiagLogGate(candidateDiagLogCooldown)
