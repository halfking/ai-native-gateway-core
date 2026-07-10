# 会话模块执行记录系统

**版本：** v1.0  
**日期：** 2026-07-10

---

## 一、问题背景

当前系统存在以下重复执行问题：

1. **安全检测重复** - SessionAuditHook 和 SecurityHook 都执行 prompt injection 检测
2. **会话查询重复** - 多个 Hook 都会查询相同的会话历史
3. **LLM 调用重复** - 摘要生成可能被多次触发
4. **健康计算重复** - 健康度计算缺乏去重机制

## 二、解决方案

建立**统一的会话模块执行记录系统**，实现：
- ✅ 去重执行（已有结果直接复用）
- ✅ 结果缓存（带 TTL 机制）
- ✅ 执行追踪（完整记录每次执行）
- ✅ 性能监控（命中率、延迟分布）

## 三、核心组件

### 3.1 数据库表

| 表名 | 用途 | 保留时间 |
|------|------|----------|
| `session_module_executions_hot` | 热表（高频查询） | 7 天 |
| `session_module_executions` | 归档表（按月分区） | 长期 |

### 3.2 核心代码

| 文件 | 用途 |
|------|------|
| `sql/migrations/startup/360_session_module_executions.sql` | 数据库表结构 + 索引 + 清理函数 |
| `domains/moduleregistry/registry.go` | 模块标识常量 + 元信息 |
| `domains/moduleexec/executor.go` | 执行器核心逻辑（Check-Execute-Record） |
| `domains/moduleexec/admin.go` | 管理员接口（统计、清理、查询） |

## 四、使用示例

### 4.1 基础用法

```go
// 1. 创建执行器
executor := moduleexec.NewExecutor(moduleexec.Config{
    DB:          dbPool,
    Redis:       redisClient,
    EnableRedis: true,
    Logger:      logger,
})

// 2. 在 Hook 中使用
result, err := executor.CheckAndExecute(
    ctx,
    sessionID,
    tenantID,
    moduleregistry.ModuleSessionAudit,
    map[string]interface{}{
        "content_hash": hash(content),
        "config_version": "v2.1",
    },
    3600, // TTL: 1 小时
    func(ctx context.Context) (*moduleexec.ExecuteResult, error) {
        // 实际检测逻辑
        return &moduleexec.ExecuteResult{
            ResultSummary: map[string]interface{}{
                "score": 3,
                "decision": "pass",
            },
        }, nil
    },
)

if err != nil {
    return err
}

if result.FromCache {
    // 命中缓存，无需重新计算
    logger.Debug("used cached result")
}
```

### 4.2 批量查询

```go
// 批量检查多个模块的执行状态
results, err := executor.BatchCheck(ctx, sessionID, []string{
    moduleregistry.ModuleSessionAudit,
    moduleregistry.ModuleSecurityScan,
    moduleregistry.ModuleSessionHealth,
})
```

### 4.3 缓存失效

```go
// 当会话状态发生重大变化时，使相关缓存失效
executor.InvalidateCache(ctx, sessionID, moduleregistry.ModuleSessionHealth)
```

## 五、模块清单

### 安全检测类
- `session_audit` - 会话审计
- `security_scan` - 通用安全扫描
- `prompt_injection` - Prompt 注入检测
- `toxicity_detection` - 有毒内容检测
- `pii_detection` - PII 检测

### 会话分析类
- `session_inspector` - 会话巡检
- `session_health` - 健康度计算
- `session_summary` - LLM 摘要生成
- `intent_analysis` - 意图分析
- `goal_analysis` - 目标分析

### 优化类
- `session_compression` - 会话压缩
- `handoff_trigger` - 切换检测
- `optimization_advice` - 优化建议

### 合规类
- `output_compliance` - 输出合规检查
- `data_ownership` - 数据所有权验证

## 六、性能指标

### 6.1 预期收益

| 指标 | 优化前 | 优化后 | 提升 |
|------|--------|--------|------|
| 安全检测重复率 | 80% | 10% | 87.5% ↓ |
| LLM API 调用 | 100% | 40% | 60% ↓ |
| 数据库 CPU | 100% | 60% | 40% ↓ |
| 响应延迟（缓存命中） | 100ms | 10ms | 90% ↓ |

### 6.2 监控 SQL

```sql
-- 模块执行统计
SELECT * FROM v_sme_module_stats;

-- 缓存命中率
SELECT * FROM v_sme_cache_hit_rate;

-- 失败执行监控
SELECT * FROM v_sme_failures;
```

## 七、实施步骤

### Phase 1: 基础设施（已完成）
- [x] 创建数据库表结构（360 迁移）
- [x] 定义模块标识体系
- [x] 实现执行器核心逻辑
- [x] 实现管理员接口

### Phase 2: 核心模块改造（待实施）
- [ ] SessionAuditHook 集成
- [ ] SessionInspectorHook 集成
- [ ] SessionHealthWorker 集成
- [ ] SecurityHook 集成

### Phase 3: 监控与告警（待实施）
- [ ] Prometheus 指标
- [ ] Grafana 仪表盘
- [ ] 告警规则配置

### Phase 4: 全面推广（待实施）
- [ ] 所有会话模块接入
- [ ] 性能测试
- [ ] 文档完善

## 八、注意事项

### 8.1 缓存失效策略

当以下情况发生时，需要主动失效缓存：
- 会话配置变更
- 模块版本升级
- 用户手动触发重算

### 8.2 TTL 设置原则

| 模块类型 | 推荐 TTL | 说明 |
|----------|----------|------|
| 实时检测（巡检） | 5-10 分钟 | 实时性要求高 |
| 安全审计 | 1 小时 | 平衡性能和准确性 |
| 健康计算 | 1 小时 | 计算成本适中 |
| LLM 摘要 | 24 小时 | 生成成本高 |
| 优化建议 | 24 小时 | 异步分析 |

### 8.3 容量规划

假设：
- 每天 10 万次会话请求
- 平均每个会话触发 5 个模块
- 每条记录约 2KB

则：
- 每天新增：50 万条 ≈ 1GB
- 热表（7天）：7GB
- 月度归档：30GB
- 年度归档：360GB

## 九、相关文档

- [SQL 迁移文件](../sql/migrations/startup/360_session_module_executions.sql)
- [模块注册表](../domains/moduleregistry/registry.go)
- [执行器核心](../domains/moduleexec/executor.go)
- [管理员接口](../domains/moduleexec/admin.go)
