# 修复：Tool Call 协议转换错误导致 alternating success/transient

**日期**: 2026-06-23  
**问题报告**: 用户在 OpenCode CLI 使用 minimax-m3 模型时，出现"一次成功，一次 transient"交替失败  
**根本原因**: Gateway 错误地将 OpenAI → OpenAI 的请求做了 OpenAI → Anthropic 协议转换  
**影响范围**: 所有使用 `openai-completions` 协议的上游 provider（MiniMax、NVIDIA NIM 等）  

---

## 问题现象

用户会话 `ses_10bf0d6e4ffeKTnHBNBwN0CnTx` 中，同一 session 内的请求出现严格的成功/失败交替：

```
10:52:18 FAIL (transient)  identity: f56663d680b8cfdf
10:52:20 OK                identity: 12dd58c036104ff0
10:52:26 FAIL (transient)  identity: f56663d680b8cfdf
10:52:28 OK                identity: 12dd58c036104ff0
10:52:31 FAIL (transient)  identity: f56663d680b8cfdf
10:52:37 OK                identity: 12dd58c036104ff0
```

所有请求都路由到同一个 provider/credential (14/6 = MiniMax)，但失败请求的错误信息：

```
upstream 400: {"type":"error","error":{"type":"bad_request_error",
"message":"invalid params, tool result's tool id(call-9d838eb3-ff5f-4ab1-9cfc-035f9212942a) not found (2013)"
```

---

## 根本原因分析

### 1. 请求结构

客户端（OpenCode CLI）发送标准的 OpenAI Chat Completions 格式：

```json
{
  "messages": [
    {"role": "user", "content": "..."},
    {"role": "assistant", "content": "", "tool_calls": [
      {"id": "call-9d838eb3...", "type": "function", "function": {...}}
    ]},
    {"role": "tool", "tool_call_id": "call-9d838eb3...", "content": "..."}
  ]
}
```

### 2. 上游 Provider 协议

```sql
SELECT c.id, p.protocol, p.display_name 
FROM credentials c JOIN providers p ON c.provider_id = p.id 
WHERE c.id = 6;

-- Result:
--  id | protocol           | display_name
-- ----+--------------------+-------------
--   6 | openai-completions | MiniMax
```

MiniMax 使用 `openai-completions` 协议，**期望收到 OpenAI 格式**。

### 3. Gateway 错误行为

`routing/executor_anthropic.go` 的 `prepareAnthropicRequestBody` 函数：

```go
// 旧代码（有 bug）
if params.ClientProtocol != "anthropic-messages" && params.ClientProtocol != "" {
    // ❌ 只检查客户端协议，不检查上游协议
    converted, _ := e.ChatToAnthropic(bodyBytes)
    bodyBytes = converted
}

// 无条件执行 Anthropic 特定转换
fixedBytes, _ := transform.FixAnthropicMessages(bodyBytes)
bodyBytes = fixedBytes
```

**错误流程**：
1. 客户端 protocol = `openai-completions`
2. 上游 protocol = `openai-completions`
3. Gateway 看到 `ClientProtocol != "anthropic-messages"` → 触发转换
4. `ChatToAnthropic` 把 `{"role": "tool", "tool_call_id": "call-xxx"}` 转成 `{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call-xxx"}]}`
5. `FixAnthropicMessages` 进一步处理成 Anthropic 格式
6. MiniMax 收到 Anthropic 格式，但它期望 OpenAI 格式 → 报错 `tool_use_id not found`

---

## 修复方案

### 修改文件
`routing/executor_anthropic.go`

### 核心修改

**1. 只在客户端和上游协议不同时才转换**

```go
// 新代码（修复后）
needsConversion := params.ClientProtocol != "anthropic-messages" && 
    params.ClientProtocol != "" && 
    cand.Protocol == "anthropic-messages"  // ← 新增：检查上游协议

if needsConversion && e.IR != nil {
    // IR 路径：Parse OpenAI → IR → Serialize Anthropic
    ...
}

// Legacy 路径
if needsConversion {
    if e.ChatToAnthropic != nil {
        converted, _ := e.ChatToAnthropic(bodyBytes)
        bodyBytes = converted
    }
}
```

**2. 只对 anthropic-messages 上游执行 Anthropic 特定转换**

```go
// 新代码（修复后）
if cand.Protocol == "anthropic-messages" {
    // 只有上游是 Anthropic 时才执行这些转换
    if e.SanitizeAnthropicTools != nil {
        bodyBytes = e.SanitizeAnthropicTools(bodyBytes)
    }
    fixedBytes, _ := transform.FixAnthropicMessages(bodyBytes)
    bodyBytes = fixedBytes
    _ = transform.ValidateAnthropicMessages(bodyBytes)
}
```

