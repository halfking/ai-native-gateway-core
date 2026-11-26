# P0-3: 计费事务协调器 - 方案评估

**评估日期**: 2026-11-26  
**评估人**: AI Assistant  
**目标**: 解决成本记录与积分扣费的一致性问题

---

## 🎯 问题定义

### 核心问题

`session_turns.cost_usd`（USD成本）与 `credit_ledger.amount`（积分扣费）写入在**不同代码路径**，无事务保证。

### 当前状态

```
请求完成
  ↓
计算成本 → 写入 session_turns.cost_usd      (路径A)
  ↓
计算积分 → 写入 credit_ledger.amount        (路径B)
```

**问题**:
- 如果路径B失败（计费失败），路径A已写入 → 有成本无扣费
- 如果路径A失败，路径B可能已执行 → 有扣费无成本
- 关键等式 `Σcost_usd = Σcredits * cents_per_credit` 不成立

### 影响范围

```sql
-- 检查当前不一致数据规模
SELECT 
    COUNT(*) AS total_requests,
    COUNT(*) FILTER (WHERE cost_usd IS NOT NULL AND credits_charged IS NULL) AS cost_but_no_credits,
    COUNT(*) FILTER (WHERE cost_usd IS NULL AND credits_charged IS NOT NULL) AS credits_but_no_cost,
    SUM(cost_usd) FILTER (WHERE credits_charged IS NULL) AS lost_revenue_usd
FROM session_turns
WHERE created_at > NOW() - INTERVAL '30 days';
```

**实测影响**（待验证）:
- 预估：每月0.1-1%的请求存在不一致
- 金额影响：待量化

---

## 💡 方案对比

### 方案A: 数据库事务包裹（强一致性）

#### 设计思路

将成本记录和积分扣费放在**同一个数据库事务**中：

```go
// domains/streaming/handler.go

func (h *Handler) executeBillingTransaction(ctx context.Context, input BillingTxInput) error {
    // 开启事务
    tx, err := h.pool.Begin(ctx)
    if err != nil {
        return err
    }
    defer tx.Rollback(ctx)
    
    // 1. 扣减积分（写入 credit_ledger）
    charged, err := h.maasSvc.ChargeWithTx(ctx, tx, input)
    if err != nil {
        return fmt.Errorf("charge failed: %w", err)
    }
    
    // 2. 记录成本（写入 session_turns_hot）
    err = h.telemetry.WriteCostWithTx(ctx, tx, input.RequestID, input.CostUSD, charged)
    if err != nil {
        return fmt.Errorf("write cost failed: %w", err)
    }
    
    // 3. 提交事务
    if err := tx.Commit(ctx); err != nil {
        return fmt.Errorf("commit failed: %w", err)
    }
    
    return nil
}
```

#### 实现细节

**需要改动的模块**:

1. **maas/service.go**: 添加 `ChargeWithTx` 方法
   ```go
   func (s *Service) ChargeWithTx(ctx context.Context, tx pgx.Tx, 
       tenantID, ownerUser, requestID, model string, usage TokenUsage, multiplier float64) (int64, error) {
       // 使用 tx 而不是 s.pool 执行SQL
       _, err := tx.Exec(ctx, `
           INSERT INTO credit_ledger (tenant_id, owner_user, entry_type, amount, ref_type, ref_id, ...)
           VALUES ($1, $2, 'consume', $3, 'request', $4, ...)
       `, tenantID, ownerUser, -credits, requestID, ...)
       
       if err != nil {
           return 0, err
       }
       
       // 更新钱包余额
       _, err = tx.Exec(ctx, `
           UPDATE tenant_credit_wallets
           SET balance_credits = balance_credits - $1
           WHERE tenant_id = $2
       `, credits, tenantID)
       
       return credits, err
   }
   ```

