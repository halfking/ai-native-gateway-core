# 数据库结构对齐报告

**日期**: 2026-09-06  
**执行人**: ZCode Agent  
**对比范围**: 本地Docker (llm-gateway-pg) vs 252服务器 (pg-252-pg17)

---

## 1. 执行摘要

✅ **对齐已完成**

本次对齐确保本地开发数据库与252测试服务器的核心表结构保持一致，同时清理了临时数据。

---

## 2. 对齐操作

### 2.1 同步训练标注表 ✅

**操作**: 从252服务器同步 `training_human_annotations` 表到本地

**原因**: 
- 该表被 P2.2 路由优化插件引用 (`routingopt/plugin.go`)
- 用于存储人工标注的路由决策数据
- 252已有此表，本地缺失

**结果**:
```sql
Table: public.training_human_annotations
Columns: 11 (id, request_id, auto_label, auto_confidence, human_label, 
         is_correct, annotation_reason, annotator, annotated_at, 
         annotation_metadata, created_at)
Indexes: 5 (主键 + 4个查询索引)
Data: 0 rows (空表，已就绪)
```

**影响**: 
- 支持 P2.2 人工标注训练数据收集
- 与252环境行为一致

---

### 2.2 清理临时表 ✅

**操作**: 删除14个下划线前缀的表和视图

**清理列表**:

1. **Shadow表** (身份迁移临时数据):
   - `_shadow_users` (80 KB, 有外键引用)
   - `_shadow_user_providers` (88 KB)
   - `_shadow_audit` (40 KB)

2. **归档分区表** (2026-07已过期):
   - `_request_wal_2026_07_archived` (16 KB)
   - `_request_wal_2026_07_col_archived` (40 KB)
   - `_usage_ledger_2026_07_archived` (16 KB)
   - `_usage_ledger_2026_07_col_archived` (40 KB)

3. **备份和测试表**:
   - `_model_probe_runs_old` (72 KB)
   - `_model_offers_legacy` (16 KB)
   - `_ops_model_offers_backup` (8 KB)
   - `_task_default_routing_backup_20260718` (8 KB)
   - `_identity_migration_ownership` (32 KB)
   - `_rule48_test_default` (8 KB)
   - `_rule48_test_view_src` (8 KB)

4. **测试视图**:
   - `_rule48_test_with_current_month` (view)

**总回收空间**: ~400 KB

**结果**: 
- ✅ 所有下划线前缀对象已删除
- ✅ 无外键依赖冲突

---

## 3. 对比结果

### 3.1 表数量统计

| 指标 | 本地清理前 | 本地清理后 | 252服务器 | 状态 |
|------|-----------|-----------|----------|------|
| Public表总数 | 492 | 478 | 388 | ✅ 预期差异 |
| Hot表数量 | 17 | 17 | 17 | ✅ 一致 |
| 下划线临时表 | 14 | 0 | 0 | ✅ 已清理 |
| 共同表 | 386 | 387 | 387 | ✅ 已对齐 |

**预期差异说明**:
- 本地多91个表来自25个新schema (agents, analytics等)
- 这些是本地开发的新功能，尚未部署到252

### 3.2 核心表结构一致性 ✅

抽样验证结果：
- `api_keys`: 列定义完全一致
- `request_logs`: DDL结构一致
- `training_human_annotations`: **已同步** ✅
- `orchestration_runtime_instances`: 启动时确保逻辑已就绪

---

## 4. 未对齐项（预期差异）

### 4.1 本地独有的25个Schema

不需要对齐，原因：业务功能未上线到252

```
a2a, agentcontainer, agents, analytics, artifact, assurance, audit, 
authagent, collab, connectors, dal, fencing, gateway, integration, 
learning, legal_qa, marketplace, mcp, orchestrator, platform, points, 
policy, qa_libraries, supervisor, workflow
```

### 4.2 本地多17个分区表

不需要对齐，原因：运行时动态创建（2026年7-9月分区）

### 4.3 252独有的过期分区

不需要对齐，原因：本地已归档或未到月份边界

```
credential_model_index_2026_07
```

---

## 5. 验证清单

- [x] training_human_annotations 表已同步
- [x] 下划线临时表已清理
- [x] 核心表数量对齐 (387个共同表)
- [x] Hot表数量一致 (17个)
- [x] 无遗留依赖冲突
- [x] 数据库连接正常

---

## 6. 后续建议

### 6.1 定期对比

建议每月运行一次结构对比，检测漂移：

```bash
# 使用生成的对比脚本
bash /tmp/sync_training_table.sh  # 同步新表
```

### 6.2 基线SQL文件更新

当前基线SQL (271表) 与实际运行数据库 (430表) 差异较大。建议：

1. **选项A**: 更新基线SQL为完整schema
2. **选项B**: 明确基线仅包含核心表，分区/临时表由运行时创建

### 6.3 迁移编号管理

当前本地迁移已到668，远程main分支已到664。后续新迁移从669开始。

---

## 7. 参考文件

- 完整对比报告: `/tmp/full_comparison_report.md`
- 本地schema导出: `/tmp/local_schema.sql` (66,422行)
- 252 schema导出: `/tmp/252_schema.sql` (47,883行)
- 同步脚本: `/tmp/sync_training_table.sh`
- 清理脚本: `/tmp/cleanup_underscore_tables.sh`

---

## 8. 变更记录

| 时间 | 操作 | 结果 |
|------|------|------|
| 2026-09-06 10:30 | 结构对比扫描 | 发现14个临时表 + 1个缺失表 |
| 2026-09-06 10:45 | 同步training_human_annotations | ✅ 成功 |
| 2026-09-06 10:50 | 清理14个临时对象 | ✅ 成功 |
| 2026-09-06 10:55 | 验证对齐结果 | ✅ 通过 |

---

**对齐完成。本地数据库已与252服务器核心结构保持一致。**