---

## 测试验证

### 新增测试
`routing/executor_anthropic_protocol_fix_test.go`

```go
// TestPrepareAnthropicRequestBody_OpenAIToOpenAI
// 验证：客户端 OpenAI → 上游 OpenAI，不做转换
func TestPrepareAnthropicRequestBody_OpenAIToOpenAI(t *testing.T) {
    // 请求包含 tool message
    sourceBody := `{"messages": [..., {"role": "tool", "tool_call_id": "call-abc"}]}`
    
    params := &ExecParams{ClientProtocol: "openai-completions"}
    cand := provider.Candidate{Protocol: "openai-completions"}
    
    result, _ := exec.prepareAnthropicRequestBody(params, cand, sourceBody)
    
    // 断言：仍是 OpenAI 格式
    toolMsg := parseMessages(result)[2]
    assert.Equal(t, "tool", toolMsg["role"])
    assert.Contains(t, toolMsg, "tool_call_id")  // ✓ OpenAI 格式
    assert.NotContains(t, toolMsg, "tool_use_id") // ✗ Anthropic 格式
}
```

### 测试结果
```bash
$ go test ./routing/...
=== RUN   TestPrepareAnthropicRequestBody_OpenAIToOpenAI
--- PASS: TestPrepareAnthropicRequestBody_OpenAIToOpenAI (0.00s)
=== RUN   TestPrepareAnthropicRequestBody_OpenAIToAnthropic
--- PASS: TestPrepareAnthropicRequestBody_OpenAIToAnthropic (0.00s)
PASS
ok  	github.com/kaixuan/llm-gateway-go/routing	3.215s
```

---

## 影响评估

### 受益场景
✅ **OpenCode CLI → MiniMax**（本次报告的场景）  
✅ **任何 OpenAI SDK → openai-completions provider**  
✅ **Cursor/RooCode → NVIDIA NIM**  

### 不受影响场景
✓ **OpenAI SDK → Claude API**（仍会正确转换）  
✓ **Anthropic SDK → Anthropic API**（不需要转换）  
✓ **Anthropic SDK → OpenAI API**（反向转换，不在此函数）  

### 潜在风险
⚠️ **如果有 provider 标记错了 protocol**（比如实际是 Anthropic 但标成 openai-completions），会失去转换  
→ 缓解措施：已有的 `format_conversion.enabled` provider 设置可以强制开启/关闭转换

---

## 部署步骤

### 1. 编译
```bash
cd services/llm-gateway-go
go test ./routing/...  # 确保测试通过
make build
```

### 2. 部署 184 (k3s)
```bash
./scripts/deploy-llm-gateway-go-184.sh
```

### 3. 部署 71 (host docker)
```bash
./scripts/deploy-llm-gateway-go-71.sh
```

### 4. 验证
```bash
# 检查日志，不应该再看到 "tool result's tool id not found" 错误
kubectl -n pms-test logs deploy/llm-gateway-go-deployment -f | grep "tool.*not found"

# 检查成功率
curl -s https://llmgo.kxpms.cn/api/credentials/6 | jq '.recent_success_rate'
```

---

## 回滚方案

如果部署后发现问题：

```bash
# 回滚到上一个 commit
cd services/llm-gateway-go
git revert HEAD
git push

# 重新部署
./scripts/deploy-llm-gateway-go-all.sh
```

或者临时通过 provider 设置禁用转换：

```sql
UPDATE providers 
SET user_overrides_json = jsonb_set(
    user_overrides_json, 
    '{format_conversion,enabled}', 
    'false'
) 
WHERE id = 14;  -- MiniMax
```

---

## 相关文档

- [OpenAI Chat Completions API](https://platform.openai.com/docs/api-reference/chat/create)
- [Anthropic Messages API](https://docs.anthropic.com/claude/reference/messages_post)
- [LLM Gateway Protocol Conversion](../architecture/protocol-conversion.md)
- [Routing Decision Logic](../architecture/routing.md)

---

## Lessons Learned

1. **协议转换必须双向检查**：不能只看客户端协议，必须同时检查上游协议
2. **测试覆盖协议组合**：OpenAI→OpenAI、OpenAI→Anthropic、Anthropic→OpenAI、Anthropic→Anthropic
3. **日志中包含协议信息**：帮助快速诊断此类问题
4. **Provider 配置审计**：定期检查 `protocol` 字段是否正确

---

**修复提交**: `<commit-sha-will-be-filled>`  
**部署时间**: 2026-06-23 19:40 UTC+8  
**验证人**: @xutaohuang
