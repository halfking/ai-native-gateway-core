# 价格计费修复 - 任务交接包

**交接日期**: 2026-11-26  
**准备人**: AI Assistant  
**目标**: 本周内完成P0-1到P0-4的部署和验证

---

## 📦 交接内容

### 任务1: P0-1 Handler集成（开发团队）
**负责人**: 后端开发工程师  
**预计时间**: 1-2小时  
**优先级**: 🔴 最高

### 任务2: 数据库迁移应用（DBA/运维）
**负责人**: DBA  
**预计时间**: 30分钟  
**优先级**: 🔴 高（P0-1依赖）

### 任务3: P0-2 API部署（运维团队）
**负责人**: 运维工程师  
**预计时间**: 30分钟  
**优先级**: 🟡 中

### 任务4: P0-4 数据验证（数据分析）
**负责人**: DBA/数据分析师  
**预计时间**: 30分钟  
**优先级**: 🟢 低

---

## 📋 任务1: P0-1 Handler集成

### 前置条件
- [ ] 已阅读集成指南: `docs/fixes/2026-11-26-p0-1-handler-integration-guide.md`
- [ ] 已应用迁移1000（见任务2）
- [ ] 本地开发环境已配置测试数据库

### 执行步骤

#### 步骤1: 找到集成点（15分钟）

在 `domains/streaming/handler.go` 中找到同时满足以下条件的位置：
- ✅ 已完成认证（有 `keyInfo.TenantID`）
- ✅ 已解析模型（有 `clientModel`）
- ✅ **流尚未开始**（`w.Write()` 未调用）

**提示**: 搜索 `ServeHTTP` 或主请求处理函数，通常在认证逻辑之后、流处理之前。

#### 步骤2: 插入预检代码（30分钟）

```go
// domains/streaming/handler.go
// 在合适位置插入（参考集成指南的伪代码）

if h.maasSvc != nil && h.maasSvc.Enabled() && keyInfo != nil && keyInfo.TenantID != "" {
    // 1. 预估成本
    estimatedCost := h.EstimateRequestCostCredits(r, clientModel)
    
    // 2. 预检余额
    decision, err := h.maasSvc.PreCheckBalance(r.Context(), keyInfo.TenantID, estimatedCost)
    
    if err != nil {
        // fail-open: 允许继续
        slog.Error("billing precheck query failed, allowing request (fail-open)",
            "request_id", requestID,
            "tenant_id", keyInfo.TenantID,
            "error", err)
    } else if !decision.Allow {
        // 阻断请求
        slog.Warn("billing precheck rejected request",
            "request_id", requestID,
            "tenant_id", keyInfo.TenantID,
            "reason", decision.Reason)
        
        h.returnBillingError(w, r, decision)
        return // ⚠️ 阻断
    } else if decision.Policy == "warn" {
        // 警告但允许
        slog.Warn("billing precheck passed with warning",
            "request_id", requestID,
            "tenant_id", keyInfo.TenantID,
            "reason", decision.Reason)
    }
}

// ... 继续现有流处理逻辑 ...
```

#### 步骤3: 添加错误响应函数（15分钟）

```go
// domains/streaming/handler.go

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

#### 步骤4: 编译验证（5分钟）

```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go

# 编译
go build ./domains/streaming
go build ./maas

# 检查错误
echo $?  # 应该输出0
```

#### 步骤5: 运行单元测试（5分钟）

```bash
# 设置测试数据库
export TEST_DATABASE_URL="postgres://user:pass@localhost:5432/llm_gateway_test"

# 运行预检测试
go test ./maas -run TestPreCheckBalance -v

