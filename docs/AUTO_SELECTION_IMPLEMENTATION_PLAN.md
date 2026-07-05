# Auto 模型选择重构实施方案

## 执行摘要

当前 `auto` 模型选择存在三大核心问题：
1. **可用性校验失效**：依赖的字段（`Tier`, `PopularityScore`, `UnavailableReason`）未从数据库加载
2. **评分公式偏离需求**：使用 8 维复杂权重，而非业务要求的"意图匹配 0.6 + 价格 0.4"
3. **缺少回退机制**：无合适结果时无明确回退，且会话缓存不验证可用性

本方案将分 **3 个阶段**、**7 个步骤** 进行重构，最小化风险，逐步替换现有逻辑。

---

## 阶段划分

### 阶段 1：数据层修复（低风险，不改决策逻辑）
**目标**：让现有代码能拿到真正需要的数据  
**改动**：数据库查询、索引刷新  
**风险**：低，只增加字段，不改逻辑

### 阶段 2：决策逻辑替换（核心改动）
**目标**：实现新的 2 维评分 + 热门池 + 会话校验  
**改动**：`Recommend()`, `Score()`, `Decide()`  
**风险**：中高，需要充分测试

### 阶段 3：回退与优化（增强鲁棒性）
**目标**：48 小时回退、会话纠偏、审计增强  
**改动**：回退查询、校正逻辑、日志  
**风险**：低，纯增强

---

## 详细实施步骤

### 步骤 1：增强索引刷新 SQL（阶段 1）

**文件**：`autoroute/index.go`, `bg/auto_index_refresher.go`

**改动 1.1**：在 `refreshIndexSQL` 中补充可用性字段

```sql
-- 当前缺失的字段
SELECT 
    ...existing...,
    cmb.available AS cmb_available,
    cmb.unavailable_reason AS cmb_unavailable_reason,
    pm.available AS pm_available,
    pm.unavailable_reason AS pm_unavailable_reason
FROM credential_model_index cmi
JOIN latest_bucket lb ON ...
JOIN credentials cr ON cr.id = cmi.credential_id
JOIN credential_model_bindings cmb 
    ON cmb.credential_id = cmi.credential_id
   AND cmb.provider_model_id = (
       SELECT pm_inner.id 
       FROM provider_models pm_inner 
       WHERE pm_inner.raw_model_name = cmi.raw_model
         AND pm_inner.provider_id = cr.provider_id
       LIMIT 1
   )
JOIN provider_models pm ON pm.id = cmb.provider_model_id
LEFT JOIN models_canonical mc ON mc.id = cmi.canonical_id
WHERE COALESCE(cr.lifecycle_status, 'active') != 'suspended'
  AND COALESCE(cr.status, 'active') NOT IN ('disabled')
  AND cmb.available = TRUE
  AND pm.available = TRUE
  AND (cmb.unavailable_reason IS NULL OR cmb.unavailable_reason NOT LIKE 'manual%')
  AND (pm.unavailable_reason IS NULL OR pm.unavailable_reason NOT LIKE 'manual%')
ORDER BY cmi.canonical_id, cmi.score_smart DESC
```

**改动 1.2**：在 `scanIndexRow()` 中读取新字段

```go
func scanIndexRow(rows interface{ Scan(dest ...any) error }) (Candidate, error) {
    var c Candidate
    var tags []string
    var ctxWindow *int
    var canonicalID *int
    var canonicalName *string
    var billingMode *string
    var priceIn, priceOut, successRate *float64
    var cmbAvailable, pmAvailable *bool
    var cmbUnavailableReason, pmUnavailableReason *string
    
    if err := rows.Scan(
        &c.CredentialID, &c.RawModel, &canonicalID,
        &canonicalName, &tags, &ctxWindow,
        &billingMode,
        &priceIn, &priceOut,
        &successRate, &c.P95LatencyMs,
        &c.ActiveSessions, &c.ConcurrencyLimit,
        &cmbAvailable, &cmbUnavailableReason,
        &pmAvailable, &pmUnavailableReason,
    ); err != nil {
        return c, err
    }
    
    // 合成 UnavailableReason（任一不可用即视为不可用）
    if cmbAvailable != nil && !*cmbAvailable {
        if cmbUnavailableReason != nil {
            c.UnavailableReason = *cmbUnavailableReason
        } else {
            c.UnavailableReason = "cmb_unavailable"
        }
    } else if pmAvailable != nil && !*pmAvailable {
        if pmUnavailableReason != nil {
            c.UnavailableReason = *pmUnavailableReason
        } else {
            c.UnavailableReason = "pm_unavailable"
        }
    }
    
    // ... 其余字段填充 ...
    return c, nil
}
```

