# 节点状态同步优化实施任务 - Handoff Prompt

## 会话目标

在前期深度分析的基础上，实施架构优化方案以解决节点状态同步滞后问题。本会话聚焦于 P1 高优先级修复和 P2 中优先级架构改进。

## 背景概述

### 核心问题回顾

系统存在**节点状态同步滞后**问题：供应商凭据恢复后，系统节点状态未能及时更新，需要手动干预。

经过深度分析，识别出三大核心问题：

1. **探测调度系统设计缺陷**
   - `next_retry_at` 字段被无条件更新，导致探测从未真正提交到队列
   - 退避阶梯机制失效：失败后延迟递增，但探测永远不会实际执行
   - 代码位置：`bg/node_probe.go:200-225`

2. **同步探测提交阻塞恢复循环**
   - 30秒恢复定时器被探测提交操作同步阻塞
   - 探测提交失败时静默忽略错误，无重试机制
   - 代码位置：`bg/credential_recovery.go:153-156`

3. **热路径恢复与定时恢复数据竞争**
   - `RestoreOnSuccess`（热路径）在请求线程中执行
   - `credential_recovery`（冷路径）在后台30秒循环中执行
   - 两者同时更新同一数据库字段，无协调机制

### 已实施的 P0 修复

1. **SQL 数据清理**：修复历史 `unavailable_recover_at` NULL 值
2. **放宽 `broken_confirmed` 守卫**：允许更多场景下的恢复尝试
3. **修复模型绑定歧义**：解决 `RestoreOnSuccess` 失败问题

详细分析文档：
- `ANALYSIS_NODE_STATE_SYNC_GAP_20260902.md` - 根因分析（6个根因）
- `NODE_STATE_SYNC_GAP_FINAL_AUDIT_20260902.md` - 最终审计报告
- `PATCH_unavailable_recover_at_cleanup.sql` - 数据修复脚本

## 本次实施任务

### P1 高优先级任务（必须完成）

#### 1. 异步化探测提交，解除恢复循环阻塞

**问题描述**：
- 当前 `bg/credential_recovery.go:153-156` 中探测提交是同步调用
- 阻塞 30秒恢复定时器，影响其他节点的恢复处理
- 提交失败时没有重试机制，错误被静默忽略

**实施要求**：
```go
// 当前代码（同步，有问题）
if probeErr := b.probeSubmitter.SubmitNodeProbe(ctx, nodeID, ...); probeErr != nil {
    log.WithError(probeErr).Warn("probe submission failed")
    // 错误被忽略，无重试
}
```

**目标实现**：
- 将探测提交改为异步操作（使用 goroutine 或专用任务队列）
- 确保提交操作不阻塞恢复循环的主线程
- 添加并发控制，避免过多 goroutine（如使用 worker pool）
- 保留必要的日志记录，便于问题追踪

**验证标准**：
- 恢复循环不再因探测提交而阻塞
- 通过日志确认异步提交正常工作
- 系统负载无明显增加

---

#### 2. 修复无条件 holdoff 更新，确保探测实际执行

**问题描述**：
- `bg/node_probe.go:200-225` 中 `next_retry_at` 被无条件更新
- 即使探测未提交到队列，`next_retry_at` 也会被推迟
- 导致退避阶梯机制失效，探测永远不会真正执行

**当前代码逻辑**：
```go
func (p *NodeProbeScheduler) scheduleNextProbe(ctx context.Context, nodeID int64, ...) {
    // 计算下次重试时间
    nextRetry := time.Now().Add(delay)
    
    // 问题：无论探测是否提交成功，都更新 next_retry_at
    _, err := p.db.ExecContext(ctx, 
        "UPDATE node_probe_state SET next_retry_at = $1 WHERE node_id = $2",
        nextRetry, nodeID)
    
    // 但探测可能根本没有提交！
}
```

**实施要求**：
- **只有在探测实际提交到队列后**才更新 `next_retry_at`
- 如果探测提交失败，保持旧的 `next_retry_at` 值
- 在下一轮恢复循环中重新尝试提交

