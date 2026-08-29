# Provider Error Aggregation 修复文档

**日期**：2026-08-29  
**修复类型**：P0 阻塞问题 + P1 高优先级  
**关联审计**：docs/audit/2026-08-29-comprehensive-24h-audit.md  
**提交**：7f1604fb9

---

## 问题描述

### P0: provider_error_details 表无业务写入

**症状**：
- `provider_error_details` 表结构已存在（Migration 435），但无 Go writer
- 表设计为错误聚合表（occurrences 字段用于计数），但缺少聚合逻辑
- `provider_error_distribution` 视图依赖该表，但无数据源

**影响**：
- 供应商错误趋势分析功能缺失
- 错误聚合看板无法展示
- 运维人员无法快速定位高频错误模式

### P1: 前置拒绝未记录

**症状**：
- circuit-open（熔断器打开）拒绝请求时，未记录到 `candidate_failure_logs_hot`
- 并发限流拒绝时，未记录到 `candidate_failure_logs_hot`

**影响**：
- 运维无法看到哪些 credential 处于熔断状态
- 无法统计因限流导致的失败次数
- 无法区分"真实失败"和"前置拒绝"

---

## 修复方案

### 1. Provider Error Aggregator 实现

#### 架构设计

```
candidate_failure_logs_hot  →  [聚合器每10分钟]  →  provider_error_details
         (原始失败日志)                                    (错误指纹去重计数)
```

#### 错误指纹定义

相同错误的判定标准（fingerprint）：
```sql
(
  provider_id,
  COALESCE(model_name, ''),
  COALESCE(endpoint, ''),
  error_type,
  COALESCE(error_code, ''),
  COALESCE(LEFT(error_message, 200), '')
)
```

注意事项：
- 使用 `COALESCE` 处理 NULL 值（PostgreSQL 认为 NULL != NULL）
- error_message 截断到 200 字符避免指纹膨胀
- 函数索引支持 UPSERT

#### 核心逻辑

**聚合策略**：
- 每 10 分钟运行一次
- 处理最近 15 分钟的失败日志（15分钟窗口覆盖10分钟间隔，容忍延迟）
- 使用 advisory lock 防止多实例并发聚合冲突

**UPSERT 行为**：
```sql
ON CONFLICT (fingerprint) DO UPDATE SET
  occurrences = provider_error_details.occurrences + EXCLUDED.occurrences,
  last_seen_at = GREATEST(provider_error_details.last_seen_at, EXCLUDED.last_seen_at),
  first_seen_at = LEAST(provider_error_details.first_seen_at, EXCLUDED.first_seen_at),
  updated_at = NOW(),
  context = EXCLUDED.context,        -- 更新为最新的 context
  request_id = EXCLUDED.request_id,  -- 更新为最新的 request_id
  tenant_id = EXCLUDED.tenant_id
```

### 2. 前置拒绝记录实现

#### Circuit-Open 拒绝记录

**位置**：`domains/streaming/executors/executor_dispatch.go:500-531`

**记录内容**：
```go
{
  "circuit_open": true,
  "rejection_type": "circuit_breaker",
  "circuit_state": "open",           // 来自 breaker.State()
  "circuit_consecutive_failures": 5,  // 来自 breaker.ConsecutiveFailures()
  "candidates_left": 2
}
```

**错误类型**：`errorsx.KindConcurrent`

#### 并发限流拒绝记录

**位置**：`domains/streaming/executors/executor_dispatch.go:554-578`

**记录内容**：
```go
{
  "rate_limit_rejection": true,
  "rejection_type": "concurrency_limiter",
  "candidates_left": 2
}
```

**错误类型**：`errorsx.KindConcurrent`

---

## 实现细节

### 文件清单

1. **bg/provider_error_aggregator.go** (新建)
   - `ProviderErrorAggregator` 结构
   - `Start()` / `Stop()` 生命周期管理
   - `aggregateErrors()` 核心聚合逻辑

2. **bg/provider_error_aggregator_test.go** (新建)
   - 创建和配置测试
   - 启动/停止测试
   - 并发安全测试
   - PartitionManager 集成测试

3. **bg/partition_manager.go** (修改)
   - 添加 `errorAggregator *ProviderErrorAggregator` 字段
   - `Start()` 时启动聚合器
   - `Stop()` 时停止聚合器

4. **bg/partition_manager_test.go** (修改)
   - 添加 `promote_session_bodies_hot_to_partition` 到预期列表

5. **domains/streaming/executors/executor_dispatch.go** (修改)
   - circuit-open 拒绝记录逻辑
   - 并发限流拒绝记录逻辑

6. **sql/migrations/startup/616_provider_error_details_unique_constraint.sql** (新建)
   - 创建唯一索引 `idx_provider_error_details_fingerprint`
   - 使用 `COALESCE` 处理 NULL 值
   - 支持 UPSERT 操作

7. **sql/migrations/startup/616_provider_error_details_unique_constraint.down.sql** (新建)
   - 回滚脚本：删除唯一索引

### 并发安全机制

#### Advisory Lock

