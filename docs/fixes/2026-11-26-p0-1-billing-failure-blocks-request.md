# P0-1: 计费失败阻断请求修复方案

## 问题描述

**严重程度**: 🔴 P0（生产级，收入直接流失）

**位置**: `domains/streaming/handler.go:8116-8128`

**现状**:
```go
charged, stampedMultiplier, err := h.maasSvc.ChargeRequestMultimodalWithMultiplier(...)
if err != nil {
    slog.Warn("maas charge failed", "request_id", evt.RequestID, "tenant_id", keyInfo.TenantID, "error", err)
    // ⚠️ 只记日志，请求继续成功返回 200
}
```

**影响**:
- 用户成功调用API（收到200响应和完整结果）
- 但系统未能扣费（`credit_ledger`无记录）
- **收入直接流失且无法追溯**
- 无法量化损失规模（需查询数据库）

## 修复方案对比

### 方案A: 强阻断（推荐用于付费客户）

**行为**: 计费失败时立即阻断请求，返回 402 Payment Required

**优点**:
- ✅ 收入不流失，逻辑清晰
- ✅ 强制保证"用了就付费"
- ✅ 实现简单，无需额外队列

**缺点**:
- ❌ 计费系统故障时会影响服务可用性
- ❌ 用户体验可能受影响（但合理）

**适用场景**:
- 付费客户（已购买套餐/充值）
- 生产环境正式调用
- 对收入保护要求高的场景

### 方案B: 软标记（推荐用于试用客户）

**行为**: 计费失败时标记为待补扣费，请求成功返回，后台异步补扣

**优点**:
- ✅ 服务可用性不受计费系统影响
- ✅ 用户体验更好（尤其试用期）
- ✅ 灵活性高

**缺点**:
- ❌ 实现复杂，需要补扣费队列
- ❌ 需要财务追踪 `pending_charge` 状态
- ❌ 存在永久欠费风险（队列消费失败）

**适用场景**:
- 试用期客户
- 免费额度内的调用
- 对可用性要求极高的场景

## 推荐方案：混合模式

根据租户类型动态选择：

```go
// 配置决定计费失败行为
type BillingFailurePolicy string

const (
    PolicyBlock  BillingFailurePolicy = "block"   // 强阻断
    PolicyQueue  BillingFailurePolicy = "queue"   // 软标记
    PolicyWarn   BillingFailurePolicy = "warn"    // 仅告警（当前行为，不推荐）
)

// 租户维度配置
// ALTER TABLE tenants ADD COLUMN billing_failure_policy text DEFAULT 'block';
```

## 实施计划

### 阶段1: 数据库迁移（无业务逻辑变更）

**新增字段用于追踪计费状态**:

```sql
-- sql/migrations/XXX_billing_status_tracking.sql

-- 1. session_turns 添加计费状态字段
ALTER TABLE session_turns 
  ADD COLUMN billing_status text DEFAULT 'charged',
  ADD COLUMN billing_error text,
  ADD COLUMN billing_retry_count int DEFAULT 0;

COMMENT ON COLUMN session_turns.billing_status IS 
  'charged: 计费成功 | pending_charge: 待补扣费 | failed: 计费失败';

-- 2. 索引：快速查询待补扣费的请求
CREATE INDEX idx_session_turns_billing_pending 
  ON session_turns(billing_status, created_at) 
  WHERE billing_status = 'pending_charge';

-- 3. 索引：快速查询计费失败的请求
CREATE INDEX idx_session_turns_billing_failed 
  ON session_turns(billing_status, created_at) 
  WHERE billing_status = 'failed';

-- 4. session_turns_hot 同步添加（热表）
ALTER TABLE session_turns_hot 
  ADD COLUMN billing_status text DEFAULT 'charged',
  ADD COLUMN billing_error text,
  ADD COLUMN billing_retry_count int DEFAULT 0;

-- 5. 租户计费失败策略配置
ALTER TABLE tenants 
  ADD COLUMN billing_failure_policy text DEFAULT 'block'
  CHECK (billing_failure_policy IN ('block', 'queue', 'warn'));

COMMENT ON COLUMN tenants.billing_failure_policy IS 
  'block: 计费失败立即阻断 | queue: 软标记后台补扣 | warn: 仅告警（不推荐）';
```

