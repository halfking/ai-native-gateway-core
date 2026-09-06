# 自检系统优化建议与待办事项

> **生成日期**: 2026-09-06  
> **基于文档**: `SELFCHECK_ARCHITECTURE_REVIEW_20260906.md`  
> **目标**: 进一步优化供应商节点状态自动更新机制

---

## 一、已完成优化总结 ✅

### P0 紧急修复 (2026-09-02 已完成)

| 编号 | 问题 | 解决方案 | 状态 | 验证方式 |
|------|------|---------|------|---------|
| P0.1 | NULL `unavailable_recover_at` 导致恢复 SQL 跳过 | 一次性 SQL 清理历史数据 | ✅ 完成 | 查询无 NULL 值行 |
| P0.2 | `broken_confirmed` 守卫过严，一个模型阻塞整个凭据 | 放宽守卫粒度为 per-model | ✅ 完成 | 代码审查已确认 |
| P0.3 | RestoreOnSuccess 模型绑定歧义导致失败 | 使用 `ResolveRawBinding` 处理 | ⚠️ 部分 | 仍抛出错误，见下方建议 |

### P1 高优先级优化 (2026-09-03 已完成)

| 编号 | 问题 | 解决方案 | 状态 | 代码位置 |
|------|------|---------|------|---------|
| P1.1 | 同步探测提交阻塞恢复循环 | 异步化 `dispatchProbe()` + semaphore | ✅ 完成 | `bg/credential_recovery.go:185-203` |
| P1.2 | 无条件 holdoff 更新导致探测不执行 | 条件更新 `next_retry_at` | ✅ 完成 | `bg/node_probe.go:pumpDueStatesToQueue()` |
| P1.3 | 探测提交失败无重试和告警 | 3次重试 + 指数退避 + 持久化失败 | ✅ 完成 | `bg/node_probe.go:submitViaQueueSource()` |

**已完成优化的整体效果**:
- ✅ 恢复循环不再被探测提交阻塞
- ✅ 探测任务提交失败会重试，不再静默丢失
- ✅ 退避阶梯机制正常工作，探测会实际执行
- ✅ 并发控制严谨，系统资源使用合理
- ⚠️ 仍存在 20-30% 场景需要手工干预 (设计限制)

---

## 二、待完善的 P0.3 修复 (高优先级) 🔧

### 问题描述

当前 `RestoreOnSuccess` 调用 `modelbinding.ResolveRawBinding()` 处理模型绑定解析，但在遇到歧义时仍然**抛出错误**而非选择第一个候选：

**当前行为**:
```go
// modelbinding/resolver.go:30-32
if len(exact) > 1 {
    return "", &ambiguousError{candidates: exact}  // ❌ 抛出错误
}
```

**日志证据** (2026-09-02):
```log
WARN execution_recorder: RestoreOnSuccess failed 
    error="resolve model binding failed: ambiguous model binding 
    (context: candidate_raw_models=MiniMax-M2.7,MiniMax-M2.7-highspeed)" 
    credential_id=42 model=minimax-m2.7
```

### 建议的修复方案

#### 方案 A: 在 ResolveRawBinding 中选择第一个候选 (推荐)

**位置**: `modelbinding/resolver.go`

```go
// 修改前
if len(exact) > 1 {
    return "", &ambiguousError{candidates: exact}
}

// 修改后
if len(exact) > 1 {
    slog.Warn("modelbinding: ambiguous model binding, using first candidate",
        "credential_id", credentialID,
        "requested_model", requestedModel,
        "candidates", exact)
    return exact[0], nil  // ✅ 选择第一个候选
}

// 同样修改 line 46-48 的 default case
default:
    slog.Warn("modelbinding: ambiguous model binding after normalization, using first candidate",
        "credential_id", credentialID,
        "requested_model", requestedModel,
        "normalized", modelname.NormalizeRouteKeyAliases(requestedModel),
        "candidates", candidates)
    return candidates[0], nil  // ✅ 选择第一个候选
}
```

**优点**:
- ✅ 简单直接，单点修复
- ✅ 所有调用 `ResolveRawBinding` 的地方都受益
- ✅ 保留警告日志便于后续优化

