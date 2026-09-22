# ZCode CLI / ZCode Interactive — 客户端格式要求

## 概述

**ZCode** 是一款国内开源/社区版的命令行编程 Agent，定位类似 Claude Code 的"终端编程助手"。它通过 OpenAI 兼容协议接入 LLM 网关，支持 OpenAI Chat Completions 与 Anthropic Messages 两种入站形态。

⚠️ **2026-09-21 审计说明**：ZCode 已在 `internal/clienttype/clienttype.go:11` 的 `Normalize` 白名单中登记，在 `telemetry/request_metadata.go:50` 的 `defaultAgentPatterns` 中也有完整 pattern 支持。本次审计补齐客户端格式文档并补强**参数级**识别。

## 入站格式

### 协议选择

ZCode 启动时会探测网关支持的协议：

- 默认 OpenAI Chat Completions（`/v1/chat/completions`）
- 探测到 `anthropic-messages` 时切换到 Anthropic Messages 协议

### HTTP 形态

```
POST {base_url}/v1/chat/completions HTTP/1.1
Host: gateway.example.com
Authorization: Bearer any-string
User-Agent: ZCode/{version} ({platform}; {arch})
Content-Type: application/json
```

`User-Agent` 例子：

```
ZCode/1.2.3 (darwin; arm64)
ZCode/0.8.0 (linux; x86_64)
ZCode/1.0.0-beta.2 (windows; amd64)
```

### ZCode 特有 Header

| Header | 说明 |
|--------|------|
| `User-Agent: ZCode/...` | **主识别信号** |
| `X-Gw-Client-Type: zcode` | 显式覆盖（可选） |
| `X-Session-Id` | ZCode 会话 ID |

无 `anthropic-version` / `anthropic-beta` 等 Anthropic 专属 header（除非协议切换到 Anthropic）。

## 系统提示词特征

ZCode 的首条 system message 包含以下自报身份：

```
You are ZCode, an interactive coding agent.
You are an agent for ZCode CLI.
...
```

模式变体（实际抓取）：
- `"You are ZCode, an interactive coding agent"`（主形态）
- `"ZCode CLI"`（子代理场景）
- `"ZCode-Interactive"`（交互式会话模式）

注册表 patterns：

```go
{"zcode", []string{"you are zcode", "zcode cli", "zcode-interactive"}}
```

## 特征参数

ZCode 在请求 body 中携带以下特征参数（用于参数级识别）：

| 参数 | 说明 | 唯一性 |
|------|------|--------|
| `model` | 形如 `zcode-default` / `claude-sonnet-4.6` / `gpt-4o` | 不唯一 |
| `metadata.session_id` | ZCode 会话 ID | **强信号**（UUID4 形态） |
| `metadata.zcode_version` | 客户端版本 | **强信号** |
| `metadata.workspace_root` | 用户工作目录哈希 | **强信号** |
| `tools[].function.name` | 命名空间 `zcode_*`（如 `zcode_bash`, `zcode_edit`） | **强信号** |
| `parallel_tool_calls` | `true`（ZCode 强制并发工具调用） | 弱信号 |

## 工具命名空间

ZCode 自带的内置工具全部以 `zcode_` 前缀：

```json
{
  "tools": [
    {"type": "function", "function": {"name": "zcode_bash", "description": "Execute a shell command"}},
    {"type": "function", "function": {"name": "zcode_edit", "description": "Edit a file"}},
    {"type": "function", "function": {"name": "zcode_read", "description": "Read a file"}},
    {"type": "function", "function": {"name": "zcode_glob", "description": "List files matching glob"}},
    {"type": "function", "function": {"name": "zcode_grep", "description": "Search file contents"}}
  ]
}
```

⚠️ **审计发现**：ZCode 工具命名不符合 OpenAI 命名规范（`^[a-zA-Z0-9_-]{1,64}$`）—— 包含下划线是合法的，但部分上游（vLLM 早期版本）会因 `_` 报错。当前网关透传 OK。

## 网关对 ZCode 的特殊支持

### 1. 协议转换

ZCode → 任意上游：与 OpenAI Chat 路径相同。

### 2. Tools 重命名

ZCode 工具名 `zcode_bash` → Anthropic 序列化为 `zcode_bash`（保持原名）；→ Gemini 序列化为 `zcode_bash`（函数名）。

### 3. metadata 字段

`metadata.zcode_version` 和 `metadata.workspace_root` 是 ZCode 私有：
- 当前通过 Extensions 透传
- 可选升级为 IR 字段（类似 `mask_sensitive_info` 的处理路径）

## 不兼容警告

- ❌ ZCode 的工具命名空间 `zcode_*` 不被 Anthropic SDK 解析，但 Anthropic API 接受任意 function name
- ❌ 部分 OpenAI 兼容上游（vLLM < 0.4）会因 `_` 在 function name 中报错
- ✅ 流式响应（OpenAI SSE）完整支持
- ✅ `parallel_tool_calls: true` 完整支持

## 网关审计点

1. **User-Agent 必须含 `zcode/` 或 `zcode-`**：兜底识别路径
2. **系统提示词含 "you are zcode"**：语义识别路径
3. **tools 含 `zcode_*` 前缀**：参数识别路径
4. **metadata.zcode_version 字段**：参数识别路径
5. 三条路径任一命中即视为 ZCode 客户端
