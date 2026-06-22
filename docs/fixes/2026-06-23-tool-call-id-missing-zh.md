# claude-sonnet-4-6 工具调用修复总结

## 问题现象

使用 `claude-sonnet-4-6` 模型时，工具调用返回错误：

```
Tool execution was interrupted during streaming recovery before this tool was executed.
Expected 'id' to be a string.
```

## 根本原因

**Anthropic SSE 流式事件顺序**：
1. `content_block_start` (tool_use) → 包含完整的 `ID`、`Name`、初始 `input`
2. `content_block_delta` (input_json_delta) → **只包含增量 JSON**，不含 ID
3. `content_block_stop` → 结束当前内容块

**代码缺陷** (`relay/anthropic_to_openai_stream.go`):
- 第 449 行：`emitToolCall("", "", d.InputJSON)` — `input_json` delta 时传入空 ID
- 第 492 行：`emitToolCall("", "", bufferedToolArgs)` — 刷新累积参数时传入空 ID
- 第 278-280 行：`if toolID != "" { entry["id"] = toolID }` — toolID 为空时不设置 `id` 字段

**影响**：OpenAI 工具调用格式**强制要求**每个 `tool_calls` chunk 必须有 `id` 字段。缺少会导致客户端解析失败。

## 修复方案

### 1. 缓存工具调用 ID (第 122 行)
```go
var (
    // ... 其他变量 ...
    currentToolCallID string  // 缓存当前 tool_use 块的 ID 供 delta 更新使用
)
```

### 2. 在 content_block_start 时缓存 ID (第 428 行)
```go
case "content_block_start":
    if err := json.Unmarshal(data, &evt); err == nil && evt.ContentBlock.Type == "tool_use" {
        currentToolCallID = evt.ContentBlock.ID  // ✅ 缓存 ID
        emitToolCall(evt.ContentBlock.ID, evt.ContentBlock.Name, evt.ContentBlock.InputRaw)
    }
```

### 3. input_json delta 使用缓存的 ID (第 450 行)
```go
case "input_json":
    emitToolCall(currentToolCallID, "", d.InputJSON)  // ✅ 使用缓存的 ID
```

### 4. 刷新 input_json_delta 时使用缓存的 ID (第 493 行)
```go
case "content_block_stop":
    if bufferedToolArgs.Len() > 0 {
        emitToolCall(currentToolCallID, "", json.RawMessage(bufferedToolArgs.String()))
        bufferedToolArgs.Reset()
    }
    currentToolCallID = ""  // ✅ 清空缓存
```

### 5. 添加兜底 ID 生成 (第 278-287 行)
```go
if toolID != "" {
    entry["id"] = toolID
} else {
    // ✅ 兜底：生成合成 ID
    entry["id"] = fmt.Sprintf("call_%s_%d", requestID, idx)
}
```

## 测试验证

### 新增测试
- `TestAnthropicToOpenAIStream_ToolCallIDPersistence` ✅ 通过
  - 验证所有 tool_calls chunk 都包含非空字符串 `id`
  - 确认所有 chunk 使用同一个来自 `content_block_start` 的 ID
  - 结果：**成功验证 2 个 tool_call chunks，都使用 ID 'toolu_xyz789'**

- `TestAnthropicToOpenAIStream_ToolCallIDFallback` ✅ 通过
  - 测试兜底 ID 生成机制

### 已有测试全部通过
- `TestAnthropicToOpenAIStream_ToolCallsFinishReason` ✅
- `TestAnthropicToOpenAIStream_InconsistentToolCalls` ✅
- `TestAnthropicToOpenAIStream_ConsistentToolCalls` ✅
- `TestStreamingToolCallsE2E` ✅

### 编译验证
```bash
cd services/llm-gateway-go
go test ./relay -v -run ToolCall     # ✅ 所有测试通过
go build -o /tmp/llm-gateway-go ./cmd/gateway  # ✅ 编译成功
```

## 部署计划

### 部署步骤
1. ✅ **代码已提交** (commit `53dcf1c6`)
2. **构建新镜像** 
3. **部署到 184 k3s** (`llmgo.kxpms.cn`)
4. **部署到 71 systemd** (`llm.kxpms.cn`)
5. **验证** 使用 claude-sonnet-4-6 测试工具调用

### 验证命令
```bash
# 测试 claude-sonnet-4-6 工具调用
curl -X POST https://llmgo.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer <api-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4-6",
    "messages": [{"role": "user", "content": "检查 codegraph 是否安装"}],
    "tools": [{"type": "function", "function": {"name": "bash", "parameters": {"type": "object", "properties": {"command": {"type": "string"}}}}}],
    "stream": true
  }'

# 预期：所有 tool_calls chunk 包含 "id" 字段，无 "Expected 'id' to be a string" 错误
```

## 影响范围

- **受影响模型**：claude-sonnet-4-6，以及其他使用 `input_json_delta` 进行流式工具参数传输的 Anthropic 模型
- **客户端影响**：所有 OpenAI 兼容客户端（Cursor、Continue、OpenAI SDK 等）
- **严重程度**：**高** - 工具调用功能完全失效
- **修复状态**：✅ **已完成** - ID 追踪和兜底生成已实现

## 相关修复

- **之前的修复** (2026-06-23, 第 528-544 行)：检测不一致的 `finish_reason:"tool_calls"`（当没有实际发送 tool_use 块时）
- **当前修复**：确保流式响应中的 tool_use 块始终具有有效的 `id` 字段

## 文件清单

- 修改：`services/llm-gateway-go/relay/anthropic_to_openai_stream.go` (4 处)
- 新增：`services/llm-gateway-go/relay/anthropic_to_openai_stream_tool_id_test.go`
- 新增：`services/llm-gateway-go/docs/fixes/2026-06-23-tool-call-id-missing.md`
