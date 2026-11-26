# P0-3: 端到端功能测试执行指南

**版本**: v1.0  
**创建日期**: 2026-11-26  
**代码版本**: 74bb5d7b3  
**前置条件**: Dev环境数据库迁移已完成 ✅

---

## 🎯 测试目标

验证P0-3计费事务协调器的完整流程：
1. 计算积分（同步）
2. 写入telemetry
3. 异步扣减钱包
4. 自动重试机制
5. 幂等性保证

---

## 📋 环境准备

### 1. 数据库要求

**必需的表**:
- `billing_retry_queue` ✅ (已通过迁移1002创建)
- `session_turns` (需存在)
- `tenant_credit_wallets` (需存在)
- `credit_ledger` (需存在)
- `api_keys` (需存在，包含测试key)

**验证命令**:
```bash
psql $DATABASE_URL -c "
SELECT table_name 
FROM information_schema.tables 
WHERE table_schema = 'public' 
  AND table_name IN ('billing_retry_queue', 'session_turns', 
                     'tenant_credit_wallets', 'credit_ledger', 'api_keys')
ORDER BY table_name;
"
```

**预期输出**: 5个表都存在

### 2. Gateway配置

**必需的环境变量**:
```bash
export LLM_GATEWAY_DATABASE_URL="postgres://user:pass@host:port/db?sslmode=disable"
export LLM_GATEWAY_REDIS_ADDR="host:port"
export LLM_GATEWAY_REDIS_PASSWORD="password"  # 如果需要
export LLM_GATEWAY_PORT=8080
```

**验证MaaS已启用**:
```bash
# 启动Gateway后检查日志
grep "maas credits billing enabled" gateway.log
grep "billing retry worker started" gateway.log
```

**预期输出**:
```
maas credits billing enabled
billing retry worker started interval=5m0s batch_size=100
```

### 3. 测试数据准备

**创建测试租户** (如果不存在):
```sql
INSERT INTO tenants (id, name, status)
VALUES ('test-tenant-001', 'Test Tenant', 'active')
ON CONFLICT (id) DO NOTHING;

-- 创建钱包（初始余额100,000积分）
INSERT INTO tenant_credit_wallets (tenant_id, balance_credits)
VALUES ('test-tenant-001', 100000)
ON CONFLICT (tenant_id) DO UPDATE 
SET balance_credits = 100000;
```

**创建测试API Key**:
```sql
INSERT INTO api_keys (key_hash, tenant_id, status, alias)
VALUES (
    encode(digest('test-key-p03-e2e', 'sha256'), 'hex'),
    'test-tenant-001',
    'active',
    'P0-3 E2E Test Key'
)
ON CONFLICT DO NOTHING
RETURNING id;
```

**获取明文key**:
```
test-key-p03-e2e
```

---

## 🧪 场景A: 正常流程测试

### 步骤1: 发起测试请求

```bash
# 保存request_id供后续查询使用
REQUEST_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')

curl -v -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-key-p03-e2e" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [
      {"role": "user", "content": "Say hello in 10 words"}
    ],
    "stream": false
  }' \
  -o /tmp/response-${REQUEST_ID}.json

# 从响应中提取实际的request_id
ACTUAL_REQUEST_ID=$(jq -r '.id' /tmp/response-${REQUEST_ID}.json)
echo "Request ID: $ACTUAL_REQUEST_ID"
```

**预期**: HTTP 200, 正常的completion响应

### 步骤2: 验证telemetry写入

**等待时间**: 请求完成后1-2秒

```sql
SELECT 
    request_id,
    cost_usd,
    credits_charged,
    rate_multiplier,
    created_at
FROM session_turns
WHERE request_id = '$ACTUAL_REQUEST_ID';
```

**预期结果**:
- `request_id` = `$ACTUAL_REQUEST_ID`
- `cost_usd` IS NOT NULL (例如: 0.0001)
- `credits_charged` IS NOT NULL (例如: 100)
- `rate_multiplier` = 1.0 (或配置的倍率)

**验证点**: ✅ 积分计算已同步完成并写入