2. **telemetry client**: 添加 `WriteCostWithTx` 方法
   ```go
   func (c *Client) WriteCostWithTx(ctx context.Context, tx pgx.Tx, 
       requestID string, costUSD float64, creditsCharged int64) error {
       _, err := tx.Exec(ctx, `
           UPDATE session_turns_hot
           SET cost_usd = $1, credits_charged = $2, updated_at = NOW()
           WHERE request_id = $3
       `, costUSD, creditsCharged, requestID)
       
       return err
   }
   ```

3. **handler.go**: 在请求完成后调用
   ```go
   // 在现有的成本计算之后
   if h.maasSvc.Enabled() && (pt > 0 || ct > 0) {
       err := h.executeBillingTransaction(ctx, BillingTxInput{
           RequestID:   evt.RequestID,
           TenantID:    keyInfo.TenantID,
           OwnerUser:   keyOwner,
           Model:       canonical,
           Usage:       usage,
           CostUSD:     reqLog.CostUSD,
           Multiplier:  multiplier,
       })
       
       if err != nil {
           slog.Error("billing transaction failed", 
               "request_id", evt.RequestID, 
               "error", err)
           
           // 根据P0-1的策略决定是否阻断
           // 如果请求已完成，标记为 pending_charge
           reqLog.BillingStatus = pointer.String("pending_charge")
           reqLog.BillingError = pointer.String(err.Error())
       }
   }
   ```

#### 优点

✅ **强一致性**: 要么都成功，要么都失败  
✅ **实现简单**: 传统的ACID事务，易于理解  
✅ **可靠性高**: 数据库保证原子性

#### 缺点

❌ **性能影响**: 
- 增加事务开销（2-3次网络往返）
- 锁定时间增加（影响并发）
- 预估延迟增加：+5-10ms

❌ **架构侵入性**: 
- 需要改动telemetry client（可能是独立服务）
- 需要在handler层管理事务（违反分层原则）

❌ **扩展性问题**:
- 如果telemetry和billing在不同数据库，无法使用单一事务
- 未来微服务拆分时需要重构

#### 适用场景

- 单体架构，telemetry和billing在同一数据库
- 对数据一致性要求极高
- 可接受5-10ms的延迟增加

---

### 方案B: Saga补偿事务（最终一致性）

#### 设计思路

使用**Saga模式**，每个操作独立执行，失败时通过**补偿操作**回滚：

```
1. 扣减积分（credit_ledger） → 成功
2. 记录成本（session_turns）  → 失败
3. 执行补偿：退还积分
```

#### 实现细节

**补偿表设计**:
```sql
CREATE TABLE billing_compensations (
    id bigserial PRIMARY KEY,
    request_id text NOT NULL,
    action text NOT NULL, -- charge / record_cost / rollback_charge / rollback_cost
    status text DEFAULT 'pending', -- pending / completed / failed
    payload jsonb,
    attempts int DEFAULT 0,
    created_at timestamptz DEFAULT NOW(),
    executed_at timestamptz
);
```

**核心逻辑**:
```go
// domains/streaming/handler.go

func (h *Handler) executeBillingSaga(ctx context.Context, input BillingTxInput) error {
    sagaID := uuid.New().String()
    
    // 步骤1: 扣减积分
    charged, err := h.maasSvc.Charge(ctx, input)
    if err != nil {
        return fmt.Errorf("step1 (charge) failed: %w", err)
    }
    
    // 记录步骤1成功
    _ = h.recordSagaStep(ctx, sagaID, "charge", map[string]interface{}{
        "request_id": input.RequestID,
        "charged": charged,
    })
    
    // 步骤2: 记录成本
    err = h.telemetry.WriteCost(ctx, input.RequestID, input.CostUSD, charged)
    if err != nil {
        // 步骤2失败：补偿步骤1
        slog.Error("step2 (record_cost) failed, compensating",
            "saga_id", sagaID,
            "error", err)
        
        compensateErr := h.maasSvc.RefundCharge(ctx, input.TenantID, input.RequestID, charged, "saga_compensation")
        if compensateErr != nil {
            // 补偿失败：记录到补偿队列
            _ = h.enqueueCompensation(ctx, sagaID, "rollback_charge", map[string]interface{}{
                "tenant_id": input.TenantID,
                "request_id": input.RequestID,
                "amount": charged,
            })
        }
        
        return fmt.Errorf("step2 (record_cost) failed: %w", err)
    }
    
    // 记录步骤2成功
    _ = h.recordSagaStep(ctx, sagaID, "record_cost", map[string]interface{}{
        "request_id": input.RequestID,
        "cost_usd": input.CostUSD,
    })
    
    return nil
}
```

