# 部署清单 - Plan Type 标准化 + 路由修复

**版本**: feat/plan-type-full (已合并到 main)
**目标环境**: test-apps → 184 生产
**预计停机时间**: 0（兼容性部署）

## 变更摘要

### 数据库变更
- ✅ **Migration 327**: Plan type 标准化（SSOT + 视图）
- ✅ **Migration 328**: Provider 过滤增强

### 代码变更
- ✅ `modelcatalog/upsert.go`: Discovery 自动写入 billing_mode
- ✅ `domains/streaming/executors/executor.go`: Quota 切换 + 死循环保护

### 影响范围
- **低风险**: 仅增强过滤逻辑，不修改现有可用路由
- **兼容性**: 向后兼容，无 breaking changes
- **性能**: 视图查询（需监控）

---

## 部署前检查

### 1. 代码准备
- [x] 所有 commits 已合并到 main
- [x] 构建通过 (`go build ./...`)
- [x] 单元测试通过 (`go test ./modelcatalog/...`)
- [x] Migration 文件已重命名（327, 328）

### 2. 数据库备份
- [ ] 备份生产数据库（credentials, credential_model_bindings 表）
- [ ] 验证备份完整性
- [ ] 记录当前 schema_migrations 版本

### 3. 环境验证
- [x] 本地 r112_postgres 测试通过（5个用例）
- [ ] test-apps 环境部署
- [ ] test-apps 运行时测试（TC6-TC8）

---

## 部署步骤（test-apps）

### Step 1: 部署代码

```bash
# 1. 拉取最新代码
cd /path/to/llm-gateway-go
git pull origin main

# 2. 构建二进制
go build -o llm-gateway ./cmd/gateway

# 3. 备份当前运行的二进制
cp /usr/local/bin/llm-gateway /usr/local/bin/llm-gateway.backup

# 4. 部署新二进制
cp llm-gateway /usr/local/bin/

# 5. 重启服务（平滑重启）
systemctl restart llm-gateway
# 或
supervisorctl restart llm-gateway
```

### Step 2: 应用 Migrations

```bash
# 1. 连接到数据库
psql -h <db-host> -U <db-user> -d llm_gateway

# 2. 检查当前版本
SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 5;

# 3. 应用 migration 327
\i migrations/327_credential_plan_type_full.sql
INSERT INTO schema_migrations (version) VALUES ('327');

# 4. 应用 migration 328
\i migrations/328_view_add_provider_filter.sql
INSERT INTO schema_migrations (version) VALUES ('328');

# 5. 验证视图
SELECT COUNT(*) FROM v_routable_credential_models WHERE is_routable = false AND unavailable_reason = 'provider_disabled';
-- 预期: > 0（禁用的 provider bindings）
```

### Step 3: 验证部署

```bash
# 1. 检查服务状态
systemctl status llm-gateway
# 或
curl http://localhost:8080/health

# 2. 检查日志（无错误）
tail -f /var/log/llm-gateway/gateway.log | grep -i error

# 3. 执行冒烟测试
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <test-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hello"}]}'

# 预期: 200 OK
```

### Step 4: 运行时测试

按照 `ROUTING_TEST_PLAN.md` 执行：

- [ ] **TC6**: 模拟 quota_exceeded，验证静默切换
- [ ] **TC7**: 所有候选失败，验证死循环保护（<30s）
- [ ] **TC8**: 客户端断开检测

---

## 回滚计划

### 场景 1: Migration 失败

```bash
# 1. 回滚 migration 328
\i migrations/328_view_add_provider_filter.down.sql
DELETE FROM schema_migrations WHERE version = '328';

# 2. 回滚 migration 327
\i migrations/327_credential_plan_type_full.down.sql
DELETE FROM schema_migrations WHERE version = '327';

# 3. 验证视图恢复
SELECT COUNT(*) FROM v_routable_credential_models;
```

### 场景 2: 运行时问题

```bash
# 1. 恢复旧二进制
cp /usr/local/bin/llm-gateway.backup /usr/local/bin/llm-gateway

# 2. 重启服务
systemctl restart llm-gateway

# 3. 回滚 migrations（同场景 1）
```

### 场景 3: 性能问题

```sql
-- 1. 临时禁用视图（回退到直接查询）
-- 修改代码使用旧的查询逻辑

-- 2. 分析慢查询
EXPLAIN ANALYZE SELECT * FROM v_routable_credential_models WHERE credential_id = 1;

-- 3. 添加缺失的索引（如果需要）
CREATE INDEX CONCURRENTLY idx_cmb_credential_id ON credential_model_bindings(credential_id);
```

---

## 监控指标

### 部署后 1 小时监控

| 指标 | 监控命令 | 预期值 |
|------|---------|--------|
| 错误率 | `grep ERROR gateway.log | wc -l` | 无显著增长 |
| quota_exceeded 客户端错误 | 查询 request_logs | 下降 |
| 平均响应时间 | 查询 request_logs.latency_ms | 无显著增长 |
| 禁用 provider 请求 | 查询 candidate_failure_logs | 应为 0 |

### 关键 SQL 查询

```sql
-- 1. 检查禁用 provider 是否被路由
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
```

---

## 生产部署（184）

### 前置条件
- [x] test-apps 运行 24 小时无问题
- [ ] 所有运行时测试通过（TC6-TC8）
- [ ] 性能指标正常

### 部署窗口
- **建议时间**: 非高峰期（凌晨 2:00-4:00 UTC+8）
- **预计耗时**: 15 分钟
- **回滚时间**: 5 分钟

### 灰度策略
1. 先部署到 1 台网关节点
2. 观察 30 分钟
3. 如果正常，滚动部署到所有节点

---

## 完成标准

### 必须满足（P0）
- [ ] Migrations 327, 328 成功应用
- [ ] 服务启动无错误
- [ ] 禁用 provider 请求数 = 0
- [ ] quota_exceeded 客户端错误率下降 > 20%
- [ ] 无死循环报告（所有请求 < 30s 返回）

### 建议满足（P1）
- [ ] 平均响应时间无显著变化（±5%）
- [ ] 凭据分布更均衡（负载均衡改善）
- [ ] 运行 7 天无新增故障

---

## 联系人

| 角色 | 姓名 | 联系方式 |
|------|------|---------|
| 开发负责人 | - | - |
| DBA | - | - |
| 运维负责人 | - | - |
| 应急响应 | - | - |

---

## 附录

- 设计文档: `docs/superpowers/specs/2026-07-03-credential-plan-type-full-design.md`
- 测试报告: `TEST_RESULTS_20260703_032400.md`
- 剩余工作: `PLAN_TYPE_REMAINING_WORK.md`
- 测试计划: `ROUTING_TEST_PLAN.md`
- 会话总结: `SESSION_SUMMARY.md`
