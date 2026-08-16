# 2026-08-16 Durable Queue Retry Boundaries

## 做了什么

为既有 `durable_llm_tasks` 队列补齐长请求的持久化重试边界。任务以 `attempt_count` 记录执行次数，初次执行后最多允许 100 次重试；没有上游恢复时间时，重试采用 2 秒指数退避并按配置上限收敛。

## 改动清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `domains/streaming/durable_recovery_worker.go` | modify | 执行账本、重试上限和退避计算 |
| `domains/streaming/durable_recovery_worker_test.go` | modify | 覆盖首次、指数、上游封顶和终态边界 |
| `domains/streaming/durable_scenario_test.go` | modify | 使用新的 retry base 选项 |
| `durable/store_claim.go` | modify | 仅领取已到达 `next_retry_at` 的任务 |
| `durable/store_claim_test.go` | modify | 固定严格到期领取契约 |
| `cmd/gateway/main.go` | modify | 将现有 survival 配置传给 durable worker |
| `CHANGELOG.md` | modify | 登记本次队列语义 |

## 为什么这样做

近期生产日志的主要可恢复失败包括 `rate_limit_exceeded` 1192 次、`db_empty` 1049 次、`compaction: no candidate` 262 次、session mirror write timeout 173 次，以及 node timeout 配置类型不匹配 1458 次。使用现有 durable 队列可以在 worker 重启或客户端传输断开后保留调度状态，而不是在请求上下文内休眠。

`attempt_count=1` 表示初次执行；`attempt_count=101` 表示初次执行加 100 次重试，若仍失败则提交 failed 终态。`next_retry_at` 是唯一调度时间权威；worker 轮询只能造成正常的延后执行，不会提前领取任务。

## 验证结果

- `go test ./domains/streaming ./durable ./config`：通过。
- `go vet ./domains/streaming ./durable ./config ./cmd/gateway`：通过。
- `go build ./cmd/gateway`：通过。
- 回归覆盖：首次延迟 2 秒、第四次执行延迟 16 秒、上游 10 分钟建议封顶为 2 分钟、101 次执行后终态失败、claim SQL 含 5 秒容差。

## 回滚

回滚本次提交即可恢复原有固定重试下限和精确到期领取规则。没有新增表、迁移或配置密钥。

## 遗留与风险

- TCP 传输断开不表示客户端显式取消，durable 任务会继续运行；需要独立的认证取消端点或客户端取消协议才能表达人工终止意图。
- 当前只有 durable-capable 请求进入队列。将全部 SSE 请求改为 durable replay 需要单独设计 OpenAI Chat、Responses 和 Anthropic 的协议安全回放机制。
- 已有 semantic commit gate 继续禁止在向客户端提交语义内容或 tool call 后透明重放。

## 下一步建议

- 在 245 预生产环境部署本分支，执行 durable 请求的重试、终态和取消语义验证，并保存 `llm-gateway-deploy-test` gate 证据。
