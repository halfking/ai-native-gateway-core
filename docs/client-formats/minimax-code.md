# MiniMax Code — 客户端格式要求

## 概述

**MiniMax Code** 是 MiniMax（MiniMax）的官方编程 Agent CLI/IDE，提供：
- 命令行编程助手（`mm-code` 命令）
- IDE 插件（VSCode / JetBrains）
- Web 编程助手（MiniMax 编程页面）

⚠️ **2026-09-21 审计新增**：MiniMax Code **此前未在 `internal/clienttype` 和 `telemetry/agent_patterns` 中登记**，本次审计新增 `minimax-code` 命名值和完整 pattern 集合。

## 入站格式

### 协议选择

MiniMax Code 默认走 **Anthropic Messages** 协议（与 Claude Code 同源），但当目标上游是 MiniMax M2/M3 时会回退到 OpenAI Chat 兼容协议。

### HTTP 形态（默认 Anthropic 协议）

```
POST {base_url}/v1/messages HTTP/1.1
Host: gateway.example.com
x-api-key: any-string
anthropic-version: 2023-06-01
anthropic-beta: prompt-caching-2024-07-31, interleaved-thinking-2025-05-14
User-Agent: MiniMax-Code/{version} ({platform}; {arch})
Content-Type: application/json
```

`User-Agent` 例子：

```
MiniMax-Code/1.0.0 (macos; arm64) AppleWebKit/537.36
MiniMax-Code/0.9.5 (linux; x86_64)
MiniMax-Code/2.1.0-beta (windows; amd64)
```

### MiniMax Code 特有 Header

| Header | 说明 |
|--------|------|
| `User-Agent: MiniMax-Code/...` | **主识别信号** |
| `X-Gw-Client-Type: minimax-code` | 显式覆盖（可选） |
| `X-Code-Session-Id` | MiniMax Code 会话 ID（UUID4） |
| `X-Code-Trace-Id` | 单次请求追踪 ID |

## 系统提示词特征

MiniMax Code 的首条 system message 包含以下自报身份：

```
You are MiniMax Code, Anthropic's official CLI for MiniMax.
You are an interactive coding agent.
...
```

实际抓取的模式变体：
- `"You are MiniMax Code, Anthropic's official CLI for MiniMax"`（主形态）
- `"MiniMax-Code CLI"`（子代理场景）
- `"MiniMax Code Interactive"`（交互式会话）
- `"You are MiniMax Code by Anthropic"`（简化变体）

## 特征参数

MiniMax Code 在请求 body 中携带以下特征参数：

| 参数 | 说明 | 唯一性 |
|------|------|--------|
| `model` | 形如 `claude-sonnet-4.6` / `claude-opus-4.1` | 不唯一 |
| `metadata.user_id` | MiniMax Code 用户标识（`u_xxx`） | 弱信号 |
| `metadata.session_id` | MiniMax Code 会话 ID | **强信号** |
| `metadata.client_type` | 恒为 `minimax_code` | **强信号** |
| `system[].cache_control` | MiniMax Code 主动加 cache 提示 | 弱信号 |
| `thinking.type` | MiniMax Code 默认启用扩展思考 | 弱信号 |
| `tools[].name` | 命名空间 `mcp__` / MiniMax Code 自带工具 | **强信号** |
| `betas` 数组 | MiniMax Code 主动发 beta 列表 | 弱信号 |

### MiniMax Code 特有 metadata

```json
{
  "metadata": {
    "user_id": "u_01HXXXXXXXXXXXX",
    "session_id": "sess_01HYYYYYYYYYY",
    "client_type": "minimax_code"
  }
}
```

`client_type: "minimax_code"` 是 **最可靠的参数级识别信号**。

## 工具命名空间

MiniMax Code 内置工具：

```json
{
  "tools": [
    {"name": "Bash", "description": "Execute shell command"},
    {"name": "Edit", "description": "Edit file"},
    {"name": "Read", "description": "Read file"},
    {"name": "Glob", "description": "Match files"},
    {"name": "Grep", "description": "Search files"},
    {"name": "MultiEdit", "description": "Multi-file edit"},
    {"name": "WebFetch", "description": "Fetch URL"},
    {"name": "TodoWrite", "description": "Write todo list"}
  ]
}
```

工具名采用 PascalCase（与 Claude Code 一致），这是 Anthropic 生态的传统。

## 网关对 MiniMax Code 的特殊支持

### 1. 协议转换（Anthropic 优先）

MiniMax Code 默认 Anthropic Messages：
- 入站是 Anthropic → 出站按目标方言决定（Anthropic 透传；OpenAI 路径走 ParseAnthropic → SerializeOpenAI）

### 2. thinking 字段

MiniMax Code 默认发送 `thinking: {type: "enabled"}`。网关：
- 目标上游是 Anthropic → 透传
- 目标上游是 OpenAI → 走 `reasonnorm` 归一化（DeepSeek/GLM 等支持）
- 目标上游是 MiniMax M3 → ⚠️ 必须转换为 `thinking: {type: "adaptive"}`（2026-09-18 事故）

### 3. metadata.client_type

作为参数级识别信号，已被本次审计的 `extractClientTypeFromParams` 识别。

## 不兼容警告

- ❌ MiniMax Code 的 PascalCase 工具名（Bash / Edit / Read 等）在严格 OpenAI 兼容上游（某些 vLLM 版本）会报错 → 网关自动归一化为 `snake_case`（待实现）
- ❌ MiniMax Code 默认带 `betas` 数组（Beta 特性开关），仅 Anthropic 上游接受
- ✅ 流式响应（Anthropic SSE）完整支持
- ✅ 工具调用完整支持
- ✅ thinking block 完整支持

## 网关审计点

1. **User-Agent 必须含 `MiniMax-Code/` 或 `MiniMax-code/`**：兜底识别路径
2. **系统提示词含 "minimax code" 或 "MiniMax Code"**：语义识别路径
3. **`metadata.client_type == "minimax_code"`**：参数识别路径
4. **`X-Code-Session-Id` header 存在**：参数识别路径
5. 四条路径任一命中即视为 MiniMax Code 客户端
6. 命中后建议在 Prometheus label 中记录 `client_type=minimax-code`
