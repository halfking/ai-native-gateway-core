# RFC 审计报告：auto 多维评分方案可复用代码分析

> 审计日期：2026-09 (本轮)
> 审计对象：`FEATURE-REQ-auto-multidim-progress-and-pricing-presets.md` 初版方案
> 审计目标：识别可复用代码，避免重复造轮子，降低实施成本与维护负担

---

## 0. 审计结论（TL;DR）

**初版方案中 70% 的"新建"组件已经存在或可通过轻量重构复用**。

| 初版设计 | 审计结果 | 复用/重构方案 |
|---|---|---|
| **SessionProgressScorer** 新建 | ❌ 不必新建 | 复用 `goal/loop_detector.go` 进展检测 + 扩展 `autoroute/outcome_feedback.go` 机制 |
| **Redis 桶存储评分** | ⚠️ 部分可复用 | 复用 `autoroute/session_intent_cache.go` 模式（已有 Redis Get/Set/Delete + TTL），只需加 4 维字段 |
| **成本预设模板** | ✅ 已存在！ | `goal/cost_presets.go` 已有 3 档预设（minimal/balanced/aggressive），直接移植到 pricing 层 |
| **换模型回路** | ✅ 已存在！ | `goal/loop_detector.go::detectLoop` + `autoroute/model_alternatives.go::RecommendModelAlternatives` 已是完整闭环 |
| **评分→动作映射** | ⚠️ 可重构 | 把 goal 的 `loopDecision{canContinue, switchModel, giveUp}` 抽象为通用决策器 |
| **decision_trace 字段扩展** | ✅ 已有字段 | `autoRouteDecision` 已有 `CandidatesTop3` + `EnabledFeatures`，加 4 个评分字段即可 |
| **pricing_source='preset'** | ✅ 已有机制 | `admin/pricing.go` 已有 `manual/imported/copied/inherited/auto` 五值，加 `preset` 第六值 |

**关键发现**：
1. **goal mode 是完整参考实现**：`loop_detector.go` + `cost_presets.go` + `store.go` 的 `ModelSwitchCount / RepeatCount / AtomicModelSwitch` 已经是"停滞检测 → 换模型 → 计数器 → 决策"的完整闭环，可直接移植到 auto。
2. **autoroute 已有完整的 outcome 回路**：`outcome_feedback.go` + `ReportRoutingOutcome` 是 2026-09-08 audit O2 刚落地的"决策时 stash → 完成时匹配 → 写 feedback"机制，**不需要新建**，只需扩展字段。
3. **成本预设不用新表**：goal 的 `ModePresets` map 可直接转为 JSON 端点，不需要 migration 715。

---

## 1. 初版方案中的"重复造轮子"识别

### 1.1 SessionProgressScorer（❌ 不必新建）

**初版设计**：
```go
// 新建 autoroute/session_progress.go
type SessionProgress struct {
    ProgressScore   float64
    QualityScore    float64
    CostEfficiency  float64
    TaskFit         float64
    Composite       float64
}
func UpdateAfterRequest(sessionID, outcome) { ... }
func LoadForAuto(sessionID) SessionProgress { ... }
```

**审计发现**：
- `goal/loop_detector.go::detectLoop` **已实现** 进展检测：
  - `budgetExhausted := sess.AutoContinueCount >= maxContinue`（进展度）
  - `repeating := sess.RepeatCount >= repeatThreshold`（重复度/质量度）
  - `hashResponse(body)` 检测输出相似度
- `autoroute/outcome_feedback.go::RoutingOutcome` **已实现** 结果回流：
  - `Success / LatencyMs / CostUSD` 三维指标
  - `ReportRoutingOutcome` 已在 `domains/streaming/handler.go:6174` 和 `request_log_pipeline.go:1123` 两个 choke point 调用

**重构方案**：
1. **不新建** `SessionProgressScorer`，而是：
   - 扩展 `RoutingOutcome` 加 `SessionID` 字段（已有 `RequestID`）
   - 在 `ReportRoutingOutcome` 内增量更新 Redis 的 4 维评分（fire-and-forget，与现有 feedback 写并行）
