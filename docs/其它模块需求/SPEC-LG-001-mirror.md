# ACC 集成规格 (llm-gateway-go 视角) — SPEC-LG-001-MIRROR

> 维护方：llm-gateway-go 维护者
> ACC 主规格：[`../../../official-deploy/services/agent-control-center/docs/其它模块需求/SPEC-LG-001-llm-gateway-go.md`](../../../official-deploy/services/agent-control-center/docs/其它模块需求/SPEC-LG-001-llm-gateway-go.md)

本文档是 ACC SPEC-LG-001 在 llm-gateway-go 视角的镜像。

---

## 1. llm-gateway-go 需要实现的内容

### 1.1 新增 / 扩展 HTTP API

> ACC SPEC §2.1 详细定义。

| Endpoint | 优先级 | 实现位置（建议） | 备注 |
| --- | --- | --- | --- |
| `POST /v1/llm/sessions/{id}/messages/stream` | P0 | `internal/handlers/messages_stream.go` | SSE 流式 |
| `POST /v1/llm/sessions/{id}/tools` | P0 | `internal/handlers/tools.go` | function calling |
| `GET /v1/llm/sessions/{id}/replay` | P1 | `internal/handlers/replay.go` | 消息回放 |
| `POST /v1/llm/usage/aggregate` | P1 | `internal/handlers/usage_aggregate.go` | 批量摘要 |
| `GET /v1/llm/budget/{tenant}` | P0 | `internal/handlers/budget.go` | 租户预算 |

### 1.2 Schema 扩展

| 字段 | 优先级 | 实现位置 | 备注 |
| --- | --- | --- | --- |
| `schema_version` | P0 | `internal/types/session.go` | llm_session_v0 / llm_message_v0 |
| `metadata.acc_*` | P1 | 同上 | run_id / profile_id / loopx_goal_id |
| `budget` 硬限 | P0 | `internal/budget/enforcer.go` | max_tokens / max_cost / max_duration / max_tool_calls |

### 1.3 流式 SSE 实现

| 组件 | 实现位置 | 备注 |
| --- | --- | --- |
| SSE writer | `internal/sse/writer.go` | 标准 text/event-stream |
| 流式解析 | `internal/sse/parser.go` | 处理 message.delta / message.tool_call / message.stop |
| 重连支持 | `internal/sse/last_event_id.go` | 客户端传 Last-Event-ID 时跳过已发 |

### 1.4 Function Calling 标准化

| 组件 | 实现位置 | 备注 |
| --- | --- | --- |
| Tool registry | `internal/tools/registry.go` | http:// local:// memora:// 路由 |
| Tool schema | `internal/tools/schema.go` | JSON Schema 校验 |
| Tool timeout | `internal/tools/timeout.go` | 默认 60s |

### 1.5 预算硬限

| 组件 | 实现位置 | 备注 |
| --- | --- | --- |
| Token 计数 | `internal/budget/token_counter.go` | 实时累计 |
| Cost 估算 | `internal/budget/cost_estimator.go` | 按 model + token 数 |
| 硬限执行 | `internal/budget/enforcer.go` | 超限立即断流，返回 429 + reason |

---

## 2. 镜像任务清单（与 ACC SPEC §7 对齐）

### Phase MA-0：契约冻结（2 周）

| TaskId | 任务 | 验收 |
| --- | --- | --- |
| `[MA-0-LG-01]` | 流式响应 + 工具调用协议 | OpenAPI 通过 `redocly lint` |
| `[MA-0-LG-02]` | 预算硬限实现 + audit | 单测覆盖每种预算字段 |

### Phase MA-1：聚合与回放（4 周）

| TaskId | 任务 | 验收 |
| --- | --- | --- |
| `[MA-1-LG-01]` | 批量摘要 API | 端到端通过 |
| `[MA-1-LG-02]` | 回放 API | 重放一致性 100% |

### Phase MA-2：多模型 Fallback（4 周）

| TaskId | 任务 | 验收 |
| --- | --- | --- |
| `[MA-2-LG-01]` | session 内 backend 切换 | 单测 + 集成测试 |
| `[MA-2-LG-02]` | 上游 provider 故障自动 fallback | chaos test 通过 |

### Phase MA-3：性能（3 周）

| TaskId | 任务 | 验收 |
| --- | --- | --- |
| `[MA-3-LG-01]` | 压力测试 | 5000 并发，p99 < 1s |

---

## 3. 验证剧本（llm-gateway-go 侧）

### 3.1 单元验证

```bash
cd ~/workspace/llm-gateway-go-2  # 或 services/llm-gateway-go
go test -race -count=1 ./...
```

### 3.2 预算硬限验证（关键）

```bash
go test -v -run TestBudgetHardLimit ./internal/budget/...
```

### 3.3 流式 SSE 验证

```bash
go test -v -run TestStreaming ./internal/sse/...
```

### 3.4 Contract test

```bash
docker run -d --name llm-gw-mock -p 4103:4103 \
  registry.kxpms.cn/llm-gw/mock:v0.1
go test -tags=contract ./internal/handlers/...
```

---

## 4. 变更同步

- 任何破坏性变更必须先在 ACC 端 [`rfcs/`](../../../official-deploy/services/agent-control-center/plans/distributed-ma/rfcs/) 提交 RFC。

---

## 5. 联系方式

- llm-gateway-go 维护者：TBD
- ACC 集成 owner：TBD
- 同步会议：每月一次（飞书群 `分布式多智能体协作`）