**建议实现方案**：
```go
// 方案1：条件更新
func (p *NodeProbeScheduler) scheduleNextProbe(ctx context.Context, nodeID int64, ...) error {
    // 先尝试提交探测
    submitErr := p.submitProbe(ctx, nodeID, ...)
    
    if submitErr != nil {
        // 提交失败，不更新 next_retry_at，保持原值
        log.WithError(submitErr).Warn("probe submission failed, will retry in next cycle")
        return submitErr
    }
    
    // 提交成功，才更新 next_retry_at
    nextRetry := time.Now().Add(delay)
    _, err := p.db.ExecContext(ctx, 
        "UPDATE node_probe_state SET next_retry_at = $1 WHERE node_id = $2",
        nextRetry, nodeID)
    return err
}

// 方案2：原子性更新
// 在探测提交成功后的回调中更新 next_retry_at
```

**验证标准**：
- 探测提交失败时，`next_retry_at` 不被更新
- 在下一轮恢复循环中，相同节点会被重新处理
- 退避阶梯机制正常工作（失败次数增加时延迟递增）

---

#### 3. 增强探测提交失败时的重试和告警机制

**问题描述**：
- 当前探测提交失败时仅记录一条 Warn 日志
- 没有重试机制，失败的探测被永久遗漏
- 没有告警通知，运维团队无法感知问题

**实施要求**：

**3.1 立即重试机制**
- 探测提交失败时，进行有限次数的立即重试（建议 2-3 次）
- 使用指数退避策略（如 100ms, 200ms, 400ms）
- 记录每次重试的结果

**3.2 持久化失败记录**
- 将持续失败的探测提交记录到专门的失败表或日志
- 包含失败时间、节点ID、失败原因、重试次数等信息
- 便于后续人工介入和问题分析

**3.3 告警机制**
- 当探测提交连续失败超过阈值时（如 5次），触发告警
- 告警应包含：节点ID、失败次数、最近一次失败原因
- 集成到现有的监控告警系统（如 Prometheus、Alertmanager）

**建议实现方案**：
```go
// 带重试的探测提交
func (p *ProbeSubmitter) SubmitWithRetry(ctx context.Context, nodeID int64, maxRetries int) error {
    var lastErr error
    backoff := 100 * time.Millisecond
    
    for attempt := 0; attempt <= maxRetries; attempt++ {
        if attempt > 0 {
            time.Sleep(backoff)
            backoff *= 2
        }
        
        err := p.SubmitNodeProbe(ctx, nodeID, ...)
        if err == nil {
            if attempt > 0 {
                log.WithField("attempt", attempt).Info("probe submission succeeded after retry")
            }
            return nil
        }
        
        lastErr = err
        log.WithError(err).WithField("attempt", attempt).Warn("probe submission failed")
    }
    
    // 所有重试都失败，记录持久化失败并考虑告警
    p.recordPersistentFailure(ctx, nodeID, lastErr)
    p.maybeAlert(ctx, nodeID, maxRetries, lastErr)
    
    return lastErr
}

// 告警逻辑
func (p *ProbeSubmitter) maybeAlert(ctx context.Context, nodeID int64, failureCount int, err error) {
    // 检查最近失败次数
    recentFailures := p.getRecentFailureCount(nodeID)
    
    if recentFailures >= ALERT_THRESHOLD {
        alert := Alert{
            Level:   "warning",
            Message: fmt.Sprintf("Node %d probe submission failed %d times", nodeID, recentFailures),
            Labels:  map[string]string{"node_id": fmt.Sprint(nodeID)},
        }
        p.alertManager.Send(alert)
    }
}
```

**验证标准**：
- 临时网络抖动导致的探测提交失败能够通过重试恢复
- 持续失败的探测提交会被记录到持久化存储
- 失败次数超过阈值时触发告警
- 运维团队能够通过监控面板看到探测提交失败的趋势

---

### P2 中优先级任务（优化改进）

#### 4. 协调热路径与冷路径的状态更新

**问题描述**：
- `RestoreOnSuccess`（热路径）：在成功请求后立即恢复节点状态
- `credential_recovery`（冷路径）：后台30秒循环定期检查和恢复
- 两者同时更新 `available`、`unavailable_since` 等字段，可能产生数据竞争

**实施要求**：

**4.1 建立更新优先级**
- 热路径优先：成功请求后的立即恢复优先级最高
- 冷路径检查：在更新前检查最近是否有热路径更新
- 避免冷路径覆盖热路径的新鲜数据

