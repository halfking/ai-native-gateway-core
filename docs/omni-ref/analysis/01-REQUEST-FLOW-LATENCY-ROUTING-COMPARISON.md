# 请求流 / 延时 / 路由选择：OmniRoute vs llm-gateway-go 深度对比与优化方案

> **目的**：学习 OmniRoute 在「请求流处理、延时管理、路由选择」上的设计，与 llm-gateway-go 逐层对比，产出可落地、可验证的优化与完善项。
> **关键结论（先读）**：llm-gateway-go 在**分布式状态、failover 深度、延时百分位追踪**上整体**更成熟**；OmniRoute 的价值集中在**若干 llm-gateway-go 缺失或更简单的具体机制**（见 §6 优化矩阵）。因此本方案不是"把 OmniRoute 搬过来"，而是"挑出 OmniRoute 做得更好/更全的几处，用 Go 原生方式补进现有架构"。
> **数据来源**：两套代码均经读源码 + 行号核对（OmniRoute v3.8.49；llm-gateway-go main）。所有 `file:line` 可复跑验证。

---

## 0. 两套系统一句话定位

| 维度 | OmniRoute (TS) | llm-gateway-go |
|---|---|---|
| 形态 | 单体 Next.js，**进程内内存状态**（module-level `Map`） | 分布式网关，**Redis + PG 外部状态** |
| 路由核心 | combo 引擎（19 策略 + auto 多因子） | Router（tier/billing/sticky + P2C/Bandit + URSMv2） |
| 延时追踪 | **仅 avgLatencyMs**（滚动平均） | **P50/P95 静态 + EMA TTFB + Bandit 延时累积 + URSM LatEWMA + 滑窗 HealthTracker** |
| failover | 候选 fallback + set-retry + cooldown-wait | 候选循环 + 单节点探活 + 同步重试(3轮) + 跨模型 fallback + 异步重试 |
| 并发控制 | Bottleneck 信号量 + 滑窗 | **5 层加权信号量**（global/pool/cred/RPM/identity/key） |

---

## 1. 请求流（Pipeline）对比

### 1.1 流程对照

| 阶段 | OmniRoute (`chatCore.ts`) | llm-gateway-go (`executor.go:1666`) |
|---|---|---|
| 入口前置 | 堆压保护、system prompt 注入、plugin onRequest | 租户上下文、body 不可变快照 |
| 身份/限流 | account semaphore + `withRateLimit` | Layer0 identity pool + sticky pick |
| 候选规划 | `resolveComboTargetPipeline`（19 策略排序） | `PlanCandidatesWithContext`（tier/billing/sticky） |
| 单候选执行 | `handleSingleModel`（每目标一次 chatCore） | `executeOpenAI`/`executeAnthropic` |
| 重试/failover | 候选循环 + set-retry + cooldown-wait | 候选循环 → 单节点探活 → 同步重试 → 跨模型 → 异步 |

### 1.2 关键差异

**llm-gateway-go 的独有优势**：
- **异步重试**（`executor.go:3213`）：同步走超 `AsyncShortTimeout`(15s) 后转后台 goroutine（`AsyncLongTimeout` 300s），返回 202 + `X-Gw-Pending`，客户端轮询。OmniRoute 无此能力——长任务只能占住连接。
- **同步无候选探活**（`executor.go:1810`）：候选为空时同步 fan-out 探活（默认 5s），恢复则重规划并递归执行。OmniRoute 的 `preScreenTargets` 仅 priority 策略且是并行可用性预检，不是"空候选自救"。
- **流前 keepalive**（`handler.go:2540`）：上游首字节未到时先向客户端写 SSE comment 保活。

**OmniRoute 的独有设计**（→ 见 §6 优化项）：
- **per-target timeout 包裹层**（`combo/targetTimeoutRunner.ts`，默认 120s）：每个候选有独立墙钟预算 + hedging 取消信号。
- **全局 combo 墙钟**（`combo.ts:795` `comboTimeoutMs`）：整个多候选尝试有总预算上限。
- **stageTrace**（`chatCore/stageTrace.ts`）：每阶段耗时打点，用于 hung-request 诊断。

---

## 2. 延时（Latency）管理对比 ⭐ 重点

### 2.1 延时追踪能力对照

