# P0-3: 计费事务协调器 - 部署验证清单

**方案**: 方案C（计算积分与扣减钱包分离）  
**状态**: ✅ 阶段1-2已完成，待部署验证  
**创建日期**: 2026-11-26

---

## 📋 部署前检查清单

### 代码完整性

- [x] ✅ 阶段1代码已实施
  - [x] `maas/calculate_credits.go`
  - [x] `maas/deduct_credits.go`
  - [x] `maas/calculate_credits_test.go`
  - [x] `domains/streaming/handler.go` (L8096-8171)

- [x] ✅ 阶段2代码已实施
  - [x] `sql/migrations/startup/1002_billing_retry_queue.sql`
  - [x] `sql/migrations/startup/1002_billing_retry_queue.down.sql`
  - [x] `maas/billing_retry_queue.go`
  - [x] `bg/billing_retry_worker.go`
  - [x] `bg/billing_retry_worker_test.go`
  - [x] `maas/integration_test.go`

- [x] ✅ 代码编译通过
  - [x] `go build ./maas`
  - [x] `go build ./bg`
  - [x] `go build ./domains/streaming`
  - [x] `go build ./cmd/gateway`

- [x] ✅ 单元测试通过
  - [x] `go test ./maas` (已修复倍率规范化逻辑)
  - [x] `go test ./bg`

- [x] ✅ Worker集成完成
  - [x] 修改 `cmd/gateway/main.go`
  - [x] 已添加import `"github.com/kaixuan/llm-gateway-go/bg"`
  - [x] 在maasSvc初始化后启动worker (L3412-3425)
  - [x] 提交: `2552f8890`

### 数据库迁移

- [x] ✅ Dev环境迁移 (acc-dev-postgres)
  - [x] 执行迁移：`docker exec -i acc-dev-postgres psql -U acc_dev -d acc_dev < sql/migrations/startup/1002_billing_retry_queue.sql`
  - [x] 验证表创建：14列，状态=✅
  - [x] 验证索引创建：4个索引（pkey + 3个业务索引），状态=✅
  - [x] 验证视图创建：v_billing_retry_stats，状态=✅
  - [x] 验证报告：`docs/fixes/2026-11-26-p0-3-migration-verification.md`
  - [x] 执行时间：2026-11-26 20:27

- [ ] ⏳ Staging环境迁移
  - [ ] 运行迁移
  - [ ] 验证数据库对象

- [ ] ⏳ Production环境迁移
  - [ ] 运行迁移
  - [ ] 验证数据库对象

### ~~Worker启动~~ (已完成)

```go
// 在 cmd/gateway/main.go 中添加
if maasSvc != nil && maasSvc.Enabled() {
    worker := bg.NewBillingRetryWorker(
        maasSvc,
        5*time.Minute,  // 扫描间隔
        100,            // 批大小
    )
    
    go worker.Start(ctx)
    
    slog.Info("billing retry worker started",
        "interval", 5*time.Minute,
        "batch_size", 100)
}
```

---

## 🧪 Dev环境验证

### 1. 功能验证

**测试场景A：正常流程**
```bash
# 1. 发起一个请求（通过API或测试工具）
curl -X POST http://dev-gateway/v1/chat/completions \
  -H "Authorization: Bearer $TEST_API_KEY" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello"}]
  }'

# 2. 查询telemetry（验证cost_usd和credits_charged同时写入）
psql -c "
SELECT request_id, cost_usd, credits_charged, rate_multiplier
FROM session_turns
WHERE request_id = '<request_id>'
"

# 预期：
# - cost_usd IS NOT NULL
# - credits_charged IS NOT NULL
# - credits_charged > 0

# 3. 查询钱包余额（验证扣减成功）
psql -c "
SELECT balance_credits
FROM tenant_credit_wallets
WHERE tenant_id = '<tenant_id>'
"

# 预期：余额已减少

# 4. 查询credit_ledger（验证扣减记录）
psql -c "
SELECT amount, entry_type, ref_id
FROM credit_ledger
WHERE tenant_id = '<tenant_id>'
  AND ref_id = '<request_id>'
ORDER BY created_at DESC
LIMIT 1
"

# 预期：
# - amount = -<credits_charged>
# - entry_type = 'consume'
```

**测试场景B：异步扣减失败 → 自动重试**
```bash
# 1. 模拟扣减失败（临时停止PG或修改租户ID为不存在）

# 2. 发起请求

# 3. 查询telemetry（验证credits_charged仍然写入）
psql -c "
SELECT request_id, cost_usd, credits_charged
FROM session_turns
WHERE request_id = '<request_id>'
"

# 预期：credits_charged有值（不受扣减失败影响）

# 4. 查询重试队列（验证任务已入队）
psql -c "
SELECT id, request_id, credits, attempts, status, next_retry_at
FROM billing_retry_queue
WHERE request_id = '<request_id>'
"

# 预期：
# - status = 'pending'
# - attempts = 0
# - next_retry_at <= NOW() + INTERVAL '1 minute'

# 5. 恢复PG连接

# 6. 等待worker处理（最多5分钟）

# 7. 再次查询重试队列
psql -c "
SELECT id, request_id, status, attempts, completed_at
FROM billing_retry_queue
WHERE request_id = '<request_id>'
"

# 预期：
# - status = 'completed'
# - attempts = 1
# - completed_at IS NOT NULL

# 8. 验证钱包已扣减
psql -c "
SELECT balance_credits
FROM tenant_credit_wallets
WHERE tenant_id = '<tenant_id>'
"
```

