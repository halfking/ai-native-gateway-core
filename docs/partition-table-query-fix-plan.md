# 分区表查询修复计划

**生成时间**: 2026-07-06  
**问题**: https://llmgo.kxpms.cn/request-logs 数据为空  
**根因**: 代码查询父表而非hot表/视图

---

## 1. 架构现状

### 已完成的数据库迁移 (Migration 341-350)

| 表名 | hot表 | 视图 | 父表（分区） |
|------|-------|------|------------|
| request_logs | ✅ request_logs_hot | ✅ request_logs_with_current_month | ✅ request_logs (月度分区) |
| usage_ledger | ✅ usage_ledger_hot | ✅ usage_ledger_with_current_month | ✅ usage_ledger (月度分区) |
| credit_ledger | ✅ credit_ledger_hot | ✅ credit_ledger_with_current_month | ✅ credit_ledger (月度分区) |
| tool_usage_stats | ✅ tool_usage_stats_hot | ✅ tool_usage_stats_with_current_month | ✅ tool_usage_stats (月度分区) |
| request_wal | ✅ request_wal_hot | ✅ request_wal_with_current_month | ✅ request_wal (月度分区) |
| routing_decision_log | ✅ routing_decision_log_hot | ✅ routing_decision_log_with_current_month | ✅ routing_decision_log (月度分区) |
| credential_model_index | ✅ credential_model_index_hot | ✅ credential_model_index_with_current_month | ✅ credential_model_index (月度分区) |
| request_logs_bodies | ✅ request_logs_bodies_hot | ✅ request_logs_bodies_with_current_month | ✅ request_logs_bodies (月度分区) |

### 查询规范

```go
// ✅ 正确：写操作 → hot表
INSERT INTO request_logs_hot (...) VALUES (...);
UPDATE request_logs_hot SET ... WHERE ...;
DELETE FROM request_logs_hot WHERE ...;

// ✅ 正确：查询当前数据（≤7天）→ hot表
SELECT * FROM request_logs_hot WHERE ts >= NOW() - INTERVAL '7 days';

// ✅ 正确：跨时段查询 → 视图
SELECT * FROM request_logs_with_current_month WHERE ts >= NOW() - INTERVAL '30 days';

// ❌ 错误：查询父表（会漏掉hot表数据）
SELECT * FROM request_logs WHERE ts >= NOW() - INTERVAL '7 days';  // 只查分区表，漏hot数据
```

---

## 2. 代码审计结果

### 问题文件统计

| 文件 | 父表查询数 | 影响 |
|------|-----------|------|
| admin/logs.go | 5处 | 🔴 **HIGH** - 前端列表页 |
| admin/analytics.go | 多处 | 🟡 MEDIUM - 分析页面 |
| admin/usage.go | 14处 | 🟡 MEDIUM - 用量统计 |
| bg/auto_index_refresher.go | 4处 | 🟡 MEDIUM - 后台任务 |
| bg/passive_probe_listener.go | 3处 | 🟡 MEDIUM - 健康检查 |

### 测试代码问题

6个测试文件仍引用 `request_logs_default`（已被删除）：
- `bg/passive_probe_listener_test.go` (3处)
- `domains/hooks/observability/telemetry/client_test.go` (3处)

---

## 3. 修复优先级

### P0 (立即修复) - admin/logs.go

**影响**: 前端 /request-logs 页面数据为空

**需要修改的查询**:

1. **Line 446, 453**: COUNT查询
   ```go
   // ❌ 当前
   SELECT COUNT(*) FROM request_logs rl WHERE ...
   
   // ✅ 修复
   SELECT COUNT(*) FROM request_logs_with_current_month rl WHERE ...
   ```

2. **Line 469**: 列表查询
   ```go
   // ❌ 当前
   SELECT ... FROM request_logs rl ...
   
   // ✅ 修复
   SELECT ... FROM request_logs_with_current_month rl ...
   ```

3. **Line 520**: 详情查询
   ```go
   // ❌ 当前
   SELECT ... FROM request_logs rl WHERE request_id = $1
   
   // ✅ 修复
   SELECT ... FROM request_logs_with_current_month rl WHERE request_id = $1
   ```

### P1 (本周修复) - 其他admin模块

- admin/analytics.go
- admin/usage.go (已有部分逻辑根据天数选择hot/view)
- admin/compression_sessions.go
- admin/credential_monitor.go

### P2 (下周修复) - 后台任务

- bg/auto_index_refresher.go
- bg/passive_probe_listener.go

### P3 (测试修复)

- 更新测试代码使用 `*_hot` 表
- 清理 `*_default` 引用

---

## 4. 立即执行的修复

### admin/logs.go 修复要点

```go
// 策略：所有查询统一使用视图
// 原因：
// 1. 前端过滤条件复杂，无法提前判断是否只查7天内
// 2. 视图已优化为2路UNION，性能可接受
// 3. 统一使用视图避免遗漏数据

const logsTable = "request_logs_with_current_month"

// 所有 FROM request_logs 替换为 FROM request_logs_with_current_month
```

---

## 5. 验证步骤

### 验证1: 数据库层

```bash
ssh root@14.103.112.184 -p 25022 "PGPASSWORD='...' psql -h 10.43.237.99 -p 5432 -U llm_gateway llm_gateway" << 'SQL'
-- 1. 确认hot表有数据
SELECT 'hot', count(*) FROM request_logs_hot;

-- 2. 确认视图能查到hot数据
SELECT 'view', count(*) FROM request_logs_with_current_month;

-- 3. 确认父表数据（应该是0或历史分区数据）
SELECT 'parent', count(*) FROM request_logs;

-- 4. 对比最新记录
SELECT 'hot_latest', ts FROM request_logs_hot ORDER BY ts DESC LIMIT 1;
SELECT 'view_latest', ts FROM request_logs_with_current_month ORDER BY ts DESC LIMIT 1;
SQL
```

### 验证2: 代码层

```bash
# 修复后检查
grep -rn "FROM request_logs " --include="*.go" | grep -v "_hot" | grep -v "_with_current_month" | grep -v "_bodies"
# 预期：只剩测试文件 + discovery模块（需单独评估）
```

### 验证3: API层

```bash
# 调用前端API
curl -H "Authorization: Bearer $TOKEN" \
  "https://llmgo.kxpms.cn/admin/logs?page=1&page_size=20"
# 预期：返回数据，items数组非空
```

---

## 6. 回滚方案

如果修复导致问题：

```bash
# 1. 回滚代码
git revert <commit-hash>

# 2. 临时措施：强制promote hot数据到父表
ssh root@14.103.112.184 -p 25022 "PGPASSWORD='...' psql -h 10.43.237.99 -p 5432 -U llm_gateway llm_gateway -c 'SELECT promote_request_logs_hot_to_partition();'"

# 3. 重启服务
kubectl rollout restart -n pms-test deployment/llm-gateway-go-deployment
```

---

## 7. 长期优化

### 7.1 查询优化决策树

```go
// 建议封装为helper函数
func selectRequestLogsTable(timeRange string) string {
    if timeRange <= 7 days {
        return "request_logs_hot"
    }
    return "request_logs_with_current_month"
}
```

### 7.2 性能监控

```sql
-- 添加监控查询
SELECT 
    schemaname,
    tablename,
    pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS size
FROM pg_tables
WHERE tablename IN ('request_logs_hot', 'request_logs', 'request_logs_with_current_month')
ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;
```

---

## 附录：完整SQL验证脚本

见 `scripts/verify-partition-migration.sh`