### 步骤3: 验证钱包扣减

**等待时间**: 请求完成后5秒内（异步扣减）

```sql
-- 查询钱包余额
SELECT 
    tenant_id,
    balance_credits,
    updated_at
FROM tenant_credit_wallets
WHERE tenant_id = 'test-tenant-001';

-- 预期: balance_credits = 100000 - credits_charged
```

**验证查询**:
```sql
-- 获取本次扣减的积分
WITH charged AS (
    SELECT credits_charged 
    FROM session_turns 
    WHERE request_id = '$ACTUAL_REQUEST_ID'
)
SELECT 
    w.balance_credits as current_balance,
    100000 - c.credits_charged as expected_balance,
    w.balance_credits = (100000 - c.credits_charged) as balance_correct
FROM tenant_credit_wallets w
CROSS JOIN charged c
WHERE w.tenant_id = 'test-tenant-001';
```

**预期结果**:
- `balance_correct` = TRUE

**验证点**: ✅ 钱包异步扣减成功

### 步骤4: 验证账本记录

```sql
SELECT 
    amount,
    entry_type,
    ref_id,
    created_at
FROM credit_ledger
WHERE tenant_id = 'test-tenant-001'
  AND ref_id = '$ACTUAL_REQUEST_ID'
ORDER BY created_at DESC;
```

**预期结果**:
- 至少1条记录
- `amount` = `-credits_charged` (负数)
- `entry_type` = 'consume'
- `ref_id` = `$ACTUAL_REQUEST_ID`

**验证点**: ✅ 账本记录完整

### 步骤5: 确认重试队列为空

```sql
SELECT COUNT(*) as pending_tasks
FROM billing_retry_queue
WHERE request_id = '$ACTUAL_REQUEST_ID';
```

**预期结果**:
- `pending_tasks` = 0 (正常流程不入队)

**场景A总结**: 如果所有验证点通过，说明正常流程100%工作。

---

## 🧪 场景B: 失败重试测试

### 步骤1: 模拟扣减失败

**方法1: 临时停止PG连接池** (需修改代码或环境)
```bash
# 在Gateway运行时，临时阻止对tenant_credit_wallets的访问
# 这需要数据库管理员权限
psql $DATABASE_URL -c "
ALTER TABLE tenant_credit_wallets RENAME TO tenant_credit_wallets_tmp;
"
```

**方法2: 使用不存在的租户**
```sql
-- 创建一个没有钱包的租户
INSERT INTO tenants (id, name, status)
VALUES ('test-tenant-no-wallet', 'No Wallet Tenant', 'active')
ON CONFLICT DO NOTHING;

-- 创建对应的API key
-- (同场景A的步骤，租户ID改为test-tenant-no-wallet)
```

### 步骤2: 发起请求

```bash
REQUEST_ID_B=$(uuidgen | tr '[:upper:]' '[:lower:]')

curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key-no-wallet" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Test retry"}]
  }' \
  -o /tmp/response-b.json

ACTUAL_REQUEST_ID_B=$(jq -r '.id' /tmp/response-b.json)
```

**预期**: 请求仍然成功返回（扣减失败不影响业务）

### 步骤3: 验证telemetry仍写入

```sql
SELECT 
    request_id,
    credits_charged
FROM session_turns
WHERE request_id = '$ACTUAL_REQUEST_ID_B';
```

**预期**:
- `credits_charged` IS NOT NULL

**验证点**: ✅ 扣减失败不影响积分计算

### 步骤4: 验证任务已入队

```sql
SELECT 
    id,
    request_id,
    tenant_id,
    credits,
    status,
    attempts,
    next_retry_at,
    created_at
FROM billing_retry_queue
WHERE request_id = '$ACTUAL_REQUEST_ID_B';
```

**预期结果**:
- 1条记录
- `status` = 'pending'
- `attempts` = 0
- `next_retry_at` <= NOW() + INTERVAL '1 minute'

**验证点**: ✅ 失败任务正确入队

### 步骤5: 恢复环境

