# 分区表架构审计报告

**日期**: 2026-07-05  
**审计人**: LLM Gateway OPS  
**审计范围**: 所有分区表的hot表架构实现

## 一、表结构汇总

### 1.1 已完成hot表迁移的表

| 表名 | Hot表 | 分区表 | View | Promote函数 | 迁移文件 |
|-----|-------|--------|------|------------|---------|
| request_logs | request_logs_hot | request_logs_YYYY_MM | request_logs_with_current_month | promote_request_logs_hot_to_partition | 341 |
| usage_ledger | usage_ledger_hot | usage_ledger_YYYY_MM | usage_ledger_with_current_month | promote_usage_ledger_hot_to_partition | 344 |
| request_wal | request_wal_hot | request_wal_YYYY_MM | request_wal_with_current_month | promote_request_wal_hot_to_partition | 345 |
| routing_decision_log | routing_decision_log_hot | routing_decision_log_YYYY_MM | routing_decision_log_with_current_month | promote_routing_decision_log_hot_to_partition | 346 |
| credential_model_index | credential_model_index_hot | credential_model_index_YYYY_MM | credential_model_index_with_current_month | promote_credential_model_index_hot_to_partition | 347 |

### 1.2 使用_default架构的表（需要迁移到hot表）

| 表名 | 当前写入目标 | 分区表 | View | 状态 |
|-----|------------|--------|------|------|
| credit_ledger | credit_ledger_default ❌ | credit_ledger_YYYY_MM | credit_ledger_with_current_month | **需要迁移到hot** |
| tool_usage_stats | tool_usage_stats_default ❌ | tool_usage_stats_YYYY_MM | tool_usage_stats_with_current_month | **需要迁移到hot** |
| request_logs_bodies | request_logs_bodies_default ❌ | request_logs_bodies_YYYY_MM | request_logs_bodies_with_current_month | **需要迁移到hot** |

## 二、代码审计结果

### 2.1 ✅ 正确使用hot表的代码

#### request_logs
- ✅ `domains/hooks/observability/telemetry/client.go`: 使用 `request_logs_hot`
- ✅ `admin/telemetry.go`: 使用 `request_logs_hot`
- ✅ `admin/data_lifecycle_blobs.go`: 使用 `request_logs_hot`
- ✅ `admin/data_lifecycle_attachments.go`: 使用 `request_logs_hot`

#### usage_ledger
- ✅ `domains/hooks/observability/telemetry/client.go`: 使用 `usage_ledger_hot`
- ✅ `admin/telemetry.go`: 使用 `usage_ledger_hot`

#### credit_ledger
- ✅ `maas/service.go`: 使用 `credit_ledger_hot`

### 2.2 ❌ 错误使用_default的代码

#### tool_usage_stats
1. **registry/usage_stats.go** (行 37-78)
   - ❌ 主要写入目标: `tool_usage_stats_default`
   - ❌ 回退写入目标: `tool_usage_stats` (父表)
   - **问题**: 应该使用 `tool_usage_stats_hot`

2. **domains/toolexecution/postgres_store.go** (行 266)
   - ✅ 已使用 `tool_usage_stats_hot`
   - ⚠️  但注释中的表名定义混乱

### 2.3 查询操作审计

#### ✅ 正确的查询模式

1. **查询热数据（7天内）** - 直接查hot表
   ```go
   // 示例: admin/data_lifecycle.go
   SELECT * FROM request_logs_hot WHERE ts > NOW() - INTERVAL '7 days'
   ```

2. **跨月聚合查询** - 使用view
   ```go
   // 示例: registry/usage_stats.go GetUsageStats
   SELECT * FROM tool_usage_stats WHERE usage_date >= ...
   ```
   - ⚠️  这个查询应该使用view或明确指定hot表（取决于时间范围）

3. **历史数据查询** - 查询父表（自动路由到分区）
   ```go
   SELECT * FROM request_logs WHERE ts < NOW() - INTERVAL '7 days'
   ```

## 三、问题汇总

### 3.1 高优先级问题

