# Auto Model Selection Decision Specification

## 目标

当用户请求 `model=auto` 时，网关需要选择一个**当前可用、任务匹配度高、价格合理**的模型。选择过程应该：

1. **硬约束优先**：只有当前可用的模型才能被选择
2. **任务理解驱动**：根据请求内容识别任务类型，优先匹配该任务类型
3. **会话校正机制**：利用上次任务结果进行小幅校正，但不锁定历史选择
4. **热门池优先**：从最近使用量最高的模型中选择，避免冷门旧模型
5. **价格敏感**：在满足任务匹配的前提下，优先选择性价比高的模型
6. **明确回退**：没有合适结果时，回退到 48 小时内使用最多的可用模型

---

## 决策流程

### Phase 0: 会话缓存检查（可选优化）

**条件**：如果请求携带 `X-Gw-Session-Id` 且会话缓存未过期（TTL 10分钟）

**行为**：
- 检查缓存的 `CachedIntent.TaskType` 是否仍适用当前请求
- 如果任务类型发生重大变化（vision/long_context/agent 硬覆盖），**必须重新决策**
- 如果缓存命中且任务未变，**必须重新验证**缓存模型的可用性：
  - 检查 `credential_model_bindings.available = true`
  - 检查 `provider_models.available = true`
  - 检查 `unavailable_reason IS NULL` 或不包含 `'manual'`
- **只有通过可用性校验，才能复用缓存**；否则重新决策

**重分类触发条件**（`shouldReclassify`）：
```go
// 硬覆盖信号
- sigs.HasImages && cached != TaskVision
- sigs.EstimatedTokens > 50_000 && cached != TaskLongContext
- sigs.ToolCount >= 3 && sigs.HasToolResults && cached != TaskAgent
```

---

### Phase 1: 任务分类

**目的**：将当前请求映射到任务类型

**任务类型**：
- `TaskVision`：图片输入
- `TaskCode`：编程、代码生成
- `TaskReasoning`：推理、数学、逻辑
- `TaskAgent`：多工具调用
- `TaskFunctionCall`：单工具调用
- `TaskCreative`：创作、写作
- `TaskLongContext`：超长上下文（> 50k tokens）
- `TaskChat`：兜底

**分类策略**：
1. **硬覆盖**（优先级最高）
   - 图片 → `TaskVision`
   - 强编程信号（代码块/IDE指纹/计划模式） → `TaskCode`
   - 超长上下文 → `TaskLongContext`

2. **工具调用判断**
   - `ToolCount >= 3 && HasToolResults` → `TaskAgent`
   - `ToolCount 1-2` → `TaskFunctionCall`

3. **关键词+模式匹配**
   - 对 `LastUserPrompt` 和 `SystemPrompt` 进行关键词扫描
   - 结合正则模式（如数学公式、算法描述）
   - 综合评分，取最高分

4. **兜底**
   - 无明确信号 → `TaskChat`

**输出**：`Classification`
- `Primary`: 主任务类型
- `Confidence`: 置信度 (0-1)
- `Classifier`: 分类器名称（heuristic/llm_fallback/session_cache）
- `Reason`: 分类原因（用于审计）

---

### Phase 2: 候选池构建（硬约束门禁）

**核心原则**：只有**当前可用**的模型才能进入候选池

**数据源**：`credential_model_index` + `credential_model_bindings` + `provider_models`

**可用性硬约束**：
```sql
WHERE cmb.credential_id = <cred_id>
  AND cmb.available = TRUE
  AND cmb.unavailable_reason IS NULL OR cmb.unavailable_reason NOT LIKE 'manual%'
  AND pm.available = TRUE
  AND pm.unavailable_reason IS NULL OR pm.unavailable_reason NOT LIKE 'manual%'
  AND c.status = 'active'
  AND c.lifecycle_status != 'suspended'
```

**候选池分层**：

1. **L1: 热门候选池（Top 3 Canonical Models）**
   - 统计最近 48 小时内，每个 `canonical_name` 的请求量
   - 按请求量降序，取 Top 3
   - 从这 3 个 canonical 模型展开所有可用 credentials

2. **L2: 兜底候选池**
   - 如果 L1 候选数量 < 3，从其余可用模型补充
   - 按 `success_rate` 降序排序

**输出**：`[]Candidate`，每个包含：
- `CredentialID`, `CanonicalID`, `CanonicalName`, `RawModel`
- `UnitPriceInPer1M`, `UnitPriceOutPer1M`
- `SuccessRate`, `P95LatencyMs`
- `ContextWindow`, `Tags`
- `TaskMatchScore`（预计算）

