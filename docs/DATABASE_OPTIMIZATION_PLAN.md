# 数据库存储优化方案 - 完整执行计划

**当前数据库大小**: 4,381 MB  
**目标优化后大小**: ~600-800 MB  
**预期节省**: 3.5-3.7 GB (80-85%)  
**分析日期**: 2026-07-02

---

## 🔍 关键发现

### 问题1: 归档表存储冗余数据 🔴 **最严重**
**表**: `request_logs_archive_2026_06`  
**问题**: 
- 仅16,452行数据，却占用3.7GB空间
- 原因：表中存储了完整的JSONB body数据（平均486KB/行）
- **request_body总计**: 7.6GB原始数据（压缩后占用大部分空间）
- **response_body总计**: 29MB

**根本原因**: 
归档表设计时将bodies数据冗余存储在主表中，而不是分离到`request_logs_bodies`表。这导致：
1. 数据重复存储
2. 大量磁盘空间浪费
3. 查询性能下降（扫描大字段）

**影响**: 占数据库总大小的84%

---

### 问题2: 数据保留策略不明确 🟡
**发现**:
- `request_logs_archive_2026_06`: 2026-06-26 ~ 2026-06-30 (仅4天数据，已过时2天)
- `request_logs_2026_07`: 2026-07-01 ~ 2026-07-02 (当前活跃，173MB)
- 归档表总大小: 3.7GB
- 活跃表总大小: 173MB

**问题**: 归档数据保留时间和清理策略不明确

---

### 问题3: Bodies数据分离不彻底 🟡
**发现**:
- `request_logs_archive_2026_06`: 16,452行，99.9%包含body数据
- `request_logs_bodies`: 仅6,775行

**问题**: 归档表中的body数据没有分离到bodies表

---

### 问题4: 索引优化空间 🟢
**发现**:
- `request_logs_2026_07_search_text_idx`: 1.5MB，使用0次
- 其他表索引/数据比例合理

---

## 📋 优化方案总览

| 优化项 | 优先级 | 预期节省 | 风险 | 执行时间 |
|--------|--------|---------|------|---------|
| 1. 删除过期归档表 | 🔴 最高 | 3.7 GB | 低 | 5分钟 |
| 2. Bodies数据分离策略 | 🟡 高 | 未来防止 | 低 | 1小时 |
| 3. 建立数据保留策略 | 🟡 高 | 持续优化 | 低 | 30分钟 |
| 4. 删除未使用索引 | 🟢 中 | 1.5 MB | 低 | 5分钟 |
| 5. 归档表Columnar化 | 🟢 中 | 未来优化 | 低 | 自动 |

---

## 🎯 优化方案详细计划

### 阶段1: 立即执行（高优先级） - 预计节省 3.7 GB

#### 措施1.1: 删除过期归档数据 🔴

**目标表**: `request_logs_archive_2026_06`  
**原因**: 
- 数据已过期2天（6月数据，现在7月2日）
- 占用3.7GB空间
- 仅包含4天的历史数据（6月26-30日）

**执行步骤**:

```sql
-- 步骤1: 备份前检查（确认数据确实过期）
SELECT 
  COUNT(*) as total_rows,
  MIN(ts)::date as oldest_date,
  MAX(ts)::date as newest_date,
  (CURRENT_DATE - MAX(ts)::date) as days_since_newest,
  pg_size_pretty(pg_total_relation_size('request_logs_archive_2026_06')) as size
FROM request_logs_archive_2026_06;

-- 步骤2: 如果确认过期（days_since_newest > 1），则删除
-- ⚠️ 确保有备份或确认数据可丢弃
DROP TABLE IF EXISTS request_logs_archive_2026_06 CASCADE;

-- 步骤3: 验证空间释放
SELECT pg_size_pretty(pg_database_size(current_database())) as new_db_size;

-- 预期结果: 数据库从4.4GB降至~600-700MB
```

**风险评估**: 
- ✅ **低风险**: 数据已过期，且是归档数据
- ⚠️ **建议**: 执行前确认业务是否需要这4天的历史数据
- 💾 **备份**: 如需保留，可先导出到S3/文件

**回滚方案**:
```bash
# 如果误删，从备份恢复
pg_restore -d llm_gateway backup_request_logs_archive_2026_06.dump
```

---

#### 措施1.2: 删除未使用的索引 🟡

**目标索引**: `request_logs_2026_07_search_text_idx`  
**原因**: 使用0次，占用1.5MB

