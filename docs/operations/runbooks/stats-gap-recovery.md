# Runbook: 统计对账Gap恢复

**告警名称:** `StatsReconciliationGapHigh`  
**严重级别:** P1 Critical  
**影响范围:** 计费准确性、财务对账  
**平均恢复时间 (MTTR):** 30分钟

---

## 1. 症状识别

### 主要症状
- Prometheus告警: `StatsReconciliationGapHigh` 触发
- gap率 > 2% 持续超过15分钟
- 财务团队报告对账数据不一致

### 关联指标
```promql
# Gap百分比
abs((stats_minute_client_count - stats_minute_rollup_count) / stats_minute_client_count) > 0.02

# 重扫臂延迟
(time() - stats_minute_rollup_last_resweep_timestamp) > 300

# 未处理事件积压
event_inbox_pending_count > 10000
```

---

## 2. 即时响应 (0-5分钟)

### 步骤1: 确认告警真实性

```bash
# SSH登录211服务器
ssh ops@prod-211.internal

# 查看实时gap
mysql llm_gateway -e "
SELECT 
    DATE_FORMAT(minute_timestamp, '%Y-%m-%d %H:%i') AS minute,
    client_count,
    rollup_count,
    ABS((client_count - rollup_count) / NULLIF(client_count, 0) * 100) AS gap_percent
FROM stats_minute_reconciliation
WHERE minute_timestamp >= DATE_SUB(NOW(), INTERVAL 30 MINUTE)
ORDER BY gap_percent DESC
LIMIT 10;
"
```

**预期输出:**
```
+------------------+--------------+--------------+-------------+
| minute           | client_count | rollup_count | gap_percent |
+------------------+--------------+--------------+-------------+
| 2024-01-15 14:25 |         5234 |         4987 |        4.72 |
| 2024-01-15 14:24 |         5189 |         5050 |        2.68 |
| ...
```

**判断:**
- 如 gap_percent > 5%: 🚨 严重，执行紧急修复
- 如 gap_percent 2-5%: ⚠️ 警告，继续排查
- 如 gap_percent < 2%: ✅ 误报，检查告警规则

### 步骤2: 通知相关方

```bash
# 发送Slack通知
curl -X POST https://hooks.slack.com/services/YOUR_WEBHOOK \
  -H 'Content-Type: application/json' \
  -d '{
    "text": ":warning: 统计对账Gap异常",
    "attachments": [{
      "color": "danger",
      "fields": [
        {"title": "Gap率", "value": "4.72%", "short": true},
        {"title": "影响时段", "value": "14:20-14:30", "short": true}
      ]
    }]
  }'

# 通知数据平台团队
echo "Stats gap alert triggered. Gap: 4.72%. Investigating..." | \
  mail -s "[P1] Stats Reconciliation Gap High" data-platform@company.com
```

---

## 3. 诊断分析 (5-15分钟)

### 诊断路径A: 重扫臂状态检查

```bash
# 检查重扫臂进程
ps aux | grep stats-resweep
# 预期: 应有1个正在运行的进程

# 检查最后执行时间
tail -n 100 /var/log/cron/stats-reconcile.log | grep "Resweep completed"
# 预期: 最近一次完成时间 < 5分钟前

# 检查重扫臂延迟指标
curl -s 'http://localhost:9090/api/v1/query?query=time()-stats_minute_rollup_last_resweep_timestamp' | jq -r '.data.result[0].value[1]'
# 预期: < 300秒
```

**异常情况:**
- ❌ **无进程:** 重扫臂崩溃 → 执行修复A1
- ❌ **延迟>300秒:** 重扫臂卡住 → 执行修复A2
- ✅ **正常:** 继续诊断路径B

### 诊断路径B: Event Inbox积压检查

```bash
# 检查未处理事件数
mysql llm_gateway -e "
SELECT 
    COUNT(*) AS pending_count,
    MIN(created_at) AS oldest_event,
    TIMESTAMPDIFF(MINUTE, MIN(created_at), NOW()) AS age_minutes
FROM event_inbox
WHERE processed = FALSE;
"
```

