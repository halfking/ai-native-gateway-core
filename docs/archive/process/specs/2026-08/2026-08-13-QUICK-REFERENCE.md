# 三层缓存系统快速参考

> **版本**：v8 (Phase 1 & 2 已完成)
> **日期**：2026-08-13
> **状态**：生产就绪 ✅

---

## 🎯 核心概念

### 三层语义

```
L1 原始会话（Raw Session）
  ├─ RawTokenEstimate  # 脱敏前 token 估算
  └─ RawMsgCount       # 原始消息数
       ↓ [脱敏] 
L2 压缩会话（Compressed Session）
  ├─ CompressedTokens      # 压缩后 token 数
  ├─ CompressedMsgs        # 压缩后消息数
  └─ CompressionQuality    # 6 维度质量评分
       ↓ [审核]
L3 审核会话（Audited Session）
  ├─ SanitizeMapRef    # Redis key: session:{id}:sanitize
  └─ SanitizeStats     # 脱敏统计（各类计数）
```

### 数据流

```
用户请求
  ↓
[SanitizeInputMiddleware]
  ├─ 脱敏检测（手机/邮箱/身份证/信用卡/密钥）
  ├─ 生成占位符（{SENSITIVE:type:index}）
  ├─ 注入 System Prompt 保护
  └─ 保存映射表到 Redis (L3)
  ↓
[CompressionHook]
  ├─ 计算原始指标 (L1)
  ├─ 执行压缩（滑动窗口 + 语义保真）
  ├─ 计算压缩质量 (L2)
  └─ 保存 SessionState 到 Redis
  ↓
发送给 LLM（占位符 + System Prompt）
  ↓
[SanitizeRestoreInterceptor]
  ├─ 从 Redis 读取映射表 (L3)
  ├─ 还原占位符为真实值
  └─ 回写 L2/L1
  ↓
返回给用户
```

---

## 📋 SessionState v8 结构

```go
type SessionState struct {
    // === L1: 原始会话（真实值） ===
    RawTokenEstimate int `json:"raw_te,omitempty"`
    RawMsgCount      int `json:"raw_mc,omitempty"`
    
    // === L2: 压缩会话（占位符） ===
    CompressedTokens   int                     `json:"cmp_te,omitempty"`
    CompressedMsgs     int                     `json:"cmp_mc,omitempty"`
    CompressionQuality CompressionQualityScore `json:"cmp_quality,omitempty"`
    
    // === L3: 审核会话（脱敏映射） ===
    SanitizeMapRef string        `json:"sanitize_ref,omitempty"`
    SanitizeStats  SanitizeStats `json:"sanitize_stats,omitempty"`
}

type CompressionQualityScore struct {
    TokenSavingsPercent float64 `json:"token_savings_pct"`
    MsgReductionPercent float64 `json:"msg_reduction_pct"`
    SemanticFidelity    float64 `json:"semantic_fidelity"`
    ContextCompleteness float64 `json:"context_completeness"`
    InfoDensity         float64 `json:"info_density"`
    UserIntentClarity   float64 `json:"user_intent_clarity"`
    OverallScore        float64 `json:"overall_score"`
}

type SanitizeStats struct {
    PlaceholderCount int   `json:"placeholder_count"`
    PhoneCount       int   `json:"phone_count,omitempty"`
    EmailCount       int   `json:"email_count,omitempty"`
    IDCardCount      int   `json:"id_card_count,omitempty"`
    CreditCardCount  int   `json:"cc_count,omitempty"`
    SecretCount      int   `json:"secret_count,omitempty"`
    SanitizedAt      int64 `json:"sanitized_at"`
}
```

---

## 🔍 Redis 数据结构

### SessionState 缓存

```bash
# Key: session:sc:tenant123:sess_abc:v1
# Type: Hash
HGETALL session:sc:tenant123:sess_abc:v1

# 字段示例
{
  "raw_te": "8500",
  "raw_mc": "30",
  "cmp_te": "3200",
  "cmp_mc": "12",
  "cmp_quality": "{...}",
  "sanitize_ref": "session:tenant123:sess_abc:sanitize",
  "sanitize_stats": "{\"placeholder_count\":5,\"phone_count\":2,...}"
}
```

### 脱敏映射表

```bash
# Key: session:tenant123:sess_abc:sanitize
# Type: Hash
HGETALL session:tenant123:sess_abc:sanitize

# 字段示例
{
  "{SENSITIVE:phone:0}": "13800138000",
  "{SENSITIVE:phone:1}": "13900139000",
  "{SENSITIVE:email:0}": "user@example.com"
}
```

