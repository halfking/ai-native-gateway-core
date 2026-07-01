# 自适应响应格式转换器 - 实现文档

## 问题背景

184 环境出现客户端响应格式不匹配的问题:
- 客户端通过 `/v1/chat/completions` 请求,但期待 Responses API 格式
- 导致错误: `Type validation failed: expected "response.output_text.delta"`
- 成功率: 71 环境 99.64%, 184 环境 93.56%

## 解决方案

实现**会话级别的自适应响应格式转换器**,自动检测和记忆每个会话的正确响应格式。

---

## 核心机制

### 1. 会话格式偏好记忆 (`ResponseFormatPreference`)

```go
type ResponseFormatPreference struct {
    prefs  map[string]string    // session_id → format
    expiry map[string]time.Time // 24小时过期
}
```

**功能**:
- 记录每个会话成功的响应格式
- 24小时自动过期
- 线程安全(RWMutex)

### 2. 自适应转换策略 (`AdaptiveResponseConverter`)

**多级回退策略**:

```
1. 检查会话偏好
   ├─ 有偏好 → 使用偏好格式
   │   ├─ 成功 → 返回
   │   └─ 失败 → 清除偏好,进入步骤2
   └─ 无偏好 → 进入步骤2

2. 尝试检测到的客户端协议
   ├─ 成功 → 记录偏好,返回
   └─ 失败 → 进入步骤3

3. 按顺序尝试所有格式
   ├─ openai-chat
   ├─ openai-responses  
   └─ anthropic-messages
   
   第一个成功的 → 记录偏好,返回
   
4. 所有格式都失败 → 返回错误
```

---

## 核心代码

### 文件结构

```
domains/streaming/
├── response_format_adapter.go       # 核心实现(272行)
└── response_format_adapter_test.go  # 测试(270行)
```

### 关键接口

```go
// ConvertResponse 自适应转换响应
func (arc *AdaptiveResponseConverter) ConvertResponse(
    ctx context.Context,
    sessionID string,           // 会话ID,用于记忆偏好
    clientProtocol string,      // 检测到的客户端协议
    upstreamBody []byte,        // 上游响应body
    upstreamProtocol string,    // 上游协议
    clientModel string,         // 客户端模型名
) ([]byte, string, error)      // 返回: body, 使用的格式, 错误
```

### 使用示例

```go
// 初始化(在 main.go 中)
prefs := streaming.NewResponseFormatPreference()
adapter := streaming.NewAdaptiveResponseConverter(prefs, irConverter)

// 在 handler 中使用
sessionID := extractSessionID(r)
convertedBody, usedFormat, err := adapter.ConvertResponse(
    ctx,
    sessionID,
    clientProtocol,      // 来自 DetectProtocol
    upstreamRespBody,
    cand.Protocol,       // 上游协议
    clientModel,
)
if err != nil {
    // 所有格式都失败,返回错误
    return err
}

// 记录使用的格式(用于监控)
slog.Info("response_converted",
    "session_id", sessionID,
    "format", usedFormat,
    "client_protocol", clientProtocol)
```

---

## 测试覆盖

### 测试场景

1. **SessionMemory** - 会话偏好记忆
   - Set/Get/Delete
   - 24小时过期

2. **OpenAIToAnthropic** - 正常转换
   - 第一次请求: 使用检测协议
   - 第二次请求: 使用记录的偏好

3. **FormatFallback** - 格式回退
   - OpenAI 格式失败
   - 自动回退到 Anthropic
   - 记录 Anthropic 偏好

4. **AllFormatsFail** - 所有格式失败
   - 返回错误
   - 不记录偏好

5. **ValidateResponseFormat** - 格式验证
   - OpenAI: 检查 `choices` 字段
   - Anthropic: 检查 `content` 字段
   - Responses: 检查 `output_text` 字段

### 测试结果