**缺点**:
- ⚠️ 可能选错模型 (如果 `MiniMax-M2.7` 和 `MiniMax-M2.7-highspeed` 的顺序在数据库中不确定)

#### 方案 B: 在 RestoreOnSuccess 中捕获错误并降级处理

**位置**: `domains/credential/writer.go`

```go
// line 117-120 修改
rawModel, err = modelbinding.ResolveRawBinding(ctx, tx, credentialID, rawModel)
if err != nil {
    var ambigErr *modelbinding.AmbiguousError
    if errors.As(err, &ambigErr) {
        // 歧义错误：记录警告，使用候选列表的第一个
        candidates := ambigErr.Candidates()
        if len(candidates) > 0 {
            slog.Warn("RestoreOnSuccess: ambiguous model binding, using first candidate",
                "credential_id", credentialID,
                "requested_model", rawModel,
                "candidates", candidates)
            rawModel = candidates[0]
        } else {
            return err  // 无候选，仍然失败
        }
    } else {
        return err  // 其他错误，直接返回
    }
}
```

**需要暴露的方法** (在 `modelbinding/resolver.go`):
```go
// AmbiguousCandidates 已存在，但需要导出 ambiguousError 类型或提供 Candidates() 方法
func (e *ambiguousError) Candidates() []string {
    return e.candidates
}

// 将 ambiguousError 导出为 AmbiguousError
type AmbiguousError = ambiguousError
```

**优点**:
- ✅ 只影响 `RestoreOnSuccess` 路径
- ✅ 其他调用者仍然会看到错误 (如 Admin API)
- ✅ 可以添加更复杂的选择逻辑 (如优先选择无后缀的模型)

**缺点**:
- ⚠️ 需要修改多个地方
- ⚠️ 其他调用者 (如路由选择) 仍会遇到歧义错误

#### 方案 C: 数据修复 - 消除歧义的根源 (长期方案)

**诊断 SQL**:
```sql
-- 查找所有存在歧义的凭据
SELECT 
    c.id AS credential_id,
    c.label,
    COUNT(DISTINCT pm.raw_model_name) AS binding_count,
    STRING_AGG(DISTINCT pm.raw_model_name, ', ') AS ambiguous_models
FROM credentials c
JOIN credential_model_bindings cmb ON c.id = cmb.credential_id
JOIN provider_models pm ON cmb.provider_model_id = pm.id
GROUP BY c.id, c.label
HAVING COUNT(DISTINCT pm.raw_model_name) > 
    COUNT(DISTINCT CASE 
        WHEN pm.standardized_name IS NOT NULL 
        THEN pm.standardized_name 
        ELSE pm.raw_model_name 
    END);
```

**修复方案**:
1. 识别歧义的凭据 (如 credential_id=42 有 `MiniMax-M2.7` 和 `MiniMax-M2.7-highspeed`)
2. 确定哪个模型是"主要"模型 (如查看请求日志，哪个使用频率更高)
3. 禁用或删除非主要模型的绑定
4. 或者使用 `admin_protected` 字段标记主要模型

**优点**:
- ✅ 从根源消除歧义
- ✅ 后续不会再遇到问题

**缺点**:
- ⚠️ 需要人工判断哪个模型是主要模型
- ⚠️ 可能需要逐个凭据处理

### 推荐实施顺序

1. **短期 (本周)**: 实施**方案 A**，快速解决热路径恢复失败
2. **中期 (下周)**: 实施**方案 C**，修复 credential_id=42 等已知歧义
3. **长期 (下月)**: 增强**方案 B** 的选择逻辑，如优先选择无后缀、使用频率高的模型

---

## 三、P2 中优先级优化 (建议实施) 📋

### P2.1 缩短探测时序窗口

**当前问题**: 探测提交到状态生效存在 10-30秒 延迟

**优化方案**:

#### P2.1.1 优先级队列

**目标**: 凭据恢复触发的探测使用更高优先级，优先执行

