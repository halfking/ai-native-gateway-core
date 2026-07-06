# 会话健康运维手册

> **版本**: v2.1.0  
> **更新日期**: 2026-07-06  
> **目标读者**: SRE、运维工程师、平台管理员

---

## 1. 概述

### 1.1 文档目的

本手册面向运维人员，提供会话健康系统的：
- 健康分计算逻辑详解
- 配置调优指南
- 监控告警设置
- 常见问题排查

### 1.2 健康评分体系架构

```
┌─────────────────────────────────────────────────┐
│              会话健康评分系统                      │
├─────────────────────────────────────────────────┤
│                                                 │
│  ┌──────────┐    ┌──────────┐    ┌──────────┐ │
│  │ 数据采集  │───▶│ 评分计算  │───▶│ 结果存储  │ │
│  └──────────┘    └──────────┘    └──────────┘ │
│       │               │                │        │
│       │               │                │        │
│  request_logs   Penalty Model   session_       │
│  session_       (100分起扣制)    summaries      │
│  summaries                                      │
│                                                 │
└─────────────────────────────────────────────────┘
```

**数据流**:
1. **采集**: 从 `request_logs` 和 `session_summaries` 读取原始数据
2. **计算**: 应用 Penalty Model，从 100 分开始逐项扣分
3. **存储**: 写入 `session_summaries` 的健康字段
4. **触发**: 后台 worker 或 API 触发

---

## 2. 健康分计算逻辑详解

### 2.1 Penalty Model (扣分模型)

**核心原则**: 从 100 分开始，根据异常指标逐项扣分

```go
// 伪代码
healthScore := 100
penalties := []Penalty{}

// 1. 错误扣分
if errorRate > 0 {
    points := min(30, errorRate * 100 / 2)  // 错误率每2%扣1分，最多扣30
    healthScore -= points
    penalties.append(Penalty{Category: "error", Points: points})
}

// 2. 延迟扣分
if avgLatency > latencyThreshold {
    points := min(20, (avgLatency - latencyThreshold) / 100)
    healthScore -= points
    penalties.append(Penalty{Category: "latency", Points: points})
}

// 3. 成本扣分
if avgCostPerRequest > costThreshold {
    points := min(15, (avgCost - costThreshold) / 0.1 * 5)
    healthScore -= points
    penalties.append(Penalty{Category: "cost", Points: points})
}

// 4. 合规扣分
if complianceIssues > 0 {
    points := min(30, complianceIssues * 10)
    healthScore -= points
    penalties.append(Penalty{Category: "compliance", Points: points})
}

// 5. 放弃扣分
if outcome == "abandoned" {
    healthScore -= 10
    penalties.append(Penalty{Category: "abandoned", Points: 10})
}

return max(0, healthScore)  // 最低0分
```

### 2.2 扣分规则表

| 类别 | 触发条件 | 计算公式 | 最大扣分 | 示例 |
|------|---------|---------|---------|------|
| **错误 (error)** | `error_count > 0` | `min(30, error_rate * 50)` | 30 | 错误率 10% → 扣 5 分 |
| **延迟 (latency)** | `avg_latency > threshold` | `min(20, (latency - threshold) / 100)` | 20 | 1200ms (阈值800ms) → 扣 4 分 |
| **成本 (cost)** | `avg_cost > threshold` | `min(15, (cost - threshold) / 0.1 * 5)` | 15 | $0.80 (阈值$0.50) → 扣 15 分 |
| **合规 (compliance)** | `compliance_issues > 0` | `min(30, issues * 10)` | 30 | 2 个 PII → 扣 20 分 |
| **放弃 (abandoned)** | `outcome = abandoned` | `10` (固定) | 10 | 用户取消 → 扣 10 分 |

### 2.3 等级划分算法

```go
func calculateGrade(score int) string {
    switch {
    case score >= 90:
        return "A"  // 优秀
    case score >= 80:
        return "B"  // 良好
    case score >= 70:
        return "C"  // 一般
    case score >= 60:
        return "D"  // 较差
    default:
        return "F"  // 失败
    }
}
```

### 2.4 结果分类 (Outcome)

**分类逻辑**:

```go
func determineOutcome(session SessionSummary) string {
    // 1. 错误终止
    if session.ErrorCount > 0 && session.ErrorRate > 0.5 {
        return "error"  // 超过一半请求失败
    }
    
    // 2. 用户放弃
    if session.DurationSeconds < 10 && session.RequestCount < 2 {
        return "abandoned"  // 极短会话，可能是放弃
    }
    
    // 3. 正常完成
    if session.SuccessCount > 0 && session.ErrorRate < 0.3 {
        return "completed"  // 大部分请求成功
    }
    
    // 4. 未知
    return "unknown"
}
```