2. 复用 `goal/loop_detector.go` 的 `hashResponse` + `RecordResponse` 逻辑（已有 DB 持久化）
3. 4 维评分公式封装为 `autoroute/session_health.go`（纯计算，无 I/O），由 `ReportRoutingOutcome` 调用

**节省工作量**：~300 行新代码 → ~80 行扩展代码（减少 73%）

---

### 1.2 Redis 存储评分（⚠️ 部分可复用）

**初版设计**：
```go
// 新建 Redis key: auto:progress:{sessionID}
// TTL 1h, multi-field hash
```

**审计发现**：
- `autoroute/session_intent_cache.go`（343 行）**已实现** Redis 读写模式：
  - `IntentRedisStore` 接口（Set/Get/Delete + TTL）
  - `toCacheIntent / fromCacheIntent` 序列化
  - `IncrementHit` 并发安全的 read-modify-write
- `domains/ursm/v2/cache` 提供底层 `Intent` 结构（Redis 权威 + 进程 LRU 镜像）

**⚠️ 复核修正（本轮二次核实）**：初稿建议"直接给 `ursmcache.Intent` 加 4 个字段"，
经核实**不成立**：`ursmcache` 是 URSM v2 的**共享包**，除 autoroute 外还有
`domains/routing/sticky.go`、`domains/streaming/executors/sticky.go`、
`domains/nodestatecache/bitmap.go`、`admin/logs_body_cache.go`、`cmd/gateway/main.go`
等 9 处消费者。往共享 `Intent` 里塞 auto 专用评分字段会把 autoroute 的关注点
泄漏进 sticky 路由与 nodestate，是错误的耦合方向。

**修正后的重构方案**：
1. **不改** `ursmcache.Intent`。新增 autoroute 私有的 `SessionHealth` 结构，
   独立 Redis key `auto:health:{sessionID}`，走**同一个 Redis client**
2. 复用 `IntentRedisStore` 的**接口模式**（Set/Get/Delete + TTL 三方法签名照抄），
   新增平行的 `HealthRedisStore` 接口 —— 复用的是模式与连接，不是结构体
3. 复用 `SessionIntentCache` 的**并发范式**：`mu sync.RWMutex` + 内存 map +
   `redisStore` nil 时退化为纯内存（fail-open），与现有缓存行为一致
4. 读路径合并：`DecideV2` 已经在读 intent，健康分读取紧随其后（同一 Redis 往返批次）

**节省工作量**：~150 行新代码 → ~60 行（复用接口模式 + 并发范式，但需独立结构体）（减少 60%）

---

### 1.3 成本预设模板（✅ 已存在！）

**初版设计**：
```sql
-- migration 715
CREATE TABLE pricing_presets (
  preset_key text,
  provider_id int,
  raw_model_name text,
  unit_price_in int,
  ...
);
```

**审计发现**：
- `goal/cost_presets.go`（206 行）**已有完整实现**：
  - `ModePresets` map：3 档（minimal/balanced/aggressive）
  - `GetPreset(mode) ModePreset` 根据 key 返回完整配置
  - `InferCostMode` 自动推断模式
- 结构包含 `MaxRetryCount / MaxContinueCount / LoopThreshold / MaxModelSwitch` 等所有需要的参数

**重构方案**：
1. **不需要** migration 715
2. 把 `goal.ModePresets` 移植为 `admin/pricing_presets.go`：
   ```go
   var PricingPresets = map[string]PricingPreset{
       "openai_official": { ... },
       "anthropic_official": { ... },
       "china_major": { ... },
       "aggregator": { ... },
       "free_tier": { ... },
       "performance": { ... },
   }
   ```
3. `GET /api/pricing/presets` 直接 JSON 序列化此 map
4. `POST /api/pricing/presets/apply` 读 map → 批量 `UPDATE model_offers SET ... WHERE provider_id=... AND pricing_source='preset'`

**节省工作量**：~250 行（migration + store + endpoint）→ ~80 行（端点 + map 移植）（减少 68%）

---

### 1.4 换模型回路（✅ 已存在！）

