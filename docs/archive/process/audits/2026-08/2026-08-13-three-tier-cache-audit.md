# LLM Gateway Go 三层会话缓存架构审计与对齐方案

> **状态**：审计 + 方案设计
> **日期**：2026-08-13
> **目标**：对照 `tests/session_cache/three_tier_cache_test.go` 设计的三层语义，核对生产代码实现，发现差距并给出方案

---

## 1. 三层缓存语义定义（来自测试蓝本）

### 1.1 设计的语义 vs 代码实现对照

| 层级 | 设计语义 | 测试蓝本类型 | 生产代码对应 | 差距 |
|------|---------|-------------|-------------|------|
| **L1** | 原始多轮会话完整历史 | `RawSessionCache` → `RawSession` | `SessionCache` L1（存 SessionState + body） | ⚠️ 语义部分对齐，但存的是"最后一次发出去的body"而非完整历史 |
| **L2** | 压缩后多轮会话（可跨轮次） | `CompressedSessionCache` → `CompressedSession` | `SessionCache` L1/L2/L3（存压缩状态元数据） | ⚠️ 语义部分对齐，但缺压缩过程数据和 AlignmentMap 全量 |
| **L3** | 脱敏+安全审核后发送会话 | `AuditedSessionCache` → `AuditedSession` | **无独立对象**，脱敏在 HTTP 中间件完成 | ❌ 缺失：没有"审核后待发送"的独立缓存层 |

### 1.2 设计的数据流（来自 three_tier_cache_test.go:398-513）

```
用户请求到达
    ↓
[步骤1] 用户请求加入 L1（原始会话缓存）
    → RawCache.AddTurn(sessionID, tenantID, userMsg, assistantMsg)
    ↓
[步骤2] 调用压缩模块，输出到 L2（压缩会话缓存）
    → CompressedCache.Compress(ctx, rawSession)
    ↓
[步骤3] 安全审计，输出到 L3（审计后会话缓存）
    → AuditedCache.Audit(ctx, compressedSession)
    ↓
[步骤4] L3 数据发送给 LLM（模拟）
    → messages: auditedSession.AuditedMessages
    ↓
[步骤5] 收到 LLM 响应后回写...
```

**关键洞察**：设计要求"收到信息**先入 L3**"，然后经过安全检查后"占位符还原后再进入 L2 压缩会话回写，再传回 L1 原始最新会话轮次回复"。

---

## 2. 生产代码实现审计

### 2.1 当前 SessionCache（compression/session_cache.go）

```
L1: in-process sync.Map LRU（≤1024 sessions，≤256MiB）
    → 存 SessionState 元数据 + lastOutboundBody（nil 表示只存 metadata）
L2: Redis Hash session:sc:{tenantID}:{gwSessionID}:v1
    → 只存 SessionState 字段（无 body）
L3: PostgreSQL（request_logs / session_bodies）
    → 回源用
```

**问题**：
- 当前 SessionCache 存的 `lastOutboundBody` 是"最后一次发出去的压缩后 body"，不是原始完整历史
- L2 纯 metadata，body 需要从 L3 rehydrate
- 没有"原始消息"和"审计后消息"的区分

### 2.2 当前 V2 SessionCache（session/v2/cache_v2.go）

```
L0: Turn delta storage（incremental messages in session_bodies）
L1: Compression metadata cache（in-memory LRU，无 body）
L2: Governance cache（Redis verdicts only）
L3: Cold start from session_turns
```

**问题**：
- V2 L1 存的是 metadata（`CompressionMeta`），不是原始消息也不是压缩后消息
- Governance cache 只存 verdicts，不是"审核后消息"
- 与三层设计语义完全不同

### 2.3 脱敏实现

```
HTTP 入口 → SanitizeInputMiddleware（handler.go:1326）
    → 脱敏在前，缓存存的就是脱敏内容
响应侧 → output_compliance → sanitize_restore（最后执行）
```

**问题**：
- 脱敏在压缩之前完成，缓存天然就是脱敏内容（语义安全但隐式）
- 没有独立的"审核后缓存"层可以查询"这条消息是否经过安全检查"