### 2.5 计算时机

健康分在以下时机计算：

| 触发方式 | 时机 | 频率 | 实现 |
|---------|------|------|------|
| **实时计算** | API 查询时若未计算 | 按需 | `session_health_api.go::getOrComputeHealth` |
| **后台 worker** | 会话结束后 5 分钟 | 批量 | 未实现（待 T1.5） |
| **手动触发** | 管理员点击"重算" | 按需 | `POST /api/admin/sessions/{id}/recompute-health` |

---

## 3. 配置调优指南

### 3.1 HealthScoreConfig 结构

```go
type HealthScoreConfig struct {
    // 延迟阈值（毫秒）
    LatencyThresholdMs int `json:"latency_threshold_ms"`
    
    // 成本阈值（美元/请求）
    CostThresholdUSD float64 `json:"cost_threshold_usd"`
    
    // 错误率容忍度（0-1）
    ErrorRateTolerance float64 `json:"error_rate_tolerance"`
    
    // 合规问题权重
    ComplianceWeight int `json:"compliance_weight"`
    
    // 是否启用成本扣分
    EnableCostPenalty bool `json:"enable_cost_penalty"`
}
```

### 3.2 默认配置

```go
func DefaultHealthScoreConfig() HealthScoreConfig {
    return HealthScoreConfig{
        LatencyThresholdMs:  800,     // 800ms
        CostThresholdUSD:    0.50,    // $0.50/请求
        ErrorRateTolerance:  0.10,    // 10% 错误率以内不扣分
        ComplianceWeight:    10,      // 每个合规问题扣 10 分
        EnableCostPenalty:   true,    // 启用成本扣分
    }
}
```

### 3.3 如何调整阈值

**场景 1: 降低延迟敏感度**

如果延迟扣分过于严格（例如系统整体延迟偏高），可以提高阈值：

```go
// config/health_score.go
config := HealthScoreConfig{
    LatencyThresholdMs: 1200,  // 从 800ms 提高到 1200ms
}
```

**场景 2: 提高成本阈值**

如果使用的模型成本普遍较高（如 `gpt-4o`），调整成本阈值避免误扣分：

```go
config := HealthScoreConfig{
    CostThresholdUSD: 1.00,  // 从 $0.50 提高到 $1.00
}
```

**场景 3: 禁用成本扣分**

某些租户不关注成本，只关注质量：

```go
config := HealthScoreConfig{
    EnableCostPenalty: false,  // 禁用成本扣分
}
```

**场景 4: 按租户差异化配置**

```go
func GetHealthScoreConfig(tenantID string) HealthScoreConfig {
    if tenantID == "enterprise_customer_001" {
        // 企业客户：更严格的健康标准
        return HealthScoreConfig{
            LatencyThresholdMs: 500,
            CostThresholdUSD:   0.30,
            ErrorRateTolerance: 0.05,
        }
    }
    
    // 默认配置
    return DefaultHealthScoreConfig()
}
```

### 3.4 配置热更新

**方法 1: 环境变量**

```bash
# .env
HEALTH_SCORE_LATENCY_THRESHOLD=1000
HEALTH_SCORE_COST_THRESHOLD=0.80
HEALTH_SCORE_ENABLE_COST_PENALTY=true
```

**方法 2: 数据库配置表**

```sql
CREATE TABLE health_score_configs (
    tenant_id TEXT PRIMARY KEY,
    latency_threshold_ms INT DEFAULT 800,
    cost_threshold_usd NUMERIC(10,4) DEFAULT 0.50,
    error_rate_tolerance NUMERIC(3,2) DEFAULT 0.10,
    compliance_weight INT DEFAULT 10,
    enable_cost_penalty BOOLEAN DEFAULT true,
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 插入租户配置
INSERT INTO health_score_configs (tenant_id, latency_threshold_ms)
VALUES ('tnt_001', 1200);
```

**方法 3: API 动态更新**

```bash
# 更新配置（需 super_admin 权限）
curl -X PUT https://api.example.com/api/admin/health-score/config \
  -H "Authorization: Bearer sk-xxx" \
  -H "Content-Type: application/json" \
  -d '{
    "latency_threshold_ms": 1000,
    "cost_threshold_usd": 0.80
  }'
```

---

## 4. 后台 Worker 监控

### 4.1 Worker 架构

**未来架构**（T1.5 实现）:

```
┌──────────────────────────────────────────┐
│         Health Score Worker              │
├──────────────────────────────────────────┤
│                                          │
│  1. 每 5 分钟扫描 session_summaries       │
│     WHERE last_health_at IS NULL         │
│     OR last_request_at > last_health_at  │
│                                          │
│  2. 批量计算健康分 (100 会话/批次)        │
│                                          │
│  3. 写入 health_score, health_grade,     │
│     outcome, last_health_at              │
│                                          │
│  4. 发送 Prometheus 指标                 │
│                                          │
└──────────────────────────────────────────┘
```

### 4.2 监控指标

#### 4.2.1 Worker 性能指标

| 指标 | 类型 | 标签 | 含义 |
|------|------|------|------|
| `health_worker_run_duration_seconds` | histogram | - | Worker 单次运行耗时 |
| `health_worker_sessions_processed_total` | counter | - | 累计处理会话数 |
| `health_worker_errors_total` | counter | error_type | 处理错误数 |
| `health_worker_lag_seconds` | gauge | - | 滞后时间（当前时间 - 最老未计算会话的 last_request_at）|

#### 4.2.2 健康分布指标

| 指标 | 类型 | 标签 | 含义 |
|------|------|------|------|
| `session_health_score_distribution` | histogram | - | 健康分分布（0-100） |
| `session_health_grade_total` | counter | grade | 各等级会话数（A/B/C/D/F）|
| `session_outcome_total` | counter | outcome | 各结果类型会话数 |

#### 4.2.3 扣分原因指标

| 指标 | 类型 | 标签 | 含义 |
|------|------|------|------|
| `session_penalty_points_total` | counter | category | 各类别累计扣分 |
| `session_penalty_triggered_total` | counter | category | 各类别触发次数 |

### 4.3 Grafana 仪表盘

**推荐面板**:

#### 面板 1: 健康分布
```promql
# 健康等级分布饼图
sum by (grade) (increase(session_health_grade_total[1h]))
```

#### 面板 2: Worker 性能
```promql
# Worker 处理速率
rate(health_worker_sessions_processed_total[5m])

# Worker 延迟
health_worker_lag_seconds
```

#### 面板 3: 扣分 TOP 原因
```promql
# 最常见的扣分原因
topk(5, sum by (category) (increase(session_penalty_triggered_total[1h])))
```

#### 面板 4: 结果分布趋势
```promql
# 完成/错误/放弃趋势
sum by (outcome) (increase(session_outcome_total[5m]))
```

---

## 5. 告警配置

### 5.1 告警规则

**文件**: `observability/alerts/session_health.yml`

```yaml
groups:
  - name: session_health
    interval: 1m
    rules:
      # 告警 1: 健康分均值过低
      - alert: SessionHealthScoreLow
        expr: |
          avg(session_health_score_distribution) < 60
        for: 15m
        labels:
          severity: warning
          component: session_health
        annotations:
          summary: "会话健康分均值过低"
          description: "过去 15 分钟平均健康分 {{ $value }}，低于 60 分阈值"
          
      # 告警 2: F 等级会话占比高
      - alert: HighFailureRate
        expr: |
          sum(increase(session_health_grade_total{grade="F"}[5m])) /
          sum(increase(session_health_grade_total[5m])) > 0.10
        for: 5m
        labels:
          severity: critical
          component: session_health
        annotations:
          summary: "F 等级会话占比超过 10%"
          description: "过去 5 分钟 {{ $value | humanizePercentage }} 的会话为 F 等级"
          
      # 告警 3: Worker 滞后严重
      - alert: HealthWorkerLagging
        expr: health_worker_lag_seconds > 600
        for: 5m
        labels:
          severity: warning
          component: session_health
        annotations:
          summary: "健康分计算滞后"
          description: "Worker 滞后 {{ $value }} 秒，有未计算的会话积压"
          
      # 告警 4: Worker 错误率高
      - alert: HealthWorkerErrors
        expr: |
          rate(health_worker_errors_total[5m]) > 0.1
        for: 5m
        labels:
          severity: warning
          component: session_health
        annotations:
          summary: "健康分计算错误率高"
          description: "Worker 错误率 {{ $value }} errors/sec"
          
      # 告警 5: 合规问题激增
      - alert: ComplianceIssuesSpike
        expr: |
          sum(increase(session_penalty_triggered_total{category="compliance"}[5m])) > 10
        for: 5m
        labels:
          severity: critical
          component: compliance
        annotations:
          summary: "合规问题激增"
          description: "过去 5 分钟触发 {{ $value }} 次合规扣分"
```

### 5.2 告警通知配置