| 能力 | OmniRoute | llm-gateway-go | 判断 |
|---|---|---|---|
| 滚动平均延时 | ✅ `comboMetrics.avgLatencyMs` | ✅ Bandit `TotalLatencyMs` | 平 |
| **P50/P95 百分位** | ❌ **仅平均** | ✅ DB `p95_latency_ms` + EMA TTFB | **Go 胜** |
| 首字节延时(TTFB) | ❌ 隐含在 avg | ✅ `TTFBTracker` EMA(0.8/0.2) 5min 过期 | **Go 胜** |
| 并发放大修正 | ❌ | ✅ `calculateLatencyScore` queue-amplification（knee/alpha/beta） | **Go 胜** |
| **预测性 TTFT 预跳过** | ✅ `combo.ts:1038` predictive-TTFT breaker | ❌ | **OmniRoute 胜** |
| 超时分层 | 5 层（fetch/idle/readiness/target/combo） | 多层（upstream/stream/firstByte/syncRetry/async） | 平 |

### 2.2 评分公式对照

**OmniRoute P2C 评分**（`targetSorters.ts:124`）——较简单：
```
score = successRate/100 + 1/log10(avgLatency+10) − breakerPenalty(HALF_OPEN→0.25)
```

**llm-gateway-go P2C 评分**（`router_scoring.go:43`）——更复杂、并发感知：
```
composite = concurrencyScore*0.4 + identityScore*0.1
          + (1−latencyScore)*0.3 + qualityScore*0.2 + (1−headroom)*0.05
其中 latencyScore 经 queue-amplification 放大后按 P95 分段 lerp
```

**判断**：llm-gateway-go 的评分在并发/延时/质量维度更全面。但 OmniRoute 的 **predictive-TTFT 预跳过**（见下）是 Go 侧缺失的具体能力。

### 2.3 🎯 优化项 O-1：引入预测性 TTFT 预跳过

**OmniRoute 做法**（`combo.ts:1038-1057`）：首次尝试（retry==0）前，若某候选的 `avgLatencyMs > config.predictiveTtftMs`，直接跳过该候选（返回 null），不浪费一次失败的上游调用。

**llm-gateway-go 现状**：`calculateLatencyScore` 已用 P95 给低分候选**降权**，但仍会**实际调用**它（P2C 只是排序，不是过滤）。慢候选照样被试一次。

**移植价值**：高。慢上游的首次失败往往就是首字节超时（30-120s），预跳过可直接省掉这个浪费。

**Go 落地方式**（接入 `PlanCandidatesWithContext` 后、failover 循环前）：
```go
// domains/streaming/executors/router_predictive.go（新建）
// 复用已有 TTFBTracker（EMA）而非 avg，更准
func (r *Router) predictiveSkip(c provider.Candidate, maxTtftMs int) bool {
    if maxTtftMs <= 0 { return false }
    ema := r.TTFBTracker.Recent(c.CredentialID) // 已有，5min EMA
    if ema <= 0 { return false }                 // 无样本不跳
    return int(ema.Milliseconds()) > maxTtftMs
}
```
- 配置：`LLM_GATEWAY_PREDICTIVE_TTFT_MS`（默认 0=关；建议灰度 60000）。
- 仅首次尝试跳过；retry 时不跳（可能已恢复）。
- 安全：跳过后候选为空则**降级不跳**（不能因预跳把请求饿死），复用现有降级路径。
- **注意**：OmniRoute 用 avg，Go 应用 EMA TTFB（`TTFBTracker` 已存在，比 avg 更反映当前状态）。

---

## 3. 路由选择（Routing）对比

### 3.1 策略覆盖对照

| 策略类别 | OmniRoute | llm-gateway-go | 判断 |
|---|---|---|---|
| 负载均衡（priority/weighted/rr/p2c/random/least-used） | ✅ 6 种 | ✅ P2C + Bandit + rr + tier | Go 更深 |
| **cost-optimized** | ✅ 按输入价排序 | ❌ | **OmniRoute 胜**（已在 omni-ref R1 规划） |
| **headroom** | ✅ `1−max(util5h,util7d)` | ✅ `calculateHeadroom`（pressure-based） | 平（Go 用并发压力，OR 用配额利用率） |
| **context-optimized** | ✅ 最大上下文优先 | ❌ | **OmniRoute 胜**（R1 规划） |
| **cache-optimized** | ✅ prompt-cache 亲和 | ❌ | **OmniRoute 胜**（R1 规划） |
| **reset-aware / reset-window** | ✅ 配额重置紧迫度评分 | ❌ | **OmniRoute 独有** |
| lkgp（last-known-good） | ✅ 持久化到 DB | ❌ | **OmniRoute 独有** |
| auto（多因子+意图） | ✅ 6-13 因子 + 意图分类 | ❌（有 autoroute 包但不同模型） | 各有 |
| quota-share（DRR+P2C） | ✅ 内部 | ❌ | OmniRoute 独有 |
| **Thompson Sampling Bandit** | ❌ | ✅ `BanditScorer`（reliability/speed/intelligence） | **Go 独有** |
| URSMv2（authoritative/canary） | ❌ | ✅ Redis + 50ms bound | **Go 独有** |