**初版设计**：
```go
// autoroute/decision_v2.go 末尾调 RecommendModelAlternatives
if composite < 70 {
    alternatives := RecommendModelAlternatives(...)
    switchModel = alternatives[0]
}
```

**审计发现**：
- `goal/loop_detector.go::detectLoop`（103 行）**已是完整实现**：
  ```go
  func detectLoop(...) loopDecision {
      if budgetExhausted || repeating {
          nextModel := pickNextModel(sess)
          return loopDecision{switchModel: nextModel, reason: "..."}
      }
      return loopDecision{canContinue: true}
  }
  ```
- `goal/loop_detector.go::applyModelSwitch`（15 行）实现原子 CAS：
  ```go
  func applyModelSwitch(...) bool {
      won, err := db.AtomicModelSwitch(ctx, ..., newModel, maxSwitch)
      if won {
          sess.ModelSwitchCount++
          sess.AutoContinueCount = 0 // 重置预算
      }
  }
  ```
- `autoroute/model_alternatives.go::RecommendModelAlternatives` 已存在（失败时推荐次优）

**重构方案**：
1. 把 `goal/loop_detector.go` 的核心逻辑抽象为 `autoroute/progress_decision.go`：
   ```go
   type ProgressDecision struct {
       Action       string // "continue" | "switch_model" | "enrich_context" | "suggest_stop"
       SwitchTarget string
       Reason       string
   }
   func DecideByProgress(health SessionHealth, tried []string) ProgressDecision
   ```
2. 在 `DecideV2` 末尾调用此函数
3. 复用 `RecommendModelAlternatives` 选次优候选

**节省工作量**：~200 行新代码 → ~60 行抽象层（减少 70%）

---

### 1.5 评分 → 动作映射（⚠️ 可重构）

**初版设计**：
```
composite >= 80         → continue
70 <= composite < 80    → enrich_context
50 <= composite < 70    → switch_model
composite < 50          → suggest_stop
```

**审计发现**：
- `goal/loop_detector.go` 已有类似映射：
  ```go
  if !budgetExhausted && !repeating {
      return loopDecision{canContinue: true}
  }
  if switchEnabled && sess.ModelSwitchCount < maxSwitch {
      return loopDecision{switchModel: nextModel}
  }
  return loopDecision{giveUp: true}
  ```

**重构方案**：
1. 把 `loopDecision` 泛化为 `ProgressDecision`（支持 4 种动作）
2. 阈值改为可配置（沿用 goal 的 settings override 模式）：
   ```go
   thresholds := loadProgressThresholds(tenantID)
   // default: continue=80, enrich=70, switch=50, stop=50
   ```

**节省工作量**：~100 行硬编码 → ~40 行可配置逻辑（减少 60%）

---

### 1.6 decision_trace 字段扩展（✅ 已有字段）

**初版设计**：
```go
type autoRouteDecision struct {
    // 新增 4 个字段
    HealthScore    int     `json:"health_score"`
    ProgressScore  float64 `json:"progress_score"`
    QualityScore   float64 `json:"quality_score"`
    ...
}
```

**审计发现**：
- `domains/streaming/auto_route.go::autoRouteDecision`（行 93-130）已有 14 个字段
- 已有 `CandidatesTop3 []autoRouteCandidate`（含 `ChannelQuality / Reliability`）
- 已有 `EnabledFeatures []string`（feature flag 列表）

**重构方案**：
1. 直接在 `autoRouteDecision` 加 5 个字段（4 维 + 综合）
2. 在 `maybeResolveAuto` 序列化时填充这些字段（从 Redis 读取的 health）

**节省工作量**：0（本就是必要改动）

---

### 1.7 pricing_source='preset'（✅ 已有机制）

**初版设计**：
```sql
UPDATE model_offers SET pricing_source = 'preset' WHERE ...
```

**审计发现**：
- `admin/pricing.go` 已有 5 个 `pricing_source` 值：
  - `manual`（行 302）
  - `imported`（行 464）
  - `copied`（行 775）
  - `inherited`（行 925）
  - `auto`（隐式，未显式设置）

**重构方案**：
1. 加第 6 个值 `preset`
2. 在 `POST /api/pricing/presets/apply` 端点写 `pricing_source='preset'`

