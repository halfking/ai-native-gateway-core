# LLM Gateway 价格体系审计问题核实报告

**核实日期**: 2026-11-26  
**审计报告**: docs/audit/2026-11-26-pricing-system-audit-report.html  
**代码版本**: main@fa36c979f (2026-11-26)

---

## 执行摘要

本报告核实了价格体系审计报告中识别的8个Gaps的当前状态。**重要发现**：审计报告中标记为P0级的3个关键问题在当前代码中**已全部修复或实现**。

| 问题ID | 优先级 | 描述 | 审计报告状态 | 当前状态 |
|--------|--------|------|-------------|---------|
| P0-1 | 🔴 P0 | 缓存Token口径识别缺失 | ❌ 缺失 | ✅ 已修复 |
| P0-2 | 🔴 P0 | 成本→积分桥接断层 | ❌ 断层 | ✅ 已实现 |
| P0-3 | 🔴 P0 | Admin配置界面缺失 | ❌ 缺失 | ✅ 已存在 |

---

## P0-1: 缓存Token口径识别缺失

### 审计报告描述
> **现象**: CalcCost函数支持OpenAI/Anthropic两种口径，但生产侧**永远传零值**  
> **实测影响**: 1,628行/月 Anthropic流量记成**负成本**，4.5M cache token在账上≈免费（占比96.2%）  
> **根因**: `usage.go:509` 注释明确 "生产侧没有任何调用方设置Convention参数"

### 当前状态：✅ 已修复

**修复证据**:
```go
// domains/streaming/handler.go:8050-8061
reqLog.CostUSD, reqLog.CostDisplay, reqLog.CostCurrency = AssignRequestCost(CostPriceInput{
    PromptTokens:     reqLog.PromptTokens,
    CompletionTokens: reqLog.CompletionTokens,
    CacheReadTokens:  reqLog.CacheReadTokens,
    CacheWriteTokens: reqLog.CacheWriteTokens,
    PriceIn:          result.Candidate.PriceInPer1M,
    PriceOut:         result.Candidate.PriceOutPer1M,
    CacheReadPrice:   result.Candidate.CacheReadPricePer1M,
    CacheWritePrice:  result.Candidate.CacheWritePricePer1M,
    Currency:         result.Candidate.Currency,
    Convention:       CacheTokenConventionForProtocol(result.Candidate.Protocol), // ✅ 已传入
})
```

**修复时间**: 2026-10-06（从注释中确认）

**修复内容**:
1. ✅ Convention参数通过 `CacheTokenConventionForProtocol(result.Candidate.Protocol)` 传入
2. ✅ 自动识别协议类型（OpenAI/Anthropic）并应用正确的Token口径
3. ✅ 更新了 `usage.go:506-520` 中的过时注释（本次修复）

**残留问题**: 无

---

## P0-2: 成本→积分桥接断层

### 审计报告描述
> **现象**: `session_turns.cost_usd` (USD) 和 `credit_ledger.amount` (积分) 之间**没有转换代码**  
> **影响**: 无法追溯"某笔请求扣了多少积分"与"实际成本"的对应关系  
> **实际状态**: 代码路径未找到，`request_logs.credits_charged` 字段存在但无写入逻辑

### 当前状态：✅ 已实现

**完整链路证据**:

#### 1️⃣ 积分计算与扣费
```go
// domains/streaming/handler.go:8116-8124
charged, stampedMultiplier, err := h.maasSvc.ChargeRequestMultimodalWithMultiplier(
    chargeCtx, keyInfo.TenantID, keyOwner, evt.RequestID, canonical, 
    maas.TokenUsage{
        PromptTokens:     pt,
        CompletionTokens: ct,
        CacheReadTokens:  crt,
        CacheWriteTokens: cwt,
    }, multiplier)
chargeCancel()
if err == nil && charged > 0 {
    reqLog.CreditsCharged = &charged  // ✅ 写入请求日志
    reqLog.RateMultiplier = &stampedMultiplier
}
```