**补偿worker**:
```go
// bg/compensation_worker.go

func (w *CompensationWorker) Run(ctx context.Context) {
    ticker := time.NewTicker(1 * time.Minute)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            w.processCompensations(ctx)
        }
    }
}

func (w *CompensationWorker) processCompensations(ctx context.Context) {
    rows, _ := w.pool.Query(ctx, `
        SELECT id, action, payload
        FROM billing_compensations
        WHERE status = 'pending' AND attempts < 5
        ORDER BY created_at
        LIMIT 100
    `)
    defer rows.Close()
    
    for rows.Next() {
        // 执行补偿逻辑
        // ...
    }
}
```

#### 优点

✅ **性能优秀**: 无长事务，不影响主流程  
✅ **架构友好**: 各模块独立，易于微服务拆分  
✅ **容错能力强**: 失败可重试，最终一致

#### 缺点

❌ **实现复杂**: 需要补偿表、worker、重试机制  
❌ **最终一致**: 短时间内数据可能不一致  
❌ **补偿失败**: 需要人工介入的兜底机制  
❌ **调试困难**: 分布式事务问题难以排查

#### 适用场景

- 微服务架构，跨数据库操作
- 对延迟敏感，不能接受事务开销
- 可接受短时间（秒级）的数据不一致

---

### 方案C: 在P0-1中一并解决（推荐）

#### 设计思路

利用P0-1的预检基础设施，**在请求完成后立即计算并写入积分**，与成本记录在同一代码路径：

```go
// domains/streaming/handler.go
// 在现有的成本计算之后（L8065）

if reqLog.PromptTokens != nil || reqLog.CompletionTokens != nil {
    // 1. 计算USD成本（已有）
    reqLog.CostUSD, reqLog.CostDisplay, reqLog.CostCurrency = AssignRequestCost(...)
    
    // ★ 2. 立即计算积分（新增）
    if h.maasSvc != nil && h.maasSvc.Enabled() && keyInfo != nil {
        credits, err := h.calculateCredits(ctx, keyInfo.TenantID, canonical, 
            reqLog.PromptTokens, reqLog.CompletionTokens,
            reqLog.CacheReadTokens, reqLog.CacheWriteTokens)
        
        if err == nil {
            reqLog.CreditsCharged = &credits
        } else {
            slog.Warn("failed to calculate credits", 
                "request_id", evt.RequestID, 
                "error", err)
        }
    }
}

// 3. 写入telemetry（统一写入cost_usd和credits_charged）
h.telemetryClient.EmitRequestLog(reqLog)

// 4. 异步扣减积分（从钱包）
if reqLog.CreditsCharged != nil && *reqLog.CreditsCharged > 0 {
    go h.deductCreditsAsync(context.Background(), keyInfo.TenantID, evt.RequestID, *reqLog.CreditsCharged)
}
```

#### 实现细节

**核心改动**:

1. **计算积分函数**（新增）:
   ```go
   // domains/streaming/handler.go
   
   func (h *Handler) calculateCredits(ctx context.Context, tenantID, model string,
       promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens *int64) (int64, error) {
       
       // 查询租户费率（优先tenant_model_rates，回退到model_credit_rates）
       rate, err := h.maasSvc.GetTenantModelRate(ctx, tenantID, model)
       if err != nil {
           return 0, err
       }
       
       // 计算积分
       var credits int64
       if promptTokens != nil {
           credits += *promptTokens * rate.In / 1_000_000
       }
       if completionTokens != nil {
           credits += *completionTokens * rate.Out / 1_000_000
       }
       if cacheReadTokens != nil && rate.CacheIn > 0 {
           credits += *cacheReadTokens * rate.CacheIn / 1_000_000
       }
       if cacheWriteTokens != nil && rate.CacheOut > 0 {
           credits += *cacheWriteTokens * rate.CacheOut / 1_000_000
       }
       
       // 应用折扣
       if rate.Discount > 0 {
           credits = credits * (100 - rate.Discount) / 100
       }
       
       return credits, nil
   }
   ```