---

## 3. 核心差距分析

### Gap 1: L1 不是"原始完整历史"

**设计意图**：L1 存完整原始消息历史（RawSession）

**实际实现**：`SessionCache` L1 只存"最后一次 outbound body"，不是完整历史

**影响**：无法追溯"这条原始消息是否被压缩进了哪个摘要"

**建议**：将 L1 改为存完整消息数组，或通过 TurnReader 从 L3 重建完整历史

### Gap 2: L2 缺少压缩过程数据

**设计意图**：L2 存压缩后的消息 + AlignmentMap + 压缩比等信息

**实际实现**：`SessionState` 虽有 `AlignmentMap` 字段（v7），但没有存"压缩后的完整消息列表"

**影响**：无法查看"压缩后实际发了什么给 LLM"

**建议**：扩展 L2 存储 `CompressedMessages []Message` 或提供查询接口

### Gap 3: L3 没有独立对象

**设计意图**：L3 是"审核后待发送"缓存，包含 AuditScore、SecurityScore 等

**实际实现**：这些字段存在于 `SessionState` v6（AuditedAt、AuditScore、SecurityScore 等），但没有存"AuditedMessages"

**影响**：无法查看"这条消息审计后的实际内容"

**建议**：在 SessionState 中增加 `AuditedMessages []Message` 字段，或新增 `AuditedSessionCache`

### Gap 4: 压缩质量评分机制缺失

**设计意图**：能够对比原始数据和压缩后数据，评估压缩质量

**实际实现**：只有 `Lossiness` 分类（none/tail/whole）和 `compression_ratio` gauge

**缺失**：
- 没有"信息密度评分"
- 没有"语义相似度评分"
- 没有"可恢复性评分"

**建议**：增加压缩质量评分系统

---

## 4. 方案设计：三层缓存对齐 + 压缩质量评分

### 4.1 方案概述

```
原始消息（Raw Messages）
    ↓ 存入 TurnWriter
L1 原始缓存（Raw Cache）：完整消息历史，从 session_turns 重建
    ↓
压缩引擎（Compressor）
    ↓
L2 压缩缓存（Compressed Cache）：压缩后消息 + AlignmentMap + 质量评分
    ↓
安全审计（Security Audit）
    ↓
L3 审核缓存（Audited Cache）：审核后消息 + 审核分数 + 发送历史
    ↓
发送给 LLM
```

### 4.2 扩展 SessionState 结构

```go
// SessionState 扩展字段（v8 或独立结构）

// L1 相关（已有部分）
type SessionState struct {
    // ... 现有字段 ...

    // 新增：原始消息统计
    RawMessageCount    int `json:"raw_mc,omitempty"`    // 原始消息数
    RawTokenEstimate   int `json:"raw_te,omitempty"`    // 原始 token 估算

    // L2 相关（新增）
    CompressedMessageCount int               `json:"cmp_mc,omitempty"` // 压缩后消息数
    CompressionRatio       float64           `json:"cmp_ratio,omitempty"` // 压缩比
    AlignmentMap           []AlignmentInfo   `json:"alignment_map,omitempty"` // 已有些段

    // 压缩质量评分（新增）
    CompressionQuality CompressionQualityScore `json:"cmp_quality,omitempty"`
}

// 压缩质量评分
type CompressionQualityScore struct {
    TokenSavingsPercent  float64 `json:"token_savings_pct"`   // Token 节省百分比
    MessageRetentionRate float64 `json:"msg_retention_rate"`  // 消息保留率
    SemanticFidelity     float64 `json:"semantic_fidelity"`   // 语义保真度 0-1
    InformationDensity   float64 `json:"info_density"`        // 信息密度评分
    LossinessClass       string  `json:"lossiness_class"`     // none/tail/whole
    OverallScore         float64 `json:"overall_score"`       // 综合评分 0-100
}

// L3 相关（新增）
type AuditResult struct {
    AuditedMessages   []Message `json:"audited_messages,omitempty"`   // 审核后消息
    AuditScore        int       `json:"audit_score"`                  // 审计分数 0-10
    SecurityScore     int       `json:"security_score"`               // 安全分数 0-10
    SensitiveDetected bool      `json:"sensitive_detected"`           // 检测到敏感内容
    PIIStripped       bool      `json:"pii_stripped"`                 // PII 已移除
    InjectionDetected bool      `json:"injection_detected"`           // 检测到注入
    JailbreakDetected bool      `json:"jailbreak_detected"`           // 检测到越狱
}
```

