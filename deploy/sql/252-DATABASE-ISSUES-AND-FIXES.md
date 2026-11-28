# 252服务器数据库问题分析和修正方案

**创建时间**: 2026-11-29  
**状态**: 待部署验证  
**负责人**: AI Assistant

## 执行摘要

在审查252服务器上llm-gateway-go的数据库迁移脚本时，发现原始迁移文件（`2026-07-26-provider-profile-system.sql`）存在多个可以优化的地方。已创建修正脚本和自动化验证工具。

**关键发现**:
- ✅ 表结构设计合理
- ⚠️  缺少数据完整性约束（分数范围、枚举值）
- ⚠️  缺少唯一性约束（可能导致重复数据）
- ⚠️  缺少外键约束（可能影响引用完整性）
- ⚠️  可以添加更多复合索引优化查询

---

## 发现的问题

### 问题1: 缺少分数范围约束

**严重程度**: 中等  
**影响**: 可能存储无效分数（如负数或超过100）

**详情**:
- `provider_profile_daily` 表的所有分数字段（`network_score`, `total_score`等）定义为 `NUMERIC(5,2)`
- 虽然数据类型允许 -999.99 到 999.99，但业务逻辑要求 0-100
- 应用层验证不足以防止数据库直接操作或数据导入错误

**修正**:
```sql
ALTER TABLE provider_profile_daily 
    ADD CONSTRAINT chk_total_score_range 
    CHECK (total_score IS NULL OR (total_score >= 0 AND total_score <= 100));
```

### 问题2: 缺少唯一性约束

**严重程度**: 中等  
**影响**: `provider_profile_metrics` 可能有重复的时间点数据

**详情**:
- `provider_profile_metrics` 表按 `(credential_id, metric_time)` 应该是唯一的
- 当前只有普通索引，允许重复数据
- 如果数据采集器并发运行或重试，可能产生重复记录

**修正**:
```sql
CREATE UNIQUE INDEX IF NOT EXISTS idx_ppm_unique_metric 
    ON provider_profile_metrics(credential_id, metric_time);
```

### 问题3: 缺少枚举值约束

**严重程度**: 低  
**影响**: 告警和测试类型可能使用未定义的值

**详情**:
- `provider_profile_alerts.alert_level` 文档说明只能是 'critical'/'warning'/'info'
- `provider_profile_alerts.alert_type` 有预定义的类型列表
- 但数据库层面没有约束，应用层错误可能存储无效值

**修正**:
```sql
ALTER TABLE provider_profile_alerts 
    ADD CONSTRAINT chk_alert_level 
    CHECK (alert_level IN ('critical', 'warning', 'info'));
```

### 问题4: 缺少外键约束

**严重程度**: 低  
**影响**: 可能存在孤儿记录（引用不存在的凭据或供应商）

**详情**:
- 所有表都引用 `credential_id` 和/或 `provider_id`
- 但没有定义外键约束到 `credentials` 和 `providers` 表
- 如果凭据或供应商被删除，相关画像数据会成为孤儿记录

**注意**: 外键约束需要谨慎添加，因为：
1. 需要确认 `credentials` 和 `providers` 表存在
2. 可能影响删除操作的性能
3. 需要决定级联删除策略（CASCADE vs RESTRICT）

**建议**（暂未实施）:
```sql
-- 仅作为参考，需要确认表结构后再决定是否添加
ALTER TABLE provider_profile_metrics 
    ADD CONSTRAINT fk_ppm_credential 
    FOREIGN KEY (credential_id) REFERENCES credentials(id) ON DELETE CASCADE;
```

### 问题5: 查询优化机会

**严重程度**: 低  
**影响**: 某些常见查询模式可以更快

**详情**:
- 常见查询: 按凭据和供应商组合查询时间序列
- 常见查询: 查询未解决的告警
- 当前索引覆盖单列查询，但复合查询可以优化

**修正**:
```sql
-- 复合索引优化
CREATE INDEX IF NOT EXISTS idx_ppm_cred_provider_time 
    ON provider_profile_metrics(credential_id, provider_id, metric_time DESC);

-- 部分索引优化
CREATE INDEX IF NOT EXISTS idx_ppa_active_alerts 
    ON provider_profile_alerts(credential_id, created_at DESC) 
    WHERE resolved_at IS NULL;
```

### 问题6: 缺少自动时间戳更新

**严重程度**: 低  
**影响**: `updated_at` 字段需要手动维护

**详情**:
- `provider_cost_reconciliation` 有 `updated_at` 字段
- 但没有触发器自动更新
- 应用层可能忘记更新该字段

**修正**:
```sql
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ language 'plpgsql';

CREATE TRIGGER update_provider_cost_reconciliation_updated_at
    BEFORE UPDATE ON provider_cost_reconciliation
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();
```

---

## 修正方案

### 方案文件

