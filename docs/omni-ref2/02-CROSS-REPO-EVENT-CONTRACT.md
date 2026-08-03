# OmniRoute v2 跨仓事件契约：Gateway → ai-session-manager

> 这份文档定义 Gateway 数据面向 `ai-session-manager` 控制面发布 canonical facts 的最小契约。ASM 只消费和投影，不重算 provider、路由、压缩或计费结论。

## 1. 事件类型

当前 v1 注册事件：

- `session.opened.v1`
- `request.completed.v1`
- `verdict.recorded.v1`
- `session.deleted.v1`
- `task.execution_host_changed.v1`

事件 envelope 的严格字段、签名和响应见 ASM 的 [`docs/omni-ref2/02-EVENT-INGRESS-CONTRACT.md`](../../../ai-session-manager/docs/omni-ref2/02-EVENT-INGRESS-CONTRACT.md)。Gateway 不得自行增加未注册事件或 payload 字段后直接投递。

## 2. Gateway producer 责任

Gateway 必须实现三段式写入：

```text
业务事务
  ├─ 更新 Gateway canonical facts
  └─ 写入 durable outbox（event_id 固定）
          ↓
      dispatcher/transport adapter
          ↓ body-bound HMAC
      POST /internal/v1/events
```

要求：

- outbox 与事实更新同事务提交，不能先发 HTTP 再写事实。
- 重试保持同一个 `event_id`、`aggregate_version` 和 canonical payload。
- dispatcher 在签名之前读取原始 JSON bytes，ASM 校验的 `sha256(raw_body)` 必须对应最终发送的 body。
- transport adapter 负责 timeout、指数退避、`Retry-After`、DLQ 和告警；业务 handler 不直接持有 ASM 网络细节。
- delivery 结果写入 producer metrics：success、duplicate、stale、retryable、terminal，label 使用 event type 和 destination，不使用 tenant ID 或 event ID。

## 3. Payload 约束

`request.completed.v1` 当前 v1 白名单只承载 provider、model、token、latency、status 和 body references。routing/compression metadata 仍是未来 schema 扩展，必须先由 ASM validator 和双方 fixture 批准后才能投递：

```json
{
  "session_id": "11111111-1111-4111-8111-111111111111",
  "turn_no": 3,
  "request_id": "req-1",
  "correlation_id": "corr-1",
  "idempotency_key": "idem-1",
  "provider": "provider-family",
  "model": "model-name",
  "status": "succeeded",
  "token_usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30},
  "latency_ms": 420,
  "body_refs": {
    "prompt_ref": "internal://body/req-1/prompt",
    "response_ref": "internal://body/req-1/response"
  }
}
```

可扩展字段是**未来版本的设计草案**，不是当前 v1 有效 payload，也不能与上面的 v1 示例一起投递。只有先更新 ASM validator、协商 schema version 或新增 event type、再更新双方 fixture 后，才可引入路由/压缩对象：

```json
{
  "routing": {
    "strategy": "balanced",
    "decision_version": "v1",
    "provider_family": "provider-family",
    "explanation_code": "p2c_low_penalty"
  },
  "compression": {
    "mode": "lite",
    "stages": ["whitespace"],
    "input_chars": 1000,
    "output_chars": 800,
    "saved_chars": 200,
    "reason_code": "context_headroom"
  }
}
```

禁止：

- prompt、response、system prompt、tool arguments、attachment body。
- API key、Bearer、cookie、secret、credential token 或高熵 token。
- 以 `tenant_id` 作为 payload 覆盖 signed tenant；envelope tenant 必须与签名 header 相同。
- 把内部 credential ID、上游 URL、绝对文件路径或 SQL 错误放入 explanation。

## 4. 版本和兼容

- 当前事件类型的 `schema_version` 为 `1`，事件类型和版本必须协同校验。
- 非向后兼容变化使用新事件类型或新版本，不静默改变字段含义。
- producer 先部署兼容写法，再部署 ASM consumer；新增可选字段必须由旧 consumer 拒绝前完成版本协商，不能依赖“未知字段会被忽略”。
- ASM 的 inbox/checkpoint/DLQ 以 `event_id` 和 aggregate version 做幂等/排序；producer 不得为重试生成新 event ID。

## 5. Gateway 与 ASM 的能力边界

| 信息 | Gateway 产生 | ASM 行为 |
|---|---|---|
| provider/model | canonical runtime metadata | 投影、筛选、聚合 |
| routing strategy/explanation | decision owner 产生 | 展示，不重算 |
| compression stage/savings | compressor 产生 | 展示和聚合，不压缩 |
| cost | Gateway 计费 owner 产生 | 读取/展示，除非另有正式契约 |
| prompt/response | Gateway 内部受控存储 | 只接收 `body_refs`，不入 ASM event |
| task summary | Gateway/离线 worker 生成 | 版本化存储和展示 |

## 6. 发布前验证

Gateway producer 发布前必须完成：

```bash
# Gateway
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
go test ./...
go vet ./...

# ASM contract and ingress
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/ai-session-manager
go test ./internal/events ./internal/consumer ./internal/plugin ./internal/db
```

跨仓集成至少覆盖：

- 同一 event 重试返回 `duplicate` 且不产生第二条 projection。
- 旧 aggregate version 返回 `stale`，不会倒退状态。
- 签名 body 改一个字节即 `401`/拒绝。
- tenant header 与 envelope 不同即 `403`。
- payload 含 prompt、response、token、credential 前缀或未知字段即拒绝。
- 数据库短暂不可用返回 retryable，并按 `Retry-After` 重试。

## 7. 启用条件

在以下条件全部满足前，ASM handshake 保持 `event_mode: "none"`，Gateway producer 只在 shadow 或测试目标投递：

- durable outbox 与 producer retry 已上线。
- ASM ingress、RLS、inbox/checkpoint/DLQ/replay 已通过负向测试。
- 五个 v1 事件的 projection 和 task migration 有可见结果。
- 当前 v1 不以 route/compression metadata schema 锁定作为启用条件；若后续选择交付该扩展，必须在独立 schema 版本和 redaction tests 就绪后另行启用。
- 监控覆盖投递延迟、retry、DLQ、duplicate、stale 和 tenant mismatch。