**节省工作量**：0（本就是必要改动）

---

## 2. 重构后的架构对比

### 2.1 初版方案（新建为主）

```
新建模块：
  autoroute/session_progress.go         (300 行)
  autoroute/progress_redis.go           (150 行)
  admin/pricing_presets.go              (200 行)
  sql/migrations/startup/715_*.sql      (50 行)
总计新增：~700 行 + 1 个 migration
```

### 2.2 重构方案（复用为主）

```
复用/扩展模块：
  autoroute/session_health.go           (80 行，纯计算，从 outcome_feedback 调用)
  autoroute/progress_decision.go        (60 行，抽象 goal/loop_detector)
  autoroute/session_intent_cache.go     (+30 行字段扩展)
  domains/ursm/v2/cache/intent.go       (+20 行字段)
  admin/pricing_presets.go              (80 行，移植 goal/cost_presets)
  domains/streaming/auto_route.go       (+5 行字段)
总计新增：~275 行 + 0 migration
减少：62% 代码量
```

---

## 3. 重构后的数据流（对比 goal mode）

### 3.1 goal mode 的完整闭环（参考实现）

```
┌─────────────────────────────────────────────────────────────┐
│ 1. 请求进入 → goal hook 拦截                                 │
└─────────────────────────────────────────────────────────────┘
                          ↓
┌─────────────────────────────────────────────────────────────┐
│ 2. loop_detector.detectLoop:                                 │
│    - hashResponse(body) → 检测重复                           │
│    - sess.AutoContinueCount >= maxContinue → 预算耗尽        │
│    - sess.RepeatCount >= repeatThreshold → 重复输出          │
└─────────────────────────────────────────────────────────────┘
                          ↓
┌─────────────────────────────────────────────────────────────┐
│ 3. loopDecision:                                             │
│    - canContinue=true → 继续用现模型                         │
│    - switchModel=nextModel → 换模型                          │
│    - giveUp=true → 停止                                      │
└─────────────────────────────────────────────────────────────┘
                          ↓
┌─────────────────────────────────────────────────────────────┐
│ 4. applyModelSwitch:                                         │
│    - db.AtomicModelSwitch(CAS under maxSwitch)              │
│    - sess.ModelSwitchCount++                                │
│    - sess.AutoContinueCount = 0 (重置预算)                  │
└─────────────────────────────────────────────────────────────┘
```

### 3.2 auto mode 重构后的闭环（移植 goal 模式）

```
┌─────────────────────────────────────────────────────────────┐
│ 1. maybeResolveAuto → DecideV2 返回 Decision                 │
└─────────────────────────────────────────────────────────────┘
                          ↓
┌─────────────────────────────────────────────────────────────┐
│ 2. 请求完成 → ReportRoutingOutcome:                          │
│    - Success / LatencyMs / CostUSD (已有)                    │
│    - 增量更新 Redis: auto:session:{sessionID}                │
│      {progress, quality, cost_eff, task_fit} (新增)         │
└─────────────────────────────────────────────────────────────┘
                          ↓
┌─────────────────────────────────────────────────────────────┐
│ 3. 下次 DecideV2:                                            │
│    - LoadSessionHealth(sessionID) from Redis                 │
│    - DecideByProgress(health, triedModels)                   │
│      → ProgressDecision{Action, SwitchTarget, Reason}        │
└─────────────────────────────────────────────────────────────┘
                          ↓
┌─────────────────────────────────────────────────────────────┐
│ 4. Action 执行:                                              │
│    - "continue" → 无动作                                     │
│    - "switch_model" → RecommendModelAlternatives             │
│    - "enrich_context" → 注入 X-Gw-Context-Hint 头            │
│    - "suggest_stop" → 注入 X-Gw-Auto-Suggested-Action 头     │
└─────────────────────────────────────────────────────────────┘
```

**关键差异**：
- goal 有 DB 持久化的 `goal_sessions` 表（`model_switch_count / repeat_count`）
- auto 用 Redis 短期缓存（TTL 1h），不持久化到 DB（降低写负担）
- auto 没有"预算"概念（没有 `AutoContinueCount`），只看健康分