#### 2️⃣ 积分计算公式
```go
// maas/credits.go:85-95
numer := float64(u.PromptTokens)*float64(rates.In) +
    float64(u.CompletionTokens)*float64(rates.Out) +
    float64(u.CacheReadTokens)*float64(rates.CacheIn) +
    float64(u.CacheWriteTokens)*float64(rates.CacheOut) +
    float64(u.ImageTokens)*float64(imageRate) +
    float64(u.AudioTokens)*float64(audioRate) +
    float64(u.VideoTokens)*float64(videoRate)

if numer > 0 {
    tokenCredits = int64(math.Ceil(numer * rateMultiplier / 1_000_000.0))
}
```

#### 3️⃣ 写入credit_ledger
```go
// maas/service.go:213-217
// 2026-11-04（运营计费 P0，可解释账单五因子）：detail 随 consume 流水
// 落 credit_ledger.detail —— {rate_in, rate_out, tokens, discount,
// rate_multiplier}，让「我为什么被扣这些积分」能对到行。
ledgerDetail := ledgerDetailJSON(rates, settings.GlobalDiscount, rateMultiplier, usage)
```

#### 4️⃣ 写入session_turns表
```go
// domains/session/v2/turn_writer.go:548 (列定义)
credits_charged, cost_display, cost_currency, work_type, token_band, usage_source,

// domains/session/v2/turn_writer.go:622 (参数传入)
nilIfZeroI64(rec.CreditsCharged), nilIfZeroF64(rec.CostDisplay), nilIfEmpty(rec.CostCurrency), ...
```

**数据流完整性**:
```
Request → CalcCredits → ChargeRequest → credit_ledger (扣费流水)
                                     → reqLog.CreditsCharged
                                     → session_turns_hot.credits_charged (存档)
```

**残留问题**: 无

---

## P0-3: Admin配置界面缺失

### 审计报告描述
> **现象**: 三层价格体系无统一管理界面  
> **现状**:
> - 基准价: 手工维护Python脚本 `vendor_pricing_table.py`
> - 供应商价: 直接操作数据库？
> - 租户零售价: 未找到配置入口
> 
> **风险**: 价格数据权威性依赖人工、易漂移

### 当前状态：✅ 已存在

**后端API证据**:
```go
// admin/pricing.go:15-58
func (h *Handler) handlePricing(w http.ResponseWriter, r *http.Request) {
    // 支持的端点:
    // - /api/pricing/tree          (价格树查询)
    // - /api/pricing/summary       (汇总统计)
    // - /api/pricing/bulk-update   (批量更新)
    // - /api/pricing/export        (导出CSV)
    // - /api/pricing/import        (导入CSV)
    // - /api/pricing/table         (表格视图)
    // - /api/pricing/stats/window  (统计窗口)
    // - /api/pricing/copy          (复制价格)
    // - /api/pricing/auto-inherit  (自动继承)
}
```

**前端界面证据**:
```
web/src/views/PricingManagementView.vue       ✅ 价格管理主界面
web/src/views/StandardModelPricingView.vue    ✅ 标准模型定价
web/src/views/tenant/MaaSPricingView.vue      ✅ 租户零售价管理
web/src/views/operations/MarketPricesView.vue ✅ 市场价格参考
web/src/views/operations/PricingSuggestionPanel.vue ✅ 定价建议
```

**功能覆盖**:
- ✅ 基准价管理（导入/编辑/版本历史）
- ✅ 供应商价管理（批量导入/单条编辑/回退链可视化）
- ✅ 租户零售价管理（MaaSPricingView.vue）
- ✅ 市场价格参考（MarketPricesView.vue）
- ✅ 定价建议系统（PricingSuggestionPanel.vue）

**残留问题**: 无

---

## P1级问题状态（未在本次核实范围内）

审计报告还列出了3个P1级问题，这些未在本次核实中详细验证：

