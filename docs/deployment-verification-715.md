# 部署验证计划：715 路由降级修复

**日期：** 2026-09-16  
**修复提交：** 294383292  
**迁移脚本：** `715_route_incidents_pending_state.sql`

---

## 修复内容概述

**问题：**
1. 凭证状态（Available/RecoverAt）未持久化到数据库，导致进程重启后凭证冷却状态丢失
2. 路由事件在单次失败（streak=1）时立即可见，而非等待配置阈值（default=3）

**修复：**
1. `credentialstate/manager.go`: 在 `MarkFailure` 和 `MarkSuccess` 后调用 `Persist()` 持久化状态
2. `routeincident/state.go`: 引入 `pending` 状态，只有 `failure_streak >= failure_to_active` 时才转为 `active`
3. 数据库迁移：扩展 `route_incidents.state` CHECK 约束和唯一索引以支持 `pending`

**影响范围：**
- 245 服务器上的 apiclaude/apigpt/suyun 路由
- 所有使用 `route_incidents` 表的监控和告警

---

## 部署前验证（测试环境）

### 1. 环境准备

```bash
# 1.1 确认测试环境数据库连接
export TEST_DB_DSN="postgres://user:pass@testdb:5432/gateway?sslmode=disable"

# 1.2 备份当前数据（可选）
pg_dump -h testdb -U user -d gateway -t route_incidents -t credential_states > backup_pre_715.sql

# 1.3 确认当前迁移版本
psql $TEST_DB_DSN -c "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 5;"
```

### 2. 部署步骤

```bash
# 2.1 停止测试服务
systemctl stop llm-gateway-test

# 2.2 部署新二进制文件
scp ./bin/llm-gateway testserver:/opt/gateway/llm-gateway.new
ssh testserver "mv /opt/gateway/llm-gateway /opt/gateway/llm-gateway.bak && mv /opt/gateway/llm-gateway.new /opt/gateway/llm-gateway"

# 2.3 启动服务（自动执行迁移）
systemctl start llm-gateway-test

# 2.4 检查启动日志
journalctl -u llm-gateway-test -f | grep -E "(715|migration|pending)"
```

**预期日志：**
```
INFO ApplyMigrations: applying 715_route_incidents_pending_state.sql
INFO Migration 715 applied successfully
```

### 3. 迁移验证

```sql
-- 3.1 确认约束已更新
SELECT conname, consrc 
FROM pg_constraint 
WHERE conrelid = 'route_incidents'::regclass 
  AND conname = 'route_incidents_state_check';
-- 预期：consrc 包含 'pending'

-- 3.2 确认索引已更新
SELECT indexdef 
FROM pg_indexes 
WHERE tablename = 'route_incidents' 
  AND indexname = 'uq_route_incidents_active_route';
-- 预期：WHERE 子句包含 state IN ('pending', 'active', 'recovering')

-- 3.3 检查现有数据
SELECT state, COUNT(*) 
FROM route_incidents 
GROUP BY state;
-- 预期：无 pending（现有数据都是 active/recovering/recovered）
```

### 4. 功能验证

#### 4.1 触发新失败事件

```bash
# 构造一个会失败的请求（使用无效凭证或过期 token）
curl -X POST http://testserver:8080/v1/chat/completions \
  -H "Authorization: Bearer invalid_key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-sonnet",
    "messages": [{"role": "user", "content": "test"}]
  }'
```

**验证点 1：第一次失败应创建 pending 事件**

```sql
SELECT id, state, failure_streak, created_at, visible_at 
FROM route_incidents 
WHERE model = 'claude-3-sonnet' 
ORDER BY created_at DESC 
LIMIT 1;
```

**预期结果：**
- `state = 'pending'`
- `failure_streak = 1`
- `visible_at IS NULL`

#### 4.2 连续失败到达阈值

```bash
# 再次发送相同失败请求（2次）
for i in {1..2}; do
  curl -X POST http://testserver:8080/v1/chat/completions \
    -H "Authorization: Bearer invalid_key" \
    -H "Content-Type: application/json" \
    -d '{
      "model": "claude-3-sonnet",
      "messages": [{"role": "user", "content": "test"}]
    }'
  sleep 2
done
```