| 问题 | 影响 | 位置 |
|-----|------|------|
| ❌ `registry/usage_stats.go` 使用 `tool_usage_stats_default` | 数据架构不一致，性能损失 | 行 37-78 |
| ⚠️ `tool_usage_stats` 和 `credit_ledger` 没有hot表迁移文件 | 架构不完整 | 缺少 348, 349 迁移 |
| ⚠️ `request_logs_bodies` 没有hot表迁移文件 | 架构不完整 | 缺少迁移 |

### 3.2 中优先级问题

| 问题 | 影响 | 位置 |
|-----|------|------|
| ⚠️ 查询操作没有明确区分hot/view/parent | 可能导致性能问题 | 多处 |
| ⚠️ 缺少完整的索引审计 | 查询性能可能不佳 | 所有hot表 |

## 四、修复方案

### 4.1 立即修复（代码层）

1. **修复 `registry/usage_stats.go`**
   ```go
   // 将 INSERT INTO tool_usage_stats_default
   // 改为 INSERT INTO tool_usage_stats_hot
   ```

2. **审计所有查询**
   - 7天内数据：查 `*_hot` 表
   - 跨月聚合：使用 `*_with_current_month` view
   - 历史数据：查父表

### 4.2 数据库迁移（需要新建迁移文件）

1. **Migration 348: tool_usage_stats_hot_independence.sql**
   - 创建 `tool_usage_stats_hot` 表
   - 迁移 `tool_usage_stats_default` 数据
   - 删除 `tool_usage_stats_default`
   - 更新 view
   - 创建 `promote_tool_usage_stats_hot_to_partition` 函数

2. **Migration 349: credit_ledger_hot_independence.sql**
   - 创建 `credit_ledger_hot` 表
   - 迁移 `credit_ledger_default` 数据
   - 删除 `credit_ledger_default`
   - 更新 view
   - 创建 `promote_credit_ledger_hot_to_partition` 函数

3. **Migration 350: request_logs_bodies_hot_independence.sql**
   - 同上模式

### 4.3 索引审计（待完成）

需要确认每个hot表都有以下索引：
- ✅ 时间戳索引（降序）
- ✅ 主键或唯一约束
- ✅ 租户+时间复合索引
- ✅ 其他业务查询字段索引

## 五、测试计划

### 5.1 单元测试
- [ ] 每个表的INSERT测试（验证写入hot表）
- [ ] 每个表的UPDATE测试（验证更新hot表）
- [ ] 每个表的DELETE测试（验证删除hot表）
- [ ] 每个表的SELECT测试（验证view聚合）

### 5.2 集成测试
- [ ] 跨月查询测试
- [ ] Promote函数测试（hot → partition）
- [ ] 性能测试（对比hot表vs _default）

### 5.3 API测试
- [ ] 遥测API写入测试
- [ ] 统计API查询测试
- [ ] Admin API数据生命周期测试

## 六、后续行动项

### 立即行动（今天）
1. ✅ 修复 `registry/usage_stats.go` 使用hot表
2. ⚠️ 审计所有查询操作
3. ⚠️ 编写测试验证

### 短期行动（本周）
1. ⚠️ 创建 tool_usage_stats_hot_independence 迁移
2. ⚠️ 创建 credit_ledger_hot_independence 迁移
3. ⚠️ 创建 request_logs_bodies_hot_independence 迁移
4. ⚠️ 更新所有相关代码

### 长期行动（本月）
1. ⚠️ 完整的索引审计和优化
2. ⚠️ 性能基准测试
3. ⚠️ 文档更新

## 七、架构设计原则总结

### 7.1 数据写入规则
```
所有 INSERT/UPDATE/DELETE 操作 → *_hot 表
```

### 7.2 数据查询规则
```
1. 明确查询热数据（0-7天）→ *_hot 表
2. 跨月聚合查询 → *_with_current_month view
3. 历史数据查询 → 父表（自动分区路由）
```

### 7.3 数据生命周期
```
新数据 → *_hot (heap, 7天)
         ↓ promote函数
历史数据 → *_YYYY_MM (columnar, 按月)
```

### 7.4 架构优势
1. **性能**: hot表heap存储，支持快速UPDATE/DELETE
2. **成本**: 历史数据columnar压缩，节省存储
3. **简洁**: 2路UNION view，避免3路UNION性能损失
4. **一致**: 所有大表统一架构，易于维护

---

**审计完成时间**: 2026-07-05  
**下次审计**: 2026-08-01（每月1号例行审计）
