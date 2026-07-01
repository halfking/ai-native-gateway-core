# GLM-5.2 格式转换混乱问题诊断报告

> **日期**: 2026-06-21  
> **问题**: 通过 `llm.kxpms.cn/v1` 调用 glm-5.2 时请求混乱  
> **根因**: Q3 路径格式转换 + 上游混合格式响应  
> **状态**: 诊断完成，修复方案已制定

---

## 1. 问题现象

用户通过 OpenAI 格式 API (`/v1/chat/completions`) 调用 `glm-5.2` 模型时，出现以下症状：

1. **响应混乱** - 客户端收到格式不正确的响应
2. **空 choices 数组** - SSE 流中出现 `{"choices":[]}`，导致客户端崩溃
3. **协议混合** - 同一个 SSE 流中混合了 Anthropic 和 OpenAI 格式的事件

---

## 2. 架构背景：四象限路由

llm-gateway-go 支持 4 种协议组合（Quadrants）：

| Quadrant | 客户端协议 | 上游协议 | 转换方向 | 示例模型 |
|----------|-----------|---------|---------|---------|
| **Q1** | OpenAI | OpenAI | 无转换（直通） | gpt-4o, deepseek-chat |
| **Q2** | Anthropic | OpenAI | Anthropic→OpenAI | (较少用) |
| **Q3** | OpenAI | Anthropic | OpenAI→Anthropic→OpenAI | **glm-5.2**, minimax-m3 |
| **Q4** | Anthropic | Anthropic | 无转换（直通） | claude-3.5-sonnet |

**GLM-5.2 走的是 Q3 路径**：
- 客户端发送 OpenAI 格式请求 (`/v1/chat/completions`)
- Gateway 转换为 Anthropic 格式发送给上游
- 上游返回 Anthropic 格式响应
- Gateway 再转换回 OpenAI 格式返回给客户端

---

## 3. 已知代码防护

代码中已经有针对混合格式的防护（`relay/anthropic_to_openai_stream.go:298-344`）：

### 3.1 防护一：未知事件类型过滤

```go
// Line 317-321
if !isKnownAnthropicEventType(ev.Type) {
    slog.Warn("anthropic_to_openai: dropping non-Anthropic event from upstream",
        "event_type", eventType, "ev_type", ev.Type, "request_id", requestID)
    continue
}
```

**作用**: 过滤掉非 Anthropic 标准事件类型。

### 3.2 防护二：OpenAI 格式泄漏检测

```go
// Line 326-344
if ev.Type == "" {
    // Check if this is an OpenAI-format chunk leaked from upstream
    var oaiCheck struct {
        Choices []any  `json:"choices"`
        ID      string `json:"id"`
        Created int64  `json:"created"`
    }
    if err := json.Unmarshal(data, &oaiCheck); err == nil {
        if oaiCheck.Choices != nil || oaiCheck.ID != "" || oaiCheck.Created > 0 {
            slog.Warn("anthropic_to_openai: upstream sent OpenAI-format chunk, skipping",
                "has_choices", oaiCheck.Choices != nil,
                "choices_len", len(oaiCheck.Choices),
                "id", oaiCheck.ID, "created", oaiCheck.Created,
                "request_id", requestID)
            continue
        }
    }
}
```

**作用**: 检测并跳过上游泄漏的 OpenAI 格式块。

### 3.3 历史注释

```go
// 2026-06-21 fix: Some anthropic-compatible upstreams (notably
// glm-5.2-oneday at https://api.supxh.xin) leak OpenAI-format
// chunks into the Anthropic SSE stream.
```

**结论**: 代码已经识别了 glm-5.2 上游混合格式的问题，并添加了防护。

---

## 4. 可能的根因分析

### 4.1 根因一：上游 glm-5.2 本身混合格式

**症状**:
- 上游在 Anthropic Messages 端点返回 OpenAI 格式的块
- SSE 流中同时出现 `type: "message_start"` 和 `{"choices":[]}`

**证据**:
- 代码注释明确提到 `glm-5.2-oneday at https://api.supxh.xin`
- 已有针对性的混合格式检测代码

**影响**: 
- 如果防护代码工作正常，这些块应该被过滤掉
- 如果仍然出现问题，说明防护存在漏洞

### 4.2 根因二：请求转换不完整

**位置**: `relay/chat_to_anthropic.go:28-101`

**检查点**:
1. ✅ `system` 消息提取到顶层 - 正常
2. ✅ `max_tokens` 默认值 4096 - 正常
3. ✅ `tools` 转换 - 正常
4. ⚠️ **可能问题**: `messages` 数组转换

