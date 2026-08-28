# llm-gateway-go 在 ACC 分布式多智能体协作平台中的角色与契约

> 维护方：llm-gateway-go 维护者 + ACC 架构组
> 主参考：[ACC 分布式多智能体协作平台总方案](../../agent-control-center/plans/distributed-multi-agent-orchestration-plan.md)
> 详细集成：[`06-memora-and-llm-gateway.md`](../../agent-control-center/plans/distributed-ma/06-memora-and-llm-gateway.md)

## 1. llm-gateway-go 在本平台中是什么

llm-gateway-go = **统一 LLM 网关**（session 持久化、token 用量、cost 控制、provider 适配）。

ACC 作为分布式多智能体的编排中枢，**所有 LLM 调用必须**经由 llm-gateway-go。ACC 不再自建 LLM 代理 / 用量统计 / 多 provider 适配。

## 2. 我（llm-gateway-go）提供什么

| 能力 | API | 用途 |
| --- | --- | --- |
| Session 创建 | `POST /v1/llm/sessions` | 创建会话 |
| 消息发送 | `POST /v1/llm/sessions/{id}/messages` | 发消息（普通） |
| 消息流式 | `POST /v1/llm/sessions/{id}/messages/stream` | SSE 流式响应（新增） |
| 用量查询 | `GET /v1/llm/sessions/{id}/usage` | token / cost 详情 |
| Session 列表 | `GET /v1/llm/sessions` | 列租户下 session |
| Session 详情 | `GET /v1/llm/sessions/{id}` | session 元信息 |
| 工具调用协议 | function calling 标准化 | 统一 tool 协议（新增） |
| 多模型 fallback | session 内切换 backend | 一个 session 内可切（新增） |
| 预算硬限 | 超出自动断流 | token / cost 超限强制断流（新增） |
| 审计 | audit log | 每次调用都记录 |
| 批量摘要 | 一段时间窗口聚合 | 给 ACC 返回（新增） |
| 回放 | session 可重放 | 给定 session_id 完整消息（新增） |
| Auth | Bearer JWT | 租户级 JWT |

## 3. 我（llm-gateway-go）需要 ACC 提供什么

### 3.1 鉴权

- ACC 颁发 JWT（claims 含 `tenant_id`、`sub`、`scope`、`llm_capability`）。
- llm-gateway-go 验证 `iss == "acc-control-plane"`、`aud == "llm-gateway"`、`exp > now()`。

### 3.2 调用方

- ACC、各智能体（LoopX、ECC、multi-agent-workflow）、companion BFF（间接）。
- 所有调用方都必须经过 ACC Token Bridge。

### 3.3 上下文传递

- 客户端把 plan / evidence 等 memora scope 引用传进来，llm-gateway-go 拉取并注入 system prompt（可选）。
- 不直接让客户端传 system prompt 大字符串，避免审计漏洞。

## 4. 契约边界

### 4.1 已有 API

| Endpoint | Method | 说明 |
| --- | --- | --- |
| `/v1/llm/sessions` | POST | 创建 session |
| `/v1/llm/sessions/{id}/messages` | POST | 发消息 |
| `/v1/llm/sessions/{id}/usage` | GET | 用量 |

### 4.2 新增 API（P0）

| Endpoint | Method | 说明 |
| --- | --- | --- |
| `/v1/llm/sessions/{id}/messages/stream` | POST | 流式响应 |
| `/v1/llm/sessions/{id}/tools` | POST | 工具调用 |
| `/v1/llm/sessions/{id}/replay` | GET | 完整消息回放 |
| `/v1/llm/usage/aggregate` | POST | 批量摘要 |
| `/v1/llm/budget/{tenant}` | GET | 租户预算 |

### 4.3 Schema 约定

```json
// POST /v1/llm/sessions
{
  "tenant_id": "...",
  "agent_id": "...",
  "model_hint": "claude-sonnet-4.5 | gpt-5 | ...",
  "system_prompt": "...",
  "tools": [
    { "name": "...", "schema": { "...": "..." } }
  ],
  "budget": { "max_tokens": 100000, "max_cost_usd": 1.0 }
}

// POST /v1/llm/sessions/{id}/messages
{
  "role": "user | assistant | tool",
  "content": "...",
  "tool_calls": [...],
  "tool_call_id": "..."
}
```

## 5. 共享 Schema 版本

| Schema | 版本 | 维护者 |
| --- | --- | --- |
| `llm_session_v0` | v0 | llm-gateway + ACC |
| `llm_message_v0` | v0 | llm-gateway + ACC |
| `llm_tool_call_v0` | v0 | llm-gateway + ACC |
| `llm_usage_v0` | v0 | llm-gateway + ACC |
| `llm_budget_v0` | v0 | llm-gateway + ACC |

## 6. ACC 这一侧需要的客户端能力

| 需求 | 说明 | 优先级 |
| --- | --- | --- |
| 流式响应 | SSE / WebSocket | P0 |
| 工具调用协议 | function calling 标准化 | P0 |
| 预算控制 | 每次调用前查 budget | P0 |
| 批量摘要 | 给 ACC 显示租户总用量 | P1 |
| 回放 | session 重放用于调试 | P1 |

## 7. 实施步骤

1. **MA-0 阶段**（2 周）：
   - 冻结契约 schema。
   - 流式响应 + 工具调用协议落地。

2. **MA-1 阶段**（4 周）：
   - ACC `llm` 客户端升级。
   - 预算硬限 + 审计。

3. **MA-2 阶段**（4 周）：
   - 多模型 fallback。
   - 批量摘要。

4. **持续**：
   - 每月同步 llm-gateway-go 版本。
   - CI 中跑 contract test。

## 8. 风险与回滚

- llm-gateway-go 不可用 → ACC 业务全停。**缓解**：多副本 + 健康检查 + 失败开关（ACC fallback 到只读本地缓存）。
- 升级不兼容 → schema 版本协商 + 双版本并存。
- 失败回滚：保留旧 API path（feature flag）。

## 9. 维护责任

| 项 | llm-gateway-go 负责 | ACC 负责 |
| --- | --- | --- |
| 会话持久化 | ✅ | 不重写 |
| Token / cost | ✅ | 摘要 + 限流策略 |
| 多 provider 适配 | ✅ | 仅配置 model_hint |
| 流式 / 工具调用协议 | ✅ 标准化 | 解析 + 路由 |
| 审计 | ✅ | 不重写 |
| 预算控制 | ✅ 硬限 | 调用前查 |
| 模型路由 | ✅ | 仅配置 |

## 10. 联系方式

- llm-gateway-go 维护者：TBD
- ACC 集成 owner：TBD
- 同步会议：每月一次（飞书群 `分布式多智能体协作`）
