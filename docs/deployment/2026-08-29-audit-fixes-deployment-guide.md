# 2026-08-29 审计修复生产部署指南

**创建时间**：2026-08-29 19:00  
**目标环境**：Staging → Production  
**关联审计**：[2026-08-29-comprehensive-24h-audit.md](../audit/2026-08-29-comprehensive-24h-audit.md)

---

## 部署前检查清单

### 代码准备 ✅

- [x] 所有 P0/P1/P2 问题已修复
- [x] 构建验证通过（`go build ./...`）
- [x] 单元测试通过（`go test ./... -short`）
- [x] 代码已推送至 `main` 分支（commit: `589ed9e43`）

### 待验证项目 ⏳

- [ ] Staging 环境部署验证
- [ ] 至少 100 个会话完成 V1/V2 双写和校验
- [ ] 前端详情页抽样验证通过
- [ ] 错误率 < 0.1%
- [ ] p99 延迟 < 500ms

---

## 部署步骤

### 第一阶段：Staging 环境部署

#### 1. 数据库 Migration（按顺序执行）

```bash
# 连接到 Staging 数据库
psql -h staging-db -U llm_gateway -d llm_gateway

# 执行 Migration（按顺序）
\i sql/migrations/startup/614_session_bodies_hot.sql
\i sql/migrations/startup/615_session_bodies_hot_promote_function.sql
\i sql/migrations/startup/616_provider_error_details_unique_constraint.sql
\i sql/migrations/startup/617_session_turns_unified_view.sql

# 验证表和视图已创建
\d session_bodies_hot
\d session_turns_unified
SELECT * FROM provider_error_details LIMIT 1;
```

#### 2. 应用部署

```bash
# 拉取最新代码
cd /path/to/llm-gateway-go
git pull origin main
git log -1  # 确认 commit 589ed9e43

# 构建
go build -o llm-gateway cmd/gateway/main.go

# 重启服务
systemctl restart llm-gateway

# 检查启动日志
journalctl -u llm-gateway -f | grep -E "provider_error_aggregator|partition_manager"
```

#### 3. 验证检查

**3.1 验证 hot 表写入**

```sql
-- 等待 5 分钟后执行
SELECT COUNT(*) FROM session_bodies_hot;
SELECT COUNT(*) FROM session_turns_hot;

-- 应该有新数据写入
```

**3.2 验证 provider_error_aggregator 运行**

```sql
-- 等待 10 分钟后执行（首次聚合）
SELECT COUNT(*) FROM provider_error_details;

-- 查看聚合结果
SELECT 
    provider_id,
    model_name,
    error_type,
    error_code,
    occurrences,
    first_seen_at,
    last_seen_at
FROM provider_error_details
ORDER BY last_seen_at DESC
LIMIT 10;
```

**3.3 验证 circuit/limiter 拒绝记录**

```sql
-- 查看是否有 circuit-open 或 concurrency_limiter 拒绝记录
SELECT 
    error_kind,
    COUNT(*) as count
FROM candidate_failure_logs_hot
WHERE ts > now() - interval '1 hour'
GROUP BY error_kind;

-- 应该能看到 'concurrent' 类型的记录
```

**3.4 验证 Prometheus 指标**

```bash
# 检查新指标是否暴露
curl -s http://localhost:9090/metrics | grep -E "hot_table_promote"

# 应该能看到：
# llm_gateway_hot_table_promote_failures_total
# llm_gateway_hot_table_promote_batches_total
# llm_gateway_hot_table_promote_rows_total
# llm_gateway_hot_table_promote_duration_seconds
```

#### 4. 功能测试

**4.1 Session V2 写入和查询测试**

```bash
# 发送测试请求（带会话 ID）
curl -X POST http://staging-gateway/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "X-Session-ID: test-session-$(date +%s)" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello"}]
  }'

# 等待 30 秒后查询
# 应该能从 session_turns_unified 和 session_bodies_unified 查到数据
```

**4.2 错误聚合测试**

```sql
-- 手动插入一条测试失败记录
INSERT INTO candidate_failure_logs_hot (
    request_id, tenant_id, provider_id, credential_id,
    model_name, endpoint, error_kind, error_code, error_message,
    ts, partition_date
) VALUES (
    'test-req-' || extract(epoch from now())::text,
    'test-tenant',
    1,
    'test-cred',
    'test-model',
    'https://test.com',
    'network',
    '500',
    'Test error message',
    now(),
    CURRENT_DATE
);

-- 等待 10 分钟后检查是否被聚合
SELECT * FROM provider_error_details 
WHERE error_message LIKE 'Test error%';
```

---

### 第二阶段：Production 部署

**前置条件**：Staging 环境运行 24 小时无异常

#### 1. 灰度发布计划

**时间窗口**：选择低峰期（建议凌晨 2:00-4:00）

**灰度策略**：
- Phase 1: 10% 流量（1 台实例）- 运行 2 小时
- Phase 2: 50% 流量（2 台实例）- 运行 4 小时
- Phase 3: 100% 流量（全部实例）- 全量切换

#### 2. 生产数据库 Migration

```bash
# 创建备份
pg_dump -h prod-db -U llm_gateway llm_gateway > backup_$(date +%Y%m%d_%H%M%S).sql

# 执行 Migration（在事务中）
psql -h prod-db -U llm_gateway -d llm_gateway <<EOF
BEGIN;
\i sql/migrations/startup/614_session_bodies_hot.sql
\i sql/migrations/startup/615_session_bodies_hot_promote_function.sql
\i sql/migrations/startup/616_provider_error_details_unique_constraint.sql
\i sql/migrations/startup/617_session_turns_unified_view.sql
COMMIT;
EOF

# 验证
psql -h prod-db -U llm_gateway -d llm_gateway -c "\d session_bodies_hot"
```

