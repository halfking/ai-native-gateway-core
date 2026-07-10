# LLM Gateway - Dashboard API & 模块执行器运维文档

## 目录
- [系统架构](#系统架构)
- [监控指标](#监控指标)
- [告警规则](#告警规则)
- [日常运维](#日常运维)
- [故障排查](#故障排查)
- [性能优化](#性能优化)
- [数据库维护](#数据库维护)
- [容量规划](#容量规划)

---

## 系统架构

### 核心组件

#### 1. Dashboard API
提供6个监控接口，用于首页数据展示：
- `/api/admin/dashboard/session-overview` - 会话总览
- `/api/admin/dashboard/session-trend` - 会话趋势
- `/api/admin/dashboard/session-health` - 健康度分布
- `/api/admin/dashboard/session-active` - 活跃会话
- `/api/admin/dashboard/module-stats` - 模块执行统计
- `/api/admin/dashboard/errors` - 错误统计
- `/api/admin/dashboard/performance` - 性能指标

#### 2. 模块执行器 (Module Executor)
Check-Execute-Record 模式，支持：
- 4个Hook集成：SessionAudit / Inspector / HealthWorker / Security
- 三级缓存：内存 → Redis → 数据库
- 自动过期与版本控制

#### 3. 数据存储
**Hot表 (高频查询)**
- `session_module_executions_hot` - 保留7天
- `dashboard_access_events_hot` - 保留30天

**分区表 (历史归档)**
- `session_module_executions` - 按月分区
- `dashboard_access_events` - 按月分区

---

## 监控指标

### Dashboard API 指标

#### 请求指标
```promql
# 请求速率 (按端点)
rate(llmgw_dashboard_api_requests_total[5m])

# 成功率
sum(rate(llmgw_dashboard_api_requests_total{status="success"}[5m])) 
/ sum(rate(llmgw_dashboard_api_requests_total[5m])) * 100

# 错误率
sum(rate(llmgw_dashboard_api_requests_total{status="error"}[5m])) 
/ sum(rate(llmgw_dashboard_api_requests_total[5m])) * 100
```

#### 延迟指标
```promql
# P50/P95/P99 延迟
histogram_quantile(0.50, rate(llmgw_dashboard_api_duration_seconds_bucket[5m]))
histogram_quantile(0.95, rate(llmgw_dashboard_api_duration_seconds_bucket[5m]))
histogram_quantile(0.99, rate(llmgw_dashboard_api_duration_seconds_bucket[5m]))

# 平均延迟
rate(llmgw_dashboard_api_duration_seconds_sum[5m]) 
/ rate(llmgw_dashboard_api_duration_seconds_count[5m])
```

#### 错误指标
```promql
# 数据库错误
rate(llmgw_dashboard_api_errors_total{error_type="database"}[5m])

# 超时错误
rate(llmgw_dashboard_api_errors_total{error_type="timeout"}[5m])

# 慢查询数量
sum(increase(llmgw_dashboard_slow_queries_total[1h]))
```

### 模块执行器指标

#### 执行指标
```promql
# 执行速率 (按模块)
rate(llmgw_module_execution_total[5m])

# 成功率
sum(rate(llmgw_module_execution_total{status="completed"}[5m])) by (module)
/ sum(rate(llmgw_module_execution_total[5m])) by (module) * 100

# 失败率
sum(rate(llmgw_module_execution_total{status="failed"}[5m])) by (module)
/ sum(rate(llmgw_module_execution_total[5m])) by (module) * 100
```

#### 缓存指标
```promql
# 总体缓存命中率
sum(rate(llmgw_module_cache_hit_total[5m])) 
/ (sum(rate(llmgw_module_cache_hit_total[5m])) + sum(rate(llmgw_module_cache_miss_total[5m]))) * 100

# 按缓存层级命中率
sum(rate(llmgw_module_cache_hit_total{level="L0"}[5m])) / sum(rate(llmgw_module_cache_hit_total[5m])) * 100
sum(rate(llmgw_module_cache_hit_total{level="L1"}[5m])) / sum(rate(llmgw_module_cache_hit_total[5m])) * 100
sum(rate(llmgw_module_cache_hit_total{level="L2"}[5m])) / sum(rate(llmgw_module_cache_hit_total[5m])) * 100
```

#### 延迟指标
```promql
# 模块执行延迟 (按模块)
histogram_quantile(0.95, rate(llmgw_module_execution_duration_seconds_bucket[5m]))

# 缓存命中的平均延迟
rate(llmgw_module_execution_duration_seconds_sum{from_cache="true"}[5m])
/ rate(llmgw_module_execution_duration_seconds_count{from_cache="true"}[5m])
```

---

## 告警规则

### 关键告警 (Critical)

#### 1. DashboardAPILowSuccessRate
**条件**: 成功率 < 95%，持续 5 分钟  
**影响**: 用户无法正常访问Dashboard  
**处理**:
```bash
# 1. 检查错误日志
tail -f /var/log/llm-gateway/error.log | grep "dashboard"

# 2. 检查数据库连接
psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "SELECT count(*) FROM pg_stat_activity;"

# 3. 检查 Prometheus 指标
curl http://localhost:9090/metrics | grep dashboard_api_errors

# 4. 重启服务 (最后手段)
systemctl restart llm-gateway
```

#### 2. DashboardAPIDatabaseErrors
**条件**: 数据库错误 > 0.5 errors/sec，持续 2 分钟  
**影响**: 数据查询失败  
**处理**:
```bash
# 1. 检查数据库健康状态
psql -c "SELECT * FROM pg_stat_activity WHERE state != 'idle' ORDER BY query_start;"

# 2. 检查慢查询
psql -c "SELECT query, calls, total_time FROM pg_stat_statements ORDER BY total_time DESC LIMIT 10;"

# 3. 检查连接池
psql -c "SELECT count(*), state FROM pg_stat_activity GROUP BY state;"

# 4. 检查数据库日志
tail -f /var/log/postgresql/postgresql.log
```

#### 3. ModuleExecutionStalled
**条件**: 10分钟内无任何模块执行  
**影响**: 系统功能完全停止  
**处理**:
```bash
# 1. 检查进程状态
ps aux | grep llm-gateway

# 2. 检查系统资源
top -bn1 | head -20
free -h
df -h

# 3. 检查网络连接
netstat -an | grep ESTABLISHED | wc -l

# 4. 检查应用日志
journalctl -u llm-gateway -n 100 --no-pager
```

### 警告告警 (Warning)

#### 4. DashboardAPIHighErrorRate
**条件**: 错误率 > 5%，持续 2 分钟  
**处理**: 分析错误类型，优化查询或增加重试

#### 5. DashboardAPIHighLatency
**条件**: P95 延迟 > 5s，持续 3 分钟  
**处理**: 检查慢查询，优化索引，增加缓存

#### 6. ModuleExecutionHighFailureRate
**条件**: 模块失败率 > 10%，持续 5 分钟  
**处理**: 检查特定模块日志，修复Bug或调整配置

---

## 日常运维

### 健康检查清单 (每日)

```bash
#!/bin/bash
# daily-health-check.sh

echo "========== LLM Gateway Health Check =========="
echo "日期: $(date)"
echo ""

# 1. 服务状态
echo "1. 服务状态"
systemctl status llm-gateway --no-pager | head -5
echo ""

# 2. API 成功率 (过去1小时)
echo "2. Dashboard API 成功率 (过去1小时)"
curl -s "http://localhost:9090/api/v1/query?query=sum(rate(llmgw_dashboard_api_requests_total{status=\"success\"}[1h]))/sum(rate(llmgw_dashboard_api_requests_total[1h]))*100" \
  | jq -r '.data.result[0].value[1]' | awk '{printf "%.2f%%\n", $1}'
echo ""

# 3. 数据库连接数
echo "3. 数据库连接数"
psql -t -c "SELECT count(*) FROM pg_stat_activity WHERE datname = 'llm_gateway';"
echo ""

# 4. Hot表记录数
echo "4. Hot表记录数"
psql -t -c "SELECT 
  (SELECT count(*) FROM session_module_executions_hot) as sme_hot,
  (SELECT count(*) FROM dashboard_access_events_hot) as dae_hot;"
echo ""

# 5. 慢查询数量 (过去24小时)
echo "5. 慢查询数量 (过去24小时)"
curl -s "http://localhost:9090/api/v1/query?query=sum(increase(llmgw_dashboard_slow_queries_total[24h]))" \
  | jq -r '.data.result[0].value[1]'
echo ""

# 6. 模块缓存命中率
echo "6. 模块缓存命中率"
curl -s "http://localhost:9090/api/v1/query?query=sum(rate(llmgw_module_cache_hit_total[1h]))/(sum(rate(llmgw_module_cache_hit_total[1h]))+sum(rate(llmgw_module_cache_miss_total[1h])))*100" \
  | jq -r '.data.result[0].value[1]' | awk '{printf "%.2f%%\n", $1}'
echo ""

# 7. 磁盘使用率
echo "7. 磁盘使用率"
df -h / | tail -1 | awk '{print $5}'
echo ""

# 8. 错误日志 (最近10条)
echo "8. 最近错误日志"
tail -10 /var/log/llm-gateway/error.log
echo ""

echo "========== Check Complete =========="
```

### 周维护任务 (每周一)

```bash
#!/bin/bash
# weekly-maintenance.sh

# 1. 归档旧数据
echo "1. 执行数据归档..."
psql -c "SELECT archive_session_module_executions(7);"
psql -c "SELECT archive_dashboard_events(30);"

# 2. 清理过期分区 (保留6个月)
echo "2. 清理过期分区..."
psql -c "DROP TABLE IF EXISTS session_module_executions_$(date -d '6 months ago' +%Y_%m);"
psql -c "DROP TABLE IF EXISTS dashboard_access_events_$(date -d '6 months ago' +%Y_%m);"

# 3. VACUUM ANALYZE
echo "3. 执行 VACUUM ANALYZE..."
psql -c "VACUUM ANALYZE session_module_executions_hot;"
psql -c "VACUUM ANALYZE dashboard_access_events_hot;"

# 4. 索引健康检查
echo "4. 检查索引膨胀..."
psql -c "SELECT schemaname, tablename, pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS size
FROM pg_tables WHERE tablename LIKE '%_hot' ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;"

# 5. 更新统计信息
echo "5. 更新统计信息..."
psql -c "ANALYZE;"

echo "周维护完成！"
```

---

## 故障排查

### 常见问题

#### Q1: Dashboard API 响应慢
**症状**: P95 延迟 > 5s

**排查步骤**:
```bash
# 1. 查看慢查询
psql -c "SELECT query, calls, mean_time, total_time 
FROM pg_stat_statements 
WHERE query LIKE '%session_summaries%' 
ORDER BY mean_time DESC LIMIT 10;"

# 2. 检查索引使用
psql -c "EXPLAIN ANALYZE 
SELECT * FROM session_summaries 
WHERE last_request_at >= NOW() - INTERVAL '1 hour';"

# 3. 查看表膨胀
psql -c "SELECT schemaname, tablename, 
  pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) 
FROM pg_tables WHERE tablename = 'session_summaries';"

# 4. 缓存命中率
curl http://localhost:9090/metrics | grep module_cache_hit
```

**解决方案**:
1. 添加缺失索引
2. 增加缓存TTL
3. 使用物化视图
4. 垂直/水平分片

#### Q2: 模块执行失败率高
**症状**: 某个模块失败率 > 10%

**排查步骤**:
```bash
# 1. 查看错误日志
tail -100 /var/log/llm-gateway/error.log | grep -A5 "module_name"

# 2. 检查数据库记录
psql -c "SELECT error_message, count(*) 
FROM session_module_executions_hot 
WHERE module_name = 'session_audit' AND status = 'failed' 
GROUP BY error_message ORDER BY count DESC LIMIT 5;"

# 3. 查看模块配置
cat /etc/llm-gateway/config.yaml | grep -A10 "modules:"

# 4. 测试模块单独执行
curl -X POST http://localhost:8080/internal/test-module \
  -d '{"module": "session_audit", "session_id": "test"}'
```

**解决方案**:
1. 修复Bug
2. 调整超时时间
3. 增加重试次数
4. 临时禁用模块

#### Q3: 数据库连接耗尽
**症状**: `pq: sorry, too many clients already`

**排查步骤**:
```bash
# 1. 查看当前连接
psql -c "SELECT count(*), state FROM pg_stat_activity GROUP BY state;"

# 2. 查看长时间运行的查询
psql -c "SELECT pid, now() - query_start AS duration, query 
FROM pg_stat_activity 
WHERE state != 'idle' AND now() - query_start > interval '1 minute';"

# 3. 检查连接池配置
grep -A5 "database:" /etc/llm-gateway/config.yaml
```

**解决方案**:
```bash
# 1. 杀死僵尸连接
psql -c "SELECT pg_terminate_backend(pid) 
FROM pg_stat_activity 
WHERE state = 'idle in transaction' 
AND now() - state_change > interval '10 minutes';"

# 2. 增加 max_connections
psql -c "ALTER SYSTEM SET max_connections = 200;"
psql -c "SELECT pg_reload_conf();"

# 3. 调整应用连接池
# 编辑 config.yaml
# database:
#   max_connections: 50
#   max_idle_connections: 10
```

---

## 性能优化

### 数据库优化

#### 1. 索引优化
```sql
-- 检查缺失索引
SELECT schemaname, tablename, attname, n_distinct, correlation
FROM pg_stats
WHERE schemaname = 'public' 
AND tablename IN ('session_module_executions_hot', 'dashboard_access_events_hot')
ORDER BY abs(correlation) DESC;

-- 创建复合索引 (示例)
CREATE INDEX CONCURRENTLY idx_sme_tenant_module_status 
ON session_module_executions_hot(tenant_id, module_name, status, created_at DESC);
```

#### 2. 查询优化
```sql
-- 使用 EXPLAIN ANALYZE 分析
EXPLAIN (ANALYZE, BUFFERS) 
SELECT * FROM session_module_executions_hot 
WHERE gw_session_id = 'xxx' AND module_name = 'session_audit';

-- 优化建议:
-- 1. 避免 SELECT *，只查询需要的字段
-- 2. 使用 LIMIT 限制结果集
-- 3. 合理使用 JOIN 而非子查询
-- 4. 避免在 WHERE 中使用函数
```

#### 3. 配置优化
```sql
-- PostgreSQL 配置建议 (根据服务器规格调整)
ALTER SYSTEM SET shared_buffers = '4GB';
ALTER SYSTEM SET effective_cache_size = '12GB';
ALTER SYSTEM SET maintenance_work_mem = '1GB';
ALTER SYSTEM SET checkpoint_completion_target = 0.9;
ALTER SYSTEM SET wal_buffers = '16MB';
ALTER SYSTEM SET default_statistics_target = 100;
ALTER SYSTEM SET random_page_cost = 1.1;
ALTER SYSTEM SET effective_io_concurrency = 200;
ALTER SYSTEM SET work_mem = '16MB';
ALTER SYSTEM SET min_wal_size = '1GB';
ALTER SYSTEM SET max_wal_size = '4GB';

-- 重启生效
SELECT pg_reload_conf();
```

### 应用层优化

#### 1. 缓存策略
```yaml
# config.yaml
cache:
  # 内存缓存 (L0)
  memory:
    max_size: 10000
    ttl: 60s
  
  # Redis缓存 (L1)
  redis:
    enabled: true
    ttl: 600s
  
  # 数据库缓存 (L2)
  database:
    ttl: 3600s

# 按模块配置不同TTL
modules:
  session_audit:
    cache_ttl: 3600  # 1小时
  session_inspector:
    cache_ttl: 300   # 5分钟
  session_health:
    cache_ttl: 3600  # 1小时
  security:
    cache_ttl: 1800  # 30分钟
```

#### 2. 连接池调优
```yaml
database:
  max_connections: 50
  max_idle_connections: 10
  max_lifetime: 3600s
  connection_timeout: 10s
```

---

## 数据库维护

### 分区管理

#### 创建新分区
```bash
# 手动创建下3个月的分区
for i in {0..2}; do
  month=$(date -d "+$i month" +%Y_%m)
  psql -c "SELECT ensure_session_module_executions_partition('$month');"
  psql -c "SELECT ensure_dashboard_events_partition('$month');"
done
```

#### 删除旧分区
```bash
# 删除6个月前的分区
old_month=$(date -d '6 months ago' +%Y_%m)
psql -c "DROP TABLE IF EXISTS session_module_executions_$old_month CASCADE;"
psql -c "DROP TABLE IF EXISTS dashboard_access_events_$old_month CASCADE;"
```

### 归档策略

#### 立即归档
```sql
-- 归档 session_module_executions_hot (7天前)
SELECT archive_session_module_executions(7);

-- 归档 dashboard_access_events_hot (30天前)
SELECT archive_dashboard_events(30);
```

#### 查看归档统计
```sql
SELECT 
  'sme_hot' as table_name,
  count(*) as total_rows,
  pg_size_pretty(pg_total_relation_size('session_module_executions_hot')) as size
FROM session_module_executions_hot
UNION ALL
SELECT 
  'dae_hot',
  count(*),
  pg_size_pretty(pg_total_relation_size('dashboard_access_events_hot'))
FROM dashboard_access_events_hot;
```

---

## 容量规划

### 估算公式

#### 1. 存储容量
```
# session_module_executions_hot
每行约 500 bytes
每天执行次数: 100万
保留7天: 100万 * 7 * 500 bytes ≈ 3.5 GB

# dashboard_access_events_hot
每行约 400 bytes
每天访问次数: 10万
保留30天: 10万 * 30 * 400 bytes ≈ 1.2 GB

# 分区归档表 (按月)
每月约: 15 GB (session_module_executions)
每月约: 1.2 GB (dashboard_access_events)
保留6个月: (15 + 1.2) * 6 ≈ 100 GB
```

#### 2. 内存需求
```
# PostgreSQL
shared_buffers: 物理内存的25% (建议 4-8 GB)
effective_cache_size: 物理内存的50-75% (建议 12-16 GB)

# LLM Gateway
内存缓存: 500 MB (10000条记录 * 50KB)
应用堆内存: 2-4 GB
```

#### 3. 网络带宽
```
# Dashboard API
平均QPS: 100
平均响应大小: 50 KB
带宽需求: 100 * 50 KB = 5 MB/s = 40 Mbps
```

### 扩容建议

| 指标 | 当前 | 告警阈值 | 扩容动作 |
|------|------|---------|---------|
| CPU使用率 | < 60% | > 80% | 增加实例 |
| 内存使用率 | < 70% | > 85% | 增加内存 |
| 磁盘使用率 | < 70% | > 80% | 扩容磁盘/归档 |
| 数据库连接数 | < 60% | > 80% | 增加连接池 |
| API P95延迟 | < 1s | > 5s | 优化查询/缓存 |
| 模块缓存命中率 | > 80% | < 50% | 增加TTL/内存 |

---

## 联系方式

- **运维负责人**: ops@example.com
- **告警通知**: alerts@example.com
- **Grafana**: https://grafana.example.com/d/dashboard-api
- **Prometheus**: https://prometheus.example.com
- **文档更新**: 2024-07-10