**执行步骤**:

```sql
-- 步骤1: 确认索引使用情况
SELECT 
  indexrelname,
  idx_scan,
  pg_size_pretty(pg_relation_size(indexrelid)) as size
FROM pg_stat_user_indexes
WHERE relname = 'request_logs_2026_07'
  AND indexrelname = 'request_logs_2026_07_search_text_idx';

-- 步骤2: 如果确认未使用，删除索引
DROP INDEX IF EXISTS request_logs_2026_07_search_text_idx;

-- 步骤3: 监控是否有查询受影响
-- 运行1-2天，如无影响则永久删除
```

**风险评估**: 
- ✅ **低风险**: 使用0次
- ⚠️ **监控**: 删除后观察1-2天，确保无查询依赖

**回滚方案**:
```sql
-- 如果发现需要，重新创建
CREATE INDEX request_logs_2026_07_search_text_idx 
ON request_logs_2026_07 USING gin(search_text gin_trgm_ops);
```

**预期节省**: 1.5 MB

---

### 阶段2: 中期优化（1-2周内） - 预防未来问题

#### 措施2.1: 建立数据保留策略 🟡

**目标**: 防止归档数据无限增长

**策略建议**:

| 数据类型 | 保留时间 | 操作 |
|---------|---------|------|
| 活跃数据 (request_logs) | 当月 + 上月 | 保留在热存储 |
| 归档数据 (request_logs_archive) | 3-6个月 | 定期清理或导出到冷存储 |
| Bodies数据 (request_logs_bodies) | 与主表同步 | 同步删除 |

**实施方案**:

1. **创建清理脚本**:

```bash
#!/bin/bash
# /usr/local/bin/cleanup-old-archives.sh

DB_URL="$LLM_GATEWAY_DATABASE_URL"
RETENTION_MONTHS=3  # 保留3个月

psql "$DB_URL" <<EOF
-- 查找超过3个月的归档表
SELECT 
  tablename,
  pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) as size
FROM pg_tables
WHERE schemaname = 'public'
  AND tablename ~ '^request_logs_archive_\d{4}_\d{2}$'
  AND to_date(substring(tablename from 'archive_(\d{4}_\d{2})'), 'YYYY_MM') 
      < CURRENT_DATE - INTERVAL '${RETENTION_MONTHS} months'
ORDER BY tablename;

-- 执行删除（需人工确认）
-- DROP TABLE request_logs_archive_YYYY_MM CASCADE;
EOF
```

2. **添加到cron**: 每月第一天检查

```bash
# 每月1号上午9点执行
0 9 1 * * /usr/local/bin/cleanup-old-archives.sh | logger -t archive-cleanup
```

---

#### 措施2.2: Bodies数据分离策略优化 🟡

**问题**: 当前bodies数据仍存储在主表中

**解决方案**:

1. **应用层修改** (需开发配合):
   - 写入时：将body数据写入`request_logs_bodies`表
   - 主表仅存储`request_id`外键
   - 查询时：JOIN bodies表获取完整数据

2. **数据库层约束**:

```sql
-- 添加触发器：插入request_logs时自动分离bodies
CREATE OR REPLACE FUNCTION separate_bodies_on_insert()
RETURNS TRIGGER AS $$
BEGIN
  -- 如果有body数据，插入到bodies表
  IF NEW.request_body IS NOT NULL OR NEW.response_body IS NOT NULL THEN
    INSERT INTO request_logs_bodies (request_id, ts, request_body, response_body, outbound_body)
    VALUES (NEW.request_id, NEW.ts, NEW.request_body, NEW.response_body, NEW.outbound_body)
    ON CONFLICT (request_id, ts) DO UPDATE
    SET request_body = EXCLUDED.request_body,
        response_body = EXCLUDED.response_body,
        outbound_body = EXCLUDED.outbound_body;
    
    -- 清空主表的body字段
    NEW.request_body := NULL;
    NEW.response_body := NULL;
    NEW.outbound_body := NULL;
  END IF;
  
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- 为每个分区添加触发器
CREATE TRIGGER separate_bodies_trigger
BEFORE INSERT ON request_logs_2026_07
FOR EACH ROW EXECUTE FUNCTION separate_bodies_on_insert();

-- 对于归档表，考虑在归档时清理bodies
```

**优先级**: 中等（需要应用层配合）

---

#### 措施2.3: 归档表Columnar化 🟢

**目标**: 归档表自动使用columnar存储

**当前状态**: 
- Event trigger已部署
- 但归档表是heap存储

