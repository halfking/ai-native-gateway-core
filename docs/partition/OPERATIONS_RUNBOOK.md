# 分区表运维手册

**日期**: 2026-07-05  
**版本**: 1.0

---

## 1. 故障排查流程

### 1.1 快速诊断

当出现分区表相关问题时，按以下顺序检查：

```bash
# 1. 运行健康检查（5 分钟内完成初步诊断）
./scripts/partition/check-partition-health.sh --env 71

# 2. 查看后台调度器日志
grep "partition_manager" /var/log/llm-gateway.log | tail -50

# 3. 验证架构对齐
./scripts/partition/verify-partition-alignment.sh --env 71
```

### 1.2 常见故障与解决方案

---

## 2. 故障场景与解决方案

### 场景 A：写入失败 - 分区约束冲突

**错误信息**：
```
ERROR: new row for relation "request_logs_default" violates partition constraint
SQLSTATE: 23514
```

**原因**：当月分区仍为 ATTACHED，DEFAULT 分区动态排除当月时间范围

**排查步骤**：
```bash
# 1. 检查当月分区状态
psql -c "
  SELECT c.relname,
         CASE WHEN i.inhrelid IS NOT NULL THEN 'ATTACHED' ELSE 'DETACHED' END
  FROM pg_class c
  LEFT JOIN pg_inherits i ON c.oid = i.inhrelid
  WHERE c.relname LIKE 'request_logs_2026_07';
"

# 2. 预期结果
# 如果显示 ATTACHED，说明 migration 337 未应用
```

**解决方案**：
```bash
# 应用 migration 337
psql < db/migrations/337_detach_current_future_partitions.sql

# 验证
./scripts/partition/check-partition-health.sh --env 71
```

**预防措施**：
- 确保 migration 337 在所有环境已应用
- 配置 Prometheus 告警：PartitionConstraintViolations

---

### 场景 B：写入失败 - Columnar 不支持 UPSERT

**错误信息**：
```
ERROR: ON CONFLICT is not supported for columnar tables
SQLSTATE: 0A000
```

**原因**：写入代码尝试对 Columnar 分区执行 UPSERT

**排查步骤**：
```bash
# 1. 检查错误发生在哪个表
# 查看应用日志中的 request_id

# 2. 确认写入目标
grep "INSERT INTO" telemetry/client.go | head -5
# 预期：INSERT INTO request_logs_default（不是父表）
```

**解决方案**：
1. 确认代码已改为写入 `*_default`
2. 如果 `*_default` 是 Columnar，需要重建为 heap：
```sql
-- 检查存储类型
SELECT c.relname, am.amname
FROM pg_class c
JOIN pg_am am ON am.oid = c.relam
WHERE c.relname = 'xxx_default';

-- 如果是 columnar，需要迁移数据重建
ALTER TABLE xxx_default SET USING heap;
```

**预防措施**：
- 代码审查确保所有写入指向 `*_default`
- 测试环境验证 UPSERT 功能

---

### 场景 C：`*_default` 表过大

**错误信息**：
```
# Prometheus 告警
ALERT: PartitionDefaultTableSizeWarning
```

**原因**：
1. promote 函数停止工作
2. 写入量激增
3. 保留窗口设置过长

**排查步骤**：
```bash
# 1. 检查表大小
./scripts/partition/report-default-sizes.sh --env 71

# 2. 检查 promote 函数执行日志
grep "promote failed\|promote batch" /var/log/llm-gateway.log | tail -20

# 3. 检查月度分区是否存在
psql -c "SELECT count(*) FROM pg_class WHERE relname LIKE 'request_logs_2026_%';"
```

**解决方案**：
```bash
# 1. 手动触发 promote（先小批次测试）
./scripts/partition/manual-promote-default.sh \
  --table request_logs \
  --retention 7 \
  --batch 1000

# 2. 如果成功，扩大处理
./scripts/partition/manual-promote-default.sh --all

# 3. 检查月度分区是否需要创建
# 如果 promote 函数报告 "target partition does not exist"
# 需要先创建月度分区
SELECT ensure_request_logs_partition(now());
```

**预防措施**：
- 配置 PartitionDefaultTableSizeWarning 告警（5GB 警告，10GB 严重）
- 确保 promote 函数每 1 小时执行

---

### 场景 D：Promote 函数执行失败

**错误信息**：
```
ERROR: duplicate key value violates unique constraint
SQLSTATE: 23505
```

**原因**：
1. 迁移中断后重复执行
2. 目标分区已有数据

**排查步骤**：
```bash
# 1. 检查目标分区数据
psql -c "SELECT count(*) FROM request_logs_2026_07;"

# 2. 检查 _default 是否还有遗留数据
psql -c "SELECT count(*) FROM request_logs_default 
        WHERE ts < now() - interval '7 days';"
```

**解决方案**：
```sql
-- 使用 ON CONFLICT DO NOTHING 避免重复
WITH del AS (
    DELETE FROM request_logs_default
    WHERE ts < now() - interval '7 days'
    RETURNING *
),
ins AS (
    INSERT INTO request_logs
    SELECT * FROM del
    ON CONFLICT DO NOTHING  -- 幂等保证
    RETURNING 1
)
SELECT count(*) FROM ins;
```