**如果用方法1**:
```sql
ALTER TABLE tenant_credit_wallets_tmp RENAME TO tenant_credit_wallets;
```

**如果用方法2**:
```sql
-- 创建钱包
INSERT INTO tenant_credit_wallets (tenant_id, balance_credits)
VALUES ('test-tenant-no-wallet', 100000);
```

### 步骤6: 等待Worker处理

**等待时间**: 最多5分钟（Worker扫描间隔）

**监控查询** (每30秒执行一次):
```sql
SELECT 
    request_id,
    status,
    attempts,
    last_attempted_at,
    completed_at
FROM billing_retry_queue
WHERE request_id = '$ACTUAL_REQUEST_ID_B';
```

**预期变化**:
1. `status` = 'pending' → 'retrying' → 'completed'
2. `attempts` = 0 → 1
3. `completed_at` IS NOT NULL

**超时处理**: 如果5分钟后仍未处理，检查：
- Gateway日志中是否有Worker错误
- Worker是否真的在运行 (`grep "billing retry worker" gateway.log`)

### 步骤7: 验证最终扣减成功

```sql
-- 查询钱包
SELECT balance_credits
FROM tenant_credit_wallets
WHERE tenant_id = 'test-tenant-no-wallet';

-- 查询账本
SELECT amount, entry_type
FROM credit_ledger
WHERE tenant_id = 'test-tenant-no-wallet'
  AND ref_id = '$ACTUAL_REQUEST_ID_B';
```

**预期**:
- 钱包余额已扣减
- 账本有consume记录

**场景B总结**: 如果任务从pending变为completed且最终扣减成功，说明自动重试机制工作正常。

---

## 🧪 场景C: 幂等性测试

### 步骤1: 完成一次正常请求

```bash
# 重用场景A的request_id
echo "Using request from Scenario A: $ACTUAL_REQUEST_ID"
```

### 步骤2: 记录初始状态

```sql
-- 记录钱包余额
SELECT balance_credits as initial_balance
FROM tenant_credit_wallets
WHERE tenant_id = 'test-tenant-001'
\gset

-- 记录账本总额
SELECT SUM(amount) as initial_ledger_sum
FROM credit_ledger
WHERE tenant_id = 'test-tenant-001'
\gset
```

### 步骤3: 手工触发重复扣减

**方法: 直接调用MaaS接口** (需要psql或测试脚本):

```sql
-- 模拟重复扣减（使用相同的request_id）
-- 注意: 这需要在数据库层面直接调用，或通过测试工具
-- 实际实现取决于MaaS的接口设计

-- 伪代码示例:
-- maasSvc.DeductCredits(ctx, DeductCreditsInput{
--     TenantID: 'test-tenant-001',
--     RequestID: '$ACTUAL_REQUEST_ID',  -- 重复的ID
--     Credits: 100,
-- })
```

**替代方法**: 通过Worker触发
```sql
-- 手工插入重复任务到重试队列
INSERT INTO billing_retry_queue 
    (tenant_id, request_id, credits, status)
VALUES 
    ('test-tenant-001', '$ACTUAL_REQUEST_ID', 100, 'pending');

-- 等待Worker处理（最多5分钟）
```

### 步骤4: 验证金额一致性

```sql
-- 对比钱包余额
SELECT 
    :initial_balance as before_duplicate,
    balance_credits as after_duplicate,
    balance_credits = :initial_balance as balance_unchanged
FROM tenant_credit_wallets
WHERE tenant_id = 'test-tenant-001';

-- 对比账本总额
SELECT 
    :initial_ledger_sum as before_duplicate,
    SUM(amount) as after_duplicate,
    SUM(amount) = :initial_ledger_sum as ledger_unchanged
FROM credit_ledger
WHERE tenant_id = 'test-tenant-001';
```

**预期结果**:
- `balance_unchanged` = TRUE
- `ledger_unchanged` = TRUE

**验证点**: ✅ 重复扣减被正确拒绝

### 步骤5: 检查重试队列状态