**验证点 2：第三次失败应转为 active**

```sql
SELECT id, state, failure_streak, created_at, visible_at 
FROM route_incidents 
WHERE model = 'claude-3-sonnet' 
ORDER BY created_at DESC 
LIMIT 1;
```

**预期结果：**
- `state = 'active'`
- `failure_streak = 3`
- `visible_at IS NOT NULL` (约等于最后一次失败时间)

#### 4.3 凭证状态持久化验证

```sql
-- 查看凭证状态
SELECT credential_id, model, available, recover_at, last_failure_at, consecutive_failures
FROM credential_states
WHERE last_failure_at > NOW() - INTERVAL '5 minutes'
ORDER BY last_failure_at DESC;
```

**预期结果：**
- `available = false`
- `recover_at` 应该是未来时间（last_failure_at + cooldown_duration）
- `consecutive_failures >= 1`

**验证点 3：重启服务后凭证状态保留**

```bash
# 重启服务
systemctl restart llm-gateway-test

# 等待 5 秒后查询
sleep 5
psql $TEST_DB_DSN -c "
SELECT credential_id, model, available, recover_at 
FROM credential_states 
WHERE recover_at > NOW() 
LIMIT 5;"
```

**预期结果：**
- 凭证仍然是 `available = false`
- `recover_at` 未被重置

#### 4.4 凭证恢复验证

```bash
# 等待冷却期结束（根据配置，可能是 5-10 分钟）
# 或手动更新 recover_at 到过去时间
psql $TEST_DB_DSN -c "
UPDATE credential_states 
SET recover_at = NOW() - INTERVAL '1 second' 
WHERE credential_id = '<target_credential_id>';"

# 发送成功请求
curl -X POST http://testserver:8080/v1/chat/completions \
  -H "Authorization: Bearer valid_key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-sonnet",
    "messages": [{"role": "user", "content": "test"}]
  }'
```

**验证点 4：凭证应恢复可用**

```sql
SELECT credential_id, model, available, recover_at, consecutive_failures
FROM credential_states
WHERE credential_id = '<target_credential_id>';
```

**预期结果：**
- `available = true`
- `recover_at IS NULL`
- `consecutive_failures = 0`

### 5. 测试环境验收标准

- [ ] 迁移脚本执行成功，无错误日志
- [ ] 第一次失败创建 `pending` 事件，不可见
- [ ] 第三次失败转为 `active`，设置 `visible_at`
- [ ] 凭证状态（available/recover_at）写入数据库
- [ ] 重启服务后凭证冷却状态保留
- [ ] 冷却期后凭证自动恢复可用

---

## 生产部署

### 1. 部署窗口

**建议时间：** 低峰期（如凌晨 2:00-4:00）  
**影响范围：** 245 服务器重启期间（预计 30 秒），所有路由请求会故障转移到其他服务器

### 2. 部署前检查

```bash
# 2.1 确认主分支最新
git log --oneline -1
# 预期：294383292 fix(incident): 修复凭证状态持久化缺失与路由事件过早可见的双重bug

# 2.2 确认测试已通过
go test ./domains/credentialstate/... -v
go test ./domains/routeincident/... -v
go test ./sql/migrations/startup/... -run Migration715 -v

# 2.3 确认生产数据库备份
pg_dump -h proddb -U prod_user -d gateway -t route_incidents -t credential_states | gzip > backup_prod_715_$(date +%Y%m%d_%H%M%S).sql.gz
```

### 3. 部署步骤（245 服务器）

```bash
# 3.1 SSH 到 245 服务器
ssh prod-245

# 3.2 切换到部署用户
sudo su - gateway

# 3.3 拉取最新代码（或使用 CI/CD 流水线）
cd /opt/gateway-src
git pull origin main
git log --oneline -1  # 确认是 294383292

# 3.4 构建二进制文件
make build
# 或使用预构建的二进制
scp ./bin/llm-gateway-linux-amd64 prod-245:/tmp/llm-gateway.new

# 3.5 停止服务
systemctl stop llm-gateway

# 3.6 替换二进制文件
mv /opt/gateway/llm-gateway /opt/gateway/llm-gateway.bak
mv /tmp/llm-gateway.new /opt/gateway/llm-gateway
chmod +x /opt/gateway/llm-gateway

# 3.7 启动服务（自动执行迁移）
systemctl start llm-gateway

# 3.8 检查启动日志
journalctl -u llm-gateway -f --since "5 minutes ago" | grep -E "(715|migration|ERROR|FATAL)"
```