---

### Phase 3: 评分与排序

**评分公式**（简化为 2 维 + 校正项）：

```
FinalScore = IntentMatchScore * 0.6 + PriceScore * 0.4 + CorrectionScore
```

#### 3.1 意图匹配分（IntentMatchScore, 0-100）

**计算方法**：基于任务类型与模型 `Tags` 的交集

```go
requiredTags := map[TaskType][]string{
    TaskReasoning:    {"reasoning", "math", "logic"},
    TaskCode:         {"code", "programming"},
    TaskAgent:        {"agent", "tool_use", "function_call"},
    TaskCreative:     {"creative", "writing"},
    TaskLongContext:  {"long_context", "128k", "200k", "512k", "1m"},
    TaskVision:       {"vision", "multimodal"},
    TaskFunctionCall: {"function_call", "tool_use"},
    TaskChat:         nil, // 无特定要求，返回 50
}

hits := 0
for _, required := range requiredTags[task] {
    for _, candidateTag := range candidate.Tags {
        if contains(candidateTag, required) {
            hits++
            break
        }
    }
}
intentMatch := (hits / len(requiredTags)) * 100  // 归一化到 0-100
```

#### 3.2 价格分（PriceScore, 0-100）

**目标**：`1000 - avg(该模型所有有效凭据的成本价格平均值)`

**计算方法**：
1. 对每个 `canonical_model`，收集其所有**有效凭据**的 `(UnitPriceInPer1M + UnitPriceOutPer1M)`
2. 计算平均值 `avgCost`
3. `priceScore = max(0, 1000 - avgCost)`

**注意**：
- 如果 `avgCost > 1000`，`priceScore = 0`
- 免费模型（`avgCost = 0`）得满分 1000

#### 3.3 校正分（CorrectionScore, -10 ~ +10）

**目的**：根据上次任务结果进行小幅校正，避免过度强化

**数据源**：从 `request_logs` 读取同 `session_id` 或同 `api_key_id` 的上一次 `auto` 请求

**校正规则**：
```go
if lastTask == currentTask {
    if lastSuccess && lastLatency < threshold {
        // 上次成功且快速 → 小幅加分
        correction = +5
    } else if !lastSuccess {
        // 上次失败 → 降权
        correction = -10
    }
} else {
    // 任务类型变化 → 校正快速衰减
    correction = 0
}

// 对同一 canonical_model 应用校正
if candidate.CanonicalName == lastChosenModel {
    candidate.CorrectionScore = correction
}
```

**约束**：
- 校正分绝对值 ≤ 10，避免历史锁定
- 只影响同一 `canonical_model`

#### 3.4 最终排序

```go
for each candidate in candidatePool {
    finalScore := intentMatch * 0.6 + priceScore * 0.4 + correctionScore
    candidate.FinalScore = finalScore
}

sort(candidates, by: FinalScore, descending)
winner := candidates[0]
```

---

### Phase 4: 无合适结果时的回退

**触发条件**：
- 候选池为空（所有模型不可用）
- 或所有候选的 `IntentMatchScore < 30`（意图匹配过低）

**回退逻辑**：
1. 查询 `request_logs`，统计最近 48 小时内使用量最高的 `canonical_model`
   ```sql
   SELECT canonical_id, count(*) as usage_count
   FROM request_logs
   WHERE ts > NOW() - INTERVAL '48 hours'
     AND success = TRUE
   GROUP BY canonical_id
   ORDER BY usage_count DESC
   LIMIT 1
   ```

2. 从该 `canonical_model` 的可用凭据中，选择 `success_rate` 最高的

3. 如果仍然没有，返回 `error: no available model`，让外层 fallback model 接管

---

### Phase 5: 决策输出与审计

**输出**：`Decision`
```go
type Decision struct {
    ChosenModel        string  // canonical_name
    ChosenCredentialID int64
    ChosenRawModel     string  // 实际发送给上游的模型名
    TaskType           TaskType
    Confidence         float64
    Profile            Profile  // smart/speed_first/cost_first
    Classifier         string
    Reason             string
    CandidatesTopN     []ScoredCandidate  // Top 3 用于审计
    DecidedAt          time.Time
}
```

**持久化到 `request_logs`**：
- `is_auto_request = TRUE`
- `task_type = decision.TaskType`
- `auto_profile = decision.Profile`
- `auto_decision = JSON(decision)`  // JSONB 列，包含完整决策树
- `auto_confidence = decision.Confidence`
- `model_chosen = decision.ChosenModel`