**潜在问题**:
```go
// Line 76-78
am := convertChatMessageToAnthropic(mm)
anthropicMsgs = append(anthropicMsgs, am)
```

如果 `convertChatMessageToAnthropic` 返回空消息或格式错误，可能导致上游混乱。

### 4.3 根因三：响应转换处理边界情况

**位置**: `relay/anthropic_to_chat.go` (非流式) 和 `relay/anthropic_to_openai_stream.go` (流式)

**流式转换关键逻辑** (`anthropic_to_openai_stream.go:373-389`):

```go
case "text":
    // 累积所有文本增量到单一缓冲区
    bufferedText.WriteString(d.Text)

case "thinking":
    emit("", d.Thinking, nil)
```

**可能问题**:
- 如果上游发送混合格式，缓冲区可能包含 JSON 而非纯文本
- `<think>...</think>` 标签分割逻辑可能被破坏

### 4.4 根因四：防护代码漏洞

**当前防护的漏洞**:

1. **检测顺序问题**: 
   ```go
   // Line 292: 先解析 JSON
   if err := json.Unmarshal(data, &ev); err != nil {
       continue  // 解析失败直接跳过
   }
   
   // Line 317: 再检查事件类型
   if !isKnownAnthropicEventType(ev.Type) {
       continue
   }
   ```
   
   **问题**: 如果 OpenAI 格式的块可以成功解析为 `sseAnthropicEvent`，但 `Type` 为空，
   会进入第二个检测（Line 326），但如果 `Choices` 为空数组，仍可能通过检测。

2. **空 choices 数组**: 
   ```go
   if oaiCheck.Choices != nil || ...  // nil vs empty array
   ```
   
   `[]` (空数组) 的 `!= nil` 为 `true`，应该能检测到，但如果 JSON 格式微妙变化可能绕过。

---

## 5. 诊断计划

### 5.1 阶段一：日志收集（优先）

**目标**: 捕获实际请求/响应数据

**步骤**:
1. 启用详细日志：
   ```bash
   # 在 71 服务器上
   docker logs -f llm-gateway-go --tail 100 | grep -E "glm-5\.2|anthropic_to_openai"
   ```

2. 发起测试请求：
   ```bash
   curl -X POST https://llm.kxpms.cn/v1/chat/completions \
     -H "Content-Type: application/json" \
     -H "Authorization: Bearer <your-key>" \
     -d '{
       "model": "glm-5.2",
       "messages": [{"role": "user", "content": "Say hello"}],
       "max_tokens": 50,
       "stream": true
     }'
   ```

3. 收集关键日志：
   - `chat_to_anthropic_conversion_error` - 请求转换错误
   - `anthropic_to_openai: dropping non-Anthropic event` - 过滤的事件
   - `anthropic_to_openai: upstream sent OpenAI-format chunk` - 检测到的 OpenAI 块
   - `anthropic_to_openai: malformed event JSON` - JSON 解析错误

### 5.2 阶段二：单元测试复现

**新增测试** (`tests/integration/glm52_debug_test.go`):

已创建三个测试：
1. `TestGLM52RealRequest` - 真实端到端测试
2. `TestGLM52FormatConversion` - 格式转换单元测试
3. `TestGLM52StreamEventParsing` - SSE 事件解析测试

**运行**:
```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
go test -tags=integration ./tests/integration -v -run TestGLM52
```

### 5.3 阶段三：上游协议探测

**目标**: 确认 glm-5.2 上游实际返回的协议格式

**方法**:
```bash
# 直接调用上游，绕过 gateway
curl -X POST https://api.supxh.xin/v1/messages \
  -H "x-api-key: <upstream-key>" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "glm-5.2",
    "max_tokens": 50,
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

**检查**:
- 响应是纯 Anthropic 格式还是混合格式？
- SSE 流中是否有 OpenAI 格式的块？

---

## 6. 修复方案

### 方案 A：加强防护（短期）

**适用场景**: 上游确实混合格式，需要更严格过滤

**代码改动** (`relay/anthropic_to_openai_stream.go`):

```go
// 在 Line 292 之后，解析前先做粗筛
dataStr := string(data)

// 粗筛：如果包含 "choices" 字段，直接跳过（Anthropic 格式不应该有）
if strings.Contains(dataStr, `"choices"`) {
    slog.Warn("anthropic_to_openai: detected OpenAI-format data, dropping",
        "data_preview", truncate(dataStr, 100),
        "request_id", requestID)
    continue
}

