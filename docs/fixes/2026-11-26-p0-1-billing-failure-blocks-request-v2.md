# P0-1: 计费失败阻断请求修复方案 v2

## 核心原则（2026-11-26 owner决策）

### 1. 余额不足双策略
- **阻断模式（block）**: 余额不足时立即返回402，拒绝服务
- **警告模式（warn）**: 余额不足时仅记录警告，允许继续使用（进入信用额度）

### 2. 信用额度机制
- 每个租户有**信用额度**（credit_limit）
- 当余额为负但在信用额度内时，允许继续使用
- 超过信用额度才真正阻断

### 3. 策略配置
- **租户级别**配置：每个租户可单独设置策略
- **全局默认**：新租户默认使用 `warn` 模式 + 1000积分信用额度

---

## 完整业务逻辑

### 判定流程

```
请求进入
  ↓
查询租户余额 & 信用额度
  ↓
余额 >= 0？
  ├─ YES → 允许使用（正常扣费）
  └─ NO → 余额 + 信用额度 >= 预估成本？
           ├─ YES → 检查策略
           │        ├─ warn模式 → 允许使用（记录警告，进入负债）
           │        └─ block模式 → 返回402（硬阻断）
           └─ NO → 超过信用额度 → 强制返回402（无论策略）
```

### 示例场景

**场景1: 余额充足**
- 租户A：余额=5000积分，信用额度=1000
- 请求预估成本=100积分
- 判定：5000 >= 100 → ✅ 允许使用

**场景2: 余额不足，但在信用额度内（warn模式）**
- 租户B：余额=-200积分，信用额度=1000，策略=warn
- 请求预估成本=100积分
- 判定：-200 + 1000 = 800 >= 100 → ✅ 允许使用（记录警告）
- 扣费后：余额变为-300积分（进入负债）

**场景3: 余额不足，但在信用额度内（block模式）**
- 租户C：余额=-200积分，信用额度=1000，策略=block
- 请求预估成本=100积分
- 判定：余额<0 且策略=block → ❌ 返回402（即使在信用额度内）

**场景4: 超过信用额度**
- 租户D：余额=-1200积分，信用额度=1000，策略=warn
- 请求预估成本=100积分
- 判定：-1200 + 1000 = -200 < 100 → ❌ 强制返回402（超过信用额度）

---

## 数据库设计

### 迁移：1000_billing_status_and_credit_limit.sql