### 4.3 压缩质量评分算法

```go
// ComputeCompressionQuality 计算压缩质量评分
func ComputeCompressionQuality(originalMsgs, compressedMsgs []Message, st *SessionState) CompressionQualityScore {
    score := CompressionQualityScore{}

    // 1. Token 节省百分比
    originalTokens := estimateTokens(originalMsgs)
    compressedTokens := st.TokenEstimate
    if originalTokens > 0 {
        score.TokenSavingsPercent = float64(originalTokens-compressedTokens) / float64(originalTokens) * 100
    }

    // 2. 消息保留率
    if len(originalMsgs) > 0 {
        score.MessageRetentionRate = float64(len(compressedMsgs)) / float64(len(originalMsgs))
    }

    // 3. 语义保真度（基于 AlignmentMap）
    if len(st.AlignmentMap) > 0 {
        preserved := 0
        for _, a := range st.AlignmentMap {
            if !a.IsCompressed {
                preserved++
            }
        }
        score.SemanticFidelity = float64(preserved) / float64(len(st.AlignmentMap))
    } else {
        score.SemanticFidelity = 1.0 // 无压缩则保真度 100%
    }

    // 4. 信息密度（基于消息内容分析）
    score.InformationDensity = computeInformationDensity(compressedMsgs)

    // 5. Lossiness 分类（已有）
    score.LossinessClass = classifyLossiness(st)

    // 6. 综合评分（加权平均）
    score.OverallScore =
        score.TokenSavingsPercent*0.2 +                    // Token 节省权重 20%
        score.MessageRetentionRate*100*0.15 +              // 消息保留率权重 15%
        score.SemanticFidelity*100*0.35 +                  // 语义保真度权重 35%
        score.InformationDensity*100*0.15 +                // 信息密度权重 15%
        getLossinessBonus(score.LossinessClass)*0.15      // Lossiness 加分权重 15%

    return score
}

// computeInformationDensity 计算信息密度
func computeInformationDensity(msgs []Message) float64 {
    if len(msgs) == 0 {
        return 0
    }
    totalDensity := 0.0
    for _, msg := range msgs {
        density := calculateMessageDensity(msg)
        totalDensity += density
    }
    return totalDensity / float64(len(msgs))
}

// calculateMessageDensity 计算单条消息的信息密度
func calculateMessageDensity(msg Message) float64 {
    // 基于内容特征计算密度：
    // - 代码片段：+0.3
    // - 错误信息：+0.3
    // - URL/链接：+0.2
    // - 数字/日期：+0.1
    // - 重复内容：-0.2
    density := 0.5 // 基础密度
    content := msg.Content

    if strings.Contains(content, "```") {
        density += 0.3
    }
    if strings.Contains(content, "error") || strings.Contains(content, "Error") {
        density += 0.3
    }
    if strings.Contains(content, "http://") || strings.Contains(content, "https://") {
        density += 0.2
    }
    if regexp.Contains(content, `\d+`) {
        density += 0.1
    }
    // 限制在 0-1 范围
    if density > 1.0 {
        density = 1.0
    }
    return density
}

func getLossinessBonus(lossiness string) float64 {
    switch lossiness {
    case LossinessNone:
        return 100
    case LossinessTail:
        return 80
    case LossinessWhole:
        return 50
    default:
        return 0
    }
}
```

### 4.4 新增 Compression Quality 日志

```go
// 在 session_compressor.go 的 Prepare 完成后记录