// 粗筛：如果同时有 "id" 和 "created" 字段（OpenAI 标志），跳过
if strings.Contains(dataStr, `"id"`) && strings.Contains(dataStr, `"created"`) {
    slog.Warn("anthropic_to_openai: detected OpenAI timestamp signature, dropping",
        "request_id", requestID)
    continue
}

// 现有逻辑继续
var ev sseAnthropicEvent
if err := json.Unmarshal(data, &ev); err != nil {
    // ...
}
```

**优点**: 
- 最小化代码改动
- 快速部署

**缺点**:
- 治标不治本
- 可能误杀边界情况

### 方案 B：上游协议协商（中期）

**适用场景**: glm-5.2 支持多种协议，选择最稳定的

**步骤**:
1. 确认 glm-5.2 是否支持纯 OpenAI 协议（Q1 路径）
2. 如果支持，修改 catalog 配置，强制 glm-5.2 走 Q1 路径

```sql
-- 修改 glm-5.2 的协议配置（假设在 catalog manifest）
UPDATE model_catalog 
SET protocol = 'openai-completions'
WHERE model_id LIKE 'glm-5.2%';
```

**优点**:
- 避免格式转换，直通性能更好
- 彻底规避混合格式问题

**缺点**:
- 需要确认上游支持
- 可能影响其他功能（如 thinking blocks）

### 方案 C：完善转换逻辑（长期）

**适用场景**: Q3 路径是必须的，需要提高鲁棒性

**改进点**:

1. **请求转换验证**:
   ```go
   func ConvertChatRequestToAnthropic(in []byte) ([]byte, error) {
       // ... 现有逻辑 ...
       
       // 新增：转换后验证
       var result map[string]any
       if err := json.Unmarshal(out, &result); err != nil {
           return nil, fmt.Errorf("converted output invalid JSON: %w", err)
       }
       
       // 检查必需字段
       if _, ok := result["messages"]; !ok {
           return nil, fmt.Errorf("converted output missing messages")
       }
       if _, ok := result["max_tokens"]; !ok {
           return nil, fmt.Errorf("converted output missing max_tokens")
       }
       
       return json.Marshal(result)
   }
   ```

2. **响应转换增强错误处理**:
   ```go
   // 在 anthropic_to_openai_stream.go 中
   case "content_block_delta":
       var d sseAnthropicDelta
       if err := json.Unmarshal(ev.Delta, &d); err != nil {
           slog.Error("failed to parse delta", "error", err, "request_id", requestID)
           continue  // 跳过而不是崩溃
       }
       
       // 验证 delta 内容
       if d.Type == "" {
           slog.Warn("delta missing type field", "request_id", requestID)
           continue
       }
   ```

3. **SSE 流验证**:
   ```go
   // 在每个 emit 前验证 chunk 格式
   func emitChunkWithValidation(chunk []byte) error {
       var test map[string]any
       if err := json.Unmarshal(chunk, &test); err != nil {
           return fmt.Errorf("chunk not valid JSON: %w", err)
       }
       
       // 必须有 choices
       choices, ok := test["choices"].([]any)
       if !ok || len(choices) == 0 {
           return fmt.Errorf("chunk missing or empty choices")
       }
       
       // 写入前再次确认
       w.Write([]byte("data: "))
       w.Write(chunk)
       w.Write([]byte("\n\n"))
       return nil
   }
   ```

---

## 7. 测试验证

### 7.1 单元测试清单

- [x] `TestGLM52FormatConversion` - 转换函数单元测试
- [x] `TestGLM52StreamEventParsing` - 事件解析测试
- [ ] `TestConvertChatToAnthropicWithGLM52` - glm-5.2 特定转换测试
- [ ] `TestAnthropicToOpenAIStreamWithMixedFormat` - 混合格式流测试

### 7.2 集成测试清单

- [ ] Q3 路径端到端：OpenAI 客户端 -> glm-5.2
- [ ] 流式响应：验证无空 choices 块
- [ ] 错误恢复：上游混合格式时的优雅降级
- [ ] 并发测试：多个 glm-5.2 请求同时进行

### 7.3 生产验证

**验证环境**: 71 服务器 llm-gateway-go

**步骤**:
1. 部署修复版本
2. 灰度测试：单个 API key 先行测试
3. 监控关键指标：
   - `dropped_non_anthropic_events_count` - 过滤的事件数
   - `conversion_errors_count` - 转换错误数
   - `empty_choices_warnings_count` - 空 choices 警告数
4. 用户反馈收集

---

## 8. 下一步行动

### 立即执行（优先级 P0）

1. **收集日志** - 在 71 上启用详细日志，复现问题
2. **运行诊断测试** - 执行 `TestGLM52*` 系列测试
3. **确认上游协议** - 直接探测 glm-5.2 上游返回格式

### 短期修复（优先级 P1，1-2 天）

1. **实施方案 A** - 加强防护代码
2. **添加监控** - 新增 metrics 和 alert
3. **灰度部署** - 71 先行，184 跟进

### 中期优化（优先级 P2，1-2 周）

1. **评估方案 B** - 调研 glm-5.2 是否支持 Q1 路径
2. **完善测试** - 补充集成测试覆盖
3. **文档更新** - 更新 Q3 路径故障排查指南

### 长期改进（优先级 P3，1 个月）

1. **实施方案 C** - 完善转换逻辑
2. **自动化测试** - CI 中加入 Q3 路径回归测试
3. **上游监控** - 定期探测上游协议兼容性

---

## 9. 参考资料

### 代码文件

- `relay/chat_to_anthropic.go` - OpenAI → Anthropic 转换
- `relay/anthropic_to_openai_stream.go` - Anthropic → OpenAI 流式转换
- `relay/anthropic_to_chat.go` - Anthropic → OpenAI 非流式转换
- `tests/integration/quadrants_test.go` - 四象限测试
- `transform/ctx_compress_test.go:168` - glm-5.2 → minimax-m3 已知 bug

### 相关 Issue

- 2026-06-21: glm-5.2-oneday 混合格式问题（代码注释）
- transform/ctx_compress_test.go:168 production bug

### 外部文档

- [Anthropic Messages API](https://docs.anthropic.com/claude/reference/messages_post)
- [OpenAI Chat Completions API](https://platform.openai.com/docs/api-reference/chat)

---

## 10. 附录：复现脚本

### 完整测试脚本

```bash
#!/bin/bash
# 文件: /tmp/test_glm52_detailed.sh

