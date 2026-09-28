# D13 — 免费 token 池

> 域知识库：[docs/audit/playbook/domains/D13-free-token-pool.md](../../../docs/audit/playbook/domains/D13-free-token-pool.md)
> R73 改动面：credential self-check、429 可信作用域和 provider candidate 隔离。
> 状态：静态 + 定向验证；免费 token 来源与配额服务未验证。

## 1. 审计要点

- 核对来源合规、自动发现/注册、密钥安全、可用性探测、配额过期和供应商隔离。
- R73 F06 证明 provider credential 429 不建立 worker-wide 跨周期记忆；shared key 仅在可信 scope 下记忆。
- 未覆盖：真实 token 池、配额服务和供应商凭据。

## 2. 业务测试

- [x] B-01：self-check 429 定向回归

## 3. 数据测试

- [ ] D-01：token 来源/配额数据库（未提供）

## 4. 压力测试

- [x] S-01：限流 race 定向通过

## 5. 安全测试

- [x] SF-01：secret self-check 日志仅记录 digest/长度，不读出值

## 6. 验收门

```bash
go build ./...
go vet ./...
# R78：原门 `./tests/48h-audit/D13-free-token-pool/...` 匹配 0 个 Go 包——空包模式只打印
# `matched no packages` 警告并退出 0，门在结构上不可能失败。本域的证据本
# 来就在下面的包里（旧门只是没接到它），故门改指真实证据所在包。
免费 token 池：证据在 ./bg 的 selfcheck 429 定向回归 + ./ratelimit 的 race 回归。
go test -race -timeout 120s ./bg -run 'TestSelfcheckRateLimitAbort|TestSelfcheckRound429Classification' -count=1
go test -race -timeout 120s ./ratelimit/... -count=1
```

## 7. 与方案文档的对齐

- RFC：docs/...
- R73：`bg/credential_selfcheck.go`；`ratelimit/scope.go`

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D13-free-token-pool/）。
知识库入口：docs/audit/playbook/domains/D13-free-token-pool.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