### 3.2 🎯 优化项 O-2：reset-aware / reset-window 配额策略

OmniRoute 独有，对**按月/按会话重置的免费/OAuth 配额**极有价值（重置前把快恢复的账号排后、把快耗尽的优先用）。

**OmniRoute 评分**（`quotaScoring.ts`）：
```
resetUrgency = clamp01(1 − msUntilReset/windowMs)   // 越接近重置越紧迫
sessionScore = 0.45*(1−used5h) + 0.55*(resetUrgency*(1−(1−used5h)))
weeklyScore  = 0.25*(1−used7d) + 0.75*(resetUrgency*(1−(1−used7d)))
score = 0.35*sessionScore + 0.65*weeklyScore
```

**Go 落地**：作为 R1（advanced-routing）的新增 `Strategy`，复用 `Candidate.BalanceUSD`/配额字段；评分函数为纯函数翻译。需 Gateway 侧有配额重置时间数据（`credentials` 表补 `quota_resets_at` 或从上游 `Retry-After`/`x-ratelimit-reset` 学习）。

### 3.3 🎯 优化项 O-3：LKGP（last-known-good provider）亲和

OmniRoute 在每次成功后把 `(provider, connectionId)` 持久化，下次请求把该候选提到最前。这比 sticky（按 session/client 锁定同一 credential）更轻——sticky 是"锁定"，LKGP 是"偏好"。

**Go 落地**：可作为 sticky 的一个**降级层**——无 session/client 键时，用 LKGP 做软偏好（`prioritizeSticky` 之后加 `prioritizeLKGP`）。存 Redis（已有基础设施）。与 sticky 不冲突：sticky 命中时优先，未命中时 LKGP 兜底。

---

## 4. 熔断器（Circuit Breaker）对比 ⭐ OmniRoute 明显更丰富

### 4.1 状态机对照

| 特性 | OmniRoute (`circuitBreaker.ts`) | llm-gateway-go (`breaker.go`) |
|---|---|---|
| 状态 | **CLOSED→DEGRADED→OPEN→HALF_OPEN**（4 态） | Closed→Open→HalfOpen→Quarantined |
| DEGRADED 中间态 | ✅ 达阈值 60% 先降级（非直接 OPEN） | ❌ 无中间态 |
| 自适应退避 | ✅ `2^(cycle−esc)` 封顶 `maxBackoffMultiplier`(16x) | ✅ 指数（按 kind） |
| **按失败类型阈值** | ✅ `kindThresholds` + `immediateOpen` | ✅ `RecordFailure(kind)` 按 kind 冷却 |
| 失败类型族 | ✅ classify429 | ✅ "transient family" 共享计数 |
| **持久化** | ✅ DB（`domainState.js`） | ✅ Redis |
| cooldown 学习 | ✅ 上游 `Retry-After`/header | ✅ rate-limit header |

**判断**：能力相近，但 OmniRoute 的 **DEGRADED 中间态**值得借鉴——在完全熔断前先"降级"（减少流量而非归零），对 OAuth/免费账号的抖动更友好。

### 4.2 🎯 优化项 O-4：DEGRADED 中间态

**Go 落地**：在 `breaker.go` 的 `Closed→Open` 之间插入 `Degraded`：失败达 `degradationThreshold`（如阈值 60%）时进入 Degraded，`Allow()` 返回 true 但**降低该 credential 的 Weight**（复用 `applyPressurePenalty` 的 weight 机制），让 P2C/Bandit 自然减少选它；继续失败才 OPEN。好处：避免硬熔断的"全有或全无"抖动。

---

## 5. 限流（Rate Limiting）对比 ⭐ OmniRoute 有 Go 缺失的能力

### 5.1 能力对照

