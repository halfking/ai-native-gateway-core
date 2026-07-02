# 数据库存储分析报告

**数据库**: llm_gateway  
**分析时间**: 2026-07-02  
**环境**: 184生产环境 (pms-test namespace)  
**PostgreSQL版本**: 带Columnar扩展

---

## 执行摘要

### 总体规模
- **数据库总大小**: 4,375 MB (4.27 GB)
- **表数据总大小**: 473 MB
- **索引总大小**: 3,903 MB (索引占总大小的89%)
- **表总数**: 154张表 (21张columnar, 133张heap)

### 存储类型分布

| 存储类型 | 表数量 | 表数据大小 | 索引大小 | 总大小 | 占比 |
|---------|--------|-----------|---------|--------|------|
| **Columnar** | 21 | 364 MB | 2.5 MB | 367 MB | 8.4% |
| **Heap** | 133 | 108 MB | 3,883 MB | 3,991 MB | 91.6% |

**关键发现**: 
- Columnar存储的表数据量是Heap的3.4倍，但索引开销极小
- Heap表的索引占其总大小的97%（索引非常重)

---

## 一、数据库整体分析

### 1.1 按Schema分组的存储统计

| Schema | 表数量 | 总大小 | 表数据大小 | 索引大小 |
|--------|--------|--------|-----------|---------|
| **public** | 163 | 4,303 MB | 448 MB | 3,855 MB |
| **columnar_internal** | 4 | 55 MB | 25 MB | 30 MB |

**说明**: 
- `public` schema包含所有业务表
- `columnar_internal` 是Columnar扩展的内部元数据表

### 1.2 表空间分布

| 表空间 | 大小 |
|--------|------|
| pg_default | 5,317 MB |
| pg_global | 947 KB |

---

## 二、TOP 30 最大的表

| 排名 | 表名 | 总大小 | 表大小 | 索引大小 | 存储类型 |
|------|------|--------|--------|---------|---------|
| 1 | request_logs_archive_2026_06 | 3,686 MB | 25 MB | 3,660 MB | heap |
| 2 | model_probe_runs | 75 MB | 69 MB | 6 MB | columnar |
| 3 | request_logs_bodies_2026_06 | 280 MB | 268 MB | 12 MB | columnar |
| 4 | request_logs_2026_07 | 168 MB | 4.5 MB | 164 MB | heap |
| 5 | usage_ledger_2026_06 | 84 MB | 7.9 MB | 76 MB | heap |
| 6 | credential_model_index_2026_06 | 42 MB | 27 MB | 14 MB | heap |
| 7 | request_wal_2026_06 | 40 MB | 3 MB | 37 MB | heap |
| 8 | usage_ledger_2026_07 | 37 MB | 3.4 MB | 34 MB | heap |
| 9 | request_logs_bodies_2026_07 | 24 MB | 11 MB | 13 MB | columnar |
| 10 | credential_model_index_2026_07 | 14 MB | 10 MB | 4 MB | heap |

**关键观察**:
1. **request_logs_archive_2026_06 异常巨大**: 3.7GB，其中索引占3.66GB（99%）
   - 这是一个严重的存储问题，需要优化索引策略
   
2. **Columnar表效率高**: 
   - `request_logs_bodies_2026_06`: 268MB数据，仅12MB索引（4.5%）
   - `model_probe_runs`: 69MB数据，6MB索引（8.7%）
   
3. **Heap表索引沉重**:
   - `request_logs_2026_07`: 4.5MB数据，164MB索引（36倍！）
   - `usage_ledger_2026_06`: 7.9MB数据，76MB索引（9.6倍）

---

## 三、存储类型对比分析

### 3.1 Heap vs Columnar 整体对比

| 指标 | Columnar | Heap | 对比 |
|------|----------|------|------|
| 表数量 | 21 | 133 | 1:6.3 |
| 表数据大小 | 364 MB | 108 MB | 3.4:1 |
| 索引大小 | 2.5 MB | 3,883 MB | 1:1553 |
| 总大小 | 367 MB | 3,991 MB | 1:10.9 |
| 平均每行字节数 | N/A | 359 bytes | - |

