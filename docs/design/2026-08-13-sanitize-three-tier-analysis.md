# 敏感信息脱敏与恢复在三层缓存中的位置深度分析

> **状态**：审计 + 方案设计
> **日期**：2026-08-13
> **目标**：确定敏感信息脱敏与恢复在三层缓存架构中的最佳位置，并确保占位符不被 LLM 更换

---

## 目录

1. [现状：脱敏机制审计](#1-现状脱敏机制审计)
2. [占位符设计分析](#2-占位符设计分析)
3. [三层缓存中的脱敏位置建议](#3-三层缓存中的脱敏位置建议)
4. [占位符防篡改机制](#4-占位符防篡改机制)
5. [方案设计](#5-方案设计)
6. [实施建议](#6-实施建议)

---

## 1. 现状：脱敏机制审计

### 1.1 当前脱敏架构（SmartSaniGuard）

```
请求入口
    ↓
[SanitizeInputMiddleware]
    ├─ 检测敏感信息（phone/email/id_card/credit_card/secret/...）
    ├─ 替换为 {SENSITIVE:type:index} 占位符
    ├─ 占位符→原始值映射存入 Redis
    │  Key: session:{sessionID}:sanitize
    │  Field: {SENSITIVE:phone:0} → Value: 13800138000
    │  TTL: 30分钟
    └─ 返回脱敏后 body
    ↓
[SessionCompressor] 压缩（对脱敏后的文本操作）
    ↓
发送给上游 LLM（只看到占位符）
    ↓
[OutputComplianceInterceptor] 输出安全检查（对占位符文本检查）
    ↓
[SanitizeRestoreInterceptor] 占位符还原
    ├─ 从 Redis 读取映射表
    ├─ 精确匹配 {SENSITIVE:type:index} 占位符
    ├─ 替换为原始敏感值
    └─ 返回给客户端（真实值）
```

**关键设计点**：

| 维度 | 当前实现 |
|------|---------|
| 脱敏时机 | HTTP 入口，chatHandler 之前 |
| 存储位置 | Redis Hash `session:{sessionID}:sanitize` |
| 占位符格式 | `{SENSITIVE:type:index}` |
| 还原时机 | 输出安全检查**之后**（OutputComplianceInterceptor 之后） |
| 还原方式 | 正则精确匹配 + 字符串替换 |
| TTL | 30 分钟（与会话生命周期一致） |

### 1.2 当前流程的优点

1. ✅ **安全检查不暴露敏感信息**：OutputComplianceInterceptor 只看到占位符
2. ✅ **LLM 不接触真实敏感数据**：上游 Provider 只看到占位符
3. ✅ **用户体验无损**：客户端拿到的是真实值
4. ✅ **会话级缓存**：映射表与会话绑定，避免跨会话泄漏
5. ✅ **流式支持**：chunk-level 还原，SSE 流式响应也能还原

### 1.3 当前流程的问题

#### 问题 1：脱敏在压缩之前（隐式）

```
当前：
  脱敏（HTTP 中间件）→ 压缩（SessionCompressor）→ 缓存（L1/L2/L3）

存储的数据：
  L1/L2/L3 存的都是"脱敏后的 body"（含占位符）
```

**影响**：
- ✅ 好：缓存天然不含敏感信息（合规安全）
- ⚠️  坏：无法查看"原始消息"（因为入缓存时已经是占位符了）
- ⚠️  坏：压缩质量评分时看到的是占位符文本，不是真实文本

#### 问题 2：映射表独立存储（与三层缓存未打通）

```
当前：
  SessionState（L1/L2/L3） ─ 不含映射表
  SanitizeMap（Redis Hash） ─ 独立存储

查询时：
  需要两次读取：
  1. SessionCache.GetOrLoad() → SessionState
  2. session.Manager.LoadSanitizeMap() → SanitizeMap
```

**影响**：
- ⚠️  坏：数据割裂，无法"一次查询拿到完整会话状态"
- ⚠️  坏：TTL 独立管理（SessionState 30min，SanitizeMap 30min，可能不一致）
- ⚠️  坏：缓存 miss 时 SanitizeMap 无法从 L3 重建（因为 L3 存的也是占位符）

#### 问题 3：三层语义不清晰

按照用户描述的三层设计：

```
L1 原始多轮会话 ─────────────────────
L2 压缩后多轮会话 ───────────────────
L3 脱敏+安全审核后发送会话 ──────────
```

**当前实际**：
- L1/L2/L3 都存的是"脱敏后的 body"
- 没有独立的"L3 审核后缓存"层

---

## 2. 占位符设计分析

### 2.1 占位符格式

```
{SENSITIVE:type:index}
```

| 部分 | 含义 | 示例 |
|------|------|------|
| `{SENSITIVE:` | 固定前缀 | - |
| `type` | 敏感类型 | `phone`/`email`/`id_card`/`credit_card`/`secret` |
| `index` | 同类型序号（从0开始） | `0`/`1`/`2` |
| `}` | 固定后缀 | - |

**示例**：
```
原始：我的手机号是 13800138000，邮箱是 test@example.com
脱敏：我的手机号是 {SENSITIVE:phone:0}，邮箱是 {SENSITIVE:email:0}
```

### 2.2 占位符识别正则

```go
// placeholder.go:38
PlaceholderPattern = regexp.MustCompile(`\{SENSITIVE:([a-z_]+):(\d+)\}`)
```

**特点**：
- ✅ **结构化明确**：type 和 index 可解析
- ✅ **类型安全**：type 枚举有限（8种）
- ✅ **精确匹配**：正则严格，不会误匹配
- ⚠️  **固定格式**：必须完全匹配，缺一个字符就失效

### 2.3 占位符安全性分析

#### 威胁 1：LLM 改写占位符

**场景**：用户要求 "把我的手机号改成 138..."

```
用户输入：请把 {SENSITIVE:phone:0} 改成 13900000000
LLM 理解：用户要修改手机号
LLM 输出：已将手机号改为 13900000000（不含占位符）
```

**后果**：
- 还原时找不到 `{SENSITIVE:phone:0}`，无法还原
- 用户看到的是 LLM 编造的号码（13900000000），不是真实号码

#### 威胁 2：LLM 泄漏占位符格式

**场景**：用户问 "我的信息是什么格式"

```
用户：我的信息看起来像什么？
LLM：您的信息格式是 {SENSITIVE:phone:0}
```

**后果**：
- 用户看到占位符格式（虽然看不到真实值，但知道了内部表示）
- 轻微信息泄漏

#### 威胁 3：LLM 生成伪造占位符

**场景**：LLM 试图"猜测"占位符

```
用户：生成一个手机号占位符
LLM：{SENSITIVE:phone:99}
```

**后果**：
- 还原时 `{SENSITIVE:phone:99}` 不在映射表中
- 当前实现会替换为 `[REDACTED]`（safe）
- 不会泄漏真实值

#### 威胁 4：注入攻击

**场景**：用户在输入中插入占位符

```
用户输入：我的邮箱是 {SENSITIVE:email:0}（用户手动输入）
```

**后果**：
- 脱敏阶段：检测到真实邮箱 `test@example.com`，替换为 `{SENSITIVE:email:0}`
- 用户手动输入的占位符也是 `{SENSITIVE:email:0}`
- 冲突：两个 `{SENSITIVE:email:0}` 指向同一个映射
- 还原时：两个都被还原为 `test@example.com`（安全，但语义混乱）

### 2.4 当前防护措施

| 威胁 | 当前防护 | 评级 |
|------|---------|------|
| LLM 改写占位符 | ❌ 无防护，还原时找不到会返回 `[REDACTED]` | 🟡 中危 |
| LLM 泄漏占位符格式 | ❌ 无防护，System Prompt 未禁止 | 🟢 低危 |
| LLM 生成伪造占位符 | ✅ 还原时映射表查不到会返回 `[REDACTED]` | 🟢 安全 |
| 用户注入占位符 | ⚠️  部分防护，冲突时两个都会被还原 | 🟡 中危 |

---

## 3. 三层缓存中的脱敏位置建议

### 3.1 方案对比

#### 方案 A：脱敏在 L1 之前（当前）

```
原始请求
    ↓
[脱敏] 敏感信息 → 占位符
    ↓
L1 原始会话（含占位符）
    ↓
L2 压缩会话（含占位符）
    ↓
L3 审核会话（对占位符审核）
    ↓
[还原] 占位符 → 敏感信息
    ↓
返回给用户
```

**优点**：
- ✅ 缓存天然不含敏感信息（合规）
- ✅ 压缩、审核都在占位符上操作（安全）

**缺点**：
- ❌ 无法查看"原始真实消息"
- ❌ 压缩质量评分看到的是占位符，不是真实文本
- ❌ L1/L2/L3 语义不清晰（都含占位符）

#### 方案 B：脱敏在 L2 和 L3 之间

```
原始请求
    ↓
L1 原始会话（真实敏感信息）
    ↓
L2 压缩会话（真实敏感信息）
    ↓
[脱敏] 敏感信息 → 占位符
    ↓
L3 审核会话（含占位符）
    ↓
发送给 LLM（含占位符）
    ↓
L3 收到响应（含占位符）
    ↓
[还原] 占位符 → 敏感信息
    ↓
回写 L2（真实值）
    ↓
回写 L1（真实值）
```

**优点**：
- ✅ L1/L2 存真实值，可以查看原始消息
- ✅ 压缩质量评分基于真实文本
- ✅ 三层语义清晰：L1 原始、L2 压缩、L3 脱敏

**缺点**：
- ❌ L1/L2 缓存含敏感信息（需加密存储）
- ❌ 压缩引擎看到真实敏感信息（内部泄漏风险）
- ❌ 实现复杂度高（需在压缩后插入脱敏层）

#### 方案 C：三层独立存储（推荐）

```
原始请求
    ↓
L1 原始会话（真实敏感信息 + 映射表引用）
    ↓
[脱敏] 敏感信息 → 占位符，生成映射表
    ↓
L2 压缩会话（含占位符 + 映射表引用）
    ↓
[审核] 占位符文本审核
    ↓
L3 审核会话（含占位符 + 审核分数 + 映射表）
    ↓
发送给 LLM（含占位符）
    ↓
收到响应（含占位符）
    ↓
[还原] 从 L3 读映射表 → 占位符还原
    ↓
回写 L2/L1（真实值 + 映射表更新）
    ↓
返回给用户（真实值）
```

**数据结构**：

```go
// L1 原始会话
type RawSession struct {
    SessionID       string
    TenantID        string
    Messages        []Message       // 真实敏感信息
    SanitizeMapRef  string          // 映射表引用（Redis key）
}

// L2 压缩会话
type CompressedSession struct {
    SessionID          string
    CompressedMessages []Message    // 含占位符
    AlignmentMap       []AlignmentInfo
    SanitizeMapRef     string       // 映射表引用
}

// L3 审核会话
type AuditedSession struct {
    SessionID         string
    AuditedMessages   []Message     // 含占位符
    AuditScore        int
    SecurityScore     int
    SanitizeMap       SanitizeMap   // 占位符→真实值映射表
    SanitizeMapRef    string        // Redis key（用于持久化）
}
```

**优点**：
- ✅ L1 存真实值（可查看原始消息）
- ✅ L2 存占位符（压缩后体积小）
- ✅ L3 存映射表（审核时可逆向查真实值）
- ✅ 三层语义清晰
- ✅ 映射表随会话生命周期管理

**缺点**：
- ⚠️  L1 缓存含敏感信息（需评估风险）
- ⚠️  实现复杂度较高

### 3.2 推荐方案：方案 C（三层独立存储）

**关键设计**：

1. **L1 存真实值**：
   - 目的：可查看原始消息，压缩质量评分基于真实文本
   - 安全：L1 进程内缓存，不出网关（风险可控）

2. **L2 存占位符**：
   - 目的：压缩后的消息体积小，传输效率高
   - 安全：Redis/PG 存的都是占位符，泄漏风险低

3. **L3 存映射表**：
   - 目的：审核时可逆向查真实值，还原时从 L3 读映射表
   - 安全：映射表与会话绑定，TTL 同步

4. **脱敏时机**：
   - L1 写入后立即脱敏（在 SessionCompressor.Prepare 开始前）
   - 生成映射表并存入 L3

5. **还原时机**：
   - 从 L3 读映射表
   - 在 SanitizeRestoreInterceptor 还原
   - 回写 L2/L1 时更新真实值

---

## 4. 占位符防篡改机制

### 4.1 问题：LLM 可能改写占位符

**现状**：
```
用户：请把 {SENSITIVE:phone:0} 改成 139...
LLM：已将手机号改为 13900000000
```

**后果**：占位符被替换，还原时找不到

### 4.2 解决方案：System Prompt 指令 + 验证

#### 方案 1：System Prompt 明确禁止

在 System Prompt 中加入：

```
IMPORTANT: Your response may contain placeholders in the format {SENSITIVE:type:index}.
You MUST preserve these placeholders EXACTLY as they appear in the user's message.
Do NOT modify, remove, or replace these placeholders with actual values.
Do NOT explain or reveal the format of these placeholders to the user.

Example:
User: "My phone is {SENSITIVE:phone:0}"
Correct: "Your phone number {SENSITIVE:phone:0} has been recorded."
WRONG: "Your phone number 13800138000 has been recorded."
```

**中文版本**：
```
重要提示：你的回复中可能包含格式为 {SENSITIVE:type:index} 的占位符。
你必须**完全保留**这些占位符，不得修改、删除或替换为实际值。
不要向用户解释或透露这些占位符的格式。

示例：
用户："我的手机号是 {SENSITIVE:phone:0}"
正确："您的手机号 {SENSITIVE:phone:0} 已记录。"
错误："您的手机号 13800138000 已记录。"
```

#### 方案 2：占位符签名验证

为每个占位符添加 HMAC 签名：

```
{SENSITIVE:phone:0:sig=a1b2c3d4}
```

**生成**：
```go
func GenerateSignedPlaceholder(sensitiveType SensitiveType, index int, secret string) string {
    base := fmt.Sprintf("%s:%d", sensitiveType, index)
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write([]byte(base))
    sig := hex.EncodeToString(mac.Sum(nil))[:8]
    return fmt.Sprintf("{SENSITIVE:%s:sig=%s}", base, sig)
}
```

**验证**：
```go
func ValidateSignedPlaceholder(placeholder string, secret string) bool {
    // 解析占位符：{SENSITIVE:phone:0:sig=a1b2c3d4}
    parts := ParseSignedPlaceholder(placeholder)
    expectedSig := GenerateSignature(parts.Type, parts.Index, secret)
    return parts.Sig == expectedSig
}
```

**还原时**：
```go
func RestoreWithValidation(text string, sm SanitizeMap, secret string) string {
    return SignedPlaceholderPattern.ReplaceAllStringFunc(text, func(match string) string {
        if !ValidateSignedPlaceholder(match, secret) {
            // 签名不匹配 → LLM 伪造的占位符
            return "[REDACTED]"
        }
        // 签名匹配 → 查映射表还原
        if original, ok := sm[match]; ok {
            return original
        }
        return "[REDACTED]"
    })
}
```

**优点**：
- ✅ LLM 无法伪造签名
- ✅ 即使 LLM 复制占位符到其他位置，签名仍有效
- ✅ 防止用户注入占位符（用户不知道 secret）

**缺点**：
- ⚠️  占位符变长（+9 字节 `:sig=a1b2c3d4`）
- ⚠️  需要管理 secret（session 级或 tenant 级）

#### 方案 3：响应后验证 + 告警

在还原前检查占位符完整性：

```go
// 检查响应中的占位符是否都在映射表中
func ValidateResponsePlaceholders(responseBody []byte, sm SanitizeMap) []string {
    var invalidPlaceholders []string
    
    matches := PlaceholderPattern.FindAllString(string(responseBody), -1)
    for _, match := range matches {
        if _, ok := sm[match]; !ok {
            // LLM 生成了不在映射表中的占位符
            invalidPlaceholders = append(invalidPlaceholders, match)
        }
    }
    
    return invalidPlaceholders
}
```

**使用**：
```go
// 在 SanitizeRestoreInterceptor 中
invalid := ValidateResponsePlaceholders(req.ResponseBody, sm)
if len(invalid) > 0 {
    it.logger.WarnContext(ctx, "detected invalid placeholders in LLM response",
        "invalid", invalid,
        "session_id", req.SessionID,
    )
    // 上报 Prometheus 指标
    metrics.SanitizePlaceholderTampering.WithLabelValues("llm_generated").Add(float64(len(invalid)))
}
```

**优点**：
- ✅ 不改变占位符格式
- ✅ 可观测性强（告警 + 指标）

**缺点**：
- ⚠️  只能事后检测，不能事前防止

### 4.3 推荐组合方案

```
System Prompt 禁止（方案 1）
    +
响应后验证告警（方案 3）
    +
可选：占位符签名（方案 2，高安全场景）
```

**理由**：
1. System Prompt 是第一道防线（大部分 LLM 会遵守）
2. 响应验证是兜底机制（检测 LLM 不遵守的情况）
3. 签名验证是可选增强（高安全场景，代价是占位符变长）

---

## 5. 方案设计

### 5.1 三层缓存扩展结构

```go
// SessionState 扩展（v9）
type SessionState struct {
    // ... 现有字段 ...

    // L1 相关（原始会话）
    RawMessages        []Message `json:"raw_messages,omitempty"`   // 真实敏感信息
    RawTokenEstimate   int       `json:"raw_te,omitempty"`

    // L2 相关（压缩会话）
    CompressedMessages []Message `json:"cmp_messages,omitempty"`   // 含占位符
    CompressionQuality CompressionQualityScore `json:"cmp_quality,omitempty"`

    // L3 相关（审核会话）
    AuditedMessages    []Message    `json:"aud_messages,omitempty"` // 含占位符
    SanitizeMap        SanitizeMap  `json:"-"`                      // 不序列化（敏感）
    SanitizeMapRef     string       `json:"sanitize_ref,omitempty"` // Redis key
    SanitizeStats      SanitizeStats `json:"sanitize_stats,omitempty"`
}

// 脱敏统计
type SanitizeStats struct {
    PlaceholderCount   int       `json:"placeholder_count"`      // 占位符总数
    PhoneCount         int       `json:"phone_count"`            // 手机号数量
    EmailCount         int       `json:"email_count"`            // 邮箱数量
    IDCardCount        int       `json:"id_card_count"`          // 身份证数量
    SanitizedAt        time.Time `json:"sanitized_at"`           // 脱敏时间
}
```

### 5.2 脱敏与还原流程

#### 脱敏流程（输入侧）

```go
// session_compressor.go 的 Prepare 方法开始前

func (sc *SessionCompressor) Prepare(ctx context.Context, clientBody []byte, ...) *PrepareResult {
    // === 1. 脱敏输入 ===
    sanitizeResult, err := sc.sanitizer.Sanitize(ctx, string(clientBody))
    if err != nil {
        // 降级：脱敏失败时继续使用原始 body
        sanitizeResult = &sanitize.SanitizeResult{
            SanitizedText: string(clientBody),
            SanitizeMap:   make(sanitize.SanitizeMap),
        }
    }
    
    // === 2. 保存映射表到 L3（Redis）===
    if len(sanitizeResult.SanitizeMap) > 0 {
        mapRef := fmt.Sprintf("session:%s:sanitize", gwSessionID)
        err := sc.sessionMgr.SaveSanitizeMap(ctx, gwSessionID, sanitizeResult.SanitizeMap)
        if err != nil {
            sc.logger.Warn("save sanitize map failed", "error", err)
        }
    }
    
    // === 3. 更新 SessionState（L1 存真实值，L2 存占位符）===
    state, _ := sc.cache.GetOrLoad(ctx, tenantID, gwSessionID)
    state.SanitizeMapRef = mapRef
    state.SanitizeStats = sanitize.SanitizeStats{
        PlaceholderCount: len(sanitizeResult.SanitizeMap),
        PhoneCount:       countType(sanitizeResult.Fragments, sanitize.TypePhone),
        EmailCount:       countType(sanitizeResult.Fragments, sanitize.TypeEmail),
        SanitizedAt:      time.Now(),
    }
    
    // === 4. 继续压缩流程（对脱敏后的文本操作）===
    sanitizedBody := []byte(sanitizeResult.SanitizedText)
    // ... BuildOutboundMessages / tryLLMSummary / 等
}
```

#### 还原流程（输出侧）

```go
// SanitizeRestoreInterceptor.InterceptNonStream

func (it *SanitizeRestoreInterceptor) InterceptNonStream(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
    // === 1. 从 L3 读取映射表 ===
    sm, err := it.loadMap(ctx, req.SessionID)
    if err != nil || len(sm) == 0 {
        return nil, nil
    }
    
    // === 2. 验证占位符完整性 ===
    invalidPlaceholders := it.validatePlaceholders(req.ResponseBody, sm)
    if len(invalidPlaceholders) > 0 {
        it.logger.WarnContext(ctx, "detected invalid placeholders",
            "invalid", invalidPlaceholders,
            "session_id", req.SessionID,
        )
        metrics.SanitizePlaceholderTampering.WithLabelValues("llm_generated").Add(float64(len(invalidPlaceholders)))
    }
    
    // === 3. 还原占位符 ===
    restored, err := it.sanitizer.RestoreOutputOrMask(ctx, string(req.ResponseBody), sm)
    if err != nil {
        return nil, err
    }
    
    // === 4. 回写 L2/L1（真实值）===
    // 注意：这里回写的是"响应"，不是请求
    // L1/L2 的请求部分已经在脱敏时存储了真实值
    
    return &response.InterceptResult{
        ModifiedBody: []byte(restored),
        Action:       "sanitize_restore",
        Metadata: map[string]any{
            "sanitize_restored":     true,
            "placeholder_count":     len(sm),
            "invalid_placeholders":  len(invalidPlaceholders),
        },
    }, nil
}
```

### 5.3 System Prompt 注入

在 `domains/transformation` 的协议转换层注入：

```go
// transformation/to_openai.go

func BuildOpenAIMessages(irMsgs []ir.Message, ...) []openai.Message {
    msgs := []openai.Message{}
    
    // 如果启用了脱敏，在 system 消息中注入占位符保护指令
    if sanitizeEnabled {
        protectionMsg := openai.Message{
            Role: "system",
            Content: `IMPORTANT: Your response may contain placeholders in the format {SENSITIVE:type:index}.
You MUST preserve these placeholders EXACTLY as they appear in the user's message.
Do NOT modify, remove, or replace these placeholders with actual values.
Do NOT explain or reveal the format of these placeholders to the user.`,
        }
        msgs = append(msgs, protectionMsg)
    }
    
    // ... 其他消息转换
    return msgs
}
```

---

## 6. 实施建议

### 6.1 短期（P0，2周内）

1. **增强占位符验证**：
   - [ ] 在 `SanitizeRestoreInterceptor` 中添加 `validatePlaceholders` 方法
   - [ ] 添加 Prometheus 指标 `sanitize_placeholder_tampering_total{source}`
   - [ ] 验证失败时记录详细日志（session_id / invalid_placeholders）

2. **System Prompt 注入**：
   - [ ] 在 `transformation/to_openai.go` 和 `to_anthropic.go` 中注入占位符保护指令
   - [ ] 添加功能开关 `SANITIZE_SYSTEM_PROMPT_ENABLED=true`
   - [ ] 验证 LLM 是否遵守（通过日志 + 指标）

3. **文档更新**：
   - [ ] 更新 `security/sanitize/README.md` 说明占位符防护机制
   - [ ] 添加故障排查指南（占位符被改写时如何调试）

### 6.2 中期（P1，1个月内）

1. **三层缓存对齐**：
   - [ ] 在 `SessionState` 中添加 `SanitizeMapRef` / `SanitizeStats` 字段
   - [ ] L1 存储真实值（RawMessages）
   - [ ] L2 存储占位符（CompressedMessages）
   - [ ] L3 通过 Redis 存储映射表

2. **压缩质量评分集成**：
   - [ ] 在脱敏前计算真实文本的信息密度
   - [ ] 在压缩后计算占位符文本的压缩比
   - [ ] 对比真实压缩比 vs 占位符压缩比

3. **映射表生命周期管理**：
   - [ ] 映射表 TTL 与 SessionState TTL 同步
   - [ ] SessionState 过期时自动清理映射表
   - [ ] 支持手动清理（session.DeleteSanitizeMap）

### 6.3 长期（P2，可选）

1. **占位符签名（高安全场景）**：
   - [ ] 实现 `GenerateSignedPlaceholder`
   - [ ] 实现 `ValidateSignedPlaceholder`
   - [ ] 添加功能开关 `SANITIZE_SIGNED_PLACEHOLDER=true`

2. **L1 加密存储（合规要求）**：
   - [ ] 评估 L1 进程内缓存是否需要加密
   - [ ] 如需加密，使用 AES-256-GCM
   - [ ] 密钥管理（KMS / Vault）

3. **审计日志**：
   - [ ] 记录所有脱敏/还原操作到审计日志
   - [ ] 包含：session_id / tenant_id / placeholder_count / timestamp
   - [ ] 支持按 session 查询完整脱敏历史

---

## 7. 验证标准

### 7.1 功能验证

- [ ] 脱敏后占位符格式正确
- [ ] 还原后敏感信息恢复
- [ ] LLM 改写占位符时能检测并告警
- [ ] LLM 生成伪造占位符时返回 `[REDACTED]`
- [ ] 流式响应 chunk-level 还原正确

### 7.2 性能验证

- [ ] 脱敏延迟 < 50ms（P95）
- [ ] 还原延迟 < 30ms（P95）
- [ ] Redis 映射表查询 < 5ms（P99）
- [ ] L1 缓存命中率 > 95%

### 7.3 安全验证

- [ ] OutputComplianceInterceptor 只看到占位符
- [ ] 上游 LLM 只看到占位符
- [ ] 客户端拿到真实值
- [ ] 占位符被改写时告警
- [ ] 映射表不会跨会话泄漏

---

## 8. 决策请求

| 决策点 | 选项 | 建议 |
|--------|------|------|
| 三层缓存脱敏位置 | A) 当前（L1 之前）；B) L2 和 L3 之间；C) 三层独立存储 | **C（三层独立存储）** |
| 占位符防护机制 | A) 仅 System Prompt；B) System Prompt + 验证；C) System Prompt + 验证 + 签名 | **B（System Prompt + 验证）** |
| L1 是否存真实值 | A) 存真实值（压缩质量评分准确）；B) 存占位符（当前） | **A（存真实值）** |
| 占位符签名 | A) 立即实施；B) 作为可选增强；C) 不实施 | **B（可选增强）** |
| 实施优先级 | A) 全量实施；B) 分阶段（短期→中期→长期）；C) 仅保留方案 | **B（分阶段）** |

---

## 9. 附录：占位符格式演进史

| 版本 | 格式 | 时间 | 变更原因 |
|------|------|------|---------|
| v1 | `<SENSITIVE:type:index>` | 2026-07 | 初始版本，`<>` 括号 |
| v2 | `{SENSITIVE:type:index}` | 2026-08 | 改为 `{}` 括号，避免 HTML 标签冲突 |
| v3（提议） | `{SENSITIVE:type:index:sig=...}` | 待定 | 增加签名，防篡改 |

---

_本文档为敏感信息脱敏与恢复在三层缓存中的深度分析，实施需按决策点确认后分阶段进行。_