| 能力 | OmniRoute | llm-gateway-go |
|---|---|---|
| 并发信号量 | Bottleneck（固定窗 reservoir） | 5 层加权信号量 ✅更强 |
| **真滑窗** | ✅ `SlidingWindowLimiter`（任意 trailing window） | ❌（信号量是并发，非时间窗 RPM） |
| **header 自学习** | ✅ `updateFromHeaders` 学 `X-RateLimit-*`/`Retry-After` | 部分（breaker 学 Retry-After） |
| 队列准入上限 | ✅ `maxQueueDepth` | ✅ acquireWaitTimeout 5s |
| 冷却后 +50ms drain | ✅ 时钟漂移保护 | ❌ |

### 5.2 🎯 优化项 O-5：真滑窗 RPM 限流 + header 自学习

llm-gateway-go 的 `RPMLimit`（`Candidate.RPMLimit`）目前是**静态配置**，且 Limiter 层是并发信号量，不是时间窗 RPM。OmniRoute 的 `SlidingWindowLimiter` 是依赖无关的"任意 trailing window N 次"精确限流，且能从上游响应头**动态学习**真实限额。

**Go 落地**：
- 新增 `domains/credential/sliding_window.go`：Redis sorted-set 实现真滑窗（`ZADD now` + `ZREMRANGEBYSCORE now-window now` + `ZCOUNT`），用于有明确 RPM/RPD 上限的 credential。
- `header` 自学习：在 `executor_chat.go` 读上游响应头，更新 Redis 里的限额 reservoir（`updateFromHeaders` 翻译）。
- 价值：对"无文档限额但上游会 429"的提供商，能自适应而非靠人工配。

---

## 6. 优化矩阵（汇总 + 优先级）

按"价值 × 落地成本 × 与现有架构契合度"排序：

| ID | 优化项 | 来源 | 价值 | 成本 | 契合 | 优先级 |
|---|---|---|---|---|---|---|
| **O-1** | 预测性 TTFT 预跳过 | OmniRoute `combo.ts:1038` | 高（省慢上游首次失败 30-120s） | 低（复用 TTFBTracker） | 高（插 PlanCandidates 后） | **P0** |
| **O-5** | 真滑窗 RPM + header 自学习 | OmniRoute `slidingWindowLimiter` | 高（自适应限额） | 中（Redis sorted-set） | 高（补 Limiter 层） | **P0** |
| **O-4** | 熔断 DEGRADED 中间态 | OmniRoute `circuitBreaker.ts` | 中（减少抖动） | 中（改 breaker 状态机） | 高（复用 weight） | **P1** |
| **O-2** | reset-aware/reset-window 策略 | OmniRoute `quotaScoring.ts` | 中（免费配额场景） | 中（需重置时间数据） | 高（R1 Strategy 插件） | **P1** |
| **O-3** | LKGP 软偏好 | OmniRoute `lkgp` | 中（sticky 兜底） | 低（Redis + prioritizeLKGP） | 高（sticky 降级层） | **P1** |
| **O-6** | cooldown-aware retry-wait | OmniRoute `comboCooldownRetry.ts` | 中（短冷却等一下而非直接失败） | 中（failover 循环改） | 中（改执行流） | **P2** |
| **O-7** | stageTrace 阶段耗时诊断 | OmniRoute `stageTrace` | 中（可观测） | 低（打点） | 高（纯观测） | **P2** |
| **O-8** | per-target 墙钟预算 | OmniRoute `targetTimeoutRunner` | 中（单候选不拖垮全局） | 中（failover 循环） | 中 | **P2** |
| O-9 | task-aware 意图路由 | OmniRoute `taskAwareRouter`+`intentClassifier` | 中（质量） | 高（需意图模型） | 中 | P3 |
| O-10 | stream recovery 透明重开 | OmniRoute `streamRecovery.ts` | 中（流中断恢复） | 高（holdback buffer） | 中 | P3 |
| O-11 | cost/context/cache-optimized 策略 | OmniRoute | 高（已在 R1 规划） | 中 | 高 | **见 omni-ref R1** |

> O-11 已在 `docs/omniroute-ref/phase1/02-advanced-routing.md` 规划，不重复；本表聚焦**新增**优化项 O-1~O-10。

---

## 7. llm-gateway-go 已有优势（保持，勿退步）

明确这些**不需要从 OmniRoute 学**，避免反向移植：

1. **分布式状态**：Redis + PG，多实例一致；OmniRoute 是单进程内存 `Map`，无法横向扩展。
2. **P50/P95 + EMA 多源延时**：比 OmniRoute 的 avg 更准。
3. **5 层加权信号量**：比 OmniRoute 的单 Bottleneck 更精细。
4. **异步重试（202 + 轮询）**：OmniRoute 无。
5. **空候选同步探活自救**：OmniRoute 无。
6. **Bandit（Thompson Sampling）+ URSMv2**：OmniRoute 无。
7. **5 阶段 failover**（候选→探活→同步重试→跨模型→异步）：比 OmniRoute 的 3 层更深。
8. **并发感知的 latency 评分**（queue-amplification）：OmniRoute 无。