**测试场景C：幂等性验证**
```bash
# 1. 发起请求A（正常完成）

# 2. 手工触发重复扣减（相同request_id）
# （模拟代码或通过测试工具）

# 3. 查询credit_ledger（验证只扣了一次）
psql -c "
SELECT COUNT(*), SUM(amount)
FROM credit_ledger
WHERE tenant_id = '<tenant_id>'
  AND ref_id = '<request_id>'
  AND entry_type = 'consume'
"

# 预期：
# - COUNT(*) 可能 > 1（订阅+余额分多条）
# - SUM(amount) = -<credits_charged>（总额一致）
```

### 2. 性能验证

**基准测试**：
```bash
# 1. 计算积分性能（应该 < 5ms）
# （使用benchmark工具或Go benchmark）

# 2. 请求完成延迟对比
# 旧逻辑（同步计费）：测量end-to-end时间
# 新逻辑（异步扣减）：测量end-to-end时间

# 预期：新逻辑延迟降低 2-3ms
```

### 3. 监控指标

**一致性覆盖率**（每日检查）：
```sql
SELECT 
    DATE(created_at) AS date,
    COUNT(*) AS total_requests,
    COUNT(*) FILTER (WHERE credits_charged IS NOT NULL) AS with_credits,
    COUNT(*) FILTER (WHERE credits_charged IS NOT NULL) * 100.0 / COUNT(*) AS coverage_pct
FROM session_turns
WHERE created_at > NOW() - INTERVAL '24 hours'
GROUP BY DATE(created_at)
ORDER BY date DESC;

-- 预期：coverage_pct > 99%
```

**重试队列统计**：
```sql
SELECT * FROM v_billing_retry_stats
WHERE date = CURRENT_DATE;

-- 预期：
-- - success_rate > 95%
-- - avg_attempts_on_success < 2
-- - failed_count < 10
```

**钱包余额一致性**（每日对账）：
```sql
SELECT 
    tenant_id,
    balance_credits AS wallet_balance,
    (SELECT SUM(amount) FROM credit_ledger WHERE tenant_id = w.tenant_id) AS ledger_total,
    balance_credits - (SELECT SUM(amount) FROM credit_ledger WHERE tenant_id = w.tenant_id) AS diff
FROM tenant_credit_wallets w
WHERE ABS(balance_credits - (SELECT SUM(amount) FROM credit_ledger WHERE tenant_id = w.tenant_id)) > 100
ORDER BY ABS(diff) DESC
LIMIT 20;

-- 预期：diff < 100（允许微小误差）
```

---

## 🚀 Staging环境验证

### 压力测试

**并发请求测试**：
```bash
# 使用k6或类似工具
k6 run --vus 100 --duration 5m load-test.js

# 验证：
# 1. 无OOM或CPU爆炸
# 2. 一致性覆盖率保持 > 99%
# 3. 重试队列无积压
```

**故障注入测试**：
```bash
# 1. 临时停止PG（30秒）
# 2. 发起100个请求
# 3. 恢复PG
# 4. 观察重试队列（应该在5-10分钟内全部完成）
```

---

## 🌐 Production环境部署

### 灰度发布策略

**阶段1：10%流量（24小时）**
- 修改负载均衡权重：10%流量到新版本
- 监控指标：
  - 一致性覆盖率
  - 重试成功率
  - P95延迟
- 如有异常：立即回滚到0%

**阶段2：50%流量（48小时）**
- 修改负载均衡权重：50%流量到新版本
- 继续监控
- 如有异常：回滚到10%或0%

**阶段3：100%流量（持续监控）**
- 全部流量到新版本
- 持续监控7天
- 确认无问题后，可关闭旧代码路径

### 回滚计划

**场景1：Worker异常（高CPU/内存）**
```bash
# 1. 停止worker（修改代码重启，或kill goroutine）
# 2. 保持新计费逻辑（不影响主流程）
# 3. 手工处理重试队列（如需要）
```

**场景2：一致性覆盖率下降（< 90%）**
```bash
# 1. 立即回滚到旧逻辑
# 2. 分析日志和数据库
# 3. 修复问题后重新部署
```

**完全回滚步骤**：
```bash
# 1. 恢复handler.go旧逻辑（git revert）
# 2. 重新编译部署
# 3. 停止worker
# 4. （可选）运行迁移回滚：./migrate down 1002
```

---

## 📊 部署后监控（前7天）

### 每日检查清单

- [ ] 一致性覆盖率 > 99%
- [ ] 重试成功率 > 95%
- [ ] 永久失败任务 < 10
- [ ] 钱包余额一致性 diff < 100
- [ ] 无ERROR日志爆炸
- [ ] 无性能回退（P95延迟）

### 告警设置

**P2告警**（需立即处理）：
- 一致性覆盖率 < 95%（连续1小时）
- 永久失败任务 > 100

**P3告警**（需关注）：
- 重试失败率 > 5%（连续1小时）
- 重试队列积压 > 1000

---

## ✅ 验证完成标准

部署被认为成功的标准：

1. ✅ 所有功能测试通过（场景A/B/C）
2. ✅ 性能无回退（延迟降低或持平）
3. ✅ 一致性覆盖率 > 99%（持续7天）
4. ✅ 重试成功率 > 95%（持续7天）
5. ✅ 钱包余额一致性 diff < 100（每日对账）
6. ✅ 无生产事故

---

**验证负责人**: 待定  
**预计验证时间**: 2-3天（Dev）+ 7天（Staging）+ 7天（Production灰度）  
**最终批准**: 待定
