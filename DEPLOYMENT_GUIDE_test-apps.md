# 部署手册 - test-apps 环境

**目标**: 部署 Plan Type 标准化 + 路由修复到 test-apps
**版本**: main@7a4c48ee
**执行人**: 运维团队
**预计时间**: 30 分钟
**风险等级**: 低（test-apps 环境，可回滚）

---

## 前置条件检查

- [ ] 确认有 test-apps 服务器 SSH 访问权限
- [ ] 确认有数据库访问权限（应用 migrations）
- [ ] 确认当前服务运行正常（健康检查通过）
- [ ] 已通知相关人员即将部署

---

## 部署步骤

### Step 1: SSH 到 test-apps 服务器

```bash
# 根据实际环境选择（示例）
ssh test-apps
# 或
ssh user@test-apps-host

# 切换到网关目录
cd /opt/llm-gateway-go
# 或根据实际路径
cd /path/to/llm-gateway-go
```

### Step 2: 备份当前版本

```bash
# 备份当前运行的二进制
sudo cp /usr/local/bin/llm-gateway /usr/local/bin/llm-gateway.backup.$(date +%Y%m%d_%H%M%S)

# 记录当前 git commit
git log -1 --oneline > /tmp/pre-deploy-commit.txt
cat /tmp/pre-deploy-commit.txt
```

### Step 3: 拉取最新代码

```bash
# 拉取最新代码
git fetch origin
git pull origin main

# 验证版本
git log -1 --oneline
# 预期输出: 7a4c48ee feat(admin): Steps 3-5 完成 - plan_type CRUD + setFreeModels + TC6-TC8 脚本
```

### Step 4: 构建新版本

```bash
# 构建
go build -o llm-gateway ./cmd/gateway

# 验证构建
ls -lh llm-gateway
./llm-gateway --version 2>&1 | head -3
```

### Step 5: 应用数据库 Migrations

⚠️ **重要**: 先在测试数据库上验证！

```bash
# 方式 1: 直接连接数据库
psql -h <db-host> -U <db-user> -d llm_gateway << 'EOF'
-- 检查当前版本
SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 5;

-- 应用 migration 327
\i migrations/327_credential_plan_type_full.sql
INSERT INTO schema_migrations (version) VALUES ('327') ON CONFLICT DO NOTHING;

-- 应用 migration 328
\i migrations/328_view_add_provider_filter.sql
INSERT INTO schema_migrations (version) VALUES ('328') ON CONFLICT DO NOTHING;

-- 验证
SELECT version FROM schema_migrations WHERE version IN ('327', '328');
SELECT COUNT(*) FROM v_routable_credential_models WHERE is_routable = false AND unavailable_reason = 'provider_disabled';
-- 预期: > 0（如果有禁用的 provider）

\q
EOF
```

**或者方式 2: 使用 docker exec（如果数据库在容器中）**

```bash
cat migrations/327_credential_plan_type_full.sql | docker exec -i <postgres-container> psql -U <user> -d llm_gateway
cat migrations/328_view_add_provider_filter.sql | docker exec -i <postgres-container> psql -U <user> -d llm_gateway
docker exec <postgres-container> psql -U <user> -d llm_gateway -c "INSERT INTO schema_migrations (version) VALUES ('327'), ('328') ON CONFLICT DO NOTHING"
```

### Step 6: 部署新二进制

```bash
# 停止服务（根据实际服务管理方式）
sudo systemctl stop llm-gateway
# 或
sudo supervisorctl stop llm-gateway

# 替换二进制
sudo cp llm-gateway /usr/local/bin/llm-gateway
sudo chmod +x /usr/local/bin/llm-gateway

# 启动服务
sudo systemctl start llm-gateway
# 或
sudo supervisorctl start llm-gateway

# 等待 3 秒
sleep 3
```

### Step 7: 验证部署

```bash
# 1. 检查服务状态
sudo systemctl status llm-gateway
# 或
sudo supervisorctl status llm-gateway

# 2. 检查健康接口
curl -sS http://localhost:8080/health
# 预期: HTTP 200

# 3. 检查日志（最近 50 行，无 ERROR）
sudo journalctl -u llm-gateway -n 50 --no-pager | grep -i error
# 或
sudo tail -50 /var/log/llm-gateway/gateway.log | grep -i error
# 预期: 无 critical error

# 4. 测试 API 可用性（冒烟测试）
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <test-api-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet-20241022",
    "messages": [{"role": "user", "content": "deployment test"}],
    "max_tokens": 10
  }' | jq .

# 预期: HTTP 200，有正常响应
```

---

## 运行时测试（TC6-TC8）

部署成功后，执行以下测试：

### TC6: Quota 耗尽后静默切换

```bash
cd /opt/llm-gateway-go
./scripts/test_tc6_quota_silent_failover.sh

# 预期输出:
# ✓ 网关健康
# ✓ 选择凭据 ID=X
# ✓ 凭据 X quota 已耗尽
# ✓ PASS: 客户端不感知 quota 失败（静默切换成功）
# ✓ PASS: 响应时间 Xs < 30s（无死循环）
# ✓ PASS: 网关已切换到其他凭据
# 🎉 TC6 通过：Quota 耗尽后静默切换功能正常
```

### TC7: 所有候选失败时不死循环