**验证**：
- 索引刷新后，`Index.entries` 中所有 `Candidate.UnavailableReason` 正确反映可用性
- 不可用的 credential/model 被正确过滤

---

### 步骤 2：增加 48 小时使用量统计（阶段 1）

**文件**：`bg/auto_index_refresher.go`

**新增 rollup**：在现有 `rollupCredentialModelIndexSQL` 之前，增加热门度统计

```go
const rollupHotModelsSQL = `
INSERT INTO hot_models_48h (canonical_id, usage_count, last_updated)
SELECT canonical_id, count(*) as usage_count, NOW()
FROM request_logs
WHERE ts > NOW() - INTERVAL '48 hours'
  AND success = TRUE
  AND canonical_id IS NOT NULL
GROUP BY canonical_id
ON CONFLICT (canonical_id) DO UPDATE SET
    usage_count = EXCLUDED.usage_count,
    last_updated = EXCLUDED.last_updated
`
```

**新建表**（migration）：
```sql
CREATE TABLE IF NOT EXISTS hot_models_48h (
    canonical_id INT PRIMARY KEY,
    usage_count BIGINT NOT NULL,
    last_updated TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_hot_models_usage ON hot_models_48h(usage_count DESC);
```

**验证**：
- 每 5 分钟刷新后，`hot_models_48h` 包含最新统计
- 可通过 admin API 查询热门 Top 3

---

### 步骤 3：重写 `Recommend()` - 热门池逻辑（阶段 2）

**文件**：`autoroute/index.go`

**核心改动**：替换现有的 `tier=primary` 逻辑，改用热门 Top 3

```go
func (idx *Index) Recommend(task TaskType, sigs ClassificationSignals, profile Profile, topN int) []ScoredCandidate {
    idx.mu.RLock()
    all := idx.entries
    idx.mu.RUnlock()

    if topN <= 0 {
        topN = 3
    }

    // Step 1: 硬过滤 - 只保留可用候选
    available := make([]Candidate, 0, len(all))
    for i := range all {
        c := all[i]
        if c.UnavailableReason == "" {
            c.TaskMatchScore = TaskMatchScore(task, c.Tags)
            available = append(available, c)
        }
    }

    if len(available) == 0 {
        // 触发 48h 回退（步骤 6 实现）
        return nil
    }

    // Step 2: 按 canonical_id 分组
    byCanonical := make(map[int][]Candidate)
    for _, c := range available {
        byCanonical[c.CanonicalID] = append(byCanonical[c.CanonicalID], c)
    }

    // Step 3: 查询热门 Top 3
    hotTop3 := idx.getHotTop3Canonicals(context.Background())
    
    // Step 4: 构建候选池（优先热门，不足则补充）
    candidatePool := []Candidate{}
    for _, canonID := range hotTop3 {
        if cands, ok := byCanonical[canonID]; ok {
            candidatePool = append(candidatePool, cands...)
        }
    }
    
    // Step 5: 如果热门池 < 3 个模型，从其余补充
    if len(hotTop3) < 3 {
        for canonID, cands := range byCanonical {
            if !contains(hotTop3, canonID) {
                candidatePool = append(candidatePool, cands...)
            }
        }
    }

    // Step 6: 评分（步骤 4 改用新公式）
    scored := make([]ScoredCandidate, 0, len(candidatePool))
    for _, c := range candidatePool {
        bd := ScoreSimplified(c, task, sigs) // 新评分函数
        scored = append(scored, ScoredCandidate{Candidate: c, Breakdown: bd})
    }

    // Step 7: 排序并返回 Top N
    sort.SliceStable(scored, func(i, j int) bool {
        return scored[i].Breakdown.Composite > scored[j].Breakdown.Composite
    })

    if len(scored) > topN {
        scored = scored[:topN]
    }

    return scored
}

func (idx *Index) getHotTop3Canonicals(ctx context.Context) []int {
    if idx.pool == nil {
        return []int{}
    }
    
    rows, err := idx.pool.Query(ctx, `
        SELECT canonical_id 
        FROM hot_models_48h 
        ORDER BY usage_count DESC 
        LIMIT 3
    `)
    if err != nil {
        return []int{}
    }
    defer rows.Close()
    
    var result []int
    for rows.Next() {
        var id int
        if err := rows.Scan(&id); err == nil {
            result = append(result, id)
        }
    }
    return result
}
```

