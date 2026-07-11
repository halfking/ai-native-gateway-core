# 会话模块执行记录系统设计文档

**版本：** v1.0  
**日期：** 2026-07-10  
**状态：** 设计完成，待实施

---

## 一、设计目标

### 1.1 核心问题

当前系统存在**模块执行重复**的问题：
- 安全检测：SessionAuditHook 和 SecurityHook 都做 prompt injection 检测
- 会话查询：多个 Hook 重复查询相同的会话历史
- LLM 调用：摘要生成可能被多次触发
- 健康计算：健康度计算缺乏去重机制

### 1.2 解决方案

建立**统一的会话模块执行记录系统**：
1. **去重执行** - 已有结果直接复用，避免重复计算
2. **结果缓存** - 带 TTL 机制，自动失效过期缓存
3. **执行追踪** - 完整记录每次模块执行情况
4. **性能监控** - 命中率、延迟分布、失败率

---

## 二、架构设计

### 2.1 三层缓存架构

```
┌─────────────────────────────────────────────────────────┐
│                     请求进入                              │
└────────────────────────┬────────────────────────────────┘
                         ↓
        ┌────────────────────────────────────┐
        │  L0: 内存缓存（30 秒 TTL）           │ ← 极短 TTL，用于超高频
        │  map + sync.RWMutex                  │
        └────────────────┬───────────────────┘
                         ↓ (未命中)
        ┌────────────────────────────────────┐
        │  L1: Redis 缓存（模块配置 TTL）       │ ← 跨实例共享
        │  key: module:exec:{sid}:{mod}:{key}  │
        └────────────────┬───────────────────┘
                         ↓ (未命中)
        ┌────────────────────────────────────┐
        │  L2: 数据库查询（Hot 表）             │ ← 持久化，7天保留
        │  session_module_executions_hot       │
        └────────────────┬───────────────────┘
                         ↓ (未命中)
        ┌────────────────────────────────────┐
        │  执行实际逻辑 + 记录到所有层           │
        └────────────────────────────────────┘
```

### 2.2 数据流向

```
Module Hook 调用
   ↓
CheckAndExecute(sessionID, moduleName, inputParams, ttl, executeFn)
   ↓
1. 计算 cache_key = hash(moduleName + inputParams)
   ↓
2. 查询 L0 → L1 → L2
   ↓
3. 命中？→ 直接返回（FromCache=true）
   ↓
4. 未命中 → 记录 execution_start（status=running）
   ↓
5. 执行 executeFn
   ↓
6. 记录 execution_success/failure
   ↓
7. 写回 L1, L2
   ↓
8. 返回结果
```

---

## 三、数据库设计

### 3.1 热表：session_module_executions_hot

**用途：** 高频查询，保留最近 7 天

**关键字段：**
- `execution_id` - 主键
- `gw_session_id` - 会话 ID
- `tenant_id` - 租户 ID
- `module_name` - 模块标识
- `module_version` - 模块版本（用于失效旧结果）
- `status` - 状态（pending/running/completed/failed/skipped）
- `cache_key` - 输入参数哈希
- `ttl_seconds` - 有效期
- `expires_at` - 过期时间
- `result_summary` - 结果摘要（JSONB）
- `result_detail` - 结果详情（JSONB）

**核心索引：**
```sql
-- 查找有效缓存
CREATE INDEX idx_sme_hot_lookup 
    ON session_module_executions_hot(gw_session_id, module_name, cache_key, status, expires_at)
    WHERE status = 'completed';

-- 租户查询
CREATE INDEX idx_sme_hot_tenant_time 
    ON session_module_executions_hot(tenant_id, created_at DESC);

-- 模块统计
CREATE INDEX idx_sme_hot_module_stats 
    ON session_module_executions_hot(module_name, status, completed_at DESC);
```

### 3.2 分区表：session_module_executions

**用途：** 长期归档，按月分区

**分区策略：**
- 按 `created_at` RANGE 分区
- 每月一个分区
- 自动创建下个月分区（pg_cron）

### 3.3 数据生命周期

```
热表（7天） → 归档表（永久）
   ↓
每日凌晨 2 点执行 archive_session_module_executions(7)
   ↓
将过期数据从 hot 表移动到分区表
   ↓
删除 hot 表中的旧数据
```

---

## 四、模块标识体系

### 4.1 标准模块名称

所有模块必须使用 `domains/moduleregistry/registry.go` 中定义的常量。

**分类：**