**关键日志检查：**
```
✓ INFO ApplyMigrations: applying 715_route_incidents_pending_state.sql
✓ INFO Migration 715 applied successfully
✓ INFO Server listening on :8080
✗ ERROR: 任何包含 "23514" 或 "route_incidents_state_check" 的错误
```

### 4. 部署后立即验证

```sql
-- 4.1 确认迁移已应用
SELECT version FROM schema_migrations WHERE version = '715' LIMIT 1;
-- 预期：返回 1 行

-- 4.2 检查现有事件状态分布
SELECT state, COUNT(*) FROM route_incidents GROUP BY state;
-- 预期：可能有 active/recovering/recovered，无 pending（除非刚好有新失败）

-- 4.3 检查凭证状态
SELECT 
    COUNT(*) as total,
    SUM(CASE WHEN available THEN 1 ELSE 0 END) as available_count,
    SUM(CASE WHEN recover_at > NOW() THEN 1 ELSE 0 END) as cooling_count
FROM credential_states;
```

### 5. 回滚预案

**触发条件：**
- 启动日志出现 CHECK 约束错误（23514）
- 事件状态异常（大量 pending 不转 active）
- 服务无法启动或频繁崩溃

**回滚步骤：**

```bash
# 5.1 停止服务
systemctl stop llm-gateway

# 5.2 恢复旧二进制文件
mv /opt/gateway/llm-gateway.bak /opt/gateway/llm-gateway

# 5.3 回滚数据库迁移
psql $PROD_DB_DSN < /opt/gateway-src/sql/migrations/startup/715_route_incidents_pending_state.down.sql

# 5.4 启动服务
systemctl start llm-gateway

# 5.5 确认回滚成功
journalctl -u llm-gateway -f
psql $PROD_DB_DSN -c "SELECT conname, consrc FROM pg_constraint WHERE conrelid = 'route_incidents'::regclass AND conname = 'route_incidents_state_check';"
# 预期：consrc 不包含 'pending'
```

---

## 监控指标（前 24 小时）

### 1. 事件可见性监控

**查询 1：Pending vs Active 比例**

```sql
-- 每小时运行一次
SELECT 
    state,
    COUNT(*) as count,
    ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2) as percentage
FROM route_incidents
WHERE created_at > NOW() - INTERVAL '24 hours'
GROUP BY state;
```

**预期结果：**
- `pending` 事件数 ≈ `active` 事件数 × 2 （假设 2/3 的事件在阈值前恢复）
- 如果 `pending` 为 0，说明阈值逻辑未生效

**查询 2：事件失败连击分布**

```sql
SELECT 
    failure_streak,
    state,
    COUNT(*) as count
FROM route_incidents
WHERE created_at > NOW() - INTERVAL '24 hours'
GROUP BY failure_streak, state
ORDER BY failure_streak, state;
```

**预期结果：**
- `failure_streak=1,2` → `state=pending`
- `failure_streak>=3` → `state=active`

### 2. 凭证恢复率监控

**查询 3：冷却期后恢复成功率**

```sql
-- 找到所有已过冷却期的凭证
WITH cooled_credentials AS (
    SELECT 
        credential_id,
        model,
        recover_at,
        available,
        last_failure_at,
        ROW_NUMBER() OVER (PARTITION BY credential_id, model ORDER BY last_failure_at DESC) as rn
    FROM credential_states
    WHERE recover_at < NOW() - INTERVAL '5 minutes'  -- 已过冷却期 5 分钟
      AND last_failure_at > NOW() - INTERVAL '24 hours'
)
SELECT 
    COUNT(*) as total_cooled,
    SUM(CASE WHEN available THEN 1 ELSE 0 END) as recovered,
    ROUND(100.0 * SUM(CASE WHEN available THEN 1 ELSE 0 END) / COUNT(*), 2) as recovery_rate
FROM cooled_credentials
WHERE rn = 1;
```