**结论**: 
- Columnar表虽然存储了更多数据，但总大小仅为Heap表的9%
- **索引开销是关键差异**: Columnar索引仅2.5MB，Heap索引3.88GB（差1553倍）

### 3.2 分区表详细统计

| 父表 | 分区数 | Heap分区 | Columnar分区 | 总大小 | Heap大小 | Columnar大小 |
|------|--------|---------|-------------|--------|---------|-------------|
| request_logs_bodies | 3 | 0 | 3 | 279 MB | - | 279 MB |
| credential_model_index | 4 | 4 | 0 | 66 MB | 66 MB | - |
| usage_ledger | 3 | 3 | 0 | 11 MB | 11 MB | - |
| request_logs | 3 | 3 | 0 | 5 MB | 5 MB | - |
| request_wal | 3 | 3 | 0 | 1.6 MB | 1.6 MB | - |
| routing_decision_log | 3 | 0 | 3 | 9 MB | - | 9 MB |
| routing_decision_log_archive | 2 | 0 | 2 | 984 KB | - | 984 KB |
| request_wal_archive | 1 | 0 | 1 | 592 KB | - | 592 KB |
| credential_model_index_archive | 2 | 0 | 2 | 40 KB | - | 40 KB |

**分区表策略**:
- **已迁移到Columnar**: `request_logs_bodies`, `routing_decision_log`, `*_archive`
- **仍使用Heap**: `credential_model_index`, `usage_ledger`, `request_logs`, `request_wal`

---

## 四、索引分析

### 4.1 TOP 20 最大的索引

| 排名 | 表名 | 索引名 | 大小 |
|------|------|--------|------|
| 1 | credential_model_index_2026_06 | ...bucket_credential_id_raw_mode_idx | 12 MB |
| 2 | chunk (columnar内部) | chunk_pkey | 11 MB |
| 3 | credential_model_index_2026_07 | ...bucket_credential_id_raw_mode_idx | 4.3 MB |
| 4 | request_logs_archive_2026_06 | idx_request_logs_archive_request_id | 2 MB |
| 5 | stripe (columnar内部) | stripe_pkey | 1.9 MB |
| 6 | assets | assets_pkey | 1.1 MB |
| 7 | request_logs_archive_2026_06 | idx_request_logs_archive_ts | 1 MB |
| ... | ... | ... | ... |

**索引问题**:
- `request_logs_archive_2026_06` 有大量索引（详见五.2）
- Columnar内部表的索引较大（chunk_pkey 11MB, stripe_pkey 1.9MB）

### 4.2 索引与表数据比例异常的表

| 表名 | 表大小 | 索引大小 | 索引/表比例 |
|------|--------|---------|-----------|
| request_logs_2026_07 | 4.5 MB | 164 MB | **36.4倍** |
| request_logs_archive_2026_06 | 25 MB | 3,660 MB | **146倍** |
| usage_ledger_2026_06 | 7.9 MB | 76 MB | **9.6倍** |
| request_wal_2026_06 | 3 MB | 37 MB | **12.3倍** |

**建议**: 这些表需要索引优化，考虑：
1. 删除未使用的索引
2. 合并重复的索引
3. 考虑部分索引或表达式索引

---

## 五、深度分析

### 5.1 Columnar 表的实际数据密度

通过直接查询表获取真实行数：

| 表名 | 行数 | 表大小 | 每行字节数 | 存储效率 |
|------|------|--------|-----------|---------|
| request_logs_bodies_2026_06 | 5,124 | 268 MB | **53 KB** | 高 |
| request_logs_bodies_2026_07 | 1,651 | 11 MB | 6,917 bytes | 中 |
| credential_model_index_2026_06 (heap) | 194,842 | 27 MB | 146 bytes | 优秀 |

**分析**:
- `request_logs_bodies` 的每行平均53KB，说明存储了大量JSON body数据
- Columnar对大字段（JSONB）的存储效率很高
- Heap表 `credential_model_index` 每行仅146字节，说明是小记录表

### 5.2 request_logs_archive_2026_06 问题分析