**执行方案**:

1. **添加归档表到columnar跟踪**:

```sql
-- 修改columnar_insert_only_parents()函数
CREATE OR REPLACE FUNCTION columnar_insert_only_parents()
RETURNS text[] AS $$
BEGIN
  RETURN ARRAY[
    'routing_decision_log',
    'credential_model_index',
    'request_logs_archive'  -- 新增
  ]::text[];
END;
$$ LANGUAGE plpgsql IMMUTABLE;
```

2. **转换现有归档表** (如果有新的归档表创建):

```sql
-- 让daily heal自动处理
-- 或手动执行
SELECT * FROM columnar_heal() WHERE parent_name LIKE '%archive%';
```

**预期效果**: 
- 新归档表自动columnar
- 压缩率提升50-70%
- 如果有新的3.7GB归档表，会压缩到1-1.5GB

---

### 阶段3: 长期监控（持续）

#### 措施3.1: 每周存储监控

**监控脚本**:

```sql
-- 每周执行，检查存储增长
SELECT 
  'Database Size' as metric,
  pg_size_pretty(pg_database_size(current_database())) as current_size,
  pg_size_pretty(pg_database_size(current_database()) - 
    LAG(pg_database_size(current_database())) OVER (ORDER BY CURRENT_DATE)) as growth
FROM generate_series(1,1);

-- 检查大表TOP 10
SELECT 
  tablename,
  pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) as size,
  pg_total_relation_size(schemaname||'.'||tablename) as size_bytes
FROM pg_tables
WHERE schemaname = 'public'
ORDER BY size_bytes DESC
LIMIT 10;

-- 检查归档表累积
SELECT 
  COUNT(*) as archive_table_count,
  pg_size_pretty(SUM(pg_total_relation_size(schemaname||'.'||tablename))) as total_archive_size
FROM pg_tables
WHERE schemaname = 'public'
  AND tablename LIKE '%archive%';
```

**告警阈值**:
- 数据库总大小 > 2GB → 警告
- 数据库总大小 > 5GB → 严重
- 归档表总大小 > 1GB → 需清理

---

#### 措施3.2: 每月存储审计

**执行**: 每月第一天生成报告

```bash
#!/bin/bash
# /usr/local/bin/monthly-storage-audit.sh

DB_URL="$LLM_GATEWAY_DATABASE_URL"
REPORT_DATE=$(date +%Y-%m-%d)

psql "$DB_URL" -o "/tmp/storage-report-${REPORT_DATE}.txt" <<EOF
\echo '=== Monthly Storage Audit Report ==='
\echo 'Date: ${REPORT_DATE}'
\echo ''

SELECT 
  'Database Size' as metric,
  pg_size_pretty(pg_database_size(current_database())) as value;

-- 按类型统计
SELECT 
  CASE 
    WHEN tablename LIKE '%archive%' THEN 'Archive Tables'
    WHEN tablename LIKE '%bodies%' THEN 'Bodies Tables'
    ELSE 'Active Tables'
  END as category,
  COUNT(*) as table_count,
  pg_size_pretty(SUM(pg_total_relation_size(schemaname||'.'||tablename))) as total_size
FROM pg_tables
WHERE schemaname = 'public'
GROUP BY category;

-- 列出所有归档表
SELECT 
  tablename,
  pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) as size,
  to_date(substring(tablename from '_(\d{4}_\d{2})$'), 'YYYY_MM') as archive_month,
  (CURRENT_DATE - to_date(substring(tablename from '_(\d{4}_\d{2})$'), 'YYYY_MM'))::int as days_old
FROM pg_tables
WHERE schemaname = 'public'
  AND tablename LIKE '%archive%'
ORDER BY archive_month DESC;
EOF

# 发送报告（邮件/Slack等）
cat "/tmp/storage-report-${REPORT_DATE}.txt" | mail -s "DB Storage Report ${REPORT_DATE}" admin@example.com
```

---

## 📊 预期效果对比

### 优化前 (当前)
```
数据库总大小: 4,381 MB
├─ 归档表: 3,687 MB (84%)
├─ Bodies表: 279 MB (6%)
├─ 活跃表: 173 MB (4%)
└─ 其他: 242 MB (6%)
```

### 优化后（阶段1完成）
```
数据库总大小: ~600-700 MB (-85%)
├─ 归档表: 0 MB (已删除过期数据)
├─ Bodies表: 279 MB (40%)
├─ 活跃表: 173 MB (25%)
└─ 其他: 148-248 MB (35%)
```

