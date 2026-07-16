# 252服务器数据库实时分析报告（更新版）

**报告时间**: 2026-07-16 23:10
**数据来源**: 252服务器实时查询 (docker exec pg-252-pg17)
**数据库**: llm_gateway (115.29.212.252:172.16.2.210:5432)

---

## 🚨 关键发现：数据量从119MB激增到12GB！

### 对比：备份 vs 实时

| 指标 | 2026-07-10备份 | 2026-07-16实时 | 增长 |
|------|---------------|---------------|------|
| **总数据库大小** | 119 MB | **12 GB** | **100倍增长** ⚠️ |
| **总记录数** | 756,640 | 约137万+ | 1.8倍 |
| **最大表** | model_probe_runs (176.9MB) | columnar_internal.chunk (10GB) | - |

---

## 1. 实时数据库规模

### 1.1 按Schema统计

| Schema | 表数量 | 总大小 | 占比 |
|--------|-------|-------|------|
| **columnar_internal** | 4 | **11 GB** | **91.7%** 🔴 |
| **public** | 235 | 832 MB | 6.9% |
| pg_catalog | 81 | 484 MB | 4.0% |
| information_schema | 4 | 248 KB | <0.1% |

**⚠️ 核心问题**：columnar_internal占了91.7%的空间，这是Citus列存储的内部表。

### 1.2 Top 20 最大表（实时）

| 排名 | 表名 | 总大小 | 表大小 | 索引大小 | 行数 |
|------|------|-------|-------|---------|------|
| 1 | columnar_internal.chunk | 10072 MB | - | - | 44,765 |
| 2 | columnar_internal.stripe | 651 MB | - | - | 2,060 |
| 3 | columnar_internal.chunk_group | 420 MB | - | - | 4,401 |
| 4 | request_logs_hot | 353 MB | 2.1 MB | 351 MB | 1,429 |
| 5 | handoff_logs | 235 MB | 3.6 MB | 232 MB | 5,831 |
| 6 | credential_model_index_2026_07 | 55 MB | 2.9 MB | 52 MB | 837,919 |
| 7 | routing_decision_log_2026_07 | 21 MB | 4.4 MB | 17 MB | 69,269 |
| 8 | usage_ledger_2026_07 | 19 MB | 6.4 MB | 13 MB | 41,730 |
| 9 | candidate_failure_logs | 18 MB | 16 MB | 1.9 MB | 40,954 |
| 10 | request_wal_hot | 14 MB | 7.2 MB | 7.0 MB | 27,599 |
| 11 | credential_model_index_2026_06 | 14 MB | 1.5 MB | 12 MB | 194,842 |
| 12 | request_wal_2026_07 | 14 MB | 6.0 MB | 8.0 MB | 27,379 |
| 13 | self_check_round_results | 8.0 MB | 7.6 MB | 440 KB | 9,814 |
| 14 | credential_model_call_history | 7.9 MB | 3.3 MB | 4.6 MB | 23,446 |
| 15 | credential_model_index_hot | 7.2 MB | 288 KB | 6.9 MB | - |

---

## 2. 数据增长分析

### 2.1 与备份对比（7月10日 → 7月16日，6天变化）

| 表名 | 备份行数 | 实时行数 | 增长 | 日均增长 |
|------|---------|---------|------|---------|
| credential_model_index_2026_07 | - | 837,919 | 新增 | ~56k/天 |
| credential_model_index_2026_06 | - | 194,842 | - | - |
| routing_decision_log_2026_07 | - | 69,269 | 新增 | ~11.5k/天 |
| usage_ledger_2026_07 | - | 41,730 | 新增 | ~7k/天 |
| candidate_failure_logs | 39,772 | 40,954 | +1,182 | +197/天 |
| handoff_logs | 1,393 | 5,831 | +4,438 | +740/天 |

### 2.2 最活跃表（操作统计）

| 表名 | 插入 | 更新 | 删除 | 净活跃度 |
|------|------|------|------|---------|
| **columnar_internal.chunk** | 13,035,808 | 0 | **93,798,934** | **106M操作** 🔴 |
| **columnar_internal.stripe** | 1,002,205 | 0 | **9,698,581** | **10.7M操作** 🔴 |
| **columnar_internal.chunk_group** | 1,002,201 | 0 | **9,698,584** | **10.7M操作** 🔴 |
| assets | 6 | 1,526,639 | 1 | 1.5M操作 |
| credential_model_bindings | 4 | 280,207 | 0 | 280K操作 |
| provider_models | 2 | 279,517 | 0 | 280K操作 |

**⚠️ 核心问题**：columnar内部表有**93M次删除操作**，这表明columnar表的数据迁移/清理非常频繁！

---

## 3. Columnar存储问题分析

### 3.1 什么是columnar_internal？

- **Citus列存储引擎的内部表**
- 用于存储columnar格式的表数据
- 包含：chunk（数据块）、stripe（条带）、chunk_group（块组）

### 3.2 为什么占用11GB？

根据操作统计：
1. **93M次删除操作** - 说明有大量columnar表数据被删除但空间未回收
2. **13M次插入操作** - 持续写入columnar表
3. **VACUUM未及时回收** - columnar表的VACUUM需要特殊处理

### 3.3 哪些表使用了columnar？

从schema分析，以下表可能使用columnar存储：
- `request_logs_*` 系列（分区表）
- `usage_ledger_*` 系列
- `request_wal_*` 系列
- `routing_decision_log_*` 系列

这些都是**日志/审计类高写入表**，使用columnar压缩存储。

---

## 4. 时间序列数据分析

### 4.1 数据时间范围