### 占位符偏移量

```bash
# Key: session:tenant123:sess_abc:sanitize:offset
# Type: Hash
HGETALL session:tenant123:sess_abc:sanitize:offset

# 字段示例
{
  "phone": "1",    # 下一个 phone 占位符用 index=2
  "email": "0",    # 下一个 email 占位符用 index=1
  "id_card": "0"
}
```

---

## 🛠️ 环境变量

```bash
# === 脱敏功能开关 ===
SANITIZE_SYSTEM_PROMPT_ENABLED=true  # System Prompt 注入（默认启用）

# === 压缩质量评分权重 ===
COMPRESSION_WEIGHT_TOKEN_SAVINGS=0.30
COMPRESSION_WEIGHT_MSG_REDUCTION=0.15
COMPRESSION_WEIGHT_SEMANTIC=0.25
COMPRESSION_WEIGHT_CONTEXT=0.15
COMPRESSION_WEIGHT_INFO_DENSITY=0.10
COMPRESSION_WEIGHT_INTENT=0.05

# === Redis 配置 ===
REDIS_ADDR=localhost:6379
REDIS_PASSWORD=
REDIS_DB=0
SESSION_CACHE_TTL=86400  # 24 小时
```

---

## 📊 监控指标

### 关键日志

```bash
# 1. 占位符验证告警
tail -f /var/log/llm-gateway-go/app.log | grep "invalid placeholders"

# 2. 压缩质量评分
tail -f /var/log/llm-gateway-go/app.log | grep "compression_quality"

# 3. 脱敏统计
tail -f /var/log/llm-gateway-go/app.log | grep "sanitize_stats"

# 4. System Prompt 注入
tail -f /var/log/llm-gateway-go/app.log | grep "placeholder_protection"
```

### 典型日志格式

```json
// 压缩质量
{
  "level": "info",
  "msg": "compression_quality",
  "session_id": "sess_abc123",
  "token_savings_pct": 62.4,
  "msg_reduction_pct": 60.0,
  "semantic_fidelity": 85.0,
  "context_completeness": 90.0,
  "info_density": 45.0,
  "user_intent_clarity": 75.0,
  "overall_score": 67.8
}

// 脱敏统计
{
  "level": "info",
  "msg": "sanitize_stats",
  "session_id": "sess_abc123",
  "placeholder_count": 5,
  "phone_count": 2,
  "email_count": 3,
  "sanitized_at": 1723545600
}

// 占位符验证告警
{
  "level": "warn",
  "msg": "invalid placeholders detected",
  "session_id": "sess_abc123",
  "invalid_placeholders": [
    "{SENSITIVE:phone:999}",
    "{SENSITIVE:fake:0}"
  ],
  "action": "rejected"
}
```

---

## 🔐 System Prompt 保护

### 注入内容

```text
IMPORTANT: The user's message may contain placeholders in the format {SENSITIVE:type:index}.

You MUST:
- Preserve these placeholders EXACTLY as they appear
- Do NOT modify, remove, or replace these placeholders with actual values
- Do NOT explain or reveal the format of these placeholders to the user
- Reference placeholders naturally in your response when needed

Example:
User: "My phone is {SENSITIVE:phone:0}"
✓ Correct: "Your phone number {SENSITIVE:phone:0} has been recorded."
✗ WRONG: "Your phone number 13800138000 has been recorded."
```

### 注入时机

- 检测到敏感信息生成占位符时
- 自动注入到第一条 `system` 消息
- 如无 `system` 消息，在数组开头插入

### 开关控制

```bash
# 启用（默认）
export SANITIZE_SYSTEM_PROMPT_ENABLED=true

# 禁用
export SANITIZE_SYSTEM_PROMPT_ENABLED=false
```

---

## 🧪 验证方法

### 1. 验证三层字段

```bash
# 查看 SessionState
redis-cli HGETALL "session:sc:tenant123:sess_abc:v1"

# 预期输出应包含
✓ raw_te (L1)
✓ raw_mc (L1)
✓ cmp_te (L2)
✓ cmp_mc (L2)
✓ cmp_quality (L2)
✓ sanitize_ref (L3)
✓ sanitize_stats (L3)
```

### 2. 验证脱敏映射

```bash
# 查看映射表
redis-cli HGETALL "session:tenant123:sess_abc:sanitize"

# 预期输出
✓ 占位符 → 真实值
✓ 格式：{SENSITIVE:type:index}
```