**4.2 添加更新时间戳**
- 为状态字段添加 `last_updated_at` 时间戳
- 更新时使用乐观锁：`WHERE last_updated_at = $old_timestamp`
- 避免旧数据覆盖新数据

**4.3 统一更新接口**
- 创建统一的节点状态更新服务
- 热路径和冷路径都通过该服务更新状态
- 在服务内部处理并发控制和数据一致性

**建议实现方案**：
```go
type NodeStateUpdater struct {
    db *sql.DB
}

// 统一的状态更新接口
func (u *NodeStateUpdater) UpdateAvailability(
    ctx context.Context,
    nodeID int64,
    available bool,
    source string, // "hot_path" 或 "cold_path"
    expectedLastUpdate *time.Time, // 乐观锁
) error {
    now := time.Now()
    
    query := `
        UPDATE credential_bindings 
        SET available = $1, 
            unavailable_since = CASE WHEN $1 THEN NULL ELSE COALESCE(unavailable_since, $2) END,
            last_state_updated_at = $2,
            last_state_update_source = $3
        WHERE id = $4
    `
    
    if expectedLastUpdate != nil {
        query += " AND last_state_updated_at = $5"
    }
    
    result, err := u.db.ExecContext(ctx, query, available, now, source, nodeID, expectedLastUpdate)
    if err != nil {
        return err
    }
    
    rowsAffected, _ := result.RowsAffected()
    if rowsAffected == 0 && expectedLastUpdate != nil {
        return ErrOptimisticLockFailed
    }
    
    return nil
}
```

**验证标准**：
- 热路径和冷路径的更新不会相互覆盖
- 通过日志或监控能看到更新来源（hot_path vs cold_path）
- 没有数据竞争或状态不一致问题

---

#### 5. 实现统一的节点状态观测点

**问题描述**：
- 当前节点状态分散在多个表和字段中
- 难以快速了解某个节点的完整状态
- 问题排查时需要查询多个表并手动关联

**实施要求**：

**5.1 创建节点状态视图**
```sql
CREATE VIEW node_health_status AS
SELECT 
    cb.id AS binding_id,
    cb.credential_id,
    cb.model_id,
    cb.available,
    cb.unavailable_since,
    cb.unavailable_recover_at,
    cb.broken_confirmed,
    nps.next_retry_at,
    nps.consecutive_failures,
    nps.last_probe_at,
    nps.last_probe_result,
    c.provider,
    c.tenant_key,
    m.model_name,
    CASE 
        WHEN cb.available THEN 'healthy'
        WHEN cb.broken_confirmed THEN 'broken'
        WHEN nps.consecutive_failures > 3 THEN 'degraded'
        ELSE 'recovering'
    END AS health_status
FROM credential_bindings cb
LEFT JOIN node_probe_state nps ON cb.id = nps.node_id
LEFT JOIN credentials c ON cb.credential_id = c.id
LEFT JOIN models m ON cb.model_id = m.id;
```

**5.2 提供查询API**
```go
type NodeHealthStatus struct {
    BindingID           int64
    CredentialID        int64
    ModelID             int64
    Available           bool
    UnavailableSince    *time.Time
    UnavailableRecoverAt *time.Time
    BrokenConfirmed     bool
    NextRetryAt         *time.Time
    ConsecutiveFailures int
    LastProbeAt         *time.Time
    LastProbeResult     string
    Provider            string
    TenantKey           string
    ModelName           string
    HealthStatus        string // "healthy", "broken", "degraded", "recovering"
}

func (s *NodeHealthService) GetNodeStatus(ctx context.Context, nodeID int64) (*NodeHealthStatus, error) {
    // 从视图查询完整状态
}

func (s *NodeHealthService) ListUnhealthyNodes(ctx context.Context) ([]*NodeHealthStatus, error) {
    // 查询所有非健康节点
}
```

**5.3 监控指标导出**
- 导出 Prometheus 指标：
  - `node_health_status{status="healthy|broken|degraded|recovering"}` - 各状态节点数量
  - `node_consecutive_failures` - 连续失败次数分布
  - `node_recovery_lag_seconds` - 恢复延迟时间

