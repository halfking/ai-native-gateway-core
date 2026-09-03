# TC-GOAL-CONTINUE：`gw-continue` 测试用例

## 用例元数据

- 优先级：P0
- 覆盖层级：Unit、Contract、Integration、Concurrency、E2E
- 关联实现：`domains/hooks/goal/mode_hook.go`、`domains/streaming/handler.go`
- 关联测试：`domains/hooks/goal/goal_test.go`、`domains/streaming/client_signal_test.go`

## TC-C01：stream 返回 `gw-continue`

**前置条件**

- Goal 已启用，`goal.client_signal_enabled=true`，mode=`auto`。
- 客户端发送 `X-Gw-Capabilities: continue`。
- Goal session 为 active，响应包含未完成内容，`finish_reason=stop`。

**步骤**

1. 通过 mock upstream 返回未完成 stream。
2. 读取完整 SSE 响应。
3. 统计 `event: gw-continue` 帧数。
4. 检查是否调用 server-side follow-up mock。

**预期**

- 恰好一个 `gw-continue` 事件。
- `data` 是合法 JSON，`type=gw_continue`、`version=1`。
- payload 中包含 session、attempt、max_attempts、tokens_used、context_window、sub_agents_pending。
- 不调用 legacy follow-up。

## TC-C02：non-stream 返回 `gw-continue` trailer

**步骤**

1. 发送 `stream=false` 的未完成响应，并声明 `continue`。
2. 读取响应 body 与 trailer。
3. 对 `X-Gw-Client-Signal-Payload-B64` 做 base64 解码和 JSON 校验。

**预期**

- 响应头提前声明 `Trailer: X-Gw-Client-Signal` 与 payload trailer。
- 响应结束后 signal 为 `gw-continue`。
- 解码后的 payload 与 stream contract 相同。
- 不触发 server-side follow-up。

## TC-C03：continue 原子预算与并发

**步骤**

1. 将 `max_auto_continue_count` 设为 3。
2. 并发发起 10 个相同 session 的未完成请求。
3. 读取数据库 `continue_attempt` 和实际 signal 数。

**预期**

- 最多 3 个请求获得 `gw-continue`。
- `continue_attempt` 不超过 3。
- `go test -race` 无数据竞争。
- tenant/session 条件不匹配时不得 claim。

## TC-C04：未声明 capability 的 legacy 回退

**步骤**

1. 发送相同未完成响应，但不带 `X-Gw-Capabilities`。
2. 检查 signal 输出和 follow-up mock。

**预期**

- 不输出 `gw-continue` 或 `gw-handoff`。
- 保持原有 `goal_continue` 服务端自调用。

## TC-C05：客户端能力与服务端开关双重门禁

分别验证：

- 客户端声明 capability，服务端开关关闭；
- 服务端开关开启，客户端不声明 capability；
- mode=`handoff` 时声明 `continue`；
- mode=`continue` 时声明 `handoff`。

预期均不得越权输出不允许的 signal。

## TC-C06：pending sub-agent 阻止误完成

1. 在响应内容中放入完成关键词。
2. 上报一个 `status=running` 的 sub-agent。
3. 执行 completion detector。

预期：`completed=false`，judgement=`subagent:pending`，不得因关键词直接结束 Goal。