**预期输出:**
```
+---------------+---------------------+-------------+
| pending_count | oldest_event        | age_minutes |
+---------------+---------------------+-------------+
|          823  | 2024-01-15 14:20:15 |           8 |
+---------------+---------------------+-------------+
```

**判断:**
- pending_count > 50000: 🚨 严重积压 → 执行修复B1
- pending_count 10000-50000: ⚠️ 中度积压 → 执行修复B2
- pending_count < 10000: ✅ 正常，继续诊断路径C

### 诊断路径C: 数据库性能检查

```bash
# 检查慢查询
mysql llm_gateway -e "
SELECT 
    DIGEST_TEXT,
    COUNT_STAR AS exec_count,
    ROUND(AVG_TIMER_WAIT/1000000000000, 2) AS avg_seconds,
    ROUND(MAX_TIMER_WAIT/1000000000000, 2) AS max_seconds
FROM performance_schema.events_statements_summary_by_digest
WHERE DIGEST_TEXT LIKE '%stats_minute%'
ORDER BY AVG_TIMER_WAIT DESC
LIMIT 5;
"

# 检查锁等待
mysql llm_gateway -e "SHOW PROCESSLIST;" | grep -i "Waiting for"
```

**异常情况:**
- 慢查询 avg_seconds > 5秒 → 执行修复C1
- 存在锁等待 → 执行修复C2

### 诊断路径D: 分区表状态检查

```bash
# 检查未来分区是否创建
mysql llm_gateway -e "
SELECT 
    PARTITION_NAME,
    FROM_UNIXTIME(PARTITION_DESCRIPTION) AS partition_for_date,
    TABLE_ROWS
FROM INFORMATION_SCHEMA.PARTITIONS
WHERE TABLE_SCHEMA = 'llm_gateway'
AND TABLE_NAME = 'event_inbox'
AND PARTITION_DESCRIPTION > UNIX_TIMESTAMP(NOW())
ORDER BY PARTITION_DESCRIPTION
LIMIT 7;
"
```

**预期:** 应看到未来7天的分区

**异常:** 无未来分区 → 执行修复D1

---

## 4. 修复方案

### 修复A1: 重启重扫臂

**适用场景:** 重扫臂进程不存在

```bash
# 1. 检查是否有僵尸进程
ps aux | grep stats-resweep | grep -v grep

# 2. 如有僵尸进程，强制kill
pkill -9 -f stats-resweep

# 3. 手动启动重扫臂
cd /opt/llm-gateway
nohup ./scripts/stats-resweep.sh >> /var/log/cron/stats-reconcile.log 2>&1 &

# 4. 验证启动成功
sleep 10
ps aux | grep stats-resweep | grep -v grep
# 应看到新进程

# 5. 观察日志
tail -f /var/log/cron/stats-reconcile.log
# 应看到 "Resweep started" 日志
```

**验证恢复:**
```bash
# 等待5分钟后检查gap
sleep 300
mysql llm_gateway -e "
SELECT AVG(ABS((client_count - rollup_count) / NULLIF(client_count, 0) * 100)) AS avg_gap_percent
FROM stats_minute_reconciliation
WHERE minute_timestamp >= DATE_SUB(NOW(), INTERVAL 10 MINUTE);
"
# avg_gap_percent 应 < 1%
```

### 修复A2: 解除重扫臂卡住

**适用场景:** 重扫臂延迟 > 300秒

```bash
# 1. 查看当前重扫状态
mysql llm_gateway -e "
SELECT 
    resweep_status,
    current_minute,
    stuck_duration_seconds
FROM stats_resweep_state
ORDER BY updated_at DESC
LIMIT 1;
"

# 2. 强制重置状态
mysql llm_gateway -e "
UPDATE stats_resweep_state 
SET resweep_status = 'idle', stuck_duration_seconds = 0
WHERE resweep_status = 'running';
"

# 3. Kill卡住的进程
pkill -9 -f stats-resweep

# 4. 重启重扫臂
cd /opt/llm-gateway
nohup ./scripts/stats-resweep.sh >> /var/log/cron/stats-reconcile.log 2>&1 &
```