```go
// 使用 FNV-1a hash 计算固定的 lock key
const lockPrefix = "llm-gateway:provider_error_aggregator"
h := fnv.New64a()
h.Write([]byte(lockPrefix))
lockKey := int64(h.Sum64())

// 尝试获取事务级别的 advisory lock
var locked bool
tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", lockKey).Scan(&locked)

if !locked {
    // 其他实例正在聚合，跳过本次执行
    return
}
```

**优点**：
- 多实例部署时只有一个实例会执行聚合
- 事务结束自动释放锁
- 不会因为进程崩溃导致死锁

#### 生命周期管理

```go
type ProviderErrorAggregator struct {
    db       *pgxpool.Pool
    interval time.Duration
    stopCh   chan struct{}  // 停止信号
    doneCh   chan struct{}  // 完成确认
}
```

**Stop() 机制**：
```go
func (a *ProviderErrorAggregator) Stop() {
    close(a.stopCh)  // 发送停止信号
    <-a.doneCh       // 等待 goroutine 退出
}
```

---

## 测试验证

### 单元测试

```bash
# 运行聚合器测试
go test ./bg -run TestProviderErrorAggregator -v

# 并发安全测试
go test ./bg -race

# 完整测试套件
go test ./bg -race -count=1
```

**测试覆盖**：
- ✅ 创建和默认配置
- ✅ 启动/停止生命周期
- ✅ 停止幂等性
- ✅ nil pool 处理（不会 panic）
- ✅ 并发调用安全性
- ✅ PartitionManager 集成

### 集成测试（需要数据库）

```sql
-- 1. 插入测试失败日志
INSERT INTO candidate_failure_logs_hot (
    request_id, tenant_id, credential_id, provider_id, 
    raw_model_name, attempt_no, error_kind, error_message, 
    upstream_status_code, ts
) VALUES (
    'test-req-001', 1, 42, 1, 
    'gpt-4', 1, 'upstream_error', 'Rate limit exceeded', 
    429, NOW()
);

-- 2. 手动触发聚合（生产环境会自动运行）
-- ProviderErrorAggregator.aggregateErrors()

-- 3. 验证结果
SELECT 
    provider_id, model_name, error_type, error_code, 
    occurrences, first_seen_at, last_seen_at
FROM provider_error_details
WHERE provider_id = 1 AND error_code = '429';

-- 预期结果：
-- provider_id=1, model_name='gpt-4', error_code='429', occurrences=1
```

### 监控验证

**日志示例**：
```
INFO provider_error_aggregator: aggregation completed 
  error_groups=5 total_occurrences=127 duration=1.23s
```

**预期行为**：
- 首次启动立即执行一次聚合
- 之后每 10 分钟执行一次
- 如果没有新错误，输出 `DEBUG no new errors to aggregate`
- 如果有错误，输出 INFO 级别统计信息

---

## 部署计划

### Staging 验证

1. **执行 migration 616**
   ```bash
   psql -f sql/migrations/startup/616_provider_error_details_unique_constraint.sql
   ```

2. **部署新代码**
   ```bash
   ./scripts/deploy-staging.sh
   ```

3. **验证聚合器运行**
   ```bash
   # 查看日志
   kubectl logs -f deployment/llm-gateway | grep provider_error_aggregator
   
   # 检查数据
   psql -c "SELECT COUNT(*), SUM(occurrences) FROM provider_error_details;"
   ```

4. **人工触发一些失败**
   ```bash
   # 触发 circuit-open
   curl -X POST https://staging.example.com/v1/chat/completions \
     -H "Authorization: Bearer invalid-key" \
     -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}'
   
   # 等待 15 分钟，观察聚合结果
   ```

### 生产部署

**前置条件**：
- ✅ Staging 验证通过
- ✅ Migration 616 已在生产数据库执行
- ✅ 监控告警已配置

**部署步骤**：
1. 灰度 10% 流量，观察 1 小时
2. 检查 `provider_error_details` 表增长情况
3. 验证 Grafana 看板显示正常
4. 全量发布

**回滚方案**：
- 代码回滚：重新部署上一个版本
- 数据回滚：执行 `616_provider_error_details_unique_constraint.down.sql`

---

## 后续优化

### P2: 自动清理机制

```sql
-- 添加到 PartitionManager 月度清理任务
DELETE FROM provider_error_details
WHERE resolved = true 
  AND updated_at < NOW() - INTERVAL '30 days';
```

### P3: 监控指标

```go
// Prometheus 指标
provider_error_aggregation_duration_seconds
provider_error_aggregation_errors_total{provider_id, error_type}
provider_error_aggregation_last_success_timestamp
```

### P3: Grafana 看板

- 错误趋势图（按 provider、error_type 分组）
- Top 10 高频错误
- 新增错误告警（first_seen_at 在 1 小时内）

---

## 参考文档

- **审计报告**：`docs/audit/2026-08-29-comprehensive-24h-audit.md`
- **表结构定义**：`sql/migrations/startup/435_provider_quality_tables.sql`
- **数据源**：`domains/streaming/executors/candidate_failure_logger.go`
- **供应商画像设计**：`docs/供应商画像/02-数据库设计.md`

---

**修复负责人**：AI Assistant  
**审核状态**：✅ 代码审查通过 / ✅ 单元测试通过 / ⏳ 等待集成测试  
**部署状态**：✅ 已提交到 main 分支 / ⏳ 等待 staging 验证
