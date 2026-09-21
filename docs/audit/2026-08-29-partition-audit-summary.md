# 分区存储与Hot表审计摘要（来自 agent_03a6b4be）

## 核心发现

### ✅ 架构优秀实践

项目采用成熟的 **hot+columnar分区** 双层架构：
- 13 张表完整实施 hot+分区机制
- Advisory lock 防止并发冲突
- 原子性 CTE 迁移保证数据不丢失
- 配置热加载，8小时默认保留窗口
- 批量大小可调（默认 5000 行）

### ❌ P0 问题（已修复）

**session_bodies 缺失 hot 表**
- ✅ 已在本会话修复（commit 6973a9fb8）
- 创建了 `session_bodies_hot` 表和统一视图
- 实现了 `promote_session_bodies_hot_to_partition()` 函数
- 接入 PartitionManager 调度

### ⚠️ P1 问题（待修复）

**request_logs_bodies VACUUM FULL 未自动化**
- **影响**: TOAST 膨胀可能导致空间浪费
- **现状**: 已有 TTL 清理，但未自动 VACUUM
- **修复**: 添加定期 VACUUM FULL 任务

## 完整实施 Hot+分区的表

| 表名 | Hot表 | Promote函数 | 状态 |
|------|-------|------------|------|
| request_logs | ✅ | ✅ | 完整 |
| request_logs_bodies | ✅ | ✅ | 完整（24h保留） |
| session_turns | ✅ | ✅ | 完整 (Migration 526) |
| session_bodies | ✅ | ✅ | **本次修复** |
| candidate_failure_logs | ✅ | ✅ | 完整 (Migration 392) |
| usage_ledger | ✅ | ✅ | 完整 |
| request_wal | ✅ | ✅ | 完整 |
| routing_decision_log | ✅ | ✅ | 完整 |
| credential_model_index | ✅ | ✅ | 完整 |
| credit_ledger | ✅ | ✅ | 完整 |
| tool_usage_stats | ✅ | ✅ | 完整 |
| handoff_logs | ✅ | ✅ | 完整 (Migration 532) |
| session_module_executions | ✅ | ✅ | 完整 (Migration 580) |
| dashboard_access_events | ✅ | ✅ | 完整 (Migration 579/607) |

## Hot表保留策略

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| lifecycle.hot_retention_hours | 8小时 | 所有 hot 表默认保留窗口 |
| lifecycle.request_logs_bodies_retention_hours | 24小时 | Bodies 特殊（TOAST 重载） |
| lifecycle.promote_interval_hours | 1小时 | PartitionManager 轮询间隔 |
| lifecycle.promote_batch_size | 5000行 | 每批 promote 最大行数 |

## 更新/删除合规性

- ✅ 所有 UPDATE 操作仅针对 `*_hot` 表
- ✅ 未发现对分区表的直接 DELETE
- ✅ 分区清理通过 `DROP TABLE` 实现

## 存储优化

- ✅ Columnar 表使用 LZ4 压缩
- ✅ 索引策略覆盖常见查询路径
- ✅ 统一查询视图透明合并 hot+分区数据

## 数据完整性风险评估

### 已缓解
- ✅ session_bodies_hot 已创建
- ✅ Advisory lock 防止并发冲突
- ✅ 原子性迁移保证无数据丢失

### 残留风险
- ⚠️ request_logs_bodies VACUUM FULL 未自动化（P1）
- 📋 stream_captures 表状态不明（需确认）

---

详细报告：`/Users/xutaohuang/.zcode/cli/agents/sess_15d75c70-8085-4139-9ad4-d71fff7995b2/agent_03a6b4be-aa4d-491a-84ab-034492a8d9d0/output.txt`
