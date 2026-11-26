# P0-1 Handler集成指南

## 目标

将预检逻辑集成到 `domains/streaming/handler.go`，在流式响应开始前检查余额，实现：
- 余额不足时返回 402 Payment Required（阻断模式）
- 或记录警告继续服务（警告模式）

---

## 核心原则

### ⚠️ 关键约束
1. **必须在流开始前**：一旦HTTP响应开始（SSE流推送），就无法改变状态码
2. **fail-open策略**：预检失败时默认允许继续（避免计费系统故障导致服务不可用）
3. **性能要求**：预检延迟需 < 50ms（单次数据库查询 + 轻量计算）

### 集成位置判断标准

在请求处理流程中找到同时满足以下条件的位置：
- ✅ 已完成认证（有 `tenantID`）
- ✅ 已解析模型名称（有 `modelName`）
- ✅ **流尚未开始**（`w.Write()` 未调用，SSE未推送）
- ✅ 可以安全返回错误响应

---

## 集成步骤

### 步骤1：在Handler添加成员变量

```go
// domains/streaming/handler.go

type Handler struct {
    // ... 现有字段 ...
    
    maasSvc *maas.Service // 确认已存在
    pool    *pgxpool.Pool // 确认已存在（cost_estimation.go需要）
}
```

### 步骤2：找到集成点

**推荐位置**：在 `ServeHTTP` 或主请求处理函数中，认证之后、流开始之前

**示例伪代码**（实际位置需要根据代码结构调整）：

```go
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // ... 现有代码：认证、解析请求 ...
    
    // ★ 假设到这里已有：
    // - keyInfo (包含 tenantID)
    // - clientModel (用户请求的模型名称)
    // - 且流尚未开始
    
    // ───────────────────────────────────────────────────
    // ★ 新增：计费预检（插入到这个位置）
    // ───────────────────────────────────────────────────
    if h.maasSvc != nil && h.maasSvc.Enabled() && keyInfo != nil && keyInfo.TenantID != "" {
        // 1. 预估请求成本
        estimatedCost := h.EstimateRequestCostCredits(r, clientModel)
        
        // 2. 预检余额
        decision, err := h.maasSvc.PreCheckBalance(r.Context(), keyInfo.TenantID, estimatedCost)
        
        if err != nil {
            // 查询失败：fail-open，记录错误但允许继续
            slog.Error("billing precheck query failed, allowing request (fail-open)",
                "request_id", requestID,
                "tenant_id", keyInfo.TenantID,
                "error", err)
            // 继续处理请求
        } else if !decision.Allow {
            // 预检不通过：返回 402 Payment Required
            slog.Warn("billing precheck rejected request",
                "request_id", requestID,
                "tenant_id", keyInfo.TenantID,
                "reason", decision.Reason,
                "balance", decision.BalanceBefore,
                "credit_limit", decision.CreditLimit,
                "estimated_cost", decision.EstimatedCost)
            
            // 返回402错误（JSON格式）
            h.returnBillingError(w, r, decision)
            return // ⚠️ 阻断请求
        } else if decision.Policy == "warn" {
            // 预检通过但有警告（进入负债）
            slog.Warn("billing precheck passed with warning (using credit line)",
                "request_id", requestID,
                "tenant_id", keyInfo.TenantID,
                "reason", decision.Reason,
                "balance", decision.BalanceBefore,
                "available", decision.AvailableCredit)
            // 继续处理请求
        }
        // else: 余额充足，静默通过
    }
    
    // ... 继续现有的流处理逻辑 ...
}
```

### 步骤3：实现错误响应函数

```go
// domains/streaming/handler.go

// returnBillingError 返回402计费错误响应
func (h *Handler) returnBillingError(w http.ResponseWriter, r *http.Request, decision *maas.BillingDecision) {
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    w.WriteHeader(http.StatusPaymentRequired)
    
    response := map[string]interface{}{
        "error": map[string]interface{}{
            "type":    "insufficient_balance",
            "message": "Insufficient balance. Please top up your account to continue using the service.",
            "code":    "billing_insufficient_balance",
            "details": map[string]interface{}{
                "current_balance":  decision.BalanceBefore,
                "credit_limit":     decision.CreditLimit,
                "available_credit": decision.AvailableCredit,
                "estimated_cost":   decision.EstimatedCost,
                "policy":           decision.Policy,
            },
        },
    }
    
    _ = json.NewEncoder(w).Encode(response)
}
```

---

## 检查清单

### 集成前检查

- [ ] 确认集成点在**流开始前**（搜索 `w.Write(` 或 `Flush()`，确保在这些调用之前）
- [ ] 确认已有 `tenantID`（从 `keyInfo` 或类似结构获取）
- [ ] 确认已有 `modelName`（从 `clientModel` 或类似变量获取）
- [ ] 确认 `h.pool` 可用（`cost_estimation.go` 需要查询数据库）

### 集成后测试

#### 测试1：余额充足（正常流程）
```bash
# 设置租户余额=5000，信用额度=1000，策略=warn
# 发起请求，预估成本=100
# 预期：返回200，请求成功
```

#### 测试2：余额不足，warn模式（进入负债）
```bash
# 设置租户余额=-200，信用额度=1000，策略=warn
# 发起请求，预估成本=100
# 预期：返回200，日志包含"using credit line"
```