**实施**:
```go
// bg/credential_recovery.go 修改探测提交
func (r *CredentialRecovery) recoverExpiredBindings(ctx context.Context) error {
    // ...
    for _, p := range seen {
        pair := p
        r.dispatchProbe(func() {
            // 使用高优先级 source
            r.probeSubmitterWithPriority(pair.credID, pair.model, "recovery_expired", 90)
        })
    }
}

// bg/node_probe.go 增加优先级参数
func (w *NodeProbeWorker) submitViaQueueSource(..., priority int) (bool, error) {
    task := ProbeQueueTask{
        // ...
        Priority: priority,  // 恢复触发的探测优先级更高
        // ...
    }
}
```

**预期效果**: 恢复触发的探测延迟从 10-30秒 缩短到 <5秒

#### P2.1.2 快速通道 - 跳过队列直接执行

**目标**: 对于 `periodic` 和 `recovery_*` source，不经过队列直接执行探测

**实施**:
```go
// bg/node_probe.go 增加快速通道
func (w *NodeProbeWorker) submitViaQueueSource(..., source string) (bool, error) {
    // 快速通道条件
    if source == "periodic" || strings.HasPrefix(source, "recovery_") {
        if w.fastLaneExecutor != nil {
            // 直接执行，不入队
            go w.fastLaneExecutor.Execute(credID, model, tenantID, source)
            return true, nil
        }
    }
    // 正常入队
    return w.enqueue(ctx, task)
}
```

**风险**: 可能增加系统负载，需要限制快速通道的并发度

**预期效果**: 恢复触发的探测延迟从 10-30秒 缩短到 <3秒

#### P2.1.3 预热机制 - 提前探测即将到期的窗口

**目标**: 在周期性配额窗口到期前 5 分钟提前探测

**实施**:
```go
// bg/periodic_quota_probe.go 增加预热扫描
func (p *PeriodicQuotaProbe) scanUpcomingExpirations(ctx context.Context) {
    // 查询 quota_recover_at 在 now+5min 以内的凭据
    rows := p.db.Query(ctx, `
        SELECT id, default_probe_model
        FROM credentials
        WHERE quota_state = 'periodic_exhausted'
          AND quota_recover_at IS NOT NULL
          AND quota_recover_at BETWEEN now() AND now() + INTERVAL '5 minutes'
    `)
    // 提交预热探测
    for rows.Next() {
        p.probeSubmitter(credID, model)
    }
}
```

**预期效果**: 窗口到期后立即恢复，无延迟

### P2.2 状态一致性监控

**目标**: 实时监控节点状态不一致情况，快速发现问题

**实施**:

#### P2.2.1 Prometheus 指标

```go
// metrics/credential_state_metrics.go (新文件)
package metrics

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

var (
    CredentialStateInconsistency = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "llmgw_credential_state_inconsistency_total",
            Help: "Number of credentials with inconsistent state",
        },
        []string{"inconsistency_type"},
    )
    
    NodeRecoveryLag = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "llmgw_node_recovery_lag_seconds",
            Help: "Time from unavailable_recover_at to actual recovery",
            Buckets: []float64{1, 5, 10, 30, 60, 300, 600, 1800, 3600},
        },
        []string{"recovery_path"},
    )
)
```

#### P2.2.2 周期性扫描 (每 5 分钟)

```go
// bg/state_consistency_checker.go (新文件)
func (c *StateConsistencyChecker) scanInconsistencies(ctx context.Context) {
    // 检查类型 1: unavailable_recover_at 已过期但仍不可用
    expired := c.countExpiredButUnavailable(ctx)
    CredentialStateInconsistency.WithLabelValues("expired_not_recovered").Set(float64(expired))
    
    // 检查类型 2: credentials.availability_state='ready' 但所有 cmb 都是 FALSE
    credReady := c.countCredentialReadyAllBindingsDown(ctx)
    CredentialStateInconsistency.WithLabelValues("cred_ready_all_bindings_down").Set(float64(credReady))
    
    // 检查类型 3: model_offers 与 cmb 不一致
    offerMismatch := c.countOfferBindingMismatch(ctx)
    CredentialStateInconsistency.WithLabelValues("offer_binding_mismatch").Set(float64(offerMismatch))
}
```