---

## 4. 成本预设的移植方案

### 4.1 goal 的 cost_presets.go（已有）

```go
var ModePresets = map[CostMode]ModePreset{
    CostModeMinimal: {
        MaxRetryCount: 2,
        MaxContinueCount: 0,
        ...
    },
    CostModeBalanced: {
        MaxRetryCount: 3,
        MaxContinueCount: 5,
        LoopThreshold: 2,
        MaxModelSwitch: 3,
        ...
    },
    CostModeAggressive: { ... },
}
```

### 4.2 移植为 admin/pricing_presets.go

```go
package admin

type PricingPreset struct {
    Name        string            `json:"name"`
    Description string            `json:"description"`
    Provider    string            `json:"provider"` // "openai" | "anthropic" | "china" | ...
    Models      map[string]Prices `json:"models"`   // raw_model_name → {in, out, cache_r, cache_w}
}

type Prices struct {
    UnitPriceIn      int    `json:"unit_price_in_per_1m"`
    UnitPriceOut     int    `json:"unit_price_out_per_1m"`
    CacheReadPrice   int    `json:"cache_read_price_per_1m"`
    CacheWritePrice  int    `json:"cache_write_price_per_1m"`
    Currency         string `json:"currency"`
    BillingMode      string `json:"billing_mode"`
}

var PricingPresets = map[string]PricingPreset{
    "openai_official": {
        Name:        "OpenAI Official Pricing (2026-09)",
        Description: "Official list prices from OpenAI API docs",
        Provider:    "openai",
        Models: map[string]Prices{
            "gpt-4o":      {UnitPriceIn: 2500, UnitPriceOut: 10000, Currency: "USD", BillingMode: "paid"},
            "gpt-4o-mini": {UnitPriceIn: 150, UnitPriceOut: 600, Currency: "USD", BillingMode: "paid"},
            ...
        },
    },
    "anthropic_official": { ... },
    "china_major": { ... },
    "aggregator": { ... },
    "free_tier": { ... },
    "performance": { ... },
}

// GET /api/pricing/presets
func (s *Server) handleGetPricingPresets(w http.ResponseWriter, r *http.Request) {
    writeJSON(w, http.StatusOK, PricingPresets)
}

// POST /api/pricing/presets/apply
func (s *Server) handleApplyPricingPreset(w http.ResponseWriter, r *http.Request) {
    var req struct {
        PresetKey string `json:"preset_key"`
        Provider  string `json:"provider"`
        Scope     string `json:"scope"` // "unpriced_only" | "all"
    }
    json.NewDecoder(r.Body).Decode(&req)
    
    preset := PricingPresets[req.PresetKey]
    // UPDATE model_offers SET ... WHERE provider_id=... AND pricing_source='preset'
}
```

**优势**：
- 无 migration
- 可在代码里快速迭代（加新预设只需改 map）
- 与 goal 的 `cost_presets.go` 同构（易维护）

---

## 5. 重构方案的风险评估

| 风险 | 初版方案 | 重构方案 | 缓解 |
|---|---|---|---|
| Redis 写频率 | 中（每请求 1 次 Set） | 中（每请求 1 次 HSet，4 个字段） | 复用 `outcome_feedback` 的 fire-and-forget |
| Redis key 冲突 | 低（新 key `auto:progress:{sessionID}`） | 低（合并到 `auto:session:{sessionID}`） | 同 key 减少 Redis 调用 |
| goal 逻辑耦合 | 无（独立实现） | 中（共享 `progress_decision.go`） | 抽象层清晰，goal/auto 各自调用 |
| 成本预设维护 | 高（DB migration + CRUD） | 低（Go map，代码内维护） | 预设变更走 code review |
| 换模型风暴 | 中（需手动限流） | 低（复用 goal 的 `maxSwitch` CAS） | 已有 DB 级原子计数器 |

---

## 6. 最终建议

### 6.1 立即可做（无风险）

1. ✅ **移植成本预设**：把 `goal/cost_presets.go` 改写为 `admin/pricing_presets.go`，3 个端点，0 migration
2. ✅ **扩展 decision_trace**：在 `autoRouteDecision` 加 5 个字段（health 相关）
3. ✅ **复用 RecommendModelAlternatives**：不改动，直接在 `DecideV2` 调用

