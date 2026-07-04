# 分区表月度维护清单

**日期**: 2026-07-05  
**版本**: 1.0

---

## 概述

本文档定义分区表架构的月度维护任务。维护窗口建议在每月 **1 号凌晨 2:00-4:00**（低峰期）。

---

## 维护日历

| 日期 | 任务 | 预计时间 | 自动化 |
|------|------|---------|--------|
| 每月 1 号 | 更新 *_with_current_month VIEW | 10 分钟 | ❌ 手动 |
| 每月 1 号 | DETACH 上月分区 | 5 分钟 | ⚠️ 半自动 |
| 每月 1 号 | ATTACH 上月分区到归档 | 5 分钟 | ❌ 手动 |
| 每周 | 健康检查 | 5 分钟 | ✅ 脚本 |
| 每日 | promote 函数执行 | 自动 | ✅ 后台 |

---

## 任务 1：更新查询 VIEW（每月 1 号）

### 1.1 背景

当月分区 DETACHED 后，`*_with_current_month` VIEW 需要手动更新 UNION ALL 列表，加入新月份。

**示例**（8 月 1 号）：
```sql
-- 旧 VIEW（7 月）
CREATE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs
UNION ALL
SELECT * FROM request_logs_2026_07  -- 7 月
UNION ALL
SELECT * FROM request_logs_default;

-- 新 VIEW（8 月）
CREATE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs
UNION ALL
SELECT * FROM request_logs_2026_08  -- 8 月
UNION ALL
SELECT * FROM request_logs_default;
```

### 1.2 执行步骤

```bash
# 1. 连接到数据库
psql -h <host> -U kxuser -d llm_gateway

# 2. 确认当前月份
SELECT now(), to_char(now(), 'YYYY_MM');

# 3. 执行 VIEW 更新（使用 transaction）
BEGIN;

-- 8 表 × 1 条 UPDATE = 8 条
DROP VIEW IF EXISTS request_logs_with_current_month;
CREATE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs
UNION ALL
SELECT * FROM request_logs_2026_08
UNION ALL
SELECT * FROM request_logs_default;

-- 对其他 7 个表重复...

COMMIT;

# 4. 验证
SELECT viewname FROM pg_views WHERE viewname LIKE '%_with_current_month';
```

### 1.3 自动脚本（待实现）

```bash
# 计划：migration 341 将实现自动化
# ./scripts/partition/update-monthly-views.sh --month 2026_08
```

### 1.4 验证

```sql
-- 测试跨月查询
SELECT count(*) FROM request_logs_with_current_month
WHERE ts >= '2026-08-01';

-- 对比父表（应包含更多数据）
SELECT count(*) FROM request_logs
WHERE ts >= '2026-08-01';
```

---

## 任务 2：分区轮转（每月 1 号）

### 2.1 背景

每月 1 号需要进行以下分区轮转：

1. **DETACH 上月分区**（从 ACTIVE 变为 INACTIVE）
2. **迁移上月数据到历史归档**（可选：转 Columnar）
3. **确保本月分区已创建**

### 2.2 DETACH 上月分区

```sql
-- 8 月 1 号执行，DETACH 7 月分区
DO $$
DECLARE
  prev_month text := to_char(now() - interval '1 month', 'YYYY_MM');
  table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY[
    'request_logs',
    'request_wal', 
    'usage_ledger',
    'routing_decision_log',
    'credential_model_index',
    'request_logs_bodies',
    'credit_ledger',
    'tool_usage_stats'
  ] LOOP
    EXECUTE format(
      'ALTER TABLE %I DETACH PARTITION %I',
      table_name,
      table_name || '_' || prev_month
    );
    RAISE NOTICE 'DETACHED %', table_name || '_' || prev_month;
  END LOOP;
END $$;
```

### 2.3 验证分区状态

```bash
./scripts/partition/check-partition-health.sh --env 71
```

预期结果：
- 2026_07 分区：DETACHED
- 2026_08 分区：DETACHED（新月份）
- *_default：ATTACHED

### 2.4 Columnar 转换（可选）

```sql
-- 将上月分区转为 Columnar（节省 60%+ 存储）
-- 仅适用于历史数据（不再频繁更新）

BEGIN;

-- 1. 创建 Columnar 版本
CREATE TABLE request_logs_2026_07_archive (
  LIKE request_logs_2026_07 INCLUDING ALL
) USING columnar;

-- 2. 复制数据
INSERT INTO request_logs_2026_07_archive
SELECT * FROM request_logs_2026_07;

-- 3. 删除 heap 版本
DROP TABLE request_logs_2026_07;

-- 4. 重命名
ALTER TABLE request_logs_2026_07_archive 
RENAME TO request_logs_2026_07;

-- 5. ATTACH 到归档（只读）
ALTER TABLE request_logs ATTACH PARTITION request_logs_2026_07
  FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

-- 6. 验证
SELECT c.relname, am.amname
FROM pg_class c
JOIN pg_am am ON am.oid = c.relam
WHERE c.relname = 'request_logs_2026_07';

COMMIT;
```

---

## 任务 3：确保本月分区已创建