### 3. 验证压缩质量

```bash
# 查看日志
tail -f /var/log/llm-gateway-go/app.log | grep "compression_quality"

# 预期输出
✓ token_savings_pct > 0
✓ overall_score: 0-100
✓ 6 个维度都有值
```

### 4. 验证 System Prompt

```bash
# 开发环境抓包或日志
# 检查发送给 LLM 的请求

# 预期
✓ messages[0].role == "system"
✓ messages[0].content 包含 "IMPORTANT: The user's message may contain placeholders"
```

---

## 🚨 故障排查

### 问题 1：占位符无法还原

**症状**：用户收到带占位符的回复

**排查**：
```bash
# 1. 检查 Redis 连接
redis-cli PING

# 2. 检查映射表是否存在
redis-cli EXISTS "session:tenant123:sess_abc:sanitize"

# 3. 检查 TTL
redis-cli TTL "session:tenant123:sess_abc:sanitize"

# 4. 检查日志
tail -f /var/log/llm-gateway-go/app.log | grep "restore.*failed"
```

**解决方案**：
- 确认 Redis 可达
- 确认 TTL 未过期（默认 24 小时）
- 检查还原拦截器是否正常注册

### 问题 2：压缩质量评分异常

**症状**：`overall_score` 为 0 或异常值

**排查**：
```bash
# 检查日志
tail -f /var/log/llm-gateway-go/app.log | grep "compression_quality"

# 检查各维度值
cat /var/log/llm-gateway-go/app.log | grep "compression_quality" | jq '.token_savings_pct, .semantic_fidelity'
```

**解决方案**：
- 确认 `RawTokenEstimate` / `CompressedTokens` 都有值
- 检查权重配置是否正确
- 确认 6 个维度都在 0-100 范围

### 问题 3：System Prompt 未注入

**症状**：LLM 泄露占位符格式

**排查**：
```bash
# 1. 检查功能开关
echo $SANITIZE_SYSTEM_PROMPT_ENABLED

# 2. 检查是否检测到敏感信息
tail -f /var/log/llm-gateway-go/app.log | grep "sanitize.*fragments"

# 3. 检查消息数组
# 开发环境抓包看请求体
```

**解决方案**：
- 确认 `SANITIZE_SYSTEM_PROMPT_ENABLED=true`
- 确认脱敏中间件正常运行
- 检查注入逻辑是否被执行

---

## 📈 性能指标

| 操作 | 预期延迟 | 实测延迟 |
|------|---------|---------|
| 占位符验证 | < 1ms | TBD |
| 压缩质量计算 | < 5ms | TBD |
| System Prompt 注入 | < 1ms | TBD |
| SanitizeStats 构建 | < 1ms | TBD |
| Redis 读写 | < 2ms | TBD |
| **端到端** | **< 10ms** | **TBD** |

---

## 📚 相关文档

### 设计文档

- [三层缓存审计](./2026-08-13-three-tier-cache-audit.md)
- [敏感信息脱敏分析](./2026-08-13-sanitize-three-tier-analysis.md)
- [实施计划](./2026-08-13-implementation-plan.md)

### 完成报告

- [Phase 1 完成报告](./2026-08-13-phase1-completion-report.md)
- [Phase 2 完成报告](./2026-08-13-phase2-completion-report.md)
- [Phase 1 & 2 最终总结](./2026-08-13-PHASE1-2-FINAL-SUMMARY.md)

### 代码文件

- `domains/hooks/compression/session_cache.go` - SessionState v8
- `domains/hooks/compression/sanitize_stats.go` - 脱敏统计
- `domains/hooks/compression/quality_score.go` - 质量评分
- `security/sanitize/system_prompt.go` - System Prompt 注入
- `security/sanitize/smart_sani_guard.go` - 脱敏中间件
- `security/sanitize/placeholder.go` - 占位符验证

---

## 🎓 最佳实践

### 1. 监控关键指标

```bash
# 每日检查
- 压缩质量评分趋势
- 占位符告警频率
- 脱敏统计分布

# 每周检查
- Redis 内存使用
- 缓存命中率
- 性能基线对比
```

### 2. 调优建议

- 根据压缩质量评分调整窗口大小
- 根据脱敏统计调整规则优先级
- 根据性能数据调整 TTL

### 3. 安全建议

- 定期审计占位符验证日志
- 监控 System Prompt 注入成功率
- 定期轮换 Redis 密码

---

**准备就绪，可部署到生产环境！** 🚀

_如有问题，请参考完整文档或联系开发团队。_