### 6.2 需轻量重构（低风险）

4. ⚠️ **抽象 progress_decision**：把 `goal/loop_detector.go` 的 `loopDecision` 泛化为 `ProgressDecision`，goal 和 auto 共享
5. ⚠️ **扩展 session_intent_cache**：加 4 个 health 字段，合并 Redis key

### 6.3 需新建（中风险，但必要）

6. 🆕 **session_health.go**：纯计算函数，从 `ReportRoutingOutcome` 调用，80 行
7. 🆕 **X-Gw-Auto-Suggested-Action 头**：注入建议动作（continue / switch / enrich / stop）

---

## 7. 修订后的实施阶段

| 阶段 | 范围 | 代码量 | 风险 | 依赖 |
|---|---|---|---|---|
| 1 成本预设 | 移植 `goal/cost_presets` + 3 端点 | ~80 行 | 低 | 无 |
| 2 评分计算 | 新建 `session_health.go` + 扩展 `ReportRoutingOutcome` | ~80 行 | 低 | 无 |
| 3 Redis 存储 | 新增 autoroute 私有 `SessionHealth` + `HealthRedisStore`（复用 intent 缓存的接口模式与 Redis 连接，**不改** 共享 `ursmcache.Intent`，见 §1.2 复核修正） | ~60 行 | 低 | 阶段 2 |
| 4 决策抽象 | 抽象 `progress_decision.go`（goal/auto 共享） | ~60 行 | 中 | 阶段 3 |
| 5 集成到 DecideV2 | 调用 `DecideByProgress` + 注入头 | ~40 行 | 中 | 阶段 4 |
| 6 可观测 | Grafana + SQL 视图 | ~50 行 | 低 | 阶段 5 |

**总代码量**：~370 行（初版 ~700 行，减少 47%）

---

## 8. 附录：关键代码位置索引

| 功能 | 文件 | 行数 | 关键函数/结构 |
|---|---|---|---|
| 进展检测（参考） | `goal/loop_detector.go` | 211 | `detectLoop / hashResponse / applyModelSwitch` |
| 成本预设（参考） | `goal/cost_presets.go` | 206 | `ModePresets / GetPreset` |
| outcome 回流 | `autoroute/outcome_feedback.go` | 100 | `RoutingOutcome / ReportRoutingOutcome` |
| session 缓存 | `autoroute/session_intent_cache.go` | 343 | `IntentRedisStore / CachedIntent` |
| 模型替代推荐 | `autoroute/model_alternatives.go` | ? | `RecommendModelAlternatives` |
| auto 决策入口 | `domains/streaming/auto_route.go` | 150 | `maybeResolveAuto / autoRouteDecision` |
| pricing 端点 | `admin/pricing.go` | 1045 | `handleBulkUpdate / handleAutoInherit` |
| decision wire | `autoroute/decision_v2.go` | 302 | `DecideV2` |

---

## 9. 审计附录：goal mode 作为"完整参考实现"

goal mode（`domains/hooks/goal/`）是本方案的**完整参考实现**，因为它已经实现了：
- 会话状态持久化（`goal_sessions` 表）
- 进展检测（`AutoContinueCount / RepeatCount`）
- 换模型（`ModelSwitchCount / AtomicModelSwitch`）
- 成本分档（`cost_presets.go` 三档）
- 重复检测（`hashResponse`）
- 原子 CAS（`CompareAndSetState / AtomicModelSwitch`）

auto mode 移植时的**关键适配**：
1. goal 持久化到 DB → auto 用 Redis 短期缓存（TTL 1h）
2. goal 有"预算"（`max_continue_count`）→ auto 用"健康分"（0-100）
3. goal 单会话单模型 → auto 多候选池（需排除 `TriedModels`）
4. goal 显式状态机（active/paused/completed/failed）→ auto 无状态（每次独立决策）

---

**审计签发**：本轮方案评审
**下一步**：输出修订后的 RFC v2（基于本审计报告重写初版方案）