**当前状态**:
- 总大小: 3,686 MB
- 表数据: 25 MB (0.7%)
- 索引: 3,660 MB (99.3%)

**问题根源**: 索引占比过高，可能原因：
1. 表的行数可能已被清理，但索引未重建
2. 索引策略不合理（过多或过宽的索引）
3. 索引膨胀（bloat）

**建议措施**:
```sql
-- 1. 检查索引使用情况
SELECT * FROM pg_stat_user_indexes WHERE relname = 'request_logs_archive_2026_06';

-- 2. 如果是archive表且不常查询，考虑删除部分索引
-- 3. 运行REINDEX清理索引膨胀
REINDEX TABLE request_logs_archive_2026_06;

-- 4. 或者考虑转换为columnar存储
```

### 5.3 Columnar vs Heap 压缩效率估算

假设heap存储平均每行200字节（保守估计），对比columnar实际存储：

| 表名 | 行数 | Columnar实际 | Heap估算 | 压缩比 | 节省空间 |
|------|------|------------|---------|--------|---------|
| request_logs_bodies_2026_06 | 5,124 | 268 MB | 1 MB | - | - |
| request_logs_bodies_2026_07 | 1,651 | 11 MB | 323 KB | - | - |

**注**: 由于`request_logs_bodies`存储大JSON（平均53KB/行），heap的估算不适用。这类大字段数据columnar有明显优势。

---

## 六、存储优化建议

### 6.1 立即执行（高优先级）

1. **优化 request_logs_archive_2026_06 索引** 🔴
   - 当前: 3.66GB索引，25MB数据
   - 预期收益: 节省3-3.5GB存储
   - 操作: 
     ```sql
     -- 分析索引使用情况
     SELECT * FROM pg_stat_user_indexes WHERE relname = 'request_logs_archive_2026_06';
     -- 删除未使用索引或REINDEX
     ```

2. **迁移更多表到Columnar** 🟡
   - 候选表: 
     - `credential_model_index` 分区（当前66MB，可能压缩到30-40MB）
     - `usage_ledger` 分区（当前11MB）
   - 预期收益: 减少索引开销，节省10-30MB
   - 已有机制: Event trigger和daily heal自动处理

### 6.2 中期优化（中优先级）

3. **索引审计和清理** 🟡
   - 目标表: `request_logs_2026_07`, `usage_ledger_2026_06`, `request_wal_2026_06`
   - 这些表的索引/数据比例超过9倍
   - 使用工具:
     ```sql
     SELECT * FROM pg_stat_user_indexes WHERE idx_scan = 0; -- 找出未使用索引
     ```

4. **表分区策略优化** 🟢
   - 考虑为老数据启用压缩或移动到归档存储
   - Archive表可以减少索引数量（仅保留必要的查询索引）

### 6.3 长期监控（低优先级）

5. **定期存储审计** 🟢
   - 频率: 每季度
   - 监控指标:
     - 数据库总大小增长率
     - 索引/数据比例
     - Columnar vs Heap分布
   - 使用本报告的SQL脚本自动生成

6. **Columnar 覆盖率目标** 🟢
   - 当前: 21张columnar表，364MB数据
   - 目标: 将大JSON字段表、append-only日志表迁移到columnar
   - 预期: columnar数据占比从8%提升到20-30%

---

## 七、监控查询

### 7.1 数据库总体健康检查
```sql
SELECT 
  'Database Size' as metric,
  pg_size_pretty(pg_database_size(current_database())) as value
UNION ALL
SELECT 
  'Table Data Size',
  pg_size_pretty(SUM(pg_relation_size(c.oid)))
FROM pg_class c
WHERE c.relkind = 'r'
  AND c.relnamespace::regnamespace::text NOT IN ('pg_catalog', 'information_schema')
UNION ALL
SELECT 
  'Index Size',
  pg_size_pretty(SUM(pg_relation_size(c.oid)))
FROM pg_class c
WHERE c.relkind = 'i'
  AND c.relnamespace::regnamespace::text NOT IN ('pg_catalog', 'information_schema');
```