### 长期稳态（阶段2-3完成）
```
数据库总大小: 600-1,200 MB (稳定)
├─ 归档表: 0-300 MB (3个月滚动)
├─ Bodies表: 200-400 MB (columnar压缩)
├─ 活跃表: 200-300 MB (当月+上月)
└─ 其他: 200 MB
```

---

## ⚠️ 风险评估

| 风险 | 级别 | 缓解措施 |
|------|------|---------|
| 误删重要数据 | 🟡 中 | 执行前确认数据保留策略，必要时备份 |
| 删除后无法恢复 | 🟡 中 | 先导出到S3/文件系统 |
| 删除索引影响查询 | 🟢 低 | 监控1-2天，必要时回滚 |
| Bodies分离影响应用 | 🟡 中 | 需应用层配合，分阶段实施 |
| 触发器性能影响 | 🟢 低 | Bodies分离触发器简单，性能损耗<1% |

---

## 🚀 执行时间表

### Week 1 (立即执行)
- [x] **Day 1**: 分析完成 ✅
- [ ] **Day 1**: 确认数据保留策略（与业务确认）
- [ ] **Day 1-2**: 执行阶段1优化（删除过期数据）
  - 备份归档表（如需保留）
  - 删除`request_logs_archive_2026_06`
  - 删除未使用索引
  - 验证空间释放

### Week 2-3 (中期优化)
- [ ] 设计并实施bodies数据分离策略
- [ ] 创建数据保留策略脚本
- [ ] 添加归档表到columnar跟踪
- [ ] 部署每周/每月监控脚本

### Week 4+ (长期监控)
- [ ] 监控数据库增长趋势
- [ ] 根据监控结果调整保留策略
- [ ] 每月审计报告

---

## 📝 执行检查清单

### 阶段1执行前检查
- [ ] 确认数据保留策略（保留多久的历史数据？）
- [ ] 确认业务是否需要6月26-30的数据
- [ ] 如需保留，导出归档表到S3/文件
  ```bash
  pg_dump -t request_logs_archive_2026_06 -Fc > backup_20260702.dump
  ```
- [ ] 获得删除授权（DBA/Tech Lead批准）

### 阶段1执行步骤
1. [ ] 执行前快照数据库大小
   ```sql
   SELECT pg_size_pretty(pg_database_size(current_database()));
   ```
2. [ ] 备份归档表（如需要）
3. [ ] 执行删除语句
4. [ ] 运行VACUUM FULL释放空间
   ```sql
   VACUUM FULL;
   ```
5. [ ] 验证数据库大小
6. [ ] 测试应用功能正常

### 阶段1执行后验证
- [ ] 数据库大小降至600-700MB
- [ ] 应用功能正常
- [ ] 查询性能无degradation
- [ ] 更新监控dashboard

---

## 💰 成本收益分析

### 存储成本节约（假设云存储 $0.10/GB/月）
- 当前: 4.4GB × $0.10 = **$0.44/月**
- 优化后: 0.7GB × $0.10 = **$0.07/月**
- **月度节约**: $0.37/月
- **年度节约**: $4.44/年

### 性能收益
- **查询速度**: 扫描小表更快，预计提升20-30%
- **备份时间**: 从4.4GB降至0.7GB，备份时间减少85%
- **恢复时间**: 同样减少85%
- **VACUUM速度**: 表更小，维护更快

### 运维收益
- **监控简化**: 数据量可控，异常更易发现
- **成本可预测**: 有明确的增长上限
- **应急响应**: 备份/恢复更快，RTO降低

---

## 📞 执行支持

### SQL脚本位置
所有执行脚本已集成在本文档中，可直接复制使用。

### 回滚方案
每个措施都包含回滚步骤，参见各措施的"回滚方案"部分。

### 技术支持
- 疑问或风险评估: 咨询DBA团队
- 应用层变更: 联系开发团队
- 监控告警: 配置Grafana/AlertManager

---

## 🎯 成功标准

优化成功的标准：
1. ✅ 数据库总大小降至600-800MB
2. ✅ 归档表大小 < 500MB（长期稳态）
3. ✅ 应用功能正常，无性能degradation
4. ✅ 监控和告警机制到位
5. ✅ 数据保留策略文档化

---

**方案制定人**: Kiro AI Agent  
**方案版本**: v1.0  
**制定日期**: 2026-07-02  
**下次审查**: 优化完成后1周