#### P2.2.3 Grafana 告警规则

```yaml
groups:
  - name: llm-gateway-credential-state
    interval: 1m
    rules:
      - alert: CredentialStateInconsistencyHigh
        expr: llmgw_credential_state_inconsistency_total > 10
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High credential state inconsistency detected"
          description: "{{ $labels.inconsistency_type }}: {{ $value }} credentials have inconsistent state"
      
      - alert: NodeRecoveryLagHigh
        expr: histogram_quantile(0.95, llmgw_node_recovery_lag_seconds_bucket) > 300
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Node recovery lag is high"
          description: "95th percentile recovery lag: {{ $value }}s"
```

**预期效果**: 
- 运维团队能实时看到状态不一致的节点数量
- 当不一致数量超过阈值时自动告警
- 可追踪恢复延迟分布

### P2.3 防御性日志增强

**当前问题**: `probeSubmitter == nil` 时静默跳过，无告警

**实施**:
```go
// bg/credential_recovery.go:1096, 1215, 1370
func (r *CredentialRecovery) recoverExpiredBindings(ctx context.Context) error {
    if r.probeSubmitter == nil {
        // 修改前: return nil (静默跳过)
        // 修改后: 记录 ERROR 级别日志
        slog.Error("credential_recovery: probeSubmitter not wired, cannot recover expired bindings",
            "stack_trace", string(debug.Stack()))
        met.RoutingCredentialRecoveryErrors.WithLabelValues("probe_submitter_nil").Inc()
        return fmt.Errorf("probeSubmitter not initialized")
    }
    // ...
}
```

**同样修改**:
- `recoverFreshDegradedBindings` (line 1215)
- `reconcileStaleNodeProbeStates` (line 1370)

**预期效果**: 
- 当钩子未就绪时立即发现问题
- 避免静默失败导致节点无法恢复

---

## 四、P3 低优先级优化 (未来考虑) 💡

### P3.1 统一状态更新服务

**目标**: 协调热路径与冷路径的状态更新，避免数据竞争

**当前评估**: 
- 暂无明显数据竞争问题
- 热路径和冷路径的更新频率不高
- 当前设计已足够健壮

**建议**: 暂不实施，优先级低

### P3.2 探测生命周期追踪表

**目标**: 追踪单个探测从提交到完成的完整过程

**当前评估**:
- 当前日志和指标已足够调试
- 追踪表会产生大量数据
- 成本效益比不高

**建议**: 暂不实施，除非遇到难以调试的问题

### P3.3 机器学习预测

**目标**: 根据历史数据预测节点故障，提前探测

**当前评估**:
- 需要大量历史数据
- 复杂度高，收益不明确
- 当前退避阶梯已能满足需求

**建议**: 暂不实施，留待未来研究

---

## 五、实施计划

### 第一阶段 (本周) - P0.3 修复 + 验证

| 任务 | 负责人 | 预计工时 | 依赖 |
|------|--------|---------|------|
| P0.3 实施方案 A - ResolveRawBinding 选择第一个候选 | - | 2h | - |
| 编写单元测试 - 覆盖歧义场景 | - | 2h | P0.3 |
| 本地验证 - 重现 credential_id=42 歧义问题 | - | 1h | P0.3 |
| 代码审查与合并 | - | 1h | 测试通过 |
| 监控 P1 优化效果 - 观察 7 天 | - | - | 部署 |

**验证标准**:
- ✅ 不再出现 `ambiguous model binding` 错误
- ✅ credential_id=42 的请求成功后节点状态正常恢复
- ✅ 日志中有 "using first candidate" 警告

### 第二阶段 (下周) - P2.1/P2.2 实施

| 任务 | 负责人 | 预计工时 | 依赖 |
|------|--------|---------|------|
| P2.1.1 优先级队列 - 设计与实现 | - | 4h | - |
| P2.2.1 Prometheus 指标 - 实现 | - | 3h | - |
| P2.2.2 状态一致性扫描 - 实现 | - | 4h | P2.2.1 |
| P2.2.3 Grafana 面板与告警 - 配置 | - | 2h | P2.2.2 |
| P2.3 防御性日志增强 | - | 1h | - |
| 集成测试 | - | 3h | 所有功能 |