**预期结果：**
- `recovery_rate >= 95%`（大部分凭证在冷却后恢复）
- 如果 `recovery_rate < 80%`，检查是否有持续故障

**查询 4：凭证状态持久化验证**

```sql
-- 检查是否有 recover_at 未来但 available=true 的异常状态
SELECT 
    credential_id,
    model,
    available,
    recover_at,
    last_failure_at,
    consecutive_failures
FROM credential_states
WHERE recover_at > NOW() AND available = true;
```

**预期结果：**
- 应该返回 0 行（冷却期内凭证应该是 unavailable）
- 如果有行，说明状态持久化逻辑有问题

### 3. 误报率监控

**查询 5：单次失败后自动恢复的事件**

```sql
-- 找到 pending 状态且 failure_streak=1 的事件，在创建 5 分钟后仍然是 pending 或已恢复
SELECT 
    DATE_TRUNC('hour', created_at) as hour,
    COUNT(*) as single_failure_pending_count
FROM route_incidents
WHERE state = 'pending'
  AND failure_streak = 1
  AND created_at > NOW() - INTERVAL '24 hours'
  AND created_at < NOW() - INTERVAL '5 minutes'
GROUP BY hour
ORDER BY hour;
```

**预期结果：**
- 这个数字应该显著高于修复前（因为单次失败不再创建 active 事件）
- 对比历史数据，误报减少应 >= 60%

**查询 6：事件生命周期分析**

```sql
SELECT 
    CASE 
        WHEN state = 'pending' AND failure_streak < 3 THEN 'Below threshold'
        WHEN state = 'active' THEN 'Visible incident'
        WHEN state = 'recovering' THEN 'Recovering'
        WHEN state = 'recovered' THEN 'Resolved'
        ELSE 'Other'
    END as lifecycle_stage,
    COUNT(*) as count,
    AVG(EXTRACT(EPOCH FROM (COALESCE(visible_at, NOW()) - created_at))) as avg_time_to_visible_seconds
FROM route_incidents
WHERE created_at > NOW() - INTERVAL '24 hours'
GROUP BY lifecycle_stage;
```

**预期结果：**
- `Below threshold` 数量应显著增加
- `Visible incident` 的 `avg_time_to_visible_seconds` 应 > 0（不再是立即可见）

### 4. 特定路由监控（245 服务器）

**查询 7：apiclaude/apigpt/suyun 行为**

```sql
SELECT 
    model,
    provider_id,
    state,
    failure_streak,
    created_at,
    visible_at,
    last_failure_at
FROM route_incidents
WHERE (model = 'claude-3-sonnet' OR model LIKE 'gpt-%' OR model = 'suyun-v1')
  AND created_at > NOW() - INTERVAL '24 hours'
ORDER BY created_at DESC
LIMIT 20;
```

**关注点：**
- 这些路由是否正确使用 `pending` 状态
- `visible_at` 是否在 `failure_streak >= 3` 时才设置
- 是否有异常的状态转换

### 5. 数据库性能监控

**查询 8：route_incidents 表大小和索引效率**

```sql
-- 表大小
SELECT 
    pg_size_pretty(pg_total_relation_size('route_incidents')) as total_size,
    pg_size_pretty(pg_relation_size('route_incidents')) as table_size,
    pg_size_pretty(pg_total_relation_size('route_incidents') - pg_relation_size('route_incidents')) as indexes_size;

-- 索引使用情况
SELECT 
    indexrelname,
    idx_scan,
    idx_tup_read,
    idx_tup_fetch
FROM pg_stat_user_indexes
WHERE schemaname = 'public' AND tablename = 'route_incidents';
```

**预期结果：**
- `uq_route_incidents_active_route` 索引应该有 `idx_scan > 0`（被使用）
- 表大小增长应该在合理范围内（pending 事件会增加记录数）

---

## 异常处理流程

### 异常 1：CHECK 约束错误（23514）

**症状：**
```
ERROR: new row for relation "route_incidents" violates check constraint "route_incidents_state_check"
DETAIL: Failing row contains (... state=pending ...)
```

