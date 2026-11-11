# Runbook: 市场行情同步失败

**告警名称:** `MarketPriceSyncFailed`  
**严重级别:** P1 Critical  
**影响范围:** 定价准确性、成本管理  
**平均恢复时间 (MTTR):** 1小时

---

## 1. 症状识别

### 主要症状
- Prometheus告警: `MarketPriceSyncFailed` 触发
- 上次成功同步时间 > 24小时
- Cron日志显示同步脚本失败
- 定价团队报告行情数据陈旧

### 关联指标
```promql
# 同步滞后时间(秒)
(time() - market_price_sync_last_success_timestamp) > 86400

# 拒绝行数
rate(market_price_reference_sync_rejected_rows[24h]) > 100

# 红线违规(最严重)
market_price_sync_redline_violation_total > 0
```

---

## 2. 即时响应 (0-5分钟)

### 步骤1: 确认同步状态

```bash
# SSH登录211服务器
ssh ops@prod-211.internal

# 查看最后同步时间
mysql llm_gateway -e "
SELECT 
    MAX(sync_timestamp) AS last_sync,
    TIMESTAMPDIFF(HOUR, MAX(sync_timestamp), NOW()) AS hours_ago
FROM market_price_reference_sync;
"
```

**预期输出:**
```
+---------------------+-----------+
| last_sync           | hours_ago |
+---------------------+-----------+
| 2024-01-14 03:00:15 |        28 |
+---------------------+-----------+
```

**判断:**
- hours_ago > 48: 🚨 严重滞后，立即修复
- hours_ago 24-48: ⚠️ 警告，尽快修复
- hours_ago < 24: ✅ 误报，检查告警规则

### 步骤2: 检查同步日志

```bash
# 查看最近的错误日志
tail -n 100 /var/log/cron/market-sync.log | grep -i -E "error|failed|fatal"

# 示例错误输出:
# [ERROR] Failed to connect to market.external.com: Connection timed out
# [ERROR] API returned 429 Too Many Requests
# [ERROR] Database write failed: Duplicate entry for key 'PRIMARY'
```

### 步骤3: 红线检查 (最高优先级)

```bash
# ⚠️ 如日志出现以下关键词，立即执行紧急响应EP-002
grep -i "routing\|billing_rules" /var/log/cron/market-sync.log | tail -20

# 检查红线违规计数器
curl -s 'http://localhost:9090/api/v1/query?query=market_price_sync_redline_violation_total' | jq -r '.data.result[0].value[1]'

# 如返回 > 0，立即:
# 1. 停止market-sync cron
# 2. 通知架构团队
# 3. 不要继续本runbook，执行紧急响应
```

---

## 3. 诊断分析 (5-20分钟)

### 诊断路径A: 外部API可达性

```bash
# 测试API连通性
curl -I https://market.external.com/api/v1/prices
# 预期: HTTP/2 200

# 测试DNS解析
nslookup market.external.com
# 预期: 返回IP地址

# 测试网络路由
traceroute market.external.com
# 预期: 能够到达目标主机

# 测试API认证
curl -H "Authorization: Bearer ${MARKET_API_KEY}" \
     https://market.external.com/api/v1/auth/validate
# 预期: {"status": "valid"}
```

**异常情况:**
- HTTP 429: API限流 → 执行修复A1
- HTTP 401/403: 认证失败 → 执行修复A2
- Connection timeout: 网络故障 → 执行修复A3
- DNS解析失败: DNS问题 → 执行修复A4

### 诊断路径B: 数据格式兼容性

```bash
# 获取最新API响应示例
curl -H "Authorization: Bearer ${MARKET_API_KEY}" \
     https://market.external.com/api/v1/prices?limit=5 \
     > /tmp/api_response_sample.json

# 检查JSON格式
jq '.' /tmp/api_response_sample.json
# 如解析失败，说明格式变更

# 对比历史格式
diff <(jq -S '.[0] | keys' /tmp/api_response_sample.json) \
     <(jq -S '.[0] | keys' /opt/llm-gateway/test/fixtures/market_api_response_v1.json)
# 如有差异，说明字段变更
```

**异常情况:**
- JSON解析失败 → 执行修复B1
- 字段缺失/新增 → 执行修复B2

### 诊断路径C: 数据库写入问题

```bash
# 检查数据库连接
mysql -h db.production.internal -u market_sync_user -p -e "SELECT 1;"
# 预期: 返回1

# 检查表空间
mysql llm_gateway -e "
SELECT 
    table_name,
    ROUND((data_length + index_length) / 1024 / 1024, 2) AS size_mb
FROM information_schema.tables
WHERE table_schema = 'llm_gateway'
AND table_name LIKE 'market_price%';
"

# 检查最近写入错误
mysql llm_gateway -e "
SELECT 
    error_type,
    error_message,
    COUNT(*) AS error_count
FROM market_price_reference_sync
WHERE sync_status = 'failed'
AND sync_timestamp > DATE_SUB(NOW(), INTERVAL 7 DAY)
GROUP BY error_type, error_message
ORDER BY error_count DESC;
"
```