```bash
./scripts/test_tc7_no_infinite_loop.sh

# 预期输出:
# ✓ 网关健康
# ✓ 已备份
# ✓ 所有活跃凭据已标记为 exhausted
# ✓ PASS: 耗时 Xs < 30s（无死循环）
# ✓ PASS: 返回错误码 429/503/500（quota_exhausted 是预期行为）
# ✓ 凭据状态已恢复
# 🎉 TC7 通过：所有候选失败时无死循环
```

### TC8: 客户端断开检测

```bash
./scripts/test_tc8_client_disconnect.sh

# 预期输出:
# ✓ 网关健康
# ✓ 已备份
# ✓ 所有凭据已标记为 exhausted
# 客户端在 8s 断开（包含 8s 等待）
# ✓ PASS: 客户端断开后失败日志停止增长
# ✓ 凭据状态已恢复
# 🎉 TC8 通过：客户端断开检测功能正常
```

---

## 监控指标（部署后 1 小时）

### 关键 SQL 查询

```sql
-- 1. 禁用 provider 是否被误路由（应为 0）
SELECT COUNT(*) FROM request_logs r
JOIN credentials c ON c.id = r.credential_id
JOIN providers p ON p.id = c.provider_id
WHERE r.created_at > NOW() - INTERVAL '1 hour'
  AND (p.enabled = false OR p.manual_disabled = true);
-- 预期: 0

-- 2. quota_exceeded 错误率
SELECT 
    COUNT(*) FILTER (WHERE error_kind = 'quota') as quota_errors,
    COUNT(*) as total_requests,
    ROUND(COUNT(*) FILTER (WHERE error_kind = 'quota') * 100.0 / NULLIF(COUNT(*), 0), 2) as quota_error_rate
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour';
-- 预期: quota_error_rate 应下降（相比部署前）

-- 3. 凭据切换统计
SELECT 
    credential_id,
    COUNT(*) as request_count,
    AVG(latency_ms) as avg_latency,
    COUNT(*) FILTER (WHERE status = 'error') as error_count
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour'
GROUP BY credential_id
ORDER BY request_count DESC
LIMIT 10;
-- 预期: 负载较为均衡

-- 4. 视图性能检查
EXPLAIN ANALYZE 
SELECT * FROM v_routable_credential_models 
WHERE credential_id = (SELECT id FROM credentials WHERE status = 'active' LIMIT 1);
-- 预期: 执行时间 < 50ms
```

### 日志监控

```bash
# 监控错误日志
sudo journalctl -u llm-gateway -f | grep -i "error\|fatal\|panic"

# 监控 quota 切换日志
sudo journalctl -u llm-gateway -f | grep -i "quota\|failover\|credential.*switch"
```

---

## 回滚计划

如果出现问题，按以下步骤回滚：

### 场景 1: 服务启动失败

```bash
# 1. 恢复旧二进制
sudo systemctl stop llm-gateway
sudo cp /usr/local/bin/llm-gateway.backup.YYYYMMDD_HHMMSS /usr/local/bin/llm-gateway
sudo systemctl start llm-gateway

# 2. 验证
curl http://localhost:8080/health
```

### 场景 2: Migrations 导致问题

```bash
# 1. 回滚 migration 328
psql -h <db-host> -U <db-user> -d llm_gateway -f migrations/328_view_add_provider_filter.down.sql
psql -h <db-host> -U <db-user> -d llm_gateway -c "DELETE FROM schema_migrations WHERE version = '328'"

# 2. 回滚 migration 327
psql -h <db-host> -U <db-user> -d llm_gateway -f migrations/327_credential_plan_type_full.down.sql
psql -h <db-host> -U <db-user> -d llm_gateway -c "DELETE FROM schema_migrations WHERE version = '327'"

# 3. 验证
psql -h <db-host> -U <db-user> -d llm_gateway -c "SELECT COUNT(*) FROM v_routable_credential_models"
# 如果视图不存在，说明回滚成功
```

### 场景 3: 性能问题

```sql
-- 临时禁用视图（直接查询表）
-- 修改代码中的查询，从 v_routable_credential_models 改为直接查 credentials + credential_model_bindings

-- 或添加缺失的索引
CREATE INDEX CONCURRENTLY idx_cmb_credential_id ON credential_model_bindings(credential_id);
CREATE INDEX CONCURRENTLY idx_cmb_provider_model_id ON credential_model_bindings(provider_model_id);
```

---

## 验收标准

### 必须满足（P0）
- [ ] 服务启动成功，健康检查返回 200
- [ ] Migrations 327, 328 应用成功
- [ ] TC6-TC8 全部通过
- [ ] 禁用 provider 请求数 = 0
- [ ] 无新增 critical error 日志

### 建议满足（P1）
- [ ] quota_exceeded 客户端错误率下降 > 20%
- [ ] 平均响应时间无显著变化（±5%）
- [ ] 运行 1 小时无异常

---

## 完成后操作

1. 在部署记录中标记完成时间
2. 通知相关团队部署成功
3. 持续监控 24 小时
4. 如果一切正常，准备生产部署（184 环境）

---

## 联系方式

| 问题类型 | 联系人 | 备注 |
|---------|-------|------|
| 部署问题 | 运维团队 | - |
| 数据库问题 | DBA | - |
| 业务逻辑问题 | 开发团队 | - |
| 紧急回滚 | On-call | 24/7 |

---

## 附录

- 设计文档: `docs/superpowers/specs/2026-07-03-credential-plan-type-full-design.md`
- 完整测试计划: `ROUTING_TEST_PLAN.md`
- 部署清单: `DEPLOYMENT_CHECKLIST.md`
- 会话总结: `SESSION_SUMMARY.md`