**原因：** 代码在迁移前部署（约束还不允许 `pending`）

**处理：**
1. 立即停止服务
2. 手动执行迁移脚本：
   ```bash
   psql $PROD_DB_DSN < sql/migrations/startup/715_route_incidents_pending_state.sql
   ```
3. 重启服务
4. 确认无错误

### 异常 2：阈值行为异常（pending 不转 active）

**症状：**
- 大量 `pending` 事件，`failure_streak > 3` 但仍然是 `pending`
- 或者相反，`failure_streak = 1` 就变成 `active`

**诊断：**

```sql
-- 检查配置
SELECT key, value FROM settings WHERE key = 'route_incidents';
```

**预期配置：**
```json
{
  "failure_to_active": 3,
  "success_to_recovering": 1,
  "success_to_recovered": 3
}
```

**处理：**
1. 如果配置错误，更新：
   ```sql
   UPDATE settings 
   SET value = '{"failure_to_active": 3, "success_to_recovering": 1, "success_to_recovered": 3}'
   WHERE key = 'route_incidents';
   ```
2. 重启服务以重新加载配置
3. 验证新事件行为

### 异常 3：凭证状态未持久化

**症状：**
- 重启后 `credential_states` 表中的 `recover_at` 全部为 NULL
- 或者 `available` 字段没有更新

**诊断：**

```bash
# 检查日志中的 Persist 调用
journalctl -u llm-gateway | grep -E "(Persist|MarkFailure|MarkSuccess)" | tail -50
```

**预期日志：**
```
DEBUG MarkFailure: credential_id=123 model=claude-3-sonnet
DEBUG Persist: saved credential state to DB
```

**处理：**
1. 如果没有 Persist 日志，确认代码版本：
   ```bash
   strings /opt/gateway/llm-gateway | grep "Persist"
   ```
2. 如果代码正确但数据库未更新，检查数据库连接和权限
3. 如果问题持续，回滚并调查

### 异常 4：性能下降

**症状：**
- 数据库 CPU 或 IO 使用率显著升高
- `route_incidents` 表查询变慢

**诊断：**

```sql
-- 检查慢查询
SELECT 
    query,
    calls,
    mean_exec_time,
    max_exec_time
FROM pg_stat_statements
WHERE query LIKE '%route_incidents%'
ORDER BY mean_exec_time DESC
LIMIT 10;

-- 检查索引膨胀
SELECT 
    indexrelname,
    pg_size_pretty(pg_relation_size(indexrelid)) as size,
    idx_scan,
    idx_tup_read / NULLIF(idx_scan, 0) as avg_tuples_per_scan
FROM pg_stat_user_indexes
WHERE tablename = 'route_incidents';
```

**处理：**
1. 如果索引膨胀，执行 `REINDEX INDEX uq_route_incidents_active_route;`
2. 如果是查询计划问题，运行 `ANALYZE route_incidents;`
3. 监控是否改善

---

## 监控脚本

**脚本：** `scripts/monitor_715_deployment.sh`