**验证**：
- 候选池优先包含 48h Top 3 模型
- 不可用模型被完全排除
- 返回的候选按新评分排序

---

### 步骤 4：简化评分公式（阶段 2）

**文件**：`autoroute/scoring_simplified.go`（新建）

```go
package autoroute

// ScoreSimplified 实现简化的 2 维评分公式：
// FinalScore = IntentMatchScore * 0.6 + PriceScore * 0.4
func ScoreSimplified(c Candidate, task TaskType, sigs ClassificationSignals) ScoringBreakdown {
    // 1. 意图匹配分（0-100）
    intentMatch := c.TaskMatchScore * 100

    // 2. 价格分（基于该模型所有候选的平均价格）
    // 注意：这里简化为单个候选的价格，真正实现需要按 canonical 聚合
    avgCost := c.UnitPriceInPer1M + c.UnitPriceOutPer1M
    priceScore := 1000 - avgCost
    if priceScore < 0 {
        priceScore = 0
    }
    // 归一化到 0-100 范围（假设 1000 对应满分）
    priceScoreNorm := priceScore / 10.0
    if priceScoreNorm > 100 {
        priceScoreNorm = 100
    }

    // 3. 组合分
    composite := intentMatch*0.6 + priceScoreNorm*0.4

    return ScoringBreakdown{
        MatchScore: intentMatch,
        PriceScore: priceScoreNorm,
        Composite:  composite,
        // 其余维度保持为 0，保持结构兼容
        SpeedScore:     0,
        StabilityScore: 0,
        PressureScore:  0,
        ContextFit:     0,
        VersionRecency: 0,
        StrengthMatch:  0,
    }
}
```

**完整实现**（按 canonical 聚合价格）：

```go
func (idx *Index) ScoreWithCanonicalPrice(c Candidate, task TaskType) ScoringBreakdown {
    // 1. 意图匹配
    intentMatch := c.TaskMatchScore * 100

    // 2. 查询该 canonical 的平均价格
    avgCost := idx.getCanonicalAvgPrice(c.CanonicalID)
    priceScore := max(0, 1000-avgCost) / 10.0 // 归一化到 0-100
    if priceScore > 100 {
        priceScore = 100
    }

    composite := intentMatch*0.6 + priceScore*0.4

    return ScoringBreakdown{
        MatchScore: intentMatch,
        PriceScore: priceScore,
        Composite:  composite,
    }
}

func (idx *Index) getCanonicalAvgPrice(canonicalID int) float64 {
    idx.mu.RLock()
    defer idx.mu.RUnlock()

    var sum, count float64
    for _, c := range idx.entries {
        if c.CanonicalID == canonicalID && c.UnavailableReason == "" {
            sum += c.UnitPriceInPer1M + c.UnitPriceOutPer1M
            count++
        }
    }
    if count == 0 {
        return 0
    }
    return sum / count
}
```

**验证**：
- 意图匹配占 60%，价格占 40%
- 价格分基于 canonical 平均，而非单个 credential

---

### 步骤 5：会话缓存可用性校验（阶段 2）

**文件**：`autoroute/decision.go`

**改动**：在缓存命中后，增加可用性验证

```go
func (d *Decider) Decide(ctx context.Context, sigs ClassificationSignals, apiKeyID int, headerProfile string, taskHint TaskType, sessionID string) (*Decision, error) {
    // Step 0: check session intent cache
    if sessionID != "" && d.intentCache != nil {
        if cached, ok := d.intentCache.Get(sessionID); ok {
            if !shouldReclassify(cached.TaskType, sigs) {
                // 新增：验证缓存的 model/credential 是否仍可用
                if d.validateCachedChoice(ctx, cached.CredentialID, cached.ChosenModel) {
                    return &Decision{
                        ChosenModel:        cached.ChosenModel,
                        ChosenCredentialID: cached.CredentialID,
                        ChosenRawModel:     cached.ChosenModel,
                        TaskType:           cached.TaskType,
                        Confidence:         cached.Confidence,
                        Profile:            cached.Profile,
                        Classifier:         "session_cache",
                        Reason:             "reused session intent (revalidated)",
                        DecidedAt:          time.Now(),
                    }, nil
                } else {
                    // 可用性校验失败，清除缓存并重新决策
                    d.intentCache.Invalidate(sessionID)
                    slog.Info("autoroute: cached choice no longer available, reclassifying",
                        "session_id", sessionID,
                        "cached_model", cached.ChosenModel,
                    )
                }
            }
        }
    }

    // ... 其余逻辑不变 ...
}

func (d *Decider) validateCachedChoice(ctx context.Context, credentialID int64, model string) bool {
    if d.index.pool == nil {
        return false
    }

    var available bool
    err := d.index.pool.QueryRow(ctx, `
        SELECT EXISTS(
            SELECT 1 
            FROM credential_model_bindings cmb
            JOIN provider_models pm ON pm.id = cmb.provider_model_id
            JOIN models_canonical mc ON mc.id = pm.canonical_id
            WHERE cmb.credential_id = $1
              AND mc.canonical_name = $2
              AND cmb.available = TRUE
              AND pm.available = TRUE
              AND (cmb.unavailable_reason IS NULL OR cmb.unavailable_reason NOT LIKE 'manual%')
              AND (pm.unavailable_reason IS NULL OR pm.unavailable_reason NOT LIKE 'manual%')
        )
    `, credentialID, model).Scan(&available)

    return err == nil && available
}
```