```bash
$ go test -v -run "TestResponseFormat|TestAdaptiveResponse" ./domains/streaming/
PASS
✅ TestResponseFormatPreference_SessionMemory
✅ TestAdaptiveResponseConverter_OpenAIToAnthropic
✅ TestAdaptiveResponseConverter_FormatFallback
✅ TestAdaptiveResponseConverter_AllFormatsFail
✅ TestValidateResponseFormat
ok  	github.com/kaixuan/llm-gateway-go/domains/streaming	0.531s
```

---

## 集成到现有代码

### 步骤 1: 初始化适配器

在 `cmd/gateway/main.go` 中:

```go
// 在 main() 函数开始处
responsePrefs := streaming.NewResponseFormatPreference()
var responseAdapter *streaming.AdaptiveResponseConverter

// 在 IR converter 初始化后
if irConverter != nil {
    responseAdapter = streaming.NewAdaptiveResponseConverter(responsePrefs, irAdapter)
}
```

### 步骤 2: 修改 handler

在 `domains/streaming/handler.go` 的非流式响应处理中:

```go
// 在收到上游响应后
if responseAdapter != nil && cand.Protocol != clientProtocol {
    convertedBody, usedFormat, err := responseAdapter.ConvertResponse(
        r.Context(),
        sessionID,
        clientProtocol,
        upstreamBody,
        cand.Protocol,
        clientModel,
    )
    if err != nil {
        slog.Error("adaptive_conversion_failed",
            "session_id", sessionID,
            "error", err)
        // 回退: 直接返回上游body
    } else {
        upstreamBody = convertedBody
        slog.Info("adaptive_conversion_succeeded",
            "session_id", sessionID,
            "format", usedFormat)
    }
}
```

### 步骤 3: 启用环境变量

确保 `LLM_GATEWAY_IR_CONVERTER=true` 已设置。

---

## 监控指标

### 关键日志

1. **response_format_preference_set** - 记录偏好
   ```
   session_id, format, expires_at
   ```

2. **using_session_format_preference** - 使用偏好
   ```
   session_id, preferred_format
   ```

3. **session_format_preference_failed** - 偏好失败
   ```
   session_id, preferred_format, error
   ```

4. **detected_format_failed** - 检测格式失败
   ```
   session_id, client_protocol, error
   ```

5. **fallback_format_succeeded** - 回退成功
   ```
   session_id, format, client_protocol
   ```

### 监控查询(Prometheus/Grafana)

```promql
# 格式回退率
rate(log{msg="fallback_format_succeeded"}[5m])
/ rate(log{msg="response_format_preference_set"}[5m])

# 格式失败率
rate(log{msg="session_format_preference_failed"}[5m])
/ rate(log{msg="using_session_format_preference"}[5m])
```

---

## 性能影响

### 内存占用

- 每个会话: ~100 bytes (session_id + format + expiry)
- 1万活跃会话: ~1 MB
- 自动清理: 每小时清理过期会话

### CPU 开销

- 格式转换: 已有 IR 转换,无额外开销
- 偏好查询: O(1) map 查询
- 可忽略不计

---

## 优势

1. **零配置** - 自动检测和适配
2. **会话记忆** - 第二次请求直接使用正确格式
3. **渐进式回退** - 多级尝试,最大化成功率
4. **可观测性** - 详细日志,方便排查
5. **向后兼容** - 不影响现有逻辑,可选启用

---

## 局限性

1. **首次请求可能重试** - 需要尝试多种格式
2. **依赖 session_id** - 无 session_id 的请求每次都尝试
3. **24小时过期** - 客户端更换格式后最多24小时才重新检测

---

## 下一步

1. **部署到 184 环境**
2. **观察日志**,确认格式回退是否工作
3. **对比成功率**,预期 184 成功率提升到 99%+
4. **收集会话格式分布**,分析客户端类型

---

## 相关文件

- `domains/streaming/response_format_adapter.go`
- `domains/streaming/response_format_adapter_test.go`
- `internal/ir/detect_audit_test.go` (协议检测审计)
- `internal/ir/simulate_sonnet5_test.go` (端到端模拟)

---

**作者**: OpenCode  
**日期**: 2026-07-02  
**版本**: v1.0