```bash
#!/bin/bash
# monitor_715_deployment.sh
# 自动化监控 715 部署后的关键指标

DB_DSN="${DB_DSN:-postgres://user:pass@localhost:5432/gateway}"
LOG_FILE="monitor_715_$(date +%Y%m%d_%H%M%S).log"

echo "=== 715 Deployment Monitoring Started ===" | tee -a "$LOG_FILE"
echo "Time: $(date)" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"

# 1. 事件状态分布
echo "1. Event State Distribution:" | tee -a "$LOG_FILE"
psql "$DB_DSN" -c "
SELECT state, COUNT(*) as count
FROM route_incidents
WHERE created_at > NOW() - INTERVAL '24 hours'
GROUP BY state;
" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"

# 2. Failure Streak 分布
echo "2. Failure Streak vs State:" | tee -a "$LOG_FILE"
psql "$DB_DSN" -c "
SELECT failure_streak, state, COUNT(*) as count
FROM route_incidents
WHERE created_at > NOW() - INTERVAL '24 hours'
GROUP BY failure_streak, state
ORDER BY failure_streak, state;
" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"

# 3. 凭证状态概览
echo "3. Credential States:" | tee -a "$LOG_FILE"
psql "$DB_DSN" -c "
SELECT 
    COUNT(*) as total,
    SUM(CASE WHEN available THEN 1 ELSE 0 END) as available,
    SUM(CASE WHEN NOT available THEN 1 ELSE 0 END) as unavailable,
    SUM(CASE WHEN recover_at > NOW() THEN 1 ELSE 0 END) as cooling
FROM credential_states;
" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"

# 4. 异常状态检查
echo "4. Anomaly Check:" | tee -a "$LOG_FILE"
psql "$DB_DSN" -c "
SELECT 'Cooling but available' as issue, COUNT(*) as count
FROM credential_states
WHERE recover_at > NOW() AND available = true
UNION ALL
SELECT 'Pending with streak >= 3', COUNT(*)
FROM route_incidents
WHERE state = 'pending' AND failure_streak >= 3
UNION ALL
SELECT 'Active with streak < 3', COUNT(*)
FROM route_incidents
WHERE state = 'active' AND failure_streak < 3;
" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"

# 5. 关键路由状态（245 服务器）
echo "5. Key Routes (apiclaude/apigpt/suyun):" | tee -a "$LOG_FILE"
psql "$DB_DSN" -c "
SELECT model, state, failure_streak, 
       created_at, visible_at, last_failure_at
FROM route_incidents
WHERE (model = 'claude-3-sonnet' OR model LIKE 'gpt-%' OR model = 'suyun-v1')
  AND created_at > NOW() - INTERVAL '2 hours'
ORDER BY created_at DESC
LIMIT 10;
" | tee -a "$LOG_FILE"
echo "" | tee -a "$LOG_FILE"

echo "=== Monitoring Complete ===" | tee -a "$LOG_FILE"
echo "Log saved to: $LOG_FILE"
```

**使用方法：**

```bash
# 赋予执行权限
chmod +x scripts/monitor_715_deployment.sh

# 每小时运行一次（前24小时）
DB_DSN="postgres://prod_user:pass@proddb:5432/gateway" ./scripts/monitor_715_deployment.sh

# 或使用 cron
# 0 * * * * cd /opt/gateway && DB_DSN="..." ./scripts/monitor_715_deployment.sh
```

---

## 成功验收标准

**部署视为成功需满足以下所有条件：**

1. ✅ **迁移成功**
   - `715_route_incidents_pending_state.sql` 执行无错误
   - 约束和索引正确创建

2. ✅ **功能正确**
   - 新失败事件在 `failure_streak < 3` 时保持 `pending`
   - `failure_streak >= 3` 时转为 `active` 并设置 `visible_at`
   - 凭证状态（available/recover_at）正确持久化到数据库

3. ✅ **持久化有效**
   - 服务重启后凭证冷却状态保留
   - 冷却期后凭证自动恢复

4. ✅ **监控指标健康**（24 小时后）
   - Pending 事件数 > 0（阈值逻辑生效）
   - 凭证恢复率 >= 95%
   - 无异常状态（pending 且 streak>=3，或 active 且 streak<3）
   - 无 CHECK 约束错误

5. ✅ **目标路由正常**
   - 245 服务器上的 apiclaude/apigpt/suyun 路由正常工作
   - 事件可见性符合预期
   - 无误报（单次失败立即报警）

6. ✅ **无性能退化**
   - 数据库查询时间无显著增加
   - 服务响应时间无显著增加
   - 磁盘空间使用在合理范围

---

## 后续行动

**24 小时后：**
- [ ] 生成验证报告（基于监控数据）
- [ ] 如果一切正常，部署到其他服务器（if applicable）
- [ ] 更新运维文档

**7 天后：**
- [ ] 回顾误报率变化（对比历史数据）
- [ ] 评估是否需要调整 `failure_to_active` 阈值
- [ ] 清理旧的 `recovered` 事件（if needed）

**相关文档：**
- 事件报告：`docs/incident-reports/2026-09-16-apiclaude-apigpt-suyun-recovery-failure.md`
- 修复 PR：（待补充 GitHub PR 链接）
- 迁移脚本：`sql/migrations/startup/715_route_incidents_pending_state.sql`
