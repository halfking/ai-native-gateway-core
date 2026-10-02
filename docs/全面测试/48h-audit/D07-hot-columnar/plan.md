# D07 — 热+分区存储

> 域知识库：[docs/audit/playbook/domains/D07-hot-columnar.md](../../../docs/audit/playbook/domains/D07-hot-columnar.md)  
> 48h 改动面（截至 R56）：<待 fill>  
> 状态：草稿（占位，待 worker 子代理按 TEMPLATE-domain.md 填充）

## 1. 审计要点

- 待 fill 1
- 待 fill 2
- 待 fill 3

## 2. 业务测试

- [ ] B-01：<待填>

## 3. 数据测试

- [ ] D-01：<待填>

## 4. 压力测试

- [ ] S-01：<待填>

## 5. 安全测试

- [ ] SF-01：<待填>

## 6. 验收门

```bash
go build ./...
go vet ./...
go test -race -timeout 60s ./tests/48h-audit/D07-hot-columnar/...
```

## 7. 与方案文档的对齐

- RFC：docs/...
- 上轮挂账：docs/audit/playbook/runs/R55-.../agent-D07.md

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D07-hot-columnar/）。
知识库入口：docs/audit/playbook/domains/D07-hot-columnar.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