**异常情况:**
- Duplicate key error → 执行修复C1
- Table is full → 执行修复C2
- Lock wait timeout → 执行修复C3

### 诊断路径D: Cron任务配置

```bash
# 检查cron配置
crontab -l | grep market-sync
# 预期: 30 2 * * * /opt/llm-gateway/scripts/market-sync.sh

# 检查脚本权限
ls -l /opt/llm-gateway/scripts/market-sync.sh
# 预期: -rwxr-xr-x

# 检查环境变量
grep MARKET_API_KEY /opt/llm-gateway/.env
# 预期: MARKET_API_KEY=sk-xxxxxx

# 检查cron服务状态
systemctl status cron
# 预期: active (running)
```

---

## 4. 修复方案

### 修复A1: 应对API限流

**适用场景:** HTTP 429 Too Many Requests

```bash
# 1. 检查当前请求频率
grep "market.external.com" /var/log/cron/market-sync.log | \
  awk '{print $1, $2}' | uniq -c | tail -20

# 2. 临时降低请求频率（修改脚本）
cd /opt/llm-gateway
cp scripts/market-sync.sh scripts/market-sync.sh.backup

# 在请求之间添加延迟
sed -i 's/curl/sleep 2; curl/g' scripts/market-sync.sh

# 3. 手动触发同步
./scripts/market-sync.sh --rate-limit=0.5  # 0.5 req/sec
```

**长期方案:**
```bash
# 联系供应商申请提额
echo "Request rate limit increase for API key: ${MARKET_API_KEY}" | \
  mail -s "API Rate Limit Request" api-support@external.com
```

### 修复A2: 更新API凭据

**适用场景:** HTTP 401/403 Unauthorized

```bash
# 1. 确认当前API key
echo "Current MARKET_API_KEY: ${MARKET_API_KEY:0:10}..."

# 2. 从密钥管理系统获取新key
# (假设使用HashiCorp Vault)
vault kv get -field=api_key secret/market-sync/credentials

# 3. 更新环境变量
NEW_KEY=$(vault kv get -field=api_key secret/market-sync/credentials)
sed -i "s/MARKET_API_KEY=.*/MARKET_API_KEY=${NEW_KEY}/" /opt/llm-gateway/.env

# 4. 测试新key
curl -H "Authorization: Bearer ${NEW_KEY}" \
     https://market.external.com/api/v1/auth/validate

# 5. 重新触发同步
./scripts/market-sync.sh
```

### 修复A3: 解决网络故障

**适用场景:** Connection timeout

```bash
# 1. 检查防火墙规则
sudo iptables -L OUTPUT -v -n | grep 443

# 2. 检查代理配置
echo $HTTP_PROXY
echo $HTTPS_PROXY

# 3. 测试备用网络路径
# 通过跳板机访问
ssh jumpbox.internal "curl -I https://market.external.com/api/v1/prices"

# 4. 如网络确实不通，联系网络团队
echo "Network connectivity issue to market.external.com" | \
  mail -s "[P1] Network Issue" network-team@company.com

# 5. 临时方案: 使用备份数据源
./scripts/market-sync.sh --source=backup
```

### 修复A4: 修复DNS问题

**适用场景:** DNS解析失败

```bash
# 1. 检查/etc/resolv.conf
cat /etc/resolv.conf

# 2. 尝试使用备用DNS
echo "nameserver 8.8.8.8" | sudo tee -a /etc/resolv.conf

# 3. 清除DNS缓存
sudo systemd-resolve --flush-caches

# 4. 验证解析
nslookup market.external.com 8.8.8.8

# 5. 临时使用IP地址
# 编辑脚本，将域名替换为IP
MARKET_IP=$(nslookup market.external.com 8.8.8.8 | grep Address | tail -1 | awk '{print $2}')
sed -i "s/market.external.com/${MARKET_IP}/g" scripts/market-sync.sh
```

### 修复B1: 处理JSON格式变更

**适用场景:** JSON解析失败