| 类别 | 模块 | TTL | 说明 |
|------|------|-----|------|
| 安全检测 | session_audit | 1h | 会话审计 |
| 安全检测 | security_scan | 30m | 通用安全扫描 |
| 安全检测 | prompt_injection | 2h | Prompt 注入 |
| 安全检测 | toxicity_detection | 1h | 有毒内容 |
| 安全检测 | pii_detection | 1h | PII 检测 |
| 会话分析 | session_inspector | 5m | 会话巡检 |
| 会话分析 | session_health | 1h | 健康度 |
| 会话分析 | session_summary | 24h | LLM 摘要 |
| 会话分析 | intent_analysis | 1h | 意图分析 |
| 会话分析 | goal_analysis | 1h | 目标分析 |
| 优化 | session_compression | 30m | 会话压缩 |
| 优化 | handoff_trigger | 10m | 切换检测 |
| 优化 | optimization_advice | 24h | 优化建议 |
| 合规 | output_compliance | 1h | 输出合规 |
| 合规 | data_ownership | 30m | 数据所有权 |

### 4.2 模块元信息

```go
type ModuleInfo struct {
    Name        string  // 模块名称
    Version     string  // 模块版本
    Description string  // 描述
    TTLSeconds  int     // 默认 TTL
}
```

**版本管理：**
- 模块升级时，version 字段变化
- 可选择性失效旧版本结果
- 支持灰度发布

---

## 五、核心实现

### 5.1 Executor 核心逻辑

**位置：** `domains/moduleexec/executor.go`

**主要方法：**

```go
// Check-Execute-Record 模式
func (e *Executor) CheckAndExecute(
    ctx context.Context,
    sessionID, tenantID, moduleName string,
    inputParams map[string]interface{},
    ttlSeconds int,
    executeFn func(context.Context) (*ExecuteResult, error),
) (*ExecuteResult, error)
```

**执行流程：**
1. 参数验证
2. 计算 cache_key
3. L0 → L1 → L2 查询
4. 命中 → 返回缓存
5. 未命中 → 记录开始
6. 执行逻辑
7. 记录结果
8. 写回缓存

### 5.2 缓存键计算

```go
func ComputeCacheKey(moduleName string, params map[string]interface{}) string {
    data, _ := json.Marshal(params)
    hash := sha256.Sum256(data)
    return moduleName + ":" + hex.EncodeToString(hash[:])[:16]
}
```

**为什么用哈希？**
- 输入参数可能是复杂结构
- 哈希保证相同输入产生相同 key
- 16 字符足够避免冲突

### 5.3 TTL 策略

| 模块类型 | TTL | 理由 |
|----------|-----|------|
| 实时检测 | 5-10 分钟 | 实时性要求高 |
| 安全审计 | 1 小时 | 平衡性能和准确性 |
| 健康计算 | 1 小时 | 计算成本适中 |
| LLM 摘要 | 24 小时 | 生成成本高，需复用 |
| 优化建议 | 24 小时 | 异步分析，结果稳定 |

---

## 六、集成示例

### 6.1 SessionAuditHook 改造

**改造前：**
```go
func (h *SessionAuditHook) Execute(ctx, env) error {
    content := extractUserContent(env)
    result := h.detector.Detect(content) // 每次都执行
    return h.handleResult(result)
}
```

**改造后：**
```go
func (h *SessionAuditHook) Execute(ctx, env) error {
    content := extractUserContent(env)
    inputParams := map[string]interface{}{
        "content_hash": hash(content),
        "config_version": h.config.Version,
    }
    
    result, err := h.executor.CheckAndExecute(
        ctx, env.SessionID, env.TenantID,
        moduleregistry.ModuleSessionAudit,
        inputParams, 3600,
        func(ctx) (*moduleexec.ExecuteResult, error) {
            detectResult := h.detector.Detect(content)
            return &moduleexec.ExecuteResult{
                ResultSummary: map[string]interface{}{
                    "score": detectResult.Score,
                    "decision": detectResult.Decision,
                },
            }, nil
        },
    )
    
    if result.FromCache {
        log.Debug("audit from cache")
    }
    
    return h.handleResult(result)
}
```

### 6.2 SessionHealthWorker 改造

**改造前：**
```go
func (w *Worker) processSession(sessionID) error {
    healthData := w.loadData(sessionID)
    score := w.computeHealth(healthData)
    w.updateDB(sessionID, score)
}
```