**验证标准**:
- ✅ 恢复触发的探测延迟 < 5秒
- ✅ Grafana 面板显示状态不一致数量
- ✅ 测试告警规则正常触发

### 第三阶段 (下月) - 长期监控与优化

| 任务 | 负责人 | 预计工时 | 依赖 |
|------|--------|---------|------|
| 方案 C 数据修复 - 消除已知歧义 | - | 4h | - |
| P2.1.2 快速通道 - 评估与设计 | - | 4h | P2.1.1 效果 |
| P2.1.3 预热机制 - 实现 | - | 3h | - |
| 性能压测 - 验证系统在高负载下的表现 | - | 6h | - |
| 文档更新 - 运维手册、故障排查指南 | - | 4h | - |

**验证标准**:
- ✅ 手工干预频率降低到 10-15%
- ✅ 无已知的模型绑定歧义
- ✅ 系统在高负载下表现稳定

---

## 六、风险评估

### 高风险项

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| P2.1.2 快速通道增加系统负载 | 中 | 高 | 限制并发度，灰度发布，监控 CPU/内存 |
| P0.3 选择错误的候选模型 | 低 | 中 | 记录警告日志，方案 C 数据修复 |

### 中风险项

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| P2.2 监控扫描影响数据库性能 | 低 | 中 | 5分钟间隔，使用只读副本 |
| P2.1.1 优先级队列导致低优先级任务饥饿 | 低 | 低 | 实施公平调度，限制高优先级任务占比 |

### 低风险项

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| P2.3 防御性日志增加日志量 | 低 | 低 | 只记录异常情况，使用 ERROR 级别 |

---

## 七、成功指标

### 量化指标

| 指标 | 当前基线 | 目标 | 测量方式 |
|------|---------|------|---------|
| 手工干预频率 | ~30% | <15% | 统计 force_enable 操作次数 / 总恢复次数 |
| RestoreOnSuccess 成功率 | ~95% (歧义导致 5% 失败) | >99% | `llmgw_restore_on_success_total{result="success"}` |
| 探测提交成功率 | ~97% | >99% | `llmgw_node_probe_queue_submission_total{outcome="success"}` |
| 恢复延迟 P95 | ~30秒 | <10秒 | `llmgw_node_recovery_lag_seconds` |
| 状态不一致数量 | 未知 | <5 | `llmgw_credential_state_inconsistency_total` |

### 质量指标

- ✅ 无已知的模型绑定歧义
- ✅ 无静默失败 (所有错误都有日志或告警)
- ✅ 所有优化都有单元测试覆盖
- ✅ 代码审查通过，符合项目规范
- ✅ 文档完整，包含故障排查指南

### 用户体验指标

- ✅ 凭据恢复后，用户请求成功率立即提升 (无需等待)
- ✅ 运维团队反馈：手工干预操作显著减少
- ✅ 无新增的性能问题或系统不稳定

---

## 八、后续行动检查清单

### 本周必做

- [ ] 实施 P0.3 方案 A (ResolveRawBinding 选择第一个候选)
- [ ] 编写单元测试，覆盖歧义场景
- [ ] 本地验证，重现并修复 credential_id=42 问题
- [ ] 监控 P1 优化效果 (观察 7 天)
- [ ] 记录任何异常或意外行为

### 下周准备

- [ ] 设计 P2.1.1 优先级队列方案
- [ ] 实现 P2.2 状态一致性监控
- [ ] 配置 Grafana 面板和告警规则
- [ ] 增强防御性日志 (P2.3)

### 持续监控

- [ ] 每日检查 `llmgw_node_probe_queue_submission_total{outcome="failed"}` 指标
- [ ] 每日检查 `RestoreOnSuccess failed` 日志
- [ ] 每周统计手工干预次数
- [ ] 每周审查状态不一致告警

---

**文档版本**: v1.0  
**最后更新**: 2026-09-06  
**下次审查**: 2026-09-13 (一周后)