**down 脚本**:
```sql
-- down
ALTER TABLE session_turns 
  DROP COLUMN IF EXISTS billing_status,
  DROP COLUMN IF EXISTS billing_error,
  DROP COLUMN IF EXISTS billing_retry_count;

ALTER TABLE session_turns_hot 
  DROP COLUMN IF EXISTS billing_status,
  DROP COLUMN IF EXISTS billing_error,
  DROP COLUMN IF EXISTS billing_retry_count;

ALTER TABLE tenants 
  DROP COLUMN IF EXISTS billing_failure_policy;

DROP INDEX IF EXISTS idx_session_turns_billing_pending;
DROP INDEX IF EXISTS idx_session_turns_billing_failed;
```

### 阶段2: 代码修复（方案A：强阻断）

**修改位置**: `domains/streaming/handler.go:8116-8130`

```go
// 当前代码（有问题）
charged, stampedMultiplier, err := h.maasSvc.ChargeRequestMultimodalWithMultiplier(...)
if err == nil && charged > 0 {
    reqLog.CreditsCharged = &charged
    reqLog.RateMultiplier = &stampedMultiplier
} else if err != nil {
    slog.Warn("maas charge failed", "request_id", evt.RequestID, "tenant_id", keyInfo.TenantID, "error", err)
}

// 修复后（方案A：强阻断）
charged, stampedMultiplier, err := h.maasSvc.ChargeRequestMultimodalWithMultiplier(chargeCtx, 
    keyInfo.TenantID, keyOwner, evt.RequestID, canonical, 
    maas.TokenUsage{
        PromptTokens:     pt,
        CompletionTokens: ct,
        CacheReadTokens:  crt,
        CacheWriteTokens: cwt,
    }, multiplier)
chargeCancel()

if err != nil {
    // ★ 修复：计费失败立即阻断
    slog.Error("maas charge failed, aborting request",
        "request_id", evt.RequestID,
        "tenant_id", keyInfo.TenantID,
        "model", canonical,
        "prompt_tokens", pt,
        "completion_tokens", ct,
        "error", err)
    
    // 标记计费失败状态
    reqLog.BillingStatus = pointer.String("failed")
    reqLog.BillingError = pointer.String(err.Error())
    reqLog.GatewayErrCode = pointer.String("billing_failed")
    reqLog.StatusCode = pointer.Int(402) // Payment Required
    
    // ⚠️ 阻断响应：不写入 responseBody，返回计费失败错误
    // 注意：此时流式响应可能已部分发送，需要在更早的位置检查
    return fmt.Errorf("billing failed: %w", err)
}

// 计费成功：记录积分和倍率
if charged > 0 {
    reqLog.CreditsCharged = &charged
    reqLog.RateMultiplier = &stampedMultiplier
    reqLog.BillingStatus = pointer.String("charged") // 显式标记成功
}
```

**关键问题：流式响应已发送**

上述修复有一个致命问题：在 `handleStreamResult` 被调用时，**流式响应可能已经部分发送给客户端**（SSE已开始推送）。此时返回错误已无法阻止客户端收到部分数据。

**正确的修复位置：在流开始前预检**

```go
// domains/streaming/handler.go
// 在 ServeHTTP 或更早的位置添加预检

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // ... 现有逻辑 ...
    
    // ★ 新增：计费预检（在流开始前）
    if h.maasSvc.Enabled() && keyInfo != nil {
        // 检查租户余额是否足够（预估成本）
        estimatedCost := h.estimateRequestCost(r, keyInfo)
        canCharge, err := h.maasSvc.PreCheckBalance(ctx, keyInfo.TenantID, estimatedCost)
        
        if !canCharge || err != nil {
            slog.Warn("billing pre-check failed",
                "request_id", requestID,
                "tenant_id", keyInfo.TenantID,
                "estimated_cost", estimatedCost,
                "error", err)
            
            // 获取租户计费失败策略
            policy := h.getTenantBillingPolicy(ctx, keyInfo.TenantID)
            
            if policy == PolicyBlock {
                // 强阻断：返回 402，不启动流
                http.Error(w, "Insufficient balance or billing service unavailable", http.StatusPaymentRequired)
                return
            }
            // 否则允许继续（软标记模式）
        }
    }
    
    // ... 继续处理请求 ...
}
```

