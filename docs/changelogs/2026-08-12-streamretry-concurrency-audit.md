# StreamRetry Concurrency Audit

## Summary

修复共享 `DefaultStreamExecutor` 下 `Wrapper.metrics` 的并发数据竞争，并为请求级指标提供隔离快照 API。

## Changes

- `Wrapper` 使用 `sync.RWMutex` 保护最近一次完成执行的指标快照。
- 新增 `ExecuteWithMetrics`，返回当前执行的局部指标。
- 新增 `ExecuteStreamWithMetrics`，返回当前 HTTP 请求的局部指标。
- 保留 `Execute` / `ExecuteStream` 的原有签名，避免影响现有调用方。
- 新增并发回归测试，覆盖共享 executor 下不同重试深度的指标隔离。
- README 说明 `Metrics()` 只能表示 latest snapshot，并发请求应使用 per-call API。

## Audit Scope

- 会话队列：复核近期 probe stream lifecycle 两轮修复，未发现新的状态泳道、回环或 task ID 污染。
- URSM v2：复核既有 M3 TOCTOU 修复、Lua atomic manual hold、NodeMirror 分片 LRU 和 generation 不变量，未发现新的 race。
- Session V2 mirror：复核 `IsAutoRequest` 过滤、bounded backlog、`backlogMu` 和 atomic feature flag，未发现新的污染或竞态。
- StreamRetry：修复共享指标快照的并发访问问题。

## Verification

```text
go build ./...
go vet ./internal/streamretry/... ./cmd/gateway/...
go test -race -count=1 ./internal/streamretry/...
```

以上命令均通过；race detector 未报告数据竞争。

## Risks

`StreamRetryEnabled` 仍默认关闭。启用前需要在 staging/245 验证 pre-stream 重试路径的幂等性，特别是计费 ledger 和远端 tool 副作用。