---

## 8. P0 详细落地（O-1 + O-5）

### O-1：预测性 TTFT 预跳过（工期 ~3 天）

**文件**：
- 新建 `domains/streaming/executors/router_predictive.go`
- 改 `executor.go` failover 循环开头（`executor.go:2121` 附近）

**接口**：
```go
type PredictiveSkipper struct {
    TTFB    *ttfb.Tracker
    MaxMs   int  // env LLM_GATEWAY_PREDICTIVE_TTFT_MS, 0=off
}

// 返回 true 表示应跳过该候选（首次尝试且 EMA TTFB 超阈值）
func (p *PredictiveSkipper) ShouldSkip(c provider.Candidate, attempt int) bool {
    if p.MaxMs <= 0 || attempt > 0 { return false }
    ema := p.TTFB.Recent(c.CredentialID)
    if !ema.HasSample() { return false }
    return ema.Milliseconds() > int64(p.MaxMs)
}
```

**接入**（failover 循环内，circuit 检查后）：
```go
if predictive.ShouldSkip(cand, attemptRound) && len(candidates) > 1 {
    continue  // 跳过，但有其他候选时才跳
}
```

**测试**：EMA 超阈值跳过；首次跳、retry 不跳；候选仅剩 1 个时不跳（降级）；TTFB 无样本不跳。

**验收**：灰度租户的慢上游首次失败率下降；无候选饿死。

### O-5：真滑窗 RPM + header 自学习（工期 ~1.5 周）

**文件**：
- 新建 `domains/credential/sliding_window.go`（Redis ZSET）
- 新建 `domains/credential/limit_learner.go`（header 学习）
- 改 `executor_chat.go` 响应处理（读 header 更新 reservoir）

**滑窗核心**（Redis Lua 原子）：
```lua
-- KEYS[1]=windowkey  ARGV[1]=now ARGV[2]=windowMs ARGV[3]=limit ARGV[4]=member
ZREMRANGEBYSCORE KEYS[1] 0 ARGV[1]-ARGV[2]
local n = ZCARD KEYS[1]
if n >= tonumber(ARGV[3]) then return 0 end
ZADD KEYS[1] ARGV[1] ARGV[4]
EXPIRE KEYS[1] ARGV[2]/1000+1
return 1
```

**header 学习**：解析 `X-RateLimit-Remaining`/`X-RateLimit-Reset`/`Retry-After`，更新 Redis 限额（参考 OmniRoute `updateFromHeaders`）。**安全**：只在响应成功时学，避免被恶意 429 污染。

**验收**：对无文档 RPM 的提供商，429 率下降；滑窗精确性测试（边界 burst 不超限）。

---

## 9. 风险与原则

| 风险 | 缓解 |
|---|---|
| 预跳过把请求饿死 | 候选≤1 时不跳；TTFB 无样本不跳；可配阈值 |
| 滑窗 Redis 延迟 | Lua 原子 + pipeline；热路径异步更新 reservoir |
| DEGRADED 与 URSMv2 冲突 | Degraded 仅调 weight，URSMv2 authoritative 仍为唯一健康权威 |
| 移植引入单进程状态 | 所有新状态走 Redis/PG，**禁止** OmniRoute 式内存 `Map` |
| OmniRoute 9 层韧性相互重叠 | Go 侧每补一层先审计与现有 breaker/limiter/URSM 的去重，不堆叠冗余层 |

**总原则**：OmniRoute 的**算法/规则/状态机**可翻译；其**进程内单例状态**必须改 Redis；其**闭包式 god-file**结构不照搬，用 Go 接口 + 组合。

---

## 10. 文档导航

- 本文档：`docs/omni-ref/analysis/01-REQUEST-FLOW-LATENCY-ROUTING-COMPARISON.md`
- 配套翻译方法论：`docs/omni-ref/01-TS-TO-GO-FUSION-GUIDE.md`
- 已有路由策略方案（含 cost/context/cache-optimized）：`docs/omniroute-ref/phase1/02-advanced-routing.md`
- 已有审计：`docs/omni-ref/00-AUDIT-EXISTING-DOCS.md`