#### 测试3：余额不足，block模式（阻断）
```bash
# 设置租户余额=-200，信用额度=1000，策略=block
# 发起请求，预估成本=100
# 预期：返回402，响应体包含错误详情
```

#### 测试4：超过信用额度（强制阻断）
```bash
# 设置租户余额=-1200，信用额度=1000，策略=warn
# 发起请求，预估成本=100
# 预期：返回402，reason包含"over credit limit"
```

#### 测试5：预检失败（fail-open）
```bash
# 临时关闭数据库或模拟查询错误
# 发起请求
# 预期：返回200（允许继续），日志包含"fail-open"
```

---

## 验证SQL

### 验证预检日志记录正常
```sql
SELECT 
    tenant_id,
    decision,
    policy,
    balance_before_credits,
    estimated_cost_credits,
    reason,
    created_at
FROM billing_precheck_log
ORDER BY created_at DESC
LIMIT 10;

-- 预期：每次请求都有对应的预检日志
```

### 验证402阻断生效
```sql
-- 查询被阻断的请求（应该没有对应的session_turns记录）
SELECT COUNT(*) 
FROM billing_precheck_log 
WHERE decision = 'reject' 
  AND created_at > NOW() - INTERVAL '1 hour';

-- 对比：这些request_id不应该出现在session_turns中
```

---

## 性能监控

### 预检延迟监控
```go
// 在集成点添加性能监控
start := time.Now()
decision, err := h.maasSvc.PreCheckBalance(r.Context(), keyInfo.TenantID, estimatedCost)
precheckDuration := time.Since(start)

if precheckDuration > 50*time.Millisecond {
    slog.Warn("billing precheck slow",
        "duration_ms", precheckDuration.Milliseconds(),
        "tenant_id", keyInfo.TenantID)
}
```

### Prometheus指标（可选）
```go
// 添加指标收集
billingPrecheckDuration.Observe(precheckDuration.Seconds())
billingPrecheckDecisions.WithLabelValues(decision.Policy, strconv.FormatBool(decision.Allow)).Inc()
```

---

## 常见问题

### Q1: 如果找不到明确的"流开始前"位置怎么办？
**A**: 搜索代码中的关键点：
```bash
# 找到响应写入的位置
grep -n "w.Write\|w.Flush\|flusher.Flush" domains/streaming/handler.go

# 找到SSE推送的位置
grep -n "data: \|event: " domains/streaming/handler.go

# 预检必须在这些调用之前
```

### Q2: 如果handler.go太复杂，请求流程分散在多个函数？
**A**: 寻找"入口函数"模式：
- `ServeHTTP` 是HTTP handler的入口
- 或者 `Handle`, `Process`, `Execute` 等命名的主函数
- 在入口函数的**早期**（认证后）插入预检

### Q3: 如果担心预检影响性能？
**A**: 优化策略：
1. **缓存余额**：在内存中缓存租户余额30秒
2. **异步预检**：对低风险租户（余额充足）跳过预检
3. **批量查询**：如果有批量请求，合并查询

示例缓存实现：
```go
// 简单的内存缓存（30秒TTL）
type balanceCache struct {
    mu    sync.RWMutex
    cache map[string]cachedBalance
}

type cachedBalance struct {
    balance    int64
    limit      int64
    policy     string
    expireAt   time.Time
}

func (h *Handler) getCachedBalance(tenantID string) (int64, int64, string, bool) {
    h.balanceCache.mu.RLock()
    defer h.balanceCache.mu.RUnlock()
    
    if cached, ok := h.balanceCache.cache[tenantID]; ok {
        if time.Now().Before(cached.expireAt) {
            return cached.balance, cached.limit, cached.policy, true
        }
    }
    return 0, 0, "", false
}
```

### Q4: 如何测试"流已开始"的场景？
**A**: 在集成点之后添加断言：
```go
// 在预检代码块之后
if _, ok := w.(http.Flusher); ok {
    slog.Error("INTEGRATION BUG: precheck after response started")
    panic("precheck must run before streaming starts")
}
```

---

## 回滚计划

如果集成后出现问题：

### 快速回滚（代码层）
```go
// 在预检代码块最开始添加开关
if os.Getenv("DISABLE_BILLING_PRECHECK") == "true" {
    goto skip_precheck
}

// ... 预检逻辑 ...

skip_precheck:
// 继续原有流程
```

### 回滚数据库迁移
```bash
# 应用回滚脚本
psql -f sql/migrations/startup/1000_billing_status_and_credit_limit.down.sql
```

---

## 下一步

集成完成后，继续：
1. ✅ 编写单元测试（见 `docs/fixes/.../test_plan.md`）
2. ✅ 灰度发布（10% → 50% → 100%）
3. ✅ 监控指标（402错误率、预检延迟、负债租户数）
4. ✅ 转向P0-2（账单状态管理API）

---

## 联系与支持

如果在集成过程中遇到问题：
- 检查日志中的 `billing precheck` 关键词
- 查看 `billing_precheck_log` 表的记录
- 确认 `tenant_credit_wallets` 表的数据正确

**关键文件清单**：
- ✅ `sql/migrations/startup/1000_billing_status_and_credit_limit.sql` - 数据库迁移
- ✅ `maas/balance_precheck.go` - 预检逻辑
- ✅ `domains/streaming/cost_estimation.go` - 成本预估
- 📋 `domains/streaming/handler.go` - **需要集成**（按本指南）