```sql
-- ============================================================
-- 迁移 1000: 计费状态追踪 + 信用额度机制
-- 目的：修复 P0-1（计费失败不阻断请求）
-- 日期：2026-11-26
-- ============================================================

-- ──────────────────────────────────────────────────────────
-- Part 1: session_turns 计费状态追踪
-- ──────────────────────────────────────────────────────────

-- 1.1 添加计费状态字段
ALTER TABLE session_turns 
  ADD COLUMN billing_status text DEFAULT 'charged',
  ADD COLUMN billing_error text,
  ADD COLUMN billing_retry_count int DEFAULT 0,
  ADD COLUMN billing_policy_applied text; -- 记录本次使用的策略（block/warn）

COMMENT ON COLUMN session_turns.billing_status IS 
  'charged: 计费成功 | pending_charge: 待补扣费 | failed: 计费失败（已超重试上限）| skipped: 跳过计费（免费额度等）';

COMMENT ON COLUMN session_turns.billing_policy_applied IS 
  '记录本次请求应用的策略：block（阻断）/ warn（警告）/ skipped（跳过）';

-- 1.2 索引：快速查询待补扣费的请求
CREATE INDEX idx_session_turns_billing_pending 
  ON session_turns(billing_status, created_at) 
  WHERE billing_status = 'pending_charge';

-- 1.3 索引：快速查询计费失败的请求
CREATE INDEX idx_session_turns_billing_failed 
  ON session_turns(billing_status, created_at) 
  WHERE billing_status = 'failed';

-- 1.4 session_turns_hot 同步添加（热表）
ALTER TABLE session_turns_hot 
  ADD COLUMN billing_status text DEFAULT 'charged',
  ADD COLUMN billing_error text,
  ADD COLUMN billing_retry_count int DEFAULT 0,
  ADD COLUMN billing_policy_applied text;

-- ──────────────────────────────────────────────────────────
-- Part 2: 租户信用额度 + 策略配置
-- ──────────────────────────────────────────────────────────

-- 2.1 tenant_credit_wallets 添加信用额度字段
ALTER TABLE tenant_credit_wallets 
  ADD COLUMN credit_limit_credits bigint DEFAULT 1000,
  ADD COLUMN credit_policy text DEFAULT 'warn' 
    CHECK (credit_policy IN ('block', 'warn'));

COMMENT ON COLUMN tenant_credit_wallets.credit_limit_credits IS 
  '信用额度（积分）。余额为负时，可透支到此额度内。默认1000积分。';

COMMENT ON COLUMN tenant_credit_wallets.credit_policy IS 
  'warn: 余额不足时警告但允许使用（进入负债）| block: 余额不足立即阻断（即使在信用额度内）';

-- 2.2 索引：快速查询负债租户
CREATE INDEX idx_tenant_credit_wallets_negative_balance 
  ON tenant_credit_wallets(balance_credits) 
  WHERE balance_credits < 0;

-- ──────────────────────────────────────────────────────────
-- Part 3: 计费预检日志表（可选，用于审计）
-- ──────────────────────────────────────────────────────────

CREATE TABLE billing_precheck_log (
    id bigserial PRIMARY KEY,
    request_id text NOT NULL,
    tenant_id text NOT NULL,
    balance_before_credits bigint NOT NULL,
    credit_limit_credits bigint NOT NULL,
    estimated_cost_credits bigint NOT NULL,
    policy text NOT NULL, -- block / warn
    decision text NOT NULL, -- allow / reject
    reason text,
    created_at timestamptz DEFAULT NOW()
);

CREATE INDEX idx_billing_precheck_log_tenant 
  ON billing_precheck_log(tenant_id, created_at DESC);

CREATE INDEX idx_billing_precheck_log_decision 
  ON billing_precheck_log(decision, created_at DESC) 
  WHERE decision = 'reject';

COMMENT ON TABLE billing_precheck_log IS 
  '计费预检日志：记录每次请求的余额检查结果，用于审计和分析';

-- ──────────────────────────────────────────────────────────
-- Part 4: 视图：负债租户监控
-- ──────────────────────────────────────────────────────────

CREATE VIEW v_tenants_in_debt AS
SELECT 
    w.tenant_id,
    w.balance_credits,
    w.credit_limit_credits,
    w.balance_credits + w.credit_limit_credits AS available_credits,
    w.credit_policy,
    CASE 
        WHEN w.balance_credits + w.credit_limit_credits < 0 THEN 'over_limit'
        WHEN w.balance_credits < 0 THEN 'in_debt'
        ELSE 'healthy'
    END AS debt_status,
    COUNT(st.id) FILTER (WHERE st.created_at > NOW() - INTERVAL '24 hours') AS requests_last_24h,
    SUM(st.credits_charged) FILTER (WHERE st.created_at > NOW() - INTERVAL '24 hours') AS credits_used_24h
FROM tenant_credit_wallets w
LEFT JOIN session_turns st ON st.tenant_id = w.tenant_id
WHERE w.balance_credits < 0
GROUP BY w.tenant_id, w.balance_credits, w.credit_limit_credits, w.credit_policy
ORDER BY w.balance_credits ASC;

COMMENT ON VIEW v_tenants_in_debt IS 
  '负债租户监控：显示所有余额为负的租户及其负债状况';

-- ──────────────────────────────────────────────────────────
-- Part 5: 默认值设置（为现有租户补齐字段）
-- ──────────────────────────────────────────────────────────

-- 5.1 为现有租户设置默认信用额度和策略
-- 注意：这会锁表，如果tenant_credit_wallets表很大，建议分批执行
UPDATE tenant_credit_wallets 
SET credit_limit_credits = 1000,
    credit_policy = 'warn'
WHERE credit_limit_credits IS NULL 
   OR credit_policy IS NULL;

-- 5.2 为历史数据回填 billing_status（可选，用于统计）
-- 注意：这是一个大批量更新，建议在维护窗口执行
-- 策略：有 credits_charged 的记为 'charged'，否则根据 cost_usd 判断
UPDATE session_turns 
SET billing_status = CASE 
    WHEN credits_charged IS NOT NULL THEN 'charged'
    WHEN cost_usd > 0 AND credits_charged IS NULL THEN 'pending_charge'
    ELSE 'charged'
END
WHERE billing_status IS NULL 
  AND created_at > NOW() - INTERVAL '30 days'; -- 只回填近30天

-- ──────────────────────────────────────────────────────────
-- Part 6: 触发器：自动记录余额变化（可选）
-- ──────────────────────────────────────────────────────────

-- 触发器：在余额变为负数时自动记录警告
CREATE OR REPLACE FUNCTION notify_negative_balance() 
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.balance_credits < 0 AND OLD.balance_credits >= 0 THEN
        -- 插入告警记录（需要有 alerts 表）
        RAISE NOTICE 'Tenant % balance went negative: % credits', 
                     NEW.tenant_id, NEW.balance_credits;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_notify_negative_balance
AFTER UPDATE OF balance_credits ON tenant_credit_wallets
FOR EACH ROW
WHEN (NEW.balance_credits < 0 AND OLD.balance_credits >= 0)
EXECUTE FUNCTION notify_negative_balance();

COMMENT ON TRIGGER trg_notify_negative_balance ON tenant_credit_wallets IS 
  '触发器：当租户余额首次变为负数时记录日志';
```