func (sc *SessionCompressor) logCompressionQuality(
    ctx context.Context,
    originalMsgs []Message,
    result *PrepareResult,
    quality CompressionQualityScore,
) {
    slog.Info("compression_quality",
        "session_id", getSessionID(ctx),
        "tenant_id", getTenantID(ctx),
        "strategy", result.CompressionStrategy,
        "lossiness", result.Lossiness,
        // 原始 vs 压缩
        "original_msg_count", len(originalMsgs),
        "original_tokens", estimateTokens(originalMsgs),
        "compressed_msg_count", result.MsgCount,
        "compressed_tokens", result.TokenEst,
        // 质量评分
        "token_savings_pct", fmt.Sprintf("%.1f%%", quality.TokenSavingsPercent),
        "semantic_fidelity", fmt.Sprintf("%.2f", quality.SemanticFidelity),
        "info_density", fmt.Sprintf("%.2f", quality.InformationDensity),
        "overall_score", fmt.Sprintf("%.1f", quality.OverallScore),
        // AlignmentMap 统计
        "alignment_map_size", len(result.AlignmentMap),
        "compressed_count", countCompressed(result.AlignmentMap),
        "preserved_count", countPreserved(result.AlignmentMap),
    )
}
```

### 4.5 完整数据流（对齐设计）

```
请求入口（HTTP）
    │
    ├─→ [L3 Audit] 安全检查 + 脱敏
    │       ↓
    │    通过 → 继续
    │       ↓
    ├─→ [TurnWriter] 写入 session_turns（原始消息）
    │       ↓
    ├─→ [SessionCache.GetOrLoad] 读取 L1/L2/L3
    │       ↓
    ├─→ [BuildOutboundMessages] LCS diff + delta-append
    │       ↓
    ├─→ [ShouldTriggerWindow] 检查是否触发压缩
    │       ↓
    ├─→ [tryLLMSummary / MechanicalTrim] 执行压缩
    │       ↓
    ├─→ [ComputeCompressionQuality] 计算质量评分
    │       ↓
    ├─→ [AlignmentMap] 构建对齐映射
    │       ↓
    ├─→ [NeverWorse Guard] 确保不变得更差
    │       ↓
    ├─→ [updateCache] 写回 L1/L2
    │       ↓
    ├─→ [Log Compression Quality] 记录压缩质量日志
    │       ↓
    └─→ 发送给 LLM
```

---

## 5. 实施步骤

### 5.1 第一阶段：定义对齐（不改代码）

- [x] 确认三层缓存语义定义（本文档完成）
- [x] 对齐生产代码与设计意图
- [ ] 明确哪些 gap 需要修复，哪些可以接受现状

### 5.2 第二阶段：增加压缩质量日志

- [ ] 在 `session_compressor.go` 的 `Prepare` 结束时调用 `logCompressionQuality`
- [ ] 扩展 `PrepareResult` 包含 `CompressionQualityScore`
- [ ] 验证日志输出正确

### 5.3 第三阶段：评估是否需要 L3 独立缓存

- [ ] 评估现有 `SessionState.AuditedAt/AuditScore/SecurityScore` 是否足够
- [ ] 如需 L3 独立对象，新增 `AuditedMessages` 字段

---

## 6. 验证标准

1. **结构验证**：`go build ./...` exit 0
2. **日志验证**：触发一次压缩请求后，查日志包含 `compression_quality` 字段
3. **AlignmentMap 验证**：触发摘要后 AlignmentMap 长度 = 原始消息数
4. **质量评分验证**：评分在合理范围内（0-100）

---

## 7. 决策请求

| 决策点 | 选项 | 建议 |
|--------|------|------|
| L3 是否需要独立缓存对象 | A) 新增 `AuditedMessages` 字段；B) 复用现有字段 | 等待确认 |
| 压缩质量评分是否需要持久化 | A) 只记日志；B) 存入 `compression_meta` | 等待确认 |
| 是否需要我现在开始实施 | A) 开始写代码；B) 仅保留方案文档 | 等待确认 |
