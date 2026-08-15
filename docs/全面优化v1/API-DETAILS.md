# llm-gateway-go v1 API 与事件细化方案

> 状态：契约细化草案。当前主入口 `cmd/gateway` 默认 `:8781`；`cmd/gateway-v2` 默认 `:8782` 是演示入口。durable outbox 已存在，通用 webhook/capabilities 仍是目标能力。

## 1. 当前 API/运行基线

| 能力 | 当前事实 | 状态 | 说明 |
| --- | --- | --- | --- |
| data plane | `cmd/gateway :8781` | CURRENT | 生产主入口 |
| demo plane | `cmd/gateway-v2 :8782` | CURRENT/PARTIAL | 演示并行入口，不替代主入口 |
| health | `/healthz` 等 | CURRENT | 现有探针保留 |
| session/request facts | gateway PG | CURRENT | 会话事实 SSOT |
| outbox | `outbox_events` + writer/dispatcher | CURRENT/PARTIAL | 需冻结 schema 与 consumer 契约 |
| SM delivery | POST SM `/internal/v1/events` | CURRENT/PARTIAL | 需 HMAC/replay/幂等验收 |
| capabilities | `/api/v2/capabilities` | CURRENT | GW-0.2 已实现（cmd/gateway/capabilities.go），响应结构见 §5 |

## 2. 平台调用标准

所有 ACC/RedClaw/Memora/Pocket 发起的 LLM 调用必须携带：

```http
Authorization: Bearer <caller-service-jwt-or-user-jwt>
X-Correlation-ID: <corr>
Traceparent: <trace>
Content-Type: application/json
```

业务 metadata 建议随请求或网关内部上下文传递：

```yaml
tenant_id: derived from JWT
project_id: string optional
task_id: string optional
run_id: string optional
dispatch_id: string optional
agent_id: string optional
agent_session_id: string optional
source_system: acc|redclaw|memora|pocket
client_ref: string optional
```

规则：

- `tenant_id` 以 JWT 为准。
- 若调用方不能提供 `task_id/run_id`，仍必须提供 `source_system` 与 `correlation_id`。
- 所有成本、配额、审计按 gateway 事实记录为权威。

## 3. event envelope

### outbox event 最小字段

```json
{
  "event_id": "evt-123",
  "schema_version": "1.0",
  "event_type": "request.completed.v1",
  "tenant_id": "tenant-123",
  "session_id": "session-123",
  "request_id": "request-123",
  "correlation_id": "corr-123",
  "source_system": "acc|redclaw|memora|pocket",
  "occurred_at": "2026-08-14T00:00:00Z",
  "payload": {}
}
```

### request.completed.v1 payload

```json
{
  "model": "model-name",
  "provider": "provider-name",
  "input_tokens": 100,
  "output_tokens": 200,
  "cost_usd": "0.0012",
  "latency_ms": 812,
  "status": "success|error",
  "error_code": null,
  "task_id": "task-123",
  "run_id": "run-123",
  "dispatch_id": "dispatch-123",
  "agent_id": "agent-123"
}
```

### session.opened.v1 / session.deleted.v1

```json
{
  "session_id": "session-123",
  "user_id": "user-123",
  "project_id": "project-123",
  "source_system": "acc|redclaw|memora|pocket",
  "metadata": {}
}
```

## 4. outbox → SM delivery

Target：`POST {session-manager}/internal/v1/events`

### Headers

```http
Content-Type: application/json
X-Gateway-Event-Signature: hmac-sha256=...
X-Gateway-Event-Timestamp: 2026-08-14T00:00:00Z
X-Gateway-Event-Nonce: random
X-Correlation-ID: corr-123
```

### Body

```json
{
  "events": [ {"event_id": "evt-123", "schema_version": "1.0", "event_type": "request.completed.v1"} ]
}
```

### Delivery semantics

- at-least-once。
- SM 以 `event_id` 幂等。
- dispatcher 维护 `pending/sent/failed/dlq`、`attempts`、`next_retry_at`。
- SM 返回 2xx 即 sent；4xx 非 retryable 进入 failed/dlq；5xx retry。
- replay 必须可按 `event_id`、时间窗、tenant 重放。

## 5. capabilities 目标接口

`GET /api/v2/capabilities`

Response:

```json
{
  "service": "llm-gateway-go",
  "version": "...",
  "status": "native",
  "features": {
    "data_plane": "current",
    "sticky_session": "current",
    "tenant_quota": "current",
    "durable_outbox": "partial",
    "plugin_runtime": "partial",
    "webhook_subscription": "planned"
  },
  "ports": {
    "primary": 8781,
    "gateway_v2_demo": 8782
  }
}
```

## 6. 插件宿主约定

SM 插件当前使用 `http-unix-socket`、`/plugin/healthz`、`/plugin/handshake`。gateway v1 多插件化目标需要：

```yaml
plugin:
  plugin_id: string
  api_contract: gateway-plugin-v1
  protocol: http-unix-socket|http
  health_path: /plugin/healthz
  handshake_path: /plugin/handshake
  capabilities: string[]
  tenant_mode: isolated|shared
  event_mode: pull|push|none
  quota:
    rpm: integer
    max_memory_mb: integer
```

## 7. 审计与成本聚合字段

gateway 写入或发出的每条 request event 必须提供：

- `tenant_id`、`user_id`、`project_id`。
- `source_system`、`task_id`、`run_id`、`dispatch_id`。
- `model/provider/credential_ref/tier`。
- `tokens/cost/latency/status/error_code`。
- `correlation_id/request_id/session_id`。

这些字段供 SM 做平台成本视图：任务 → LLM 请求 → 记忆增长。

## 8. 验收

- ACC/RedClaw/Memora/Pocket 生产 profile 中没有 provider 直连。
- SM 能按 `correlation_id` 查到 gateway request event。
- outbox 重放不产生重复 projection。
- 端口文档与启动日志一致：gateway `:8781`、gateway-v2 demo `:8782`。
