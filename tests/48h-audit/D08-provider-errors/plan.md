# D08 — 供应商错误处理

> 域知识库：[docs/audit/playbook/domains/D08-provider-errors.md](../../../docs/audit/playbook/domains/D08-provider-errors.md)
> R73 改动面：429 分类、上游错误 body、Ollama overflow 与 failover。
> 状态：历史 D08 结论有效；R73 新增路径已定向验证，真实供应商未验证。

## 1. 审计要点

- 分类必须保留 status/body 证据，provider 429 不得污染 gateway shared-key cooldown；context overflow 只允许一次内部恢复。
- 历史错误闭环与 errorsx SSOT 报告已在位；R73 F06/F04 定向测试通过。
- 未覆盖：真实 provider 错误 envelope 与多供应商 failover。

## 2. 业务测试

- [x] B-01：provider/gateway 429 定向回归

## 3. 数据测试

- [x] D-01：Ollama 4xx body-aware classification 回归

## 4. 压力测试

- [ ] S-01：真实多供应商 fault injection（未验证）

## 5. 安全测试

- [x] SF-01：上游保留 scope header 丢弃、错误响应不回显内部标记

## 6. 验收门

```bash
go build ./...
go vet ./...
go test -race -timeout 60s ./tests/48h-audit/D08-provider-errors/...
```

## 7. 与方案文档的对齐

- RFC：docs/...
- R73：`docs/audit/runs/2026-09-28/R73-remediation-plan.md` §F04/F06

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D08-provider-errors/）。
知识库入口：docs/audit/playbook/domains/D08-provider-errors.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