### 修复B1: 扩容Event消费者 (严重积压)

**适用场景:** pending_count > 50000

```bash
# 1. 检查当前消费者数量
ps aux | grep event-consumer | wc -l

# 2. 临时扩容至4个消费者实例
for i in {1..3}; do
    nohup /opt/llm-gateway/bin/event-consumer \
        --worker-id=$i \
        --batch-size=1000 \
        >> /var/log/event-consumer-$i.log 2>&1 &
done

# 3. 观察消费速率
watch -n 10 'mysql llm_gateway -e "SELECT COUNT(*) FROM event_inbox WHERE processed=FALSE;"'
# 应持续下降

# 4. 待积压降至 < 10000后，停掉临时消费者
pkill -f "event-consumer --worker-id=[1-3]"
```

### 修复B2: 提高消费速率 (中度积压)

**适用场景:** pending_count 10000-50000

```bash
# 临时提高batch size
mysql llm_gateway -e "
UPDATE system_config 
SET value = '5000' 
WHERE key = 'event_consumer_batch_size';
"

# 重启消费者使配置生效
systemctl restart event-consumer

# 观察15分钟，待积压清除后恢复默认配置
# ...
mysql llm_gateway -e "
UPDATE system_config 
SET value = '1000' 
WHERE key = 'event_consumer_batch_size';
"
systemctl restart event-consumer
```

### 修复C1: 优化慢查询

**适用场景:** 慢查询影响性能

```bash
# 1. 识别慢查询
mysql llm_gateway -e "
SELECT DIGEST_TEXT
FROM performance_schema.events_statements_summary_by_digest
WHERE DIGEST_TEXT LIKE '%stats_minute%'
AND AVG_TIMER_WAIT/1000000000000 > 5
LIMIT 1;
" > /tmp/slow_query.txt

# 2. 添加缺失索引 (示例)
mysql llm_gateway -e "
CREATE INDEX idx_stats_minute_timestamp 
ON stats_minute_reconciliation(minute_timestamp) 
ALGORITHM=INPLACE LOCK=NONE;
"

# 3. 更新表统计信息
mysql llm_gateway -e "
ANALYZE TABLE stats_minute_reconciliation;
ANALYZE TABLE event_inbox;
"
```

### 修复C2: 解除数据库锁

**适用场景:** 存在锁等待

```bash
# 1. 识别持锁进程
mysql llm_gateway -e "
SELECT 
    r.trx_id AS blocking_trx,
    r.trx_mysql_thread_id AS blocking_thread,
    TIMESTAMPDIFF(SECOND, r.trx_started, NOW()) AS lock_duration_sec,
    r.trx_query AS blocking_query
FROM information_schema.innodb_lock_waits w
JOIN information_schema.innodb_trx r ON r.trx_id = w.blocking_trx_id
ORDER BY lock_duration_sec DESC;
"

# 2. Kill长时间持锁的线程 (谨慎操作)
# 确认blocking_query不是关键事务后执行:
mysql llm_gateway -e "KILL <blocking_thread_id>;"

# 3. 验证锁释放
mysql llm_gateway -e "
SELECT COUNT(*) AS waiting_locks
FROM information_schema.innodb_lock_waits;
"
# 应返回0
```

### 修复D1: 创建缺失分区

**适用场景:** 未来分区缺失

```bash
# 1. 执行分区维护脚本
cd /opt/llm-gateway
./scripts/partition-maintenance.sh --create-future-partitions --days=7

# 2. 验证分区创建
mysql llm_gateway -e "
SELECT COUNT(*) AS future_partitions
FROM INFORMATION_SCHEMA.PARTITIONS
WHERE TABLE_NAME = 'event_inbox'
AND PARTITION_DESCRIPTION > UNIX_TIMESTAMP(NOW());
"
# 应 >= 7
```

---

## 5. 验证恢复 (15-30分钟)

### 验证检查点