**钉钉/企微通知模板**:

```markdown
## 🚨 会话健康告警

**告警**: {{ .GroupLabels.alertname }}
**严重度**: {{ .GroupLabels.severity }}
**触发时间**: {{ .StartsAt }}

### 详情
{{ .Annotations.description }}

### 快速链接
- [健康仪表盘](https://grafana.example.com/d/session-health)
- [告警详情](https://prometheus.example.com/alerts)

### 处理建议
1. 检查 Grafana 仪表盘确认趋势
2. 查看最近 F 等级会话的扣分明细
3. 检查是否有系统性问题（如提供商故障）
```

---

## 6. Troubleshooting 常见问题

### 6.1 问题: 健康分全部为 NULL

**症状**: `session_summaries.health_score` 字段为 NULL

**排查步骤**:

1. 检查 worker 是否运行
```bash
# 查看进程
ps aux | grep health_worker

# 查看日志
tail -f /var/log/llmgw/health_worker.log
```

2. 检查是否有数据库连接问题
```sql
-- 检查是否有会话数据
SELECT COUNT(*) FROM session_summaries WHERE last_request_at > NOW() - INTERVAL '1 hour';
```

3. 手动触发计算
```bash
curl -X POST https://api.example.com/api/admin/sessions/gw_abc123/recompute-health \
  -H "Authorization: Bearer sk-xxx"
```

**解决方案**:
- 重启 health worker
- 检查数据库权限
- 批量重算：`scripts/batch_recompute_health.sh`

---

### 6.2 问题: 健康分异常偏低

**症状**: 大量会话健康分 < 60，但实际运行正常

**排查步骤**:

1. 查看扣分明细
```sql
SELECT 
    gw_session_id,
    health_score,
    health_grade,
    error_count,
    avg_latency_ms,
    total_cost_usd / NULLIF(request_count, 0) as avg_cost_per_request
FROM session_summaries
WHERE health_score < 60
ORDER BY last_request_at DESC
LIMIT 10;
```

2. 检查阈值配置是否合理
```bash
# 查看当前配置
curl https://api.example.com/api/admin/health-score/config \
  -H "Authorization: Bearer sk-xxx"
```

3. 分析扣分分布
```sql
-- 统计扣分原因
SELECT 
    CASE 
        WHEN error_count > 0 THEN 'error'
        WHEN avg_latency_ms > 800 THEN 'latency'
        WHEN total_cost_usd / NULLIF(request_count, 0) > 0.50 THEN 'cost'
        ELSE 'other'
    END as penalty_reason,
    COUNT(*) as count
FROM session_summaries
WHERE health_score < 60
GROUP BY 1
ORDER BY 2 DESC;
```

**解决方案**:
- 如果延迟扣分占比高 → 提高 `LatencyThresholdMs`
- 如果成本扣分占比高 → 提高 `CostThresholdUSD` 或禁用成本扣分
- 如果是系统性问题 → 优化底层基础设施

---

### 6.3 问题: Worker 处理速度慢

**症状**: `health_worker_lag_seconds` 持续增长

**排查步骤**:

1. 检查积压数量
```sql
SELECT COUNT(*) 
FROM session_summaries 
WHERE last_health_at IS NULL 
   OR last_request_at > last_health_at;
```

2. 检查数据库性能
```sql
-- 慢查询
EXPLAIN ANALYZE
SELECT * FROM session_summaries
WHERE last_health_at IS NULL
LIMIT 100;
```

3. 检查 worker 并发数
```bash
# 查看 worker 配置
cat config/health_worker.yaml
```

**解决方案**:
- 增加 worker 并发数（从 1 提高到 4）
- 增加批次大小（从 100 提高到 500）
- 添加索引：
```sql
CREATE INDEX CONCURRENTLY idx_session_summaries_health_pending 
ON session_summaries (last_request_at) 
WHERE last_health_at IS NULL;
```

---

### 6.4 问题: 等级分布不符合预期

**症状**: F 等级占比过高（>20%）或过低（<1%）

**诊断**:

```sql
-- 查看等级分布
SELECT 
    health_grade,
    COUNT(*) as count,
    ROUND(COUNT(*) * 100.0 / SUM(COUNT(*)) OVER (), 2) as percentage
FROM session_summaries
WHERE last_request_at > NOW() - INTERVAL '1 day'
  AND health_grade IS NOT NULL
GROUP BY health_grade
ORDER BY health_grade;
```

**正常分布参考**:
- A: 30-40%
- B: 25-35%
- C: 15-25%
- D: 5-10%
- F: 5-10%