### down 迁移

```sql
-- ============================================================
-- 迁移 1000 回滚: 删除计费状态追踪和信用额度
-- ============================================================

-- Part 6: 删除触发器
DROP TRIGGER IF EXISTS trg_notify_negative_balance ON tenant_credit_wallets;
DROP FUNCTION IF EXISTS notify_negative_balance();

-- Part 4: 删除视图
DROP VIEW IF EXISTS v_tenants_in_debt;

-- Part 3: 删除预检日志表
DROP TABLE IF EXISTS billing_precheck_log;

-- Part 2: 删除信用额度字段
DROP INDEX IF EXISTS idx_tenant_credit_wallets_negative_balance;

ALTER TABLE tenant_credit_wallets 
  DROP COLUMN IF EXISTS credit_limit_credits,
  DROP COLUMN IF EXISTS credit_policy;

-- Part 1: 删除计费状态字段
DROP INDEX IF EXISTS idx_session_turns_billing_pending;
DROP INDEX IF EXISTS idx_session_turns_billing_failed;

ALTER TABLE session_turns 
  DROP COLUMN IF EXISTS billing_status,
  DROP COLUMN IF EXISTS billing_error,
  DROP COLUMN IF EXISTS billing_retry_count,
  DROP COLUMN IF EXISTS billing_policy_applied;

ALTER TABLE session_turns_hot 
  DROP COLUMN IF EXISTS billing_status,
  DROP COLUMN IF EXISTS billing_error,
  DROP COLUMN IF EXISTS billing_retry_count,
  DROP COLUMN IF EXISTS billing_policy_applied;
```

---

## 代码实现

### 1. 预检逻辑（核心）

