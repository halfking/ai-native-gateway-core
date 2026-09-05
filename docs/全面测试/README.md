# 全面测试

## Goal 客户端信号专项

本目录提供 `gw-continue` 与 `gw-handoff` 两种场景的全面测试入口，包括测试方案、测试矩阵、P0 用例、执行顺序和发布门禁。

- [专项测试方案](../05-testing/02-test-plans/TP-CLIENT-GOAL-SIGNALS.md)
- [`gw-continue` 测试用例](../05-testing/03-test-cases/TC-GOAL-CONTINUE.md)
- [`gw-handoff` 测试用例](../05-testing/03-test-cases/TC-GOAL-HANDOFF.md)
- [现行测试矩阵](../05-testing/01-strategy/test-matrix.md)
- [协议与实现说明](../GOAL_CLIENT_SIGNALS.md)

## 两种场景的最小验收标准

### `gw-continue`

- 客户端声明 `X-Gw-Capabilities: continue`；
- Goal 未完成时，stream 尾部或 non-stream trailer 返回一个合法 signal；
- payload 包含版本、session/request correlation、attempt 和预算字段；
- 不触发同一轮 server-side follow-up；
- 并发请求不得超过 `max_auto_continue_count`；
- 未声明 capability 时保持 legacy 回退。

### `gw-handoff`

- 客户端声明 `X-Gw-Capabilities: handoff`；
- 上下文 token 达到阈值且 Goal 未完成时返回一个合法 signal；
- payload 包含 `reason=context_near_limit` 和 threshold；
- 不消耗 continue budget，不自动创建新 session；
- 未达阈值、服务端开关关闭或 mode 不允许时不得输出 handoff；
- 客户端换会话后的 lineage 由独立 E2E 用例验证。

## 执行命令

```bash
go test ./domains/hooks/goal ./domains/hooks/response ./domains/streaming ./settings ./cmd/gateway ./db
go test -race ./domains/hooks/goal ./domains/streaming
go test ./...
```

没有真实 PostgreSQL、mock upstream 或客户端 harness 时，相关集成/E2E 项必须记录为 `UNKNOWN`，不能将单元测试结果替代为 PASS。