### 阶段3: 补扣费队列（方案B支持）

如果采用软标记模式，需要补扣费机制：

```go
// bg/billing_retry_worker.go

type BillingRetryWorker struct {
    pool     *pgxpool.Pool
    maasSvc  *maas.Service
    interval time.Duration // 默认每5分钟
}

func (w *BillingRetryWorker) Run(ctx context.Context) {
    ticker := time.NewTicker(w.interval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            w.retryPendingCharges(ctx)
        }
    }
}

func (w *BillingRetryWorker) retryPendingCharges(ctx context.Context) {
    // 1. 查询 pending_charge 状态的请求
    rows, err := w.pool.Query(ctx, `
        SELECT request_id, tenant_id, cost_usd, 
               prompt_tokens, completion_tokens,
               cache_read_tokens, cache_write_tokens,
               model, billing_retry_count
        FROM session_turns
        WHERE billing_status = 'pending_charge'
          AND billing_retry_count < 5
          AND created_at > NOW() - INTERVAL '7 days'
        ORDER BY created_at
        LIMIT 100
    `)
    if err != nil {
        slog.Error("failed to query pending charges", "error", err)
        return
    }
    defer rows.Close()
    
    // 2. 逐条重试扣费
    for rows.Next() {
        var req struct {
            RequestID        string
            TenantID         string
            CostUSD          float64
            PromptTokens     int64
            CompletionTokens int64
            CacheReadTokens  int64
            CacheWriteTokens int64
            Model            string
            RetryCount       int
        }
        if err := rows.Scan(&req.RequestID, &req.TenantID, ...); err != nil {
            continue
        }
        
        // 3. 重试扣费
        charged, _, err := w.maasSvc.ChargeRequestMultimodalWithMultiplier(ctx,
            req.TenantID, "", req.RequestID, req.Model,
            maas.TokenUsage{
                PromptTokens:     req.PromptTokens,
                CompletionTokens: req.CompletionTokens,
                CacheReadTokens:  req.CacheReadTokens,
                CacheWriteTokens: req.CacheWriteTokens,
            }, 1.0)
        
        if err == nil && charged > 0 {
            // 4. 扣费成功：更新状态
            _, _ = w.pool.Exec(ctx, `
                UPDATE session_turns
                SET billing_status = 'charged',
                    credits_charged = $1,
                    billing_error = NULL
                WHERE request_id = $2
            `, charged, req.RequestID)
            
            slog.Info("billing retry succeeded",
                "request_id", req.RequestID,
                "tenant_id", req.TenantID,
                "charged", charged)
        } else {
            // 5. 扣费失败：增加重试计数
            _, _ = w.pool.Exec(ctx, `
                UPDATE session_turns
                SET billing_retry_count = billing_retry_count + 1,
                    billing_error = $1,
                    billing_status = CASE 
                        WHEN billing_retry_count + 1 >= 5 THEN 'failed'
                        ELSE 'pending_charge'
                    END
                WHERE request_id = $2
            `, err.Error(), req.RequestID)
            
            slog.Warn("billing retry failed",
                "request_id", req.RequestID,
                "tenant_id", req.TenantID,
                "retry_count", req.RetryCount+1,
                "error", err)
        }
    }
}
```

## 测试计划

### 单元测试

```go
// domains/streaming/handler_billing_test.go

func TestBillingFailureBlocksRequest(t *testing.T) {
    // 模拟计费失败
    mockMaaS := &MockMaaSService{
        ChargeFunc: func(...) (int64, float64, error) {
            return 0, 0, errors.New("insufficient balance")
        },
    }
    
    handler := &Handler{maasSvc: mockMaaS}
    
    // 发起请求
    resp := handler.ServeHTTP(...)
    
    // 断言：返回 402
    assert.Equal(t, 402, resp.StatusCode)
    assert.Contains(t, resp.Body, "billing failed")
}

func TestBillingSuccessAllowsRequest(t *testing.T) {
    mockMaaS := &MockMaaSService{
        ChargeFunc: func(...) (int64, float64, error) {
            return 100, 1.0, nil
        },
    }
    
    handler := &Handler{maasSvc: mockMaaS}
    
    resp := handler.ServeHTTP(...)
    
    // 断言：返回 200
    assert.Equal(t, 200, resp.StatusCode)
}
```

