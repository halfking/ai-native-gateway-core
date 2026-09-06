# 快速部署指南 - 252服务器

## 立即执行（需要SSH访问252服务器）

### 选项 1: 一键部署（推荐）

```bash
# SSH 到252服务器
ssh user@<env:LAN_GATEWAY_IP>

# 进入项目目录
cd /path/to/llm-gateway-go

# 拉取最新代码
git pull origin main

# 执行部署脚本
./deploy/sql/deploy-provider-profile.sh
```

### 选项 2: 手动执行（如果脚本有问题）

```bash
# SSH 到252服务器
ssh user@<env:LAN_GATEWAY_IP>

# 执行 SQL 脚本
psql -h localhost -U postgres -d llm_gateway \
  -f /path/to/llm-gateway-go/deploy/sql/migrations/2026-07-26-provider-profile-system.sql
```

### 选项 3: 从本地推送执行（如果252可以SSH）

```bash
# 在本地执行
cat deploy/sql/migrations/2026-07-26-provider-profile-system.sql | \
  ssh user@<env:LAN_GATEWAY_IP> "psql -h localhost -U postgres -d llm_gateway"
```

---

## 验证部署成功

```sql
-- 连接数据库
psql -h localhost -U postgres -d llm_gateway

-- 检查表
SELECT COUNT(*) FROM pg_tables 
WHERE schemaname='public' AND tablename LIKE 'provider_profile%';
-- 应该返回: 5

-- 检查索引
SELECT COUNT(*) FROM pg_indexes 
WHERE schemaname='public' AND indexname LIKE '%provider_profile%';
-- 应该返回: 10+

-- 检查 credentials 扩展
\d credentials
-- 应该看到 auto_disabled_at, auto_disabled_reason, auto_enabled_at, auto_enabled_reason
```

---

## 启用系统

### 方法 1: 通过配置文件

编辑配置文件（如 `/etc/llm-gateway/config.yaml`），添加：

```yaml
provider_profile:
  enabled: true
  collection_interval: 7200
  aggregation_interval: 86400
  cleanup_interval: 604800
```

### 方法 2: 通过数据库（如果使用 settings 表）

```sql
INSERT INTO settings (key, value, updated_at)
VALUES 
  ('provider_profile.enabled', 'true', NOW()),
  ('provider_profile.collection_interval', '7200', NOW()),
  ('provider_profile.aggregation_interval', '86400', NOW()),
  ('provider_profile.cleanup_interval', '604800', NOW())
ON CONFLICT (key) DO UPDATE 
  SET value = EXCLUDED.value, updated_at = NOW();
```

### 方法 3: 通过环境变量

```bash
export PROVIDER_PROFILE_ENABLED=true
export PROVIDER_PROFILE_COLLECTION_INTERVAL=7200
export PROVIDER_PROFILE_AGGREGATION_INTERVAL=86400
export PROVIDER_PROFILE_CLEANUP_INTERVAL=604800
```

---

## 重启网关

```bash
# 使用 systemd
sudo systemctl restart llm-gateway

# 或手动重启
kill -TERM $(cat /var/run/llm-gateway.pid)
/usr/local/bin/llm-gateway &
```

---

## 查看日志验证

```bash
# 实时查看日志
tail -f /var/log/llm-gateway/gateway.log | grep "provider profile"

# 或使用 journalctl
journalctl -u llm-gateway -f | grep "provider profile"
```

**期望看到**：
```
[INFO] provider profile system initialized ...
[INFO] provider profile collector started interval=2h0m0s
[INFO] provider profile aggregator started interval=24h0m0s
[INFO] provider profile cleaner started interval=168h0m0s
[INFO] CHECKPOINT: provider profile system started
```

**如果看到**：
```
[INFO] provider profile: disabled via settings
```
说明配置未启用，请检查配置。

---

## 监控数据

等待2小时后，查看采集数据：

```sql
-- 查看采集记录
SELECT credential_id, metric_time, network_latency_p95, 
       availability_total_requests, availability_success_requests
FROM provider_profile_metrics
ORDER BY metric_time DESC
LIMIT 10;
```

等待次日凌晨4点后，查看每日画像：

```sql
-- 查看画像数据
SELECT credential_id, profile_date, 
       network_score, availability_score, stability_score, scale_score, total_score
FROM provider_profile_daily
ORDER BY profile_date DESC, total_score DESC
LIMIT 10;
```

---

## 完成！

系统部署完成后：
- ✅ 数据库已部署
- ✅ 系统已集成
- ✅ 配置已启用
- ✅ 网关已重启
- ✅ 日志显示正常

供应商画像系统现在正在运行！🎉