**预防措施**：
- promote 函数已内置 `ON CONFLICT DO NOTHING`
- 监控 `partition_manager_promote_errors_total` 指标

---

### 场景 E：查询返回数据不完整

**现象**：
- 查询父表 `SELECT * FROM request_logs WHERE ts >= '2026-07-01'`
- 结果不包含 2026-07 数据

**原因**：
- 2026-07 分区已 DETACHED
- 父表查询不包含 DETACHED 分区

**排查步骤**：
```bash
# 1. 检查分区状态
psql -c "
  SELECT c.relname,
         CASE WHEN i.inhrelid IS NOT NULL THEN 'ATTACHED' ELSE 'DETACHED' END
  FROM pg_class c
  LEFT JOIN pg_inherits i ON c.oid = i.inhrelid
  WHERE c.relname LIKE 'request_logs_2026%';
"

# 2. 确认使用了 VIEW
psql -c "SELECT count(*) FROM request_logs_with_current_month 
        WHERE ts >= '2026-07-01';"
```

**解决方案**：
1. **短期**：使用 VIEW 替代父表查询
```sql
SELECT * FROM request_logs_with_current_month
WHERE ts >= '2026-07-01';
```

2. **长期**：修改应用代码使用 VIEW

**预防措施**：
- 创建 `*_with_current_month` VIEW
- 代码审查确保跨月查询使用 VIEW

---

### 场景 F：Promote 函数未执行

**现象**：
- `*_default` 表持续增长
- 日志中无 promote 相关输出

**原因**：
1. `bg/partition_manager.go` 进程未运行
2. `promoteInterval` 设置为 0

**排查步骤**：
```bash
# 1. 检查进程状态
ps aux | grep partition_manager

# 2. 检查配置
grep "promoteInterval\|PromoteInterval" bg/partition_manager.go
```

**解决方案**：
```bash
# 1. 重启服务
systemctl restart llm-gateway

# 2. 手动触发 promote
./scripts/partition/manual-promote-default.sh --all
```

**预防措施**：
- 配置 PartitionPromoteLag 告警（2 小时未执行）
- 监控进程健康状态

---

## 3. 紧急操作

### 3.1 完全重建 `*_default`

如果 `*_default` 严重损坏：

```sql
-- 1. 备份数据
CREATE TABLE request_logs_default_backup AS 
SELECT * FROM request_logs_default;

-- 2. 重建表
DROP TABLE request_logs_default;
CREATE TABLE request_logs_default PARTITION OF request_logs DEFAULT;

-- 3. 恢复数据（分批）
INSERT INTO request_logs_default
SELECT * FROM request_logs_default_backup
WHERE ts >= now() - interval '7 days';

-- 4. 验证
SELECT count(*) FROM request_logs_default;
```

### 3.2 紧急清理大量积压

```bash
# 强制迁移所有积压数据
./scripts/partition/manual-promote-default.sh \
  --all \
  --retention 1 \
  --batch 10000
```

### 3.3 临时允许写入父表（不推荐，仅紧急）

```sql
-- 临时 ATTACH 当月分区（允许父表写入）
ALTER TABLE request_logs ATTACH PARTITION request_logs_2026_07
  FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

-- 记录操作原因
-- ... 紧急操作完成 ...

-- 恢复 DETACHED
ALTER TABLE request_logs DETACH PARTITION request_logs_2026_07;
```

---

## 4. 诊断命令速查

### 4.1 分区状态
```sql
-- 检查所有分区状态
SELECT c.relname,
       CASE WHEN i.inhrelid IS NOT NULL THEN 'ATTACHED' ELSE 'DETACHED' END AS status,
       pg_get_expr(c.relpartbound, c.oid) AS bounds
FROM pg_class c
LEFT JOIN pg_inherits i ON c.oid = i.inhrelid
WHERE c.relname LIKE 'request_logs_%'
ORDER BY c.relname;
```

### 4.2 表大小
```sql
SELECT 
  schemaname || '.' || tablename AS table_name,
  pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS size
FROM pg_stat_user_tables
WHERE tablename LIKE '%_default'
ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;
```

### 4.3 写入统计
```sql
SELECT 
  relname,
  n_tup_ins AS inserts,
  n_tup_upd AS updates,
  n_tup_del AS deletes,
  n_live_tup AS live_rows,
  n_dead_tup AS dead_rows
FROM pg_stat_user_tables
WHERE relname LIKE '%_default'
  OR relname LIKE '%_2026_%';
```

### 4.4 Promote 函数测试
```sql
SELECT promote_request_logs_default_batch('7 days'::interval, 100);
-- 返回移动的行数
-- 0 = 无更多数据
```

---

## 5. 联系人和升级路径

| 严重性 | 联系 | 升级时间 |
|--------|------|---------|
| P0 (全局不可用) | 值班 SRE | 15 分钟 |
| P1 (部分功能受损) | Team Lead | 1 小时 |
| P2 (性能下降) | Infrastructure | 4 小时 |
| P3 (预防性) | GitHub Issue | 工作日 |

---

**维护团队**: Infrastructure Team  
**最后更新**: 2026-07-05
