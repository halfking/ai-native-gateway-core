# Pre-Stream Keepalive (2026-06-28)

为缓解“上游重试期间客户端首字等待超时”，网关对 OpenAI chat-completions
流式请求提供可选的 **pre-stream keepalive** 能力：在拿到 upstream
response 之前，先返回 `200 text/event-stream` 并周期性发出 SSE 注释
`: keep-alive\n\n`，让客户端首字计时器持续被刷新。

## 默认行为

- **默认关闭**（`LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE=false`）。
- 仅对 `clientProtocol == "openai-completions"` 且 `stream=true` 的请求生效。
- 拿到真正的 upstream `resp` 后立即停掉 keep-alive，再走原有
  `StreamChat` / 失败 SSE error 路径。
- 一旦 response 已被预热，async 202 fallback 会被禁用；所有失败统一
  落 SSE error envelope（而不是 JSON）。
- keep-alive 周期复用 `LLM_GATEWAY_KEEPALIVE_INTERVAL`（默认 15s）。

## 灰度上线步骤

1. **在 1–2 个非关键租户上开启**（env 形式）：

   ```yaml
   env:
     - name: LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE
       value: "true"
   ```

2. **观察至少 1 个完整重试周期**（约 30 分钟）：

   - 看 `request_logs` 中 `error_kind=first_byte_timeout` 是否显著下降。
   - 看 `request_logs` 中 `error_kind=client_cancel` 是否有异常上扬
     （说明客户端可能因为收到 200 后立刻出现异常 JSON 报错而提前断开）。
   - 看上游 `/healthz` 中长尾请求是否仍然走完。

3. **无异常后扩展到全量**：

   - 配置文件同步增加：

     ```yaml
     enable_pre_stream_keepalive: true
     ```

   - env 仍可临时覆盖，用于紧急回退。

## 紧急回退

设置环境变量即可，无需重新打包：

```bash
export LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE=false
```

或者把 deployment 中该项删除。回退后行为等价于 2026-06-28 之前的
实现：流式请求在拿到 upstream `resp` 之前不会写任何 header / body。

## 与既有 timeout 配置的关系

| 配置 | 行为 |
|------|------|
| `LLM_GATEWAY_FIRST_BYTE_TIMEOUT` | 拿到 upstream `resp` 后第一行的等待超时 |
| `LLM_GATEWAY_STREAM_TIMEOUT` | 流式连接整体最长存活时间 |
| `LLM_GATEWAY_KEEPALIVE_INTERVAL` | 注释间隔（也用于 prewarm） |
| `LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE` | 启用/禁用 prewarm 预热 |

四个值互相独立。prewarm 仅在“等待 upstream 响应”这一段起作用，不会
改变拿到 `resp` 之后的行为。

## 失败语义

预热成功后，所有失败路径都必须通过 SSE envelope 报出：

```
data: {"error":{"message":"...","type":"server_error","code":"provider_error"}}
```

不会回退到 JSON / 4xx HTTP 状态码，因为 200 已经被预热写入。

## 测试覆盖

`domains/streaming/pre_stream_keepalive_test.go` 覆盖：

- `TestStartPreStreamKeepalive_WritesInitialComment` — 预热写出 comment
- `TestPreStreamKeepalive_DisabledByDefault` — 默认关闭
- `TestPreStreamKeepalive_EnabledViaEnv` — env 打开
- `TestPreStreamKeepalive_InitialCommentArrivesBeforeContent` — comment 在
  content 之前（顺序不变量）
- `TestPreStreamKeepalive_StopIsIdempotent` — `stop()` 幂等
- `TestPreStreamKeepalive_PrewarmThenSSEError` — 失败时 SSE error，且
  comment 仍在 error 之前

`domains/streaming/executors/executor_prestream_test.go` 覆盖：

- `TestShouldAsyncFallback_DisabledWhenPreStreamPrepared` — prewarm 后
  禁用 async 202 fallback
