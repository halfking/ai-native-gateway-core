# 252服务器部署 - 执行清单

## 连接到252服务器

```bash
ssh -p 25022 <env:LAN_GATEWAY_IP>
```

## 快速部署（3个命令）

### 选项A: 使用自动化脚本

```bash
# 1. 进入项目目录
cd /path/to/llm-gateway-go

# 2. 拉取最新代码
git pull origin main

# 3. 执行部署脚本（需要修改路径）
./deploy/sql/deploy-252-complete.sh
```

### 选项B: 手动执行（如果脚本失败）

```bash
# 1. 进入项目目录
cd /path/to/llm-gateway-go
git pull origin main

# 2. 执行数据库迁移
psql -h localhost -U postgres -d llm_gateway \
  -f deploy/sql/migrations/2026-07-26-provider-profile-system.sql

# 3. 添加配置
psql -h localhost -U postgres -d llm_gateway <<EOF
INSERT INTO settings (key, value, updated_at)
VALUES 
  ('provider_profile.enabled', 'true', NOW()),
  ('provider_profile.collection_interval', '7200', NOW()),
  ('provider_profile.aggregation_interval', '86400', NOW()),
  ('provider_profile.cleanup_interval', '604800', NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();
EOF

# 4. 重启网关
sudo systemctl restart llm-gateway
```

## 验证部署

```bash
# 1. 检查表
psql -h localhost -U postgres -d llm_gateway -c \
  "SELECT tablename FROM pg_tables WHERE tablename LIKE 'provider_profile%' ORDER BY tablename;"

# 应该看到5个表:
# - provider_profile_alerts
# - provider_profile_daily
# - provider_profile_metrics
# - provider_profile_whitelist
# - provider_credibility_tests

# 2. 检查配置
psql -h localhost -U postgres -d llm_gateway -c \
  "SELECT * FROM settings WHERE key LIKE 'provider_profile%';"

# 3. 查看日志
tail -f /var/log/llm-gateway/gateway.log | grep "provider profile"

# 应该看到:
# [INFO] provider profile system initialized ...
# [INFO] provider profile collector started ...
# [INFO] CHECKPOINT: provider profile system started
```

## 等待采集数据

```bash
# 2小时后检查采集数据
psql -h localhost -U postgres -d llm_gateway -c \
  "SELECT COUNT(*) FROM provider_profile_metrics;"

# 次日凌晨4点后检查聚合数据
psql -h localhost -U postgres -d llm_gateway -c \
  "SELECT COUNT(*) FROM provider_profile_daily;"
```

## 完成！

✅ 数据库已部署  
✅ 配置已启用  
✅ 网关已重启  
✅ 系统正在运行  

---

**注意**: 如果遇到问题，参考完整文档:
- `docs/供应商管理/数据库部署说明.md`
- `deploy/sql/DEPLOY-252.md`