```go
// maas/balance_precheck.go

package maas

import (
    "context"
    "fmt"
    "log/slog"
)

// BillingDecision 计费预检决策
type BillingDecision struct {
    Allow           bool   // 是否允许请求
    Policy          string // 应用的策略：block / warn / skipped
    Reason          string // 决策原因
    BalanceBefore   int64  // 预检前余额
    CreditLimit     int64  // 信用额度
    EstimatedCost   int64  // 预估成本
    AvailableCredit int64  // 可用额度（余额+信用额度）
}

// PreCheckBalance 预检租户余额是否足够
func (s *Service) PreCheckBalance(ctx context.Context, tenantID string, estimatedCostCredits int64) (*BillingDecision, error) {
    if !s.Enabled() {
        return &BillingDecision{Allow: true, Policy: "skipped", Reason: "maas disabled"}, nil
    }
    
    // 1. 查询租户余额、信用额度、策略
    var balance, creditLimit int64
    var policy string
    
    err := s.pool.QueryRow(ctx, `
        SELECT balance_credits, credit_limit_credits, credit_policy
        FROM tenant_credit_wallets
        WHERE tenant_id = $1
    `, tenantID).Scan(&balance, &creditLimit, &policy)
    
    if err != nil {
        slog.Warn("failed to query tenant wallet for precheck",
            "tenant_id", tenantID,
            "error", err)
        
        // 查询失败：fail-open（允许继续，记录警告）
        return &BillingDecision{
            Allow:  true,
            Policy: "warn",
            Reason: "wallet query failed, fail-open",
        }, nil
    }
    
    // 2. 计算可用额度
    availableCredit := balance + creditLimit
    
    // 3. 判定逻辑
    var decision BillingDecision
    decision.BalanceBefore = balance
    decision.CreditLimit = creditLimit
    decision.EstimatedCost = estimatedCostCredits
    decision.AvailableCredit = availableCredit
    
    if balance >= 0 {
        // 场景1: 余额充足
        decision.Allow = true
        decision.Policy = "allow"
        decision.Reason = "sufficient balance"
    } else if availableCredit >= estimatedCostCredits {
        // 场景2: 余额不足，但在信用额度内
        if policy == "block" {
            // block模式：即使在信用额度内也阻断
            decision.Allow = false
            decision.Policy = "block"
            decision.Reason = fmt.Sprintf("balance negative (policy=block), balance=%d", balance)
        } else {
            // warn模式：允许使用，进入负债
            decision.Allow = true
            decision.Policy = "warn"
            decision.Reason = fmt.Sprintf("using credit line, balance=%d, available=%d", balance, availableCredit)
        }
    } else {
        // 场景3: 超过信用额度，强制阻断
        decision.Allow = false
        decision.Policy = "block"
        decision.Reason = fmt.Sprintf("over credit limit, balance=%d, limit=%d, available=%d < estimated=%d",
            balance, creditLimit, availableCredit, estimatedCostCredits)
    }
    
    // 4. 记录预检日志（异步，不阻塞主流程）
    go s.logBillingPrecheck(context.Background(), tenantID, &decision)
    
    return &decision, nil
}

// logBillingPrecheck 记录预检日志
func (s *Service) logBillingPrecheck(ctx context.Context, tenantID string, decision *BillingDecision) {
    decisionStr := "allow"
    if !decision.Allow {
        decisionStr = "reject"
    }
    
    _, err := s.pool.Exec(ctx, `
        INSERT INTO billing_precheck_log 
        (tenant_id, balance_before_credits, credit_limit_credits, 
         estimated_cost_credits, policy, decision, reason)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
    `, tenantID, decision.BalanceBefore, decision.CreditLimit,
       decision.EstimatedCost, decision.Policy, decisionStr, decision.Reason)
    
    if err != nil {
        slog.Warn("failed to log billing precheck", "error", err)
    }
}
```

### 2. 成本预估逻辑

```go
// domains/streaming/cost_estimation.go

package streaming

import (
    "encoding/json"
    "net/http"
)

// estimateRequestCost 预估请求成本（单位：积分）
func (h *Handler) estimateRequestCost(r *http.Request, model string) int64 {
    // 1. 解析请求体，提取token数预估
    body, _ := io.ReadAll(r.Body)
    r.Body = io.NopCloser(bytes.NewReader(body)) // 恢复body供后续使用
    
    var req struct {
        Messages []struct {
            Content string `json:"content"`
        } `json:"messages"`
        MaxTokens int `json:"max_tokens"`
    }
    _ = json.Unmarshal(body, &req)
    
    // 2. 粗略估算输入token（1个字符≈0.5token，中文≈1token）
    inputChars := 0
    for _, msg := range req.Messages {
        inputChars += len(msg.Content)
    }
    estimatedInputTokens := int64(inputChars / 2)
    if estimatedInputTokens < 100 {
        estimatedInputTokens = 100 // 最小预估
    }
    
    // 3. 预估输出token（使用max_tokens或默认值）
    estimatedOutputTokens := int64(req.MaxTokens)
    if estimatedOutputTokens == 0 {
        estimatedOutputTokens = 2048 // 默认预估
    }
    
    // 4. 查询模型单价（从缓存或数据库）
    rate, err := h.maasSvc.GetModelRate(r.Context(), model)
    if err != nil || rate == nil {
        // 查询失败：使用保守估算（平均单价）
        return (estimatedInputTokens + estimatedOutputTokens) * 10 / 1000000 // 假设10积分/百万token
    }
    
    // 5. 计算预估成本
    estimatedCost := (estimatedInputTokens * rate.CreditsPerMIn +
                     estimatedOutputTokens * rate.CreditsPerMOut) / 1000000
    
    // 6. 加安全余量（+20%）
    return estimatedCost * 120 / 100
}
```

### 3. 在handler中接入预检