# 预期：所有测试通过
```

### 验收标准

- [ ] 代码编译通过
- [ ] 单元测试全部通过
- [ ] 集成点在**流开始前**（确认：在集成点之后搜索 `w.Write(`，确保距离足够远）
- [ ] 错误响应格式正确（JSON）

### 失败排查

**问题1**: 找不到合适的集成点
- **解决**: 在 `ServeHTTP` 入口处添加，尽可能早（认证后即可）

**问题2**: 编译错误 "Handler 没有 maasSvc 字段"
- **解决**: 确认 Handler 结构体包含 `maasSvc *maas.Service`

**问题3**: 单元测试失败
- **解决**: 检查 `TEST_DATABASE_URL` 是否正确，数据库是否已应用迁移1000

---

## 📋 任务2: 数据库迁移应用

### 前置条件
- [ ] 已备份数据库
- [ ] 有DBA权限
- [ ] 已阅读迁移文件

### 执行步骤

#### Dev环境（立即执行）

```bash
# 连接dev数据库
psql -h dev-db-host -U admin -d llm_gateway_dev

# 应用迁移1000
\i /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/sql/migrations/startup/1000_billing_status_and_credit_limit.sql

# 应用迁移1001
\i /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/sql/migrations/startup/1001_bill_payment_tracking.sql

# 验证字段已添加
\d tenant_credit_wallets
\d maas_monthly_bills
\d billing_precheck_log
\d maas_bill_status_audit

# 验证视图已创建
\dv v_tenants_in_debt
\dv v_overdue_bills
```

#### Staging环境（观察3天后）

```bash
# 同上，但使用staging数据库
psql -h staging-db-host -U admin -d llm_gateway_staging
\i ...
```

#### Production环境（观察7天后）

```bash
# ⚠️ 在维护窗口执行
# ⚠️ 确保已在staging验证无问题

psql -h prod-db-host -U admin -d llm_gateway_prod
BEGIN;
\i /path/to/1000_billing_status_and_credit_limit.sql
-- 检查输出无错误
COMMIT;

BEGIN;
\i /path/to/1001_bill_payment_tracking.sql
COMMIT;
```

### 验收标准

- [ ] 迁移成功执行（无ERROR）
- [ ] 所有字段已添加
- [ ] 所有表已创建
- [ ] 所有视图已创建
- [ ] 触发器已创建

### 回滚步骤（如果需要）

```bash
# 应用down迁移
\i /path/to/1000_billing_status_and_credit_limit.down.sql
\i /path/to/1001_bill_payment_tracking.down.sql
```

---

## 📋 任务3: P0-2 API部署

### 前置条件
- [ ] 已应用迁移1001
- [ ] 有部署权限

### 执行步骤

#### 步骤1: 注册路由（5分钟）

在 `admin/handler.go` 中找到路由注册函数，添加：

```go
// admin/handler.go

func (h *Handler) registerRoutes(mux *http.ServeMux) {
    // ... 现有路由 ...
    
    // ★ 添加这一行
    h.registerBillPaymentRoutes(mux)
}
```

**注意**: `registerBillPaymentRoutes` 函数已在 `admin/bill_payment_handlers.go` 中定义。

#### 步骤2: 编译部署（10分钟）

```bash
# 编译
go build -o llm-gateway ./cmd/server

# 部署到dev
scp llm-gateway dev-server:/opt/llm-gateway/
ssh dev-server 'sudo systemctl restart llm-gateway'

# 检查服务状态
ssh dev-server 'sudo systemctl status llm-gateway'
```

#### 步骤3: 测试API（15分钟）

```bash
# 获取admin token
ADMIN_TOKEN="<your-super-admin-token>"

# 测试1: 标记账单为已付款
curl -X POST http://dev-server:8080/api/admin/maas/bills/mark-paid \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{
    "bill_id": 1,
    "payment_date": "2026-11-25T10:00:00Z",
    "payment_method": "wire_transfer",
    "payment_voucher": "TEST001",
    "payment_amount_cents": 50000
  }'

# 预期响应: {"success": true, "message": "Bill marked as paid successfully", "bill_id": 1}

# 测试2: 查询账单列表
curl "http://dev-server:8080/api/admin/maas/bills/list?status=paid&limit=10" \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# 预期响应: {"bills": [...], "total": N, ...}

# 测试3: 获取账单详情
curl "http://dev-server:8080/api/admin/maas/bills/details?bill_id=1" \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# 预期响应: {"bill": {...}, "audit_logs": [...]}
```

### 验收标准

- [ ] 服务启动成功
- [ ] 5个API端点全部可访问
- [ ] 权限控制生效（非admin返回403）
- [ ] 审计日志正确记录

---

## 📋 任务4: P0-4 数据验证

### 前置条件
- [ ] 有生产只读副本访问权限
- [ ] 已阅读验证脚本: `docs/fixes/2026-11-26-p0-4-cache-token-convention-verification.md`

### 执行步骤

#### 步骤1: 连接数据库（5分钟）

```bash
# 连接生产只读副本
psql -h prod-readonly-host -U analyst -d llm_gateway_prod
```

#### 步骤2: 执行验证SQL（15分钟）

```sql
-- 验证1: 负成本检查
SELECT 
    DATE(created_at) AS date,
    COUNT(*) FILTER (WHERE cost_usd < 0) AS negative_cost_count,
    COUNT(*) AS total_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE cost_usd < 0) / COUNT(*), 2) AS negative_pct