1. **原始迁移**: `deploy/sql/migrations/2026-07-26-provider-profile-system.sql`
   - 创建所有表和基础索引
   - 状态: 应该已在252上执行

2. **修正脚本**: `deploy/sql/migrations/2026-07-26-provider-profile-fixes.sql`
   - 添加所有约束和优化
   - 状态: 新创建，待执行

3. **验证脚本**: `deploy/sql/verify-and-fix-252.sh`
   - 自动检查数据库状态
   - 执行必要的迁移和修正
   - 验证最终结果

### 执行步骤

#### 选项A: 使用自动化脚本（推荐）

```bash
# 1. 确保252服务器可访问
ping 192.168.1.252

# 2. 执行验证和修正脚本
cd /path/to/llm-gateway-go
./deploy/sql/verify-and-fix-252.sh

# 脚本会自动:
# - 检查SSH和数据库连接
# - 验证表结构
# - 执行缺失的迁移
# - 应用修正和优化
# - 验证最终状态
```

#### 选项B: 手动执行（如果自动化失败）

```bash
# 1. 连接到252服务器
ssh -p 25022 root@192.168.1.252

# 2. 进入项目目录
cd /data/llm-gateway-go  # 或实际路径

# 3. 拉取最新代码
git pull origin main

# 4. 检查表是否已存在
psql -U postgres -d llm_gateway -c "
  SELECT tablename FROM pg_tables 
  WHERE schemaname='public' 
  AND tablename LIKE 'provider_profile%'
  ORDER BY tablename;
"

# 5a. 如果表不存在，执行原始迁移
psql -U postgres -d llm_gateway \
  -f deploy/sql/migrations/2026-07-26-provider-profile-system.sql

# 5b. 执行修正脚本（无论表是否已存在）
psql -U postgres -d llm_gateway \
  -f deploy/sql/migrations/2026-07-26-provider-profile-fixes.sql

# 6. 验证结果
psql -U postgres -d llm_gateway -c "
  SELECT 
    COUNT(*) as table_count
  FROM pg_tables 
  WHERE schemaname='public' 
  AND (tablename LIKE 'provider_profile%' 
       OR tablename = 'provider_credibility_tests'
       OR tablename = 'provider_cost_reconciliation');
"
```

#### 选项C: 本地数据库连接（如果SSH不可用）

```bash
# 如果252服务器的PostgreSQL端口对外开放
export PGHOST=192.168.1.252
export PGPORT=5432
export PGUSER=postgres
export PGDATABASE=llm_gateway

# 执行修正脚本
psql -f deploy/sql/migrations/2026-07-26-provider-profile-fixes.sql
```

---

## 验证清单

部署后验证以下项目:

### 表结构验证

```sql
-- 1. 检查6个核心表都存在
SELECT tablename FROM pg_tables 
WHERE schemaname='public' 
AND (tablename LIKE 'provider_profile%' 
     OR tablename = 'provider_credibility_tests'
     OR tablename = 'provider_cost_reconciliation')
ORDER BY tablename;

-- 应该返回:
-- provider_cost_reconciliation
-- provider_credibility_tests
-- provider_profile_alerts
-- provider_profile_daily
-- provider_profile_metrics
-- provider_profile_whitelist
```

### 约束验证

```sql
-- 2. 检查CHECK约束数量
SELECT 
    table_name,
    constraint_name,
    constraint_type
FROM information_schema.table_constraints
WHERE constraint_schema = 'public'
AND table_name IN (
    'provider_profile_daily',
    'provider_profile_alerts',
    'provider_credibility_tests'
)
AND constraint_type = 'CHECK'
ORDER BY table_name, constraint_name;

-- 应该至少有12个CHECK约束
```

### 索引验证

```sql
-- 3. 检查索引数量和类型
SELECT 
    schemaname,
    tablename,
    indexname,
    indexdef
FROM pg_indexes
WHERE schemaname = 'public'
AND (indexname LIKE '%provider_profile%'
     OR indexname LIKE '%ppm_%'
     OR indexname LIKE '%ppd_%'
     OR indexname LIKE '%pct_%'
     OR indexname LIKE '%ppa_%')
ORDER BY tablename, indexname;

-- 应该有15+个索引
```

### 触发器验证

```sql
-- 4. 检查触发器
SELECT 
    trigger_name,
    event_object_table,
    action_statement
FROM information_schema.triggers
WHERE trigger_schema = 'public'
AND event_object_table = 'provider_cost_reconciliation';

-- 应该有1个update触发器
```

### credentials表扩展验证

```sql
-- 5. 检查credentials表新字段
SELECT 
    column_name,
    data_type,
    is_nullable
FROM information_schema.columns
WHERE table_name = 'credentials'
AND column_name LIKE 'auto_%'
ORDER BY column_name;

-- 应该有4个字段:
-- auto_disabled_at, auto_disabled_reason
-- auto_enabled_at, auto_enabled_reason
```

---

## 回滚方案

