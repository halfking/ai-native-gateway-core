# 2026-08-20 — apigpt 节点配额耗尽自动切换

## 1. 问题

apigpt/apiclaude.cc 节点费用耗尽时，部分响应包含 `insufficient credit`、
`quota exhausted`、`No available accounts` 或中文“节点费用已用完”。这些文本
此前会优先命中并发过载分类，导致同一失效凭据重复重试，最终中断客户端请求。

## 2. 修复

- 扩展 `budgetExceededRe`，覆盖 apigpt 常见的英文、中文配额耗尽响应。
- 从 `concurrentOverloadRe` 移除与配额耗尽冲突的文本模式。
- 保留中文并发错误优先级，避免“并发过大，达到上限”误判为配额耗尽。
- 同步修正普通响应、SSE 错误片段和包装错误三条分类路径。

## 3. 运行时行为

配额耗尽响应现在分类为 `KindQuotaPermanent`。dispatch failover 会跳过同凭据
重试，熔断耗尽节点，并重新规划同模型的其他可用凭据；周期性配额带 reset
提示时仍保留 `KindQuotaPeriodic` 恢复语义。

## 4. 验证

- `go test ./errorsx/ -count=1 -timeout=60s`
- `go test ./domains/dispatch/ ./domains/streaming/executors/... -count=1 -timeout=300s`
- `go test ./... -count=1 -timeout=600s`
- `go build ./...`
- `go vet ./errorsx/...`