| 表名 | 最早时间 | 最新时间 | 跨度 | 状态 |
|------|---------|---------|------|------|
| request_logs_hot | 2026-07-15 22:55 | 2026-07-16 23:06 | 1天 | 活跃 |
| handoff_logs | 2026-07-09 12:55 | 2026-07-12 12:46 | 3天 | 历史 |
| credential_model_index_2026_07 | 2026-07-01 08:00 | 2026-07-15 22:20 | 15天 | 当月 |
| candidate_failure_logs | 2026-06-23 17:34 | 2026-06-26 02:25 | 3天 | 旧数据 |

### 4.2 分区表状态

**request_logs系列**：
- request_logs_hot: 353 MB ✅ 活跃
- request_logs_2026_08: 312 KB (预创建)
- request_logs_2026_07: 296 KB (本月)

**credential_model_index系列**：
- credential_model_index_2026_07: 55 MB, 838K行 ✅ 主力
- credential_model_index_2026_06: 14 MB, 195K行 (上月)

---

## 5. 🚨 严重问题与风险

### 5.1 P0 紧急问题

1. **Columnar空间膨胀**（11GB / 12GB = 91.7%）
   - 风险：磁盘很快耗尽
   - 原因：VACUUM FULL未执行或不生效
   - 影响：数据库性能下降，可能OOM

2. **删除操作未回收**（93M次删除）
   - columnar表的删除不会立即释放空间
   - 需要手动VACUUM FULL或ALTER TABLE重建

3. **索引膨胀**
   - request_logs_hot: 351MB索引 vs 2.1MB数据（167倍）
   - handoff_logs: 232MB索引 vs 3.6MB数据（64倍）

### 5.2 P1 中高风险

1. **历史数据未归档**
   - candidate_failure_logs: 6月23-26日的数据还在（3周前）
   - handoff_logs: 7月9-12日数据（235MB）

2. **分区表未自动清理**
   - 2026_06月分区还保留14-55MB数据
   - 应该归档或删除

---

## 6. 建议行动方案

### 6.1 立即执行（今晚）

1. **查看磁盘空间**
   ```bash
   ssh root@115.29.212.252 "df -h"
   ```

2. **手动VACUUM columnar表**
   ```sql
   -- 查看哪些表使用了columnar
   SELECT tablename FROM pg_tables 
   WHERE schemaname='public' 
   AND tablename IN (
       SELECT tablename FROM columnar.options
   );
   
   -- 对columnar表执行VACUUM
   VACUUM FULL request_logs_hot;
   VACUUM FULL request_logs_2026_07;
   ```

3. **清理旧数据**
   ```sql
   -- 删除3周前的失败日志
   DELETE FROM candidate_failure_logs 
   WHERE ts < NOW() - INTERVAL '14 days';
   
   -- 归档或删除旧handoff_logs
   DELETE FROM handoff_logs 
   WHERE created_at < NOW() - INTERVAL '7 days';
   ```

### 6.2 短期方案（本周内）

1. **重建索引**
   ```sql
   REINDEX TABLE request_logs_hot;
   REINDEX TABLE handoff_logs;
   ```

2. **设置自动VACUUM**
   ```sql
   ALTER TABLE request_logs_hot SET (
     autovacuum_vacuum_scale_factor = 0.05,
     autovacuum_vacuum_threshold = 1000
   );
   ```

3. **归档6月分区**
   ```sql
   -- 导出后删除
   -- pg_dump -t credential_model_index_2026_06 ...
   DROP TABLE credential_model_index_2026_06 CASCADE;
   ```

### 6.3 中期方案（2周内）

1. **实施自动归档策略**
   - 日志表保留14天
   - 索引表保留30天
   - 定时任务每天清理

2. **监控columnar空间**
   - 每天统计columnar_internal大小
   - 阈值告警（> 5GB）

3. **优化columnar设置**
   - 调整chunk_group_row_limit
   - 调整stripe_row_limit
   - 考虑是否需要columnar（某些表可能不需要）

---

## 7. 容量规划

### 7.1 当前增长速率

基于6天数据（7/10 → 7/16）：
- 总数据库：119MB → 12GB = **+11.9GB**
- 日均增长：**~2.0 GB/天**
- 主要来源：columnar_internal

### 7.2 预测

| 时间 | 预计大小 | 风险等级 |
|------|---------|---------|
| 1周后 | ~26 GB | 🟡 中 |
| 2周后 | ~40 GB | 🟠 高 |
| 1个月 | ~72 GB | 🔴 严重 |

**⚠️ 如果不清理，1个月内可能超过磁盘容量！**

---

## 8. 对比总结

### 备份 vs 实时对比表

| 维度 | 7月10日备份 | 7月16日实时 | 变化 |
|------|-----------|-----------|------|
| 总大小 | 119 MB | 12 GB | **+10,000%** |
| 最大表 | model_probe_runs (177MB) | columnar_internal.chunk (10GB) | 不同表 |
| public表总计 | ~119 MB | 832 MB | +7倍 |
| 行数(credential_model_index) | - | 838K (7月) | 新增 |
| 索引膨胀 | 未观察 | 严重（167倍） | 新问题 |

---

## 9. 下一步行动

**P0（今晚必做）**：
- [ ] 检查磁盘空间
- [ ] 手动VACUUM columnar表
- [ ] 清理candidate_failure_logs旧数据

**P1（明天）**：
- [ ] 重建索引
- [ ] 设置自动VACUUM参数
- [ ] 归档6月分区

**P2（本周）**：
- [ ] 实施自动归档策略
- [ ] 建立监控告警
- [ ] 审查columnar使用必要性

---

*报告生成时间: 2026-07-16 23:10*
*下次更新: 清理后再次运行分析*