### E2E测试

1. **场景1**: 正常计费流程
   - 发起请求 → 计费成功 → 返回200 → `billing_status='charged'`

2. **场景2**: 余额不足（方案A）
   - 发起请求 → 计费失败 → 返回402 → `billing_status='failed'`

3. **场景3**: 余额不足（方案B）
   - 发起请求 → 计费失败 → 返回200 → `billing_status='pending_charge'`
   - 补扣费worker运行 → 重试成功 → `billing_status='charged'`

## 部署计划

### 第1天：数据库迁移
- ✅ 在dev环境应用迁移
- ✅ 验证字段添加成功
- ✅ 验证索引创建成功

### 第2天：代码修复（staging）
- ✅ 部署修复代码到staging
- ✅ 运行E2E测试
- ✅ 模拟计费失败场景

### 第3天：灰度发布（production）
- ✅ 部署到10%流量
- ✅ 监控指标：
  - `billing_failure_rate`: 计费失败率（目标 < 0.1%）
  - `billing_402_count`: 返回402的请求数
  - `billing_pending_count`: 待补扣费请求数
- ✅ 无异常后扩大到100%

### 第4天：全量发布 + 监控
- ✅ 100%流量
- ✅ 持续监控7天
- ✅ 生成影响报告

## 监控指标

```sql
-- 每日计费失败率
SELECT 
    DATE(created_at) AS date,
    COUNT(*) AS total_requests,
    COUNT(*) FILTER (WHERE billing_status = 'failed') AS failed_count,
    COUNT(*) FILTER (WHERE billing_status = 'pending_charge') AS pending_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE billing_status = 'failed') / COUNT(*), 2) AS failed_pct
FROM session_turns
WHERE created_at > NOW() - INTERVAL '7 days'
GROUP BY DATE(created_at)
ORDER BY date DESC;

-- 待补扣费请求监控
SELECT 
    billing_retry_count,
    COUNT(*) AS count,
    SUM(cost_usd) AS total_cost_usd
FROM session_turns
WHERE billing_status = 'pending_charge'
GROUP BY billing_retry_count
ORDER BY billing_retry_count;
```

## 回滚计划

如果修复导致严重问题（如大量402误报）：

1. **立即回滚**：部署之前的代码版本
2. **数据库回滚**：执行 down 迁移（删除新增字段）
3. **恢复监控**：确认计费失败率恢复到修复前水平

## 风险评估

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| 计费系统故障导致大量402 | 中 | 高 | 灰度发布 + 快速回滚 |
| 预检逻辑过于严格（误拒） | 中 | 中 | 调整预检阈值 + 软标记模式 |
| 补扣费队列消费失败 | 低 | 中 | 告警 + 人工介入 |
| 性能下降（预检耗时） | 低 | 低 | 缓存租户余额 + 异步预检 |

## 附录：影响范围估算

运行以下SQL评估当前损失规模：

```sql
-- 查询：近7天有成本但无积分的请求
SELECT 
    DATE(created_at) AS date,
    COUNT(*) AS missing_charge_count,
    SUM(cost_usd) AS lost_revenue_usd,
    AVG(cost_usd) AS avg_cost_per_request
FROM session_turns
WHERE created_at > NOW() - INTERVAL '7 days'
  AND cost_usd > 0
  AND credits_charged IS NULL
GROUP BY DATE(created_at)
ORDER BY date DESC;
```

**预期结果**：
- 如果 `missing_charge_count > 0`，说明问题真实存在
- `lost_revenue_usd` 为直接收入损失金额

## 决策点

**需要owner决策**：

1. 采用方案A（强阻断）还是方案B（软标记）？
   - 建议：付费客户用A，试用客户用B（混合模式）

2. 计费失败率阈值多少视为正常？
   - 建议：< 0.1%

3. 补扣费重试次数和间隔？
   - 建议：5次，间隔5分钟

4. 是否需要回填历史数据的 `billing_status`？
   - 建议：回填近30天数据用于统计分析