```bash
# 1. 分析新格式
jq '.' /tmp/api_response_sample.json | head -50

# 2. 更新解析逻辑
cd /opt/llm-gateway
git checkout -b hotfix/market-api-format-v2

# 编辑parser (Python示例)
cat > scripts/market_price_parser_v2.py << 'EOF'
import json

def parse_response(response_json):
    # 兼容新旧格式
    if 'data' in response_json:  # 新格式
        prices = response_json['data']['prices']
    else:  # 旧格式
        prices = response_json['prices']
    
    return [
        {
            'model': p['model_name'],
            'input_price': p.get('input_price_per_1m', p.get('input_price')),  # 字段名变更兼容
            'output_price': p.get('output_price_per_1m', p.get('output_price'))
        }
        for p in prices
    ]
EOF

# 3. 测试新parser
python3 scripts/market_price_parser_v2.py < /tmp/api_response_sample.json

# 4. 更新market-sync.sh使用新parser
sed -i 's/market_price_parser.py/market_price_parser_v2.py/g' scripts/market-sync.sh

# 5. 执行同步
./scripts/market-sync.sh --dry-run  # 先dry-run
./scripts/market-sync.sh             # 正式执行
```

### 修复B2: 处理字段缺失

**适用场景:** 必需字段缺失

```bash
# 1. 确认缺失字段
diff <(jq -S '.[0] | keys' /tmp/api_response_sample.json) \
     <(jq -S '.[0] | keys' test/fixtures/expected_fields.json)

# 2. 为缺失字段提供默认值
cat > scripts/add_default_values.sh << 'EOF'
#!/bin/bash
# 为缺失字段添加默认值
jq 'map(. + {
  "currency": (.currency // "USD"),
  "updated_at": (.updated_at // now)
})' "$1"
EOF

chmod +x scripts/add_default_values.sh

# 3. 在pipeline中插入默认值填充
# 修改market-sync.sh
sed -i '/curl.*api\/v1\/prices/a | ./scripts/add_default_values.sh -' scripts/market-sync.sh

# 4. 执行同步
./scripts/market-sync.sh
```

### 修复C1: 解决主键冲突

**适用场景:** Duplicate key error

```bash
# 1. 查找冲突记录
mysql llm_gateway -e "
SELECT 
    canonical_name,
    dimension,
    COUNT(*) AS dup_count
FROM market_price_reference
GROUP BY canonical_name, dimension
HAVING dup_count > 1;
"

# 2. 分析冲突原因
mysql llm_gateway -e "
SELECT * FROM market_price_reference
WHERE canonical_name = '<conflicting_name>'
AND dimension = '<conflicting_dimension>';
"

# 3. 删除重复记录（保留最新）
mysql llm_gateway -e "
DELETE t1 FROM market_price_reference t1
INNER JOIN market_price_reference t2
WHERE 
    t1.canonical_name = t2.canonical_name
    AND t1.dimension = t2.dimension
    AND t1.updated_at < t2.updated_at;
"

# 4. 重新执行同步
./scripts/market-sync.sh
```

### 修复C2: 扩展表空间

**适用场景:** Table is full

```bash
# 1. 检查磁盘空间
df -h /var/lib/mysql

# 2. 清理历史数据
mysql llm_gateway -e "
DELETE FROM market_price_reference_sync
WHERE sync_timestamp < DATE_SUB(NOW(), INTERVAL 90 DAY)
LIMIT 10000;
"

# 3. 优化表
mysql llm_gateway -e "
OPTIMIZE TABLE market_price_reference;
OPTIMIZE TABLE market_price_reference_sync;
"

# 4. 如仍不足，扩容磁盘
sudo lvextend -L +20G /dev/mapper/vg_data-lv_mysql
sudo resize2fs /dev/mapper/vg_data-lv_mysql
```

### 修复C3: 解决锁超时

**适用场景:** Lock wait timeout

```bash
# 1. 查找持锁事务
mysql llm_gateway -e "
SELECT 
    r.trx_id,
    r.trx_mysql_thread_id,
    r.trx_query,
    TIMESTAMPDIFF(SECOND, r.trx_started, NOW()) AS trx_duration_sec
FROM information_schema.innodb_lock_waits w
JOIN information_schema.innodb_trx r ON r.trx_id = w.blocking_trx_id;
"

# 2. Kill长事务
mysql llm_gateway -e "KILL <thread_id>;"

# 3. 优化同步事务大小
# 将大事务拆分为小批次
sed -i 's/BEGIN/BEGIN; SET SESSION innodb_lock_wait_timeout=10;/g' scripts/market-sync.sh

# 4. 重新执行
./scripts/market-sync.sh
```

---

## 5. 验证恢复 (20-60分钟)

### 验证检查点

**检查1: 同步成功执行**
```bash
# 查看最新同步记录
mysql llm_gateway -e "
SELECT 
    sync_timestamp,
    rows_inserted,
    rows_updated,
    rows_rejected,
    sync_status
FROM market_price_reference_sync
ORDER BY sync_timestamp DESC
LIMIT 5;
"
```

**预期输出:**
```
+---------------------+---------------+--------------+---------------+-------------+
| sync_timestamp      | rows_inserted | rows_updated | rows_rejected | sync_status |
+---------------------+---------------+--------------+---------------+-------------+
| 2024-01-15 15:30:00 |           234 |          156 |             2 | success     |
+---------------------+---------------+--------------+---------------+-------------+
```

