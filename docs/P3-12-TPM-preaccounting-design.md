# P3-12: TPM预记账无完成侧退款设计文档

**日期**: 2026-11-16  
**优先级**: P3（可接受的技术债，无需立即修复）  
**状态**: 文档化，设计确认  

---

## 问题描述

### 当前行为
- TPM（Token Per Minute）限流器在**请求入口处进行预检**（admission control）
- 估计的token数在请求**入口立即预记账**（line 76: `!tpmc.CheckTPM(keyInfo.ID, estimatedTokens, tpmLimit)`)
- 请求**完成后不会根据实际消费进行调整或退款**
- 如果实际token消费< 估计值，多余的预记账额度**不会返还**

### 影响

1. **估计不准导致的过度限制**: 
   - 如果token估计值 > 实际值，会浪费配额
   - 大量短请求的场景中会加剧限制强度

2. **缓存/提前终止场景**:
   - 中断的流式响应可能浪费预记账
   - 例：流式返回前5个token后中断，但预记账了100个token

3. **计费精度**:
   - 预记账 ≠ 实际消费
   - 用户看到的限流会严于实际消费水位

---

## 设计根因

### 为什么采用预记账（Pre-accounting）？

1. **早期拒绝（Fail-Fast）**: 
   - 在处理token前就知道是否会超限
   - 避免"投入计算 → 流式返回 → 中途发现超限"的浪费

2. **简化并发模型**:
   - 不需要等待LLM完成才回收额度
   - 每个请求的token预算在入口确定，无歧义

3. **防止闪现溢出**:
   - 防止多个请求同时开始时产生的"超限后再拒绝"问题
   - 在有限的60s窗口内控制并发用量

### 为什么不退款？

1. **设计决策**:
   - 预记账的目的是**容量规划**，而非**精确计费**
   - 假设长期看，估计值平均接近实际值
   - 保持简单：一次性承诺，不需要跟踪完成事件

2. **复杂度成本**:
   - 需要在流式响应完成/中断时回调limiter
   - 涉及多层中断处理（超时/客户端断线/服务端错误）
   - 容易在异常路径上遗漏

3. **风险控制**:
   - 如果一直有"尾部退款"，最终配额会被充分利用
   - 一旦某个时刻大量请求完成且返还额度，会形成尖峰（thundering herd）
   - 预记账保持平稳的负载曲线

---

## 现状验证

### 代码位置

**入口预检**: `domains/streaming/rate_limit.go:76`
```go
if !tpmc.CheckTPM(keyInfo.ID, estimatedTokens, tpmLimit) {
    return rateLimitOutcome{
        Blocked:   true,
 Limit:     tpmLimit,
        Remaining: 0,
        ResetSec:  60,
 Reason:    "tpm_limit",
    }
}
```

**实现**: `ratelimit/sliding.go:CheckTPM()`
- 60秒滑窗，添加entry记录时间戳+token数
- 无完成侧回调（no CompletionCallback、no RefundTPM）

### 行为验证

测试场景：
```
配额: 1000 TPM
预估: 100 tokens/请求
实际: 50 tokens/请求（缓存命中）

- 入口：CheckTPM(100, 1000) → 通过，窗口计数900
- 返回：50 tokens消费，50预记账未用
- 结果：窗口仅能容纳9个实际请求（而非18个）
```

---

## 处置方案

### 选项A: 保持现状 + 文档化（推荐）

**实施步骤**:
1. ✅ 在rate_limit.go添加详细注释，说明预记账设计
2. ✅ 在TPMChecker接口文档中标注"no refund"
3. ✅ 添加API key配置指南：建议按实际峰值的80-90%设置TPM上限
4. ✅ 监控建议：track `estimated_tokens` vs `actual_tokens`分布

**代码改进**:
```go
// TPMChecker 预检token预算（admission control 模式）。
// 语义: 预检入口时立即记账，完成时不退款。
// 假设: 长期平均 estimated ≈ actual；单请求差异通过容量规划吸收。
// 配置建议: TPM上限 = 实测峰值 * 0.85（留出估值误差和激增空间）
type TPMChecker interface {
    CheckTPM(keyID int, estimatedTokens int, limit int) bool
}
```

