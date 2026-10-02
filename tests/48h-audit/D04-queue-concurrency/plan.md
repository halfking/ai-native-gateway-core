# D04 — 多队列并发与限流

> 域知识库：[docs/audit/playbook/domains/D04-queue-concurrency.md](../../../docs/audit/playbook/domains/D04-queue-concurrency.md)
> R73 改动面：self-check 限流分类、额外压缩调用计费、流式取消与重试边界。
> 状态：R73 续审记录；真实多节点压力未验证。

## 1. 审计要点

- 校验全局/维度队列、credential limiter、取消、重试与额外内部调用是否重复计数。
- 历史 D04 报告覆盖 worker recover/stop、队列终态 CAS 与 F15 额外调用计费；R73 F06 定向限流 race 通过。
- 未覆盖：真实 Redis/多实例调度和生产负载。

## 2. 业务测试

- [x] B-01：`go test ./bg -run 'TestSelfcheckRateLimitAbort|TestSelfcheckRound429Classification' -count=1`

## 3. 数据测试

- [ ] D-01：Redis 分布式 limiter 证据（未连接）

## 4. 压力测试

- [x] S-01：`go test ./ratelimit -race -count=1`

## 5. 安全测试

- [x] SF-01：gateway/provider 429 scope 隔离与保留 header 丢弃回归通过

## 6. 验收门

```bash
go build ./...
go vet ./...
# R78：原门 `./tests/48h-audit/D04-queue-concurrency/...` 匹配 0 个 Go 包——空包模式只打印
# `matched no packages` 警告并退出 0，门在结构上不可能失败。本域的证据本
# 来就在下面的包里（旧门只是没接到它），故门改指真实证据所在包。
队列并发/自检限流分类：证据在 ./bg 的 selfcheck 限流分类与中止测试 + ./ratelimit 的 race 回归。
go test -race -timeout 120s ./bg -run 'TestSelfcheckRateLimitAbort|TestSelfcheckRound429Classification' -count=1
go test -race -timeout 120s ./ratelimit/... -count=1
```

## 7. 与方案文档的对齐

- RFC：docs/...
- R73：`docs/audit/runs/2026-09-28/R73-remediation-plan.md` §F06

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D04-queue-concurrency/）。
知识库入口：docs/audit/playbook/domains/D04-queue-concurrency.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