**验证**：
- 缓存命中时，如果模型已不可用，会触发重新决策
- 日志中可见 "cached choice no longer available"

---

### 步骤 6：48 小时回退机制（阶段 3）

**文件**：`autoroute/index.go`

**改动**：在 `Recommend()` 返回 nil 时触发回退

```go
func (idx *Index) Recommend(task TaskType, sigs ClassificationSignals, profile Profile, topN int) []ScoredCandidate {
    // ... 前面逻辑 ...

    if len(candidatePool) == 0 {
        // 触发 48h 回退
        fallback := idx.get48hFallback(context.Background())
        if fallback != nil {
            return []ScoredCandidate{{
                Candidate: *fallback,
                Breakdown: ScoringBreakdown{
                    Composite: 50, // 中等分数
                    MatchScore: 50,
                    PriceScore: 50,
                },
            }}
        }
        return nil
    }

    // 或者：意图匹配过低时也触发
    if len(scored) > 0 && scored[0].Breakdown.MatchScore < 30 {
        slog.Warn("autoroute: low intent match, trying 48h fallback",
            "best_match_score", scored[0].Breakdown.MatchScore,
        )
        fallback := idx.get48hFallback(context.Background())
        if fallback != nil {
            return []ScoredCandidate{{
                Candidate: *fallback,
                Breakdown: ScoringBreakdown{
                    Composite: 50,
                    MatchScore: 30, // 反映实际低匹配
                    PriceScore: 50,
                },
            }}
        }
    }

    return scored
}

func (idx *Index) get48hFallback(ctx context.Context) *Candidate {
    if idx.pool == nil {
        return nil
    }

    var canonicalID int
    err := idx.pool.QueryRow(ctx, `
        SELECT canonical_id
        FROM request_logs
        WHERE ts > NOW() - INTERVAL '48 hours'
          AND success = TRUE
          AND canonical_id IS NOT NULL
        GROUP BY canonical_id
        ORDER BY count(*) DESC
        LIMIT 1
    `).Scan(&canonicalID)

    if err != nil {
        return nil
    }

    // 从该 canonical 中选择 success_rate 最高的可用 credential
    idx.mu.RLock()
    defer idx.mu.RUnlock()

    var best *Candidate
    for i := range idx.entries {
        c := &idx.entries[i]
        if c.CanonicalID == canonicalID && c.UnavailableReason == "" {
            if best == nil || c.SuccessRate > best.SuccessRate {
                best = c
            }
        }
    }

    return best
}
```

**验证**：
- 无可用候选时，返回 48h 最常用模型
- 意图匹配过低时，也会尝试回退

---

### 步骤 7：更新测试用例（阶段 3）

**文件**：`autoroute/index_test.go`, `autoroute/decision_test.go`

**新增测试**：