FROM session_turns
WHERE created_at >= '2026-09-01'
  AND created_at < '2026-12-01'
  AND cost_usd IS NOT NULL
GROUP BY DATE(created_at)
ORDER BY date;

-- 记录结果：2026-10-06之后应该negative_cost_count = 0

-- 验证2: Anthropic cache>prompt场景
SELECT 
    DATE(created_at) AS date,
    COUNT(*) AS cache_gt_prompt_count,
    COUNT(*) FILTER (WHERE cost_usd < 0) AS negative_cost_count,
    AVG(COALESCE(cost_usd, 0)) AS avg_cost_usd
FROM session_turns
WHERE created_at >= '2026-10-01'
  AND created_at < '2026-12-01'
  AND cache_read_tokens > prompt_tokens
  AND protocol LIKE '%anthropic%'
GROUP BY DATE(created_at)
ORDER BY date;

-- 记录结果：2026-10-06之后应该negative_cost_count = 0且avg_cost_usd > 0

-- 验证3: 修复前后对比
WITH periods AS (
    SELECT 
        CASE 
            WHEN created_at < '2026-10-06' THEN 'before_fix'
            ELSE 'after_fix'
        END AS period,
        COUNT(*) AS request_count,
        SUM(COALESCE(cache_read_tokens, 0)) AS total_cache_tokens,
        SUM(COALESCE(cost_usd, 0)) AS total_cost_usd,
        COUNT(*) FILTER (WHERE cost_usd < 0) AS negative_cost_count
    FROM session_turns
    WHERE created_at >= '2026-09-06'
      AND created_at < '2026-11-06'
      AND protocol LIKE '%anthropic%'
    GROUP BY period
)
SELECT * FROM periods ORDER BY period;

-- 记录结果对比
```

#### 步骤3: 填写验证报告（10分钟）

将SQL结果填入模板：`docs/fixes/2026-11-26-p0-4-cache-token-convention-verification.md` 中的"验证报告模板"部分。

### 验收标准

- [ ] 2026-10-06之后，负成本行数 = 0
- [ ] cache>prompt场景，平均成本 > 0
- [ ] 修复后的总成本高于修复前（cache正确计费）

---

## 📊 进度跟踪

| 任务 | 负责人 | 状态 | 预计完成 | 实际完成 |
|------|--------|------|----------|----------|
| 任务1: P0-1集成 | 后端开发 | ⏳ 待开始 | 周二 | - |
| 任务2: 数据库迁移 | DBA | ⏳ 待开始 | 周一 | - |
| 任务3: P0-2部署 | 运维 | ⏳ 待开始 | 周三 | - |
| 任务4: P0-4验证 | 数据分析 | ⏳ 待开始 | 周四 | - |

---

## 🚨 风险与应对

| 风险 | 概率 | 影响 | 应对措施 |
|------|------|------|----------|
| 找不到合适的handler集成点 | 中 | 高 | 在ServeHTTP入口处插入（尽可能早） |
| 迁移执行时间过长（大表） | 低 | 中 | 在维护窗口执行，提前测试 |
| P0-1导致大量402误报 | 低 | 高 | 灰度发布，快速回滚准备 |
| 验证发现修复未生效 | 低 | 高 | 检查代码部署状态，重新部署 |

---

## 📞 联系方式

**问题反馈**:
- 技术问题：查看对应文档的"常见问题"部分
- 紧急问题：联系架构师/技术负责人

**相关文档**:
- P0-1集成指南: `docs/fixes/2026-11-26-p0-1-handler-integration-guide.md`
- P0-2使用文档: `docs/fixes/2026-11-26-p0-2-completion-summary.md`
- P0-4验证脚本: `docs/fixes/2026-11-26-p0-4-cache-token-convention-verification.md`
- 今日总结: `docs/fixes/2026-11-26-work-summary.md`

---

**交接人**: AI Assistant  
**交接日期**: 2026-11-26  
**状态**: ✅ 交接包已准备完毕