**审计能力**：
- 通过 `request_logs.auto_decision` 可回溯每次决策的候选池、评分明细、校正因子
- 支持 A/B 测试：对比不同评分权重、不同热门池大小的效果

---

## 与现有代码的差异

| 维度 | 现有实现 | 新规格 |
|------|---------|--------|
| **评分维度** | 8 维（price/speed/stability/match/pressure/context/version/strength） | 2 维（intent/price）+ 校正项 |
| **候选初始化** | `tier=primary` + `PopularityScore` top 20（但字段未加载） | 48h 请求量 Top 3 canonical models |
| **可用性校验** | 只过滤 `UnavailableReason != ""`（但字段未加载） | 硬约束：`cmb.available && pm.available` |
| **会话缓存** | 直接复用，不重新验证可用性 | 复用前必须重新验证可用性 |
| **回退机制** | 无明确回退，返回 nil | 48h 最常用可用模型 |
| **价格分** | 归一化到 P75（0-100） | `1000 - avgCost`（绝对值） |
| **校正机制** | 无 | 基于上次任务结果的小幅校正（±10） |

---

## 实现路径

### 1. 数据层修改

**新增索引刷新字段**：
```sql
-- 在 refreshIndexSQL 中补充
SELECT 
    ...existing fields...,
    cmb.available AS cmb_available,
    pm.available AS pm_available,
    cmb.unavailable_reason AS cmb_unavailable_reason,
    pm.unavailable_reason AS pm_unavailable_reason
FROM credential_model_index cmi
JOIN credential_model_bindings cmb ON ...
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE cmb.available = TRUE
  AND pm.available = TRUE
  AND cmb.unavailable_reason IS DISTINCT FROM 'manual'
  AND pm.unavailable_reason IS DISTINCT FROM 'manual'
```

**新增热门度统计**：
```sql
-- 在 bg/auto_index_refresher.go 的 rollup 中增加
WITH recent_usage AS (
    SELECT canonical_id, count(*) as usage_48h
    FROM request_logs
    WHERE ts > NOW() - INTERVAL '48 hours'
      AND success = TRUE
    GROUP BY canonical_id
)
```

### 2. 核心逻辑修改

**文件**：`autoroute/index.go`, `autoroute/scoring.go`, `autoroute/decision.go`

**改动点**：
- `Recommend()` 重写候选池逻辑，改用 Top 3 热门 canonical
- `Score()` 简化为 2 维 + 校正
- `Decide()` 增加可用性重校验，增加 48h 回退

### 3. 会话缓存增强

**文件**：`autoroute/session_intent_cache.go`, `autoroute/decision.go`

**改动点**：
- `Decide()` 在缓存命中后，增加可用性验证 SQL
- 验证失败则 `Invalidate(sessionID)` 并重新决策

### 4. 测试更新

**文件**：`autoroute/index_test.go`, `autoroute/decision_test.go`, `autoroute/scoring_test.go`

**新增测试**：
- `TestRecommend_AvailabilityGate`：验证不可用模型被过滤
- `TestRecommend_HotTop3`：验证热门池逻辑
- `TestScore_IntentAndPrice`：验证 2 维评分
- `TestDecide_SessionCacheRevalidation`：验证缓存可用性重校验
- `TestDecide_48hFallback`：验证回退逻辑

---

## 预期效果

1. **不再选择不可用模型**
   - 硬约束保证 100%

2. **不再选择冷门旧模型**
   - 候选池限制在 48h Top 3 热门

3. **意图匹配度提升**
   - 简化评分后，任务匹配权重 60%

4. **价格合理性提升**
   - 价格权重 40%，且基于实际平均成本

5. **会话连贯性增强**
   - 校正机制避免频繁切换，但不锁定

6. **可审计性**
   - 每次决策的完整上下文存入 `request_logs.auto_decision`

---

## 风险与缓解

| 风险 | 缓解措施 |
|------|---------|
| Top 3 热门池过窄，导致候选不足 | L2 兜底池补充 |
| 新模型冷启动困难 | 保留 admin 手动置顶机制 |
| 历史数据缺失导致回退失败 | 兜底到系统默认模型 |
| 会话缓存验证增加延迟 | 验证 SQL 加索引，预估 < 5ms |

---

## 后续优化方向

1. **机器学习增强**：用历史成功率训练意图分类器
2. **动态权重**：根据用户反馈调整 0.6/0.4 权重
3. **多目标优化**：增加延迟、稳定性等维度
4. **分布式缓存**：Redis 替换进程内缓存，支持多实例

---

_Last Updated: 2026-06-28_
_Author: Kiro AI Assistant_
