# 客户端/Agent 请求格式文档目录

本目录覆盖网关**入站**侧（即客户端/Agent 发往网关的请求）所支持的协议与格式要求。

网关对外提供 OpenAI 兼容的 API 网关服务，主要承接三类客户端：

1. **OpenAI 标准客户端**：直接使用 OpenAI SDK 调用的客户端
2. **Anthropic 标准客户端**：使用 Anthropic SDK 的客户端
3. **第三方 Agent / IDE 插件**：通过 OpenAI 兼容模式接入（Claude Code 除外，它走 Anthropic 协议）

## 文件清单

| 文件 | 内容 |
|------|------|
| [openai-sdk.md](openai-sdk.md) | OpenAI 官方 SDK（Python/Node）请求格式要求 |
| [anthropic-sdk.md](anthropic-sdk.md) | Anthropic 官方 SDK 请求格式要求 |
| [claude-code.md](claude-code.md) | Claude Code CLI / Agent 请求格式要求 |
| [cursor.md](cursor.md) | Cursor IDE 内置 Agent 请求格式要求 |
| [openai-agents-sdk.md](openai-agents-sdk.md) | OpenAI Agents SDK 请求格式要求 |
| [langchain.md](langchain.md) | LangChain / LangGraph 请求格式要求 |
| [litellm.md](litellm.md) | LiteLLM 代理请求格式要求 |
| [curl-rest.md](curl-rest.md) | curl / REST 直连请求格式要求 |
| [zcode.md](zcode.md) | ZCode CLI / ZCode Interactive 编程 Agent（OpenAI 兼容） |
| [minimax-code.md](minimax-code.md) | MiniMax Code 编程 Agent（Anthropic 优先，三层识别链路） |
| [deepseek-code.md](deepseek-code.md) | DeepSeek 编程客户端（OpenAI Chat + reasoning_content） |
| [compatibility-matrix.md](compatibility-matrix.md) | 客户端 × 厂商格式兼容矩阵 |

## 入站协议总览

| 入站协议 | 路径 | 主要客户端 |
|----------|------|------------|
| OpenAI Chat Completions | `POST /v1/chat/completions` | OpenAI SDK、Cursor、Continue、AutoGen、CrewAI、LangChain OpenAIChat、Agent Zero、ZCode CLI、DeepSeek Code |
| OpenAI Responses | `POST /v1/responses` | OpenAI Agents SDK（gpt-5） |
| Anthropic Messages | `POST /v1/messages` | Anthropic SDK、Claude Code、MiniMax Code |

## 客户端到网关到原厂的三跳路径

```
客户端 → 网关入站（OpenAI/Anthropic/Responses）
           ↓ ParseXxx → IR
           ↓ URSM v2 路由决策
           ↓ SerializeYyy + paramguard
       厂商出站（厂商原生协议）
           ↓ 厂商响应
           ↓ ParseYyyResponse → IR
           ↓ SerializeXxxResponse
       客户端出站（客户端期望的格式）
```

## 入站/出站不对称场景

| 场景 | 入站协议 | 出站协议 |
|------|----------|----------|
| Claude Code 通过 Anthropic SDK 访问 Anthropic 模型 | Anthropic | Anthropic |
| Claude Code 通过 Anthropic SDK 访问 DeepSeek | Anthropic | OpenAI Chat |
| OpenAI SDK 客户端访问 OpenAI 模型 | OpenAI Chat | OpenAI Chat |
| OpenAI SDK 客户端访问 Anthropic 模型 | OpenAI Chat | Anthropic Messages |
| Cursor 访问任意模型 | OpenAI Chat | 厂商原生 |
| OpenAI Agents SDK 访问 gpt-5 | OpenAI Responses | OpenAI Responses |
| OpenAI Agents SDK 访问 Claude | OpenAI Responses | Anthropic Messages |
| ZCode CLI 访问任意模型 | OpenAI Chat | 厂商原生 |
| MiniMax Code 访问 Anthropic 模型 | Anthropic | Anthropic |
| MiniMax Code 访问 OpenAI / DeepSeek / GLM | Anthropic | OpenAI Chat |
| MiniMax Code 访问 MiniMax M2/M3 | Anthropic | MiniMax 原生（`thinking: adaptive`） |
| DeepSeek Code 访问 OpenAI 兼容模型 | OpenAI Chat | OpenAI Chat |
| DeepSeek Code 访问 Anthropic 模型 | OpenAI Chat | Anthropic Messages（reasoning → thinking） |
| DeepSeek Code 访问 DeepSeek-R1（reasoner） | OpenAI Chat | OpenAI Chat（透传 `reasoning_content`） |

这种不对称是网关的核心价值：客户端不需要知道后端是什么模型。

## 错误传播

网关需把厂商的错误转换为客户端期望的格式：

- 客户端是 OpenAI → 厂商错误转 `{"error": {"message", "type", "code"}}`
- 客户端是 Anthropic → 厂商错误转 `{"type": "error", "error": {"type", "message"}}`
- HTTP 状态码按严重程度映射（4xx 客户端错误、5xx 上游错误）

## 客户端识别链路（2026-09-21 新增）

国内三家编程客户端（ZCode CLI、MiniMax Code、DeepSeek Code）的识别走
**三层 fallback**，与海外客户端复用同一份 Prometheus label 池：

```
1. X-Gw-Client-Type header           → 显式指定（最高优先级）
2. User-Agent 匹配                   → 标准路径
3. 参数级识别（body 字段）           → 新增（telemetry/body_client_marker.go）
4. 系统提示词语义匹配                → 兜底（telemetry/agent_patterns）
```

参数级识别信号（详见各客户端文档 §"特征参数"）：

| 客户端 | 参数级识别信号 |
|--------|--------------|
| `zcode` | `metadata.zcode_version` 字段；tools 含 `zcode_*` 前缀 |
| `minimax-code` | `metadata.client_type == "minimax_code"`；`X-Code-Session-Id` header |
| `deepseek-code` | `model` 以 `deepseek-` 开头；`metadata.deepseek_session_id`；tools 含 `read_file`/`execute_command` 等 |

完整审计见 [2026-09-21-zcode-minimax-deepseek-conversion-audit.md](2026-09-21-zcode-minimax-deepseek-conversion-audit.md)。