**检查2: 数据质量验证**
```bash
# 验证数据完整性
mysql llm_gateway -e "
SELECT 
    COUNT(*) AS total_models,
    COUNT(DISTINCT canonical_name) AS unique_models,
    SUM(CASE WHEN vendor_list_in_per_1m IS NULL THEN 1 ELSE 0 END) AS missing_vendor_price,
    SUM(CASE WHEN market_min_in_per_1m IS NULL THEN 1 ELSE 0 END) AS missing_market_price
FROM market_price_reference;
"
```

**检查3: 契约测试**
```bash
# 执行红线契约测试
cd /opt/llm-gateway
make test-contract-market-sync

# 预期输出:
# ✅ Test 1/11: Write target whitelist check ... PASSED
# ✅ Test 2/11: NULL semantics check ... PASSED
# ...
# ✅ All 11 tests PASSED
```

**检查4: 下游服务验证**
```bash
# 测试定价API
curl -H "Authorization: Bearer ${ADMIN_TOKEN}" \
     http://localhost:8080/api/admin/maas/market-price-overview | jq '.'

# 预期: 返回最新行情数据

# 检查定价页
curl http://localhost:8080/pricing | grep "last_updated"
# 预期: 显示最新更新时间
```

---

## 6. 恢复后监控

### 第1小时: 密集监控

```bash
# 每10分钟检查同步延迟
for i in {1..6}; do
    echo "=== Check $i/6 at $(date) ==="
    curl -s 'http://localhost:9090/api/v1/query?query=(time()-market_price_sync_last_success_timestamp)/3600' | \
      jq -r '.data.result[0].value[1] | tonumber | "Sync lag: \(.) hours"'
    sleep 600
done
```

### 第24小时: 持续观察

- [ ] 确认第二天cron自动执行成功
- [ ] 查看拒绝行数是否正常（< 10/天）
- [ ] 对比修复前后的数据质量

### 第7天: 稳定性评估

- [ ] 统计7天同步成功率（应 > 95%）
- [ ] 分析是否有新的失败模式
- [ ] 决定是否需要架构优化

---

## 7. 根因分析

### 根因分类统计 (历史数据)

| 根因类别 | 占比 | MTTR | 预防措施 |
|----------|------|------|----------|
| API限流/认证 | 40% | 30min | 增加重试逻辑，监控key有效期 |
| 格式变更 | 25% | 2h | 版本协商，向后兼容parser |
| 网络故障 | 15% | 1h | 多路径冗余，备用数据源 |
| 数据库问题 | 12% | 45min | 容量规划，性能优化 |
| Cron配置 | 8% | 15min | 配置管理自动化 |

### 深度分析模板

```markdown
## 事件时间线
- 14:50: 告警触发
- 15:00: 开始排查
- 15:15: 确认根因为API限流
- 15:30: 应用临时修复(降低请求频率)
- 15:45: 同步成功，告警解除
- 16:00: 发起API提额申请

## 根因
外部API供应商调整了速率限制，从10 req/s降至5 req/s，未提前通知。

## 影响
- 行情数据滞后28小时
- 影响233个模型的定价参考
- 无客户直接影响(计费仍使用内部价格)

## 改进措施
- 短期: 降低请求频率至4 req/s，留20%余量
- 中期: 实现自适应限流(根据429响应动态调整)
- 长期: 与供应商建立SLA，要求提前通知变更
```

---

## 8. 后续行动

### 立即行动 (24小时内)

- [ ] 更新事件报告
- [ ] 如修改了脚本，提交PR并code review
- [ ] 更新监控告警阈值(如需要)
- [ ] 通知定价团队恢复情况

### 短期改进 (1周内)

- [ ] 实现自动重试机制
- [ ] 增加格式兼容性测试
- [ ] 配置备用数据源
- [ ] 文档化新的排查技巧

### 长期优化 (1月内)

- [ ] 评估实时同步方案(vs 每日批量)
- [ ] 建立供应商沟通渠道(SLA)
- [ ] 实现数据质量自动验证
- [ ] 增加同步dashboard可观测性

---

## 9. 参考资料

- [运营体系运维手册](../OPERATIONS_MANUAL.md#22-sop-002-市场行情同步执行)
- [市场行情同步架构](../../architecture/market-price-sync.md)
- [红线契约测试](../../tests/contract/market_price_sync_contract_test.sh)
- [Grafana仪表盘](http://grafana.internal/d/market-price-sync)
- [外部API文档](https://market.external.com/docs/api)

---

**Runbook版本:** v1.0  
**最后更新:** 2024-01-XX  
**维护团队:** Pricing Team