```go
// TestRecommend_AvailabilityGate 验证可用性硬门禁
func TestRecommend_AvailabilityGate(t *testing.T) {
    candidates := []Candidate{
        {CredentialID: 1, CanonicalName: "available", UnavailableReason: "", Tags: []string{"code"}},
        {CredentialID: 2, CanonicalName: "unavailable", UnavailableReason: "manual", Tags: []string{"code"}},
    }
    idx := &Index{entries: candidates, lastRefresh: time.Now()}

    results := idx.Recommend(TaskCode, ClassificationSignals{}, ProfileSmart, 3)

    if len(results) != 1 {
        t.Fatalf("expected 1 available candidate, got %d", len(results))
    }
    if results[0].Candidate.CanonicalName != "available" {
        t.Errorf("expected 'available', got %s", results[0].Candidate.CanonicalName)
    }
}

// TestRecommend_HotTop3Priority 验证热门池优先
func TestRecommend_HotTop3Priority(t *testing.T) {
    // 需要 mock getHotTop3Canonicals() 返回 [1, 2, 3]
    // 验证候选池优先包含这 3 个 canonical 的所有 credentials
}

// TestDecide_SessionCacheRevalidation 验证缓存可用性重校验
func TestDecide_SessionCacheRevalidation(t *testing.T) {
    // mock pool.QueryRow 返回 available=false
    // 验证缓存被 Invalidate，并触发重新决策
}

// TestRecommend_48hFallback 验证回退机制
func TestRecommend_48hFallback(t *testing.T) {
    // mock 48h 查询返回 canonical_id=99
    // 验证 Recommend 返回该 canonical 的最佳 credential
}
```

---

## 风险评估与缓解

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|---------|
| SQL 性能回归 | 索引刷新变慢 | 中 | 增加索引，WHERE 条件前置 |
| 热门池过窄 | 候选不足 | 中 | L2 兜底池补充 |
| 会话校验延迟 | 缓存命中变慢 | 低 | 查询加索引，预估 < 5ms |
| 新评分与旧评分差异大 | 用户体验变化 | 高 | 灰度发布，A/B 测试 |
| 48h 回退误用旧模型 | 质量下降 | 低 | 只在无可用候选时触发 |

---

## 上线计划

### 第 1 周：数据层准备
- [ ] 步骤 1: 增强索引刷新 SQL
- [ ] 步骤 2: 增加 48h 使用量统计表
- [ ] 验证：索引刷新不报错，`UnavailableReason` 正确

### 第 2 周：核心逻辑替换
- [ ] 步骤 3: 重写 `Recommend()` - 热门池
- [ ] 步骤 4: 简化评分公式
- [ ] 步骤 5: 会话缓存可用性校验
- [ ] 验证：单元测试全通过

### 第 3 周：回退与测试
- [ ] 步骤 6: 48h 回退机制
- [ ] 步骤 7: 更新测试用例
- [ ] 集成测试、压测

### 第 4 周：灰度发布
- [ ] 10% 流量灰度（通过 feature flag）
- [ ] 监控关键指标：选中模型分布、成功率、延迟
- [ ] 100% 全量

---

## 回滚方案

**Feature Flag**：
```go
// autoroute/index.go
const useSimplifiedScoring = true // 通过环境变量控制

func (idx *Index) Recommend(...) []ScoredCandidate {
    if useSimplifiedScoring {
        return idx.recommendSimplified(...)
    }
    return idx.recommendLegacy(...)
}
```

**回滚触发条件**：
- 成功率下降 > 5%
- P95 延迟增加 > 50%
- 用户投诉模型选择不合理 > 10 例/天

---

## 监控指标

**关键指标**：
1. `auto_request_success_rate`：auto 请求成功率
2. `auto_decision_latency_p95`：决策延迟 P95
3. `auto_chosen_model_distribution`：选中模型分布（观察是否过度集中）
4. `auto_cache_hit_rate`：会话缓存命中率
5. `auto_cache_revalidation_fail_rate`：缓存重校验失败率
6. `auto_48h_fallback_trigger_rate`：48h 回退触发率

**告警阈值**：
- 成功率 < 95% → P0
- 缓存重校验失败率 > 10% → P1
- 48h 回退触发率 > 5% → P2（可能热门池太窄）

---

## 总结

本方案通过 **分阶段、小步快跑** 的方式，逐步将 `auto` 选模逻辑从复杂的 8 维评分替换为清晰的 2 维评分，同时增强可用性保障和回退机制。

**核心改进**：
1. ✅ 硬约束保证只选可用模型
2. ✅ 热门池避免冷门旧模型
3. ✅ 2 维评分符合业务需求
4. ✅ 会话缓存带重校验
5. ✅ 明确的 48h 回退机制
6. ✅ 完整的审计与监控

**预期效果**：
- 不再选择不可用模型（100% 保证）
- 模型选择更符合任务意图（意图权重 60%）
- 价格更合理（价格权重 40%）
- 会话连贯性更好（校正机制）
- 系统更健壮（回退机制）

---

_Prepared by: Kiro AI Assistant_  
_Date: 2026-06-28_