**检查1: Gap收敛**
```bash
# 持续观察gap趋势
for i in {1..6}; do
    echo "=== Check $i/6 at $(date) ==="
    mysql llm_gateway -e "
    SELECT 
        DATE_FORMAT(minute_timestamp, '%H:%i') AS minute,
        ABS((client_count - rollup_count) / NULLIF(client_count, 0) * 100) AS gap_percent
    FROM stats_minute_reconciliation
    WHERE minute_timestamp >= DATE_SUB(NOW(), INTERVAL 10 MINUTE)
    ORDER BY minute_timestamp DESC
    LIMIT 5;
    "
    sleep 300  # 每5分钟检查一次
done
```

**通过标准:** 
- 最近10分钟平均gap < 1%
- 最大gap < 2%
- gap趋势持续下降

**检查2: 重扫臂健康**
```bash
# 检查重扫臂延迟
curl -s 'http://localhost:9090/api/v1/query?query=time()-stats_minute_rollup_last_resweep_timestamp' | jq -r '.data.result[0].value[1]'
# 应 < 120秒
```

**检查3: Event积压清除**
```bash
mysql llm_gateway -e "
SELECT COUNT(*) AS pending FROM event_inbox WHERE processed=FALSE;
"
# 应 < 1000
```

### 恢复确认

```bash
# 运行完整对账脚本
cd /opt/llm-gateway
./scripts/audit/request-stats-reconcile.sh --time-range="last-1-hour"

# 检查输出
# 预期: 
# ✅ Client面 vs Rollup面 gap: 0.45%
# ✅ Rollup面 vs Ledger面 gap: 0.12%
# ✅ Cost链完整性: 98.7%
```

---

## 6. 根因分析

### 常见根因

| 根因 | 触发条件 | 频率 | 预防措施 |
|------|----------|------|----------|
| **重扫臂崩溃** | OOM / 未捕获异常 | 中 | 增加内存，改进错误处理 |
| **Event积压** | 流量突增 / 消费者慢 | 高 | 自动扩容，提高消费速率 |
| **慢查询** | 缺失索引 / 表统计过期 | 中 | 定期ANALYZE，监控查询性能 |
| **分区表维护失败** | Cron任务失败 / 磁盘满 | 低 | 监控分区创建，预留磁盘 |
| **数据库死锁** | 并发事务冲突 | 低 | 优化事务范围，降低隔离级别 |

### 根因调查步骤

1. **查看崩溃日志**
```bash
grep -i "error\|fatal\|panic" /var/log/cron/stats-reconcile.log | tail -50
```

2. **查看系统资源**
```bash
# 内存使用历史
sar -r -f /var/log/sa/sa$(date +%d -d yesterday) | tail -20

# CPU使用历史
sar -u -f /var/log/sa/sa$(date +%d) | tail -20
```

3. **查看数据库慢查询日志**
```bash
pt-query-digest /var/log/mysql/slow.log | head -100
```

---

## 7. 后续行动

### 立即行动 (24小时内)

- [ ] 更新事件报告: `incident-reports/stats-gap-YYYY-MM-DD.md`
- [ ] 调整告警阈值 (如频繁误报)
- [ ] 补充监控指标 (如发现新的先导指标)

### 短期改进 (1周内)

- [ ] 如根因是OOM: 增加重扫臂内存限制
- [ ] 如根因是积压: 实现自动扩容机制
- [ ] 如根因是慢查询: 添加缺失索引
- [ ] 更新runbook (记录新发现的排查技巧)

### 长期优化 (1月内)

- [ ] 评估重扫臂架构改进 (如流式处理)
- [ ] 实现gap自愈机制 (自动触发补统)
- [ ] 建立历史gap基线 (机器学习异常检测)

---

## 8. 参考资料

- [运营体系运维手册](../OPERATIONS_MANUAL.md)
- [统计对账架构文档](../../architecture/stats-reconciliation.md)
- [Event Inbox设计文档](../../design/event-inbox-consumer.md)
- [Grafana仪表盘](http://grafana.internal/d/stats-reconciliation)
- [告警规则](../monitoring/alerts.yml#L10-L45)

---

**Runbook版本:** v1.0  
**最后更新:** 2024-01-XX  
**下次审查:** 2024-04-XX  
**维护团队:** Data Platform Team
