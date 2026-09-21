# TC-GOAL-HANDOFF：`gw-handoff` 测试用例

## 用例元数据

- 优先级：P0
- 覆盖层级：Unit、Contract、Integration、E2E
- 关联实现：`domains/hooks/goal/mode_hook.go`、`domains/streaming/handler.go`
- 关联迁移：`sql/migrations/startup/645_goal_client_signal.sql`

## TC-H01：达到阈值时 stream 返回 `gw-handoff`

**前置条件**

- Goal 已启用，client-driven signal 已启用，mode=`auto`。
- 客户端发送 `X-Gw-Capabilities: handoff`。
- `tokens_used >= handoff_signal_threshold_tokens`。
- Goal 未完成，且 session 仍 active。

**步骤**

1. 通过 mock upstream 返回未完成 stream。
2. 设置 token 使用量达到阈值。
3. 读取响应尾部 SSE。
4. 检查 continue budget 和 legacy follow-up mock。

**预期**

- 恰好一个 `event: gw-handoff` 事件。
- `data` 是合法 JSON，`type=gw_handoff`、`version=1`。
- payload 包含 `reason=context_near_limit`、`handoff_threshold`、session/request correlation 字段。
- `continue_attempt` 不增加。
- 不触发 server-side follow-up。

## TC-H02：non-stream 返回 `gw-handoff` trailer

**步骤**

1. 发送 `stream=false` 的未完成响应，声明 `handoff`。
2. 设置 token 使用量达到阈值。
3. 读取响应 trailer。
4. 解码 `X-Gw-Client-Signal-Payload-B64`。

**预期**

- signal header 为 `gw-handoff`。
- payload 可 base64 解码且 JSON 合法。
- `reason` 与 threshold 正确。
- 不触发 legacy follow-up。

## TC-H03：handoff 未达阈值

1. 声明 `handoff`，但 `tokens_used < threshold`。
2. 保持 Goal 未完成。

预期：不输出 `gw-handoff`；若 mode 允许 continue 且客户端未声明 continue，则不输出 continue，并按兼容策略处理；不得错误触发 handoff。

## TC-H04：handoff 与 continue 的模式矩阵

验证以下组合：

| mode | capability | 达阈值 | 预期 |
|---|---|---:|---|
| `handoff` | handoff | 是 | `gw-handoff` |
| `handoff` | continue | 是 | 无 signal |
| `continue` | handoff | 是 | 无 signal |
| `both` | handoff | 是 | `gw-handoff` |
| `auto` | continue | 否/是 | `gw-continue` |
| `auto` | continue,handoff | 是 | `gw-handoff` 优先 |

## TC-H05：handoff 不自动创建 lineage session

1. 收到 `gw-handoff`。
2. 不模拟客户端换会话，检查数据库。

预期：网关只发送控制信号；不会自动创建新 session，也不会自行填写 `parent_session_key` / `handoff_reason`。客户端创建新 session 后，另行验证 lineage 写入流程。

## TC-H06：租户隔离与重启恢复

1. tenant A/B 使用相同 session ID，分别触发 handoff。
2. 重启 gateway 后重复请求。
3. 检查两租户 signal 配置、threshold 和 `continue_attempt`。

预期：配置与状态按 tenant/session 隔离；handoff 不消耗 continue budget；重启不改变其语义。