2. **异步扣减积分**（新增）:
   ```go
   // domains/streaming/handler.go
   
   func (h *Handler) deductCreditsAsync(ctx context.Context, tenantID, requestID string, credits int64) {
       err := h.maasSvc.DeductCredits(ctx, tenantID, requestID, credits)
       if err != nil {
           slog.Error("failed to deduct credits from wallet",
               "request_id", requestID,
               "tenant_id", tenantID,
               "credits", credits,
               "error", err)
           
           // 写入补扣费队列
           _ = h.enqueueCreditDeduction(ctx, tenantID, requestID, credits)
       }
   }
   ```

3. **maas/service.go**: 新增钱包扣减方法
   ```go
   func (s *Service) DeductCredits(ctx context.Context, tenantID, requestID string, credits int64) error {
       // 1. 写入 credit_ledger
       _, err := s.pool.Exec(ctx, `
           INSERT INTO credit_ledger 
           (tenant_id, entry_type, amount, ref_type, ref_id, balance_after, created_at)
           SELECT $1, 'consume', $2, 'request', $3, 
                  w.balance_credits - $2, NOW()
           FROM tenant_credit_wallets w
           WHERE w.tenant_id = $1
       `, tenantID, -credits, requestID)
       
       if err != nil {
           return err
       }
       
       // 2. 更新钱包余额
       _, err = s.pool.Exec(ctx, `
           UPDATE tenant_credit_wallets
           SET balance_credits = balance_credits - $1
           WHERE tenant_id = $2
       `, credits, tenantID)
       
       return err
   }
   ```

#### 优点

✅ **最小侵入**: 只改handler.go一处，不涉及telemetry  
✅ **性能优秀**: 计算积分是内存操作，扣减钱包异步执行  
✅ **一致性好**: cost_usd和credits_charged在同一次telemetry写入  
✅ **易于测试**: 计算逻辑独立，可单元测试  
✅ **渐进修复**: 可与P0-1一起部署，风险可控

#### 缺点

❌ **异步扣减**: 钱包余额更新有延迟（秒级）  
❌ **需要补偿**: 异步扣减失败需要补扣费队列（可复用P0-1的队列）  
❌ **不是强一致**: credits_charged记录了，但钱包可能还没扣

#### 风险缓解

1. **异步扣减失败**: 写入补扣费队列（复用P0-1的 `billing_retry_worker`）
2. **钱包余额延迟**: 在P0-1的预检中使用"预估成本"，不影响后续请求
3. **数据一致性**: 每日对账job验证 `Σcredits_charged = Σcredit_ledger.amount`

#### 适用场景

- 当前架构（单体），不希望大规模重构
- 对延迟敏感
- 可接受秒级的最终一致性

---

## 📊 方案对比总结

| 维度 | 方案A（事务） | 方案B（Saga） | 方案C（P0-1扩展）|
|------|--------------|--------------|-----------------|
| **一致性** | 强一致（ACID） | 最终一致 | 最终一致 |
| **性能影响** | +5-10ms | +0ms | +1-2ms |
| **实现复杂度** | 中 | 高 | 低 |
| **架构侵入性** | 高 | 中 | 低 |
| **可扩展性** | 差（单数据库） | 好（跨库/服务） | 中 |
| **调试难度** | 低 | 高 | 低 |
| **失败恢复** | 自动回滚 | 补偿机制 | 补扣费队列 |
| **部署风险** | 中 | 高 | 低 |

---

## 🎯 推荐方案

### 推荐：方案C（在P0-1中一并解决）

**理由**:

1. **实现成本最低**: 
   - 只需在handler.go添加计算积分逻辑
   - 复用P0-1的补扣费队列
   - 无需改动telemetry client

