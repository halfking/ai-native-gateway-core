# 2026-09-21 zcode / MiniMax Code / DeepSeek Code 客户端格式转换审计

## 审计范围

本次审计针对三个新增的国内编程客户端：

| 客户端 | catalog code | 状态 |
|--------|--------------|------|
| ZCode CLI | `zcode` | 已在 `internal/clienttype` 登记；本次补强参数级识别 |
| MiniMax Code | `minimax-code` | **新增** |
| DeepSeek Code | `deepseek-code` | **新增** |

## 现有格式转换对各客户端的兼容性

### ZCode CLI

| 维度 | 兼容性 | 说明 |
|------|--------|------|
| 入站协议 | ✅ | 默认 OpenAI Chat Completions，可切换 Anthropic |
| 出站协议 | ✅ | 走标准 OpenAI Chat 路径 |
| tools 重命名 | ✅ | `zcode_*` 前缀的工具名符合 OpenAI 命名规范 |
| Extensions 透传 | ✅ | `metadata.zcode_version` 等私有字段走 Extensions |
| 用户标识 | ✅ | 通过 `metadata.user_id` 或 `user` 字段 |

**审计结论**：ZCode CLI 当前能正确转换，**无需代码改动**。

### MiniMax Code

| 维度 | 兼容性 | 说明 |
|------|--------|------|
| 入站协议 | ⚠️ | 默认 Anthropic Messages（与 Claude Code 同源），网关需识别 |
| 出站协议 | ⚠️ | 目标上游 MiniMax M3 时需将 `thinking.type: "enabled"` 转换为 `adaptive`（2026-09-18 事故已修） |
| metadata.client_type | ⚠️ | **新增参数级识别信号**，需新增客户端类型 |
| betas 数组 | ⚠️ | 仅 Anthropic 上游接受，目标 OpenAI/Gemini 时需丢弃 |
| tools PascalCase | ✅ | Anthropic 工具命名规范 |

**审计结论**：MiniMax Code 需要 **新增客户端识别**，格式转换层已具备能力。

### DeepSeek Code

| 维度 | 兼容性 | 说明 |
|------|--------|------|
| 入站协议 | ✅ | 默认 OpenAI Chat Completions |
| 出站协议 | ✅ | 走标准 OpenAI Chat 路径 |
| model 字段 `deepseek-*` | ✅ | 不需要特殊处理，作为普通 model 字段 |
| reasoning_content | ✅ | IR 已有 `InternalResponse.ReasoningContent` |
| 流式 + reasoning_content | ✅ | `StreamChunk.ReasoningDelta` 已支持 |
| tools `snake_case` | ✅ | OpenAI 命名规范 |

**审计结论**：DeepSeek Code 转换层完整，**仅需新增客户端识别**。

## 客户端识别需要新增的能力

### 当前已有能力

1. **HTTP 头检测**（`extractClientType`）：
   - `X-Gw-Client-Type` header
   - User-Agent 模式匹配（`zcode/`, `zcode-` 等）

2. **系统提示词语义识别**（`DetectAgentFromSystemPrompt`）：
   - 17 类 agent 模式
   - 已支持 zcode，缺 minimax-code 和 deepseek-code

3. **客户端类型归一化**（`clienttype.Normalize`）：
   - 已支持 zcode、codex、roocode 等
   - 缺 minimax-code、deepseek-code

### 需要新增的能力

#### 1. 新增 `minimax-code` 和 `deepseek-code` 到 `clienttype.Normalize`

```go
case "minimax-code", "deepseek-code":
    return value
```

#### 2. 新增 User-Agent 模式到 `extractClientType`

```go
// 实际落地（R52 勘误）：裸 "deepseek-" 前缀因会误匹配 deepseek-chat 等模型名被弃用，
// 采用三个产品锚定形式。
case strings.Contains(uaLower, "minimax-code"):
    return "minimax-code"
case strings.Contains(uaLower, "deepseek-code"), strings.Contains(uaLower, "deepseek-ide"),
    strings.Contains(uaLower, "deepseek-cli"):
    return "deepseek-code"
```

⚠️ 上述告警已按 R52 落实：UA 侧不用裸 `deepseek-`；body 参数级识别同样收紧——
`model` 以 `deepseek-` 开头**单独不再判定**，须与 DeepSeek Code 工具集共现
（模型选择 ≠ 客户端身份）。

#### 3. 新增系统提示词 pattern 到 `agent_patterns`

```go
{"minimax-code", []string{
    "you are minimax code",
    "minimax-code cli",
    "minimax code by anthropic",
    "minimax-code interactive",
    "minimax's official cli",
}},
{"deepseek-code", []string{
    "you are deepseek code",
    "deepseek-code cli",
    "deepseek ide coding",
    "deepseek chat coding mode",
    "deepseek-coder",
    "deepseek-v3 coding",
}},
```

#### 4. **新增参数级识别**（核心需求）

当前仅有 header + system prompt 两条路径，缺**请求 body 参数级识别**。新增函数：

```go
// ExtractClientTypeFromBody scans the parsed request body for client-
// specific markers (metadata fields, model name prefixes, tool name
// conventions) and returns the canonical client type.
func ExtractClientTypeFromBody(body []byte) string
```

**识别规则**：

| 客户端 | 参数级识别信号 |
|--------|--------------|
| `zcode` | `metadata.zcode_version` 字段；tools 含 `zcode_*` 前缀 |
| `minimax-code` | `metadata.client_type == "minimax_code"`；`X-Code-Session-Id` header |
| `deepseek-code` | `metadata.deepseek_session_id`（独立强信号）；`model` deepseek-* × 工具集共现（R52 收紧：单一信号不足判，防 SDK 客户端误归类） |

#### 5. 三层 fallback 链路

```
1. X-Gw-Client-Type / X-Agent-Name header → 显式指定（最高优先级）
2. User-Agent 匹配                        → 标准路径
3. 系统提示词语义匹配                     → 兜底（覆盖弱名）
4. 参数级识别（body 字段）                → 仅覆盖弱名（R52 勘误：
   实现中 body-marker 在 fillAttemptMeta 之后跑，不会压过具体 UA 结论）
```

## 实施计划

| 步骤 | 文件 | 工作量 |
|------|------|--------|
| 1 | `internal/clienttype/clienttype.go` | 新增 `minimax-code` / `deepseek-code` |
| 2 | `domains/streaming/client_fingerprint.go` | User-Agent 新模式 |
| 3 | `domains/streaming/executors/executor.go` | 同步 User-Agent 模式（包边界） |
| 4 | `telemetry/request_metadata.go` | 新增系统提示词 pattern |
| 5 | `telemetry/body_client_marker.go`（新文件） | 参数级识别函数 |
| 6 | `telemetry/request_metadata.go` | 接入 `ExtractClientTypeFromBody`（R52 勘误：`ExtractClientNameFromBody` 符号不存在） |
| 7 | 各处测试 | 验证 |