#### 3. 逐台部署应用

**对于每台实例**：

```bash
# 1. 停止实例
systemctl stop llm-gateway

# 2. 更新代码
cd /path/to/llm-gateway-go
git pull origin main

# 3. 构建
go build -o llm-gateway cmd/gateway/main.go

# 4. 启动
systemctl start llm-gateway

# 5. 健康检查（等待 30 秒）
curl http://localhost:8080/health
curl http://localhost:8080/readiness

# 6. 观察日志 5 分钟
journalctl -u llm-gateway -f

# 如果正常，继续下一台；如果异常，立即回滚
```

#### 4. 监控指标

**关键指标监控**：

1. **错误率**：`llm_gateway_request_errors_total / llm_gateway_requests_total < 0.001`
2. **延迟**：`histogram_quantile(0.99, llm_gateway_request_duration_seconds) < 0.5`
3. **Hot 表健康度**：
   - `llm_gateway_hot_table_promote_failures_total` 无增长
   - `llm_gateway_hot_table_promote_rows_total` 持续增长
4. **聚合器健康度**：
   - `provider_error_details` 表行数持续增长
   - 无聚合器错误日志

**告警配置**：

```yaml
# Prometheus 告警规则
groups:
  - name: llm_gateway_audit_fixes
    interval: 30s
    rules:
      - alert: HotTablePromoteFailure
        expr: rate(llm_gateway_hot_table_promote_failures_total[5m]) > 0
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Hot table promote failing for {{ $labels.table }}"
          description: "Table {{ $labels.table }} has failed promote operations"
      
      - alert: ProviderErrorAggregatorDown
        expr: |
          (time() - provider_error_aggregation_last_success_timestamp) > 1200
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "Provider error aggregator not running"
          description: "Last successful aggregation was {{ $value }}s ago"
```

---

## 验证清单

### Staging 环境验证 ⏳

- [ ] Migration 614-617 执行成功
- [ ] session_bodies_hot 有数据写入
- [ ] session_turns_unified 视图可查询
- [ ] provider_error_details 有聚合数据
- [ ] candidate_failure_logs_hot 包含 circuit/limiter 拒绝
- [ ] Prometheus 指标正常暴露
- [ ] 无错误日志或异常告警
- [ ] 运行 24 小时稳定

### Production 环境验证 ⏳

- [ ] Phase 1 (10% 流量) 运行 2 小时正常
- [ ] Phase 2 (50% 流量) 运行 4 小时正常
- [ ] Phase 3 (100% 流量) 全量切换成功
- [ ] 错误率 < 0.1%
- [ ] p99 延迟 < 500ms
- [ ] 至少 100 个会话完成 V1/V2 双写
- [ ] 前端详情页抽样验证通过
- [ ] 监控指标正常，无告警

---

## 回滚方案

### 数据库回滚

**注意**：hot 表已有数据写入，不建议回滚数据库

如果必须回滚：

```sql
-- 1. 停止所有应用实例
-- 2. 执行回滚 SQL
BEGIN;
DROP VIEW IF EXISTS session_turns_unified;
DROP VIEW IF EXISTS session_bodies_unified;
DROP TABLE IF EXISTS session_bodies_hot;
-- 注意：不要删除 provider_error_details，已有聚合数据
COMMIT;

-- 3. 恢复备份（如果需要）
psql -h db -U llm_gateway llm_gateway < backup_YYYYMMDD_HHMMSS.sql
```

### 应用回滚

```bash
# 回滚到上一个稳定版本
git checkout <previous-stable-commit>
go build -o llm-gateway cmd/gateway/main.go
systemctl restart llm-gateway
```

---

## 常见问题排查

### 问题 1：session_bodies_hot 无数据写入

**排查**：
```sql
-- 检查是否有会话请求
SELECT COUNT(*) FROM request_logs_hot WHERE gw_session_id IS NOT NULL;

-- 检查应用日志
journalctl -u llm-gateway | grep "session_bodies"
```

**可能原因**：
- 代码未正确部署
- 数据库连接问题
- RLS 策略阻止写入

### 问题 2：provider_error_aggregator 不运行

**排查**：
```bash
# 检查启动日志
journalctl -u llm-gateway | grep "provider_error_aggregator"

# 检查进程
ps aux | grep llm-gateway
```

**可能原因**：
- PartitionManager 未启动
- 数据库连接失败
- Advisory lock 冲突

### 问题 3：promote 失败

**排查**：
```sql
-- 检查 hot 表数据量
SELECT 'session_bodies_hot' as table_name, COUNT(*) FROM session_bodies_hot
UNION ALL
SELECT 'session_turns_hot', COUNT(*) FROM session_turns_hot;

-- 检查 promote 函数
SELECT promote_session_bodies_hot_to_partition('8 hours'::interval, 1000);
```

**可能原因**：
- 分区表不存在
- Advisory lock 被占用
- 函数签名不匹配

---

## 联系方式

**技术负责人**：AI Assistant  
**紧急联系**：待填写  
**Slack 频道**：#llm-gateway-ops  
**文档链接**：
- [审计报告](../audit/2026-08-29-comprehensive-24h-audit.md)
- [任务完成报告](../audit/2026-08-29-task-completion-update.md)

---

**部署负责人**：_____________  
**部署时间**：_____________  
**部署结果**：[ ] 成功 / [ ] 部分成功 / [ ] 失败  
**备注**：_____________