**改造后：**
```go
func (w *Worker) processSession(sessionID, tenantID) error {
    result, err := w.executor.CheckAndExecute(
        ctx, sessionID, tenantID,
        moduleregistry.ModuleSessionHealth,
        map[string]interface{}{
            "check_time": time.Now().Truncate(time.Hour).Unix(),
        },
        3600,
        func(ctx) (*moduleexec.ExecuteResult, error) {
            healthData := w.loadData(sessionID)
            score, grade, outcome := w.computeHealth(healthData)
            w.updateDB(sessionID, score, grade, outcome)
            return &moduleexec.ExecuteResult{
                ResultSummary: map[string]interface{}{
                    "health_score": score,
                    "health_grade": grade,
                },
            }, nil
        },
    )
    return err
}
```

---

## 七、监控与运维

### 7.1 关键指标

**Prometheus 指标：**
```go
// 模块执行总数
session_module_execution_total{module, status}

// 模块执行延迟
session_module_execution_duration_seconds{module}

// 缓存命中率
session_module_cache_hits_total{module}
session_module_cache_misses_total{module}
```

**业务指标：**
- 各模块执行次数
- 各模块缓存命中率
- 各模块 P50/P95/P99 延迟
- 失败率

### 7.2 监控视图

```sql
-- 模块执行统计（24小时）
SELECT * FROM v_sme_module_stats;

-- 缓存命中率
SELECT * FROM v_sme_cache_hit_rate;

-- 失败执行监控
SELECT * FROM v_sme_failures;
```

### 7.3 告警规则

**建议告警：**
- 任一模块失败率 > 5%
- 任一模块 P99 延迟 > 5s
- 缓存命中率 < 50%（优化信号）
- 热表记录数 > 1000万（性能预警）

### 7.4 维护任务

**每日任务：**
- 凌晨 2 点：归档 7 天前的数据
- 凌晨 3 点：清理过期记录

**每月任务：**
- 1 号：自动创建下个月分区
- 30 号：备份归档表

---

## 八、性能分析

### 8.1 预期收益

| 指标 | 优化前 | 优化后 | 提升 |
|------|--------|--------|------|
| 安全检测重复率 | 80% | 10% | 87.5% ↓ |
| LLM API 调用 | 100% | 40% | 60% ↓ |
| 数据库 CPU | 100% | 60% | 40% ↓ |
| 响应延迟（缓存命中） | 100ms | 10ms | 90% ↓ |
| 数据库查询次数 | 100% | 30% | 70% ↓ |

### 8.2 容量规划

**假设：**
- 每天 10 万次会话请求
- 平均每个会话触发 5 个模块
- 每条记录约 2KB

**容量估算：**
- 每天新增：50 万条 ≈ 1GB
- 热表（7天）：7GB
- 月度归档：30GB
- 年度归档：360GB

**优化建议：**
- result_detail 大字段可移到对象存储（S3/OSS）
- 只在热表保留 result_summary
- 归档表按需查询 result_detail

---

## 九、实施路线图

### Phase 1: 基础设施 ✅
- [x] 创建数据库表结构
- [x] 定义模块标识体系
- [x] 实现执行器核心逻辑
- [x] 实现管理员接口

### Phase 2: 核心模块改造
- [ ] SessionAuditHook 集成
- [ ] SessionInspectorHook 集成
- [ ] SessionHealthWorker 集成
- [ ] SecurityHook 集成

### Phase 3: 监控与告警
- [ ] Prometheus 指标
- [ ] Grafana 仪表盘
- [ ] 告警规则配置

### Phase 4: 全面推广
- [ ] 所有会话模块接入
- [ ] 性能测试与调优
- [ ] 文档完善与培训

---

## 十、风险与注意事项

### 10.1 缓存失效风险

**问题：** 缓存可能导致结果过期但仍被使用

**缓解措施：**
- 合理设置 TTL
- 模块升级时主动失效
- 提供手动失效接口
- 监控缓存命中率

### 10.2 数据一致性风险

**问题：** 并发执行可能导致重复

**缓解措施：**
- 使用数据库唯一约束
- 记录 execution_start 时检查
- 失败时降级直接执行

### 10.3 性能风险

**问题：** 数据库查询可能成为瓶颈

**缓解措施：**
- 三层缓存（L0/L1/L2）
- 批量查询接口
- 定期清理过期数据
- 分区表归档

---

## 十一、相关文档

- [SQL 迁移文件](../sql/migrations/startup/378_session_module_executions.sql)
- [模块注册表](../domains/moduleregistry/registry.go)
- [执行器核心](../domains/moduleexec/executor.go)
- [管理员接口](../domains/moduleexec/admin.go)
- [使用文档](../domains/moduleexec/README.md)
