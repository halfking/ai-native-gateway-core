# Bandit Scoring 部署操作手册

## 📋 目录

1. [部署前准备](#部署前准备)
2. [部署步骤](#部署步骤)
3. [验证检查](#验证检查)
4. [回滚方案](#回滚方案)
5. [监控指标](#监控指标)
6. [故障排查](#故障排查)

---

## 部署前准备

### 环境要求

- ✅ PostgreSQL 数据库（credentials 表）
- ✅ Go 1.21+
- ✅ 数据库备份（强烈推荐）

### 预检查清单

```bash
# 1. 检查当前数据库版本
psql -U kxuser -d llm_gateway -c "SELECT version();"

# 2. 检查 credentials 表是否存在
psql -U kxuser -d llm_gateway -c "\d credentials" | head -5

# 3. 备份生产数据库
pg_dump -U kxuser -d llm_gateway -t credentials > credentials_backup_$(date +%Y%m%d_%H%M%S).sql

# 4. 检查磁盘空间
df -h | grep postgres

# 5. 验证编译
cd services/llm-gateway-go
go build ./cmd/gateway/...
```

---

## 部署步骤

### Step 1: 运行 Migration（约 10-30 秒）

```bash
# 在测试环境先运行（强烈推荐）
psql -U kxuser -h test-db.example.com -d llm_gateway_test < deploy/sql/migrations/033_bandit_scoring.sql

# 生产环境运行
psql -U kxuser -h prod-db.example.com -d llm_gateway < deploy/sql/migrations/033_bandit_scoring.sql
```

**预期输出：**
```
ALTER TABLE
ALTER TABLE
ALTER TABLE
...
CREATE INDEX
COMMENT
```

**验证 Migration：**
```bash
# 检查新字段是否创建
psql -U kxuser -d llm_gateway -c "\d credentials" | grep bandit

# 应该看到：
# bandit_alpha              | real                        | default 1.0
# bandit_beta               | real                        | default 1.0
# bandit_success_count      | bigint                      | default 0
# bandit_failure_count      | bigint                      | default 0
# bandit_429_count          | integer                     | default 0
# bandit_total_latency_ms   | bigint                      | default 0
# bandit_avg_latency_ms     | bigint                      | default 0
# penalty_429_accumulated   | real                        | default 0.0
# penalty_429_last_at       | timestamp with time zone    |
# intelligence_rank         | integer                     | default 50
```

---

### Step 2: 金丝雀部署（第1天）

#### 2.1 选择金丝雀实例

```bash
# 在单个实例上启用（约占 5-10% 流量）
# 例如：gateway-1 
ssh gateway-1.prod.example.com
```

#### 2.2 配置环境变量

**方式 1: 环境变量**
```bash
# 编辑 systemd service 文件
sudo vim /etc/systemd/system/llm-gateway.service

# 添加：
Environment="LLM_GATEWAY_ENABLE_BANDIT_SCORING=true"

# 重启服务
sudo systemctl daemon-reload
sudo systemctl restart llm-gateway
```

**方式 2: config.yml**
```yaml
# /etc/llm-gateway/config.yml
enable_bandit_scoring: true
```

#### 2.3 验证启动

```bash
# 查看启动日志
sudo journalctl -u llm-gateway -f

# 预期日志：
# bandit: loaded 156 credential scores from database
# bandit_scoring enabled=true flush_interval=10s batch_size=100
```

---

### Step 3: 监控观察（第2-3天）

#### 3.1 监控指标

```bash
# 查看 Bandit 更新频率
psql -U kxuser -d llm_gateway -c "
SELECT 
    COUNT(*) as total_credentials,
    COUNT(CASE WHEN last_scored_at > NOW() - INTERVAL '1 hour' THEN 1 END) as recently_updated,
    MAX(last_scored_at) as last_update
FROM credentials
WHERE status <> 'disabled';
"

# 预期结果（运行1小时后）：
#  total_credentials | recently_updated | last_update
# -------------------+------------------+------------------------
#  156               | 45               | 2026-06-26 15:23:41+00
```

#### 3.2 检查错误率

```bash
# 查看应用日志
sudo journalctl -u llm-gateway --since "1 hour ago" | grep -i "error\|fail" | wc -l

# 对比启用前后的错误率（应该持平或降低）
```

#### 3.3 检查数据库写入

```bash
# 查看 bandit 字段的更新统计
psql -U kxuser -d llm_gateway -c "
SELECT 
    PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY bandit_success_count) as median_success,
    PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY bandit_failure_count) as median_failure,
    AVG(bandit_avg_latency_ms) as avg_latency
FROM credentials
WHERE last_scored_at IS NOT NULL;
"
```

---

### Step 4: 扩展部署（第4-7天）

如果金丝雀观察期无问题：

```bash
# 扩展到 50% 实例
for host in gateway-{2,3,4,5}.prod.example.com; do
    ssh $host "echo 'export LLM_GATEWAY_ENABLE_BANDIT_SCORING=true' >> ~/.bashrc"
    ssh $host "sudo systemctl restart llm-gateway"
done

# 验证每个实例
for host in gateway-{2,3,4,5}.prod.example.com; do
    echo "=== $host ==="
    ssh $host "sudo journalctl -u llm-gateway --since '1 minute ago' | grep bandit_scoring"
done
```

---

### Step 5: 全量部署（第7-14天）

观察期结束后，全量启用：

```bash
# 在所有实例上启用
ansible-playbook -i inventory/production playbooks/enable-bandit-scoring.yml
```

**Ansible Playbook 示例：**
```yaml
# playbooks/enable-bandit-scoring.yml
---
- hosts: llm_gateway
  tasks:
    - name: Enable Bandit Scoring
      lineinfile:
        path: /etc/systemd/system/llm-gateway.service
        regexp: '^Environment="LLM_GATEWAY_ENABLE_BANDIT_SCORING='
        line: 'Environment="LLM_GATEWAY_ENABLE_BANDIT_SCORING=true"'
      notify: restart gateway

  handlers:
    - name: restart gateway
      systemd:
        name: llm-gateway
        state: restarted
        daemon_reload: yes
```

---

## 验证检查

### 自动化验证脚本

```bash
#!/bin/bash
# verify-bandit-deployment.sh

set -e

DB_HOST="localhost"
DB_USER="kxuser"
DB_NAME="llm_gateway"

echo "=== Bandit Scoring 部署验证 ==="
echo ""

# 1. 检查 Migration
echo "1. 检查数据库字段..."
FIELD_COUNT=$(psql -h $DB_HOST -U $DB_USER -d $DB_NAME -t -c "
SELECT COUNT(*) FROM information_schema.columns 
WHERE table_name = 'credentials' 
AND column_name IN ('bandit_alpha', 'bandit_beta', 'bandit_success_count');
")

if [ "$FIELD_COUNT" -eq 3 ]; then
    echo "   ✅ Migration 已应用"
else
    echo "   ❌ Migration 未应用或不完整"
    exit 1
fi

# 2. 检查服务日志
echo "2. 检查服务状态..."
if journalctl -u llm-gateway --since "5 minutes ago" | grep -q "bandit_scoring.*enabled=true"; then
    echo "   ✅ Bandit Scoring 已启用"
else
    echo "   ⚠️  Bandit Scoring 未启用或日志未找到"
fi

# 3. 检查数据更新
echo "3. 检查数据库更新..."
UPDATED_COUNT=$(psql -h $DB_HOST -U $DB_USER -d $DB_NAME -t -c "
SELECT COUNT(*) FROM credentials 
WHERE last_scored_at > NOW() - INTERVAL '1 hour';
")

echo "   最近1小时内更新的凭据数: $UPDATED_COUNT"
if [ "$UPDATED_COUNT" -gt 0 ]; then
    echo "   ✅ 数据库正在更新"
else
    echo "   ⚠️  数据库尚未更新（可能需要等待10秒刷新周期）"
fi

# 4. 检查冷启动恢复
echo "4. 检查冷启动恢复..."
if journalctl -u llm-gateway --since "5 minutes ago" | grep -q "bandit: loaded.*credential scores"; then
    LOADED=$(journalctl -u llm-gateway --since "5 minutes ago" | grep "bandit: loaded" | tail -1)
    echo "   ✅ $LOADED"
else
    echo "   ⚠️  未找到加载日志（可能是首次启动）"
fi

echo ""
echo "=== 验证完成 ==="
```

运行验证：
```bash
chmod +x verify-bandit-deployment.sh
./verify-bandit-deployment.sh
```

---

## 回滚方案

### 快速回滚（无需重启，立即生效）

**方式 1: 禁用环境变量**
```bash
# 编辑 service 文件
sudo vim /etc/systemd/system/llm-gateway.service

# 改为：
Environment="LLM_GATEWAY_ENABLE_BANDIT_SCORING=false"

# 重启
sudo systemctl daemon-reload
sudo systemctl restart llm-gateway
```

**方式 2: 配置文件**
```yaml
# /etc/llm-gateway/config.yml
enable_bandit_scoring: false
```

**验证回滚：**
```bash
# 日志应显示：
# bandit_scoring enabled=false (或不显示 bandit 相关日志)
sudo journalctl -u llm-gateway --since "1 minute ago" | grep bandit
```

---

### 完全回滚 Migration（如需删除字段）

**警告：** 这会删除所有历史学习数据！

```sql
-- rollback_033_bandit_scoring.sql
ALTER TABLE credentials 
DROP COLUMN IF EXISTS bandit_alpha,
DROP COLUMN IF EXISTS bandit_beta,
DROP COLUMN IF EXISTS bandit_success_count,
DROP COLUMN IF EXISTS bandit_failure_count,
DROP COLUMN IF EXISTS bandit_429_count,
DROP COLUMN IF EXISTS bandit_total_latency_ms,
DROP COLUMN IF EXISTS bandit_avg_latency_ms,
DROP COLUMN IF EXISTS penalty_429_accumulated,
DROP COLUMN IF EXISTS penalty_429_last_at,
DROP COLUMN IF EXISTS intelligence_rank,
DROP COLUMN IF EXISTS quota_remaining,
DROP COLUMN IF EXISTS quota_reset_at;

DROP INDEX IF EXISTS idx_credentials_bandit_score;
```

运行回滚：
```bash
psql -U kxuser -d llm_gateway < rollback_033_bandit_scoring.sql
```

---

## 监控指标

### 关键指标

| 指标 | 说明 | 目标值 |
|-----|------|--------|
| **请求成功率** | 成功请求 / 总请求 | ≥ 95% |
| **P95 延迟** | 95分位延迟 | ≤ 启用前的 100% |
| **429 错误率** | 429 / 总请求 | ≤ 启用前的 80% |
| **数据库写入频率** | Flush 频率 | 每 10s 一次 |
| **冷启动加载时间** | LoadFromDB 耗时 | < 5s |

### Prometheus Queries（未来 P2）

```promql
# 请求成功率
sum(rate(llm_gateway_requests_total{status="success"}[5m])) 
/ 
sum(rate(llm_gateway_requests_total[5m]))

# P95 延迟
histogram_quantile(0.95, 
  sum(rate(llm_gateway_request_duration_seconds_bucket[5m])) by (le)
)

# 429 错误率
sum(rate(llm_gateway_requests_total{status="429"}[5m])) 
/ 
sum(rate(llm_gateway_requests_total[5m]))
```

---

## 故障排查

### 问题 1: 服务启动失败

**症状：**
```bash
sudo systemctl status llm-gateway
# Active: failed
```

**排查：**
```bash
# 查看详细错误
sudo journalctl -u llm-gateway -n 50

# 常见原因：
# 1. 数据库连接失败
# 2. Migration 未运行
# 3. 配置文件格式错误
```

**解决：**
```bash
# 测试数据库连接
psql -U kxuser -d llm_gateway -c "SELECT 1;"

# 验证 Migration
psql -U kxuser -d llm_gateway -c "\d credentials" | grep bandit_alpha

# 检查配置文件
cat /etc/llm-gateway/config.yml | grep enable_bandit
```

---

### 问题 2: Bandit 未启用

**症状：**
日志中无 "bandit_scoring enabled=true"

**排查：**
```bash
# 检查环境变量
sudo systemctl show llm-gateway | grep Environment

# 检查配置文件
cat /etc/llm-gateway/config.yml | grep enable_bandit_scoring
```

**解决：**
```bash
# 重新设置环境变量
sudo vim /etc/systemd/system/llm-gateway.service
# 添加：Environment="LLM_GATEWAY_ENABLE_BANDIT_SCORING=true"

sudo systemctl daemon-reload
sudo systemctl restart llm-gateway
```

---

### 问题 3: 数据库未更新

**症状：**
```sql
SELECT COUNT(*) FROM credentials WHERE last_scored_at > NOW() - INTERVAL '1 hour';
-- 返回 0
```

**排查：**
```bash
# 检查是否有请求流量
sudo journalctl -u llm-gateway --since "10 minutes ago" | grep "recordBandit"

# 检查 Flusher 是否运行
sudo journalctl -u llm-gateway --since "1 minute ago" | grep "bandit flush"
```

**解决：**
- 确保有真实请求流量
- 等待至少 10 秒（flush 间隔）
- 检查数据库权限

---

### 问题 4: LoadFromDB 失败

**症状：**
```
bandit: failed to load state from database: ...
```

**排查：**
```bash
# 手动测试查询
psql -U kxuser -d llm_gateway -c "
SELECT id, bandit_alpha, bandit_beta 
FROM credentials 
WHERE status <> 'disabled' 
LIMIT 5;
"
```

**解决：**
- 确认字段名正确（`bandit_alpha` 而非 `alpha`）
- 确认数据库用户有 SELECT 权限
- 查看完整错误日志

---

## 联系方式

- **技术支持：** DevOps 团队
- **紧急联系：** on-call engineer
- **文档维护：** AI Assistant (2026-06-26)

---

## 附录

### 相关文档

- [审计修正报告](./bandit-audit-fixes-complete.md)
- [Phase 1 完成报告](./phase1-bandit-integration-complete.md)
- [审计总结](./AUDIT-SUMMARY.md)

### 更新日志

| 日期 | 版本 | 变更 |
|-----|------|------|
| 2026-06-26 | v1.0 | 初始版本 |