**验证标准**：
- 能够通过单一查询获取节点的完整健康状态
- 监控面板能够实时显示节点健康度分布
- 问题排查时间显著缩短

---

#### 6. 建立完整的探测生命周期追踪

**问题描述**：
- 当前探测从提交到执行再到结果处理的过程不透明
- 难以追踪单个探测的完整生命周期
- 问题排查时无法确定探测在哪个环节卡住或失败

**实施要求**：

**6.1 探测生命周期阶段**
定义探测的标准生命周期：
1. **SUBMITTED** - 探测已提交到队列
2. **SCHEDULED** - 探测已被调度，等待执行
3. **EXECUTING** - 探测正在执行中
4. **COMPLETED** - 探测执行完成（成功或失败）
5. **PROCESSED** - 探测结果已处理，状态已更新

**6.2 探测追踪表**
```sql
CREATE TABLE probe_lifecycle_events (
    id BIGSERIAL PRIMARY KEY,
    probe_id VARCHAR(64) NOT NULL,
    node_id BIGINT NOT NULL,
    stage VARCHAR(32) NOT NULL, -- SUBMITTED, SCHEDULED, EXECUTING, COMPLETED, PROCESSED
    timestamp TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    details JSONB, -- 阶段特定的详细信息
    error_message TEXT,
    duration_ms INT, -- 从上一阶段到当前阶段的耗时
    INDEX idx_probe_id (probe_id),
    INDEX idx_node_id_timestamp (node_id, timestamp DESC)
);
```

**6.3 追踪代码埋点**
```go
type ProbeTracker struct {
    db *sql.DB
}

func (t *ProbeTracker) TrackStage(
    ctx context.Context,
    probeID string,
    nodeID int64,
    stage ProbeStage,
    details map[string]interface{},
    err error,
) error {
    detailsJSON, _ := json.Marshal(details)
    errMsg := ""
    if err != nil {
        errMsg = err.Error()
    }
    
    _, dbErr := t.db.ExecContext(ctx, `
        INSERT INTO probe_lifecycle_events 
        (probe_id, node_id, stage, details, error_message, duration_ms)
        VALUES ($1, $2, $3, $4, $5, 
            (SELECT EXTRACT(EPOCH FROM (NOW() - MAX(timestamp))) * 1000 
             FROM probe_lifecycle_events 
             WHERE probe_id = $1))
    `, probeID, nodeID, stage, detailsJSON, errMsg)
    
    return dbErr
}

// 在关键点埋点
func (p *ProbeSubmitter) SubmitNodeProbe(ctx context.Context, nodeID int64, ...) error {
    probeID := generateProbeID()
    
    // 阶段1: 提交
    p.tracker.TrackStage(ctx, probeID, nodeID, SUBMITTED, nil, nil)
    
    err := p.queueClient.Enqueue(probeID, nodeID, ...)
    if err != nil {
        p.tracker.TrackStage(ctx, probeID, nodeID, SUBMITTED, nil, err)
        return err
    }
    
    // 阶段2: 调度
    p.tracker.TrackStage(ctx, probeID, nodeID, SCHEDULED, nil, nil)
    
    return nil
}
```

**6.4 追踪查询API**
```go
func (t *ProbeTracker) GetProbeLifecycle(ctx context.Context, probeID string) ([]*ProbeLifecycleEvent, error) {
    // 返回探测的完整生命周期事件序列
}

func (t *ProbeTracker) GetNodeProbeHistory(ctx context.Context, nodeID int64, limit int) ([]*ProbeLifecycleEvent, error) {
    // 返回节点最近的探测历史
}

func (t *ProbeTracker) GetStuckProbes(ctx context.Context, stageTimeout time.Duration) ([]*ProbeInfo, error) {
    // 查找卡在某个阶段超过指定时间的探测
}
```

**6.5 可视化和告警**
- 提供探测生命周期可视化（如 Grafana 面板）
- 显示每个阶段的平均耗时和成功率
- 对卡住的探测（某阶段停留时间过长）进行告警

**验证标准**：
- 能够追踪单个探测从提交到完成的完整过程
- 能够识别探测在哪个阶段卡住或失败
- 能够统计各阶段的耗时分布和成功率
- 运维团队能够快速定位探测系统的瓶颈

---

## 实施顺序建议