```go
// domains/streaming/handler.go

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // ... 现有的认证、解析逻辑 ...
    
    // ★ 新增：计费预检（在流开始前）
    if h.maasSvc.Enabled() && keyInfo != nil {
        // 1. 预估请求成本
        estimatedCost := h.estimateRequestCost(r, clientModel)
        
        // 2. 预检余额
        decision, err := h.maasSvc.PreCheckBalance(ctx, keyInfo.TenantID, estimatedCost)
        
        if err != nil {
            slog.Error("billing precheck failed", 
                "request_id", requestID,
                "tenant_id", keyInfo.TenantID,
                "error", err)
            // fail-open: 允许继续（记录到监控）
            decision = &maas.BillingDecision{Allow: true, Policy: "warn", Reason: "precheck error"}
        }
        
        // 3. 根据决策决定是否阻断
        if !decision.Allow {
            slog.Warn("billing precheck rejected request",
                "request_id", requestID,
                "tenant_id", keyInfo.TenantID,
                "reason", decision.Reason,
                "balance", decision.BalanceBefore,
                "credit_limit", decision.CreditLimit,
                "estimated_cost", decision.EstimatedCost)
            
            // 返回 402 Payment Required
            w.Header().Set("Content-Type", "application/json")
            w.WriteHeader(http.StatusPaymentRequired)
            json.NewEncoder(w).Encode(map[string]interface{}{
                "error": map[string]interface{}{
                    "type":    "insufficient_balance",
                    "message": "Insufficient balance. Please top up your account.",
                    "details": map[string]interface{}{
                        "balance":        decision.BalanceBefore,
                        "credit_limit":   decision.CreditLimit,
                        "available":      decision.AvailableCredit,
                        "estimated_cost": decision.EstimatedCost,
                    },
                },
            })
            return // ⚠️ 阻断请求
        }
        
        // 4. 预检通过：继续处理请求
        if decision.Policy == "warn" {
            slog.Warn("billing precheck passed with warning",
                "request_id", requestID,
                "tenant_id", keyInfo.TenantID,
                "reason", decision.Reason)
        }
    }
    
    // ... 继续现有的流处理逻辑 ...
}
```

---

## 测试用例

```go
// maas/balance_precheck_test.go

func TestPreCheckBalance_SufficientBalance(t *testing.T) {
    // 场景1: 余额充足
    svc := setupTestService(t)
    setTenantBalance(t, svc, "tenant1", 5000, 1000, "warn")
    
    decision, err := svc.PreCheckBalance(ctx, "tenant1", 100)
    
    assert.NoError(t, err)
    assert.True(t, decision.Allow)
    assert.Equal(t, "allow", decision.Policy)
}

func TestPreCheckBalance_InDebtWarnMode(t *testing.T) {
    // 场景2: 余额不足，warn模式，在信用额度内
    svc := setupTestService(t)
    setTenantBalance(t, svc, "tenant2", -200, 1000, "warn")
    
    decision, err := svc.PreCheckBalance(ctx, "tenant2", 100)
    
    assert.NoError(t, err)
    assert.True(t, decision.Allow)
    assert.Equal(t, "warn", decision.Policy)
    assert.Contains(t, decision.Reason, "using credit line")
}

func TestPreCheckBalance_InDebtBlockMode(t *testing.T) {
    // 场景3: 余额不足，block模式，即使在信用额度内也阻断
    svc := setupTestService(t)
    setTenantBalance(t, svc, "tenant3", -200, 1000, "block")
    
    decision, err := svc.PreCheckBalance(ctx, "tenant3", 100)
    
    assert.NoError(t, err)
    assert.False(t, decision.Allow)
    assert.Equal(t, "block", decision.Policy)
}

func TestPreCheckBalance_OverCreditLimit(t *testing.T) {
    // 场景4: 超过信用额度，强制阻断
    svc := setupTestService(t)
    setTenantBalance(t, svc, "tenant4", -1200, 1000, "warn")
    
    decision, err := svc.PreCheckBalance(ctx, "tenant4", 100)
    
    assert.NoError(t, err)
    assert.False(t, decision.Allow)
    assert.Equal(t, "block", decision.Policy)
    assert.Contains(t, decision.Reason, "over credit limit")
}
```

---

## 下一步：立即执行第1步

我现在将创建迁移文件1000，这是最安全的第一步。