### 7.2 识别索引膨胀
```sql
SELECT 
  schemaname,
  tablename,
  indexname,
  pg_size_pretty(pg_relation_size(indexrelid)) as index_size,
  idx_scan as index_scans,
  idx_tup_read as tuples_read,
  idx_tup_fetch as tuples_fetched
FROM pg_stat_user_indexes
WHERE pg_relation_size(indexrelid) > 10485760  -- >10MB
ORDER BY pg_relation_size(indexrelid) DESC
LIMIT 20;
```

### 7.3 找出未使用的索引
```sql
SELECT 
  schemaname,
  tablename,
  indexname,
  pg_size_pretty(pg_relation_size(indexrelid)) as size,
  idx_scan as scans
FROM pg_stat_user_indexes
WHERE idx_scan = 0
  AND pg_relation_size(indexrelid) > 1048576  -- >1MB
ORDER BY pg_relation_size(indexrelid) DESC;
```

### 7.4 Columnar vs Heap 对比
```sql
SELECT 
  am.amname as storage_type,
  COUNT(*) as table_count,
  pg_size_pretty(SUM(pg_relation_size(c.oid))) as total_size
FROM pg_class c
JOIN pg_am am ON c.relam = am.oid
WHERE c.relkind = 'r'
  AND c.relnamespace::regnamespace::text NOT IN ('pg_catalog', 'information_schema')
GROUP BY am.amname
ORDER BY SUM(pg_relation_size(c.oid)) DESC;
```

---

## 八、成本节约估算

### 当前存储成本（假设）
- 总存储: 4,375 MB
- 云存储单价: $0.10/GB/月
- 当前月成本: **$0.44/月** (4.375 GB × $0.10)

### 优化后预期
| 优化项 | 节省空间 | 节省成本/月 |
|--------|---------|-----------|
| 优化request_logs_archive_2026_06索引 | 3.5 GB | $0.35 |
| 迁移更多表到Columnar | 50 MB | $0.005 |
| 清理未使用索引 | 200 MB | $0.02 |
| **总计** | **~3.75 GB** | **~$0.375/月** |

**年度节省**: $4.50/年

**注**: 虽然绝对金额较小，但相对节省达到85%，且对查询性能和备份时间有正面影响。

---

## 九、附录

### 9.1 Columnar 表清单（21张）

完整列表可通过以下查询获取：
```sql
SELECT 
  c.relname,
  pg_size_pretty(pg_relation_size(c.oid)) as size
FROM pg_class c
JOIN pg_am am ON c.relam = am.oid
WHERE am.amname = 'columnar' AND c.relkind = 'r'
ORDER BY pg_relation_size(c.oid) DESC;
```

主要columnar表：
- model_probe_runs (69 MB)
- request_logs_bodies_2026_06 (268 MB)
- request_logs_bodies_2026_07 (11 MB)
- routing_decision_log_* 分区
- credential_model_index_archive_* 分区
- routing_decision_log_archive_* 分区

### 9.2 WAL 信息
- 当前WAL位置: 4/D1EED90
- WAL文件: 00000001000000040000000D

---

## 十、总结

### 关键指标
| 指标 | 值 |
|------|-----|
| 数据库总大小 | 4,375 MB |
| 表/索引比例 | 1:8.3 (索引是表的8倍) |
| Columnar采用率 | 8.4% (按大小) |
| 最大优化机会 | request_logs_archive_2026_06 (3.7GB) |

### 健康评估
- ✅ **Columnar基础设施**: 运行良好，21张表成功迁移
- ⚠️ **索引策略**: 需要优化，部分表索引过重
- 🔴 **Archive表**: request_logs_archive_2026_06需要立即处理
- ✅ **分区策略**: 合理，按月分区有效

### 下一步行动
1. 立即调查并优化 `request_logs_archive_2026_06` 的索引
2. 运行未使用索引检测并清理
3. 继续监控columnar迁移进度（通过daily heal自动完成）
4. 每季度重新运行本报告以跟踪趋势

---

**报告生成**: 2026-07-02  
**下次审查**: 2026-10-02 (3个月后)  
**维护人**: 数据库管理团队