1. **第一步：P1.2** - 修复无条件 holdoff 更新（最关键，解决探测不执行的根本问题）
2. **第二步：P1.1** - 异步化探测提交（解除阻塞，提升并发性能）
3. **第三步：P1.3** - 增强重试和告警（提升可靠性和可观测性）
4. **第四步：P2.6** - 建立探测生命周期追踪（为后续优化提供数据支持）
5. **第五步：P2.5** - 实现节点状态观测点（统一监控视图）
6. **第六步：P2.4** - 协调热路径与冷路径（最复杂，需要前面的基础）

## 验证和测试要求

### 单元测试
- 每个修改的函数都需要相应的单元测试
- 特别关注边界条件和错误处理

### 集成测试
- 模拟凭据失效和恢复的完整流程
- 验证探测提交、调度、执行的完整链路
- 测试热路径和冷路径的并发场景

### 本地验证
- 使用本地部署环境测试完整流程
- 检查日志输出，确认各阶段按预期工作
- 验证数据库状态更新的正确性

### 性能测试
- 测试异步化后的系统吞吐量
- 验证并发探测提交对系统负载的影响
- 确保优化后性能无明显下降

## 关键文件清单

### 需要修改的核心文件
- `bg/credential_recovery.go` - 恢复循环主逻辑
- `bg/node_probe.go` - 探测调度逻辑
- `probe_service.go` - 探测执行逻辑
- `domains/credential/writer.go` - RestoreOnSuccess 热路径

### 需要创建的新文件
- `bg/probe_tracker.go` - 探测生命周期追踪
- `services/node_health_service.go` - 节点健康状态服务
- `services/node_state_updater.go` - 统一状态更新服务

### SQL 迁移文件
- `sql/migrations/domain/XXX_add_probe_lifecycle_tracking.sql`
- `sql/migrations/domain/XXX_add_node_state_update_tracking.sql`
- `sql/migrations/domain/XXX_create_node_health_view.sql`

### 测试文件
- `bg/credential_recovery_test.go`
- `bg/node_probe_test.go`
- `bg/probe_tracker_test.go`
- `services/node_health_service_test.go`

## 成功标准

本次实施成功的标志：

1. **探测执行可靠性**
   - 探测提交成功率 > 99.9%
   - 探测从提交到执行的平均延迟 < 5秒
   - 退避阶梯机制正常工作

2. **恢复时效性**
   - 凭据恢复后，节点状态在 30秒内自动更新
   - 无需手动干预

3. **系统稳定性**
   - 恢复循环不被阻塞
   - 热路径和冷路径无数据竞争
   - 无内存泄漏或资源耗尽

4. **可观测性**
   - 能够追踪任意探测的完整生命周期
   - 能够实时查看所有节点的健康状态
   - 探测提交失败时有及时告警

5. **代码质量**
   - 所有修改都有对应的单元测试
   - 测试覆盖率 > 80%
   - 代码审查通过

## 注意事项

1. **向后兼容性**
   - 数据库 schema 变更需要支持平滑升级
   - 新旧代码需要能够共存一段时间

2. **性能影响**
   - 异步化后需要控制 goroutine 数量
   - 探测追踪的数据量可能较大，考虑定期清理

3. **错误处理**
   - 所有异步操作的错误都需要被捕获和记录
   - 关键错误需要触发告警

4. **可配置性**
   - 重试次数、超时时间等参数应该可配置
   - 便于不同环境使用不同的策略

5. **渐进式部署**
   - 优先在测试环境验证
   - 生产环境采用灰度发布
   - 保留快速回滚能力

## 参考文档

- `ANALYSIS_NODE_STATE_SYNC_GAP_20260902.md` - 根因分析报告
- `NODE_STATE_SYNC_GAP_FINAL_AUDIT_20260902.md` - 最终审计报告
- `PATCH_unavailable_recover_at_cleanup.sql` - P0修复SQL脚本

## 后续规划

完成 P1 和 P2 任务后，建议进行：
1. 性能压测，验证系统在高负载下的表现
2. 长期运行测试，观察探测追踪数据的增长情况
3. 收集实际生产数据，优化探测调度策略
4. 考虑引入机器学习模型，预测节点故障

---

**准备开始实施？请从 P1.2 "修复无条件 holdoff 更新" 开始！**