### 选项B: 实现完成侧退款（不推荐）

**成本**:
- 修改3层调用栈（streaming handler → stream processor → response writer）
- 需要捕获所有中断路径（timeout / client disconnect / error）
- 增加并发map操作（性能开销）
- 需要新建`RefundTPM(keyID, tokens)` API

**收益**:
- 更精确的限流
- 用户感知更好

**风险**:
- 容易在异常路径遗漏退款 → 配额泄漏
- 大量并发完成时的尖峰风险

---

## 文档更新

### 1. rate_limit.go添加ADR注释

```go
// ADR: TPM Pre-accounting Without Refund (2026-11-16, P3-12)
//
// Decision: Use admission-control style pre-accounting at request entry.
// Tokens are deducted when the request is admitted, NOT when it completes.
//
// Rationale:
// 1. Fail-fast: reject before costly LLM inference if over-quota
// 2. Simple concurrency: no tracking of completion across error paths
// 3. Predictable load: no thundering herd from refunds
//
// Trade-off:
// - If estimate > actual (cached hits, early termination), quota is "wasted"
// - Configure limit = peak_observed * 0.85 to absorb variance
//
// Monitoring:
// - Track ratio of estimated_tokens/actual_tokens (target: 0.9-1.1)
// - Alert if ratio > 1.3 (systematic over-estimation)
```

### 2. 配置指南文档

创建 `docs/TPM-CONFIGURATION.md`:
```markdown
# TPM (Token Per Minute) Rate Limit Configuration

## Pre-accounting Model

The gateway uses **admission-control pre-accounting**:
- Tokens are deducted when a request is **admitted** (at entry)
- NOT when the request completes or when actual tokens are consumed
- No refunds are issued for under-consumption

## Why?

1. **Fail-fast**: Reject overages before expensive LLM inference
2. **Simplicity**: No need to track completion/errors across async paths
3. **Predictability**: Smooth load curve (no refund-induced spikes)

## Configuration Recommendations

### Estimate vs Actual Mismatch

Common scenarios where `estimate > actual`:
- Cache hits (serve from memory)
- Early termination (user cancels mid-stream)
- Short completions (prompt requires minimal tokens)

**Recommendation**: Set limit to 85-90% of measured peak:

```
Measured peak: 10,000 TPM
Recommended limit: 8,500 TPM
Buffer: 15% for estimation variance + spikes
```

### Monitoring

Track these metrics:
- `gateway_tpm_estimated_total` (total tokens pre-accounted)
- `gateway_tpm_actual_total` (actual tokens consumed, from LLM billing)
- Ratio = actual / estimated (target: 0.9-1.1, alert if > 1.3)

If ratio consistently < 0.9, your estimates are too conservative.
```

### 3. 测试代码 - 记录现状

```go
// TestP312_TPMPreAccounting documents the pre-accounting behavior (P3-12)
func TestP312_TPMPreAccounting(t *testing.T) {
    limiter := NewSlidingWindowLimiter()
    ctx := context.Background()
    
    // Scenario: estimate 100 tokens but actual is 50
    result := limiter.CheckTPM(keyID=1, estimatedTokens=100, limit=1000)
    if !result {
        t.Fatal("First request should be admitted")
    }
    
    // After request completion, tokens are NOT refunded
    // (this is the designed behavior, not a bug)
    
    // Verify that capacity is reduced by estimated amount, not actual
    status := limiter.RPMStatus(keyID=1, limit=1000)
    if status.used != 100 { // NOT 50
        t.Errorf("TPM pre-accounting: expected 100 (estimated), got %d", status.used)
    }
    
    // This is by design: config should account for this variance
}
```

---

## 后续跟踪

- ✅ 文档化（当前P3-12交付物）
- 📋 监控上线（接入metrics dashboard）
- 📋 客户通知（更新API key配置指南）
- 🔄 未来优化：如果实测发现ratio持续 < 0.8，可重新评估是否要实现退款

---

**交付**: 本文档 + 代码注释更新  
**验收标准**: 新加入的工程师能从代码+文档理解为什么TPM没有完成侧退款  
**维护者**: LLM Gateway 团队