如果修正脚本导致问题:

### 回滚修正（保留表结构）

```sql
BEGIN;

-- 删除新添加的约束
ALTER TABLE provider_profile_daily DROP CONSTRAINT IF EXISTS chk_network_score_range;
ALTER TABLE provider_profile_daily DROP CONSTRAINT IF EXISTS chk_total_score_range;
ALTER TABLE provider_profile_alerts DROP CONSTRAINT IF EXISTS chk_alert_level;
ALTER TABLE provider_profile_alerts DROP CONSTRAINT IF EXISTS chk_alert_type;
ALTER TABLE provider_credibility_tests DROP CONSTRAINT IF EXISTS chk_test_type;

-- 删除新添加的索引
DROP INDEX IF EXISTS idx_ppm_cred_provider_time;
DROP INDEX IF EXISTS idx_ppa_active_alerts;
DROP INDEX IF EXISTS idx_ppm_unique_metric;

-- 删除触发器
DROP TRIGGER IF EXISTS update_provider_cost_reconciliation_updated_at ON provider_cost_reconciliation;
DROP FUNCTION IF EXISTS update_updated_at_column();

COMMIT;
```

### 完全回滚（删除所有供应商画像表）

**警告**: 这会删除所有数据！仅在测试环境或数据可重建时使用。

```sql
BEGIN;

-- 删除所有表（级联删除索引和约束）
DROP TABLE IF EXISTS provider_profile_metrics CASCADE;
DROP TABLE IF EXISTS provider_profile_daily CASCADE;
DROP TABLE IF EXISTS provider_credibility_tests CASCADE;
DROP TABLE IF EXISTS provider_profile_alerts CASCADE;
DROP TABLE IF EXISTS provider_cost_reconciliation CASCADE;
DROP TABLE IF EXISTS provider_profile_whitelist CASCADE;

-- 删除credentials表的扩展字段
ALTER TABLE credentials DROP COLUMN IF EXISTS auto_disabled_at;
ALTER TABLE credentials DROP COLUMN IF EXISTS auto_disabled_reason;
ALTER TABLE credentials DROP COLUMN IF EXISTS auto_enabled_at;
ALTER TABLE credentials DROP COLUMN IF EXISTS auto_enabled_reason;

COMMIT;
```

---

## 下一步行动

1. **立即执行**:
   - [ ] 确认252服务器网络连接
   - [ ] 运行 `verify-and-fix-252.sh` 脚本
   - [ ] 验证表结构和约束

2. **后续监控**:
   - [ ] 检查应用日志确认数据采集正常
   - [ ] 2小时后检查 `provider_profile_metrics` 是否有数据
   - [ ] 次日检查 `provider_profile_daily` 聚合数据
   - [ ] 监控数据库性能指标

3. **文档更新**:
   - [ ] 更新数据库架构文档
   - [ ] 记录实际部署结果
   - [ ] 添加运维手册条目

---

## 附录: 快速参考

### 常用查询

```sql
-- 查看最近的指标数据
SELECT 
    credential_id,
    provider_id,
    metric_time,
    availability_total_requests,
    availability_success_requests
FROM provider_profile_metrics
ORDER BY metric_time DESC
LIMIT 10;

-- 查看每日画像分数
SELECT 
    credential_id,
    profile_date,
    total_score,
    network_score,
    availability_score
FROM provider_profile_daily
ORDER BY profile_date DESC
LIMIT 10;

-- 查看未解决的告警
SELECT 
    credential_id,
    alert_type,
    alert_level,
    current_score,
    message,
    created_at
FROM provider_profile_alerts
WHERE resolved_at IS NULL
ORDER BY created_at DESC;
```

### 数据清理

```sql
-- 清理7天前的指标数据
DELETE FROM provider_profile_metrics
WHERE created_at < NOW() - INTERVAL '7 days';

-- 清理365天前的每日数据
DELETE FROM provider_profile_daily
WHERE profile_date < CURRENT_DATE - INTERVAL '365 days';
```

### 性能监控

```sql
-- 检查表大小
SELECT 
    schemaname,
    tablename,
    pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS size
FROM pg_tables
WHERE schemaname = 'public'
AND (tablename LIKE 'provider_profile%'
     OR tablename = 'provider_credibility_tests'
     OR tablename = 'provider_cost_reconciliation')
ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;

-- 检查索引使用情况
SELECT 
    schemaname,
    tablename,
    indexname,
    idx_scan as index_scans,
    idx_tup_read as tuples_read,
    idx_tup_fetch as tuples_fetched
FROM pg_stat_user_indexes
WHERE schemaname = 'public'
AND (indexname LIKE '%provider_profile%'
     OR indexname LIKE '%ppm_%'
     OR indexname LIKE '%ppd_%')
ORDER BY idx_scan DESC;
```

---

**文档版本**: 1.0  
**最后更新**: 2026-11-29  
**维护者**: AI Assistant
