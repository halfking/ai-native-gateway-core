# 运营体系运维手册
# Operations System Manual v1.0

**最后更新：** 2024-01-XX  
**维护团队：** Operations Engineering  
**紧急联系：** ops-oncall@company.com

---

## 📋 目录

- [1. 系统概览](#1-系统概览)
- [2. 标准操作程序 (SOP)](#2-标准操作程序-sop)
- [3. 故障排查指南](#3-故障排查指南)
- [4. 紧急响应预案](#4-紧急响应预案)
- [5. 日常运维检查清单](#5-日常运维检查清单)
- [6. 关键指标解读](#6-关键指标解读)
- [7. 红线守则](#7-红线守则)
- [8. 变更管理](#8-变更管理)

---

## 1. 系统概览

### 1.1 六大运营组件

| 组件 | 职责 | 关键指标 | 依赖 |
|------|------|----------|------|
| **请求统计对账系统** | 保证计费准确性 | gap率<2%, 重扫延迟<5min | MySQL, 分区表 |
| **市场行情同步** | 保证定价时效性 | 同步延迟<24h, 拒绝行<100/天 | 外部API, FX汇率 |
| **探测恢复系统** | 保证凭据可用性 | 挂起凭据恢复时间<24h | 回退梯, balance_floor |
| **营销活动管理** | 保证活动准确执行 | 兑现成功率>95%, 预算偏差<1% | redemptions表, ledger |
| **定价版本化** | 保证价格历史完整 | 现行价唯一性, 期连续性 | model_price_versions |
| **免费模型系统** | 保证免费服务可用 | 扫描周期<2h, API可用性>99% | freellmapi, omnifree |

### 1.2 数据流拓扑

```
[客户端请求] 
    ↓
[event_inbox] → [stats_minute_client] ← [统计对账重扫臂]
    ↓                                        ↓
[routing] → [billing计算] ← [model_price_versions]
    ↓                            ↑
[履行] ← [credential探测恢复]  [market_price_sync]
    ↓                            
[redemptions] → [营销活动兑现]
```

### 1.3 部署架构

- **生产环境：** 211服务器
- **Cron任务：** market-sync (0 3 * * *), stats-reconcile (*/5 * * * *)
- **监控栈：** Prometheus + Grafana + Alertmanager
- **数据库：** MySQL 8.0+（需分区表支持）

---

## 2. 标准操作程序 (SOP)

### 2.1 SOP-001: 每日健康检查

**频率：** 每日早上 10:00 AM  
**执行人：** 值班运维  
**预计耗时：** 15分钟

#### 检查步骤

1. **检查Cron任务执行状态**
```bash
# 登录211服务器
ssh ops@211.production.internal

# 检查cron任务日志
crontab -l | grep -E "market-sync|stats-reconcile"
tail -n 50 /var/log/cron/market-sync.log
tail -n 50 /var/log/cron/stats-reconcile.log
```

2. **检查关键指标**
```bash
# 统计对账gap率
curl -s http://localhost:9090/api/v1/query?query='abs((stats_minute_client_count-stats_minute_rollup_count)/stats_minute_client_count)' | jq '.data.result[0].value[1]'

# 市场行情同步延迟（秒）
curl -s http://localhost:9090/api/v1/query?query='time()-market_price_sync_last_success_timestamp' | jq '.data.result[0].value[1]'

# 探测恢复挂起凭据数
curl -s http://localhost:9090/api/v1/query?query='count(credential_health_status{status="suspended"})' | jq '.data.result[0].value[1]'
```

3. **检查告警状态**
```bash
# 查看活跃告警
curl -s http://alertmanager:9093/api/v2/alerts | jq '.[] | select(.status.state=="active") | {name: .labels.alertname, severity: .labels.severity}'
```

4. **记录日报**
   - 填写《运营系统日报》模板：`docs/operations/daily_reports/YYYY-MM-DD.md`
   - 标注异常项和待跟进事项

#### 通过标准

- [ ] 所有Cron任务最近24小时内成功执行
- [ ] 统计对账gap率 < 2%
- [ ] 市场行情同步延迟 < 24小时
- [ ] 无P0/P1级别活跃告警
- [ ] 挂起凭据数 < 5个

#### 异常升级

- 如发现通过标准不满足，立即执行对应故障排查流程（见第3节）
- P0告警：立即通知on-call工程师
- P1告警：1小时内响应

---

### 2.2 SOP-002: 市场行情同步执行

**触发时机：** 每日 03:00 AM（自动cron） 或 手动触发  
**执行人：** 系统自动 / 运维工程师  
**预计耗时：** 5-10分钟

#### 自动执行（Cron）

```bash
# Cron配置（已部署在211服务器）
0 3 * * * /opt/llm-gateway/scripts/market-sync.sh >> /var/log/cron/market-sync.log 2>&1
```

#### 手动触发（紧急补偿）

```bash
# 1. 登录211服务器
ssh ops@211.production.internal

# 2. 切换到项目目录
cd /opt/llm-gateway

# 3. 执行同步脚本（带时间窗口）
./scripts/market-sync.sh --start-date=2024-01-15 --end-date=2024-01-16

# 4. 验证执行结果
echo $?  # 应返回0
tail -n 100 /var/log/cron/market-sync.log
```

#### 执行检查点

1. **前置检查**
   - [ ] 确认外部API可访问（market.external.com）
   - [ ] 确认数据库连接正常
   - [ ] 确认磁盘空间充足（> 10GB）

2. **执行中监控**
   - 观察日志输出，确认无ERROR级别日志
   - 观察指标：`market_price_reference_sync_rejected_rows` 应为0或极小值

3. **后置验证**
   - [ ] 查询`market_price_reference`表，确认新数据写入
   - [ ] 验证汇率更新时间：`SELECT updated_at FROM fx_rates ORDER BY updated_at DESC LIMIT 1;`
   - [ ] 触发契约测试：`make test-contract-market-sync`

#### 红线检查（自动）

同步脚本内置红线保护，如触发则自动回滚：
- ❌ 禁止写入`routing`表
- ❌ 禁止写入`billing_rules`表
- ✅ 仅允许写入`market_price_reference`表

#### 失败处理

- **失败现象：** 脚本返回非0退出码
- **立即行动：** 执行 [故障排查 FR-002](#fr-002-市场行情同步失败)
- **通知：** 如连续失败2次，通知定价团队

---

### 2.3 SOP-003: 统计对账重扫执行

**触发时机：** 每5分钟（自动cron）  
**执行人：** 系统自动  
**预计耗时：** 30-60秒

#### 自动执行（Cron）

```bash
# Cron配置（已部署在211服务器）
*/5 * * * * /opt/llm-gateway/scripts/stats-resweep.sh >> /var/log/cron/stats-reconcile.log 2>&1
```

#### 手动触发（应急修复）

```bash
# 1. 登录211服务器
ssh ops@211.production.internal

# 2. 执行重扫（指定时间范围）
cd /opt/llm-gateway
./scripts/stats-resweep.sh --minute-start="2024-01-15 14:00" --minute-end="2024-01-15 14:30"

# 3. 验证gap是否收敛
mysql -e "
SELECT 
    minute_timestamp,
    client_count,
    rollup_count,
    ABS((client_count - rollup_count) / client_count * 100) AS gap_percent
FROM stats_minute_reconciliation
WHERE minute_timestamp BETWEEN '2024-01-15 14:00' AND '2024-01-15 14:30'
ORDER BY gap_percent DESC
LIMIT 10;
"
```

#### 执行检查点

1. **重扫臂延迟监控**
   - 指标：`stats_minute_rollup_last_resweep_timestamp`
   - 阈值：< 5分钟
   - 告警：超过5分钟触发 `StatsResweepArmDelayed`

2. **Gap收敛验证**
   - 正常状态：gap率在重扫后1小时内降至 < 0.5%
   - 异常状态：gap率持续 > 2% 超过15分钟

3. **分区表健康检查**
```bash
# 检查分区表未来7天分区是否创建
mysql -e "
SELECT 
    PARTITION_NAME, 
    PARTITION_DESCRIPTION,
    TABLE_ROWS
FROM INFORMATION_SCHEMA.PARTITIONS
WHERE TABLE_NAME = 'event_inbox'
AND PARTITION_DESCRIPTION > UNIX_TIMESTAMP(NOW())
ORDER BY PARTITION_DESCRIPTION
LIMIT 10;
"
```

#### 异常处理

- **异常1：重扫臂卡住（延迟>10分钟）**
  - 检查进程：`ps aux | grep stats-resweep`
  - 如僵死进程存在，kill后重启：`kill -9 <PID> && ./scripts/stats-resweep.sh`

- **异常2：Gap持续高位（>2%超过1小时）**
  - 执行 [故障排查 FR-001](#fr-001-统计对账gap高)

---

### 2.4 SOP-004: 探测恢复系统维护

**频率：** 每周一次  
**执行人：** 运维工程师  
**预计耗时：** 30分钟

#### 检查步骤

1. **检查长期挂起凭据**
```sql
-- 查找挂起超过24小时的凭据
SELECT 
    credential_id,
    provider,
    last_probe_at,
    suspended_since,
    TIMESTAMPDIFF(HOUR, suspended_since, NOW()) AS hours_suspended
FROM credential_health
WHERE status = 'suspended'
AND TIMESTAMPDIFF(HOUR, suspended_since, NOW()) > 24
ORDER BY hours_suspended DESC;
```

2. **手动触发探测恢复**
```bash
# 对指定凭据执行探测
curl -X POST http://localhost:8080/api/internal/probe/trigger \
  -H "Content-Type: application/json" \
  -d '{"credential_id": "cred-xxx", "force_fallback": true}'
```

3. **检查回退梯配置**
```bash
# 查看当前回退梯配置
cat /opt/llm-gateway/config/probe_fallback_ladder.json

# 验证配置有效性
./scripts/validate-probe-config.sh
```

4. **清理僵尸探测记录**
```sql
-- 清理30天前的探测历史
DELETE FROM credential_probe_history
WHERE created_at < DATE_SUB(NOW(), INTERVAL 30 DAY)
LIMIT 10000;
```

#### 通过标准

- [ ] 挂起超24h的凭据数 < 3个
- [ ] 回退梯全失败率 < 20%（过去7天）
- [ ] warnGate节流触发 < 10次/小时

---

### 2.5 SOP-005: 营销活动上线检查

**触发时机：** 新营销活动上线前  
**执行人：** 运营工程师 + 运维工程师  
**预计耗时：** 1小时

#### 上线前检查清单

1. **活动配置验证**
```sql
-- 验证活动配置完整性
SELECT 
    campaign_id,
    name,
    start_date,
    end_date,
    total_budget_credits,
    discount_rate,
    status
FROM promotion_campaigns
WHERE campaign_id = '<new_campaign_id>';

-- 检查资格规则
SELECT * FROM campaign_eligibility_rules WHERE campaign_id = '<new_campaign_id>';
```

2. **预算额度检查**
```sql
-- 确认预算未超预期
SELECT 
    SUM(total_budget_credits) AS total_budget_all_campaigns
FROM promotion_campaigns
WHERE status = 'active'
AND end_date > NOW();
-- 应 < 公司月度营销预算上限
```

3. **折扣计算测试**
```bash
# 执行活动契约测试
cd /opt/llm-gateway
make test-contract-campaign campaign_id=<new_campaign_id>

# 预期输出：所有折扣场景测试通过
```

4. **监控配置**
```yaml
# 在 alerts.yml 中添加活动专属告警
- alert: Campaign<ID>BudgetNearExhaustion
  expr: |
    (promotion_campaigns_granted_credits{campaign_id="<new_campaign_id>"}
    / promotion_campaigns_total_budget_credits{campaign_id="<new_campaign_id>"}) > 0.80
  labels:
    severity: warning
    campaign_id: "<new_campaign_id>"
```

#### 上线后验证（1小时内）

```sql
-- 检查兑现成功率
SELECT 
    COUNT(*) AS total_redemptions,
    SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) AS successful,
    SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) AS failed,
    (SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) / COUNT(*) * 100) AS success_rate
FROM promotion_redemptions
WHERE campaign_id = '<new_campaign_id>'
AND created_at > DATE_SUB(NOW(), INTERVAL 1 HOUR);
-- success_rate 应 > 95%
```

---

### 2.6 SOP-006: 定价版本发布

**触发时机：** 模型定价变更  
**执行人：** 定价团队 + 运维工程师  
**预计耗时：** 30分钟

#### 发布步骤

1. **准备新版本数据**
```sql
-- 插入新价格版本（示例）
INSERT INTO model_price_versions 
(model_id, mode, input_price_per_million, output_price_per_million, valid_from, valid_to, version, is_current)
VALUES 
('gpt-4-turbo', 'standard', 10.00, 30.00, '2024-02-01 00:00:00', NULL, 2, FALSE);
```

2. **执行价格切换**
```bash
# 方式1: 使用管理API（推荐）
curl -X PUT http://localhost:8080/api/admin/maas/model-price-mode \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "model_id": "gpt-4-turbo",
    "mode": "standard",
    "new_version": 2,
    "effective_at": "2024-02-01T00:00:00Z"
  }'

# 方式2: 直接SQL（应急用）
UPDATE model_price_versions SET is_current = FALSE 
WHERE model_id = 'gpt-4-turbo' AND mode = 'standard' AND is_current = TRUE;

UPDATE model_price_versions SET is_current = TRUE, valid_from = '2024-02-01 00:00:00'
WHERE model_id = 'gpt-4-turbo' AND mode = 'standard' AND version = 2;
```

3. **验证切换结果**
```sql
-- 验证唯一性：每个(model, mode)只有1个is_current=TRUE
SELECT model_id, mode, COUNT(*) AS current_count
FROM model_price_versions
WHERE is_current = TRUE
GROUP BY model_id, mode
HAVING current_count > 1;
-- 应返回0行

-- 验证连续性：检查时间段无空洞
SELECT 
    model_id,
    mode,
    version,
    valid_from,
    LEAD(valid_from) OVER (PARTITION BY model_id, mode ORDER BY valid_from) AS next_valid_from,
    valid_to
FROM model_price_versions
WHERE model_id = 'gpt-4-turbo' AND mode = 'standard'
ORDER BY valid_from;
-- valid_to 应等于 next_valid_from（或NULL if 最新版本）
```

4. **触发缓存刷新**
```bash
# 刷新定价缓存
curl -X POST http://localhost:8080/api/internal/cache/refresh?resource=model_pricing
```

#### 回滚预案

```sql
-- 回滚到上一版本
UPDATE model_price_versions 
SET is_current = TRUE 
WHERE model_id = 'gpt-4-turbo' AND mode = 'standard' AND version = 1;

UPDATE model_price_versions 
SET is_current = FALSE 
WHERE model_id = 'gpt-4-turbo' AND mode = 'standard' AND version = 2;
```

---

## 3. 故障排查指南

### FR-001: 统计对账Gap高

**症状：** 告警 `StatsReconciliationGapHigh` 触发，gap率 > 2%  
**影响：** 计费准确性受影响，财务对账困难

#### 排查步骤

**步骤1: 确认gap分布**
```sql
-- 查看最近1小时gap分布
SELECT 
    minute_timestamp,
    client_count,
    rollup_count,
    ABS(client_count - rollup_count) AS gap_abs,
    ABS((client_count - rollup_count) / client_count * 100) AS gap_percent
FROM stats_minute_reconciliation
WHERE minute_timestamp >= DATE_SUB(NOW(), INTERVAL 1 HOUR)
ORDER BY gap_percent DESC
LIMIT 20;
```

**步骤2: 检查event_inbox积压**
```sql
-- 检查未处理事件
SELECT 
    COUNT(*) AS pending_events,
    MIN(created_at) AS oldest_event
FROM event_inbox
WHERE processed = FALSE;
-- 如 pending_events > 10000，说明消费滞后
```

**步骤3: 检查重扫臂状态**
```bash
# 检查重扫臂进程
ps aux | grep stats-resweep

# 检查最后执行时间
tail -n 100 /var/log/cron/stats-reconcile.log | grep "Resweep completed"
```

**步骤4: 检查数据库锁等待**
```sql
-- 查看锁等待
SHOW PROCESSLIST;
-- 寻找 State = 'Waiting for table metadata lock' 或 'Locked' 的进程
```

#### 常见原因与解决方案

| 原因 | 症状 | 解决方案 |
|------|------|----------|
| **重扫臂卡住** | 重扫时间戳长时间不更新 | Kill僵死进程，手动触发重扫 |
| **event_inbox积压** | pending_events > 10000 | 扩容消费者进程，或临时提高消费速率 |
| **分区表切换失败** | 日志出现partition error | 执行 SOP-007 分区表维护 |
| **数据库慢查询** | gap收敛缓慢 | 优化慢查询，添加索引 |
| **成本链断裂** | cost_usd全0行占比高 | 检查pricing服务可用性，补全成本数据 |

#### 应急修复

```bash
# 1. 强制重扫最近1小时
./scripts/stats-resweep.sh --minute-start="$(date -u -d '1 hour ago' '+%Y-%m-%d %H:00')" --minute-end="$(date -u '+%Y-%m-%d %H:%M')"

# 2. 如仍未收敛，扩大重扫范围至3小时
./scripts/stats-resweep.sh --minute-start="$(date -u -d '3 hours ago' '+%Y-%m-%d %H:00')" --minute-end="$(date -u '+%Y-%m-%d %H:%M')"

# 3. 触发全量契约测试
make test-contract-stats
```

#### 升级条件

- gap率持续 > 5% 超过2小时：P1事件，通知数据平台团队
- 财务对账受阻：P0事件，立即升级CTO

---

### FR-002: 市场行情同步失败

**症状：** 告警 `MarketPriceSyncFailed` 触发，上次成功同步 > 24小时  
**影响：** 定价数据陈旧，可能使用过期价格计费

#### 排查步骤

**步骤1: 检查同步日志**
```bash
tail -n 200 /var/log/cron/market-sync.log | grep -E "ERROR|FATAL|failed"
```

**步骤2: 检查外部API可达性**
```bash
# 测试外部API连通性
curl -I https://market.external.com/api/v1/prices

# 检查DNS解析
nslookup market.external.com

# 检查网络路由
traceroute market.external.com
```

**步骤3: 检查API凭据有效性**
```bash
# 测试API认证
curl -H "Authorization: Bearer ${MARKET_API_KEY}" https://market.external.com/api/v1/auth/validate

# 如返回401，说明API key失效
```

**步骤4: 检查数据库连接**
```bash
# 测试数据库连接
mysql -h db.production.internal -u market_sync_user -p -e "SELECT 1;"
```

#### 常见原因与解决方案

| 原因 | 症状 | 解决方案 |
|------|------|----------|
| **外部API限流** | HTTP 429 Too Many Requests | 增加请求间隔，或联系供应商提额 |
| **API凭据过期** | HTTP 401 Unauthorized | 更新`MARKET_API_KEY`环境变量，重启服务 |
| **网络故障** | Connection timeout | 检查防火墙规则，联系网络团队 |
| **响应数据格式变更** | JSON解析错误 | 更新解析逻辑，兼容新格式 |
| **数据库写入失败** | Duplicate key error | 检查唯一索引冲突，清理脏数据 |

#### 应急修复

```bash
# 1. 手动触发同步（跳过失败的日期）
./scripts/market-sync.sh --start-date="2024-01-16" --end-date="2024-01-17" --skip-errors

# 2. 如API完全不可用，使用备份数据源
./scripts/market-sync.sh --use-backup-source

# 3. 最坏情况：使用上一次成功的价格数据
mysql -e "
INSERT INTO market_price_reference (model_id, provider, price_per_million, updated_at)
SELECT model_id, provider, price_per_million, NOW()
FROM market_price_reference_backup
WHERE backup_date = (SELECT MAX(backup_date) FROM market_price_reference_backup);
"
```

#### 红线确认

- ✅ 确认脚本未写入`routing`或`billing_rules`表
- ✅ 运行契约测试：`make test-contract-market-sync`

---

### FR-003: 探测恢复失效

**症状：** 告警 `CredentialStuckSuspended` 触发，凭据挂起 > 24小时  
**影响：** 路由可用凭据减少，请求成功率下降

#### 排查步骤

**步骤1: 确认挂起凭据详情**
```sql
SELECT 
    c.credential_id,
    c.provider,
    c.model_id,
    ch.status,
    ch.suspended_since,
    ch.last_probe_at,
    ch.consecutive_failures,
    ch.last_error_message
FROM credentials c
JOIN credential_health ch ON c.credential_id = ch.credential_id
WHERE ch.status = 'suspended'
AND TIMESTAMPDIFF(HOUR, ch.suspended_since, NOW()) > 24;
```

**步骤2: 检查探测任务执行**
```bash
# 检查探测任务cron
crontab -l | grep credential-probe

# 检查最近探测日志
tail -n 100 /var/log/cron/credential-probe.log
```

**步骤3: 手动执行探测**
```bash
# 对单个凭据强制探测
curl -X POST http://localhost:8080/api/internal/probe/trigger \
  -H "Content-Type: application/json" \
  -d '{
    "credential_id": "cred-stuck-123",
    "force_fallback": true,
    "bypass_warn_gate": false
  }'

# 观察返回结果
```

**步骤4: 检查balance_floor守卫**
```sql
-- 查看余额守卫配置
SELECT * FROM balance_floor_guards WHERE credential_id = 'cred-stuck-123';

-- 检查是否触发warnGate节流
SELECT 
    COUNT(*) AS throttle_count,
    MAX(throttled_at) AS last_throttle
FROM balance_floor_guard_throttle_log
WHERE credential_id = 'cred-stuck-123'
AND throttled_at > DATE_SUB(NOW(), INTERVAL 24 HOUR);
```

#### 常见原因与解决方案

| 原因 | 症状 | 解决方案 |
|------|------|----------|
| **回退梯全失败** | 所有fallback模型都失败 | 更新回退梯配置，添加新fallback模型 |
| **balance_floor过度保守** | warnGate频繁节流 | 调整balance_floor阈值或禁用warnGate |
| **凭据真实失效** | API key被供应商封禁 | 联系供应商解封，或废弃该凭据 |
| **探测任务未运行** | cron任务失效 | 重启cron服务，确认任务正常调度 |
| **数据库re-arm失败** | 证据门控状态未更新 | 手动执行 `UPDATE balance_floor_guards SET armed = TRUE` |

#### 应急修复

```bash
# 1. 绕过warnGate，强制探测
curl -X POST http://localhost:8080/api/internal/probe/trigger \
  -d '{"credential_id": "cred-stuck-123", "bypass_warn_gate": true}'

# 2. 如凭据真实失效，手动标记为永久失效
mysql -e "
UPDATE credentials SET status = 'permanently_disabled' WHERE credential_id = 'cred-stuck-123';
DELETE FROM routing WHERE credential_id = 'cred-stuck-123';
"

# 3. 如问题广泛存在，批量重置探测状态
mysql -e "
UPDATE credential_health 
SET status = 'healthy', consecutive_failures = 0 
WHERE status = 'suspended' 
AND TIMESTAMPDIFF(HOUR, suspended_since, NOW()) > 48;
"
```

---

### FR-004: 营销活动异常

**症状：** 告警 `CampaignRedemptionFailureRateHigh`，兑现失败率 > 5%  
**影响：** 用户投诉增加，品牌信任受损

#### 排查步骤

**步骤1: 统计失败分布**
```sql
SELECT 
    failure_reason,
    COUNT(*) AS failure_count,
    COUNT(*) / (SELECT COUNT(*) FROM promotion_redemptions WHERE created_at > DATE_SUB(NOW(), INTERVAL 1 HOUR)) * 100 AS failure_percent
FROM promotion_redemptions
WHERE status = 'failed'
AND created_at > DATE_SUB(NOW(), INTERVAL 1 HOUR)
GROUP BY failure_reason
ORDER BY failure_count DESC;
```

**步骤2: 检查活动状态**
```sql
SELECT 
    campaign_id,
    name,
    status,
    total_budget_credits,
    granted_credits,
    (granted_credits / total_budget_credits * 100) AS budget_used_percent,
    start_date,
    end_date
FROM promotion_campaigns
WHERE campaign_id = '<failing_campaign_id>';
```

**步骤3: 检查资格规则**
```sql
-- 查看资格规则配置
SELECT * FROM campaign_eligibility_rules WHERE campaign_id = '<failing_campaign_id>';

-- 检查是否规则过于严格
SELECT 
    COUNT(*) AS eligible_users
FROM users u
WHERE <eligibility_conditions>;
-- 如结果为0，说明无人符合资格
```

**步骤4: 检查折扣计算逻辑**
```bash
# 触发折扣计算测试
make test-campaign-discount campaign_id=<failing_campaign_id>

# 查看测试输出，确认计算逻辑正确
```

#### 常见失败原因

| failure_reason | 含义 | 解决方案 |
|----------------|------|----------|
| `budget_exhausted` | 活动预算用尽 | 追加预算或终止活动 |
| `user_not_eligible` | 用户不符合资格 | 放宽资格规则或修正规则逻辑 |
| `discount_calculation_error` | 折扣计算异常 | 修复计算逻辑bug，重新部署 |
| `duplicate_redemption` | 用户重复兑现 | 正常行为，如频率异常需调查作弊 |
| `campaign_expired` | 活动已过期 | 延长活动时间或下线活动入口 |

#### 应急修复

```bash
# 1. 临时提高预算（需审批）
mysql -e "
UPDATE promotion_campaigns 
SET total_budget_credits = total_budget_credits + 100000 
WHERE campaign_id = '<failing_campaign_id>';
"

# 2. 暂停活动（止损）
mysql -e "
UPDATE promotion_campaigns 
SET status = 'paused' 
WHERE campaign_id = '<failing_campaign_id>';
"

# 3. 手动补偿失败用户
./scripts/campaign-compensation.sh --campaign-id=<failing_campaign_id> --failed-users-csv=failed_users.csv
```

---

### FR-005: 定价版本冲突

**症状：** 告警 `CurrentPriceViolation`，同一模型多个现行价  
**影响：** 计费不确定性，可能造成财务纠纷

#### 排查步骤

**步骤1: 确认冲突范围**
```sql
SELECT 
    model_id,
    mode,
    COUNT(*) AS current_price_count,
    GROUP_CONCAT(version ORDER BY version) AS conflicting_versions
FROM model_price_versions
WHERE is_current = TRUE
GROUP BY model_id, mode
HAVING COUNT(*) > 1;
```

**步骤2: 检查冲突版本详情**
```sql
SELECT 
    model_id,
    mode,
    version,
    input_price_per_million,
    output_price_per_million,
    valid_from,
    valid_to,
    is_current,
    updated_at
FROM model_price_versions
WHERE model_id = '<conflicting_model>'
AND mode = '<conflicting_mode>'
ORDER BY version DESC;
```

**步骤3: 确认正确的现行价**
- 查看定价团队的变更记录
- 对比外部市场价格
- 确认最新版本号

**步骤4: 修复冲突**
```sql
-- 方案1: 保留最新版本
UPDATE model_price_versions
SET is_current = FALSE
WHERE model_id = '<conflicting_model>'
AND mode = '<conflicting_mode>'
AND version < (SELECT MAX(version) FROM model_price_versions WHERE model_id = '<conflicting_model>' AND mode = '<conflicting_mode>');

-- 方案2: 保留指定版本
UPDATE model_price_versions SET is_current = FALSE WHERE model_id = '<conflicting_model>' AND mode = '<conflicting_mode>';
UPDATE model_price_versions SET is_current = TRUE WHERE model_id = '<conflicting_model>' AND mode = '<conflicting_mode>' AND version = <correct_version>;
```

#### 预防措施

- 使用 PUT `/api/admin/maas/model-price-mode` API（内置唯一性保护）
- 避免直接SQL操作`is_current`字段
- 每次变更后运行契约测试：`make test-contract-pricing`

---

## 4. 紧急响应预案

### EP-001: 统计系统完全失效

**触发条件：** gap率 > 50% 或 event_inbox 积压 > 100万行

#### 响应流程

1. **立即行动（5分钟内）**
```bash
# 1. 停止写入event_inbox（启用熔断）
curl -X POST http://localhost:8080/api/internal/circuit-breaker/open?component=event_inbox

# 2. 通知on-call工程师和CTO
./scripts/alert-oncall.sh --severity=P0 --message="Stats system outage"

# 3. 切换到备份统计方案（如有）
mysql -e "UPDATE system_config SET stats_mode = 'backup_aggregator' WHERE key = 'stats_strategy';"
```

2. **根因分析（30分钟内）**
- 检查数据库磁盘空间：`df -h /var/lib/mysql`
- 检查慢查询日志：`pt-query-digest /var/log/mysql/slow.log`
- 检查分区表状态：执行 SOP-007
- 检查消费者进程：`ps aux | grep event-consumer`

3. **恢复方案**
   - 如磁盘满：清理历史数据，扩容磁盘
   - 如分区表失效：手动创建分区
   - 如消费者崩溃：重启消费者，扩容实例
   - 如数据库锁死：Kill长事务，优化索引

4. **恢复验证**
```bash
# 重新开启写入
curl -X POST http://localhost:8080/api/internal/circuit-breaker/close?component=event_inbox

# 观察gap收敛
watch -n 10 'curl -s http://localhost:9090/api/v1/query?query=stats_reconciliation_gap_percent | jq'
```

5. **事后复盘（24小时内）**
   - 填写《P0事件报告》模板
   - 更新 runbook 和监控阈值
   - 制定长期优化方案

---

### EP-002: 行情同步红线触发

**触发条件：** 告警 `MarketPriceSyncRedLineViolation` 触发

#### 响应流程

1. **立即行动（立刻）**
```bash
# 1. 紧急停止market-sync cron任务
crontab -l > /tmp/crontab.backup
crontab -l | grep -v market-sync | crontab -

# 2. 回滚最近一次同步（如已写入）
mysql -e "
DELETE FROM routing WHERE updated_at > DATE_SUB(NOW(), INTERVAL 10 MINUTE);
DELETE FROM billing_rules WHERE updated_at > DATE_SUB(NOW(), INTERVAL 10 MINUTE);
"

# 3. 拉取最新代码，确认红线保护生效
cd /opt/llm-gateway && git pull && make test-contract-market-sync
```

2. **影响评估**
```sql
-- 检查是否有数据污染
SELECT COUNT(*) AS contaminated_rows
FROM routing
WHERE updated_at > DATE_SUB(NOW(), INTERVAL 10 MINUTE)
AND updated_by = 'market-sync';

SELECT COUNT(*) AS contaminated_rows
FROM billing_rules
WHERE updated_at > DATE_SUB(NOW(), INTERVAL 10 MINUTE)
AND updated_by = 'market-sync';
```

3. **根因分析**
   - 检查代码变更：`git log --since="1 week ago" -- scripts/market-sync.sh`
   - 检查是否绕过契约测试
   - 检查是否有人手动修改SQL

4. **恢复路径**
   - 如无污染数据：修复代码，重新部署，恢复cron
   - 如有污染数据：从备份恢复，或手动清理污染行

#### ⚠️ 关键注意

- **绝对红线：行情不回流路由/计费**
- 任何违反此红线的代码变更需经过架构评审
- 契约测试失败禁止合并代码

---

### EP-003: 大规模凭据失效

**触发条件：** 可用凭据数 < 总凭据数的 20%

#### 响应流程

1. **立即行动（5分钟内）**
```bash
# 1. 查看凭据健康状况
mysql -e "
SELECT status, COUNT(*) FROM credential_health GROUP BY status;
"

# 2. 如是供应商侧大规模故障，启用降级策略
curl -X POST http://localhost:8080/api/internal/degradation/enable \
  -d '{"strategy": "fallback_to_secondary_providers"}'

# 3. 通知客户团队准备公告
./scripts/notify-customer-support.sh --incident=credential-outage
```

2. **供应商沟通**
   - 检查供应商状态页：https://status.openai.com
   - 联系供应商技术支持
   - 获取ETA恢复时间

3. **临时缓解**
```bash
# 启用所有可用的fallback凭据
mysql -e "
UPDATE credentials 
SET status = 'active' 
WHERE provider IN ('anthropic', 'google') 
AND status = 'standby';
"

# 降低探测频率，减少供应商压力
mysql -e "
UPDATE system_config 
SET value = '3600' 
WHERE key = 'credential_probe_interval_seconds';
"
```

4. **客户通知**
   - 发布服务状态公告
   - 说明影响范围和ETA
   - 提供workaround方案（如切换模型）

---

## 5. 日常运维检查清单

### 每日检查（10:00 AM）

- [ ] 执行 SOP-001 每日健康检查
- [ ] 查看Grafana运营仪表盘，确认关键指标正常
- [ ] 查看Alertmanager，确认无活跃P0/P1告警
- [ ] 检查211服务器磁盘使用率 < 80%
- [ ] 检查数据库连接池使用率 < 70%
- [ ] 填写《运营系统日报》

### 每周检查（周一 10:00 AM）

- [ ] 执行 SOP-004 探测恢复系统维护
- [ ] 查看过去7天告警统计，分析趋势
- [ ] 检查数据库备份完整性（最近一次成功备份时间）
- [ ] 检查分区表未来7天分区创建情况
- [ ] 审查运维日志，标记重复问题
- [ ] 更新 runbook（如有新故障类型）

### 每月检查（每月1日 10:00 AM）

- [ ] 执行全量契约测试：`make test-contract-all`
- [ ] 生成月度运营报告（gap率、同步成功率、活动ROI）
- [ ] 审查所有营销活动预算使用情况
- [ ] 清理30天前的历史数据（probe_history, event_inbox_old）
- [ ] 评估监控告警规则准确性（误报率、漏报率）
- [ ] 与定价团队对账：定价变更记录 vs 实际生效版本
- [ ] 与财务团队对账：统计数据 vs 财务账单

---

## 6. 关键指标解读

### 6.1 统计对账指标

| 指标 | 含义 | 正常范围 | 异常阈值 | 处理动作 |
|------|------|----------|----------|----------|
| `stats_reconciliation_gap_percent` | 统计对账差异率 | < 0.5% | > 2% | 执行 FR-001 |
| `stats_minute_rollup_lag_seconds` | 重扫臂延迟 | < 60s | > 300s | 重启重扫臂 |
| `request_stats_cost_usd_zero_ratio` | 成本链全0占比 | < 1% | > 10% | 检查pricing服务 |
| `event_inbox_pending_count` | 未处理事件数 | < 1000 | > 10000 | 扩容消费者 |

**解读示例：**
```
stats_reconciliation_gap_percent = 1.8%
✅ 正常：低于2%阈值，重扫臂正常工作

stats_reconciliation_gap_percent = 3.5%（持续15分钟）
🚨 异常：触发告警，需执行 FR-001 排查
```

### 6.2 行情同步指标

| 指标 | 含义 | 正常范围 | 异常阈值 | 处理动作 |
|------|------|----------|----------|----------|
| `market_price_sync_lag_hours` | 同步滞后时间 | < 1h | > 24h | 执行 FR-002 |
| `market_price_sync_rejected_rows_24h` | 24h拒绝行数 | < 10 | > 100 | 检查数据格式 |
| `market_price_fx_rate_age_hours` | 汇率更新滞后 | < 24h | > 48h | 手动触发汇率更新 |

### 6.3 探测恢复指标

| 指标 | 含义 | 正常范围 | 异常阈值 | 处理动作 |
|------|------|----------|----------|----------|
| `credential_suspended_count` | 挂起凭据数 | 0-2 | > 5 | 执行 SOP-004 |
| `credential_suspended_duration_hours` | 最长挂起时长 | < 12h | > 24h | 执行 FR-003 |
| `probe_fallback_all_failed_rate` | 回退梯全失败率 | < 5% | > 20% | 更新回退梯配置 |

### 6.4 营销活动指标

| 指标 | 含义 | 正常范围 | 异常阈值 | 处理动作 |
|------|------|----------|----------|----------|
| `campaign_redemption_success_rate` | 兑现成功率 | > 98% | < 95% | 执行 FR-004 |
| `campaign_budget_utilization` | 预算使用率 | 0-80% | > 95% | 评估追加预算 |
| `campaign_discount_error_count` | 折扣计算错误数 | 0 | > 0 | 立即修复逻辑 |

---

## 7. 红线守则

### 7.1 数据隔离红线

**原则：行情不回流路由/计费**

| ✅ 允许 | ❌ 禁止 |
|--------|--------|
| market-sync → `market_price_reference` | market-sync → `routing` |
| market-sync → `fx_rates` | market-sync → `billing_rules` |
| pricing服务读取`market_price_reference` | 任何服务直接读`market_price_reference`用于路由决策 |

**验证方式：**
- 每次代码变更运行：`make test-contract-market-sync`
- CI/CD pipeline强制检查

### 7.2 NULL语义红线

**原则：NULL ≠ 0, NULL = 未知**

| 场景 | ✅ 正确处理 | ❌ 错误处理 |
|------|------------|------------|
| `cost_usd` = NULL | 标记为"成本未计算"，触发告警 | 当作0处理，导致免费 |
| `balance` = NULL | 拒绝路由决策，要求明确值 | 当作0或无限大 |
| `discount_rate` = NULL | 不应用折扣 | 当作0%折扣 |

**验证方式：**
- 运行：`make test-null-semantics`
- 每月审查NULL字段分布

### 7.3 定价唯一性红线

**原则：每个(model, mode)同一时刻仅有1个is_current=TRUE**

**检查SQL：**
```sql
-- 应返回0行
SELECT model_id, mode, COUNT(*) AS current_count
FROM model_price_versions
WHERE is_current = TRUE
GROUP BY model_id, mode
HAVING current_count > 1;
```

**违规处理：**
- 立即触发 EP-002 紧急响应
- 回滚到最近一次正确状态
- 禁止直接SQL修改，必须通过API

---

## 8. 变更管理

### 8.1 变更分类

| 变更类型 | 审批级别 | 测试要求 | 回滚方案 |
|----------|----------|----------|----------|
| **低风险** (配置调整、文档更新) | 运维工程师 | 本地验证 | 立即回滚 |
| **中风险** (代码部署、数据库DDL) | Tech Lead | 完整契约测试 | 灰度回滚 |
| **高风险** (架构变更、红线相关) | CTO | 全量测试+预演 | 备份恢复 |

### 8.2 变更流程

1. **提交变更申请**（Jira ticket）
2. **代码评审**（至少1名reviewer）
3. **契约测试通过**（`make test-contract-all`）
4. **预发布环境验证**（staging）
5. **变更窗口执行**（周二/周四 14:00-16:00）
6. **生产验证**（执行对应SOP验证步骤）
7. **变更记录归档**（更新 changelog）

### 8.3 紧急变更

**适用场景：** P0生产故障修复

**快速通道：**
- 口头审批（on-call + Tech Lead）
- 最小化测试（仅核心路径）
- 立即部署
- 24小时内补充完整测试和文档

---

## 附录

### A. 常用命令速查

```bash
# 查看系统状态
systemctl status llm-gateway

# 查看实时日志
tail -f /var/log/llm-gateway/app.log

# 查看告警
curl -s http://alertmanager:9093/api/v2/alerts | jq '.[] | select(.status.state=="active")'

# 触发契约测试
make test-contract-all

# 手动触发市场同步
/opt/llm-gateway/scripts/market-sync.sh

# 手动触发统计重扫
/opt/llm-gateway/scripts/stats-resweep.sh --minute-start="2024-01-15 14:00"

# 查看数据库连接
mysql -e "SHOW PROCESSLIST;"

# 查看分区表状态
mysql -e "SELECT PARTITION_NAME, TABLE_ROWS FROM INFORMATION_SCHEMA.PARTITIONS WHERE TABLE_NAME='event_inbox';"
```

### B. 联系方式

| 团队 | 职责 | 联系方式 |
|------|------|----------|
| **运维On-call** | 7x24紧急响应 | ops-oncall@company.com, PagerDuty |
| **数据平台团队** | 统计对账问题 | data-platform@company.com |
| **定价团队** | 行情同步、定价版本 | pricing@company.com |
| **基础设施团队** | 探测恢复、凭据管理 | infrastructure@company.com |
| **运营团队** | 营销活动问题 | operations@company.com |

### C. 文档索引

- [运营体系方案总结](../运营体系方案总结.md)
- [运营体系审计报告](../audit/运营体系审计报告.md)
- [监控告警规则](monitoring/alerts.yml)
- [Grafana仪表盘](monitoring/dashboards/)
- [契约测试套件](../../test/contract/)

---

**文档版本：** v1.0  
**最后审查：** 2024-01-XX  
**下次审查：** 2024-04-XX