**调整建议**:
- 如果 F 过多 → 放宽阈值或降低扣分权重
- 如果 A 过多 → 说明系统运行良好，可收紧标准

---

### 6.5 问题: 合规扣分误报

**症状**: 正常会话被扣合规分

**排查步骤**:

1. 查看具体触发原因
```sql
SELECT 
    gw_session_id,
    compliance_status,
    prompt_injection_detected,
    pii_detected,
    toxic_output_detected
FROM session_summaries
WHERE compliance_issues_count > 0
ORDER BY last_request_at DESC
LIMIT 10;
```

2. 检查检测规则配置
```bash
# 查看合规检测配置
curl https://api.example.com/api/admin/compliance/config \
  -H "Authorization: Bearer sk-xxx"
```

**解决方案**:
- 调整 PII 检测正则表达式
- 添加白名单
- 降低合规扣分权重：
```go
config := HealthScoreConfig{
    ComplianceWeight: 5,  // 从 10 降到 5
}
```

---

## 7. 性能优化建议

### 7.1 数据库索引

**必需索引**:

```sql
-- 健康分查询索引
CREATE INDEX CONCURRENTLY idx_session_summaries_health_score 
ON session_summaries (health_score DESC, last_request_at DESC);

-- 等级查询索引
CREATE INDEX CONCURRENTLY idx_session_summaries_health_grade 
ON session_summaries (health_grade, last_request_at DESC);

-- Outcome 查询索引
CREATE INDEX CONCURRENTLY idx_session_summaries_outcome 
ON session_summaries (outcome, last_request_at DESC);

-- Worker 扫描索引
CREATE INDEX CONCURRENTLY idx_session_summaries_health_pending 
ON session_summaries (last_request_at) 
WHERE last_health_at IS NULL OR last_request_at > last_health_at;
```

### 7.2 缓存策略

**API 端点缓存**:

```go
// 健康分布查询缓存 5 分钟
cacheKey := fmt.Sprintf("health:dist:%s:%s", tenantID, dateRange)
cacheTTL := 5 * time.Minute

// 单会话健康详情不缓存（需实时）
// 列表查询缓存 1 分钟
```

### 7.3 批量计算优化

**Worker 批处理策略**:

```go
// 每批处理 500 个会话
batchSize := 500

// 使用事务批量写入
tx.Begin()
for _, session := range batch {
    health := ComputeHealth(session, config)
    tx.Exec("UPDATE session_summaries SET health_score = $1 WHERE session_key = $2", 
            health.HealthScore, session.SessionKey)
}
tx.Commit()
```

---

## 8. 运维检查清单

### 8.1 日常巡检（每天）

- [ ] 检查健康分均值（目标 >70）
- [ ] 检查 F 等级占比（目标 <10%）
- [ ] 检查 worker 滞后时间（目标 <5 分钟）
- [ ] 检查告警是否触发
- [ ] 查看 Grafana 仪表盘异常趋势

### 8.2 周检查（每周一）

- [ ] 分析上周健康分趋势
- [ ] 统计扣分原因 TOP 5
- [ ] 检查配置是否需要调整
- [ ] 审查 F 等级会话样本（随机抽 10 个）
- [ ] 检查数据库性能（慢查询）

### 8.3 月检查（每月 1 号）

- [ ] 生成月度健康报告
- [ ] 评估配置优化效果
- [ ] 检查索引膨胀情况
- [ ] 归档历史数据（>90 天）
- [ ] 更新运维文档

---

## 9. 参考资源

### 9.1 相关文档

- [产品规划文档](../session-management-analytics-plan.md)
- [API 文档](../api/session-analytics.yaml)
- [用户手册](../user-guide/session-management.md)

### 9.2 代码位置

| 功能 | 文件路径 |
|------|---------|
| 健康分计算 | `admin/session_health.go` |
| API 端点 | `admin/session_health_api.go` |
| 配置结构 | `admin/session_health_config.go` |
| Worker（待实现） | `bg/session_health_worker.go` |

### 9.3 数据库表

| 表名 | 用途 |
|------|------|
| `session_summaries` | 健康分存储（health_score, health_grade, outcome）|
| `request_logs` | 原始数据源 |
| `health_score_configs` | 租户配置（待实现）|

### 9.4 监控链接

- Grafana: https://grafana.example.com/d/session-health
- Prometheus: https://prometheus.example.com/graph?g0.expr=session_health_score_distribution
- 告警: https://alertmanager.example.com

---

**文档版本**: v2.1.0  
**最后更新**: 2026-07-06  
**维护人**: SRE Team  
**联系方式**: sre@example.com
