# Session Bodies Hot 表修复总结

## 问题描述

`session_bodies` 表直接写入月分区表，违反了系统架构原则：
- 所有大数据表必须采用 hot+分区架构
- Hot 表保留 8 小时数据，支持快速 UPDATE/DELETE
- 批量 promote 到 columnar 分区表

## 修复内容

### 1. SQL Migrations

**Migration 614: session_bodies_hot 表**
- 文件：`sql/migrations/startup/614_session_bodies_hot.sql`
- 创建 `session_bodies_hot` 表（与分区表结构一致）
- 添加主键、唯一约束和索引
- 创建 `session_bodies_unified` 视图（hot + 分区联合查询）

**Migration 615: Promote 函数**
- 文件：`sql/migrations/startup/615_session_bodies_hot_promote_function.sql`
- 实现 `promote_session_bodies_hot_to_partition(interval, integer)` 函数
- 支持批量原子迁移（默认 5000 行/批次）
- 使用 advisory lock 防止并发冲突
- 使用 FOR UPDATE SKIP LOCKED 避免阻塞

### 2. Go 代码修改

**domains/session/v2/bodies_writer.go**
- ✅ 写入路径改为 `session_bodies_hot` (行281)
- ✅ 所有读取路径改为 `session_bodies_unified` 视图 (行340, 415, 466)
- ✅ 添加注释说明架构和 promote 机制

**bg/partition_manager.go**
- ✅ 添加 `session_bodies_hot` 到 `promoteSpecs()` (行766)
- PartitionManager 每小时自动执行 promote

### 3. 数据流

```
写入：session_bodies_hot (8h 窗口)
  ↓ (PartitionManager 每小时 promote)
月分区：session_bodies_YYYY_MM (columnar 压缩)

读取：session_bodies_unified 视图
  - 自动合并 hot 表和分区表数据
  - 透明切换，无需修改上层代码
```

## 验证结果

- ✅ `go test ./domains/session/v2` 通过
- ✅ `go test ./bg` 通过
- ✅ `go build ./...` 编译成功
- ✅ 与 session_turns_hot 架构一致

## 部署步骤

### Staging 环境

1. 执行 migration 614 和 615
   ```bash
   psql -f sql/migrations/startup/614_session_bodies_hot.sql
   psql -f sql/migrations/startup/615_session_bodies_hot_promote_function.sql
   ```

2. 验证表和函数创建
   ```sql
   \d session_bodies_hot
   \d+ session_bodies_unified
   SELECT promote_session_bodies_hot_to_partition('8 hours', 100);
   ```

3. 部署新代码，观察写入和读取

4. 等待 1 小时，确认 PartitionManager 自动 promote

5. 验证数据完整性
   ```sql
   -- 检查 hot 表数据量（应该 < 8小时）
   SELECT count(*), max(ts), min(ts) FROM session_bodies_hot;
   
   -- 检查视图能查到所有数据
   SELECT count(*) FROM session_bodies_unified;
   ```

### 生产环境

1. 选择低峰期（避免影响写入）

2. 在 readonly 副本上先执行，验证 migration 无误

3. 在主库执行 migration 614 和 615

4. 部署代码（滚动更新，逐个实例）

5. 监控指标：
   - `session_bodies_hot` 表大小
   - Promote 成功次数和耗时
   - 查询延迟（unified view）

## 回滚计划

如果发现问题，可以回滚：

```sql
-- 1. 修改代码，改回直接写 session_bodies
-- 2. 合并 hot 表数据到分区表
INSERT INTO session_bodies 
SELECT * FROM session_bodies_hot 
ON CONFLICT DO NOTHING;

-- 3. 执行 down migration
\i sql/migrations/startup/615_session_bodies_hot_promote_function.down.sql
\i sql/migrations/startup/614_session_bodies_hot.down.sql
```

## 风险评估

- **低风险**：架构变更透明，读取通过统一视图
- **中等复杂度**：需要 migration + 代码同步部署
- **可回滚**：提供完整的 down migration
- **已验证**：与现有 session_turns_hot 架构完全一致

## 后续优化

- [ ] 添加 Prometheus 指标监控 promote 操作
- [ ] 配置告警规则（hot 表数据堆积）
- [ ] 性能测试：对比直接写分区表 vs hot 表的吞吐量
- [ ] 文档更新：架构图和运维手册

---

**修复日期**：2026-08-29  
**修复作者**：AI Assistant  
**关联审计**：docs/audit/2026-08-29-comprehensive-24h-audit.md