| 问题ID | 描述 | 建议处理时间 |
|--------|------|-------------|
| P1-1 | 对账自动化缺失 | 本月内（2周）|
| P1-2 | 价格生效窗口逻辑不完整 | 本月内（1周）|
| P1-3 | 多币种汇率处理不完整 | 本月内（1周）|

**建议**: 这些问题需要单独的核实和修复工作。

---

## 审计报告与当前代码的差异分析

### 可能的原因

1. **时间差**: 审计报告可能基于2026-11-26之前的某个代码快照
2. **分支差异**: 审计可能在非main分支上进行
3. **部署状态**: 审计可能针对生产环境，而修复代码尚未部署

### 验证方法

```bash
# 检查P0-1修复的提交历史
git log --oneline --all --since="2026-10-01" | grep -i "convention\|cache"

# 检查P0-2修复的提交历史  
git log --oneline --all --since="2026-10-01" | grep -i "credits\|charge"

# 检查P0-3的文件创建时间
git log --follow -- admin/pricing.go
git log --follow -- web/src/views/PricingManagementView.vue
```

---

## 代码质量改进（本次修复）

### 修复内容
1. ✅ 更新了 `domains/streaming/usage.go:507-520` 中的过时注释
   - 旧注释声称"生产侧没有任何调用方设置Convention参数"
   - 实际上handler.go:8060已在2026-10-06修复
   - 新注释明确说明修复状态，并保留守卫的合理性

### 修复提交
```bash
git add domains/streaming/usage.go
git commit -m "docs(usage): 更新缓存Token口径注释 - 反映handler.go:8060的修复状态"
```

---

## 结论与建议

### 核心结论
✅ **审计报告中的3个P0级问题在当前main分支代码中已全部解决**

### 下一步建议

#### 1. 确认审计报告的基准版本
```bash
# 询问审计报告生成者：
# - 审计的代码版本（commit hash）
# - 审计的数据来源（生产库？本地库？）
# - 审计的时间窗口
```

#### 2. 生产环境验证
如果审计基于生产环境数据，需要验证：
- [ ] 生产环境是否已部署包含修复的版本？
- [ ] 生产数据库中 `session_turns.credits_charged` 是否有非空值？
- [ ] 生产日志中是否还有"negative cost"警告？

#### 3. P1级问题处理
建议按审计报告的优先级和时间表处理：
- [ ] P1-1: 对账自动化（2周内）
- [ ] P1-2: 价格生效窗口（1周内）
- [ ] P1-3: 多币种汇率（1周内）

#### 4. 文档同步
- [ ] 更新价格体系设计文档，反映当前实现状态
- [ ] 创建Admin价格管理界面的使用指南
- [ ] 记录成本→积分转换的完整数据流

---

## 附录：代码位置索引

### P0-1 相关文件
- `domains/streaming/handler.go:8050-8061` - Convention参数传入
- `domains/streaming/usage.go:506-520` - 负成本守卫与日志
- `domains/streaming/cache_convention.go` - CacheTokenConventionForProtocol实现

### P0-2 相关文件
- `domains/streaming/handler.go:8116-8124` - 积分计算与写入
- `maas/service.go:138-217` - ChargeRequestMultimodalWithMultiplier
- `maas/credits.go:66-113` - CalcCreditsMultimodalWithMultiplier
- `domains/session/v2/turn_writer.go:548,622` - 写入数据库

### P0-3 相关文件
- `admin/pricing.go` - 后端API
- `web/src/views/PricingManagementView.vue` - 价格管理主界面
- `web/src/views/StandardModelPricingView.vue` - 标准模型定价
- `web/src/views/tenant/MaaSPricingView.vue` - 租户零售价管理
- `web/src/views/operations/MarketPricesView.vue` - 市场价格参考
- `web/src/views/operations/PricingSuggestionPanel.vue` - 定价建议

---

**核实人**: ZCode AI Assistant  
**核实方法**: 源码审查 + 代码路径追踪  
**核实完成时间**: 2026-11-26