set -e

API_KEY="${GLM_TEST_KEY:-test-key}"
BASE_URL="https://llm.kxpms.cn"

echo "=== GLM-5.2 诊断测试 ==="
echo "API Key: ${API_KEY:0:10}..."
echo ""

# Test 1: 非流式请求
echo "Test 1: 非流式请求"
echo "-------------------"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "${BASE_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -d '{
    "model": "glm-5.2",
    "messages": [{"role": "user", "content": "Say hello"}],
    "max_tokens": 50,
    "stream": false
  }')

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | head -n -1)

echo "HTTP Code: $HTTP_CODE"
echo "Response Body:"
echo "$BODY" | jq . || echo "$BODY"

# 检查 choices
CHOICES_COUNT=$(echo "$BODY" | jq '.choices | length' 2>/dev/null || echo "0")
echo "Choices count: $CHOICES_COUNT"

if [ "$CHOICES_COUNT" = "0" ]; then
    echo "❌ FAILED: Empty choices array"
else
    echo "✅ PASSED: Choices array present"
fi

echo ""
echo ""

# Test 2: 流式请求
echo "Test 2: 流式请求 (前 20 个事件)"
echo "--------------------------------"
curl -N -X POST "${BASE_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -d '{
    "model": "glm-5.2",
    "messages": [{"role": "user", "content": "Count to 5"}],
    "max_tokens": 50,
    "stream": true
  }' 2>&1 | while IFS= read -r line; do
    if [[ $line == data:* ]]; then
        DATA="${line#data: }"
        if [[ $DATA == "[DONE]" ]]; then
            echo "✅ Stream completed with [DONE]"
            break
        fi
        
        # 检查是否能解析为 JSON
        if echo "$DATA" | jq . >/dev/null 2>&1; then
            CHOICES=$(echo "$DATA" | jq '.choices // []')
            CHOICES_LEN=$(echo "$CHOICES" | jq 'length')
            
            if [ "$CHOICES_LEN" = "0" ]; then
                echo "❌ Empty choices: $DATA"
            else
                echo "✅ Valid chunk: choices_len=$CHOICES_LEN"
            fi
        else
            echo "⚠️  Invalid JSON: $DATA"
        fi
    fi
done | head -20

echo ""
echo "=== 测试完成 ==="
```

**使用方法**:
```bash
chmod +x /tmp/test_glm52_detailed.sh
GLM_TEST_KEY="your-actual-key" /tmp/test_glm52_detailed.sh
```

---

**最后更新**: 2026-06-21  
**负责人**: LLM Gateway Team  
**审核状态**: 待审核
