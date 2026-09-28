# D09 — 节点状态自检

> 域知识库：[docs/audit/playbook/domains/D09-node-state-selfcheck.md](../../../docs/audit/playbook/domains/D09-node-state-selfcheck.md)
> R73 改动面：self-check 429 可信作用域、跨周期 cooldown 与 provider 限流边界。
> 状态：R73 F06 定向验证通过；真实多节点状态存储未验证。

## 1. 审计要点

- 所有请求结果应经统一状态模块；区分 gateway shared key admission 与 provider credential 429。
- `self-check` 仅对可信 `shared_key` 建立跨周期记忆；provider 429 只终止当前 credential 当前轮。
- 未覆盖：真实 Redis 状态、跨进程自检和生产探针。

## 2. 业务测试

- [x] B-01：`TestSelfcheckRateLimitAbort` / `TestSelfcheckRound429Classification`

## 3. 数据测试

- [ ] D-01：Redis 跨周期记忆实测（未连接）

## 4. 压力测试

- [x] S-01：`go test ./ratelimit -race -count=1`

## 5. 安全测试

- [x] SF-01：provider/gateway scope 隔离回归

## 6. 验收门

```bash
go build ./...
go vet ./...
# R78：原门 `./tests/48h-audit/D09-node-state-selfcheck/...` 匹配 0 个 Go 包——空包模式只打印
# `matched no packages` 警告并退出 0，门在结构上不可能失败。本域的证据本
# 来就在下面的包里（旧门只是没接到它），故门改指真实证据所在包。
节点状态/自检：证据在 ./bg 的 selfcheck 限流分类与中止测试 + ./ratelimit 的 race 回归。
go test -race -timeout 120s ./bg -run 'TestSelfcheckRateLimitAbort|TestSelfcheckRound429Classification' -count=1
go test -race -timeout 120s ./ratelimit/... -count=1
```

## 7. 与方案文档的对齐

- RFC：docs/...
- R73：`docs/audit/runs/2026-09-28/R73-remediation-plan.md` §F06

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D09-node-state-selfcheck/）。
知识库入口：docs/audit/playbook/domains/D09-node-state-selfcheck.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