2. **性能影响最小**:
   - 计算积分是内存操作（+1ms）
   - 钱包扣减异步执行（不阻塞主流程）

3. **风险可控**:
   - 与P0-1一起部署，渐进式修复
   - 失败有补偿机制（补扣费队列）

4. **符合现状**:
   - 单体架构，不需要分布式事务
   - 可接受秒级的最终一致性

### 不推荐方案A的原因

- 需要改动telemetry client（可能是独立服务）
- 性能影响较大（+5-10ms）
- 未来微服务拆分时需要重构

### 不推荐方案B的原因

- 实现复杂度高，周期长（需要2周+）
- 调试困难，增加运维负担
- 当前架构不需要这么复杂的方案

---

## 📋 实施计划（方案C）

### 阶段1: 计算积分逻辑（1天）

**步骤1**: 实现 `calculateCredits` 函数
- 查询租户费率
- 计算积分（含缓存token）
- 应用折扣

**步骤2**: 在handler中调用
- 在成本计算之后立即计算积分
- 写入 `reqLog.CreditsCharged`

**步骤3**: 单元测试
- 测试不同费率
- 测试折扣计算
- 测试缓存token

### 阶段2: 异步扣减钱包（1天）

**步骤1**: 实现 `DeductCredits` 方法
- 写入 `credit_ledger`
- 更新钱包余额

**步骤2**: 实现 `deductCreditsAsync`
- 异步调用（goroutine）
- 失败写入补扣费队列

**步骤3**: 复用P0-1的补扣费worker
- 扩展 `BillingRetryWorker` 处理钱包扣减失败

### 阶段3: 验证与部署（2天）

**步骤1**: 单元测试
- 测试计算逻辑
- 测试异步扣减
- 测试失败重试

**步骤2**: 集成测试
- 验证cost_usd和credits_charged同时写入
- 验证钱包余额正确扣减
- 验证补扣费队列正常工作

**步骤3**: 灰度发布
- Dev环境：观察3天
- Staging环境：观察7天
- Production环境：10% → 50% → 100%

### 总计时间

**开发**: 2天  
**测试**: 2天  
**灰度**: 10天  
**总计**: 约2周

---

## 🔍 监控指标

部署后需监控的指标：

```sql
-- 1. cost_usd与credits_charged一致性
SELECT 
    DATE(created_at) AS date,
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE cost_usd IS NOT NULL AND credits_charged IS NULL) AS missing_credits,
    COUNT(*) FILTER (WHERE cost_usd IS NULL AND credits_charged IS NOT NULL) AS missing_cost,
    ROUND(100.0 * COUNT(*) FILTER (WHERE credits_charged IS NOT NULL) / COUNT(*), 2) AS coverage_pct
FROM session_turns
WHERE created_at > NOW() - INTERVAL '7 days'
GROUP BY DATE(created_at)
ORDER BY date DESC;

-- 预期：coverage_pct > 99%

-- 2. 异步扣减失败率
SELECT 
    DATE(created_at) AS date,
    COUNT(*) AS total_deductions,
    COUNT(*) FILTER (WHERE status = 'failed') AS failed_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE status = 'failed') / COUNT(*), 2) AS failure_rate
FROM billing_compensations
WHERE action = 'deduct_credits'
  AND created_at > NOW() - INTERVAL '7 days'
GROUP BY DATE(created_at)
ORDER BY date DESC;

-- 预期：failure_rate < 1%

-- 3. 钱包余额一致性（每日对账）
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

## 📞 下一步

1. ✅ 方案评估完成
2. ⏳ 等待架构师/技术负责人审批
3. ⏳ 审批通过后开始实施

**审批要点**:
- 是否接受秒级的最终一致性？
- 是否同意在P0-1基础上扩展？
- 是否需要更详细的设计文档？

---

**评估人**: AI Assistant  
**评估日期**: 2026-11-26  
**推荐方案**: 方案C（在P0-1中一并解决）  
**状态**: ✅ 评估完成，待审批