```sql
SELECT 
    request_id,
    status,
    attempts,
    last_error
FROM billing_retry_queue
WHERE request_id = '$ACTUAL_REQUEST_ID'
ORDER BY created_at DESC;
```

**预期**: 
- 如果有记录，`status` 应该是 'completed' 或 'failed'
- `last_error` 可能包含 "duplicate" 或 "already processed" 信息

**场景C总结**: 如果金额保持不变，说明幂等性保护工作正常。

---

## 📊 测试报告模板

### 执行摘要

| 场景 | 状态 | 耗时 | 备注 |
|------|------|------|------|
| 场景A: 正常流程 | ⏳ | - | - |
| 场景B: 失败重试 | ⏳ | - | - |
| 场景C: 幂等性 | ⏳ | - | - |

### 验证点清单

**场景A** (5个验证点):
- [ ] telemetry写入: credits_charged有值
- [ ] 钱包扣减: balance_credits正确
- [ ] 账本记录: credit_ledger有consume记录
- [ ] 重试队列: 无pending任务
- [ ] 响应正常: HTTP 200

**场景B** (6个验证点):
- [ ] telemetry不受影响: credits_charged仍写入
- [ ] 任务入队: billing_retry_queue有记录
- [ ] Worker处理: status变为completed
- [ ] 最终一致: 钱包最终扣减成功
- [ ] 账本完整: 有consume记录
- [ ] 时效性: 5分钟内完成

**场景C** (2个验证点):
- [ ] 钱包不变: balance_credits未重复扣减
- [ ] 账本不变: SUM(amount)保持一致

### 性能指标

```sql
-- 计算积分耗时（从日志提取或代码埋点）
-- 目标: < 5ms

-- 请求完成延迟（对比旧版本）
-- 目标: 降低 2-3ms

-- 一致性覆盖率
SELECT 
    COUNT(*) FILTER (WHERE credits_charged IS NOT NULL) * 100.0 / COUNT(*) 
        as coverage_pct
FROM session_turns
WHERE created_at > NOW() - INTERVAL '1 hour';
-- 目标: > 99%
```

### 问题记录

| 问题描述 | 严重性 | 状态 | 解决方案 |
|----------|--------|------|----------|
| - | - | - | - |

---

## 🚨 故障排查

### Worker未启动

**症状**: billing_retry_queue中的pending任务长时间不处理

**检查**:
```bash
grep "billing retry worker started" gateway.log
```

**原因**:
1. MaaS未启用 (`maasSvc.Enabled()` = false)
2. Worker启动代码未执行
3. Worker goroutine panic

**解决**: 检查Gateway启动日志和环境变量

### 扣减失败但未入队

**症状**: 钱包未扣减，但billing_retry_queue也没有记录

**检查**:
```sql
SELECT request_id, credits_charged
FROM session_turns
WHERE credits_charged IS NOT NULL
  AND created_at > NOW() - INTERVAL '1 hour'
ORDER BY created_at DESC
LIMIT 10;
```

**原因**:
1. 扣减代码抛出异常未捕获
2. 入队逻辑有bug

**解决**: 检查Gateway错误日志

### 重试无限循环

**症状**: attempts持续增长但status始终是pending/retrying

**检查**:
```sql
SELECT request_id, attempts, last_error
FROM billing_retry_queue
WHERE attempts > 5
ORDER BY attempts DESC;
```

**原因**:
1. 钱包不存在（但错误可重试）
2. 数据库连接池耗尽
3. 死锁

**解决**: 检查last_error字段，分析根本原因

---

## ✅ 验收标准

P0-3功能被认为通过测试的标准：

1. ✅ 场景A所有验证点通过
2. ✅ 场景B所有验证点通过
3. ✅ 场景C所有验证点通过
4. ✅ 一致性覆盖率 > 99% (1小时窗口)
5. ✅ Worker重试成功率 > 95%
6. ✅ 无ERROR级别日志（除预期的错误场景）

**签字**: _____________  
**日期**: _____________

---

**文档版本**: v1.0  
**最后更新**: 2026-11-26  
**维护者**: P0-3项目组