### 3.1 检查

```sql
-- 检查当前月份分区
SELECT c.relname,
       CASE WHEN i.inhrelid IS NOT NULL THEN 'ATTACHED' ELSE 'DETACHED' END
FROM pg_class c
LEFT JOIN pg_inherits i ON c.oid = i.inhrelid
WHERE c.relname LIKE 'request_logs_2026%';
```

### 3.2 如果缺失，创建

```sql
-- 创建 2026_08 分区
SELECT ensure_request_logs_partition('2026-08-01'::timestamptz);

-- 验证
SELECT c.relname FROM pg_class c
WHERE c.relname = 'request_logs_2026_08';
```

### 3.3 DETACH 新月份（关键）

```sql
-- 确保新月份分区是 DETACHED（这样 *_default 才能接收写入）
ALTER TABLE request_logs DETACH PARTITION request_logs_2026_08;
```

---

## 任务 4：Promote 函数积压检查

### 4.1 检查积压

```bash
./scripts/partition/report-default-sizes.sh --env 71 --format json
```

### 4.2 手动清理（如果需要）

```bash
# 如果 *_default 大小异常，执行手动迁移
./scripts/partition/manual-promote-default.sh --all --retention 7 --batch 5000
```

---

## 任务 5：备份验证（每周）

### 5.1 验证备份完整性

```sql
-- 检查备份保留
SELECT 
  backup_id,
  start_time,
  end_time,
  status,
  pg_size_pretty(total_size)
FROM backup_history
ORDER BY start_time DESC
LIMIT 10;
```

### 5.2 测试恢复（季度）

```bash
# 从备份恢复到一个临时实例
pg_restore -h test-instance -U kxuser -d llm_gateway_test backup.dump

# 验证分区状态
psql -h test-instance -c "SELECT count(*) FROM request_logs_with_current_month;"
```

---

## 任务 6：监控指标审查（每周）

### 6.1 检查 Prometheus 告警历史

```bash
# 查看过去一周的告警
curl -s 'http://prometheus:9090/api/v1/alerts?active=true' | \
  jq '.data.alerts[] | select(.labels.component=="partition")'
```

### 6.2 关键指标趋势

| 指标 | 正常范围 | 关注阈值 |
|------|---------|---------|
| `*_default` 大小 | < 5GB | > 5GB |
| promote 执行延迟 | < 1 小时 | > 2 小时 |
| 约束冲突错误 | 0 | > 0 |
| 死元组比例 | < 5% | > 10% |

---

## 月度检查清单（可打印）

```
分区表月度维护 - [日期: _______]

□ 1. 更新 VIEW（每月 1 号）
  □ 连接数据库
  □ 执行 VIEW 更新 SQL
  □ 验证跨月查询

□ 2. 分区轮转
  □ DETACH 上月分区
  □ 验证分区状态
  □ （可选）Columnar 转换

□ 3. 确保本月分区
  □ 检查本月分区存在
  □ 创建缺失的分区
  □ DETACH 本月分区

□ 4. Promote 积压检查
  □ 运行报告脚本
  □ 手动清理（如需要）

□ 5. 备份验证（每周）
  □ 检查备份状态
  □ 验证恢复能力

□ 6. 监控审查（每周）
  □ 检查告警历史
  □ 趋势分析

签名：_______________
日期：_______________
```

---

## 自动化路线图

| 任务 | 当前状态 | 计划实现 | 依赖 |
|------|---------|---------|------|
| VIEW 自动更新 | 手动 | migration 341 | update_monthly_views() 函数 |
| 分区 DETACH 自动 | 手动 | bg/partition_manager.go | 月度调度逻辑 |
| Columnar 转换脚本 | 手动 | scripts/ | 存储团队 |
| 告警自动处理 | 告警 | PagerDuty 集成 | on-call 流程 |

---

## 常见问题

### Q1：忘记更新 VIEW 会怎样？

**A**：跨月查询会遗漏上个月的 DETACHED 分区数据。例如 8 月 15 号查询 7 月数据时，如果 VIEW 未更新，会丢失 7 月数据。

**处理**：立即更新 VIEW：
```sql
DROP VIEW request_logs_with_current_month;
CREATE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs
UNION ALL
SELECT * FROM request_logs_2026_07
UNION ALL
SELECT * FROM request_logs_default;
```

### Q2：忘记 DETACH 新月份会怎样？

**A**：写入 `*_default` 会失败，报分区约束冲突错误。

**处理**：立即 DETACH：
```sql
ALTER TABLE request_logs DETACH PARTITION request_logs_2026_08;
```

### Q3：Promote 函数停止工作很久了怎么办？

**A**：
1. 检查 bg/partition_manager.go 日志
2. 手动执行 promote：
   ```bash
   ./scripts/partition/manual-promote-default.sh --all
   ```
3. 如果 `*_default` 数据量很大（> 10GB），分批处理：
   ```bash
   # 先清理 7 天前的数据
   ./scripts/partition/manual-promote-default.sh --all --retention 7 --batch 5000
   ```

---

**维护团队**: Infrastructure Team  
**最后更新**: 2026-07-05
