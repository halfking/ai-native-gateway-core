# Goal 客户端信号全面测试方案

> 适用功能：`gw-continue`（同会话继续）与 `gw-handoff`（客户端发起跨会话迁移）
>
> 适用版本：实现客户端 Goal 信号的当前提交
>
> 结果状态遵循 `docs/05-testing/01-strategy/test-matrix.md`：`PASS`、`FAIL`、`UNKNOWN`、`SKIPPED-CONFIG`。

## 1. 测试目标

验证客户端声明能力后，网关能够在 Goal 未完成时：

1. 为支持 `continue` 的客户端返回 `gw-continue`；
2. 为支持 `handoff` 且上下文达到阈值的客户端返回 `gw-handoff`；
3. 不把客户端信号与服务端 legacy follow-up 同时执行；
4. 在并发、重试、进程重启和租户隔离场景下保持预算与状态一致；
5. 对未声明能力的客户端保持既有 `goal_continue` 自调用行为；
6. 在子代理仍 pending 时阻止完成判定。

## 2. 测试范围与环境

| 层级 | 目标 | 环境 | 门禁 |
|---|---|---|---|
| Unit | capability 解析、payload、模式、阈值、pending gate | Go mock/fake store | 必须 PASS |
| Contract | SSE frame、non-stream trailer、JSON schema、版本字段 | `httptest` + fake interceptor | 必须 PASS |
| Integration | Chat stream/non-stream 真实 handler 链路 | mock upstream、隔离 PG/Redis | 发布前必须 PASS |
| Concurrency | 原子 continue claim、重复请求、预算上限 | `go test -race` | 不得出现超发 |
| Migration | 空库、升级库、重复执行、回滚资产 | 隔离 PostgreSQL | MIGRATION-GATE |
| E2E | 客户端消费信号并继续/换会话 | 真实客户端或等价 harness | 无环境时 UNKNOWN |

## 3. 前置配置与 fixture

服务端：

```text
LLM_GATEWAY_GOAL_ENABLED=true
LLM_GATEWAY_GOAL_CLIENT_DRIVEN=true
LLM_GATEWAY_GOAL_CLIENT_SIGNAL_MODE=auto
LLM_GATEWAY_GOAL_HANDOFF_SIGNAL_THRESHOLD=200000
LLM_GATEWAY_GOAL_MAX_AUTO_CONTINUE=3
```

客户端请求头：

```text
X-Gw-Capabilities: continue
X-Gw-Capabilities: handoff
X-Gw-Capabilities: continue,handoff
```

最小未完成响应 fixture：

```json
{"choices":[{"message":{"role":"assistant","content":"仍在处理第 2 步"},"finish_reason":"stop"}]}
```

最小完成响应 fixture：

```json
{"choices":[{"message":{"role":"assistant","content":"任务完成，所有文件都已成功重构。"},"finish_reason":"stop"}]}
```

子代理 fixture：

```text
X-Gw-Sub-Agents: [{"id":"agent-1","status":"running"}]
```

## 4. P0 场景矩阵

| 场景 | 输入 | 预期 | 失败判定 |
|---|---|---|---|
| C-01 continue stream | stream=true，声明 `continue`，Goal 未完成 | 尾部出现一个 `event: gw-continue`；不触发 server follow-up | 缺 event、重复 event、仍发自调用 |
| C-02 continue non-stream | stream=false，声明 `continue`，Goal 未完成 | body 前已声明 trailer，响应结束包含 signal header 与 base64 JSON | header 缺失/非法 JSON/与 event 不一致 |
| H-01 handoff stream | 声明 `handoff`，tokens ≥ threshold，Goal 未完成 | 尾部出现一个 `event: gw-handoff`；不占用 continue budget | 错发 continue、重复 handoff、预算被增加 |
| H-02 handoff non-stream | stream=false，声明 `handoff`，tokens ≥ threshold | trailer signal 为 `gw-handoff`，payload 含 threshold/reason | 仍执行 legacy follow-up 或 trailer 不可解码 |
| F-01 legacy fallback | 不声明 `continue/handoff` | 保持 `goal_continue` 服务端自调用 | 返回自定义 signal 或丢失续跑 |
| G-01 server gate | client 有 capability，但 `goal.client_signal_enabled=false` | 保持 legacy follow-up | 未开启时泄露 signal |
| G-02 mode gate | mode=continue/handoff/both/auto | 仅允许对应 signal 类型 | 模式越权 |
| B-01 budget | 连续请求达到 max | 前 max 次可 claim，后续不发 continue | 超发、并发重复 claim |
| B-02 restart | 进程重启后继续请求 | 从 `continue_attempt` 恢复预算 | 从 0 重置导致超发 |
| S-01 pending agent | completion 文本 + running sub-agent | 不判定完成，记录 `subagent:pending` | 错误标记 completed |
| T-01 tenant isolation | tenant A/B 同 session ID | capability、预算、snapshot 互不影响 | 跨租户读取/计数 |

## 5. 执行顺序与门禁

1. `go test ./domains/hooks/goal ./domains/hooks/response ./domains/streaming ./settings ./cmd/gateway ./db`。
2. `go test -race ./domains/hooks/goal ./domains/streaming`。
3. `go test ./...`。
4. 使用隔离 PG 执行 migration 空库/升级库/重复执行/回滚验证。
5. 使用 mock upstream 执行 C-01、C-02、H-01、H-02、F-01。
6. 真实客户端消费信号的 E2E 无环境时记为 `UNKNOWN`，不可记为 PASS。

发布门禁：所有 P0 Unit/Contract/Integration/Concurrency/Migration 用例必须 PASS；任何能力泄露、预算超发、跨租户污染或 legacy 回退回归均阻断发布。

## 6. 观测与证据

每次执行记录：commit、配置、tenant/session/request ID、capability header、signal event/header、payload version、continue_attempt、tokens_used、context_window、follow-up 是否触发、sub-agent snapshot、migration version。日志不得包含 API key 或完整敏感正文。
