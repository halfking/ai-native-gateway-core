# DeepSeek Code / DeepSeek 编程客户端 — 客户端格式要求

## 概述

**DeepSeek 编程客户端**（包含 DeepSeek IDE、DeepSeek Chat、deepseek-cli、DeepSeek-V3.2 Coding 模式等）是 DeepSeek 官方及社区推出的编程 Agent。

⚠️ **2026-09-21 审计新增**：DeepSeek 编程客户端 **此前未在 `internal/clienttype` 和 `telemetry/agent_patterns` 中作为客户端类型登记**。注意：`deepseek` 在 `internal/paramreg/dialect.go:111` 中作为**方言**已登记（用于出站请求到 DeepSeek API），但本次新增的是**客户端识别**（用于入站请求的客户端来源）。

## 入站格式

### 协议选择

DeepSeek 编程客户端的协议选择取决于版本：

| 客户端版本 | 入站协议 |
|-----------|----------|
| DeepSeek IDE / Chat 网页版 | OpenAI Chat Completions（自家 API） |
| DeepSeek CLI / Coding 模式 | OpenAI Chat Completions |
| 社区 deepseek-cli (npm) | OpenAI Chat Completions |
| DeepSeek-V3.2 Coding | OpenAI Chat Completions + reasoning_content |

### HTTP 形态（默认 OpenAI Chat 协议）

```
POST {base_url}/v1/chat/completions HTTP/1.1
Host: gateway.example.com
Authorization: Bearer any-string
User-Agent: DeepSeek-Code/{version} ({platform}; {arch})
Content-Type: application/json
```

`User-Agent` 例子：

```
DeepSeek-Code/1.0.0 (macos; arm64)
DeepSeek-Code/0.5.2 (linux; x86_64)
DeepSeek-IDE/2.0.0 (windows; amd64)
deepseek-cli/0.3.1 (darwin; arm64)
```

### DeepSeek Code 特有 Header

| Header | 说明 |
|--------|------|
| `User-Agent: DeepSeek-...` | **主识别信号** |
| `X-Gw-Client-Type: deepseek-code` | 显式覆盖（可选） |
| `X-DeepSeek-Session-Id` | DeepSeek 会话 ID |
| `X-DeepSeek-Trace-Id` | 单次请求追踪 ID |

## 系统提示词特征

DeepSeek 编程客户端的系统提示词通常包含：

```
You are DeepSeek Code, an interactive coding agent.
You are powered by DeepSeek-V3.
...
```

实际抓取的模式变体：
- `"You are DeepSeek Code, an interactive coding agent"`（主形态）
- `"DeepSeek-Code CLI"`（子代理场景）
- `"DeepSeek IDE coding assistant"`（IDE 模式）
- `"You are DeepSeek Chat coding mode"`（Chat 网页编程模式）
- `"deepseek-coder"`（早期版本）
- `"You are deepseek-v3 coding"`（V3 系列）

## 特征参数

DeepSeek 编程客户端在请求 body 中携带以下特征参数：

| 参数 | 说明 | 唯一性 |
|------|------|--------|
| `model` | 形如 `deepseek-chat` / `deepseek-reasoner` / `deepseek-coder` | **强信号** |
| `reasoning_effort` | DeepSeek Reasoner 强制启用 | 弱信号 |
| `stream` | 恒为 `true`（DeepSeek 编程客户端默认流式） | 弱信号 |
| `temperature` | DeepSeek 编程客户端常用 `0.0` | 弱信号 |
| `tools[].function.name` | DeepSeek 工具（`read_file`, `write_file`, `execute_command`） | **强信号** |
| `metadata.deepseek_session_id` | DeepSeek 会话 ID | **强信号** |

### DeepSeek Code 模型命名约定

```
deepseek-chat           # DeepSeek-V3 对话模型
deepseek-reasoner       # DeepSeek-R1 推理模型（强制流式 + reasoning_content）
deepseek-coder          # DeepSeek-Coder（旧版代码模型）
deepseek-v3             # DeepSeek-V3 显式版本
deepseek-v3-chat        # DeepSeek-V3 Chat 模式
deepseek-v3.2-exp       # DeepSeek-V3.2 实验版
```

模型名以 `deepseek-` 前缀是 **最直接的参数级识别信号**。

## 工具命名空间

DeepSeek 编程客户端的内置工具：

```json
{
  "tools": [
    {"type": "function", "function": {"name": "read_file", "description": "Read file contents"}},
    {"type": "function", "function": {"name": "write_file", "description": "Write file"}},
    {"type": "function", "function": {"name": "execute_command", "description": "Execute shell command"}},
    {"type": "function", "function": {"name": "search_files", "description": "Search files by glob"}},
    {"type": "function", "function": {"name": "edit_file", "description": "Edit existing file"}}
  ]
}
```

工具名采用 `snake_case`，符合 OpenAI 命名规范。

## 网关对 DeepSeek Code 的特殊支持

### 1. 协议转换

DeepSeek Code → 任意上游：与 OpenAI Chat 路径相同。

### 2. reasoning_content 处理

DeepSeek-R1（reasoner）响应包含 `reasoning_content`：

```json
{
  "choices": [{
    "message": {
      "role": "assistant",
      "content": "最终答案",
      "reasoning_content": "思考过程..."
    }
  }]
}
```

网关：
- 解析为 `IR.InternalResponse.ReasoningContent`
- 流式：`delta.reasoning_content` 累积到 `IR.StreamChunk.Delta.ReasoningContent`（R52 勘误：`ReasoningDelta` 符号不存在）
- 序列化到 OpenAI Chat 客户端：保留顶层 `reasoning_content`
- 序列化到 Anthropic：作为 `thinking` block 但**无 signature 不能伪造**

### 3. 工具名规范化

DeepSeek 工具名已经是 snake_case，无需转换。

## 不兼容警告

- ❌ DeepSeek-R1 模型的 reasoning_content 长度极长（可达数十万 token），部分上游（Anthropic）会因为 thinking block 无 signature 而失败
- ❌ DeepSeek-R1 强制流式，非流式响应可能不可用
- ✅ 流式响应（OpenAI SSE）完整支持
- ✅ `reasoning_content` 解析与序列化完整支持
- ✅ 工具调用完整支持

## 网关审计点

1. **User-Agent 必须含 `deepseek-` 或 `DeepSeek-`**：兜底识别路径
2. **系统提示词含 "deepseek code" / "deepseek-coder" / "deepseek ide coding"**：语义识别路径
3. **`model` 字段以 `deepseek-` 开头**：参数识别路径（最直接）
4. **`metadata.deepseek_session_id` 字段**：参数识别路径
5. **`tools[].function.name` 包含 `read_file` / `execute_command` 等**：参数识别路径
6. 五条路径任一命中即视为 DeepSeek 编程客户端
