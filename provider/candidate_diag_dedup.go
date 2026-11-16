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
//
//	· 指标照旧每事件 +1：`llmgw_routing_candidate_diagnostics_total` 的口径
//	  是「事件发生数」，与日志合并无关 ⇒ 去重只省日志，不丢任何计数。
//	· 只对 `db_empty` 生效：其余 candidate_diag 事件（db_unavailable /
//	  enrich_empty / db_query_retry …）要么罕见、要么是状态变化信号，
//	  逐条保留。
//	· 冷却窗内**不丢首条**：每个 (model, tenant) 组合的首条 WARN 原样打
//	  （含完整归因 args），重复条合并为「下一条放行时附 suppressed=N」——
//	  运维仍能在洪水里看到第一次告警与被压掉的量级。
//
// ★ 键的选择：`event + model + tenant_id + cause_class`。同一模型不同租户的
// ★ 归因结论可能不同（候选 SQL 限定 p.tenant_id）⇒ 按租户分键，不合并。
//
// ★★ 2026-11-17 批判审计补：`cause_class` 进键是**必需**的，不是可选优化。
//
//	原实现键里没有归因 ⇒ 同一 (model, tenant) 在冷却窗内归因从 A 变成 B 时，
//	B 被静默吞掉最多 5 分钟。而本文件原注释写着「模型登记/状态闸被改时会
//	**立即以新内容出现**」——那句话在原实现下是**假的**（状态闸被改 ⇒ 归因变了
//	⇒ 但键没变 ⇒ 要等冷却窗到期才出现）。
//	危害不是「日志少了」，而是**处置方向被误导**：旧归因「模型未登记」、
//	新归因「凭据配额耗尽 ⇒ 充值」，后者是可立即执行的运维动作，被压掉意味着
//	运营在 5 分钟里仍按旧结论排查。
//	⇒ 与 bg 侧对齐：`bg/probe_round_log_dedup.go` 的键含 `rootCause`，并有
//	`TestLogProbeRoundRootCauseRootCauseChangeNotGated` 钉住「根因变化立刻放行」。
//	⇒ 键里放的是**有界的归因类**（cause_class，有限枚举）而不是 `likely_cause`
//	原文：Summarize() 把 offer 计数也拼进句子（3↔4 抖动会让键交替 ⇒ 洪水压不住）。
package provider

import (
	"sync"
	"time"
)

// candidateDiagLogCooldown 是同一 (model, tenant, cause_class) 的 db_empty 日志冷却窗。
// 5 分钟：Owner 处置周期以分钟计，窗口内重复打屏无信息增量；窗口外放行
// 一条以确认归因未漂移。
//
// ★ 「未漂移」的检查现在**由 cause_class 进键保证**：归因变了就是新键 ⇒
// 立刻放行，不必等窗口到期。窗口外放行只是再确认一次「同一归因仍在持续」。
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
//
//	是线性小对象（时间戳+计数），不设 LRU（无证据支撑额外复杂度）。
type candidateDiagLogGate struct {
	mu       sync.Mutex
	entries  map[string]*candidateDiagLogEntry
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
func (g *candidateDiagLogGate) allow(event, model, tenant, cause string, now time.Time) (bool, int) {
	if g == nil {
		return true, 0
	}
	// ★ cause 进键：归因变了就是新键 ⇒ 立刻放行（见文件头「2026-11-17 批判审计补」）。
	// cause 为空串时键退化成 `event|model|tenant|`，仍是合法键；此时所有
	// 未带 cause_class 的调用方共享一个桶——与旧行为相比不更差。
	key := event + "|" + model + "|" + tenant + "|" + cause
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
